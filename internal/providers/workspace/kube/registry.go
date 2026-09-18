package kube

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"golang.org/x/crypto/bcrypt"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const (
	// registryCacheName is the fixed name of the in-cluster registry used as the
	// devcontainer build cache when a template opts in instead of naming an
	// external one. One instance is shared by every devcontainer template in
	// the lab that opts in.
	registryCacheName = "easylab-registry-cache"
	// registryCacheImage is the standard, minimal open-source OCI registry.
	registryCacheImage = "registry:2"
	registryCachePort  = 5000
	// registryCacheDiskSize is generous enough to hold several devcontainer
	// images' worth of cached layers without needing to be configurable.
	registryCacheDiskSize = "10Gi"

	// registryAuthSecretName is the kubernetes.io/dockerconfigjson Secret holding the
	// registry's generated credentials, with one entry per host it is reached under
	// (the internal Service address and, once exposed, the Ingress host). It is the
	// source of truth for the password, and is used as-is wherever a client needs
	// it: a workspace pod's imagePullSecrets, envbuilder's docker config, and this
	// process's own pull verification.
	registryAuthSecretName = "easylab-registry-cache-auth"
	// registryHtpasswdSecretName holds the bcrypt htpasswd line the registry itself
	// checks credentials against, derived from registryAuthSecretName's password.
	registryHtpasswdSecretName = "easylab-registry-cache-htpasswd"
	registryHtpasswdKey        = "htpasswd"
	registryAuthUsername       = "easylab"
	registryAuthVolumeName     = "auth"
	registryAuthMountPath      = "/auth"
	// annotationHtpasswdSum records, on the registry's pod template, which htpasswd
	// the running pod was started with. The registry reads the file once at startup,
	// so a changed htpasswd only takes effect through the rollout a changed sum
	// triggers — and a registry created before authentication existed has no sum at
	// all, which is what migrates it.
	annotationHtpasswdSum = "easylab.io/htpasswd-sum"
)

// registryCacheLabels intentionally does not reuse Backend.labels: that helper
// stamps easylab.io/lab-id, which is also what ListWorkspaces/GetWorkspace
// select workspaces by — this resource must never carry that label, or it
// would show up in a student's or admin's workspace list.
func registryCacheLabels() map[string]string {
	return map[string]string{
		labelManagedBy: managedByValue,
		labelName:      registryCacheName,
	}
}

// EnsureBuildCache provisions (idempotently) an in-cluster registry used as the
// devcontainer build cache and returns its repo address. The registry has no
// TLS: its Service is plain HTTP — envbuilder is a userspace HTTP client, not the
// kubelet, so DevcontainerSpec.Insecure (rather than node-level trust
// configuration) is what makes this work despite the missing TLS.
//
// It does require authentication. Baking exposes it through an Ingress (see
// EnsureRegistryIngress), and an anonymous registry there would let anyone read
// every baked image — workshop repo included — or push poisoned cache layers
// students then build from. The credentials are generated here and injected by
// this backend wherever its own registry is used (envbuilderDockerConfig,
// createDeployment's imagePullSecrets, registryPullAuthHeader).
func (b *Backend) EnsureBuildCache(ctx context.Context) (string, error) {
	labels := registryCacheLabels()

	if err := b.createRegistryCachePVC(ctx, labels); err != nil {
		return "", err
	}
	htpasswd, err := b.ensureRegistryAuth(ctx)
	if err != nil {
		return "", err
	}
	if err := b.createRegistryCacheDeployment(ctx, labels, htpasswdSum(htpasswd)); err != nil {
		return "", err
	}
	if err := b.createRegistryCacheService(ctx, labels); err != nil {
		return "", err
	}

	return b.registryInternalHost() + "/cache", nil
}

// registryInternalHost is the registry's in-cluster Service address, host:port.
func (b *Backend) registryInternalHost() string {
	return fmt.Sprintf("%s.%s.svc.cluster.local:%d", registryCacheName, b.namespace, registryCachePort)
}

// isOwnRegistryHost reports whether host (as it appears in an image reference,
// port included) is this backend's in-cluster registry — either its internal
// Service address or the Ingress host EnsureRegistryIngress exposes it under, both
// of which start with the registry's name.
func isOwnRegistryHost(host string) bool {
	return strings.HasPrefix(host, registryCacheName+".")
}

