package kube

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"

	"easylab/internal/providers/workspace"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"
)

const (
	// portalNamePrefix names everything a lab's student portal is made of: one set
	// per lab, so labs sharing a namespace on a BYO cluster each keep their own.
	portalNamePrefix = "easylab-portal-"
	// portalNameMaxLen leaves room for the "-config"/"-outbox" suffixes within the
	// 63-character limit a Service name (and so the shared base name) is held to.
	portalNameMaxLen = 55

	portalConfigSuffix = "-config"
	portalOutboxSuffix = "-outbox"
	// portalIngressAccessSuffix names the Role/RoleBinding created in the ingress
	// controller's namespace for a lab without a domain (see ensurePortalRBAC).
	portalIngressAccessSuffix = "-ingress"

	// portalHostLabel is the DNS label the portal is served under: "portal.{domain}".
	portalHostLabel = "portal"
	// portalContainerPort is the port the EasyLab server listens on in the image.
	portalContainerPort = 8080
	portalContainerName = "portal"

	// labelPortalLab marks a portal's resources with their lab. It deliberately is
	// not labelLabID: that is what ListWorkspaces selects by, and the portal's own
	// Deployment must never show up in a student's or admin's workspace list.
	labelPortalLab = "easylab.io/portal-lab"

	// annotationPortalHash records what the portal Deployment was built from, for
	// the same reason as annotationPrepullHash: comparing pod templates directly
	// would see the API server's defaults as a change on every reconcile.
	annotationPortalHash = "easylab.io/portal-hash"

	// portalOutboxMaxBytes keeps the outbox well inside a ConfigMap's 1MiB limit.
	// Records queued past it are refused rather than making every later write fail.
	portalOutboxMaxBytes = 900 * 1024

	// EnvPortalNamespace and EnvPortalLabID tell a portal pod which lab it serves
	// and where; EnsurePortal sets both on the Deployment.
	EnvPortalNamespace = "EASYLAB_PORTAL_NAMESPACE"
	EnvPortalLabID     = "EASYLAB_PORTAL_LAB_ID"
	// EnvPortalKubeconfig points NewInCluster at a kubeconfig file instead of the
	// pod's service account, to run a portal outside a cluster during development.
	EnvPortalKubeconfig = "EASYLAB_PORTAL_KUBECONFIG"
)

// NewInCluster builds a kube Backend from the identity of the pod it runs in. It
// is how an in-lab student portal reaches its own cluster: with the namespaced
// permissions EnsurePortal granted it, not a kubeconfig.
func NewInCluster(namespace string) (workspace.Backend, error) {
	var (
		cfg *rest.Config
		err error
	)
	if path := strings.TrimSpace(os.Getenv(EnvPortalKubeconfig)); path != "" {
		cfg, err = clientcmd.BuildConfigFromFlags("", path)
	} else {
		cfg, err = rest.InClusterConfig()
	}
	if err != nil {
		return nil, fmt.Errorf("failed to load in-cluster configuration: %w", err)
	}
	cfg.QPS = clientQPS
	cfg.Burst = clientBurst
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to build kubernetes client: %w", err)
	}
	return newBackend(cs, namespace), nil
}

// portalName returns the base name of a lab's portal resources.
func portalName(labID string) string {
	name := portalNamePrefix + sanitizeDNS(labID)
	if len(name) > portalNameMaxLen {
		name = strings.TrimRight(name[:portalNameMaxLen], "-")
	}
	return name
}

func portalLabels(labID string) map[string]string {
	return map[string]string{
		labelManagedBy: managedByValue,
		labelName:      portalName(labID),
		labelPortalLab: sanitizeDNS(labID),
	}
}

