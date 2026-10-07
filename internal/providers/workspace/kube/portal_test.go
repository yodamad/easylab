package kube

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"easylab/internal/providers/workspace"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const portalTestNS = "workshops"

func portalSpec(labID, domain string) workspace.PortalSpec {
	return workspace.PortalSpec{
		LabID:  labID,
		Image:  "docker.io/yodamad/easylab:v1.0.0",
		Domain: domain,
		Config: map[string][]byte{"state.json": []byte(`{"lab_id":"` + labID + `"}`)},
	}
}

func TestPortalName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, labID, expected string
	}{
		{name: "simple", labID: "job-123", expected: "easylab-portal-job-123"},
		{name: "sanitized", labID: "Job_ABC", expected: "easylab-portal-job-abc"},
		{name: "truncated without trailing dash", labID: strings.Repeat("a", 39) + "-" + strings.Repeat("b", 20), expected: "easylab-portal-" + strings.Repeat("a", 39)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := portalName(tt.labID)
			assert.Equal(t, tt.expected, got)
			// The longest derived name must still fit a DNS-1123 label.
			assert.LessOrEqual(t, len(got+portalIngressAccessSuffix), 63)
		})
	}
}

func TestEnsurePortal_CreatesEverything(t *testing.T) {
	t.Parallel()
	b, cs := newTestBackend()
	ctx := context.Background()
	spec := portalSpec("lab-1", "lab.example.com")
	spec.WildcardTLSSecret = "easylab-wildcard-tls"

	url, err := b.EnsurePortal(ctx, spec)
	require.NoError(t, err)
	assert.Equal(t, "https://lab.example.com", url)

	name := portalName("lab-1")

	_, err = cs.CoreV1().ServiceAccounts(portalTestNS).Get(ctx, name, metav1.GetOptions{})
	require.NoError(t, err)
	binding, err := cs.RbacV1().RoleBindings(portalTestNS).Get(ctx, name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, name, binding.RoleRef.Name)
	require.Len(t, binding.Subjects, 1)
	assert.Equal(t, name, binding.Subjects[0].Name)
	assert.Equal(t, portalTestNS, binding.Subjects[0].Namespace)

	sec, err := cs.CoreV1().Secrets(portalTestNS).Get(ctx, name+portalConfigSuffix, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, spec.Config, sec.Data)
	_, err = cs.CoreV1().ConfigMaps(portalTestNS).Get(ctx, name+portalOutboxSuffix, metav1.GetOptions{})
	require.NoError(t, err)

	dep, err := cs.AppsV1().Deployments(portalTestNS).Get(ctx, name, metav1.GetOptions{})
	require.NoError(t, err)
	pod := dep.Spec.Template.Spec
	assert.Equal(t, name, pod.ServiceAccountName)
	require.Len(t, pod.Containers, 1)
	assert.Equal(t, spec.Image, pod.Containers[0].Image)
	assert.Equal(t, []string{"/app/main", "--mode", "student", "--port", "8080"}, pod.Containers[0].Command)
	env := map[string]corev1.EnvVar{}
	for _, e := range pod.Containers[0].Env {
		env[e.Name] = e
	}
	assert.Equal(t, "lab-1", env[EnvPortalLabID].Value)
	require.NotNil(t, env[EnvPortalNamespace].ValueFrom)
	assert.Equal(t, "metadata.namespace", env[EnvPortalNamespace].ValueFrom.FieldRef.FieldPath)

	svc, err := cs.CoreV1().Services(portalTestNS).Get(ctx, name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, int32(portalContainerPort), svc.Spec.Ports[0].TargetPort.IntVal)
	assert.Equal(t, dep.Spec.Template.Labels[labelName], svc.Spec.Selector[labelName])

	ing, err := cs.NetworkingV1().Ingresses(portalTestNS).Get(ctx, name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "lab.example.com", ing.Spec.Rules[0].Host)
	require.Len(t, ing.Spec.TLS, 1)
	assert.Equal(t, "easylab-wildcard-tls", ing.Spec.TLS[0].SecretName)
	assert.NotContains(t, ing.Annotations, clusterIssuerAnnotation)

	// A lab with a domain needs nothing outside its own namespace.
	roles, err := cs.RbacV1().Roles(ingressControllerNamespace).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	assert.Empty(t, roles.Items)
}

