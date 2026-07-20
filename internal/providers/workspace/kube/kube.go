// Package kube implements the workspace.Backend interface by provisioning one
// OpenVSCode Server workspace per student directly on a Kubernetes cluster using
// client-go. Each workspace is a Deployment + Service + Ingress (+ optional PVC),
// labeled so it can be listed and cleaned up per lab.
package kube

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	neturl "net/url"
	"regexp"
	"strings"
	"time"

	"easylab/internal/providers/workspace"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

func init() {
	// Register the kube backend so callers can select it via workspace.New.
	_ = workspace.Register(workspace.DefaultBackend, func(kubeconfig, namespace string) (workspace.Backend, error) {
		return New(kubeconfig, namespace)
	})
}

const (
	// DefaultNamespace is where student workspaces are created when the lab does
	// not specify one.
	DefaultNamespace = "workshops"
	// DefaultImage is the OpenVSCode Server image used when a lab does not set one.
	DefaultImage = "gitpod/openvscode-server:latest"

	// workspaceContainerName is the reserved name of the IDE container (sidecars
	// may not reuse it).
	workspaceContainerName = "workspace"

	labelManagedBy  = "app.kubernetes.io/managed-by"
	labelLabID      = "easylab.io/lab-id"
	labelOwner      = "easylab.io/owner"
	labelName       = "app.kubernetes.io/name"
	annotationIDE    = "easylab.io/ide"
	annotationToken  = "easylab.io/token"
	annotationFolder = "easylab.io/folder"
	annotationDomain = "easylab.io/domain"
	annotationScheme = "easylab.io/scheme"

	managedByValue = "easylab"

	// nipDomainSuffix is the wildcard-DNS service used to expose workspaces when a
	// lab has no domain: "{ip}.nip.io" resolves to {ip}, so student workspaces are
	// reachable through the ingress controller without any DNS setup.
	nipDomainSuffix = "nip.io"

	// Well-known ingress-nginx controller location. These mirror the defaults
	// coder.SetupHTTPS installs into; a lab that pre-installs the controller
	// elsewhere simply gets no nip.io fallback (workspace stays in-cluster).
	ingressNginxNamespace = "ingress-nginx"
	ingressNginxService   = "ingress-nginx-controller"

	schemeHTTPS = "https"
	schemeHTTP  = "http"

	// envbuilderImage builds a devcontainer inside the student's own pod: it
	// clones the repo, builds devcontainer.json (image, Dockerfile, features) and
	// then execs the init script in the result.
	envbuilderImage = "ghcr.io/coder/envbuilder:latest"

	// ideMountPath is where the IDE bundle is injected in devcontainer mode. The
	// image envbuilder builds is the workshop's own and contains no IDE, so the
	// bundle is copied onto a volume by an init container and started from here.
	ideMountPath  = "/ide"
	ideVolumeName = "ide"

	// workspaceVolumeName is the PVC holding the student's files.
	workspaceVolumeName = "workspace"
)

var dns1123Invalid = regexp.MustCompile(`[^a-z0-9-]`)

// ideProfile captures the image/port/paths/binaries that differ between IDE bases.
type ideProfile struct {
	kind         string
	defaultImage string
	port         int32
	workspaceDir string // folder the IDE opens / git repo clones into
	serverBin    string // path to the IDE binary (for exec + extension install)

	// bundleRoot is the self-contained IDE install inside defaultImage, written as
	// a shell expression evaluated in that image; bundleBin is the launcher's path
	// relative to it. Devcontainer mode copies bundleRoot onto the /ide volume and
	// starts bundleBin from there. Both launchers resolve their install root from
	// their own path at startup, so a relocated bundle finds its runtime only if
	// the tree is copied whole and these two stay consistent.
	bundleRoot string
	bundleBin  string
}

var ideProfiles = map[string]ideProfile{
	workspace.IDEOpenVSCode: {
		kind:         workspace.IDEOpenVSCode,
		defaultImage: "gitpod/openvscode-server:latest",
		port:         3000,
		workspaceDir: "/home/workspace",
		serverBin:    "${OPENVSCODE_SERVER_ROOT:-/home/.openvscode-server}/bin/openvscode-server",
		bundleRoot:   "${OPENVSCODE_SERVER_ROOT:-/home/.openvscode-server}",
		bundleBin:    "bin/openvscode-server",
	},
	workspace.IDECodeServer: {
		kind:         workspace.IDECodeServer,
		defaultImage: "codercom/code-server:latest",
		port:         8080,
		workspaceDir: "/home/coder/project",
		serverBin:    "code-server",
		// The .deb installs the self-contained bundle here; /usr/bin/code-server is
		// a small wrapper around bin/code-server inside it, which is what serverBin
		// reaches through PATH in plain mode.
		bundleRoot: "/usr/lib/code-server",
		bundleBin:  "bin/code-server",
	},
}

// profileFor returns the IDE profile for a kind, defaulting to openvscode.
func profileFor(kind string) ideProfile {
	if p, ok := ideProfiles[kind]; ok {
		return p
	}
	return ideProfiles[workspace.IDEOpenVSCode]
}

// Backend is a client-go backed workspace.Backend scoped to one cluster + namespace.
type Backend struct {
	client    kubernetes.Interface
	namespace string
}

// New builds a kube Backend from a kubeconfig file's contents.
func New(kubeconfig, namespace string) (workspace.Backend, error) {
	if strings.TrimSpace(kubeconfig) == "" {
		return nil, fmt.Errorf("kubeconfig is empty")
	}
	cfg, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfig))
	if err != nil {
		return nil, fmt.Errorf("failed to parse kubeconfig: %w", err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to build kubernetes client: %w", err)
	}
	return newBackend(cs, namespace), nil
}

