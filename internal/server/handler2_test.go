package server

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// waitForJobsTerminal blocks until all jobs in jm have reached a terminal state
// (completed, failed, destroyed, dry-run-completed) or the timeout elapses.
// This prevents t.TempDir cleanup from racing with background goroutines that
// create job directories.
func waitForJobsTerminal(jm *JobManager, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		allDone := true
		for _, j := range jm.GetAllJobs() {
			j.mu.RLock()
			s := j.Status
			j.mu.RUnlock()
			if s == JobStatusPending || s == JobStatusRunning {
				allDone = false
				break
			}
		}
		if allDone {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// --- writeJSONError tests ---

func TestWriteJSONError(t *testing.T) {
	w := httptest.NewRecorder()
	writeJSONError(w, http.StatusBadRequest, "something went wrong")

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

	var body map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, false, body["success"])
	assert.Equal(t, "something went wrong", body["message"])
}

func TestWriteJSONError_StatusCodes(t *testing.T) {
	for _, code := range []int{http.StatusBadRequest, http.StatusNotFound, http.StatusInternalServerError} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			w := httptest.NewRecorder()
			writeJSONError(w, code, "error")
			assert.Equal(t, code, w.Code)
		})
	}
}

// --- filterAzureVMSizesByCPURAM tests ---

func TestFilterAzureVMSizesByCPURAM_NoFilter(t *testing.T) {
	sizes := []azureVMSize{
		{Name: "Standard_B2s", VCPUs: 2, RAMGB: 4},
		{Name: "Standard_D4s_v3", VCPUs: 4, RAMGB: 16},
	}
	got := filterAzureVMSizesByCPURAM(sizes, 0, 0, 0, 0)
	assert.Equal(t, sizes, got)
}

func TestFilterAzureVMSizesByCPURAM_MinVCPUs(t *testing.T) {
	sizes := []azureVMSize{
		{Name: "tiny", VCPUs: 1, RAMGB: 2},
		{Name: "medium", VCPUs: 4, RAMGB: 16},
	}
	got := filterAzureVMSizesByCPURAM(sizes, 2, 0, 0, 0)
	assert.Len(t, got, 1)
	assert.Equal(t, "medium", got[0].Name)
}

func TestFilterAzureVMSizesByCPURAM_MaxVCPUs(t *testing.T) {
	sizes := []azureVMSize{
		{Name: "small", VCPUs: 2, RAMGB: 4},
		{Name: "large", VCPUs: 16, RAMGB: 64},
	}
	got := filterAzureVMSizesByCPURAM(sizes, 0, 4, 0, 0)
	assert.Len(t, got, 1)
	assert.Equal(t, "small", got[0].Name)
}

func TestFilterAzureVMSizesByCPURAM_MinRAM(t *testing.T) {
	sizes := []azureVMSize{
		{Name: "low-mem", VCPUs: 2, RAMGB: 1},
		{Name: "high-mem", VCPUs: 4, RAMGB: 32},
	}
	got := filterAzureVMSizesByCPURAM(sizes, 0, 0, 8, 0)
	assert.Len(t, got, 1)
	assert.Equal(t, "high-mem", got[0].Name)
}

func TestFilterAzureVMSizesByCPURAM_MaxRAM(t *testing.T) {
	sizes := []azureVMSize{
		{Name: "small", VCPUs: 2, RAMGB: 4},
		{Name: "huge", VCPUs: 8, RAMGB: 128},
	}
	got := filterAzureVMSizesByCPURAM(sizes, 0, 0, 0, 16)
	assert.Len(t, got, 1)
	assert.Equal(t, "small", got[0].Name)
}

func TestFilterAzureVMSizesByCPURAM_Empty(t *testing.T) {
	got := filterAzureVMSizesByCPURAM(nil, 2, 8, 4, 32)
	assert.Empty(t, got)
}

// --- buildWorkspaceName tests ---

