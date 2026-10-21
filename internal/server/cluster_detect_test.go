package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"easylab/internal/clusterscan"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubScan replaces the Handler's cluster scan with a canned answer, so the
// handler can be exercised without a cluster.
func stubScan(h *Handler, rep *clusterscan.Report, errs []error) *clusterscan.Options {
	var got clusterscan.Options
	h.scanCluster = func(_ context.Context, _ string, opts clusterscan.Options) (*clusterscan.Report, []error) {
		got = opts
		return rep, errs
	}
	return &got
}

func detectRequest(body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/cluster/detect", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

func newDetectHandler() *Handler {
	return NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
}

// okReport is a scan of a stock Traefik + cert-manager cluster.
func okReport() *clusterscan.Report {
	return &clusterscan.Report{
		Cluster: clusterscan.ClusterFinding{
			Probe:   clusterscan.Probe{Status: clusterscan.StatusOK},
			Version: "v1.31.4",
		},
		Ingress: clusterscan.IngressFinding{
			Probe:      clusterscan.Probe{Status: clusterscan.StatusOK},
			Namespace:  "traefik",
			Service:    "traefik",
			Class:      "traefik",
			Flavour:    "traefik",
			ExternalIP: "51.68.1.2",
		},
		CertManager: clusterscan.CertManagerFinding{
			Probe:     clusterscan.Probe{Status: clusterscan.StatusOK},
			Installed: true,
			Namespace: "cert-manager",
		},
		Issuers: clusterscan.IssuerFinding{
			Probe: clusterscan.Probe{Status: clusterscan.StatusOK},
			Items: []clusterscan.ClusterIssuerInfo{
				{Name: "letsencrypt-prod", Kind: "acme", HasDNS01: true, Ready: true},
			},
		},
		Storage: clusterscan.StorageFinding{
			Probe:   clusterscan.Probe{Status: clusterscan.StatusOK},
			Default: "csi-cinder-high-speed",
		},
		Nodes: clusterscan.NodeFinding{
			Probe: clusterscan.Probe{Status: clusterscan.StatusOK},
			Count: 3,
			Labels: []clusterscan.NodeLabel{
				{Key: "nodepool", Values: []string{"control", "workspaces"}, Nodes: 3},
			},
		},
		Namespaces: clusterscan.NamespaceFinding{
			Probe:         clusterscan.Probe{Status: clusterscan.StatusOK},
			WorkspaceName: "workshops",
		},
		Permissions: clusterscan.PermissionFinding{
			Probe: clusterscan.Probe{Status: clusterscan.StatusOK},
		},
	}
}

func TestDetectCluster_RejectsBadRequests(t *testing.T) {
	tests := []struct {
		name        string
		method      string
		body        string
		scanErrs    []error
		wantStatus  int
		wantMessage string
	}{
		{
			name:        "GET is refused",
			method:      http.MethodGet,
			wantStatus:  http.StatusMethodNotAllowed,
			wantMessage: "method not allowed",
		},
		{
			name:        "a missing kubeconfig asks for one",
			method:      http.MethodPost,
			body:        "workspace_namespace=workshops",
			wantStatus:  http.StatusBadRequest,
			wantMessage: "provide a kubeconfig",
		},
		{
			name:        "a whitespace-only kubeconfig asks for one",
			method:      http.MethodPost,
			body:        "kubeconfig_content=%20%0A",
			wantStatus:  http.StatusBadRequest,
			wantMessage: "provide a kubeconfig",
		},
		{
			name:        "an unparseable kubeconfig is named as such",
			method:      http.MethodPost,
			body:        "kubeconfig_content=not-a-kubeconfig",
			scanErrs:    []error{clusterscan.ErrInvalidKubeconfig},
			wantStatus:  http.StatusUnprocessableEntity,
			wantMessage: "does not look like a usable kubeconfig",
		},
		{
			name:        "an unreachable cluster is named as such",
			method:      http.MethodPost,
			body:        "kubeconfig_content=apiVersion%3A%20v1",
			scanErrs:    []error{errors.New("dial tcp 10.0.0.1:6443: connect: connection refused")},
			wantStatus:  http.StatusUnprocessableEntity,
			wantMessage: "could not reach the cluster",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newDetectHandler()
			stubScan(h, nil, tt.scanErrs)

			req := detectRequest(tt.body)
			req.Method = tt.method
			w := httptest.NewRecorder()
			h.DetectCluster(w, req)

			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			assert.Contains(t, w.Body.String(), tt.wantMessage)
		})
	}
}

func TestDetectCluster_Success(t *testing.T) {
	h := newDetectHandler()
	opts := stubScan(h, okReport(), nil)

	w := httptest.NewRecorder()
	h.DetectCluster(w, detectRequest("kubeconfig_content=apiVersion%3A%20v1&workspace_namespace=devoxx"))

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
	assert.Equal(t, "devoxx", opts.WorkspaceNamespace, "the submitted namespace should be the one probed")

	var resp clusterDetectResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.NotNil(t, resp.Report)
	assert.Equal(t, "v1.31.4", resp.Report.Cluster.Version)
	assert.Equal(t, "traefik", resp.Suggested.NginxIngressNamespace)
	require.NotNil(t, resp.Suggested.InstallNginxIngress)
	assert.False(t, *resp.Suggested.InstallNginxIngress)
}

