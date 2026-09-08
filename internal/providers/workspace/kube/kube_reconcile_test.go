package kube

import (
	"context"
	"errors"
	"testing"

	"easylab/internal/providers/workspace"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	netv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"
)

// seedIngress creates a workspace Ingress in whatever (possibly stale) shape a past
// lab configuration would have left behind.
func seedIngress(t *testing.T, b *Backend, name, host, class, tlsSecret, issuer string) {
	t.Helper()
	annotations := map[string]string{}
	if issuer != "" {
		annotations[clusterIssuerAnnotation] = issuer
	}
	pathType := netv1.PathTypePrefix
	ing := &netv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: b.namespace, Annotations: annotations},
		Spec: netv1.IngressSpec{
			IngressClassName: &class,
			Rules: []netv1.IngressRule{{
				Host: host,
				IngressRuleValue: netv1.IngressRuleValue{
					HTTP: &netv1.HTTPIngressRuleValue{Paths: []netv1.HTTPIngressPath{{
						Path: "/", PathType: &pathType,
						Backend: netv1.IngressBackend{Service: &netv1.IngressServiceBackend{
							Name: name, Port: netv1.ServiceBackendPort{Number: 80},
						}},
					}}},
				},
			}},
		},
	}
	if tlsSecret != "" {
		ing.Spec.TLS = []netv1.IngressTLS{{Hosts: []string{host}, SecretName: tlsSecret}}
	}
	_, err := b.client.NetworkingV1().Ingresses(b.namespace).Create(context.Background(), ing, metav1.CreateOptions{})
	require.NoError(t, err)
}

func getIngress(t *testing.T, b *Backend, name string) *netv1.Ingress {
	t.Helper()
	ing, err := b.client.NetworkingV1().Ingresses(b.namespace).Get(context.Background(), name, metav1.GetOptions{})
	require.NoError(t, err)
	return ing
}

func TestReconcileIngressTLS(t *testing.T) {
	const (
		name   = "ws-1"
		domain = "lab.example.com"
		host   = name + "." + domain
	)

	tests := []struct {
		name string
		// the Ingress as it exists on the cluster
		seedHost, seedClass, seedSecret, seedIssuer string
		// the lab's current configuration
		spec workspace.Spec
		// expectations after reconcile
		wantSecret, wantIssuer, wantClass string
		wantUpdate                        bool
	}{
		{
			name:       "adopts the wildcard secret when the lab gained a DNS provider",
			seedHost:   host,
			seedClass:  workspaceIngressClass,
			seedSecret: name + "-tls",
			seedIssuer: "letsencrypt-prod",
			spec:       workspace.Spec{Domain: domain, WildcardTLSSecret: "easylab-wildcard-tls"},
			wantSecret: "easylab-wildcard-tls",
			wantIssuer: "",
			wantClass:  workspaceIngressClass,
			wantUpdate: true,
		},
		{
			name:       "adds a missing cluster-issuer annotation",
			seedHost:   host,
			seedClass:  workspaceIngressClass,
			spec:       workspace.Spec{Domain: domain, ClusterIssuer: "letsencrypt-prod"},
			wantSecret: name + "-tls",
			wantIssuer: "letsencrypt-prod",
			wantClass:  workspaceIngressClass,
			wantUpdate: true,
		},
		{
			name:       "follows a renamed cluster issuer",
			seedHost:   host,
			seedClass:  workspaceIngressClass,
			seedSecret: name + "-tls",
			seedIssuer: "letsencrypt-prod",
			spec:       workspace.Spec{Domain: domain, ClusterIssuer: "easylab-staging"},
			wantSecret: name + "-tls",
			wantIssuer: "easylab-staging",
			wantClass:  workspaceIngressClass,
			wantUpdate: true,
		},
		{
			name:       "corrects a pre-Traefik ingress class",
			seedHost:   host,
			seedClass:  "nginx",
			seedSecret: "easylab-wildcard-tls",
			spec:       workspace.Spec{Domain: domain, WildcardTLSSecret: "easylab-wildcard-tls"},
			wantSecret: "easylab-wildcard-tls",
			wantClass:  workspaceIngressClass,
			wantUpdate: true,
		},
		{
			name:       "leaves an already-correct ingress untouched",
			seedHost:   host,
			seedClass:  workspaceIngressClass,
			seedSecret: "easylab-wildcard-tls",
			spec:       workspace.Spec{Domain: domain, WildcardTLSSecret: "easylab-wildcard-tls"},
			wantSecret: "easylab-wildcard-tls",
			wantClass:  workspaceIngressClass,
			wantUpdate: false,
		},
		{
			name:       "leaves a nip.io workspace on its own URL",
			seedHost:   name + ".1.2.3.4.nip.io",
			seedClass:  workspaceIngressClass,
			spec:       workspace.Spec{Domain: domain, WildcardTLSSecret: "easylab-wildcard-tls"},
			wantSecret: "",
			wantClass:  workspaceIngressClass,
			wantUpdate: false,
		},
		{
			name:       "does nothing when the lab has no certificate source",
			seedHost:   host,
			seedClass:  workspaceIngressClass,
			spec:       workspace.Spec{Domain: domain},
			wantSecret: "",
			wantClass:  workspaceIngressClass,
			wantUpdate: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, cs := newTestBackend()
			seedIngress(t, b, name, tt.seedHost, tt.seedClass, tt.seedSecret, tt.seedIssuer)
			cs.ClearActions()

			require.NoError(t, b.reconcileIngressTLS(context.Background(), name, tt.spec))

			updated := false
			for _, a := range cs.Actions() {
				if a.GetVerb() == "update" && a.GetResource().Resource == "ingresses" {
					updated = true
				}
			}
			assert.Equal(t, tt.wantUpdate, updated, "unexpected write behaviour")

			ing := getIngress(t, b, name)
			if tt.wantSecret == "" {
				assert.Empty(t, ing.Spec.TLS)
			} else {
				require.Len(t, ing.Spec.TLS, 1)
				assert.Equal(t, tt.wantSecret, ing.Spec.TLS[0].SecretName)
				assert.Equal(t, []string{tt.seedHost}, ing.Spec.TLS[0].Hosts)
			}
			assert.Equal(t, tt.wantIssuer, ing.Annotations[clusterIssuerAnnotation])
			require.NotNil(t, ing.Spec.IngressClassName)
			assert.Equal(t, tt.wantClass, *ing.Spec.IngressClassName)
			// The student's URL must never move.
			assert.Equal(t, tt.seedHost, ing.Spec.Rules[0].Host)
		})
	}
}

