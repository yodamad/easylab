package kube

import (
	"context"
	"strings"
	"testing"
	"time"

	"easylab/internal/providers/workspace"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// ideContainer returns the workspace (IDE) container of a created deployment.
func ideContainer(t *testing.T, cs *fake.Clientset, name string) corev1.Container {
	t.Helper()
	dep, err := cs.AppsV1().Deployments("workshops").Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	for _, c := range dep.Spec.Template.Spec.Containers {
		if c.Name == workspaceContainerName {
			return c
		}
	}
	t.Fatalf("workspace container not found in %s", name)
	return corev1.Container{}
}

func hasArg(args []string, flag, val string) bool {
	for i, a := range args {
		if a == flag && i+1 < len(args) && args[i+1] == val {
			return true
		}
	}
	return false
}

func TestEnsureWorkspace_CodeServerDefaults(t *testing.T) {
	b, cs := newTestBackend()
	ws, err := b.EnsureWorkspace(context.Background(), workspace.Spec{
		LabID: "job-1", Owner: "alice", Domain: "lab.example.com", Token: "tok123",
	})
	if err != nil {
		t.Fatal(err)
	}
	c := ideContainer(t, cs, ws.ID)
	if !strings.Contains(c.Image, "code-server") {
		t.Errorf("expected code-server image, got %q", c.Image)
	}
	if c.Ports[0].ContainerPort != 8080 {
		t.Errorf("expected port 8080, got %d", c.Ports[0].ContainerPort)
	}
	if !hasArg(c.Args, "--auth", "password") {
		t.Errorf("expected --auth password arg, got %v", c.Args)
	}
	if ws.IDE != workspace.IDECodeServer {
		t.Errorf("expected IDE code-server, got %q", ws.IDE)
	}
	if ws.OpenURL != ws.URL {
		t.Errorf("expected OpenURL to be the bare login-page URL, got %q", ws.OpenURL)
	}
}

// TestEnsureWorkspace_LegacyIDEValue pins the backward-compatibility contract:
// a lab saved before OpenVSCode support was removed must still produce a working
// code-server workspace, and must not report the retired value back to callers.
func TestEnsureWorkspace_LegacyIDEValue(t *testing.T) {
	b, cs := newTestBackend()
	ws, err := b.EnsureWorkspace(context.Background(), workspace.Spec{
		LabID: "job-1", Owner: "alice", IDE: workspace.IDEOpenVSCode,
		Domain: "lab.example.com", Token: "tok123",
	})
	if err != nil {
		t.Fatal(err)
	}
	c := ideContainer(t, cs, ws.ID)
	if !strings.Contains(c.Image, "code-server") {
		t.Errorf("expected code-server image, got %q", c.Image)
	}
	if ws.IDE != workspace.IDECodeServer {
		t.Errorf("expected the legacy value to normalize to code-server, got %q", ws.IDE)
	}
}

func TestEnsureWorkspace_CodeServerAuth(t *testing.T) {
	b, cs := newTestBackend()
	ws, err := b.EnsureWorkspace(context.Background(), workspace.Spec{
		LabID: "job-1", Owner: "bob", IDE: workspace.IDECodeServer, Domain: "lab.example.com", Token: "pw123",
	})
	if err != nil {
		t.Fatal(err)
	}
	c := ideContainer(t, cs, ws.ID)
	if !strings.Contains(c.Image, "code-server") {
		t.Errorf("expected code-server image, got %q", c.Image)
	}
	if c.Ports[0].ContainerPort != 8080 {
		t.Errorf("expected port 8080, got %d", c.Ports[0].ContainerPort)
	}
	foundPassword := false
	for _, e := range c.Env {
		if e.Name == "PASSWORD" && e.Value == "pw123" {
			foundPassword = true
		}
	}
	if !foundPassword {
		t.Errorf("expected PASSWORD env, got %v", c.Env)
	}
	// code-server opens on the base URL (login page), not a ?tkn= link.
	if strings.Contains(ws.OpenURL, "tkn=") || ws.OpenURL != ws.URL {
		t.Errorf("expected bare OpenURL for code-server, got %q", ws.OpenURL)
	}
}

func TestEnsureWorkspace_StartupScriptWrapsCommand(t *testing.T) {
	b, cs := newTestBackend()
	ws, err := b.EnsureWorkspace(context.Background(), workspace.Spec{
		LabID: "job-1", Owner: "carol", Domain: "d",
		StartupScript: "echo hello",
		Extensions:    []string{"golang.go"},
		Token:         "t",
	})
	if err != nil {
		t.Fatal(err)
	}
	c := ideContainer(t, cs, ws.ID)
	if len(c.Command) == 0 {
		t.Fatalf("expected wrapped command, got args-only %v", c.Args)
	}
	script := c.Command[len(c.Command)-1]
	if !strings.Contains(script, "echo hello") {
		t.Errorf("startup script missing from command: %q", script)
	}
	if !strings.Contains(script, "--install-extension") {
		t.Errorf("extension install missing from command: %q", script)
	}
	if !strings.Contains(script, "exec ") {
		t.Errorf("expected exec of the IDE server: %q", script)
	}
}

func TestEnsureWorkspace_NoSetupUsesPlainArgs(t *testing.T) {
	b, cs := newTestBackend()
	ws, err := b.EnsureWorkspace(context.Background(), workspace.Spec{LabID: "job-1", Owner: "dave", Domain: "d", Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	c := ideContainer(t, cs, ws.ID)
	if len(c.Command) != 0 {
		t.Errorf("expected no wrapped command without setup, got %v", c.Command)
	}
	if len(c.Args) == 0 {
		t.Errorf("expected plain start args")
	}
}

func TestEnsureWorkspace_SidecarAndMount(t *testing.T) {
	b, cs := newTestBackend()
	ws, err := b.EnsureWorkspace(context.Background(), workspace.Spec{
		LabID: "job-1", Owner: "erin", Domain: "d", Token: "t",
		Sidecars: []workspace.Sidecar{{Name: "db", Image: "postgres:16", Ports: []int{5432}, Env: map[string]string{"POSTGRES_PASSWORD": "x"}}},
		Mounts:   []workspace.Mount{{Type: "configmap", Name: "app-config", Path: "/etc/config"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	dep, _ := cs.AppsV1().Deployments("workshops").Get(context.Background(), ws.ID, metav1.GetOptions{})

	var sidecar *corev1.Container
	for i := range dep.Spec.Template.Spec.Containers {
		if dep.Spec.Template.Spec.Containers[i].Name == "db" {
			sidecar = &dep.Spec.Template.Spec.Containers[i]
		}
	}
	if sidecar == nil {
		t.Fatalf("sidecar container 'db' not found")
	}
	if sidecar.Image != "postgres:16" || sidecar.Ports[0].ContainerPort != 5432 {
		t.Errorf("unexpected sidecar spec: %+v", sidecar)
	}

	foundVol := false
	for _, v := range dep.Spec.Template.Spec.Volumes {
		if v.ConfigMap != nil && v.ConfigMap.Name == "app-config" {
			foundVol = true
		}
	}
	if !foundVol {
		t.Errorf("configmap volume not found: %v", dep.Spec.Template.Spec.Volumes)
	}
	c := ideContainer(t, cs, ws.ID)
	foundMount := false
	for _, m := range c.VolumeMounts {
		if m.MountPath == "/etc/config" {
			foundMount = true
		}
	}
	if !foundMount {
		t.Errorf("mount /etc/config not found: %v", c.VolumeMounts)
	}
}

func TestEnsureWorkspace_PrivilegedSidecar(t *testing.T) {
	b, cs := newTestBackend()
	ws, err := b.EnsureWorkspace(context.Background(), workspace.Spec{
		LabID: "job-1", Owner: "frank", Domain: "d", Token: "t",
		Sidecars: []workspace.Sidecar{{
			Name: "docker", Image: "docker:dind", Ports: []int{2375},
			Env:          map[string]string{"DOCKER_TLS_CERTDIR": ""},
			Privileged:   true,
			Capabilities: []string{"SYS_ADMIN"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	dep, _ := cs.AppsV1().Deployments("workshops").Get(context.Background(), ws.ID, metav1.GetOptions{})
	var dind *corev1.Container
	for i := range dep.Spec.Template.Spec.Containers {
		if dep.Spec.Template.Spec.Containers[i].Name == "docker" {
			dind = &dep.Spec.Template.Spec.Containers[i]
		}
	}
	if dind == nil {
		t.Fatal("docker sidecar not found")
	}
	if dind.SecurityContext == nil || dind.SecurityContext.Privileged == nil || !*dind.SecurityContext.Privileged {
		t.Errorf("expected privileged securityContext, got %+v", dind.SecurityContext)
	}
	if dind.SecurityContext.Capabilities == nil || len(dind.SecurityContext.Capabilities.Add) != 1 || dind.SecurityContext.Capabilities.Add[0] != "SYS_ADMIN" {
		t.Errorf("expected SYS_ADMIN capability, got %+v", dind.SecurityContext.Capabilities)
	}
}

func newTestBackend() (*Backend, *fake.Clientset) {
	cs := fake.NewSimpleClientset()
	return newBackend(cs, "workshops"), cs
}

func TestWorkspaceName_DeterministicAndSafe(t *testing.T) {
	n1 := workspaceName("job-abc", "Alice.Smith", "docker")
	n2 := workspaceName("job-abc", "Alice.Smith", "docker")
	if n1 != n2 {
		t.Fatalf("workspaceName not deterministic: %q vs %q", n1, n2)
	}
	if got := workspaceName("job-xyz", "Alice.Smith", "docker"); got == n1 {
		t.Fatalf("workspaceName collided across labs: %q", got)
	}
	// Different template for the same lab/owner must yield a different workspace.
	if got := workspaceName("job-abc", "Alice.Smith", "go"); got == n1 {
		t.Fatalf("workspaceName collided across templates: %q", got)
	}
	if len(n1) > 63 {
		t.Fatalf("workspaceName too long (%d): %q", len(n1), n1)
	}
	for _, c := range n1 {
		if !(c == '-' || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')) {
			t.Fatalf("workspaceName has invalid DNS-1123 char %q in %q", c, n1)
		}
	}
}

func TestEnsureWorkspace_CreatesResources(t *testing.T) {
	b, cs := newTestBackend()
	ctx := context.Background()

	ws, err := b.EnsureWorkspace(ctx, workspace.Spec{
		LabID:    "job-1",
		Owner:    "alice",
		Image:    "codercom/code-server:latest",
		DiskSize: "5Gi",
		Domain:   "lab.example.com",
		Token:    "secret-token",
	})
	if err != nil {
		t.Fatalf("EnsureWorkspace error: %v", err)
	}
	if ws.URL == "" || ws.Token != "secret-token" {
		t.Fatalf("unexpected workspace: %+v", ws)
	}

	name := ws.ID
	if _, err := cs.AppsV1().Deployments("workshops").Get(ctx, name, metav1.GetOptions{}); err != nil {
		t.Errorf("deployment not created: %v", err)
	}
	if _, err := cs.CoreV1().Services("workshops").Get(ctx, name, metav1.GetOptions{}); err != nil {
		t.Errorf("service not created: %v", err)
	}
	if _, err := cs.CoreV1().PersistentVolumeClaims("workshops").Get(ctx, name, metav1.GetOptions{}); err != nil {
		t.Errorf("pvc not created: %v", err)
	}
	if _, err := cs.NetworkingV1().Ingresses("workshops").Get(ctx, name, metav1.GetOptions{}); err != nil {
		t.Errorf("ingress not created: %v", err)
	}
}

// TestEnsureWorkspace_AttributesTemplate pins that the template a workspace was
// created from survives the round-trip: it is returned by EnsureWorkspace and
// read back out of the cluster by ListWorkspaces, so the admin UI can correlate
// running workspaces with configured templates.
func TestEnsureWorkspace_AttributesTemplate(t *testing.T) {
	b, _ := newTestBackend()
	ctx := context.Background()

	created, err := b.EnsureWorkspace(ctx, workspace.Spec{
		LabID:    "job-1",
		Owner:    "alice",
		Template: "python-lab",
		Domain:   "lab.example.com",
		Token:    "t",
	})
	if err != nil {
		t.Fatalf("EnsureWorkspace error: %v", err)
	}
	if created.Template != "python-lab" {
		t.Errorf("EnsureWorkspace template = %q, want %q", created.Template, "python-lab")
	}

	list, err := b.ListWorkspaces(ctx, "job-1")
	if err != nil {
		t.Fatalf("ListWorkspaces error: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("ListWorkspaces returned %d workspaces, want 1", len(list))
	}
	if list[0].Template != "python-lab" {
		t.Errorf("ListWorkspaces template = %q, want %q", list[0].Template, "python-lab")
	}
}

func TestEnsureWorkspace_Idempotent(t *testing.T) {
	b, cs := newTestBackend()
	ctx := context.Background()
	spec := workspace.Spec{LabID: "job-1", Owner: "bob", Domain: "lab.example.com", Token: "t"}

	if _, err := b.EnsureWorkspace(ctx, spec); err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	if _, err := b.EnsureWorkspace(ctx, spec); err != nil {
		t.Fatalf("second ensure should be a no-op: %v", err)
	}
	deps, _ := cs.AppsV1().Deployments("workshops").List(ctx, metav1.ListOptions{})
	if len(deps.Items) != 1 {
		t.Fatalf("expected exactly 1 deployment, got %d", len(deps.Items))
	}
}

// withIngressLB seeds a ready ingress-nginx controller Service carrying the given
// external IP, which is what the nip.io fallback looks for.
func withIngressLB(cs *fake.Clientset, ip, hostname string) {
	lb := corev1.LoadBalancerIngress{IP: ip, Hostname: hostname}
	_, _ = cs.CoreV1().Services(ingressNginxNamespace).Create(context.Background(), &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: ingressNginxService, Namespace: ingressNginxNamespace},
		Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
		Status: corev1.ServiceStatus{
			LoadBalancer: corev1.LoadBalancerStatus{Ingress: []corev1.LoadBalancerIngress{lb}},
		},
	}, metav1.CreateOptions{})
}

// A lab with no domain is exposed through nip.io on the ingress LoadBalancer IP,
// over plain HTTP: that mode provisions no cert-manager, so requesting a
// certificate would leave the ingress serving nginx's self-signed default.
func TestEnsureWorkspace_NipIOFallbackWhenNoDomain(t *testing.T) {
	b, cs := newTestBackend()
	withIngressLB(cs, "51.75.20.10", "")
	ctx := context.Background()

	// ClusterIssuer mirrors the handler, which always sets one; the fallback must
	// drop it rather than request a cert it cannot obtain.
	ws, err := b.EnsureWorkspace(ctx, workspace.Spec{
		LabID: "job-1", Owner: "dave", Token: "tok", ClusterIssuer: "letsencrypt-prod",
	})
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}

	wantHost := ws.ID + ".51.75.20.10.nip.io"
	if ws.URL != "http://"+wantHost+"/" {
		t.Errorf("expected plain-HTTP nip.io URL, got %q", ws.URL)
	}
	if ws.OpenURL != ws.URL {
		t.Errorf("expected the bare nip.io OpenURL, got %q", ws.OpenURL)
	}

	ing, err := cs.NetworkingV1().Ingresses("workshops").Get(ctx, ws.ID, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected an ingress for the nip.io fallback: %v", err)
	}
	if ing.Spec.Rules[0].Host != wantHost {
		t.Errorf("expected ingress host %q, got %q", wantHost, ing.Spec.Rules[0].Host)
	}
	if len(ing.Spec.TLS) != 0 {
		t.Errorf("expected no TLS without cert-manager, got %+v", ing.Spec.TLS)
	}
	if issuer, ok := ing.Annotations["cert-manager.io/cluster-issuer"]; ok {
		t.Errorf("expected no cluster-issuer on the nip.io fallback, got %q", issuer)
	}
}

// nip.io encodes an IP, so a hostname-only LoadBalancer cannot be used.
func TestEnsureWorkspace_NoFallbackForHostnameOnlyLB(t *testing.T) {
	b, cs := newTestBackend()
	withIngressLB(cs, "", "lb.elb.example.com")
	ctx := context.Background()

	ws, err := b.EnsureWorkspace(ctx, workspace.Spec{LabID: "job-1", Owner: "dave", Token: "t"})
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if ws.URL != "" {
		t.Errorf("expected no URL for a hostname-only LoadBalancer, got %q", ws.URL)
	}
	ings, _ := cs.NetworkingV1().Ingresses("workshops").List(ctx, metav1.ListOptions{})
	if len(ings.Items) != 0 {
		t.Errorf("expected no ingress, got %d", len(ings.Items))
	}
}

// A configured domain keeps TLS: the fallback must not weaken real labs.
func TestEnsureWorkspace_DomainKeepsHTTPSAndRequestsCert(t *testing.T) {
	b, cs := newTestBackend()
	withIngressLB(cs, "51.75.20.10", "") // present, but the domain must win
	ctx := context.Background()

	ws, err := b.EnsureWorkspace(ctx, workspace.Spec{
		LabID: "job-1", Owner: "erin", Domain: "lab.example.com",
		ClusterIssuer: "letsencrypt-prod", Token: "t",
	})
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if ws.URL != "https://"+ws.ID+".lab.example.com/" {
		t.Errorf("expected HTTPS URL on the configured domain, got %q", ws.URL)
	}

	ing, err := cs.NetworkingV1().Ingresses("workshops").Get(ctx, ws.ID, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get ingress: %v", err)
	}
	if len(ing.Spec.TLS) != 1 || ing.Spec.TLS[0].SecretName != ws.ID+"-tls" {
		t.Errorf("expected a per-host TLS secret, got %+v", ing.Spec.TLS)
	}
	if ing.Annotations["cert-manager.io/cluster-issuer"] != "letsencrypt-prod" {
		t.Errorf("expected cluster-issuer annotation, got %q", ing.Annotations["cert-manager.io/cluster-issuer"])
	}
}

// The resolved fallback domain lives only in the deployment's annotations, so
// reads must rebuild the same URL the student was originally given.
func TestGetWorkspace_PreservesNipIORouting(t *testing.T) {
	b, cs := newTestBackend()
	withIngressLB(cs, "1.2.3.4", "")
	ctx := context.Background()

	created, err := b.EnsureWorkspace(ctx, workspace.Spec{LabID: "job-1", Owner: "dave", Token: "tok"})
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}

	got, err := b.GetWorkspace(ctx, created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.URL != created.URL {
		t.Errorf("GetWorkspace URL %q does not match created URL %q", got.URL, created.URL)
	}

	// A repeat Ensure (the student clicking "create" again) must be idempotent too.
	again, err := b.EnsureWorkspace(ctx, workspace.Spec{LabID: "job-1", Owner: "dave", Token: "tok"})
	if err != nil {
		t.Fatalf("re-ensure: %v", err)
	}
	if again.URL != created.URL {
		t.Errorf("re-ensure URL %q does not match created URL %q", again.URL, created.URL)
	}
}

// Workspaces created before the scheme was recorded predate the fallback and were
// always TLS-terminated.
func TestSchemeFromDeployment_DefaultsToHTTPS(t *testing.T) {
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{}}}
	if got := schemeFromDeployment(dep); got != schemeHTTPS {
		t.Errorf("expected https default, got %q", got)
	}
}

func TestRouting(t *testing.T) {
	tests := []struct {
		name       string
		labDomain  string
		lbIP       string
		wantDomain string
		wantScheme string
	}{
		{"configured domain is used as-is", "lab.example.com", "1.2.3.4", "lab.example.com", schemeHTTPS},
		{"no domain falls back to nip.io", "", "1.2.3.4", "1.2.3.4.nip.io", schemeHTTP},
		{"no domain and no ingress IP is unreachable", "", "", "", schemeHTTPS},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			b, cs := newTestBackend()
			if tt.lbIP != "" {
				withIngressLB(cs, tt.lbIP, "")
			}
			domain, scheme := b.Routing(context.Background(), tt.labDomain)
			if domain != tt.wantDomain || scheme != tt.wantScheme {
				t.Errorf("Routing() = (%q, %q), want (%q, %q)", domain, scheme, tt.wantDomain, tt.wantScheme)
			}
		})
	}
}

func TestEnsureWorkspace_NoDomainSkipsIngress(t *testing.T) {
	b, cs := newTestBackend()
	ctx := context.Background()
	ws, err := b.EnsureWorkspace(ctx, workspace.Spec{LabID: "job-1", Owner: "carol", Token: "t"})
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if ws.URL != "" {
		t.Errorf("expected empty URL with no domain, got %q", ws.URL)
	}
	ings, _ := cs.NetworkingV1().Ingresses("workshops").List(ctx, metav1.ListOptions{})
	if len(ings.Items) != 0 {
		t.Errorf("expected no ingress when domain is empty, got %d", len(ings.Items))
	}
}

// A lab with a DNS provider gets one wildcard certificate at provisioning, and
// every workspace is served from it. A lab without one falls back to a
// per-workspace certificate requested through the ClusterIssuer.
func TestEnsureWorkspace_IngressTLSSource(t *testing.T) {
	const issuerAnnotation = "cert-manager.io/cluster-issuer"

	tests := []struct {
		name              string
		wildcardTLSSecret string
		clusterIssuer     string
		wantSecret        string
		wantIssuer        string
	}{
		{
			name:              "wildcard secret is used directly, no certificate requested",
			wildcardTLSSecret: "easylab-wildcard-tls",
			clusterIssuer:     "letsencrypt-prod",
			wantSecret:        "easylab-wildcard-tls",
			wantIssuer:        "",
		},
		{
			name:              "without a wildcard secret the ingress requests its own",
			wildcardTLSSecret: "",
			clusterIssuer:     "letsencrypt-prod",
			wantSecret:        "", // per-workspace, name derived below
			wantIssuer:        "letsencrypt-prod",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			b, cs := newTestBackend()
			ctx := context.Background()

			ws, err := b.EnsureWorkspace(ctx, workspace.Spec{
				LabID:             "job-1",
				Owner:             "alice",
				Token:             "t",
				Domain:            "lab.example.com",
				ClusterIssuer:     tt.clusterIssuer,
				WildcardTLSSecret: tt.wildcardTLSSecret,
			})
			require.NoError(t, err)

			ing, err := cs.NetworkingV1().Ingresses("workshops").Get(ctx, ws.Name, metav1.GetOptions{})
			require.NoError(t, err)
			require.Len(t, ing.Spec.TLS, 1)

			wantSecret := tt.wantSecret
			if wantSecret == "" {
				wantSecret = ws.Name + "-tls"
			}
			assert.Equal(t, wantSecret, ing.Spec.TLS[0].SecretName)
			assert.Equal(t, tt.wantIssuer, ing.Annotations[issuerAnnotation])
		})
	}
}

func TestListAndDeleteWorkspaces(t *testing.T) {
	b, cs := newTestBackend()
	ctx := context.Background()
	if _, err := b.EnsureWorkspace(ctx, workspace.Spec{LabID: "job-1", Owner: "alice", Domain: "d", Token: "t"}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.EnsureWorkspace(ctx, workspace.Spec{LabID: "job-1", Owner: "bob", Domain: "d", Token: "t"}); err != nil {
		t.Fatal(err)
	}
	// A workspace in a different lab must not show up.
	if _, err := b.EnsureWorkspace(ctx, workspace.Spec{LabID: "job-2", Owner: "carol", Domain: "d", Token: "t"}); err != nil {
		t.Fatal(err)
	}

	list, err := b.ListWorkspaces(ctx, "job-1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 workspaces for job-1, got %d", len(list))
	}

	if err := b.DeleteWorkspace(ctx, "job-1", list[0].ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := cs.AppsV1().Deployments("workshops").Get(ctx, list[0].ID, metav1.GetOptions{}); err == nil {
		t.Errorf("deployment %s should have been deleted", list[0].ID)
	}
}

func TestDeploymentReadiness(t *testing.T) {
	tests := []struct {
		name      string
		status    appsv1.DeploymentStatus
		wantPhase string
		wantReady bool
	}{
		{"ready", appsv1.DeploymentStatus{ReadyReplicas: 1}, workspace.PhaseRunning, true},
		{
			"progress deadline exceeded",
			appsv1.DeploymentStatus{Conditions: []appsv1.DeploymentCondition{{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionFalse, Reason: "ProgressDeadlineExceeded"}}},
			workspace.PhaseFailed, false,
		},
		{"starting", appsv1.DeploymentStatus{UnavailableReplicas: 1}, workspace.PhaseAgentsStarting, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dep := &appsv1.Deployment{Status: tt.status}
			phase, ready := deploymentReadiness(dep)
			if phase != tt.wantPhase || ready != tt.wantReady {
				t.Errorf("deploymentReadiness = (%q, %v), want (%q, %v)", phase, ready, tt.wantPhase, tt.wantReady)
			}
		})
	}
}

func TestGetWorkspace_ReflectsCreationTime(t *testing.T) {
	b, cs := newTestBackend()
	ctx := context.Background()
	spec := workspace.Spec{LabID: "job-1", Owner: "dave", Template: "docker", Domain: "d", Token: "t"}
	ws, err := b.EnsureWorkspace(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	// Backdate the deployment's creation time and confirm Get reflects it.
	dep, _ := cs.AppsV1().Deployments("workshops").Get(ctx, ws.ID, metav1.GetOptions{})
	dep.CreationTimestamp = metav1.NewTime(time.Now().Add(-3 * time.Hour))
	_, _ = cs.AppsV1().Deployments("workshops").Update(ctx, dep, metav1.UpdateOptions{})

	got, err := b.GetWorkspace(ctx, ws.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if time.Since(got.CreatedAt) < 2*time.Hour {
		t.Errorf("expected backdated CreatedAt, got %v", got.CreatedAt)
	}
	if got.Token != "t" {
		t.Errorf("expected token from annotation, got %q", got.Token)
	}
}

func TestEnsureWorkspace_FSGroupAndGitBranch(t *testing.T) {
	b, cs := newTestBackend()
	ctx := context.Background()
	ws, err := b.EnsureWorkspace(ctx, workspace.Spec{
		LabID: "job-1", Owner: "gina", Template: "go", Domain: "d", Token: "t",
		GitRepo: "https://example.com/repo.git", GitBranch: "dev", DiskSize: "5Gi",
	})
	if err != nil {
		t.Fatal(err)
	}
	dep, _ := cs.AppsV1().Deployments("workshops").Get(ctx, ws.ID, metav1.GetOptions{})
	// fsGroup makes the PVC writable by the IDE user.
	if dep.Spec.Template.Spec.SecurityContext == nil ||
		dep.Spec.Template.Spec.SecurityContext.FSGroup == nil ||
		*dep.Spec.Template.Spec.SecurityContext.FSGroup != 1000 {
		t.Errorf("expected pod fsGroup=1000, got %+v", dep.Spec.Template.Spec.SecurityContext)
	}
	// git-clone init container clones the requested branch.
	if len(dep.Spec.Template.Spec.InitContainers) == 0 {
		t.Fatal("expected a git-clone init container")
	}
	script := strings.Join(dep.Spec.Template.Spec.InitContainers[0].Command, " ")
	if !strings.Contains(script, "--branch 'dev'") {
		t.Errorf("expected --branch 'dev' in clone command: %q", script)
	}
}

func TestEnsureWorkspace_ReadinessProbeAndProgressDeadline(t *testing.T) {
	b, cs := newTestBackend()
	ws, err := b.EnsureWorkspace(context.Background(), workspace.Spec{LabID: "job-1", Owner: "ivy", Domain: "d", Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	dep, _ := cs.AppsV1().Deployments("workshops").Get(context.Background(), ws.ID, metav1.GetOptions{})
	if dep.Spec.ProgressDeadlineSeconds == nil || *dep.Spec.ProgressDeadlineSeconds != 1800 {
		t.Errorf("expected progressDeadlineSeconds=1800, got %v", dep.Spec.ProgressDeadlineSeconds)
	}
	c := ideContainer(t, cs, ws.ID)
	if c.ReadinessProbe == nil || c.ReadinessProbe.TCPSocket == nil {
		t.Fatalf("expected a TCP readiness probe, got %+v", c.ReadinessProbe)
	}
	if c.ReadinessProbe.TCPSocket.Port.IntValue() != 8080 {
		t.Errorf("expected readiness probe on port 8080, got %v", c.ReadinessProbe.TCPSocket.Port)
	}
}

// TestEnsureWorkspace_GitFolderOpenPath pins that a template's subfolder reaches
// the IDE. code-server takes it as the positional start argument — it is not a
// URL parameter, so a regression here would silently open the repo root instead.
func TestEnsureWorkspace_GitFolderOpenPath(t *testing.T) {
	ctx := context.Background()

	b, cs := newTestBackend()
	ws, err := b.EnsureWorkspace(ctx, workspace.Spec{
		LabID: "job-1", Owner: "jo", Domain: "lab.example.com", Token: "t",
		GitRepo: "https://example.com/repo.git", DiskSize: "5Gi", GitFolder: "backend",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ws.OpenURL, "folder=") {
		t.Errorf("the folder is a start argument, not a URL param: %q", ws.OpenURL)
	}
	c := ideContainer(t, cs, ws.ID)
	found := false
	for _, a := range c.Args {
		if a == "/home/coder/project/backend" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected code-server to open /home/coder/project/backend, got args %v", c.Args)
	}
}

func TestEnsureWorkspace_TwoTemplatesTwoWorkspaces(t *testing.T) {
	b, cs := newTestBackend()
	ctx := context.Background()
	a, err := b.EnsureWorkspace(ctx, workspace.Spec{LabID: "job-1", Owner: "hank", Template: "docker", Domain: "d", Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	g, err := b.EnsureWorkspace(ctx, workspace.Spec{LabID: "job-1", Owner: "hank", Template: "go", Domain: "d", Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == g.ID {
		t.Fatalf("same student got the same workspace for two templates: %s", a.ID)
	}
	list, _ := cs.AppsV1().Deployments("workshops").List(ctx, metav1.ListOptions{})
	if len(list.Items) != 2 {
		t.Fatalf("expected 2 deployments, got %d", len(list.Items))
	}
}