// TestDetectCluster_NeverLeaksKubeconfig is the security regression: the
// kubeconfig is credentials, and nothing derived from it may come back to the
// browser or reach the audit log.
func TestDetectCluster_NeverLeaksKubeconfig(t *testing.T) {
	const sentinel = "SENTINEL-TOKEN-xyz"
	const server = "https://secret-cluster.internal:6443"

	as, err := NewAuditStore(t.TempDir())
	require.NoError(t, err)

	h := newDetectHandler()
	h.SetAuditStore(as)
	stubScan(h, okReport(), nil)

	kubeconfig := "apiVersion: v1\nclusters:\n- cluster:\n    server: " + server +
		"\n  name: c\nusers:\n- name: u\n  user:\n    token: " + sentinel + "\n"

	w := httptest.NewRecorder()
	h.DetectCluster(w, detectRequest("kubeconfig_content="+url.QueryEscape(kubeconfig)))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	assert.NotContains(t, w.Body.String(), sentinel, "the response must not echo the credentials")
	assert.NotContains(t, w.Body.String(), "secret-cluster.internal", "the response must not echo the API server address")

	entries := waitForAuditEntry(t, as, "cluster.detect")
	require.NotEmpty(t, entries)
	for _, e := range entries {
		assert.NotContains(t, e.Detail, sentinel)
		assert.NotContains(t, e.Detail, "secret-cluster.internal")
		// It should still say something useful about the cluster.
		assert.Contains(t, e.Detail, "v1.31.4")
	}
}

