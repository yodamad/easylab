package clusterscan

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// certManagerResources is the discovery answer that says cert-manager is installed.
var certManagerResources = []*metav1.APIResourceList{{
	GroupVersion: certManagerGroupVersion,
	APIResources: []metav1.APIResource{{Name: "clusterissuers", Kind: "ClusterIssuer", Namespaced: false}},
}}

type fixture struct {
	objects   []runtime.Object
	issuers   []runtime.Object
	resources []*metav1.APIResourceList
	version   *version.Info
	// allowAll grants every SelfSubjectAccessReview. The fake clientset answers
	// "not allowed" by default, which would otherwise make every fixture report
	// missing permissions.
	denyAll bool
}

func newTestScanner(t *testing.T, f fixture) (*Scanner, *fake.Clientset) {
	t.Helper()

	cs := fake.NewSimpleClientset(f.objects...)
	cs.Resources = f.resources
	disco, ok := cs.Discovery().(*fakediscovery.FakeDiscovery)
	require.True(t, ok, "expected a fake discovery client")
	if f.version != nil {
		disco.FakedServerVersion = f.version
	} else {
		disco.FakedServerVersion = &version.Info{GitVersion: "v1.31.4", Major: "1", Minor: "31"}
	}

	if !f.denyAll {
		cs.PrependReactor("create", "selfsubjectaccessreviews", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, &authv1.SelfSubjectAccessReview{
				Status: authv1.SubjectAccessReviewStatus{Allowed: true},
			}, nil
		})
	}

	return newScanner(cs, newFakeDynamic(f.issuers...)), cs
}

func newFakeDynamic(objs ...runtime.Object) dynamic.Interface {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{clusterIssuerGVR: "ClusterIssuerList"},
		objs...,
	)
}

func node(name string, labels map[string]string) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
}

func lbService(namespace, name string, labels map[string]string, ip, hostname string) *corev1.Service {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Labels: labels},
		Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
	}
	if ip != "" || hostname != "" {
		svc.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: ip, Hostname: hostname}}
	}
	return svc
}

func ingressClass(name, controller string, isDefault bool) *netv1.IngressClass {
	ic := &netv1.IngressClass{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       netv1.IngressClassSpec{Controller: controller},
	}
	if isDefault {
		ic.Annotations = map[string]string{"ingressclass.kubernetes.io/is-default-class": "true"}
	}
	return ic
}

func storageClass(name, provisioner string, isDefault bool) *storagev1.StorageClass {
	sc := &storagev1.StorageClass{
		ObjectMeta:  metav1.ObjectMeta{Name: name},
		Provisioner: provisioner,
	}
	if isDefault {
		sc.Annotations = map[string]string{"storageclass.kubernetes.io/is-default-class": "true"}
	}
	return sc
}

func namespace(name string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
}

// clusterIssuer builds an ACME ClusterIssuer as the dynamic client would see it.
func clusterIssuer(name, server, solver string, ready bool) *unstructured.Unstructured {
	status := "False"
	if ready {
		status = "True"
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": certManagerGroupVersion,
		"kind":       "ClusterIssuer",
		"metadata":   map[string]any{"name": name},
		"spec": map[string]any{
			"acme": map[string]any{
				"server":  server,
				"solvers": []any{map[string]any{solver: map[string]any{}}},
			},
		},
		"status": map[string]any{
			"conditions": []any{map[string]any{
				"type": "Ready", "status": status, "message": "issuer checked",
			}},
		},
	}}
}

// traefikCluster is the shape EasyLab provisions itself, and the baseline the
// other fixtures deviate from. It deliberately leaves out the "workshops"
// namespace so the happy path reports nothing at all.
func traefikCluster() fixture {
	return fixture{
		objects: []runtime.Object{
			node("node-1", map[string]string{"nodepool": "workspaces", "kubernetes.io/hostname": "node-1"}),
			node("node-2", map[string]string{"nodepool": "workspaces", "kubernetes.io/hostname": "node-2"}),
			node("node-3", map[string]string{"nodepool": "control", "kubernetes.io/hostname": "node-3"}),
			lbService("traefik", "traefik", map[string]string{"app.kubernetes.io/name": "traefik"}, "51.68.1.2", ""),
			ingressClass("traefik", "traefik.io/ingress-controller", true),
			storageClass("csi-cinder-high-speed", "cinder.csi.openstack.org", true),
			namespace("default"),
			namespace("traefik"),
		},
		issuers:   []runtime.Object{clusterIssuer("letsencrypt-prod", "https://acme-v02.api.letsencrypt.org/directory", "dns01", true)},
		resources: certManagerResources,
	}
}