// ensureRegistryAuth makes sure the registry's credentials exist, that
// registryAuthSecretName carries an entry for the internal host and every one of
// extraHosts, and that the htpasswd the registry checks against matches its
// password. It returns that htpasswd. Nothing is written when all of it is
// already in place, so it is safe on every call.
func (b *Backend) ensureRegistryAuth(ctx context.Context, extraHosts ...string) (string, error) {
	secrets := b.client.CoreV1().Secrets(b.namespace)

	cfg := dockerConfig{}
	exists := false
	existing, err := secrets.Get(ctx, registryAuthSecretName, metav1.GetOptions{})
	switch {
	case err == nil:
		exists = true
		if raw := existing.Data[corev1.DockerConfigJsonKey]; len(raw) > 0 {
			if err := json.Unmarshal(raw, &cfg); err != nil {
				return "", fmt.Errorf("failed to parse registry auth secret: %w", err)
			}
		}
	case !apierrors.IsNotFound(err):
		return "", fmt.Errorf("failed to read registry auth secret: %w", err)
	}
	if cfg.Auths == nil {
		cfg.Auths = map[string]dockerAuth{}
	}

	internal := b.registryInternalHost()
	cred, ok := cfg.Auths[internal]
	changed := !exists
	if !ok || cred.Username == "" || cred.Password == "" {
		password, err := randomRegistryPassword()
		if err != nil {
			return "", err
		}
		cred = dockerAuth{
			Username: registryAuthUsername,
			Password: password,
			Auth:     base64.StdEncoding.EncodeToString([]byte(registryAuthUsername + ":" + password)),
		}
		// A new password invalidates every entry written with the old one.
		for host := range cfg.Auths {
			cfg.Auths[host] = cred
		}
		changed = true
	}
	for _, host := range append([]string{internal}, extraHosts...) {
		if host = strings.TrimSpace(host); host != "" && cfg.Auths[host] != cred {
			cfg.Auths[host] = cred
			changed = true
		}
	}
	if changed {
		encoded, err := json.Marshal(cfg)
		if err != nil {
			return "", fmt.Errorf("failed to encode registry auth secret: %w", err)
		}
		if err := b.applySecret(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: registryAuthSecretName, Namespace: b.namespace, Labels: registryCacheLabels()},
			Type:       corev1.SecretTypeDockerConfigJson,
			Data:       map[string][]byte{corev1.DockerConfigJsonKey: encoded},
		}); err != nil {
			return "", err
		}
	}

	htpasswd := ""
	htSecret, err := secrets.Get(ctx, registryHtpasswdSecretName, metav1.GetOptions{})
	switch {
	case err == nil:
		htpasswd = string(htSecret.Data[registryHtpasswdKey])
	case !apierrors.IsNotFound(err):
		return "", fmt.Errorf("failed to read registry htpasswd secret: %w", err)
	}
	if htpasswdMatches(htpasswd, cred.Username, cred.Password) {
		return htpasswd, nil
	}
	// MinCost on purpose: the registry runs a bcrypt compare on every single
	// request, and a burst of kubelet pulls and envbuilder cache hits is a lot of
	// requests. Cost only slows brute-forcing a leaked hash, which a 256-bit random
	// password already makes hopeless — a higher cost would buy nothing but CPU.
	hash, err := bcrypt.GenerateFromPassword([]byte(cred.Password), bcrypt.MinCost)
	if err != nil {
		return "", fmt.Errorf("failed to hash registry password: %w", err)
	}
	htpasswd = cred.Username + ":" + string(hash) + "\n"
	if err := b.applySecret(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: registryHtpasswdSecretName, Namespace: b.namespace, Labels: registryCacheLabels()},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{registryHtpasswdKey: []byte(htpasswd)},
	}); err != nil {
		return "", err
	}
	return htpasswd, nil
}

// randomRegistryPassword returns 256 bits of randomness, hex-encoded so it is safe
// in an htpasswd line and a basic-auth header alike.
func randomRegistryPassword() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("failed to generate registry password: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// htpasswdMatches reports whether htpasswd holds a line for username whose bcrypt
// hash verifies against password.
func htpasswdMatches(htpasswd, username, password string) bool {
	for _, line := range strings.Split(htpasswd, "\n") {
		user, hash, ok := strings.Cut(strings.TrimSpace(line), ":")
		if ok && user == username {
			return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
		}
	}
	return false
}

func htpasswdSum(htpasswd string) string {
	sum := sha256.Sum256([]byte(htpasswd))
	return hex.EncodeToString(sum[:])
}

// registryAuths returns the auths entries of registryAuthSecretName, or nil when
// the Secret does not exist yet (a registry never provisioned has nothing to
// authenticate against).
func (b *Backend) registryAuths(ctx context.Context) (map[string]dockerAuth, error) {
	sec, err := b.client.CoreV1().Secrets(b.namespace).Get(ctx, registryAuthSecretName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read registry auth secret: %w", err)
	}
	var cfg dockerConfig
	if err := json.Unmarshal(sec.Data[corev1.DockerConfigJsonKey], &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse registry auth secret: %w", err)
	}
	return cfg.Auths, nil
}

func (b *Backend) createRegistryCachePVC(ctx context.Context, labels map[string]string) error {
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: registryCacheName, Namespace: b.namespace, Labels: labels},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(registryCacheDiskSize)},
			},
		},
	}
	if _, err := b.client.CoreV1().PersistentVolumeClaims(b.namespace).Create(ctx, pvc, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create registry cache PVC: %w", err)
	}
	return nil
}