// newBackend is the injectable constructor used by tests (with a fake clientset).
func newBackend(client kubernetes.Interface, namespace string) *Backend {
	if namespace == "" {
		namespace = DefaultNamespace
	}
	return &Backend{client: client, namespace: namespace}
}

// workspaceName derives a deterministic, DNS-1123-safe resource name unique per
// (lab, owner, template). The lab+template are folded in via a short hash so a
// student can hold one workspace per template, and the same student in two labs
// does not collide within a shared namespace.
func workspaceName(labID, owner, template string) string {
	owner = sanitizeDNS(owner)
	if owner == "" {
		owner = "student"
	}
	sum := sha1.Sum([]byte(labID + "\x00" + template))
	hash := hex.EncodeToString(sum[:])[:8]
	// Reserve room for "ws-", "-", and the 8-char hash (<=63 total).
	const maxOwner = 63 - len("ws-") - 1 - 8
	if len(owner) > maxOwner {
		owner = owner[:maxOwner]
	}
	return fmt.Sprintf("ws-%s-%s", owner, hash)
}

// sanitizeDNS lowercases and strips characters not allowed in DNS-1123 labels.
func sanitizeDNS(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = dns1123Invalid.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	return s
}

// ingressIP returns the external IP assigned to the ingress-nginx controller
// Service, or "" when the controller is absent or has no IP yet. Only an IP is
// usable: nip.io encodes an address, so a hostname-only LoadBalancer yields "".
func (b *Backend) ingressIP(ctx context.Context) string {
	svc, err := b.client.CoreV1().Services(ingressNginxNamespace).Get(ctx, ingressNginxService, metav1.GetOptions{})
	if err != nil {
		return ""
	}
	for _, ing := range svc.Status.LoadBalancer.Ingress {
		if ing.IP != "" {
			return ing.IP
		}
	}
	return ""
}

// resolveRouting decides how a workspace is exposed and returns the spec to build
// from plus the URL scheme.
//
// A lab with a domain gets "{name}.{domain}" over HTTPS, with certificates issued
// by cert-manager. Without a domain we fall back to nip.io over the ingress-nginx
// LoadBalancer IP, served on plain HTTP: that mode provisions no cert-manager
// ClusterIssuer, so there is nothing to issue a certificate with. When no ingress
// IP is available either, Domain stays empty and the workspace is in-cluster only.
func (b *Backend) resolveRouting(ctx context.Context, spec workspace.Spec) (workspace.Spec, string) {
	if strings.TrimSpace(spec.Domain) != "" {
		return spec, schemeHTTPS
	}
	ip := b.ingressIP(ctx)
	if ip == "" {
		return spec, schemeHTTPS
	}
	spec.Domain = ip + "." + nipDomainSuffix
	spec.ClusterIssuer = ""
	spec.WildcardTLSSecret = ""
	return spec, schemeHTTP
}

// Routing reports the base domain and scheme workspaces are served under, so
// callers can show a lab's public base URL without creating a workspace.
func (b *Backend) Routing(ctx context.Context, labDomain string) (string, string) {
	spec, scheme := b.resolveRouting(ctx, workspace.Spec{Domain: labDomain})
	return spec.Domain, scheme
}

func (b *Backend) labels(labID, owner, name string) map[string]string {
	return map[string]string{
		labelManagedBy: managedByValue,
		labelLabID:     sanitizeDNS(labID),
		labelOwner:     sanitizeDNS(owner),
		labelName:      name,
	}
}

// EnsureWorkspace creates the workspace resources if absent and returns the
// current workspace state. It is idempotent.
func (b *Backend) EnsureWorkspace(ctx context.Context, spec workspace.Spec) (workspace.Workspace, error) {
	name := workspaceName(spec.LabID, spec.Owner, spec.Template)

	existing, err := b.client.AppsV1().Deployments(b.namespace).Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		// Workspace already exists — return its actual access token (a freshly
		// generated spec.Token would not match the running pod, which keeps its
		// original token/password) and the routing recorded at create time, which
		// is the only place a nip.io fallback domain is stored.
		return b.toWorkspace(existing, domainFromDeployment(existing), tokenFromDeployment(existing)), nil
	}
	if !apierrors.IsNotFound(err) {
		return workspace.Workspace{}, fmt.Errorf("failed to look up workspace %s: %w", name, err)
	}

	spec, scheme := b.resolveRouting(ctx, spec)
	if err := b.createResources(ctx, name, spec, scheme); err != nil {
		return workspace.Workspace{}, err
	}

	dep, err := b.client.AppsV1().Deployments(b.namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return workspace.Workspace{}, fmt.Errorf("failed to read created workspace %s: %w", name, err)
	}
	return b.toWorkspace(dep, spec.Domain, spec.Token), nil
}

// GetWorkspace returns the workspace with the given resource name.
func (b *Backend) GetWorkspace(ctx context.Context, name string) (workspace.Workspace, error) {
	dep, err := b.client.AppsV1().Deployments(b.namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return workspace.Workspace{}, fmt.Errorf("workspace not found: %s", name)
		}
		return workspace.Workspace{}, fmt.Errorf("failed to get workspace %s: %w", name, err)
	}
	return b.toWorkspace(dep, domainFromDeployment(dep), tokenFromDeployment(dep)), nil
}