// portalRules is everything a portal may do in its lab's namespace: what serving
// students takes (creating, reading and deleting workspaces) and nothing an admin
// action needs. Kubernetes RBAC cannot scope by label, so on a namespace shared by
// several labs this reaches their workspaces too.
//
// Secrets are read-only and limited by name to exactly what serving a student
// reads: the portal's own config, the credentials the lab's templates reference
// (templateSecrets), and the in-cluster registry's generated credentials. That
// leaves out everything else in the namespace — notably another lab's portal
// config, which holds the secret that lab's sign-in assertions are verified
// with, and the TLS keys. The registry a template may build against is
// provisioned by the admin (see reconcilePortal in internal/server), so the
// portal never has to write a Secret. The outbox is the single ConfigMap it can
// touch.
func portalRules(name string, templateSecrets []string) []rbacv1.PolicyRule {
	secrets := sortedUnique(append([]string{
		name + portalConfigSuffix,
		registryAuthSecretName,
		registryHtpasswdSecretName,
	}, templateSecrets...))
	return []rbacv1.PolicyRule{
		{APIGroups: []string{"apps"}, Resources: []string{"deployments"}, Verbs: []string{"get", "list", "create", "update", "delete"}},
		{APIGroups: []string{""}, Resources: []string{"services", "persistentvolumeclaims"}, Verbs: []string{"get", "create", "delete"}},
		{APIGroups: []string{"networking.k8s.io"}, Resources: []string{"ingresses"}, Verbs: []string{"get", "create", "update", "delete"}},
		{APIGroups: []string{""}, Resources: []string{"secrets"}, ResourceNames: secrets, Verbs: []string{"get"}},
		{APIGroups: []string{""}, Resources: []string{"configmaps"}, ResourceNames: []string{name + portalOutboxSuffix}, Verbs: []string{"get", "update"}},
	}
}

// ensurePortalRBAC gives the portal its own identity and the permissions in
// portalRules. A lab without a domain exposes workspaces through nip.io, which
// takes the ingress controller's address — the one thing the portal reads outside
// its namespace — so that lab also gets a read on that single Service.
func (b *Backend) ensurePortalRBAC(ctx context.Context, labID string, templateSecrets []string, needsIngressIP bool) error {
	name := portalName(labID)
	labels := portalLabels(labID)

	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: b.namespace, Labels: labels}}
	if _, err := b.client.CoreV1().ServiceAccounts(b.namespace).Create(ctx, sa, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create portal service account %s: %w", name, err)
	}

	if err := b.applyRole(ctx, b.namespace, name, labels, portalRules(name, templateSecrets)); err != nil {
		return err
	}
	if err := b.ensureRoleBinding(ctx, b.namespace, name, name, labels); err != nil {
		return err
	}

	ingressRole := name + portalIngressAccessSuffix
	if !needsIngressIP {
		return b.removePortalIngressAccess(ctx, ingressRole)
	}
	rules := []rbacv1.PolicyRule{{
		APIGroups:     []string{""},
		Resources:     []string{"services"},
		ResourceNames: []string{ingressControllerService},
		Verbs:         []string{"get"},
	}}
	if err := b.applyRole(ctx, ingressControllerNamespace, ingressRole, labels, rules); err != nil {
		// No ingress controller where we expect one: the portal then has no nip.io
		// fallback either, exactly like the workspaces it would create.
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	return b.ensureRoleBinding(ctx, ingressControllerNamespace, ingressRole, name, labels)
}

// applyRole creates the Role, or brings an existing one's rules up to date — the
// rules are the part an upgrade can change.
func (b *Backend) applyRole(ctx context.Context, namespace, name string, labels map[string]string, rules []rbacv1.PolicyRule) error {
	roles := b.client.RbacV1().Roles(namespace)
	role := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels}, Rules: rules}
	_, err := roles.Create(ctx, role, metav1.CreateOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create role %s/%s: %w", namespace, name, err)
	}
	existing, err := roles.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to look up role %s/%s: %w", namespace, name, err)
	}
	if equality.Semantic.DeepEqual(existing.Rules, rules) {
		return nil
	}
	existing.Rules = rules
	if _, err := roles.Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("failed to update role %s/%s: %w", namespace, name, err)
	}
	return nil
}