func TestReconcileIngressTLS_NoopWithoutTarget(t *testing.T) {
	tests := []struct {
		name string
		spec workspace.Spec
	}{
		{name: "lab has no domain", spec: workspace.Spec{WildcardTLSSecret: "easylab-wildcard-tls"}},
		{name: "ingress does not exist", spec: workspace.Spec{Domain: "lab.example.com", WildcardTLSSecret: "easylab-wildcard-tls"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, _ := newTestBackend()
			assert.NoError(t, b.reconcileIngressTLS(context.Background(), "ws-missing", tt.spec))
		})
	}
}

// A cluster that refuses the Ingress update must not stop a student reaching their
// IDE — EnsureWorkspace treats the reconcile as best-effort.
func TestEnsureWorkspace_ReturnsWorkspaceWhenReconcileFails(t *testing.T) {
	const domain = "lab.example.com"
	b, cs := newTestBackend()
	spec := workspace.Spec{
		LabID: "lab-1", Owner: "alice", Template: "docker",
		Domain: domain, WildcardTLSSecret: "easylab-wildcard-tls", Token: "tok",
	}
	name := workspaceName(spec.LabID, spec.Owner, spec.Template)

	_, err := b.EnsureWorkspace(context.Background(), spec)
	require.NoError(t, err)

	// Put the Ingress into the stale shape this change exists to repair...
	ing := getIngress(t, b, name)
	ing.Spec.TLS = nil
	_, err = b.client.NetworkingV1().Ingresses(b.namespace).Update(context.Background(), ing, metav1.UpdateOptions{})
	require.NoError(t, err)

	// ...then make the repair itself impossible.
	cs.PrependReactor("update", "ingresses", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("ingresses.networking.k8s.io is forbidden")
	})

	ws, err := b.EnsureWorkspace(context.Background(), spec)
	require.NoError(t, err, "a failed TLS reconcile must not block the workspace")
	assert.Equal(t, name, ws.Name)
	assert.Empty(t, getIngress(t, b, name).Spec.TLS, "the update was rejected, so the ingress stays stale")
}