func warningKeys(rep *Report) []string {
	keys := make([]string, 0, len(rep.Warnings))
	for _, w := range rep.Warnings {
		keys = append(keys, w.Key)
	}
	return keys
}

func TestScan_Findings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		fixture fixture
		opts    Options
		want    func(t *testing.T, rep *Report)
	}{
		{
			name:    "traefik cluster reports everything and warns about nothing",
			fixture: traefikCluster(),
			want: func(t *testing.T, rep *Report) {
				assert.Equal(t, StatusOK, rep.Cluster.Status)
				assert.Equal(t, "v1.31.4", rep.Cluster.Version)
				assert.Equal(t, 3, rep.Nodes.Count)
				assert.Equal(t, "traefik", rep.Ingress.Namespace)
				assert.Equal(t, "traefik", rep.Ingress.Service)
				assert.Equal(t, "traefik", rep.Ingress.Class)
				assert.Equal(t, flavourTraefik, rep.Ingress.Flavour)
				assert.Equal(t, "51.68.1.2", rep.Ingress.ExternalIP)
				assert.True(t, rep.CertManager.Installed)
				require.Len(t, rep.Issuers.Items, 1)
				assert.True(t, rep.Issuers.Items[0].Usable())
				assert.Equal(t, "csi-cinder-high-speed", rep.Storage.Default)
				assert.False(t, rep.Storage.NodeLocal)
				assert.Empty(t, warningKeys(rep), "a stock cluster should produce no warnings")
			},
		},
		{
			name: "ingress-nginx is reported with its own class and warned about",
			fixture: func() fixture {
				f := traefikCluster()
				f.objects = []runtime.Object{
					lbService("ingress-nginx", "ingress-nginx-controller", map[string]string{
						"app.kubernetes.io/name":      "ingress-nginx",
						"app.kubernetes.io/component": "controller",
					}, "51.68.1.2", ""),
					ingressClass("nginx", "k8s.io/ingress-nginx", true),
					storageClass("csi", "cinder.csi.openstack.org", true),
				}
				return f
			}(),
			want: func(t *testing.T, rep *Report) {
				assert.Equal(t, "nginx", rep.Ingress.Class)
				assert.Equal(t, "ingress-nginx", rep.Ingress.Flavour)
				assert.Equal(t, "ingress-nginx", rep.Ingress.Namespace)
				assert.Equal(t, "ingress-nginx-controller", rep.Ingress.Service)
				assert.Contains(t, warningKeys(rep), WarnIngressNonTraefik)
			},
		},
		{
			name: "hostname-only load balancer is reported and warned about",
			fixture: func() fixture {
				f := traefikCluster()
				f.objects = []runtime.Object{
					lbService("traefik", "traefik", map[string]string{"app.kubernetes.io/name": "traefik"},
						"", "abc123.eu-west-1.elb.amazonaws.com"),
					ingressClass("traefik", "traefik.io/ingress-controller", true),
					storageClass("gp3", "ebs.csi.aws.com", true),
				}
				return f
			}(),
			want: func(t *testing.T, rep *Report) {
				assert.Empty(t, rep.Ingress.ExternalIP)
				assert.Equal(t, "abc123.eu-west-1.elb.amazonaws.com", rep.Ingress.ExternalHost)
				assert.Contains(t, warningKeys(rep), WarnIngressHostnameOnly)
			},
		},
		{
			name: "node-local default storage class is warned about",
			fixture: func() fixture {
				f := traefikCluster()
				f.objects = append(f.objects[:len(f.objects)-3], storageClass("local-path", "rancher.io/local-path", true))
				return f
			}(),
			want: func(t *testing.T, rep *Report) {
				assert.Equal(t, "local-path", rep.Storage.Default)
				assert.True(t, rep.Storage.NodeLocal)
				assert.Contains(t, warningKeys(rep), WarnStorageNodeLocal)
			},
		},
		{
			name: "no default storage class is warned about",
			fixture: func() fixture {
				f := traefikCluster()
				f.objects = append(f.objects[:len(f.objects)-3], storageClass("slow", "cinder.csi.openstack.org", false))
				return f
			}(),
			want: func(t *testing.T, rep *Report) {
				assert.Empty(t, rep.Storage.Default)
				assert.Contains(t, warningKeys(rep), WarnStorageAbsent)
			},
		},
		{
			name: "two default storage classes are warned about",
			fixture: func() fixture {
				f := traefikCluster()
				f.objects = append(f.objects[:len(f.objects)-3],
					storageClass("a-fast", "cinder.csi.openstack.org", true),
					storageClass("b-fast", "cinder.csi.openstack.org", true),
				)
				return f
			}(),
			want: func(t *testing.T, rep *Report) {
				assert.Contains(t, warningKeys(rep), WarnStorageMultipleDefaults)
			},
		},
		{
			name: "cert-manager namespace comes from its validating webhook",
			fixture: func() fixture {
				f := traefikCluster()
				f.objects = append(f.objects, &admissionv1.ValidatingWebhookConfiguration{
					ObjectMeta: metav1.ObjectMeta{Name: "cert-manager-webhook"},
					Webhooks: []admissionv1.ValidatingWebhook{{
						Name: "webhook.cert-manager.io",
						ClientConfig: admissionv1.WebhookClientConfig{
							Service: &admissionv1.ServiceReference{Namespace: "certs", Name: "cert-manager-webhook"},
						},
					}},
				})
				return f
			}(),
			want: func(t *testing.T, rep *Report) {
				assert.True(t, rep.CertManager.Installed)
				assert.Equal(t, "certs", rep.CertManager.Namespace)
				assert.Equal(t, "webhook", rep.CertManager.NamespaceFrom)
			},
		},
		{
			name: "cert-manager namespace falls back to a labelled deployment",
			fixture: func() fixture {
				f := traefikCluster()
				f.objects = append(f.objects, &appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "tls-system",
						Name:      "cert-manager",
						Labels:    map[string]string{"app.kubernetes.io/name": "cert-manager"},
					},
				})
				return f
			}(),
			want: func(t *testing.T, rep *Report) {
				assert.Equal(t, "tls-system", rep.CertManager.Namespace)
				assert.Equal(t, "deployment", rep.CertManager.NamespaceFrom)
			},
		},
		{
			name: "issuer flags are read and a stalled issuer is warned about",
			fixture: func() fixture {
				f := traefikCluster()
				f.issuers = []runtime.Object{
					clusterIssuer("http-only", "https://acme-v02.api.letsencrypt.org/directory", "http01", false),
				}
				return f
			}(),
			want: func(t *testing.T, rep *Report) {
				require.Len(t, rep.Issuers.Items, 1)
				issuer := rep.Issuers.Items[0]
				assert.Equal(t, "acme", issuer.Kind)
				assert.False(t, issuer.HasDNS01)
				assert.True(t, issuer.HasHTTP01)
				assert.False(t, issuer.Ready)
				assert.False(t, issuer.Usable())
				assert.Contains(t, warningKeys(rep), WarnIssuerNoDNS01)
				assert.Contains(t, warningKeys(rep), WarnIssuerNotReady)
			},
		},
		{
			name: "staging-only issuers are warned about",
			fixture: func() fixture {
				f := traefikCluster()
				f.issuers = []runtime.Object{
					clusterIssuer("letsencrypt-staging", "https://acme-staging-v02.api.letsencrypt.org/directory", "dns01", true),
				}
				return f
			}(),
			want: func(t *testing.T, rep *Report) {
				require.Len(t, rep.Issuers.Items, 1)
				assert.True(t, rep.Issuers.Items[0].Staging)
				assert.Contains(t, warningKeys(rep), WarnIssuerStagingOnly)
			},
		},
		{
			name: "issuers are skipped, not reported absent, without cert-manager",
			fixture: func() fixture {
				f := traefikCluster()
				f.resources = nil
				f.issuers = nil
				return f
			}(),
			want: func(t *testing.T, rep *Report) {
				assert.False(t, rep.CertManager.Installed)
				assert.Equal(t, StatusAbsent, rep.CertManager.Status)
				assert.Equal(t, StatusSkipped, rep.Issuers.Status,
					"issuers were never probed, which is not the same as there being none")
				assert.Contains(t, warningKeys(rep), WarnCertManagerAbsent)
				assert.NotContains(t, warningKeys(rep), WarnIssuerNoDNS01)
			},
		},
		{
			name: "an existing workspace namespace is reported",
			fixture: func() fixture {
				f := traefikCluster()
				f.objects = append(f.objects, namespace("workshops"))
				return f
			}(),
			want: func(t *testing.T, rep *Report) {
				assert.True(t, rep.Namespaces.WorkspaceExists)
				assert.Equal(t, "workshops", rep.Namespaces.WorkspaceName)
				assert.Contains(t, warningKeys(rep), WarnNamespaceExists)
			},
		},
		{
			name:    "a custom workspace namespace is the one checked",
			fixture: traefikCluster(),
			opts:    Options{WorkspaceNamespace: "devoxx"},
			want: func(t *testing.T, rep *Report) {
				assert.Equal(t, "devoxx", rep.Namespaces.WorkspaceName)
				assert.False(t, rep.Namespaces.WorkspaceExists)
			},
		},
		{
			name:    "an empty cluster reports absent everywhere without panicking",
			fixture: fixture{},
			want: func(t *testing.T, rep *Report) {
				require.NotNil(t, rep)
				assert.Equal(t, StatusOK, rep.Cluster.Status)
				assert.Equal(t, 0, rep.Nodes.Count)
				assert.Equal(t, StatusAbsent, rep.Ingress.Status)
				assert.False(t, rep.CertManager.Installed)
				assert.Equal(t, StatusSkipped, rep.Issuers.Status)
				assert.Contains(t, warningKeys(rep), WarnStorageAbsent)
			},
		},
		{
			name: "an old cluster is warned about",
			fixture: func() fixture {
				f := traefikCluster()
				f.version = &version.Info{GitVersion: "v1.21.0", Major: "1", Minor: "21"}
				return f
			}(),
			want: func(t *testing.T, rep *Report) {
				assert.Contains(t, warningKeys(rep), "cluster.version_old")
			},
		},
		{
			name: "a single-node cluster is warned about",
			fixture: func() fixture {
				f := traefikCluster()
				f.objects = append([]runtime.Object{node("solo", nil)}, f.objects[3:]...)
				return f
			}(),
			want: func(t *testing.T, rep *Report) {
				assert.Equal(t, 1, rep.Nodes.Count)
				assert.Contains(t, warningKeys(rep), WarnNodesSingle)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s, _ := newTestScanner(t, tt.fixture)
			rep, _ := Scan(context.Background(), s, tt.opts)
			require.NotNil(t, rep)
			tt.want(t, rep)
		})
	}
}