// The portal's own Deployment must not be mistaken for a student workspace.
func TestEnsurePortal_IsNotListedAsWorkspace(t *testing.T) {
	t.Parallel()
	b, _ := newTestBackend()
	ctx := context.Background()

	_, err := b.EnsurePortal(ctx, portalSpec("lab-1", "lab.example.com"))
	require.NoError(t, err)

	workspaces, err := b.ListWorkspaces(ctx, "lab-1")
	require.NoError(t, err)
	assert.Empty(t, workspaces)
}

func TestPortalRules_AreNarrow(t *testing.T) {
	t.Parallel()
	name := portalName("lab-1")
	sawSecrets := false
	for _, rule := range portalRules(name, []string{"git-token", " registry-creds ", "git-token", ""}) {
		assert.NotContains(t, rule.Verbs, "*")
		assert.NotContains(t, rule.Resources, "*")
		for _, res := range rule.Resources {
			switch res {
			case "secrets":
				sawSecrets = true
				assert.Equal(t, []string{"get"}, rule.Verbs, "secrets must stay read-only and unlistable")
				// Its own config, the registry's generated credentials, and the
				// credentials the templates name — nothing else in the namespace.
				assert.ElementsMatch(t, []string{
					name + portalConfigSuffix,
					registryAuthSecretName,
					registryHtpasswdSecretName,
					"git-token",
					"registry-creds",
				}, rule.ResourceNames)
			case "configmaps":
				assert.Equal(t, []string{name + portalOutboxSuffix}, rule.ResourceNames, "only the outbox ConfigMap is reachable")
			}
		}
	}
	assert.True(t, sawSecrets)
}

// On a shared namespace, one lab's portal must not be able to read another's
// config: it holds the secret that lab's sign-in assertions are verified with.
func TestPortalRules_DoNotReachAnotherLabsConfig(t *testing.T) {
	t.Parallel()
	b, cs := newTestBackend()
	ctx := context.Background()

	spec := portalSpec("lab-1", "one.example.com")
	spec.SecretNames = []string{"git-token"}
	_, err := b.EnsurePortal(ctx, spec)
	require.NoError(t, err)
	_, err = b.EnsurePortal(ctx, portalSpec("lab-2", "two.example.com"))
	require.NoError(t, err)

	role, err := cs.RbacV1().Roles(portalTestNS).Get(ctx, portalName("lab-1"), metav1.GetOptions{})
	require.NoError(t, err)
	var readable []string
	for _, rule := range role.Rules {
		for _, res := range rule.Resources {
			if res == "secrets" {
				require.NotEmpty(t, rule.ResourceNames, "a secrets rule without names reaches the whole namespace")
				readable = append(readable, rule.ResourceNames...)
			}
		}
	}
	assert.Contains(t, readable, portalName("lab-1")+portalConfigSuffix)
	assert.Contains(t, readable, "git-token")
	assert.NotContains(t, readable, portalName("lab-2")+portalConfigSuffix)

	// A template that starts naming a new credential widens the role on reconcile,
	// and one that stops naming it narrows it again.
	spec.SecretNames = []string{"other-token"}
	_, err = b.EnsurePortal(ctx, spec)
	require.NoError(t, err)
	role, err = cs.RbacV1().Roles(portalTestNS).Get(ctx, portalName("lab-1"), metav1.GetOptions{})
	require.NoError(t, err)
	readable = nil
	for _, rule := range role.Rules {
		for _, res := range rule.Resources {
			if res == "secrets" {
				readable = append(readable, rule.ResourceNames...)
			}
		}
	}
	assert.Contains(t, readable, "other-token")
	assert.NotContains(t, readable, "git-token")
}

func TestEnsurePortal_TLSSources(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		wildcard      string
		clusterIssuer string
		wantURL       string
		wantSecret    string
		wantIssuer    string
	}{
		{name: "wildcard secret wins", wildcard: "wild", clusterIssuer: "letsencrypt", wantURL: "https://lab.example.com", wantSecret: "wild"},
		{name: "per-host certificate", clusterIssuer: "letsencrypt", wantURL: "https://lab.example.com", wantSecret: portalName("lab-1") + "-tls", wantIssuer: "letsencrypt"},
		{name: "no certificate source", wantURL: "http://lab.example.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			b, cs := newTestBackend()
			ctx := context.Background()
			spec := portalSpec("lab-1", "lab.example.com")
			spec.WildcardTLSSecret = tt.wildcard
			spec.ClusterIssuer = tt.clusterIssuer

			url, err := b.EnsurePortal(ctx, spec)
			require.NoError(t, err)
			assert.Equal(t, tt.wantURL, url)

			ing, err := cs.NetworkingV1().Ingresses(portalTestNS).Get(ctx, portalName("lab-1"), metav1.GetOptions{})
			require.NoError(t, err)
			if tt.wantSecret == "" {
				assert.Empty(t, ing.Spec.TLS)
			} else {
				require.Len(t, ing.Spec.TLS, 1)
				assert.Equal(t, tt.wantSecret, ing.Spec.TLS[0].SecretName)
			}
			assert.Equal(t, tt.wantIssuer, ing.Annotations[clusterIssuerAnnotation])

			status, err := b.PortalStatus(ctx, "lab-1")
			require.NoError(t, err)
			assert.True(t, status.Deployed)
			assert.Equal(t, tt.wantURL, status.URL)
			assert.Equal(t, spec.Image, status.Image)
		})
	}
}