// ListWorkspaces returns all workspaces belonging to a lab.
func (b *Backend) ListWorkspaces(ctx context.Context, labID string) ([]workspace.Workspace, error) {
	selector := fmt.Sprintf("%s=%s,%s=%s", labelManagedBy, managedByValue, labelLabID, sanitizeDNS(labID))
	list, err := b.client.AppsV1().Deployments(b.namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, fmt.Errorf("failed to list workspaces for lab %s: %w", labID, err)
	}
	out := make([]workspace.Workspace, 0, len(list.Items))
	for i := range list.Items {
		dep := &list.Items[i]
		out = append(out, b.toWorkspace(dep, domainFromDeployment(dep), tokenFromDeployment(dep)))
	}
	return out, nil
}

// DeleteWorkspace removes every resource that makes up a workspace.
func (b *Backend) DeleteWorkspace(ctx context.Context, labID, id string) error {
	// id is the resource name shared by every object in the workspace.
	var firstErr error
	del := func(kind string, err error) {
		if err != nil && !apierrors.IsNotFound(err) && firstErr == nil {
			firstErr = fmt.Errorf("failed to delete %s %s: %w", kind, id, err)
		}
	}
	del("ingress", b.client.NetworkingV1().Ingresses(b.namespace).Delete(ctx, id, metav1.DeleteOptions{}))
	del("service", b.client.CoreV1().Services(b.namespace).Delete(ctx, id, metav1.DeleteOptions{}))
	del("deployment", b.client.AppsV1().Deployments(b.namespace).Delete(ctx, id, metav1.DeleteOptions{}))
	del("pvc", b.client.CoreV1().PersistentVolumeClaims(b.namespace).Delete(ctx, id, metav1.DeleteOptions{}))
	return firstErr
}

// Reachable reports whether the cluster API answers.
func (b *Backend) Reachable(ctx context.Context) bool {
	_, err := b.client.Discovery().ServerVersion()
	return err == nil
}

// createResources creates the PVC (optional), Deployment, Service and Ingress.
func (b *Backend) createResources(ctx context.Context, name string, spec workspace.Spec, scheme string) error {
	labels := b.labels(spec.LabID, spec.Owner, name)
	p := profileFor(spec.IDE)
	hasPVC := strings.TrimSpace(spec.DiskSize) != ""

	if hasPVC {
		if err := b.createPVC(ctx, name, labels, spec.DiskSize); err != nil {
			return err
		}
	}
	if err := b.createDeployment(ctx, name, labels, spec, p, hasPVC, scheme); err != nil {
		return err
	}
	if err := b.createService(ctx, name, labels, p.port); err != nil {
		return err
	}
	// Only expose the workspace externally when a domain is configured; otherwise
	// an ingress with an empty host would match every request on the controller.
	if workspaceHost(name, spec.Domain) != "" {
		if err := b.createIngress(ctx, name, labels, spec); err != nil {
			return err
		}
	}
	return nil
}

func (b *Backend) createPVC(ctx context.Context, name string, labels map[string]string, size string) error {
	qty, err := resource.ParseQuantity(size)
	if err != nil {
		return fmt.Errorf("invalid disk size %q: %w", size, err)
	}
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: b.namespace, Labels: labels},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: qty},
			},
		},
	}
	if _, err := b.client.CoreV1().PersistentVolumeClaims(b.namespace).Create(ctx, pvc, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create PVC %s: %w", name, err)
	}
	return nil
}

