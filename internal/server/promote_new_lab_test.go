package server

import (
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"easylab/internal/clusterscan"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// promoteSourceConfig is a source lab with every setting the new lab is supposed
// to inherit, and several it must not.
func promoteSourceConfig() *LabConfig {
	return &LabConfig{
		StackName:                   "devoxx-2025",
		Description:                 "Last year's edition",
		UseExistingCluster:          true,
		ExternalKubeconfig:          "apiVersion: v1\nkind: Config\n",
		ClusterProfile:              "ovh-prod",
		Domain:                      "lab2025.example.com",
		WildcardDomain:              "*.lab2025.example.com",
		AcmeEmail:                   "me@example.com",
		DNSProvider:                 "ovh",
		DNSZone:                     "example.com",
		DNSProfile:                  "ovh-dns",
		DNSCredentials:              map[string]string{"ovhAppKey": "super-secret-key"},
		WorkspaceNamespace:          "workshops",
		WorkspaceLifetimeHours:      8,
		WorkspaceIdleTimeoutMinutes: 30,
		AutoBake:                    true,
		BakedImages:                 map[string]BakedImage{"go": {Image: "registry/go:baked"}},
		DisabledTemplates:           map[string]bool{"go": true},
		WorkspaceTemplates: []WorkspaceTemplate{{
			Name:             "go",
			Image:            "golang:1.26",
			GitRepo:          "https://git.example.com/workshop.git",
			IDE:              "code-server",
			GitAuthSecret:    "git-creds",
			ImagePullSecrets: []string{"regcred"},
			Env:              map[string]string{"FOO": "bar"},
		}},
	}
}

// promoteSourceLab creates a completed lab from cfg and returns its id.
func promoteSourceLab(t *testing.T, jm *JobManager, cfg *LabConfig) string {
	t.Helper()
	id := jm.CreateJob(cfg)
	jm.UpdateJobStatus(id, JobStatusCompleted)
	return id
}

// getPromotePage renders the promote-to-a-new-lab page. It chdirs to the repo
// root because getTemplate resolves web/ relative to the working directory.
func getPromotePage(t *testing.T, h *Handler, labID, template string) *httptest.ResponseRecorder {
	t.Helper()
	target := "/admin/promote?lab_id=" + url.QueryEscape(labID) + "&template=" + url.QueryEscape(template)
	rec := httptest.NewRecorder()
	h.ServePromoteToNewLab(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func TestServePromoteToNewLab_Rejections(t *testing.T) {
	h, jm := newUploadTestHandler(t)
	t.Chdir("../..")
	labID := promoteSourceLab(t, jm, promoteSourceConfig())

	tests := []struct {
		name     string
		method   string
		query    string
		wantCode int
	}{
		{name: "wrong method", method: http.MethodPost, query: "?lab_id=" + labID + "&template=go", wantCode: http.StatusMethodNotAllowed},
		{name: "no lab_id", method: http.MethodGet, query: "?template=go", wantCode: http.StatusBadRequest},
		{name: "no template", method: http.MethodGet, query: "?lab_id=" + labID, wantCode: http.StatusBadRequest},
		{name: "unknown lab", method: http.MethodGet, query: "?lab_id=job-nope&template=go", wantCode: http.StatusNotFound},
		{name: "unknown template", method: http.MethodGet, query: "?lab_id=" + labID + "&template=rust", wantCode: http.StatusNotFound},
		{name: "template name is case sensitive", method: http.MethodGet, query: "?lab_id=" + labID + "&template=GO", wantCode: http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServePromoteToNewLab(rec, httptest.NewRequest(tt.method, "/admin/promote"+tt.query, nil))
			assert.Equal(t, tt.wantCode, rec.Code)
		})
	}
}

func TestServePromoteToNewLab_CarriesTheRightFields(t *testing.T) {
	h, jm := newUploadTestHandler(t)
	t.Chdir("../..")
	labID := promoteSourceLab(t, jm, promoteSourceConfig())

	rec := getPromotePage(t, h, labID, "go")
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	t.Run("fixed fields", func(t *testing.T) {
		for _, want := range []string{
			`name="use_existing_cluster" value="true"`,
			`name="templates_mode" value="yaml"`,
			`name="promoted_from" value="` + labID + `"`,
			`name="promoted_template" value="go"`,
		} {
			assert.Contains(t, body, want)
		}
	})

	t.Run("inherited settings", func(t *testing.T) {
		for _, want := range []string{
			`id="workspace_lifetime_hours" name="workspace_lifetime_hours" value="8"`,
			`id="workspace_idle_timeout_minutes" name="workspace_idle_timeout_minutes" value="30"`,
			`id="workspace_namespace" name="workspace_namespace" value="workshops"`,
			`id="dns_zone" name="dns_zone" value="example.com"`,
			`id="acme_email" name="acme_email" value="me@example.com"`,
			`id="auto_bake" name="auto_bake" value="true" checked`,
		} {
			assert.Contains(t, body, want)
		}
	})

	t.Run("the template definition travels as YAML", func(t *testing.T) {
		assert.Contains(t, body, `name="templates_yaml"`)
		assert.Contains(t, body, "golang:1.26")
		assert.Contains(t, body, "https://git.example.com/workshop.git")
	})

	t.Run("the cluster settings default to installing, never reusing", func(t *testing.T) {
		// createLabConfigFromForm reads an absent flag as false, which means
		// "already installed on this cluster". A page that omitted these would
		// claim an ingress controller the cluster may not have.
		assert.Contains(t, body, `name="install_nginx_ingress" value="true"`)
		assert.Contains(t, body, `name="install_cert_manager" value="true"`)
		assert.Contains(t, body, `name="dns_already_configured" value="false"`)
		assert.Contains(t, body, `id="`+promoteDetectContainerID+`"`)
	})

	t.Run("infrastructure and secrets do not travel", func(t *testing.T) {
		// The cluster and the URL are what the promotion changes, and a DNS
		// credential must never be rendered into HTML.
		for _, unwanted := range []string{
			"super-secret-key",
			"apiVersion: v1\nkind: Config",
			`name="external_kubeconfig"`,
			`name="wildcard_domain"`,
			`name="dns_provider"`,
			`name="provider"`,
			`name="network_region"`,
			`name="nodepool_flavor"`,
			"registry/go:baked",
			// A spent schedule would close or delete the new lab on the first
			// cleanup tick; parseLabCloseDate even rejects a past date outright.
			`name="lab_close_date"`,
			`name="lab_deletion_date"`,
			// "days" would multiply the inherited hours by 24.
			`name="workspace_lifetime_unit"`,
		} {
			assert.NotContains(t, body, unwanted)
		}
		// The domain is the other thing the admin supplies, so it starts empty.
		assert.NotContains(t, body, "lab2025.example.com")
	})

	t.Run("no field name appears twice", func(t *testing.T) {
		// r.FormValue returns the FIRST value for a name, so a second copy of any
		// of these would silently beat the one the admin or the scan set.
		for _, field := range []string{
			"use_existing_cluster", "templates_mode", "templates_yaml",
			"stack_name", "domain", "acme_email", "dns_profile", "dns_zone",
			"cluster_profile", "kubeconfig_file", "kubeconfig_content",
			"install_nginx_ingress", "install_cert_manager", "dns_already_configured",
			"workspace_namespace", "workspace_lifetime_hours", "workspace_idle_timeout_minutes",
			"student_portal", "auto_bake", "promoted_from", "promoted_template",
		} {
			assert.Equal(t, 1, strings.Count(body, `name="`+field+`"`), "field %q", field)
		}
	})

	t.Run("credentials are named but not collected", func(t *testing.T) {
		// parseWizardSecrets rejects a credential row that has a name and no
		// token, so prefilling the names would block lab creation outright.
		assert.Contains(t, body, "git-creds")
		assert.Contains(t, body, "regcred")
		assert.NotContains(t, body, `name="secret_name"`)
		assert.NotContains(t, body, `name="secret_token"`)
	})
}

func TestServePromoteToNewLab_SuggestsAFreeStackName(t *testing.T) {
	t.Run("the source name when it is free", func(t *testing.T) {
		assert.Equal(t, "devoxx", suggestedPromoteStackNameFrom("devoxx", nil))
	})
	t.Run("a numbered name when it is not", func(t *testing.T) {
		taken := map[string]bool{"devoxx": true, "devoxx-2": true}
		assert.Equal(t, "devoxx-3", suggestedPromoteStackNameFrom("devoxx", taken))
	})
	t.Run("a fallback when there is no source name", func(t *testing.T) {
		assert.Equal(t, "promoted", suggestedPromoteStackNameFrom("", nil))
	})

	t.Run("on the page", func(t *testing.T) {
		h, jm := newUploadTestHandler(t)
		t.Chdir("../..")
		labID := promoteSourceLab(t, jm, promoteSourceConfig())

		rec := getPromotePage(t, h, labID, "go")
		require.Equal(t, http.StatusOK, rec.Code)
		// "devoxx-2025" is taken by the source lab itself.
		assert.Contains(t, rec.Body.String(), `name="stack_name" value="devoxx-2025-2"`)
	})
}

func TestServePromoteToNewLab_SourceNeedNotBeUp(t *testing.T) {
	// PromoteTemplate only ever reads the source lab, and so does this page, so a
	// destroyed or failed lab can still be promoted from via a bookmarked link.
	for _, status := range []JobStatus{JobStatusFailed, JobStatusDestroyed, JobStatusPending} {
		t.Run(string(status), func(t *testing.T) {
			h, jm := newUploadTestHandler(t)
			t.Chdir("../..")
			labID := jm.CreateJob(promoteSourceConfig())
			jm.UpdateJobStatus(labID, status)

			rec := getPromotePage(t, h, labID, "go")
			require.Equal(t, http.StatusOK, rec.Code)
			assert.Contains(t, rec.Body.String(), "golang:1.26")
		})
	}
}

func TestServePromoteToNewLab_Escapes(t *testing.T) {
	h, jm := newUploadTestHandler(t)
	t.Chdir("../..")
	cfg := promoteSourceConfig()
	cfg.StackName = `<img src=x onerror=alert(1)>`
	cfg.DNSZone = `"><script>alert(1)</script>`
	cfg.AcmeEmail = `"><script>alert(2)</script>`
	cfg.WorkspaceTemplates[0].Image = `golang"><script>alert(3)</script>`
	cfg.WorkspaceTemplates[0].GitAuthSecret = `<script>alert(4)</script>`
	labID := promoteSourceLab(t, jm, cfg)

	rec := getPromotePage(t, h, labID, "go")
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	// The payloads must survive only as text: no tag may actually be opened, and
	// no attribute may escape the one it was interpolated into.
	assert.NotContains(t, body, "<script>alert")
	assert.NotContains(t, body, "<img src=x")
	assert.NotContains(t, body, `value="<`)
	assert.NotContains(t, body, `"><script`)
	assert.Contains(t, body, "&lt;script&gt;")
	assert.Contains(t, body, "&lt;img src=x onerror=alert(1)&gt;")
}

func TestServePromoteToNewLab_StudentPortalNeedsAnImage(t *testing.T) {
	tests := []struct {
		name          string
		sourceEnabled bool
		wantChecked   bool
	}{
		{name: "not inherited when the source had none", sourceEnabled: false, wantChecked: false},
		// A build with no portal image cannot run one, so inheriting the flag
		// would promise a portal that never appears. Same gate as ServeAdminUI.
		{name: "dropped when this build ships no portal image", sourceEnabled: true, wantChecked: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, jm := newUploadTestHandler(t)
			t.Chdir("../..")
			cfg := promoteSourceConfig()
			cfg.StudentPortal = tt.sourceEnabled
			labID := promoteSourceLab(t, jm, cfg)

			rec := getPromotePage(t, h, labID, "go")
			require.Equal(t, http.StatusOK, rec.Code)
			got := strings.Contains(rec.Body.String(), `name="student_portal" value="true" checked`)
			assert.Equal(t, tt.wantChecked, got)
		})
	}
}

func TestDNSProfileIDByName(t *testing.T) {
	profiles := []wizardDNSProfile{
		{ID: "p1", Name: "ovh-dns", Provider: "ovh"},
		{ID: "p2", Name: "azure-dns", Provider: "azure"},
	}

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "match", input: "ovh-dns", want: "p1"},
		{name: "no match", input: "gone", want: ""},
		{name: "empty", input: "", want: ""},
		{name: "whitespace only", input: "   ", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, dnsProfileIDByName(profiles, tt.input))
		})
	}
}