// A second EnsurePortal must roll the image, follow a domain change, refresh the
// config, and leave queued outbox records alone.
func TestEnsurePortal_Reconciles(t *testing.T) {
	t.Parallel()
	b, cs := newTestBackend()
	ctx := context.Background()

	_, err := b.EnsurePortal(ctx, portalSpec("lab-1", "old.example.com"))
	require.NoError(t, err)
	require.NoError(t, b.AppendPortalOutbox(ctx, "lab-1", map[string]string{"rec-1": "{}"}))

	spec := portalSpec("lab-1", "new.example.com")
	spec.Image = "docker.io/yodamad/easylab:v2.0.0"
	spec.WildcardTLSSecret = "wild"
	spec.Config = map[string][]byte{"state.json": []byte("v2")}
	url, err := b.EnsurePortal(ctx, spec)
	require.NoError(t, err)
	assert.Equal(t, "https://new.example.com", url)

	name := portalName("lab-1")
	dep, err := cs.AppsV1().Deployments(portalTestNS).Get(ctx, name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "docker.io/yodamad/easylab:v2.0.0", dep.Spec.Template.Spec.Containers[0].Image)

	ing, err := cs.NetworkingV1().Ingresses(portalTestNS).Get(ctx, name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "new.example.com", ing.Spec.Rules[0].Host)
	require.Len(t, ing.Spec.TLS, 1)
	assert.Equal(t, "wild", ing.Spec.TLS[0].SecretName)

	config, err := b.ReadPortalConfig(ctx, "lab-1")
	require.NoError(t, err)
	assert.Equal(t, []byte("v2"), config["state.json"])

	outbox, err := cs.CoreV1().ConfigMaps(portalTestNS).Get(ctx, name+portalOutboxSuffix, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"rec-1": "{}"}, outbox.Data)
}

// Without a domain the portal follows the workspaces onto nip.io, and is the one
// case where it needs to read something outside its namespace.
func TestEnsurePortal_NipIOFallback(t *testing.T) {
	t.Parallel()
	b, cs := newTestBackend()
	ctx := context.Background()

	_, err := cs.CoreV1().Services(ingressControllerNamespace).Create(ctx, &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: ingressControllerService, Namespace: ingressControllerNamespace},
		Status: corev1.ServiceStatus{LoadBalancer: corev1.LoadBalancerStatus{
			Ingress: []corev1.LoadBalancerIngress{{IP: "203.0.113.7"}},
		}},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	spec := portalSpec("lab-1", "")
	spec.ClusterIssuer = "letsencrypt"
	url, err := b.EnsurePortal(ctx, spec)
	require.NoError(t, err)
	assert.Equal(t, "http://203.0.113.7.nip.io", url)

	role, err := cs.RbacV1().Roles(ingressControllerNamespace).Get(ctx, portalName("lab-1")+portalIngressAccessSuffix, metav1.GetOptions{})
	require.NoError(t, err)
	require.Len(t, role.Rules, 1)
	assert.Equal(t, []string{ingressControllerService}, role.Rules[0].ResourceNames)
	assert.Equal(t, []string{"get"}, role.Rules[0].Verbs)
}

func TestEnsurePortal_HostAlreadyUsedByAnotherLab(t *testing.T) {
	t.Parallel()
	b, _ := newTestBackend()
	ctx := context.Background()

	_, err := b.EnsurePortal(ctx, portalSpec("lab-1", "lab.example.com"))
	require.NoError(t, err)

	_, err = b.EnsurePortal(ctx, portalSpec("lab-2", "lab.example.com"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already used by lab lab-1")

	// The lab that owns the host can still reconcile its own portal.
	_, err = b.EnsurePortal(ctx, portalSpec("lab-1", "lab.example.com"))
	require.NoError(t, err)
}

func TestEnsurePortal_RequiresLabAndImage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		spec workspace.PortalSpec
	}{
		{name: "no lab", spec: workspace.PortalSpec{Image: "img"}},
		{name: "no image", spec: workspace.PortalSpec{LabID: "lab-1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			b, _ := newTestBackend()
			_, err := b.EnsurePortal(context.Background(), tt.spec)
			require.Error(t, err)
		})
	}
}

