package kube

import (
	"context"
	"testing"

	"easylab/internal/providers/workspace"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestIngressClassFor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		spec workspace.Spec
		want string
	}{
		{"unset falls back to what EasyLab installs", workspace.Spec{}, workspaceIngressClass},
		{"whitespace is treated as unset", workspace.Spec{IngressClass: "   "}, workspaceIngressClass},
		{"an explicit class is honoured", workspace.Spec{IngressClass: "nginx"}, "nginx"},
		{"an explicit class is trimmed", workspace.Spec{IngressClass: " haproxy\n"}, "haproxy"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, ingressClassFor(tt.spec))
		})
	}
}

func TestIngressLocationFor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                       string
		spec                       workspace.Spec
		wantNamespace, wantService string
	}{
		{
			name:          "unset falls back to Traefik's conventional location",
			spec:          workspace.Spec{},
			wantNamespace: ingressControllerNamespace,
			wantService:   ingressControllerService,
		},
		{
			name:          "a reused controller is found where the lab says",
			spec:          workspace.Spec{IngressNamespace: "ingress-nginx", IngressService: "ingress-nginx-controller"},
			wantNamespace: "ingress-nginx",
			wantService:   "ingress-nginx-controller",
		},
		{
			name:          "each half falls back independently",
			spec:          workspace.Spec{IngressNamespace: "kube-system"},
			wantNamespace: "kube-system",
			wantService:   ingressControllerService,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			namespace, service := ingressLocationFor(tt.spec)
			assert.Equal(t, tt.wantNamespace, namespace)
			assert.Equal(t, tt.wantService, service)
		})
	}
}

func TestCreateIngress_UsesSpecIngressClass(t *testing.T) {
	tests := []struct {
		name      string
		specClass string
		wantClass string
	}{
		{"a lab that names no class gets Traefik", "", workspaceIngressClass},
		{"a lab on ingress-nginx gets its own class", "nginx", "nginx"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, _ := newTestBackend()
			spec := workspace.Spec{
				Domain:        "lab.example.com",
				ClusterIssuer: "letsencrypt-prod",
				IngressClass:  tt.specClass,
			}
			require.NoError(t, b.createIngress(context.Background(), "ws-1", nil, spec))

			ing := getIngress(t, b, "ws-1")
			require.NotNil(t, ing.Spec.IngressClassName)
			assert.Equal(t, tt.wantClass, *ing.Spec.IngressClassName)
		})
	}
}

// TestReconcileIngressTLS_KeepsSpecIngressClass is the regression that matters
// most in this change. The reconciler rewrites the ingress class on every
// EnsureWorkspace to repair labs created before the nginx-to-Traefik switch. Left
// pointing at the built-in default, it would silently stamp "traefik" over a
// reused class on the first student request and break routing with no error
// anywhere, because the Ingress update itself succeeds.
func TestReconcileIngressTLS_KeepsSpecIngressClass(t *testing.T) {
	const (
		name   = "ws-1"
		domain = "lab.example.com"
		host   = name + "." + domain
	)

	tests := []struct {
		name      string
		seedClass string
		specClass string
		wantClass string
	}{
		{
			name:      "a reused class survives reconciliation",
			seedClass: "nginx",
			specClass: "nginx",
			wantClass: "nginx",
		},
		{
			name:      "a lab that names no class still gets the legacy repair",
			seedClass: "nginx",
			specClass: "",
			wantClass: workspaceIngressClass,
		},
		{
			name:      "a class corrected on the lab is applied to an existing ingress",
			seedClass: workspaceIngressClass,
			specClass: "haproxy",
			wantClass: "haproxy",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, _ := newTestBackend()
			seedIngress(t, b, name, host, tt.seedClass, "easylab-wildcard-tls", "")

			spec := workspace.Spec{
				Domain:            domain,
				WildcardTLSSecret: "easylab-wildcard-tls",
				IngressClass:      tt.specClass,
			}
			// Twice, because the damage this guards against only appeared on a
			// second pass over an Ingress that was already correct.
			require.NoError(t, b.reconcileIngressTLS(context.Background(), name, spec))
			require.NoError(t, b.reconcileIngressTLS(context.Background(), name, spec))

			ing := getIngress(t, b, name)
			require.NotNil(t, ing.Spec.IngressClassName)
			assert.Equal(t, tt.wantClass, *ing.Spec.IngressClassName)
			// The student's URL must never move.
			assert.Equal(t, host, ing.Spec.Rules[0].Host)
		})
	}
}