func (b *Backend) createDeployment(ctx context.Context, name string, labels map[string]string, spec workspace.Spec, p ideProfile, hasPVC bool, scheme string) error {
	image := spec.Image
	if image == "" {
		image = p.defaultImage
	}

	// Both clone modes below reference the secret rather than reading it, so a bad
	// reference would otherwise surface only as a stuck pod.
	if strings.TrimSpace(spec.GitRepo) != "" {
		if err := b.verifyGitAuthSecret(ctx, spec.GitAuthSecret); err != nil {
			return err
		}
	}

	env := []corev1.EnvVar{}
	for k, v := range spec.Env {
		env = append(env, corev1.EnvVar{Name: k, Value: v})
	}
	// code-server authenticates via the PASSWORD env var (openvscode uses a CLI flag).
	if p.kind == workspace.IDECodeServer {
		env = append(env, corev1.EnvVar{Name: "PASSWORD", Value: spec.Token})
	}

	container := corev1.Container{
		Name:      workspaceContainerName,
		Image:     image,
		Ports:     []corev1.ContainerPort{{ContainerPort: p.port, Name: "http"}},
		Env:       env,
		Resources: buildResources(spec.CPU, spec.Memory),
		// Only mark the pod Ready once the IDE is actually listening — a wrapped
		// startup script runs before the server exec, so the port opens late.
		ReadinessProbe: &corev1.Probe{
			ProbeHandler:        corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt(int(p.port))}},
			InitialDelaySeconds: 5,
			PeriodSeconds:       10,
		},
	}

	volumes := []corev1.Volume{}
	initContainers := []corev1.Container{}

	if spec.Devcontainer != nil {
		dockerConfig, err := b.dockerConfigFromSecret(ctx, spec.Devcontainer.RegistryAuthSecret)
		if err != nil {
			return err
		}
		applyDevcontainer(&container, spec, devcontainerProfile(p), dockerConfig)
		initContainers = append(initContainers, ideInjectInit(p))
		volumes = append(volumes, corev1.Volume{
			Name:         ideVolumeName,
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		})
	} else if command, args, wrapped := buildBootstrap(spec, p); wrapped {
		// Setup (startup script / dotfiles / extensions) wraps the IDE start command;
		// otherwise the plain per-IDE args run under the image entrypoint.
		container.Command = command
		container.Args = args
	} else {
		container.Args = args
	}

	if hasPVC {
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{Name: workspaceVolumeName, MountPath: p.workspaceDir})
		volumes = append(volumes, corev1.Volume{
			Name: workspaceVolumeName,
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: name},
			},
		})
		// In devcontainer mode envbuilder does its own clone, so a git-clone init
		// container would only race it for the same directory.
		if strings.TrimSpace(spec.GitRepo) != "" && spec.Devcontainer == nil {
			initContainers = append(initContainers, gitCloneInit(spec.GitRepo, spec.GitBranch, p.workspaceDir, spec.GitAuthSecret))
		}
	}

	// ConfigMap/Secret mounts.
	for i, m := range spec.Mounts {
		vol, mount, ok := buildMount(i, m)
		if !ok {
			continue
		}
		volumes = append(volumes, vol)
		container.VolumeMounts = append(container.VolumeMounts, mount)
	}

	containers := []corev1.Container{container}
	// Sidecar containers (e.g. a database) co-located in the pod.
	for _, sc := range spec.Sidecars {
		if c, ok := buildSidecar(sc); ok {
			containers = append(containers, c)
		}
	}

	replicas := int32(1)
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: b.namespace,
			Labels:    labels,
			Annotations: map[string]string{
				annotationDomain: spec.Domain,
				annotationScheme: scheme,
				annotationIDE:    p.kind,
				annotationToken:  spec.Token,
				annotationFolder: openvscodeFolder(spec, p),
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			// Generous deadline so a long image pull or apt-get startup script does
			// not get marked ProgressDeadlineExceeded (→ failed) prematurely.
			ProgressDeadlineSeconds: int32Ptr(1800),
			Selector:                &metav1.LabelSelector{MatchLabels: map[string]string{labelName: name}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					// Both IDE images run as uid/gid 1000; fsGroup makes freshly
					// provisioned PVCs group-writable so the IDE can write.
					SecurityContext: &corev1.PodSecurityContext{FSGroup: int64Ptr(1000)},
					// A devcontainer build relocates the whole root filesystem, and
					// envbuilder cannot remount the projected service-account token — it
					// dies with "temp remount ... permission denied". The workspace never
					// calls the Kubernetes API, so the token is only dead weight (and
					// something a student with a shell should not be handed); dropping it
					// removes the mount the build trips on. Plain workspaces keep the
					// cluster default.
					AutomountServiceAccountToken: automountServiceAccountToken(spec),
					// Pod-level, so it covers the IDE image, the sidecars and the init
					// containers alike. The devcontainer build pulls from inside the pod
					// with its own credentials and is unaffected by this.
					ImagePullSecrets: imagePullSecrets(spec.ImagePullSecrets),
					InitContainers:   initContainers,
					Containers:       containers,
					Volumes:          volumes,
				},
			},
		},
	}
	if _, err := b.client.AppsV1().Deployments(b.namespace).Create(ctx, dep, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create deployment %s: %w", name, err)
	}
	return nil
}

func int64Ptr(v int64) *int64 { return &v }
func int32Ptr(v int32) *int32 { return &v }

// automountServiceAccountToken opts devcontainer workspaces out of the projected
// service-account token: envbuilder remounts the whole filesystem to build and
// fails on that mount ("temp remount ... permission denied"). Returning nil leaves
// the cluster default for plain workspaces, so their behaviour is unchanged.
func automountServiceAccountToken(spec workspace.Spec) *bool {
	if spec.Devcontainer == nil {
		return nil
	}
	v := false
	return &v
}

// openFolder returns the directory the IDE should open (repo subfolder or the
// workspace root).
func openFolder(spec workspace.Spec, p ideProfile) string {
	if f := strings.Trim(strings.TrimSpace(spec.GitFolder), "/"); f != "" {
		return p.workspaceDir + "/" + f
	}
	return p.workspaceDir
}

// openvscodeFolder returns the subfolder to embed in an openvscode workspace URL
// (?folder=), or "" when not applicable (no subfolder, or code-server, which opens
// its folder via a positional CLI arg instead).
func openvscodeFolder(spec workspace.Spec, p ideProfile) string {
	if p.kind != workspace.IDEOpenVSCode || strings.TrimSpace(spec.GitFolder) == "" {
		return ""
	}
	return openFolder(spec, p)
}

// ideStartArgs returns the args that start the IDE server for a profile. For
// code-server the folder to open is the positional argument; openvscode opens the
// folder via its URL (?folder=) instead, since it has no reliable open-folder flag.
func ideStartArgs(spec workspace.Spec, p ideProfile) []string {
	switch p.kind {
	case workspace.IDECodeServer:
		return []string{"--bind-addr", fmt.Sprintf("0.0.0.0:%d", p.port), "--auth", "password", openFolder(spec, p)}
	default: // openvscode
		return []string{"--host", "0.0.0.0", "--port", fmt.Sprintf("%d", p.port), "--connection-token", spec.Token}
	}
}

// buildBootstrap returns the container command/args. When a startup script,
// dotfiles repo, or extensions are configured it returns a wrapped command that
// runs setup best-effort and then execs the IDE; otherwise it returns the plain
// IDE args (wrapped=false) to run under the image's entrypoint.
func buildBootstrap(spec workspace.Spec, p ideProfile) (command, args []string, wrapped bool) {
	setup := setupSteps(spec, p)
	if setup == "" {
		return nil, ideStartArgs(spec, p), false
	}
	return []string{"/bin/bash", "-lc", setup + ideExecLine(spec, p)}, nil, true
}