// ensureRoleBinding binds role (in namespace) to the portal's service account,
// which always lives in the backend's own namespace.
func (b *Backend) ensureRoleBinding(ctx context.Context, namespace, role, serviceAccount string, labels map[string]string) error {
	binding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: role, Namespace: namespace, Labels: labels},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: role},
		Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: serviceAccount, Namespace: b.namespace}},
	}
	if _, err := b.client.RbacV1().RoleBindings(namespace).Create(ctx, binding, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create role binding %s/%s: %w", namespace, role, err)
	}
	return nil
}

// removePortalIngressAccess deletes the Role/RoleBinding ensurePortalRBAC creates
// in the ingress controller's namespace. Absent is not an error.
func (b *Backend) removePortalIngressAccess(ctx context.Context, name string) error {
	if err := b.client.RbacV1().RoleBindings(ingressControllerNamespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to delete role binding %s/%s: %w", ingressControllerNamespace, name, err)
	}
	if err := b.client.RbacV1().Roles(ingressControllerNamespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to delete role %s/%s: %w", ingressControllerNamespace, name, err)
	}
	return nil
}

// portalDeployment builds the portal's Deployment: the EasyLab image started in
// student mode. One replica — student sessions live in the process's memory.
func (b *Backend) portalDeployment(labID, image string) *appsv1.Deployment {
	name := portalName(labID)
	labels := portalLabels(labID)
	replicas := int32(1)
	sum := sha1.Sum([]byte(strings.Join([]string{image, labID, b.namespace}, "\x00")))

	probe := func() *corev1.Probe {
		return &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{Path: "/health", Port: intstr.FromInt(portalContainerPort)},
			},
			PeriodSeconds: 10,
		}
	}

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   b.namespace,
			Labels:      labels,
			Annotations: map[string]string{annotationPortalHash: hex.EncodeToString(sum[:])},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{labelName: name}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					ServiceAccountName: name,
					Containers: []corev1.Container{{
						Name:            portalContainerName,
						Image:           image,
						ImagePullPolicy: corev1.PullIfNotPresent,
						Command:         []string{"/app/main", "--mode", "student", "--port", fmt.Sprint(portalContainerPort)},
						Env: []corev1.EnvVar{
							{Name: EnvPortalLabID, Value: labID},
							{Name: EnvPortalNamespace, ValueFrom: &corev1.EnvVarSource{
								FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"},
							}},
						},
						Ports:          []corev1.ContainerPort{{ContainerPort: portalContainerPort}},
						ReadinessProbe: probe(),
						LivenessProbe:  probe(),
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("50m"),
								corev1.ResourceMemory: resource.MustParse("64Mi"),
							},
							Limits: corev1.ResourceList{
								corev1.ResourceMemory: resource.MustParse("256Mi"),
							},
						},
					}},
				},
			},
		},
	}
}

// applyPortalDeployment creates the portal Deployment, or rolls it when what it is
// built from (in practice: the image, after an EasyLab upgrade) changed.
func (b *Backend) applyPortalDeployment(ctx context.Context, desired *appsv1.Deployment) error {
	deployments := b.client.AppsV1().Deployments(b.namespace)
	existing, err := deployments.Get(ctx, desired.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if _, err := deployments.Create(ctx, desired, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create portal deployment %s: %w", desired.Name, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to look up portal deployment %s: %w", desired.Name, err)
	}
	if existing.Annotations[annotationPortalHash] == desired.Annotations[annotationPortalHash] {
		return nil
	}
	if existing.Annotations == nil {
		existing.Annotations = map[string]string{}
	}
	existing.Annotations[annotationPortalHash] = desired.Annotations[annotationPortalHash]
	existing.Spec.Template = desired.Spec.Template
	if _, err := deployments.Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("failed to update portal deployment %s: %w", desired.Name, err)
	}
	return nil
}