// postPromoteDetect drives DetectClusterForPromote with a urlencoded body.
func postPromoteDetect(h *Handler, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/promote/detect-cluster", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.DetectClusterForPromote(rec, req)
	return rec
}

func TestDetectClusterForPromote(t *testing.T) {
	absentCertManager := func() *clusterscan.Report {
		rep := okReport()
		rep.CertManager = clusterscan.CertManagerFinding{Probe: clusterscan.Probe{Status: clusterscan.StatusAbsent}}
		rep.Issuers = clusterscan.IssuerFinding{Probe: clusterscan.Probe{Status: clusterscan.StatusSkipped}}
		return rep
	}
	forbiddenIngress := func() *clusterscan.Report {
		rep := okReport()
		rep.Ingress = clusterscan.IngressFinding{Probe: clusterscan.Probe{
			Status: clusterscan.StatusForbidden,
			Detail: "not allowed to list services",
		}}
		return rep
	}
	unusableIssuer := func() *clusterscan.Report {
		rep := okReport()
		rep.Issuers.Items = []clusterscan.ClusterIssuerInfo{
			{Name: "letsencrypt-http", Kind: "acme", HasHTTP01: true, Ready: true},
		}
		return rep
	}

	tests := []struct {
		name            string
		report          *clusterscan.Report
		errs            []error
		form            url.Values
		wantContains    []string
		wantNotContains []string
	}{
		{
			name:   "a stock cluster is reused",
			report: okReport(),
			form:   url.Values{"kubeconfig_content": {"kube"}, "dns_profile": {"p1"}},
			wantContains: []string{
				`name="install_nginx_ingress" value="false"`,
				`name="nginx_ingress_namespace" value="traefik"`,
				`name="nginx_ingress_service_name" value="traefik"`,
				`name="ingress_class_name" value="traefik"`,
				`name="install_cert_manager" value="false"`,
				`name="cert_manager_namespace" value="cert-manager"`,
				`name="dns_already_configured" value="true"`,
				`name="cluster_issuer_name" value="letsencrypt-prod"`,
				"Detected on this cluster",
				"Kubernetes v1.31.4",
				"Ingress controller",
				"ClusterIssuer",
				"51.68.1.2",
			},
		},
		{
			name:   "no cert-manager means install one",
			report: absentCertManager(),
			form:   url.Values{"kubeconfig_content": {"kube"}, "dns_profile": {"p1"}},
			wantContains: []string{
				`name="install_cert_manager" value="true"`,
				`name="dns_already_configured" value="false"`,
				"finding-absent",
				"not found",
			},
			wantNotContains: []string{`name="cluster_issuer_name"`, `name="cert_manager_namespace"`},
		},
		{
			name:   "a denied probe is not read as reuse",
			report: forbiddenIngress(),
			form:   url.Values{"kubeconfig_content": {"kube"}},
			wantContains: []string{
				// "silence beats a guess": a probe that was refused says nothing
				// about the cluster, so it must not become "reuse".
				`name="install_nginx_ingress" value="true"`,
				"finding-unknown",
				"couldn&#39;t check",
			},
			wantNotContains: []string{`name="nginx_ingress_namespace"`},
		},
		{
			name:   "an issuer with no DNS-01 solver is not adopted",
			report: unusableIssuer(),
			form:   url.Values{"kubeconfig_content": {"kube"}, "dns_profile": {"p1"}},
			wantContains: []string{
				`name="dns_already_configured" value="false"`,
				"HTTP-01 only",
			},
			wantNotContains: []string{`name="cluster_issuer_name"`},
		},
		{
			name:   "a usable issuer is not adopted without a DNS provider",
			report: okReport(),
			form:   url.Values{"kubeconfig_content": {"kube"}},
			wantContains: []string{
				// One ClusterIssuer carries one DNS-01 solver; with no DNS
				// provider there is nothing to answer a challenge with.
				`name="dns_already_configured" value="false"`,
				`name="install_cert_manager" value="false"`,
			},
			wantNotContains: []string{`name="cluster_issuer_name"`},
		},
		{
			name:   "an unparseable kubeconfig keeps the defaults",
			report: nil,
			errs:   []error{clusterscan.ErrInvalidKubeconfig},
			form:   url.Values{"kubeconfig_content": {"not a kubeconfig"}},
			wantContains: []string{
				"error-message",
				"usable kubeconfig",
				// The container is where these live, so a failed scan must not
				// leave the form without them.
				`name="install_nginx_ingress" value="true"`,
				`name="install_cert_manager" value="true"`,
			},
		},
		{
			name:         "an unreachable cluster keeps the defaults",
			report:       nil,
			form:         url.Values{"kubeconfig_content": {"kube"}},
			wantContains: []string{"error-message", "Could not reach the cluster", `name="install_nginx_ingress" value="true"`},
		},
		{
			name:         "no kubeconfig at all",
			report:       okReport(),
			form:         url.Values{},
			wantContains: []string{"error-message", "Pick a saved cluster", `name="install_nginx_ingress" value="true"`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newDetectHandler()
			stubScan(h, tt.report, tt.errs)

			rec := postPromoteDetect(h, tt.form)
			require.Equal(t, http.StatusOK, rec.Code)
			assert.Contains(t, rec.Header().Get("Content-Type"), "text/html")

			body := rec.Body.String()
			// outerHTML swaps replace the container, so it has to carry its own id
			// or the second scan has nothing to target.
			assert.Contains(t, body, `id="`+promoteDetectContainerID+`"`)
			for _, want := range tt.wantContains {
				assert.Contains(t, body, want)
			}
			for _, unwanted := range tt.wantNotContains {
				assert.NotContains(t, body, unwanted)
			}
		})
	}
}