// setupSteps returns the best-effort setup lines (startup script, dotfiles,
// extensions) that run before the IDE starts, or "" when there is nothing to do.
// Shared by the plain bootstrap and the devcontainer init script so a workspace
// is provisioned the same way whichever built its image.
func setupSteps(spec workspace.Spec, p ideProfile) string {
	var b strings.Builder
	if s := strings.TrimSpace(spec.StartupScript); s != "" {
		fmt.Fprintf(&b, "{ %s ; } || true\n", s)
	}
	if repo := strings.TrimSpace(spec.DotfilesRepo); repo != "" {
		fmt.Fprintf(&b, "if git clone %s \"$HOME/.dotfiles\"; then for s in install.sh setup.sh bootstrap.sh; do [ -x \"$HOME/.dotfiles/$s\" ] && \"$HOME/.dotfiles/$s\" && break; done; fi\n", shellQuote(repo))
	}
	for _, ext := range spec.Extensions {
		ext = strings.TrimSpace(ext)
		if ext == "" {
			continue
		}
		fmt.Fprintf(&b, "%s --install-extension %s || true\n", p.serverBin, shellQuote(ext))
	}
	return b.String()
}

// ideExecLine returns the line that execs the IDE with its start args, shell-quoted.
func ideExecLine(spec workspace.Spec, p ideProfile) string {
	var b strings.Builder
	fmt.Fprintf(&b, "exec %s", p.serverBin)
	for _, a := range ideStartArgs(spec, p) {
		fmt.Fprintf(&b, " %s", shellQuote(a))
	}
	return b.String()
}

// devcontainerProfile retargets an IDE profile at the injected bundle: in
// devcontainer mode the IDE is not in the image, it sits on the /ide volume.
func devcontainerProfile(p ideProfile) ideProfile {
	p.serverBin = ideMountPath + "/" + p.bundleBin
	return p
}

// applyDevcontainer turns the workspace container into an envbuilder build of the
// repo's devcontainer.json.
//
// envbuilder clones the repo, builds devcontainer.json (image or Dockerfile, plus
// features) into the container's own filesystem, then drops root and execs the
// init script inside the result. Because the build replaces the filesystem,
// anything the IDE needs must arrive on a volume the build is told to leave alone.
func applyDevcontainer(c *corev1.Container, spec workspace.Spec, p ideProfile, dockerConfig string) {
	c.Image = envbuilderImage
	// envbuilder requires root to build; it drops to the devcontainer's user
	// before running the init script, so the IDE itself does not stay root.
	c.SecurityContext = &corev1.SecurityContext{RunAsUser: int64Ptr(0)}
	c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{Name: ideVolumeName, MountPath: ideMountPath})
	// envbuilder is driven entirely by env; its own entrypoint must run.
	c.Command = nil
	c.Args = nil
	c.Env = append(c.Env, devcontainerEnv(spec, p, dockerConfig)...)
}

// devcontainerEnv builds envbuilder's configuration.
func devcontainerEnv(spec workspace.Spec, p ideProfile, dockerConfig string) []corev1.EnvVar {
	dc := spec.Devcontainer

	env := []corev1.EnvVar{
		{Name: "ENVBUILDER_GIT_URL", Value: gitURLWithRef(spec.GitRepo, spec.GitBranch)},
		// Clone where a plain workspace clones, so git_folder and the IDE's open
		// folder mean the same thing in both modes.
		{Name: "ENVBUILDER_WORKSPACE_FOLDER", Value: p.workspaceDir},
		{Name: "ENVBUILDER_INIT_SCRIPT", Value: devcontainerInitScript(spec, p)},
		// What the build must not wipe: the injected IDE, and the student's files.
		{Name: "ENVBUILDER_IGNORE_PATHS", Value: strings.Join([]string{ideMountPath, p.workspaceDir}, ",")},
		// A failed build must fail the pod. Continuing would present a workspace
		// that looks fine but is missing everything the devcontainer installs.
		{Name: "ENVBUILDER_EXIT_ON_BUILD_FAILURE", Value: "true"},
	}

	if v := strings.TrimSpace(dc.Dir); v != "" {
		env = append(env, corev1.EnvVar{Name: "ENVBUILDER_DEVCONTAINER_DIR", Value: v})
	}
	if v := strings.TrimSpace(dc.CacheRepo); v != "" {
		env = append(env,
			corev1.EnvVar{Name: "ENVBUILDER_CACHE_REPO", Value: v},
			// Push the built layers so the next student starts from the cache rather
			// than repeating the build. This is the whole point of requiring a cache
			// repo: the first workspace pays the build, the rest do not.
			corev1.EnvVar{Name: "ENVBUILDER_PUSH_IMAGE", Value: "true"},
		)
	}
	if v := strings.TrimSpace(dc.FallbackImage); v != "" {
		env = append(env, corev1.EnvVar{Name: "ENVBUILDER_FALLBACK_IMAGE", Value: v})
	}
	if dockerConfig != "" {
		env = append(env, corev1.EnvVar{Name: "ENVBUILDER_DOCKER_CONFIG_BASE64", Value: dockerConfig})
	}
	if dc.Insecure {
		env = append(env, corev1.EnvVar{Name: "ENVBUILDER_INSECURE", Value: "true"})
	}
	// Credentials for a private workshop repo, by reference so the token stays out
	// of the Deployment. Safe to hand envbuilder despite the IDE running as its
	// child: envbuilder's run() defers options.UnsetEnv(), which strips both the
	// prefixed and unprefixed names before it execs the init script, so the
	// student's shell does not inherit the workshop author's token.
	//
	// That behaviour is what makes this safe, and envbuilderImage tracks :latest —
	// if the scrub ever goes away, the token would reach every student. The
	// end-to-end check for it is in docs/templates.md.
	if s := strings.TrimSpace(spec.GitAuthSecret); s != "" {
		env = append(env, basicAuthEnv(s, "ENVBUILDER_GIT_USERNAME", "ENVBUILDER_GIT_PASSWORD")...)
	}
	return env
}