// applyPortalIngress exposes the portal under host, creating the Ingress or
// bringing an existing one back in line. Unlike a workspace's, the portal's host
// is re-derived too: nobody has bookmarked a URL the lab's domain no longer serves.
func (b *Backend) applyPortalIngress(ctx context.Context, labID, host, tlsSecret, clusterIssuer string) error {
	name := portalName(labID)
	pathType := netv1.PathTypePrefix
	ingressClass := workspaceIngressClass

	desired := &netv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: b.namespace, Labels: portalLabels(labID), Annotations: map[string]string{}},
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
									Name: name,
									Port: netv1.ServiceBackendPort{Number: 80},
								},
							},
						}},
					},
				},
			}},
		},
	}
	if clusterIssuer != "" {
		desired.Annotations[clusterIssuerAnnotation] = clusterIssuer
	}
	if tlsSecret != "" {
		desired.Spec.TLS = []netv1.IngressTLS{{Hosts: []string{host}, SecretName: tlsSecret}}
	}

	ingresses := b.client.NetworkingV1().Ingresses(b.namespace)
	existing, err := ingresses.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if _, err := ingresses.Create(ctx, desired, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create portal ingress %s: %w", name, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to look up portal ingress %s: %w", name, err)
	}

	changed := false
	if !equality.Semantic.DeepEqual(existing.Spec, desired.Spec) {
		existing.Spec = desired.Spec
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
	if !changed {
		return nil
	}
	if _, err := ingresses.Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("failed to reconcile portal ingress %s: %w", name, err)
	}
	log.Printf("Reconciled portal ingress %s (host %q, secret %q, issuer %q)", name, host, tlsSecret, clusterIssuer)
	return nil
}

// portalHostOwner returns the lab already serving its portal under host in this
// namespace, or "" when the host is free (or is this lab's own). Two labs sharing
// both a namespace and a domain would otherwise answer on the same URL.
func (b *Backend) portalHostOwner(ctx context.Context, labID, host string) (string, error) {
	list, err := b.client.NetworkingV1().Ingresses(b.namespace).List(ctx, metav1.ListOptions{LabelSelector: labelPortalLab})
	if err != nil {
		return "", fmt.Errorf("failed to list portal ingresses: %w", err)
	}
	for _, ing := range list.Items {
		owner := ing.Labels[labelPortalLab]
		if owner == sanitizeDNS(labID) {
			continue
		}
		for _, rule := range ing.Spec.Rules {
			if rule.Host == host {
				return owner, nil
			}
		}
	}
	return "", nil
}

// portalURL is the public URL of a portal Ingress, "" when it has no host.
func portalURL(ing *netv1.Ingress) string {
	if ing == nil || len(ing.Spec.Rules) == 0 || ing.Spec.Rules[0].Host == "" {
		return ""
	}
	scheme := schemeHTTP
	if len(ing.Spec.TLS) > 0 {
		scheme = schemeHTTPS
	}
	return fmt.Sprintf("%s://%s", scheme, ing.Spec.Rules[0].Host)
}