func TestDetectClusterForPromote_Rejections(t *testing.T) {
	h := newDetectHandler()
	stubScan(h, okReport(), nil)

	rec := httptest.NewRecorder()
	h.DetectClusterForPromote(rec, httptest.NewRequest(http.MethodGet, "/api/promote/detect-cluster", nil))
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

func TestDetectClusterForPromote_ScansTheRequestedNamespace(t *testing.T) {
	h := newDetectHandler()
	opts := stubScan(h, okReport(), nil)

	rec := postPromoteDetect(h, url.Values{
		"kubeconfig_content":  {"kube"},
		"workspace_namespace": {"devoxx-2026"},
	})
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "devoxx-2026", opts.WorkspaceNamespace)
}

func TestDetectClusterForPromote_EscapesClusterValues(t *testing.T) {
	// A ClusterIssuer or namespace name comes off a cluster EasyLab does not own.
	rep := okReport()
	rep.Ingress.Namespace = `<script>alert(1)</script>`
	rep.Issuers.Items[0].Name = `"><script>alert(2)</script>`
	rep.Warnings = []clusterscan.Warning{{Key: "x", Message: `<script>alert(3)</script>`}}

	h := newDetectHandler()
	stubScan(h, rep, nil)

	rec := postPromoteDetect(h, url.Values{"kubeconfig_content": {"kube"}, "dns_profile": {"p1"}})
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	assert.NotContains(t, body, "<script>alert")
	assert.Contains(t, body, "&lt;script&gt;")
	assert.Contains(t, body, "Worth knowing")
}