func (b *Backend) createRegistryCacheDeployment(ctx context.Context, labels map[string]string, htpasswdSum string) error {
	replicas := int32(1)
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: registryCacheName, Namespace: b.namespace, Labels: labels},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			// The data volume is ReadWriteOnce: the default RollingUpdate would surge a
			// second pod that can never attach it (the same deadlock the workspace
			// Deployment avoids), and an htpasswd change does roll this Deployment.
			Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType},
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{labelName: registryCacheName}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels, Annotations: map[string]string{annotationHtpasswdSum: htpasswdSum}},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "registry",
						Image: registryCacheImage,
						// The registry image itself changes rarely enough that reusing
						// whatever a node already has is the right default here too.
						ImagePullPolicy: corev1.PullIfNotPresent,
						Ports:           []corev1.ContainerPort{{ContainerPort: registryCachePort, Name: "http"}},
						Env:             registryAuthEnv(),
						VolumeMounts: []corev1.VolumeMount{
							{Name: "data", MountPath: "/var/lib/registry"},
							registryAuthVolumeMount(),
						},
						ReadinessProbe: &corev1.Probe{
							ProbeHandler:        corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt(registryCachePort)}},
							InitialDelaySeconds: 5,
							PeriodSeconds:       10,
						},
					}},
					Volumes: []corev1.Volume{
						{
							Name: "data",
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: registryCacheName},
							},
						},
						registryAuthVolume(),
					},
				},
			},
		},
	}
	_, err := b.client.AppsV1().Deployments(b.namespace).Create(ctx, dep, metav1.CreateOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create registry cache deployment: %w", err)
	}
	return b.reconcileRegistryCacheDeployment(ctx, htpasswdSum)
}

// registryAuthEnv turns on the registry's built-in htpasswd authentication.
func registryAuthEnv() []corev1.EnvVar {
	return []corev1.EnvVar{
		{Name: "REGISTRY_AUTH", Value: "htpasswd"},
		{Name: "REGISTRY_AUTH_HTPASSWD_REALM", Value: "easylab-registry"},
		{Name: "REGISTRY_AUTH_HTPASSWD_PATH", Value: registryAuthMountPath + "/" + registryHtpasswdKey},
	}
}

func registryAuthVolumeMount() corev1.VolumeMount {
	return corev1.VolumeMount{Name: registryAuthVolumeName, MountPath: registryAuthMountPath, ReadOnly: true}
}

func registryAuthVolume() corev1.Volume {
	return corev1.Volume{
		Name:         registryAuthVolumeName,
		VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: registryHtpasswdSecretName}},
	}
}

// reconcileRegistryCacheDeployment brings an existing registry Deployment in line
// with the htpasswd it should be running with. A registry created before
// authentication existed is the main case — it carries no auth configuration at
// all and would keep serving anonymously forever, since createRegistryCacheDeployment
// is otherwise create-once — but the same check also rolls the registry onto a
// regenerated htpasswd. Only the auth-related fields are touched.
func (b *Backend) reconcileRegistryCacheDeployment(ctx context.Context, htpasswdSum string) error {
	dep, err := b.client.AppsV1().Deployments(b.namespace).Get(ctx, registryCacheName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to look up registry cache deployment: %w", err)
	}
	if dep.Spec.Template.Annotations[annotationHtpasswdSum] == htpasswdSum {
		return nil
	}

	pod := &dep.Spec.Template.Spec
	for i := range pod.Containers {
		c := &pod.Containers[i]
		if c.Name != "registry" {
			continue
		}
		for _, want := range registryAuthEnv() {
			c.Env = upsertEnv(c.Env, want)
		}
		c.VolumeMounts = upsertVolumeMount(c.VolumeMounts, registryAuthVolumeMount())
	}
	pod.Volumes = upsertVolume(pod.Volumes, registryAuthVolume())
	if dep.Spec.Template.Annotations == nil {
		dep.Spec.Template.Annotations = map[string]string{}
	}
	dep.Spec.Template.Annotations[annotationHtpasswdSum] = htpasswdSum
	// See createRegistryCacheDeployment: the rollout this update triggers must not
	// surge a second pod onto the ReadWriteOnce volume.
	dep.Spec.Strategy = appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType}

	if _, err := b.client.AppsV1().Deployments(b.namespace).Update(ctx, dep, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("failed to reconcile registry cache deployment: %w", err)
	}
	log.Printf("Reconciled authentication on registry cache deployment %s", registryCacheName)
	return nil
}