// EnsurePortal deploys (idempotently) a lab's student portal: service account and
// permissions, config Secret, outbox ConfigMap, Deployment, Service and Ingress.
func (b *Backend) EnsurePortal(ctx context.Context, spec workspace.PortalSpec) (string, error) {
	if strings.TrimSpace(spec.LabID) == "" {
		return "", fmt.Errorf("cannot deploy a portal without a lab ID")
	}
	if strings.TrimSpace(spec.Image) == "" {
		return "", fmt.Errorf("cannot deploy a portal without an image")
	}
	name := portalName(spec.LabID)
	labels := portalLabels(spec.LabID)

	// Same routing decision as a workspace: the lab's domain over HTTPS, else
	// nip.io over HTTP with no certificate source at all.
	routed, _ := b.resolveRouting(ctx, workspace.Spec{
		Domain:            spec.Domain,
		WildcardTLSSecret: spec.WildcardTLSSecret,
		ClusterIssuer:     spec.ClusterIssuer,
	})
	host := workspaceHost(portalHostLabel, routed.Domain)
	if host != "" {
		owner, err := b.portalHostOwner(ctx, spec.LabID, host)
		if err != nil {
			return "", err
		}
		if owner != "" {
			return "", fmt.Errorf("portal host %s is already used by lab %s in namespace %s", host, owner, b.namespace)
		}
	}

	if err := b.ensurePortalRBAC(ctx, spec.LabID, spec.SecretNames, strings.TrimSpace(spec.Domain) == ""); err != nil {
		return "", err
	}

	if err := b.applySecret(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name + portalConfigSuffix, Namespace: b.namespace, Labels: labels},
		Type:       corev1.SecretTypeOpaque,
		Data:       spec.Config,
	}); err != nil {
		return "", err
	}

	// Created once and never overwritten: it may already hold records the admin
	// has not drained yet.
	outbox := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name + portalOutboxSuffix, Namespace: b.namespace, Labels: labels}}
	if _, err := b.client.CoreV1().ConfigMaps(b.namespace).Create(ctx, outbox, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return "", fmt.Errorf("failed to create portal outbox %s: %w", outbox.Name, err)
	}

	if err := b.applyPortalDeployment(ctx, b.portalDeployment(spec.LabID, spec.Image)); err != nil {
		return "", err
	}
	if err := b.createService(ctx, name, labels, portalContainerPort); err != nil {
		return "", err
	}

	if host == "" {
		return "", nil
	}
	tlsSecret, clusterIssuer := workspaceTLS(name, routed)
	if err := b.applyPortalIngress(ctx, spec.LabID, host, tlsSecret, clusterIssuer); err != nil {
		return "", err
	}
	scheme := schemeHTTP
	if tlsSecret != "" {
		scheme = schemeHTTPS
	}
	return fmt.Sprintf("%s://%s", scheme, host), nil
}

// SyncPortalConfig replaces a deployed portal's config. Nothing is written when
// it is already current, so it is safe to call on every admin change.
func (b *Backend) SyncPortalConfig(ctx context.Context, labID string, config map[string][]byte) error {
	name := portalName(labID) + portalConfigSuffix
	secrets := b.client.CoreV1().Secrets(b.namespace)
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		existing, err := secrets.Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("failed to look up portal config %s: %w", name, err)
		}
		if portalConfigEqual(existing.Data, config) {
			return nil
		}
		existing.Data = config
		_, err = secrets.Update(ctx, existing, metav1.UpdateOptions{})
		return err
	})
}

func portalConfigEqual(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		other, ok := b[k]
		if !ok || !bytes.Equal(v, other) {
			return false
		}
	}
	return true
}

// ReadPortalConfig returns the config the admin last synced for the lab.
func (b *Backend) ReadPortalConfig(ctx context.Context, labID string) (map[string][]byte, error) {
	name := portalName(labID) + portalConfigSuffix
	sec, err := b.client.CoreV1().Secrets(b.namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to read portal config %s: %w", name, err)
	}
	return sec.Data, nil
}

// AppendPortalOutbox queues records for the admin. A full outbox refuses the
// write: the admin is not draining, and growing past a ConfigMap's size limit
// would turn every later append into an error too.
func (b *Backend) AppendPortalOutbox(ctx context.Context, labID string, records map[string]string) error {
	if len(records) == 0 {
		return nil
	}
	name := portalName(labID) + portalOutboxSuffix
	configMaps := b.client.CoreV1().ConfigMaps(b.namespace)
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		cm, err := configMaps.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("failed to read portal outbox %s: %w", name, err)
		}
		size := 0
		for k, v := range cm.Data {
			size += len(k) + len(v)
		}
		if cm.Data == nil {
			cm.Data = make(map[string]string, len(records))
		}
		for id, record := range records {
			size += len(id) + len(record)
			cm.Data[id] = record
		}
		if size > portalOutboxMaxBytes {
			return fmt.Errorf("portal outbox %s is full (%d bytes queued)", name, size)
		}
		_, err = configMaps.Update(ctx, cm, metav1.UpdateOptions{})
		return err
	})
}