// devcontainerInitScript is what envbuilder execs inside the image it just built.
// It applies the same setup a plain workspace gets, then starts the IDE from the
// injected bundle rather than from the image, which has none.
func devcontainerInitScript(spec workspace.Spec, p ideProfile) string {
	return setupSteps(spec, p) + ideExecLine(spec, p)
}

// gitURLWithRef appends the branch the way envbuilder takes it: as a fragment on
// the clone URL rather than a separate option.
func gitURLWithRef(repo, branch string) string {
	repo = strings.TrimSpace(repo)
	if b := strings.TrimSpace(branch); b != "" {
		return repo + "#refs/heads/" + b
	}
	return repo
}

// ideInjectInit copies the IDE bundle onto a shared volume so the built
// devcontainer has an IDE to start.
func ideInjectInit(p ideProfile) corev1.Container {
	// Run as root: the emptyDir mount root at ideMountPath is owned by root, but the
	// IDE image's default user is uid 1000. As uid 1000 both the copy and the chmod
	// fail with EPERM on the mount root itself -- cp -a can't set its timestamps and
	// chmod -R can't change its mode. Root sidesteps that, and a+rX below is what
	// grants the devcontainer's own (possibly non-root) user read/execute access.
	// cp -dR rather than -a: we neither need nor want source ownership/timestamps,
	// and -d keeps the bundle's relative node_modules/.bin symlinks as links, which
	// still resolve once the tree lands at ideMountPath. Dereferencing instead would
	// turn any inert dangling link into a failed init container.
	script := fmt.Sprintf(`cp -dR "%s/." %s/ && chmod -R a+rX %s`,
		p.bundleRoot, ideMountPath, ideMountPath)
	return corev1.Container{
		Name:            "ide-inject",
		Image:           p.defaultImage,
		Command:         []string{"sh", "-c", script},
		SecurityContext: &corev1.SecurityContext{RunAsUser: int64Ptr(0)},
		VolumeMounts:    []corev1.VolumeMount{{Name: ideVolumeName, MountPath: ideMountPath}},
	}
}

// dockerConfigFromSecret reads a dockerconfigjson Secret and base64-encodes it for
// ENVBUILDER_DOCKER_CONFIG_BASE64. Credentials are referenced by Secret name
// rather than carried in the lab config, so they never reach the templates export.
// An empty name means an anonymous cache registry, which is allowed.
func (b *Backend) dockerConfigFromSecret(ctx context.Context, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", nil
	}

	sec, err := b.client.CoreV1().Secrets(b.namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to read registry auth secret %q: %w", name, err)
	}
	for _, key := range []string{corev1.DockerConfigJsonKey, "config.json"} {
		if data := sec.Data[key]; len(data) > 0 {
			return base64.StdEncoding.EncodeToString(data), nil
		}
	}
	return "", fmt.Errorf("registry auth secret %q has no %q key", name, corev1.DockerConfigJsonKey)
}

// verifyGitAuthSecret checks the referenced basic-auth Secret exists and carries
// the keys the clone will read, so a typo fails here — where the admin sees it —
// rather than as a pod stuck in CreateContainerConfigError, which only a cluster
// operator would ever look at.
//
// The values are read but deliberately not returned: they reach git through a
// secretKeyRef resolved by the kubelet, so this process never handles the token.
// An empty name means an anonymous repo, which is allowed.
func (b *Backend) verifyGitAuthSecret(ctx context.Context, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}

	sec, err := b.client.CoreV1().Secrets(b.namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to read git auth secret %q: %w", name, err)
	}
	for _, key := range []string{corev1.BasicAuthUsernameKey, corev1.BasicAuthPasswordKey} {
		if len(sec.Data[key]) == 0 {
			return fmt.Errorf("git auth secret %q has no %q key", name, key)
		}
	}
	return nil
}

// buildMount turns a ConfigMap/Secret mount spec into a Volume + VolumeMount.
func buildMount(i int, m workspace.Mount) (corev1.Volume, corev1.VolumeMount, bool) {
	if strings.TrimSpace(m.Name) == "" || strings.TrimSpace(m.Path) == "" {
		return corev1.Volume{}, corev1.VolumeMount{}, false
	}
	volName := fmt.Sprintf("mount-%d", i)
	vol := corev1.Volume{Name: volName}
	switch strings.ToLower(strings.TrimSpace(m.Type)) {
	case "secret":
		vol.VolumeSource = corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: m.Name}}
	default: // configmap
		vol.VolumeSource = corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: m.Name}}}
	}
	return vol, corev1.VolumeMount{Name: volName, MountPath: m.Path, ReadOnly: true}, true
}

// buildSidecar turns a sidecar spec into a container.
func buildSidecar(sc workspace.Sidecar) (corev1.Container, bool) {
	scName := sanitizeDNS(sc.Name)
	if scName == "" || scName == workspaceContainerName || strings.TrimSpace(sc.Image) == "" {
		return corev1.Container{}, false
	}
	c := corev1.Container{Name: scName, Image: sc.Image}
	for _, port := range sc.Ports {
		c.Ports = append(c.Ports, corev1.ContainerPort{ContainerPort: int32(port)})
	}
	for k, v := range sc.Env {
		c.Env = append(c.Env, corev1.EnvVar{Name: k, Value: v})
	}
	if sc.Privileged || len(sc.Capabilities) > 0 {
		secCtx := &corev1.SecurityContext{}
		if sc.Privileged {
			priv := true
			secCtx.Privileged = &priv
		}
		if len(sc.Capabilities) > 0 {
			caps := make([]corev1.Capability, 0, len(sc.Capabilities))
			for _, name := range sc.Capabilities {
				if name = strings.TrimSpace(name); name != "" {
					caps = append(caps, corev1.Capability(name))
				}
			}
			secCtx.Capabilities = &corev1.Capabilities{Add: caps}
		}
		c.SecurityContext = secCtx
	}
	return c, true
}