// TestScan_ForbiddenProbesDegrade is the decision that a denied check must never
// take the rest of the scan down with it.
func TestScan_ForbiddenProbesDegrade(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		verb     string
		resource string
		assert   func(t *testing.T, rep *Report)
	}{
		{
			name: "nodes", verb: "list", resource: "nodes",
			assert: func(t *testing.T, rep *Report) {
				assert.Equal(t, StatusForbidden, rep.Nodes.Status)
				assert.Contains(t, rep.Nodes.Detail, "couldn't check")
				assert.Equal(t, StatusOK, rep.Storage.Status, "storage must still be reported")
				assert.True(t, rep.CertManager.Installed, "cert-manager must still be reported")
			},
		},
		{
			name: "storage classes", verb: "list", resource: "storageclasses",
			assert: func(t *testing.T, rep *Report) {
				assert.Equal(t, StatusForbidden, rep.Storage.Status)
				assert.Equal(t, StatusOK, rep.Nodes.Status)
				assert.Equal(t, StatusOK, rep.Ingress.Status)
			},
		},
		{
			name: "ingress classes", verb: "list", resource: "ingressclasses",
			assert: func(t *testing.T, rep *Report) {
				assert.Equal(t, StatusForbidden, rep.Ingress.ClassesProbe.Status)
				// The Service lookup is a separate permission and still succeeded.
				assert.Equal(t, StatusOK, rep.Ingress.Status)
				assert.Equal(t, "traefik", rep.Ingress.Service)
			},
		},
		{
			name: "namespaces fall back to getting just this lab's one", verb: "list", resource: "namespaces",
			assert: func(t *testing.T, rep *Report) {
				// Listing was denied, so the names are unknown, but the question that
				// matters was still answered by a direct get.
				assert.Empty(t, rep.Namespaces.Names)
				assert.Equal(t, StatusOK, rep.Nodes.Status)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s, cs := newTestScanner(t, traefikCluster())
			cs.PrependReactor(tt.verb, tt.resource, func(k8stesting.Action) (bool, runtime.Object, error) {
				return true, nil, apierrors.NewForbidden(
					schema.GroupResource{Resource: tt.resource}, "", errors.New("nope"))
			})

			rep, errs := Scan(context.Background(), s, Options{})
			require.NotNil(t, rep, "a denied probe must not void the report")
			assert.Equal(t, StatusOK, rep.Cluster.Status)
			tt.assert(t, rep)
			assert.NotEmpty(t, errs, "the raw error should come back for the caller to log")
		})
	}
}