// DrainPortalOutbox applies every queued record, oldest ID first, and removes the
// accepted ones. The removal re-reads the outbox, so records the portal queued
// while apply was running are left for the next drain.
func (b *Backend) DrainPortalOutbox(ctx context.Context, labID string, apply func(id, record string) error) error {
	name := portalName(labID) + portalOutboxSuffix
	configMaps := b.client.CoreV1().ConfigMaps(b.namespace)

	cm, err := configMaps.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to read portal outbox %s: %w", name, err)
	}
	if len(cm.Data) == 0 {
		return nil
	}

	ids := make([]string, 0, len(cm.Data))
	for id := range cm.Data {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	var accepted []string
	for _, id := range ids {
		if err := apply(id, cm.Data[id]); err != nil {
			log.Printf("Portal outbox %s: record %s left queued: %v", name, id, err)
			continue
		}
		accepted = append(accepted, id)
	}
	if len(accepted) == 0 {
		return nil
	}

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current, err := configMaps.Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("failed to read portal outbox %s: %w", name, err)
		}
		for _, id := range accepted {
			delete(current.Data, id)
		}
		_, err = configMaps.Update(ctx, current, metav1.UpdateOptions{})
		return err
	})
}

// RemovePortal deletes everything EnsurePortal created for the lab. Absent
// resources are not an error; the first real failure is reported after every
// deletion has been attempted.
func (b *Backend) RemovePortal(ctx context.Context, labID string) error {
	name := portalName(labID)
	var firstErr error
	del := func(kind string, err error) {
		if err != nil && !apierrors.IsNotFound(err) && firstErr == nil {
			firstErr = fmt.Errorf("failed to delete portal %s %s: %w", kind, name, err)
		}
	}
	opts := metav1.DeleteOptions{}
	del("ingress", b.client.NetworkingV1().Ingresses(b.namespace).Delete(ctx, name, opts))
	del("service", b.client.CoreV1().Services(b.namespace).Delete(ctx, name, opts))
	del("deployment", b.client.AppsV1().Deployments(b.namespace).Delete(ctx, name, opts))
	del("role binding", b.client.RbacV1().RoleBindings(b.namespace).Delete(ctx, name, opts))
	del("role", b.client.RbacV1().Roles(b.namespace).Delete(ctx, name, opts))
	del("service account", b.client.CoreV1().ServiceAccounts(b.namespace).Delete(ctx, name, opts))
	del("config", b.client.CoreV1().Secrets(b.namespace).Delete(ctx, name+portalConfigSuffix, opts))
	del("outbox", b.client.CoreV1().ConfigMaps(b.namespace).Delete(ctx, name+portalOutboxSuffix, opts))
	del("ingress access", b.removePortalIngressAccess(ctx, name+portalIngressAccessSuffix))
	return firstErr
}

// PortalStatus reports what is deployed for the lab's portal.
func (b *Backend) PortalStatus(ctx context.Context, labID string) (workspace.PortalStatus, error) {
	name := portalName(labID)
	dep, err := b.client.AppsV1().Deployments(b.namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return workspace.PortalStatus{}, nil
	}
	if err != nil {
		return workspace.PortalStatus{}, fmt.Errorf("failed to look up portal deployment %s: %w", name, err)
	}
	status := workspace.PortalStatus{Deployed: true}
	_, status.Ready = deploymentReadiness(dep)
	if len(dep.Spec.Template.Spec.Containers) > 0 {
		status.Image = dep.Spec.Template.Spec.Containers[0].Image
	}
	ing, err := b.client.NetworkingV1().Ingresses(b.namespace).Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		status.URL = portalURL(ing)
	} else if !apierrors.IsNotFound(err) {
		return status, fmt.Errorf("failed to look up portal ingress %s: %w", name, err)
	}
	return status, nil
}