func TestSuggestedFromReport(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		report func() *clusterscan.Report
		want   func(t *testing.T, s clusterSuggestion)
	}{
		{
			name:   "nil report suggests nothing",
			report: func() *clusterscan.Report { return nil },
			want: func(t *testing.T, s clusterSuggestion) {
				assert.Equal(t, clusterSuggestion{}, s)
			},
		},
		{
			name:   "a stock cluster is reused rather than reinstalled",
			report: okReport,
			want: func(t *testing.T, s clusterSuggestion) {
				require.NotNil(t, s.InstallNginxIngress)
				assert.False(t, *s.InstallNginxIngress)
				assert.Equal(t, "traefik", s.NginxIngressNamespace)
				assert.Equal(t, "traefik", s.NginxIngressServiceName)
				assert.Equal(t, "traefik", s.IngressClassName)
				require.NotNil(t, s.InstallCertManager)
				assert.False(t, *s.InstallCertManager)
				assert.Equal(t, "cert-manager", s.CertManagerNamespace)
				assert.Equal(t, "letsencrypt-prod", s.ClusterIssuerName)
				require.NotNil(t, s.DNSAlreadyConfigured)
				assert.True(t, *s.DNSAlreadyConfigured)
				assert.Equal(t, "nodepool", s.NodeSelectorKey)
				assert.Equal(t, "control", s.NodeSelectorValue)
			},
		},
		{
			name: "a non-traefik class is carried through",
			report: func() *clusterscan.Report {
				rep := okReport()
				rep.Ingress.Namespace = "ingress-nginx"
				rep.Ingress.Service = "ingress-nginx-controller"
				rep.Ingress.Class = "nginx"
				rep.Ingress.Flavour = "ingress-nginx"
				return rep
			},
			want: func(t *testing.T, s clusterSuggestion) {
				assert.Equal(t, "nginx", s.IngressClassName)
				assert.Equal(t, "ingress-nginx", s.NginxIngressNamespace)
			},
		},
		{
			// A denied probe says nothing about the cluster, so suggesting "install
			// one" could wipe out a controller the credentials just cannot see.
			name: "a forbidden ingress probe suggests nothing at all",
			report: func() *clusterscan.Report {
				rep := okReport()
				rep.Ingress = clusterscan.IngressFinding{
					Probe: clusterscan.Probe{Status: clusterscan.StatusForbidden},
				}
				return rep
			},
			want: func(t *testing.T, s clusterSuggestion) {
				assert.Nil(t, s.InstallNginxIngress)
				assert.Empty(t, s.NginxIngressNamespace)
				assert.Empty(t, s.IngressClassName)
			},
		},
		{
			name: "an absent ingress controller is never turned into 'install'",
			report: func() *clusterscan.Report {
				rep := okReport()
				rep.Ingress = clusterscan.IngressFinding{
					Probe: clusterscan.Probe{Status: clusterscan.StatusAbsent},
				}
				return rep
			},
			want: func(t *testing.T, s clusterSuggestion) {
				assert.Nil(t, s.InstallNginxIngress,
					"leaving the choice alone is safer than asserting it")
			},
		},
		{
			name: "an issuer that cannot issue is not offered",
			report: func() *clusterscan.Report {
				rep := okReport()
				rep.Issuers.Items = []clusterscan.ClusterIssuerInfo{
					{Name: "http-only", Kind: "acme", HasHTTP01: true, Ready: true},
					{Name: "stalled", Kind: "acme", HasDNS01: true, Ready: false},
				}
				return rep
			},
			want: func(t *testing.T, s clusterSuggestion) {
				assert.Empty(t, s.ClusterIssuerName,
					"neither issuer can issue a wildcard certificate")
				assert.Nil(t, s.DNSAlreadyConfigured)
				// cert-manager itself is still there and still reused.
				require.NotNil(t, s.InstallCertManager)
				assert.False(t, *s.InstallCertManager)
			},
		},
		{
			name: "the first usable issuer wins over an unusable one",
			report: func() *clusterscan.Report {
				rep := okReport()
				rep.Issuers.Items = []clusterscan.ClusterIssuerInfo{
					{Name: "broken", Kind: "acme", HasDNS01: true, Ready: false},
					{Name: "good", Kind: "acme", HasDNS01: true, Ready: true},
				}
				return rep
			},
			want: func(t *testing.T, s clusterSuggestion) {
				assert.Equal(t, "good", s.ClusterIssuerName)
			},
		},
		{
			name: "without cert-manager nothing about TLS is suggested",
			report: func() *clusterscan.Report {
				rep := okReport()
				rep.CertManager = clusterscan.CertManagerFinding{
					Probe: clusterscan.Probe{Status: clusterscan.StatusAbsent},
				}
				rep.Issuers = clusterscan.IssuerFinding{
					Probe: clusterscan.Probe{Status: clusterscan.StatusSkipped},
				}
				return rep
			},
			want: func(t *testing.T, s clusterSuggestion) {
				assert.Nil(t, s.InstallCertManager)
				assert.Empty(t, s.CertManagerNamespace)
				assert.Empty(t, s.ClusterIssuerName)
			},
		},
		{
			name: "an existing workspace namespace is offered",
			report: func() *clusterscan.Report {
				rep := okReport()
				rep.Namespaces.WorkspaceExists = true
				return rep
			},
			want: func(t *testing.T, s clusterSuggestion) {
				assert.Equal(t, "workshops", s.WorkspaceNamespace)
			},
		},
		{
			name:   "a namespace that does not exist yet is not offered",
			report: okReport,
			want: func(t *testing.T, s clusterSuggestion) {
				assert.Empty(t, s.WorkspaceNamespace)
			},
		},
		{
			name: "a node-local default points at a network-attached class",
			report: func() *clusterscan.Report {
				rep := okReport()
				rep.Storage.Default = "local-path"
				rep.Storage.NodeLocal = true
				rep.Storage.Classes = []clusterscan.StorageClassInfo{
					{Name: "local-path", Provisioner: "rancher.io/local-path", Default: true, NodeLocal: true},
					{Name: "csi-cinder-high-speed", Provisioner: "cinder.csi.openstack.org"},
				}
				return rep
			},
			want: func(t *testing.T, s clusterSuggestion) {
				assert.Equal(t, "csi-cinder-high-speed", s.StorageClass)
			},
		},
		{
			name: "a node-local default with no alternative suggests nothing",
			report: func() *clusterscan.Report {
				rep := okReport()
				rep.Storage.NodeLocal = true
				rep.Storage.Classes = []clusterscan.StorageClassInfo{
					{Name: "local-path", Provisioner: "rancher.io/local-path", Default: true, NodeLocal: true},
				}
				return rep
			},
			want: func(t *testing.T, s clusterSuggestion) {
				assert.Empty(t, s.StorageClass, "there is no better class to offer")
			},
		},
		{
			name: "a healthy default storage class is left alone",
			report: func() *clusterscan.Report {
				rep := okReport()
				rep.Storage.Classes = []clusterscan.StorageClassInfo{
					{Name: "csi-cinder-high-speed", Default: true},
				}
				return rep
			},
			want: func(t *testing.T, s clusterSuggestion) {
				assert.Empty(t, s.StorageClass)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tt.want(t, suggestedFromReport(tt.report()))
		})
	}
}

func TestAuditDetailForScan(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		report   func() *clusterscan.Report
		contains []string
	}{
		{
			name:     "a full cluster is summarized",
			report:   okReport,
			contains: []string{"kubernetes v1.31.4", "3 nodes", "ingress traefik/traefik class traefik", "cert-manager in cert-manager, 1 issuer(s)"},
		},
		{
			name: "an empty cluster says what is missing",
			report: func() *clusterscan.Report {
				rep := okReport()
				rep.Ingress = clusterscan.IngressFinding{}
				rep.CertManager = clusterscan.CertManagerFinding{}
				rep.Warnings = []clusterscan.Warning{{Key: "storageclass.absent", Message: "x"}}
				return rep
			},
			contains: []string{"no ingress controller found", "no cert-manager", "1 warning(s)"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			detail := auditDetailForScan(tt.report())
			for _, want := range tt.contains {
				assert.Contains(t, detail, want)
			}
		})
	}
}