func TestScan_ForbiddenServiceListFallsBackToWellKnownNames(t *testing.T) {
	t.Parallel()

	s, cs := newTestScanner(t, traefikCluster())
	cs.PrependReactor("list", "services", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "services"}, "", errors.New("nope"))
	})

	rep, _ := Scan(context.Background(), s, Options{})
	require.NotNil(t, rep)
	// traefik/traefik is a conventional location, so a direct get still finds it.
	assert.Equal(t, "traefik", rep.Ingress.Namespace)
	assert.Equal(t, "traefik", rep.Ingress.Service)
	assert.Equal(t, "51.68.1.2", rep.Ingress.ExternalIP)
}

func TestScan_MissingPermissionsAreReported(t *testing.T) {
	t.Parallel()

	f := traefikCluster()
	f.denyAll = true // the fake answers "not allowed" with no reactor
	s, _ := newTestScanner(t, f)

	rep, _ := Scan(context.Background(), s, Options{})
	require.NotNil(t, rep)
	assert.Equal(t, StatusOK, rep.Permissions.Status)
	assert.NotEmpty(t, rep.Permissions.Missing)
	assert.Contains(t, warningKeys(rep), WarnPermissionsMissing)
}

func TestScan_TimeoutDegrades(t *testing.T) {
	t.Parallel()

	s, cs := newTestScanner(t, traefikCluster())
	cs.PrependReactor("list", "nodes", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, context.DeadlineExceeded
	})

	rep, errs := Scan(context.Background(), s, Options{ProbeTimeout: 10 * time.Millisecond})
	require.NotNil(t, rep)
	assert.Equal(t, StatusTimeout, rep.Nodes.Status)
	assert.Equal(t, StatusOK, rep.Storage.Status, "a slow probe must not stall the others")
	assert.NotEmpty(t, errs)
}