func TestRecordPromotedLabAudit(t *testing.T) {
	tests := []struct {
		name       string
		form       url.Values
		wantDetail string
	}{
		{
			name:       "both fields present",
			form:       url.Values{"promoted_from": {"job-source"}, "promoted_template": {"go"}},
			wantDetail: "go from lab job-source",
		},
		{
			// The detail lands in an append-only log that Recent reads whole, so
			// an oversized value would cost every later read.
			name: "an oversized value is bounded",
			form: url.Values{
				"promoted_from":     {"job-source"},
				"promoted_template": {strings.Repeat("x", 5000)},
			},
			wantDetail: strings.Repeat("x", maxPromoteAuditValueLength) + "… from lab job-source",
		},
		{name: "no source", form: url.Values{"promoted_template": {"go"}}},
		{name: "no template", form: url.Values{"promoted_from": {"job-source"}}},
		// The wizard sends neither, so its own lab.create path must be untouched.
		{name: "an ordinary lab creation", form: url.Values{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _ := newUploadTestHandler(t)
			as, err := NewAuditStore(t.TempDir())
			require.NoError(t, err)
			h.SetAuditStore(as)

			req := httptest.NewRequest(http.MethodPost, "/api/labs", strings.NewReader(tt.form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			require.NoError(t, req.ParseForm())
			h.recordPromotedLabAudit(req, "job-new")

			if tt.wantDetail == "" {
				// recordAudit writes asynchronously, so give it the chance to
				// write something before concluding that it did not.
				time.Sleep(100 * time.Millisecond)
				entries, err := as.Recent(0)
				require.NoError(t, err)
				for _, e := range entries {
					assert.NotEqual(t, "lab.template_promote", e.Action)
				}
				return
			}
			entries := waitForAuditEntry(t, as, "lab.template_promote")
			require.Len(t, entries, 1)
			assert.Equal(t, "job-new", entries[0].LabID, "recorded on the lab the promotion created")
			assert.Equal(t, tt.wantDetail, entries[0].Detail)
		})
	}
}

// TestPromoteToNewLab_EndToEnd is the test that proves the premise of the whole
// page: that its fields alone are enough for the existing creation path to build
// the right lab. It renders the page, submits exactly what the page carries plus
// the two things the admin supplies, and asserts on the lab that comes out.
func TestPromoteToNewLab_EndToEnd(t *testing.T) {
	h, jm := newUploadTestHandler(t)
	t.Chdir("../..")
	h.pulumiExec = &PulumiExecutor{workDir: t.TempDir(), jobManager: jm}
	sourceID := promoteSourceLab(t, jm, promoteSourceConfig())

	page := getPromotePage(t, h, sourceID, "go")
	require.Equal(t, http.StatusOK, page.Code)
	body := page.Body.String()

	// The fields the page carries, read back out of the rendered HTML so the test
	// cannot drift from the template.
	form := url.Values{}
	for _, name := range []string{
		"use_existing_cluster", "templates_mode", "promoted_from", "promoted_template",
		"install_nginx_ingress", "install_cert_manager", "dns_already_configured",
	} {
		value := hiddenValueFrom(t, body, name)
		form.Set(name, value)
	}
	form.Set("templates_yaml", extractTextarea(t, body, "templates_yaml"))
	form.Set("workspace_namespace", "workshops")
	form.Set("workspace_lifetime_hours", "8")
	form.Set("workspace_idle_timeout_minutes", "30")
	form.Set("auto_bake", "true")
	// What the admin supplies.
	form.Set("stack_name", "devoxx-2026")
	form.Set("domain", "lab2026.example.com")
	form.Set("kubeconfig_content", "apiVersion: v1\nkind: Config\nclusters: []\n")

	req := httptest.NewRequest(http.MethodPost, "/api/labs", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.CreateLab(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	// The newest job that is not the source is the one just created.
	var created *Job
	for _, job := range jm.GetAllJobs() {
		if job.ID != sourceID {
			created = job
			break
		}
	}
	require.NotNil(t, created, "a lab should have been created")

	created.mu.RLock()
	cfg := created.Config
	created.mu.RUnlock()
	require.NotNil(t, cfg)

	assert.True(t, cfg.UseExistingCluster)
	assert.Equal(t, "devoxx-2026", cfg.StackName)
	assert.Equal(t, "lab2026.example.com", cfg.Domain)
	assert.Equal(t, "apiVersion: v1\nkind: Config\nclusters: []\n", cfg.ExternalKubeconfig)

	t.Run("exactly the promoted template", func(t *testing.T) {
		require.Len(t, cfg.WorkspaceTemplates, 1)
		got := cfg.WorkspaceTemplates[0]
		assert.Equal(t, "go", got.Name)
		assert.Equal(t, "golang:1.26", got.Image)
		assert.Equal(t, "https://git.example.com/workshop.git", got.GitRepo)
		assert.Equal(t, "git-creds", got.GitAuthSecret)
		assert.Equal(t, []string{"regcred"}, got.ImagePullSecrets)
		assert.Equal(t, map[string]string{"FOO": "bar"}, got.Env)
	})

	t.Run("the inherited settings arrived", func(t *testing.T) {
		assert.Equal(t, "workshops", cfg.WorkspaceNamespace)
		assert.Equal(t, 8, cfg.WorkspaceLifetimeHours)
		assert.Equal(t, 30, cfg.WorkspaceIdleTimeoutMinutes)
		assert.True(t, cfg.AutoBake)
	})

	t.Run("the cluster is treated as empty until a scan says otherwise", func(t *testing.T) {
		require.NotNil(t, cfg.InstallNginxIngress)
		assert.True(t, *cfg.InstallNginxIngress)
		require.NotNil(t, cfg.InstallCertManager)
		assert.True(t, *cfg.InstallCertManager)
		assert.False(t, cfg.DNSAlreadyConfigured)
	})

	t.Run("nothing lab-scoped or secret came along", func(t *testing.T) {
		assert.Empty(t, cfg.BakedImages)
		assert.Empty(t, cfg.DisabledTemplates)
		assert.Empty(t, cfg.WildcardDomain)
		assert.Empty(t, cfg.DNSCredentials)
		assert.Nil(t, cfg.LabCloseDate)
		assert.Nil(t, cfg.LabDeletionDate)
	})

	t.Run("the source lab is untouched", func(t *testing.T) {
		assert.Equal(t, []string{"go"}, labTemplateNames(t, jm, sourceID))
	})
}

// hiddenValueFrom pulls a hidden input's value straight out of rendered HTML, so
// the end-to-end test submits what the page actually carries.
func hiddenValueFrom(t *testing.T, body, name string) string {
	t.Helper()
	marker := `name="` + name + `" value="`
	i := strings.Index(body, marker)
	require.GreaterOrEqual(t, i, 0, "hidden field %q not found", name)
	rest := body[i+len(marker):]
	end := strings.Index(rest, `"`)
	require.GreaterOrEqual(t, end, 0, "unterminated value for %q", name)
	return rest[:end]
}

// extractTextarea pulls a textarea's contents out of rendered HTML.
func extractTextarea(t *testing.T, body, id string) string {
	t.Helper()
	open := strings.Index(body, `id="`+id+`"`)
	require.GreaterOrEqual(t, open, 0, "textarea %q not found", id)
	start := strings.Index(body[open:], ">")
	require.GreaterOrEqual(t, start, 0)
	rest := body[open+start+1:]
	end := strings.Index(rest, "</textarea>")
	require.GreaterOrEqual(t, end, 0, "unterminated textarea %q", id)
	return html.UnescapeString(rest[:end])
}