func upsertEnv(env []corev1.EnvVar, want corev1.EnvVar) []corev1.EnvVar {
	for i := range env {
		if env[i].Name == want.Name {
			env[i] = want
			return env
		}
	}
	return append(env, want)
}

func upsertVolumeMount(mounts []corev1.VolumeMount, want corev1.VolumeMount) []corev1.VolumeMount {
	for i := range mounts {
		if mounts[i].Name == want.Name {
			mounts[i] = want
			return mounts
		}
	}
	return append(mounts, want)
}

func upsertVolume(volumes []corev1.Volume, want corev1.Volume) []corev1.Volume {
	for i := range volumes {
		if volumes[i].Name == want.Name {
			volumes[i] = want
			return volumes
		}
	}
	return append(volumes, want)
}

func (b *Backend) createRegistryCacheService(ctx context.Context, labels map[string]string) error {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: registryCacheName, Namespace: b.namespace, Labels: labels},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{labelName: registryCacheName},
			Ports: []corev1.ServicePort{{
				Port:       registryCachePort,
				TargetPort: intstr.FromInt(registryCachePort),
				Protocol:   corev1.ProtocolTCP,
			}},
		},
	}
	if _, err := b.client.CoreV1().Services(b.namespace).Create(ctx, svc, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create registry cache service: %w", err)
	}
	return nil
}

// EnsureRegistryIngress provisions (idempotently) an Ingress exposing the in-cluster
// registry under domain, using the same TLS material a workspace Ingress would
// (wildcardTLSSecret when set, else a per-host cert-manager certificate via
// clusterIssuer), and returns the external host. This is what makes a baked image
// pullable by the kubelet: envbuilder can push over the registry's internal plain-HTTP
// ClusterIP address, but an ordinary image pull needs a trusted registry, which this
// Ingress+TLS is what provides.
//
// This does not reuse createIngress: that helper hardcodes backend port 80 (matching
// every workspace Service, which always listens on 80), but the registry Service
// listens on registryCachePort — reusing it as-is would silently misroute.
func (b *Backend) EnsureRegistryIngress(ctx context.Context, domain, wildcardTLSSecret, clusterIssuer string) (string, error) {
	host := workspaceHost(registryCacheName, domain)
	if host == "" {
		return "", fmt.Errorf("cannot expose the registry without a domain")
	}
	// The kubelet and this process's pull verification reach the registry under
	// host, so the credentials must carry an entry for it — a dockerconfigjson auth
	// is matched on the exact registry host.
	if _, err := b.ensureRegistryAuth(ctx, host); err != nil {
		return "", err
	}

	pathType := netv1.PathTypePrefix
	ingressClass := workspaceIngressClass
	annotations := map[string]string{}

	tlsSecret := wildcardTLSSecret
	if tlsSecret == "" && clusterIssuer != "" {
		tlsSecret = registryCacheName + "-tls"
		annotations[clusterIssuerAnnotation] = clusterIssuer
	}

	ing := &netv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: registryCacheName, Namespace: b.namespace, Labels: registryCacheLabels(), Annotations: annotations},
		Spec: netv1.IngressSpec{
			IngressClassName: &ingressClass,
			Rules: []netv1.IngressRule{{
				Host: host,
				IngressRuleValue: netv1.IngressRuleValue{
					HTTP: &netv1.HTTPIngressRuleValue{
						Paths: []netv1.HTTPIngressPath{{
							Path:     "/",
							PathType: &pathType,
							Backend: netv1.IngressBackend{
								Service: &netv1.IngressServiceBackend{
									Name: registryCacheName,
									Port: netv1.ServiceBackendPort{Number: registryCachePort},
								},
							},
						}},
					},
				},
			}},
		},
	}
	if tlsSecret != "" {
		ing.Spec.TLS = []netv1.IngressTLS{{Hosts: []string{host}, SecretName: tlsSecret}}
	}
	_, err := b.client.NetworkingV1().Ingresses(b.namespace).Create(ctx, ing, metav1.CreateOptions{})
	if err == nil {
		return host, nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return "", fmt.Errorf("failed to create registry ingress: %w", err)
	}
	// An existing Ingress is the case this function was named for, and until now it
	// was the case it did not handle: a registry exposed before the lab had a
	// wildcard certificate keeps a TLS config nothing issues, the kubelet refuses to
	// pull over it, and every bake ends in "image was built, but never became
	// pullable". Every field below is re-derived from the caller's current arguments,
	// so reconciling them is what actually makes this idempotent.
	if err := b.reconcileRegistryIngress(ctx, host, tlsSecret, clusterIssuer, ingressClass); err != nil {
		return "", err
	}
	return host, nil
}