// TestClassify pins the guarantee that no cluster address or raw error text can
// reach a Report, which is what makes a Report safe to send to a browser.
func TestClassify(t *testing.T) {
	t.Parallel()

	secret := "https://10.0.0.1:6443 connection refused for user token abc123"

	tests := []struct {
		name       string
		err        error
		wantStatus Status
		wantLogged bool
	}{
		{"nil is ok", nil, StatusOK, false},
		{"forbidden", apierrors.NewForbidden(schema.GroupResource{Resource: "nodes"}, "n", errors.New("x")), StatusForbidden, true},
		{"unauthorized", apierrors.NewUnauthorized("bad token"), StatusForbidden, true},
		{"not found is absent, not an error", apierrors.NewNotFound(schema.GroupResource{Resource: "nodes"}, "n"), StatusAbsent, false},
		{"server timeout", apierrors.NewServerTimeout(schema.GroupResource{Resource: "nodes"}, "list", 1), StatusTimeout, true},
		{"deadline exceeded", context.DeadlineExceeded, StatusTimeout, true},
		{"anything else", errors.New(secret), StatusError, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			probe, logged := classify(tt.err)
			assert.Equal(t, tt.wantStatus, probe.Status)
			assert.Equal(t, tt.wantLogged, logged != nil)
			assert.NotContains(t, probe.Detail, "10.0.0.1")
			assert.NotContains(t, probe.Detail, "abc123")
		})
	}
}