// gitCredentialHelper feeds git the credentials from the environment. The token
// reaches git down the helper's stdout rather than through the clone URL, so it
// never lands in the Deployment's container args — which anything with read
// access to the namespace can fetch with `kubectl get deploy -o yaml`.
//
// The ${...} are expanded by the shell git spawns to run the helper, not by the
// shell running the clone script: shellQuote keeps them literal on the way in.
// git passes the operation (get/store/erase) as $1, which f ignores — fine for a
// one-shot clone that only ever needs "get".
const gitCredentialHelper = `!f() { printf '%s\n' "username=${GIT_USERNAME}" "password=${GIT_PASSWORD}"; }; f`

// credential.helper is multi-valued and git asks each configured helper in order,
// taking the first answer. Setting it only appends, so any helper already
// configured in the image or a mounted gitconfig would answer first and silently
// win — handing the clone the wrong credentials. An empty value resets the list,
// so this pair means "ours, and only ours".
var gitCredentialArgs = []string{"-c", "credential.helper=", "-c", "credential.helper=" + shellQuote(gitCredentialHelper)}

// basicAuthEnv references a basic-auth Secret's username/password from a
// container's environment. The kubelet resolves the values when the container
// starts, so the Deployment carries the Secret's name and nothing else.
func basicAuthEnv(secretName, userVar, passVar string) []corev1.EnvVar {
	ref := func(key string) *corev1.EnvVarSource {
		return &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
			Key:                  key,
		}}
	}
	return []corev1.EnvVar{
		{Name: userVar, ValueFrom: ref(corev1.BasicAuthUsernameKey)},
		{Name: passVar, ValueFrom: ref(corev1.BasicAuthPasswordKey)},
	}
}

// imagePullSecrets turns Secret names into the pod-level references the kubelet
// pulls with. Blank entries are skipped rather than passed on as an unnamed
// reference, which the API server would reject for the whole pod.
func imagePullSecrets(names []string) []corev1.LocalObjectReference {
	out := make([]corev1.LocalObjectReference, 0, len(names))
	for _, n := range names {
		if n = strings.TrimSpace(n); n != "" {
			out = append(out, corev1.LocalObjectReference{Name: n})
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// gitCloneInit returns an init container that clones repo into the workspace
// volume on first start (skipped if the volume already has contents) and chowns
// it to uid/gid 1000 (both IDE users) so the IDE can write. An optional branch
// selects a single branch to clone. A non-empty authSecret names a basic-auth
// Secret whose credentials are fed to git through a credential helper.
func gitCloneInit(repo, branch, dir, authSecret string) corev1.Container {
	branchFlag := ""
	if b := strings.TrimSpace(branch); b != "" {
		branchFlag = fmt.Sprintf("--branch %s --single-branch ", shellQuote(b))
	}

	// gitCmd carries a literal %s (from the helper's printf). It is only ever
	// passed to Sprintf as an argument, never as part of the format string, so it
	// is not rescanned — folding it into the format below would break the clone.
	gitCmd := "git"
	var env []corev1.EnvVar
	if s := strings.TrimSpace(authSecret); s != "" {
		gitCmd = "git " + strings.Join(gitCredentialArgs, " ")
		env = basicAuthEnv(s, "GIT_USERNAME", "GIT_PASSWORD")
	}

	script := fmt.Sprintf(`if [ -z "$(ls -A %s 2>/dev/null)" ]; then %s clone %s%s %s && chown -R 1000:1000 %s; fi`,
		dir, gitCmd, branchFlag, shellQuote(repo), dir, dir)
	return corev1.Container{
		Name:         "git-clone",
		Image:        "alpine/git:latest",
		Command:      []string{"sh", "-c", script},
		Env:          env,
		VolumeMounts: []corev1.VolumeMount{{Name: workspaceVolumeName, MountPath: dir}},
	}
}

func (b *Backend) createService(ctx context.Context, name string, labels map[string]string, targetPort int32) error {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: b.namespace, Labels: labels},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{labelName: name},
			Ports: []corev1.ServicePort{{
				Port:       80,
				TargetPort: intstr.FromInt(int(targetPort)),
				Protocol:   corev1.ProtocolTCP,
			}},
		},
	}
	if _, err := b.client.CoreV1().Services(b.namespace).Create(ctx, svc, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create service %s: %w", name, err)
	}
	return nil
}