// reconcileRegistryIngress aligns an existing registry Ingress' routing and
// certificate configuration with the values EnsureRegistryIngress just computed.
func (b *Backend) reconcileRegistryIngress(ctx context.Context, host, tlsSecret, clusterIssuer, ingressClass string) error {
	existing, err := b.client.NetworkingV1().Ingresses(b.namespace).Get(ctx, registryCacheName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to look up registry ingress: %w", err)
	}

	var desiredTLS []netv1.IngressTLS
	if tlsSecret != "" {
		desiredTLS = []netv1.IngressTLS{{Hosts: []string{host}, SecretName: tlsSecret}}
	}

	changed := false
	if !equality.Semantic.DeepEqual(existing.Spec.TLS, desiredTLS) {
		existing.Spec.TLS = desiredTLS
		changed = true
	}
	if existing.Annotations[clusterIssuerAnnotation] != clusterIssuer {
		if clusterIssuer == "" {
			delete(existing.Annotations, clusterIssuerAnnotation)
		} else {
			if existing.Annotations == nil {
				existing.Annotations = map[string]string{}
			}
			existing.Annotations[clusterIssuerAnnotation] = clusterIssuer
		}
		changed = true
	}
	if existing.Spec.IngressClassName == nil || *existing.Spec.IngressClassName != ingressClass {
		existing.Spec.IngressClassName = &ingressClass
		changed = true
	}
	if len(existing.Spec.Rules) > 0 && existing.Spec.Rules[0].Host != host {
		existing.Spec.Rules[0].Host = host
		changed = true
	}

	if !changed {
		return nil
	}
	if _, err := b.client.NetworkingV1().Ingresses(b.namespace).Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("failed to reconcile registry ingress: %w", err)
	}
	log.Printf("Reconciled TLS on registry ingress %s (secret %q, issuer %q)", registryCacheName, tlsSecret, clusterIssuer)
	return nil
}

// BakedImageRepo returns the internal (push, used by the bake Job) and external (pull,
// handed to student pods) repository references for a template's baked image, both
// under a fixed :latest tag — a rebuild overwrites it, no rollback/history in v1.
// The externalRepo returned here is only the starting point for the pull side: the
// server resolves it through BakedImageDigest and stores the resulting immutable
// "repo@sha256:..." reference, because :latest plus PullIfNotPresent would otherwise
// let a node keep serving the pre-rebuild image.
// Pure string building: it does not provision anything, and does not require the
// Ingress from EnsureRegistryIngress to already exist (callers ensure that separately
// when the pull path needs it).
func (b *Backend) BakedImageRepo(labID, template, domain string) (internalRepo, externalRepo string) {
	path := fmt.Sprintf("baked/%s/%s", sanitizeDNS(labID), sanitizeDNS(template))
	// internalRepo feeds ENVBUILDER_CACHE_REPO directly and must be a bare
	// repository, no :tag suffix — envbuilder appends its own tag when pushing
	// the whole image; an explicit tag here breaks its destination-tag
	// resolution ("repository can only contain the characters ...", confirmed
	// from a real bake's logs) and the push silently fails while the Job still
	// exits 0 (only the final push step errors, not the build).
	internalRepo = b.registryInternalHost() + "/" + path
	if domain == "" {
		return internalRepo, ""
	}
	// externalRepo is a pull reference (spec.Image / manifest lookups), where an
	// explicit tag is the norm — unlike internalRepo, this one is fine as-is.
	externalRepo = fmt.Sprintf("%s/%s:latest", workspaceHost(registryCacheName, domain), path)
	return internalRepo, externalRepo
}