func TestNodeLabelSuggestions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		nodes    []corev1.Node
		wantKeys []string
	}{
		{
			name:     "no nodes",
			nodes:    nil,
			wantKeys: []string{},
		},
		{
			name: "pool labels are kept and noise is dropped",
			nodes: []corev1.Node{
				*node("a", map[string]string{
					"nodepool":                         "workspaces",
					"kubernetes.io/hostname":           "a",
					"kubernetes.io/os":                 "linux",
					"kubernetes.io/arch":               "amd64",
					"beta.kubernetes.io/instance-type": "b2-7",
					"node.kubernetes.io/instance-type": "b2-7",
					"topology.kubernetes.io/zone":      "gra",
				}),
				*node("b", map[string]string{
					"nodepool":                    "control",
					"kubernetes.io/hostname":      "b",
					"kubernetes.io/os":            "linux",
					"topology.kubernetes.io/zone": "gra",
				}),
			},
			// zone has one value (so it sorts first), nodepool has two.
			wantKeys: []string{"topology.kubernetes.io/zone", "nodepool"},
		},
		{
			name: "a key unique per node is dropped above two nodes",
			nodes: []corev1.Node{
				*node("a", map[string]string{"machine-id": "1", "pool": "x"}),
				*node("b", map[string]string{"machine-id": "2", "pool": "x"}),
				*node("c", map[string]string{"machine-id": "3", "pool": "y"}),
			},
			wantKeys: []string{"pool"},
		},
		{
			// On two nodes a real two-pool label is indistinguishable from a
			// per-node one, so it is kept rather than silently hidden.
			name: "a two-value key is kept on a two-node cluster",
			nodes: []corev1.Node{
				*node("a", map[string]string{"pool": "control"}),
				*node("b", map[string]string{"pool": "workspaces"}),
			},
			wantKeys: []string{"pool"},
		},
		{
			name: "a single node keeps its labels, since nothing is unique-per-node yet",
			nodes: []corev1.Node{
				*node("a", map[string]string{"pool": "x"}),
			},
			wantKeys: []string{"pool"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := nodeLabelSuggestions(tt.nodes)
			keys := make([]string, 0, len(got))
			for _, l := range got {
				keys = append(keys, l.Key)
			}
			assert.Equal(t, tt.wantKeys, keys)

			// Order must be stable across runs, or the suggestions jump around.
			again := nodeLabelSuggestions(tt.nodes)
			assert.Equal(t, got, again)
		})
	}
}

func TestNodeLabelSuggestions_CapsValues(t *testing.T) {
	t.Parallel()

	nodes := make([]corev1.Node, 0, 40)
	for i := 0; i < 40; i++ {
		// Two keys: one with many values, one shared, so neither is unique-per-node.
		nodes = append(nodes, *node("n"+string(rune('a'+i%26))+string(rune('a'+i/26)), map[string]string{
			"many":   "v" + string(rune('a'+i)),
			"shared": "same",
		}))
	}
	got := nodeLabelSuggestions(nodes)
	for _, l := range got {
		assert.LessOrEqual(t, len(l.Values), maxLabelValues, "key %q", l.Key)
	}
}