func (b *Backend) createIngress(ctx context.Context, name string, labels map[string]string, spec workspace.Spec) error {
	host := workspaceHost(name, spec.Domain)
	pathType := netv1.PathTypePrefix
	ingressClass := "nginx"

	annotations := map[string]string{
		// Websocket / long-lived connections for the IDE (same tuning the old Coder ingress used).
		"nginx.ingress.kubernetes.io/proxy-read-timeout": "3600",
		"nginx.ingress.kubernetes.io/proxy-send-timeout": "3600",
		"nginx.ingress.kubernetes.io/proxy-body-size":    "0",
		"nginx.ingress.kubernetes.io/proxy-http-version": "1.1",
	}

	tlsSecret := spec.WildcardTLSSecret
	if tlsSecret == "" && spec.ClusterIssuer != "" {
		// No shared wildcard cert — request a per-host certificate from cert-manager.
		tlsSecret = name + "-tls"
		annotations["cert-manager.io/cluster-issuer"] = spec.ClusterIssuer
	}

	ing := &netv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: b.namespace, Labels: labels, Annotations: annotations},
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
	// Only advertise TLS when a certificate source exists: the nip.io fallback has
	// no ClusterIssuer, and an ingress pointing at a secret nothing ever creates
	// would serve the controller's self-signed default certificate.
	if host != "" && tlsSecret != "" {
		ing.Spec.TLS = []netv1.IngressTLS{{Hosts: []string{host}, SecretName: tlsSecret}}
	}
	if _, err := b.client.NetworkingV1().Ingresses(b.namespace).Create(ctx, ing, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create ingress %s: %w", name, err)
	}
	return nil
}

// toWorkspace maps a Deployment (plus routing context) to a workspace.Workspace.
func (b *Backend) toWorkspace(dep *appsv1.Deployment, domain, token string) workspace.Workspace {
	phase, ready := deploymentReadiness(dep)
	ide := dep.Annotations[annotationIDE]
	if ide == "" {
		ide = workspace.DefaultIDEKind
	}
	host := workspaceHost(dep.Name, domain)
	// URL is the base workspace URL (shown to the student). OpenURL is the redirect
	// target: openvscode carries the connection token for a silent open; code-server
	// uses the base URL (the student enters Token on the login page).
	url, openURL := "", ""
	if host != "" {
		url = fmt.Sprintf("%s://%s/", schemeFromDeployment(dep), host)
		openURL = url
		if ide == workspace.IDEOpenVSCode && token != "" {
			openURL = url + "?tkn=" + token
			// openvscode opens a subfolder via the standard VS Code-web ?folder= param.
			if folder := dep.Annotations[annotationFolder]; folder != "" {
				openURL += "&folder=" + neturl.QueryEscape(folder)
			}
		}
	}
	return workspace.Workspace{
		ID:        dep.Name,
		Name:      dep.Name,
		Owner:     dep.Labels[labelOwner],
		URL:       url,
		OpenURL:   openURL,
		Token:     token,
		IDE:       ide,
		CreatedAt: dep.CreationTimestamp.Time,
		UpdatedAt: deploymentUpdatedAt(dep),
		Ready:     ready,
		Phase:     phase,
	}
}

// deploymentUpdatedAt returns the newest condition transition time, falling back
// to the creation time.
func deploymentUpdatedAt(dep *appsv1.Deployment) time.Time {
	latest := dep.CreationTimestamp.Time
	for _, c := range dep.Status.Conditions {
		if c.LastUpdateTime.Time.After(latest) {
			latest = c.LastUpdateTime.Time
		}
	}
	return latest
}

// workspaceHost builds the ingress host for a workspace. Returns "" when no
// domain is configured (workspace is reachable only in-cluster).
func workspaceHost(name, domain string) string {
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return ""
	}
	return name + "." + domain
}

func buildResources(cpu, mem string) corev1.ResourceRequirements {
	reqs := corev1.ResourceList{}
	if q, err := resource.ParseQuantity(strings.TrimSpace(cpu)); err == nil && cpu != "" {
		reqs[corev1.ResourceCPU] = q
	}
	if q, err := resource.ParseQuantity(strings.TrimSpace(mem)); err == nil && mem != "" {
		reqs[corev1.ResourceMemory] = q
	}
	if len(reqs) == 0 {
		return corev1.ResourceRequirements{}
	}
	return corev1.ResourceRequirements{Requests: reqs, Limits: reqs}
}

// shellQuote single-quotes a value for safe embedding in a sh -c script.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// domainFromDeployment reconstructs the base domain from a workspace's ingress
// host label if present. It returns "" so callers fall back to lab config; the
// canonical domain is supplied by handlers, this is a best-effort convenience.
func domainFromDeployment(dep *appsv1.Deployment) string {
	return dep.Annotations[annotationDomain]
}

// schemeFromDeployment returns the URL scheme recorded at create time. Workspaces
// created before the scheme was tracked predate the nip.io fallback and were
// always TLS-terminated, so they default to https.
func schemeFromDeployment(dep *appsv1.Deployment) string {
	if s := dep.Annotations[annotationScheme]; s != "" {
		return s
	}
	return schemeHTTPS
}

// tokenFromDeployment returns the workspace access token stored at create time.
func tokenFromDeployment(dep *appsv1.Deployment) string {
	return dep.Annotations[annotationToken]
}

// deploymentReadiness derives a readiness phase from a Deployment's status.
func deploymentReadiness(dep *appsv1.Deployment) (phase string, ready bool) {
	if dep.Status.ReadyReplicas >= 1 {
		return workspace.PhaseRunning, true
	}
	for _, cond := range dep.Status.Conditions {
		if cond.Type == appsv1.DeploymentProgressing && cond.Status == corev1.ConditionFalse && cond.Reason == "ProgressDeadlineExceeded" {
			return workspace.PhaseFailed, false
		}
		if cond.Type == appsv1.DeploymentReplicaFailure && cond.Status == corev1.ConditionTrue {
			return workspace.PhaseFailed, false
		}
	}
	if dep.Status.UnavailableReplicas > 0 || dep.Status.ReadyReplicas == 0 {
		return workspace.PhaseAgentsStarting, false
	}
	return workspace.PhaseProvisioning, false
}
