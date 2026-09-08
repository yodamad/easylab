package kube

import (
	"context"
	"fmt"
	"log"

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
// TLS or authentication: it is reachable only from inside the cluster, over
// plain HTTP — envbuilder is a userspace HTTP client, not the kubelet, so
// DevcontainerSpec.Insecure (rather than node-level trust configuration) is
// what makes this work despite the missing TLS.
func (b *Backend) EnsureBuildCache(ctx context.Context) (string, error) {
	labels := registryCacheLabels()

	if err := b.createRegistryCachePVC(ctx, labels); err != nil {
		return "", err
	}
	if err := b.createRegistryCacheDeployment(ctx, labels); err != nil {
		return "", err
	}
	if err := b.createRegistryCacheService(ctx, labels); err != nil {
		return "", err
	}

	return fmt.Sprintf("%s.%s.svc.cluster.local:%d/cache", registryCacheName, b.namespace, registryCachePort), nil
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

func (b *Backend) createRegistryCacheDeployment(ctx context.Context, labels map[string]string) error {
	replicas := int32(1)
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: registryCacheName, Namespace: b.namespace, Labels: labels},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{labelName: registryCacheName}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "registry",
						Image: registryCacheImage,
						// The registry image itself changes rarely enough that reusing
						// whatever a node already has is the right default here too.
						ImagePullPolicy: corev1.PullIfNotPresent,
						Ports:           []corev1.ContainerPort{{ContainerPort: registryCachePort, Name: "http"}},
						VolumeMounts:    []corev1.VolumeMount{{Name: "data", MountPath: "/var/lib/registry"}},
						ReadinessProbe: &corev1.Probe{
							ProbeHandler:        corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt(registryCachePort)}},
							InitialDelaySeconds: 5,
							PeriodSeconds:       10,
						},
					}},
					Volumes: []corev1.Volume{{
						Name: "data",
						VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: registryCacheName},
						},
					}},
				},
			},
		},
	}
	if _, err := b.client.AppsV1().Deployments(b.namespace).Create(ctx, dep, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create registry cache deployment: %w", err)
	}
	return nil
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
	internalRepo = fmt.Sprintf("%s.%s.svc.cluster.local:%d/%s", registryCacheName, b.namespace, registryCachePort, path)
	if domain == "" {
		return internalRepo, ""
	}
	// externalRepo is a pull reference (spec.Image / manifest lookups), where an
	// explicit tag is the norm — unlike internalRepo, this one is fine as-is.
	externalRepo = fmt.Sprintf("%s/%s:latest", workspaceHost(registryCacheName, domain), path)
	return internalRepo, externalRepo
}