func TestBuildWorkspaceName_Basic(t *testing.T) {
	name := buildWorkspaceName("my-lab", "1234567890abcdef", "my-template")
	if name == "" {
		t.Error("buildWorkspaceName() returned empty string")
	}
	if len(name) > 32 {
		t.Errorf("buildWorkspaceName() len = %d, want <= 32", len(name))
	}
}

func TestBuildWorkspaceName_MaxLength(t *testing.T) {
	name := buildWorkspaceName(
		"very-long-lab-name-here",
		"very-long-lab-id-suffix",
		"very-long-template-name",
	)
	if len(name) > 32 {
		t.Errorf("buildWorkspaceName() len = %d, exceeds 32", len(name))
	}
}

func TestBuildWorkspaceName_EmptyLabName(t *testing.T) {
	name := buildWorkspaceName("", "abc1234567", "template")
	assert.NotEmpty(t, name)
	assert.Contains(t, name, "lab")
}

func TestBuildWorkspaceName_EmptyTemplateName(t *testing.T) {
	name := buildWorkspaceName("lab", "abc1234567", "")
	assert.NotEmpty(t, name)
}

func TestBuildWorkspaceName_SpecialChars(t *testing.T) {
	name := buildWorkspaceName("Lab Name 123!", "abc1234567", "My_Template.v2")
	// Should only contain lowercase alphanumeric and hyphens
	for _, c := range name {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
			t.Errorf("buildWorkspaceName() contains invalid char %q: %q", c, name)
		}
	}
}

func TestBuildWorkspaceName_ShortLabID(t *testing.T) {
	name := buildWorkspaceName("lab", "short", "tmpl")
	assert.NotEmpty(t, name)
}

// --- RetryJob error paths ---