func TestSyncPortalConfig(t *testing.T) {
	t.Parallel()
	b, _ := newTestBackend()
	ctx := context.Background()

	// A portal that was never deployed is not an error: there is nothing to sync.
	require.NoError(t, b.SyncPortalConfig(ctx, "lab-1", map[string][]byte{"k": []byte("v")}))
	_, err := b.ReadPortalConfig(ctx, "lab-1")
	require.Error(t, err)

	_, err = b.EnsurePortal(ctx, portalSpec("lab-1", "lab.example.com"))
	require.NoError(t, err)
	require.NoError(t, b.SyncPortalConfig(ctx, "lab-1", map[string][]byte{"k": []byte("v")}))

	config, err := b.ReadPortalConfig(ctx, "lab-1")
	require.NoError(t, err)
	assert.Equal(t, map[string][]byte{"k": []byte("v")}, config)
}

func TestPortalOutbox_AppendAndDrain(t *testing.T) {
	t.Parallel()
	b, cs := newTestBackend()
	ctx := context.Background()

	_, err := b.EnsurePortal(ctx, portalSpec("lab-1", "lab.example.com"))
	require.NoError(t, err)

	require.NoError(t, b.AppendPortalOutbox(ctx, "lab-1", map[string]string{"002": "b", "001": "a"}))
	require.NoError(t, b.AppendPortalOutbox(ctx, "lab-1", map[string]string{"003": "c"}))

	// Oldest ID first; a record apply refuses stays queued.
	var seen []string
	err = b.DrainPortalOutbox(ctx, "lab-1", func(id, record string) error {
		seen = append(seen, id+"="+record)
		if id == "002" {
			return fmt.Errorf("not now")
		}
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"001=a", "002=b", "003=c"}, seen)

	outbox, err := cs.CoreV1().ConfigMaps(portalTestNS).Get(ctx, portalName("lab-1")+portalOutboxSuffix, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"002": "b"}, outbox.Data)
}

func TestDrainPortalOutbox_NoPortal(t *testing.T) {
	t.Parallel()
	b, _ := newTestBackend()
	called := false
	err := b.DrainPortalOutbox(context.Background(), "lab-1", func(string, string) error {
		called = true
		return nil
	})
	require.NoError(t, err)
	assert.False(t, called)
}

func TestAppendPortalOutbox_RefusesWhenFull(t *testing.T) {
	t.Parallel()
	b, _ := newTestBackend()
	ctx := context.Background()

	_, err := b.EnsurePortal(ctx, portalSpec("lab-1", "lab.example.com"))
	require.NoError(t, err)

	err = b.AppendPortalOutbox(ctx, "lab-1", map[string]string{"big": strings.Repeat("x", portalOutboxMaxBytes+1)})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is full")
}

func TestRemovePortal(t *testing.T) {
	t.Parallel()
	b, cs := newTestBackend()
	ctx := context.Background()

	// Removing a portal that was never deployed is fine.
	require.NoError(t, b.RemovePortal(ctx, "lab-1"))

	_, err := b.EnsurePortal(ctx, portalSpec("lab-1", "lab.example.com"))
	require.NoError(t, err)
	_, err = b.EnsurePortal(ctx, portalSpec("lab-2", "other.example.com"))
	require.NoError(t, err)

	require.NoError(t, b.RemovePortal(ctx, "lab-1"))

	status, err := b.PortalStatus(ctx, "lab-1")
	require.NoError(t, err)
	assert.False(t, status.Deployed)

	name := portalName("lab-1")
	_, err = cs.CoreV1().Secrets(portalTestNS).Get(ctx, name+portalConfigSuffix, metav1.GetOptions{})
	assert.Error(t, err)
	_, err = cs.CoreV1().ServiceAccounts(portalTestNS).Get(ctx, name, metav1.GetOptions{})
	assert.Error(t, err)
	_, err = cs.RbacV1().Roles(portalTestNS).Get(ctx, name, metav1.GetOptions{})
	assert.Error(t, err)

	// The other lab sharing the namespace keeps its portal.
	other, err := b.PortalStatus(ctx, "lab-2")
	require.NoError(t, err)
	assert.True(t, other.Deployed)
}