func TestPermissionChecks(t *testing.T) {
	t.Parallel()

	var asked []authv1.ResourceAttributes
	var mu = make(chan struct{}, 1)
	mu <- struct{}{}

	f := traefikCluster()
	f.denyAll = true
	s, cs := newTestScanner(t, f)
	cs.PrependReactor("create", "selfsubjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		create, ok := action.(k8stesting.CreateAction)
		require.True(t, ok)
		review, ok := create.GetObject().(*authv1.SelfSubjectAccessReview)
		require.True(t, ok)

		<-mu
		asked = append(asked, *review.Spec.ResourceAttributes)
		mu <- struct{}{}

		// Deny exactly one, to prove Missing is derived from the answers.
		allowed := review.Spec.ResourceAttributes.Resource != "daemonsets"
		return true, &authv1.SelfSubjectAccessReview{
			Status: authv1.SubjectAccessReviewStatus{Allowed: allowed, Reason: "test"},
		}, nil
	})

	rep, _ := Scan(context.Background(), s, Options{WorkspaceNamespace: "devoxx"})
	require.NotNil(t, rep)

	assert.Equal(t, StatusOK, rep.Permissions.Status)
	require.Len(t, rep.Permissions.Missing, 1)
	assert.Equal(t, "create daemonsets in devoxx", rep.Permissions.Missing[0])

	// The workspace namespace is the one asked about, and the portal's Role check
	// targets the ingress controller's namespace rather than a hardcoded default.
	var sawWorkspaceNS, sawIngressRole, sawClusterScoped bool
	for _, a := range asked {
		if a.Namespace == "devoxx" && a.Resource == "deployments" {
			sawWorkspaceNS = true
		}
		if a.Namespace == "traefik" && a.Resource == "roles" {
			sawIngressRole = true
		}
		if a.Namespace == "" && a.Resource == "nodes" && a.Verb == "list" {
			sawClusterScoped = true
		}
	}
	assert.True(t, sawWorkspaceNS, "asked: %+v", asked)
	assert.True(t, sawIngressRole, "asked: %+v", asked)
	assert.True(t, sawClusterScoped, "asked: %+v", asked)
}

func TestClusterIssuerInfo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		obj  map[string]any
		want ClusterIssuerInfo
	}{
		{
			name: "acme with a dns01 solver, ready",
			obj:  clusterIssuer("prod", "https://acme-v02.api.letsencrypt.org/directory", "dns01", true).Object,
			want: ClusterIssuerInfo{
				Name: "prod", Kind: "acme",
				ACMEServer: "https://acme-v02.api.letsencrypt.org/directory",
				HasDNS01:   true, Ready: true, Message: "issuer checked",
			},
		},
		{
			name: "staging is detected from the server url",
			obj:  clusterIssuer("stg", "https://acme-staging-v02.api.letsencrypt.org/directory", "dns01", true).Object,
			want: ClusterIssuerInfo{
				Name: "stg", Kind: "acme",
				ACMEServer: "https://acme-staging-v02.api.letsencrypt.org/directory",
				Staging:    true, HasDNS01: true, Ready: true, Message: "issuer checked",
			},
		},
		{
			name: "a self-signed issuer has no acme fields",
			obj: map[string]any{
				"metadata": map[string]any{"name": "selfsigned"},
				"spec":     map[string]any{"selfSigned": map[string]any{}},
			},
			want: ClusterIssuerInfo{Name: "selfsigned", Kind: "selfSigned"},
		},
		{
			name: "a bare object does not panic",
			obj:  map[string]any{},
			want: ClusterIssuerInfo{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, clusterIssuerInfo(tt.obj))
		})
	}
}

func TestNew_RejectsBadKubeconfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		kubeconfig string
	}{
		{"empty", ""},
		{"whitespace", "   \n"},
		{"not yaml at all", "this is not a kubeconfig"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s, err := New(tt.kubeconfig)
			require.Error(t, err)
			assert.Nil(t, s)
			// The error reaches a log line, so it must not quote the input back.
			assert.NotContains(t, err.Error(), "this is not a kubeconfig")
		})
	}
}

func TestParseMinor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want int
	}{
		{"31", 31},
		{"30+", 30},
		{"", 0},
		{"abc", 0},
		{"24", 24},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, parseMinor(tt.in))
		})
	}
}