func TestHandler_RetryJob_WrongMethod(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("GET", "/api/jobs/x/retry", nil)
	w := httptest.NewRecorder()
	h.RetryJob(w, req)
	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

func TestHandler_RetryJob_InvalidPath(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("POST", "/api/invalid/x/retry", nil)
	w := httptest.NewRecorder()
	h.RetryJob(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandler_RetryJob_JobNotFound(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("POST", "/api/jobs/nonexistent/retry", nil)
	w := httptest.NewRecorder()
	h.RetryJob(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestHandler_RetryJob_NotFailed(t *testing.T) {
	jm := NewJobManager("")
	id := jm.CreateJob(&LabConfig{StackName: "test"})
	// Status is pending, not failed
	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("POST", "/api/jobs/"+id+"/retry", nil)
	w := httptest.NewRecorder()
	h.RetryJob(w, req)
	// Returns HTML error, not a 4xx
	assert.Contains(t, w.Body.String(), "not in failed")
}

// --- DeleteLab error paths ---

func TestHandler_DeleteLab_WrongMethod(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("GET", "/api/labs/x/delete", nil)
	w := httptest.NewRecorder()
	h.DeleteLab(w, req)
	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

func TestHandler_DeleteLab_InvalidPath(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("POST", "/api/invalid/x/delete", nil)
	w := httptest.NewRecorder()
	h.DeleteLab(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandler_DeleteLab_JobNotFound(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("POST", "/api/labs/nonexistent/delete", nil)
	w := httptest.NewRecorder()
	h.DeleteLab(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// --- DeleteWorkspace error paths ---

func TestHandler_DeleteWorkspace_WrongMethod(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("GET", "/api/workspaces/delete", nil)
	w := httptest.NewRecorder()
	h.DeleteWorkspace(w, req)
	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

// --- GetAzureLocations WrongMethod ---

func TestHandler_GetAzureLocations_WrongMethod(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("POST", "/api/azure/locations", nil)
	w := httptest.NewRecorder()
	h.GetAzureLocations(w, req)
	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

// --- ListProviders ---

func TestHandler_ListProviders(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("GET", "/api/providers", nil)
	w := httptest.NewRecorder()
	h.ListProviders(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
}

// --- ListLabWorkspaces path tests ---

func TestHandler_ListLabWorkspaces_InvalidPath(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("GET", "/api/invalid/path/workspaces", nil)
	w := httptest.NewRecorder()
	h.ListLabWorkspaces(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandler_ListLabWorkspaces_JobNotFound(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("GET", "/api/labs/nonexistent/workspaces", nil)
	w := httptest.NewRecorder()
	h.ListLabWorkspaces(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// --- DetectTemplateVariables ---

func TestHandler_DetectTemplateVariables_WrongMethod(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("GET", "/api/detect-variables", nil)
	w := httptest.NewRecorder()
	h.DetectTemplateVariables(w, req)
	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

// --- processLabRequest (via CreateLab/DryRunLab) ---

func TestHandler_CreateLab_BYOKRequiresKubeconfig(t *testing.T) {
	jm := NewJobManager(t.TempDir())
	pe := NewPulumiExecutor(jm, t.TempDir())
	h := NewHandler(jm, pe, NewCredentialsManager(), nil, nil, nil)

	form := url.Values{}
	form.Set("use_existing_cluster", "true")
	form.Set("stack_name", "test-stack")
	req := httptest.NewRequest("POST", "/api/labs", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.CreateLab(w, req)
	// Workspace templates default automatically, so the first BYOK error is the
	// missing kubeconfig.
	assert.Contains(t, w.Body.String(), "Kubeconfig")
}

func TestHandler_DryRunLab_BYOKNoTemplates(t *testing.T) {
	jm := NewJobManager(t.TempDir())
	pe := NewPulumiExecutor(jm, t.TempDir())
	h := NewHandler(jm, pe, NewCredentialsManager(), nil, nil, nil)

	form := url.Values{}
	form.Set("use_existing_cluster", "true")
	req := httptest.NewRequest("POST", "/api/labs/dry-run", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.DryRunLab(w, req)
	assert.Contains(t, w.Body.String(), "Kubeconfig")
}

func TestHandler_CreateLab_BYOKGitTemplate_NoKubeconfig(t *testing.T) {
	jm := NewJobManager(t.TempDir())
	pe := NewPulumiExecutor(jm, t.TempDir())
	h := NewHandler(jm, pe, NewCredentialsManager(), nil, nil, nil)

	form := url.Values{}
	form.Set("use_existing_cluster", "true")
	form.Set("stack_name", "test-stack")
	form.Set("template_0_name", "my-tmpl")
	form.Set("template_0_source", "git")
	form.Set("template_0_git_repo", "https://github.com/example/repo")
	req := httptest.NewRequest("POST", "/api/labs", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.CreateLab(w, req)
	// Exercises processLabRequest up to saveUploadedTemplateFiles;
	// URL-encoded forms can't supply file uploads so we get a template upload error
	body := w.Body.String()
	assert.Contains(t, body, "error-message")
}

func TestHandler_CreateLab_BYOKGitTemplate_MultipartForm(t *testing.T) {
	jm := NewJobManager(t.TempDir())
	pe := NewPulumiExecutor(jm, t.TempDir())
	h := NewHandler(jm, pe, NewCredentialsManager(), nil, nil, nil)

	// Use multipart form so saveUploadedTemplateFiles works correctly
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	w.WriteField("use_existing_cluster", "true")
	w.WriteField("stack_name", "test-stack")
	w.WriteField("template_0_name", "my-tmpl")
	w.WriteField("template_0_source", "git")
	w.WriteField("template_0_git_repo", "https://github.com/example/repo")
	w.WriteField("kubeconfig_content", "apiVersion: v1\nkind: Config\nclusters: []\ncontexts: []\nusers: []")
	w.Close()

	req := httptest.NewRequest("POST", "/api/labs", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	wr := httptest.NewRecorder()
	h.CreateLab(wr, req)
	// With multipart form: saveUploadedTemplateFiles succeeds (ErrMissingFile → continue)
	// → executeLabJobWithID is called → returns job HTML
	body := wr.Body.String()
	assert.True(t, strings.Contains(body, "job") || strings.Contains(body, "Job Created"), "expected job response, got: "+body)

	// Wait for background goroutine to finish before t.TempDir cleanup runs.
	// Without this, the goroutine may still be writing job subdirectories when
	// cleanup tries to remove the temp dir, causing ENOTEMPTY.
	waitForJobsTerminal(jm, 5*time.Second)
}

func TestHandler_DryRunLab_BYOKGitTemplate_MultipartForm(t *testing.T) {
	jm := NewJobManager(t.TempDir())
	pe := NewPulumiExecutor(jm, t.TempDir())
	h := NewHandler(jm, pe, NewCredentialsManager(), nil, nil, nil)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("use_existing_cluster", "true")
	mw.WriteField("stack_name", "dry-run-stack")
	mw.WriteField("template_0_name", "my-tmpl")
	mw.WriteField("template_0_source", "git")
	mw.WriteField("template_0_git_repo", "https://github.com/example/repo")
	mw.WriteField("kubeconfig_content", "apiVersion: v1")
	mw.Close()

	req := httptest.NewRequest("POST", "/api/labs/dry-run", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	wr := httptest.NewRecorder()
	h.DryRunLab(wr, req)
	body := wr.Body.String()
	assert.True(t, strings.Contains(body, "job") || strings.Contains(body, "Dry Run") || strings.Contains(body, "Job Created"))
}

// --- ServeCredentials ---

func TestHandler_ServeCredentials(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("GET", "/credentials", nil)
	w := httptest.NewRecorder()
	h.ServeCredentials(w, req)
	// Template will fail (no web/ dir), but the handler body is exercised.
}

// --- Credentials GetOVHCredentials path ---

func TestHandler_GetOVHCredentials_ViaLabsPath(t *testing.T) {
	cm := NewCredentialsManager()
	// Set OVH credentials
	cm.SetCredentials(&OVHCredentials{
		ApplicationKey:    "key",
		ApplicationSecret: "secret",
		ConsumerKey:       "consumer",
		ServiceName:       "service",
		Endpoint:          "endpoint",
	})
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, cm, nil, nil, nil)
	req := httptest.NewRequest("GET", "/api/ovh-credentials", nil)
	w := httptest.NewRecorder()
	h.GetOVHCredentials(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	// Verify secrets are not exposed
	assert.NotContains(t, w.Body.String(), "secret")
}

// --- SetCredentials Azure path ---

func TestHandler_SetCredentials_AzureProvider(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	form := url.Values{}
	form.Set("provider", "azure")
	form.Set("azure_client_id", "my-client")
	form.Set("azure_client_secret", "secret")
	form.Set("azure_tenant_id", "tenant")
	form.Set("azure_subscription_id", "sub")
	req := httptest.NewRequest("POST", "/api/credentials", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.SetCredentials(w, req)
	assert.Contains(t, w.Body.String(), "Azure credentials saved")
}

func TestHandler_SetCredentials_UnknownProvider(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	form := url.Values{}
	form.Set("provider", "unknown-cloud")
	req := httptest.NewRequest("POST", "/api/credentials", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.SetCredentials(w, req)
	assert.Contains(t, w.Body.String(), "Unsupported Provider")
}

func TestHandler_SetCredentials_WrongMethod(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("GET", "/api/credentials", nil)
	w := httptest.NewRecorder()
	h.SetCredentials(w, req)
	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

// --- GetCredentials paths ---

func TestHandler_GetCredentials_AzureConfigured(t *testing.T) {
	cm := NewCredentialsManager()
	cm.SetCredentials(&AzureCredentials{
		ClientID:       "id",
		ClientSecret:   "secret",
		TenantID:       "tenant",
		SubscriptionID: "sub",
	})
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, cm, nil, nil, nil)
	req := httptest.NewRequest("GET", "/api/credentials?provider=azure", nil)
	w := httptest.NewRecorder()
	h.GetCredentials(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	// The actual client_secret value should not be returned
	assert.NotContains(t, w.Body.String(), "\"client_secret\"")
}

func TestHandler_GetCredentials_NotConfigured(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("GET", "/api/credentials?provider=azure", nil)
	w := httptest.NewRecorder()
	h.GetCredentials(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// --- ServeCredentials ---

func TestHandler_ServeCredentials_DefaultProvider(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("GET", "/credentials", nil)
	w := httptest.NewRecorder()
	h.ServeCredentials(w, req)
	// Template will fail; but handler body runs (not-configured OVH path)
}

func TestHandler_ServeCredentials_OVHConfigured(t *testing.T) {
	cm := NewCredentialsManager()
	cm.SetCredentials(&OVHCredentials{ServiceName: "svc", Endpoint: "ovh-eu"})
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, cm, nil, nil, nil)
	req := httptest.NewRequest("GET", "/credentials?provider=ovh", nil)
	w := httptest.NewRecorder()
	h.ServeCredentials(w, req)
}

func TestHandler_ServeCredentials_AzureConfigured(t *testing.T) {
	cm := NewCredentialsManager()
	cm.SetCredentials(&AzureCredentials{SubscriptionID: "sub", TenantID: "tenant"})
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, cm, nil, nil, nil)
	req := httptest.NewRequest("GET", "/credentials?provider=azure", nil)
	w := httptest.NewRecorder()
	h.ServeCredentials(w, req)
}

// --- GetCredentials default OVH ---

func TestHandler_GetCredentials_DefaultOVH_NotConfigured(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("GET", "/api/credentials", nil) // no ?provider
	w := httptest.NewRecorder()
	h.GetCredentials(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestHandler_GetCredentials_WrongMethod(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("POST", "/api/credentials?provider=ovh", nil)
	w := httptest.NewRecorder()
	h.GetCredentials(w, req)
	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

// --- RequestWorkspace error paths ---

func TestHandler_RequestWorkspace_MissingEmail(t *testing.T) {
	jm := NewJobManager("")
	id := jm.CreateJob(&LabConfig{StackName: "test"})
	jm.UpdateJobStatus(id, JobStatusCompleted)

	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	form := strings.NewReader("lab_id=" + id + "&email=invalid")
	req := httptest.NewRequest("POST", "/api/workspace/request", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.RequestWorkspace(w, req)
	// "invalid" is not a valid email, should fail
	// Can't easily test the success case without Coder
}
// The lab's public base URL is resolved through the workspace backend: a lab with
// no domain of its own is exposed via nip.io on the ingress LoadBalancer IP, which
// is only known at runtime.
func TestGetCoderCredentials_BaseURL(t *testing.T) {
	tests := []struct {
		name          string
		labDomain     string
		routingDomain string
		routingScheme string
		wantURL       string
	}{
		{"configured domain is served over HTTPS", "lab.example.com", "", "", "https://lab.example.com"},
		{"domainless lab falls back to nip.io over HTTP", "", "1.2.3.4.nip.io", "http", "http://1.2.3.4.nip.io"},
		{"no ingress IP means no public URL", "", "", "https", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jm := NewJobManager("")
			id := jm.CreateJob(&LabConfig{
				StackName: "test", Domain: tt.labDomain, WorkspaceNamespace: "workshops",
			})
			jm.UpdateJobStatus(id, JobStatusCompleted)
			job, _ := jm.GetJob(id)
			job.mu.Lock()
			job.Kubeconfig = "fake-kubeconfig"
			job.mu.Unlock()

			h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
			useFakeBackend(h, &fakeBackend{routingDomain: tt.routingDomain, routingScheme: tt.routingScheme})

			req := httptest.NewRequest(http.MethodGet, "/api/labs/"+id+"/coder-credentials", nil)
			w := httptest.NewRecorder()
			h.GetCoderCredentials(w, req)

			require.Equal(t, http.StatusOK, w.Code)
			var body map[string]string
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
			assert.Equal(t, tt.wantURL, body["url"])
			assert.Equal(t, "workshops", body["namespace"])
		})
	}
}