func TestIngressIP_UsesSpecLocation(t *testing.T) {
	tests := []struct {
		name        string
		serviceNS   string
		serviceName string
		ip          string
		hostname    string
		spec        workspace.Spec
		wantIP      string
	}{
		{
			name:        "the conventional location is read when the lab names none",
			serviceNS:   ingressControllerNamespace,
			serviceName: ingressControllerService,
			ip:          "51.68.1.2",
			spec:        workspace.Spec{},
			wantIP:      "51.68.1.2",
		},
		{
			name:        "a reused controller elsewhere is found",
			serviceNS:   "ingress-nginx",
			serviceName: "ingress-nginx-controller",
			ip:          "51.68.9.9",
			spec:        workspace.Spec{IngressNamespace: "ingress-nginx", IngressService: "ingress-nginx-controller"},
			wantIP:      "51.68.9.9",
		},
		{
			name:        "a controller the lab does not point at is not found",
			serviceNS:   "ingress-nginx",
			serviceName: "ingress-nginx-controller",
			ip:          "51.68.9.9",
			spec:        workspace.Spec{},
			wantIP:      "",
		},
		{
			name:        "a hostname-only load balancer yields no IP",
			serviceNS:   ingressControllerNamespace,
			serviceName: ingressControllerService,
			hostname:    "abc.elb.amazonaws.com",
			spec:        workspace.Spec{},
			wantIP:      "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, cs := newTestBackend()
			svc := &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Namespace: tt.serviceNS, Name: tt.serviceName},
				Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
			}
			svc.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: tt.ip, Hostname: tt.hostname}}
			_, err := cs.CoreV1().Services(tt.serviceNS).Create(context.Background(), svc, metav1.CreateOptions{})
			require.NoError(t, err)

			assert.Equal(t, tt.wantIP, b.ingressIP(context.Background(), tt.spec))
		})
	}
}

// TestResolveRouting_NipIoFollowsSpecLocation proves the nip.io fallback uses the
// lab's own controller location, which is what makes a quick-start lab work on a
// cluster whose controller is not where EasyLab would have put it.
func TestResolveRouting_NipIoFollowsSpecLocation(t *testing.T) {
	b, cs := newTestBackend()
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ingress-nginx", Name: "ingress-nginx-controller"},
		Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
	}
	svc.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: "51.68.9.9"}}
	_, err := cs.CoreV1().Services("ingress-nginx").Create(context.Background(), svc, metav1.CreateOptions{})
	require.NoError(t, err)

	routed, scheme := b.resolveRouting(context.Background(), workspace.Spec{
		IngressNamespace: "ingress-nginx",
		IngressService:   "ingress-nginx-controller",
		IngressClass:     "nginx",
	})
	assert.Equal(t, "51.68.9.9."+nipDomainSuffix, routed.Domain)
	assert.Equal(t, schemeHTTP, scheme)
	// The class is routing configuration, not certificate configuration: the
	// fallback clears the latter but must keep the former.
	assert.Equal(t, "nginx", routed.IngressClass)
	assert.Empty(t, routed.ClusterIssuer)
	assert.Empty(t, routed.WildcardTLSSecret)
}

func TestEnsureRegistryIngress_UsesGivenClass(t *testing.T) {
	tests := []struct {
		name      string
		class     string
		wantClass string
	}{
		{"an empty class falls back to Traefik", "", workspaceIngressClass},
		{"a reused class is honoured, so the kubelet can pull", "nginx", "nginx"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, _ := newTestBackend()
			_, err := b.EnsureRegistryIngress(context.Background(), "lab.example.com", "easylab-wildcard-tls", "", tt.class)
			require.NoError(t, err)

			ing := getIngress(t, b, registryCacheName)
			require.NotNil(t, ing.Spec.IngressClassName)
			assert.Equal(t, tt.wantClass, *ing.Spec.IngressClassName)
		})
	}
}

func TestEnsurePortal_IngressClassAndRBACNamespace(t *testing.T) {
	b, cs := newTestBackend()

	// The portal's cross-namespace Role is only created for a lab without a domain
	// (the nip.io fallback is the one thing it reads outside its namespace), so the
	// controller Service has to be reachable where the lab says it is.
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ingress-nginx", Name: "ingress-nginx-controller"},
		Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
	}
	svc.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: "51.68.9.9"}}
	_, err := cs.CoreV1().Services("ingress-nginx").Create(context.Background(), svc, metav1.CreateOptions{})
	require.NoError(t, err)

	_, err = b.EnsurePortal(context.Background(), workspace.PortalSpec{
		LabID:            "job-abc",
		Image:            "easylab:test",
		IngressClass:     "nginx",
		IngressNamespace: "ingress-nginx",
		IngressService:   "ingress-nginx-controller",
	})
	require.NoError(t, err)

	name := portalName("job-abc")

	ing, err := cs.NetworkingV1().Ingresses(b.namespace).Get(context.Background(), name, metav1.GetOptions{})
	require.NoError(t, err)
	require.NotNil(t, ing.Spec.IngressClassName)
	assert.Equal(t, "nginx", *ing.Spec.IngressClassName,
		"the portal must be served by the same controller as the lab's workspaces")

	// The Role granting the portal its read on the controller Service must land in
	// the controller's namespace, not Traefik's.
	role, err := cs.RbacV1().Roles("ingress-nginx").Get(context.Background(), name+portalIngressAccessSuffix, metav1.GetOptions{})
	require.NoError(t, err, "the portal's ingress-read Role should be in the reused controller's namespace")
	require.Len(t, role.Rules, 1)
	assert.Equal(t, []string{"ingress-nginx-controller"}, role.Rules[0].ResourceNames)

	_, err = cs.RbacV1().Roles(ingressControllerNamespace).Get(context.Background(), name+portalIngressAccessSuffix, metav1.GetOptions{})
	assert.Error(t, err, "nothing should have been created in Traefik's namespace")
}
