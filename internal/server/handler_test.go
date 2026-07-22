package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"easylab/internal/providers/workspace"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetFormKeys(t *testing.T) {
	tests := []struct {
		name     string
		postForm url.Values
		form     url.Values
		want     []string
	}{
		{
			name:     "empty forms",
			postForm: url.Values{},
			form:     url.Values{},
			want:     []string{},
		},
		{
			name: "postForm only",
			postForm: url.Values{
				"key1": []string{"value1"},
				"key2": []string{"value2"},
			},
			form: url.Values{},
			want: []string{"key1", "key2"},
		},
		{
			name:     "form only",
			postForm: url.Values{},
			form: url.Values{
				"key3": []string{"value3"},
			},
			want: []string{"key3"},
		},
		{
			name: "both forms with duplicates",
			postForm: url.Values{
				"key1": []string{"value1"},
			},
			form: url.Values{
				"key1": []string{"value1"}, // duplicate
				"key2": []string{"value2"},
			},
			want: []string{"key1", "key2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := &http.Request{
				PostForm: tt.postForm,
				Form:     tt.form,
			}

			got := getFormKeys(r)

			// Convert to map for easier comparison
			gotMap := make(map[string]bool)
			for _, k := range got {
				gotMap[k] = true
			}

			// Check all expected keys are present
			for _, k := range tt.want {
				if !gotMap[k] {
					t.Errorf("getFormKeys() missing key %s", k)
				}
			}

			// Check no extra keys
			if len(got) != len(tt.want) {
				t.Errorf("getFormKeys() got %d keys, want %d", len(got), len(tt.want))
			}
		})
	}
}

func TestNewHandler(t *testing.T) {
	jm := NewJobManager("")
	pe := &PulumiExecutor{}
	cm := NewCredentialsManager()

	h := NewHandler(jm, pe, cm, nil, nil, nil)

	if h == nil {
		t.Fatal("NewHandler() returned nil")
	}

	if h.jobManager != jm {
		t.Error("NewHandler() jobManager mismatch")
	}

	if h.pulumiExec != pe {
		t.Error("NewHandler() pulumiExec mismatch")
	}

	if h.credentialsManager != cm {
		t.Error("NewHandler() credentialsManager mismatch")
	}

	if h.templates == nil {
		t.Error("NewHandler() templates is nil")
	}
}

func TestHandler_ServeUI_NotRoot(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

	req := httptest.NewRequest("GET", "/notroot", nil)
	w := httptest.NewRecorder()

	h.ServeUI(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("ServeUI() status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestHandler_CreateLab_WrongMethod(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

	req := httptest.NewRequest("GET", "/api/labs", nil)
	w := httptest.NewRecorder()

	h.CreateLab(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("CreateLab() status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandler_DryRunLab_WrongMethod(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

	req := httptest.NewRequest("GET", "/api/labs/dry-run", nil)
	w := httptest.NewRecorder()

	h.DryRunLab(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("DryRunLab() status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandler_LaunchLab_WrongMethod(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

	req := httptest.NewRequest("GET", "/api/labs/launch", nil)
	w := httptest.NewRecorder()

	h.LaunchLab(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("LaunchLab() status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandler_LaunchLab_MissingJobID(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

	req := httptest.NewRequest("POST", "/api/labs/launch", nil)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	h.LaunchLab(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("LaunchLab() status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandler_LaunchLab_JobNotFound(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

	form := url.Values{}
	form.Set("job_id", "nonexistent")
	req := httptest.NewRequest("POST", "/api/labs/launch", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	h.LaunchLab(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("LaunchLab() status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestHandler_GetJobStatus_InvalidPath(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

	req := httptest.NewRequest("GET", "/invalid/path", nil)
	w := httptest.NewRecorder()

	h.GetJobStatus(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("GetJobStatus() status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandler_GetJobStatus_JobNotFound(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

	req := httptest.NewRequest("GET", "/api/jobs/nonexistent/status", nil)
	w := httptest.NewRecorder()

	h.GetJobStatus(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("GetJobStatus() status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestHandler_GetJobStatus_Found(t *testing.T) {
	jm := NewJobManager("")
	config := &LabConfig{StackName: "test"}
	jobID := jm.CreateJob(config)

	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

	req := httptest.NewRequest("GET", "/api/jobs/"+jobID+"/status", nil)
	w := httptest.NewRecorder()

	h.GetJobStatus(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("GetJobStatus() status = %d, want %d", w.Code, http.StatusOK)
	}

	if !strings.Contains(w.Body.String(), "pending") {
		t.Error("GetJobStatus() response should contain 'pending' status")
	}
}

func TestHandler_GetJobStatusJSON_InvalidPath(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

	req := httptest.NewRequest("GET", "/invalid/path", nil)
	w := httptest.NewRecorder()

	h.GetJobStatusJSON(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("GetJobStatusJSON() status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandler_GetJobStatusJSON_JobNotFound(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

	req := httptest.NewRequest("GET", "/api/jobs/nonexistent", nil)
	w := httptest.NewRecorder()

	h.GetJobStatusJSON(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("GetJobStatusJSON() status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestHandler_GetJobStatusJSON_Found(t *testing.T) {
	jm := NewJobManager("")
	config := &LabConfig{StackName: "test"}
	jobID := jm.CreateJob(config)

	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

	req := httptest.NewRequest("GET", "/api/jobs/"+jobID, nil)
	w := httptest.NewRecorder()

	h.GetJobStatusJSON(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("GetJobStatusJSON() status = %d, want %d", w.Code, http.StatusOK)
	}

	contentType := w.Header().Get("Content-Type")
	if contentType != "application/json" {
		t.Errorf("GetJobStatusJSON() Content-Type = %s, want application/json", contentType)
	}
}

func TestHandler_DownloadKubeconfig_InvalidPath(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

	req := httptest.NewRequest("GET", "/invalid/path", nil)
	w := httptest.NewRecorder()

	h.DownloadKubeconfig(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("DownloadKubeconfig() status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandler_DownloadKubeconfig_JobNotFound(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

	req := httptest.NewRequest("GET", "/api/jobs/nonexistent/kubeconfig", nil)
	w := httptest.NewRecorder()

	h.DownloadKubeconfig(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("DownloadKubeconfig() status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestHandler_DownloadKubeconfig_NotCompleted(t *testing.T) {
	jm := NewJobManager("")
	config := &LabConfig{StackName: "test"}
	jobID := jm.CreateJob(config) // Status is pending, not completed

	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

	req := httptest.NewRequest("GET", "/api/jobs/"+jobID+"/kubeconfig", nil)
	w := httptest.NewRecorder()

	h.DownloadKubeconfig(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("DownloadKubeconfig() status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandler_DownloadKubeconfig_NoKubeconfig(t *testing.T) {
	jm := NewJobManager("")
	config := &LabConfig{StackName: "test"}
	jobID := jm.CreateJob(config)
	jm.UpdateJobStatus(jobID, JobStatusCompleted)

	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

	req := httptest.NewRequest("GET", "/api/jobs/"+jobID+"/kubeconfig", nil)
	w := httptest.NewRecorder()

	h.DownloadKubeconfig(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("DownloadKubeconfig() status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestHandler_DownloadKubeconfig_Success(t *testing.T) {
	jm := NewJobManager("")
	config := &LabConfig{StackName: "test"}
	jobID := jm.CreateJob(config)
	jm.UpdateJobStatus(jobID, JobStatusCompleted)
	jm.SetKubeconfig(jobID, "apiVersion: v1\nkind: Config")

	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

	req := httptest.NewRequest("GET", "/api/jobs/"+jobID+"/kubeconfig", nil)
	w := httptest.NewRecorder()

	h.DownloadKubeconfig(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("DownloadKubeconfig() status = %d, want %d", w.Code, http.StatusOK)
	}

	contentType := w.Header().Get("Content-Type")
	if contentType != "application/x-yaml" {
		t.Errorf("DownloadKubeconfig() Content-Type = %s, want application/x-yaml", contentType)
	}

	contentDisp := w.Header().Get("Content-Disposition")
	if !strings.Contains(contentDisp, "attachment") {
		t.Error("DownloadKubeconfig() missing attachment Content-Disposition")
	}
}

func TestHandler_DownloadKubeconfig_FailedJobWithKubeconfig(t *testing.T) {
	jm := NewJobManager("")
	config := &LabConfig{StackName: "test"}
	jobID := jm.CreateJob(config)
	jm.UpdateJobStatus(jobID, JobStatusFailed)
	jm.SetKubeconfig(jobID, "apiVersion: v1\nkind: Config")

	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

	req := httptest.NewRequest("GET", "/api/jobs/"+jobID+"/kubeconfig", nil)
	w := httptest.NewRecorder()

	h.DownloadKubeconfig(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("DownloadKubeconfig() status = %d, want %d", w.Code, http.StatusOK)
	}

	contentType := w.Header().Get("Content-Type")
	if contentType != "application/x-yaml" {
		t.Errorf("DownloadKubeconfig() Content-Type = %s, want application/x-yaml", contentType)
	}

	contentDisp := w.Header().Get("Content-Disposition")
	if !strings.Contains(contentDisp, "attachment") {
		t.Error("DownloadKubeconfig() missing attachment Content-Disposition")
	}
}

func TestHandler_DownloadKubeconfig_SuccessViaLabsPath(t *testing.T) {
	jm := NewJobManager("")
	config := &LabConfig{StackName: "test"}
	jobID := jm.CreateJob(config)
	jm.UpdateJobStatus(jobID, JobStatusCompleted)
	jm.SetKubeconfig(jobID, "apiVersion: v1\nkind: Config")

	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

	// Labs list page uses /api/labs/{id}/kubeconfig
	req := httptest.NewRequest("GET", "/api/labs/"+jobID+"/kubeconfig", nil)
	w := httptest.NewRecorder()

	h.DownloadKubeconfig(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("DownloadKubeconfig() via /api/labs/ status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestHandler_ListLabs(t *testing.T) {
	jm := NewJobManager("")

	// Create some jobs with different statuses
	config := &LabConfig{StackName: "test"}
	jobID1 := jm.CreateJob(config)
	jm.UpdateJobStatus(jobID1, JobStatusCompleted)

	jm.CreateJob(config) // Pending job - should not be in list

	jobID3 := jm.CreateJob(config)
	jm.UpdateJobStatus(jobID3, JobStatusCompleted)

	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

	req := httptest.NewRequest("GET", "/api/labs", nil)
	w := httptest.NewRecorder()

	h.ListLabs(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("ListLabs() status = %d, want %d", w.Code, http.StatusOK)
	}

	contentType := w.Header().Get("Content-Type")
	if contentType != "application/json" {
		t.Errorf("ListLabs() Content-Type = %s, want application/json", contentType)
	}
}

func TestHandler_RequestWorkspace_WrongMethod(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

	req := httptest.NewRequest("GET", "/api/workspace/request", nil)
	w := httptest.NewRecorder()

	h.RequestWorkspace(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("RequestWorkspace() status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandler_SetOVHCredentials_WrongMethod(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

	req := httptest.NewRequest("GET", "/api/ovh-credentials", nil)
	w := httptest.NewRecorder()

	h.SetOVHCredentials(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("SetOVHCredentials() status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandler_GetOVHCredentials_WrongMethod(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

	req := httptest.NewRequest("POST", "/api/ovh-credentials", nil)
	w := httptest.NewRecorder()

	h.GetOVHCredentials(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GetOVHCredentials() status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandler_GetOVHCredentials_NotConfigured(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

	req := httptest.NewRequest("GET", "/api/ovh-credentials", nil)
	w := httptest.NewRecorder()

	h.GetOVHCredentials(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("GetOVHCredentials() status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestHandler_GetOVHCredentials_Configured(t *testing.T) {
	cm := NewCredentialsManager()
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

	if w.Code != http.StatusOK {
		t.Errorf("GetOVHCredentials() status = %d, want %d", w.Code, http.StatusOK)
	}

	// Should not expose secrets
	body := w.Body.String()
	if strings.Contains(body, "secret") {
		t.Error("GetOVHCredentials() should not expose ApplicationSecret")
	}
}

func TestHandler_DestroyStack_WrongMethod(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

	req := httptest.NewRequest("GET", "/api/stack/destroy", nil)
	w := httptest.NewRecorder()

	h.DestroyStack(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("DestroyStack() status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandler_DestroyStack_MissingJobID(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

	req := httptest.NewRequest("POST", "/api/stack/destroy", nil)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	h.DestroyStack(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("DestroyStack() status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandler_DestroyStack_JobNotFound(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

	form := url.Values{}
	form.Set("job_id", "nonexistent")
	req := httptest.NewRequest("POST", "/api/stack/destroy", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	h.DestroyStack(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("DestroyStack() status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestHandler_RecreateLab_WrongMethod(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

	req := httptest.NewRequest("GET", "/api/labs/recreate", nil)
	w := httptest.NewRecorder()

	h.RecreateLab(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("RecreateLab() status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandler_RecreateLab_MissingJobID(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

	req := httptest.NewRequest("POST", "/api/labs/recreate", nil)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	h.RecreateLab(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("RecreateLab() status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandler_RecreateLab_JobNotFound(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

	form := url.Values{}
	form.Set("job_id", "nonexistent")
	req := httptest.NewRequest("POST", "/api/labs/recreate", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	h.RecreateLab(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("RecreateLab() status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestHandler_ServeStatic_DirectoryTraversal(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

	req := httptest.NewRequest("GET", "/static/../../../etc/passwd", nil)
	w := httptest.NewRecorder()

	h.ServeStatic(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("ServeStatic() status = %d, want %d for directory traversal", w.Code, http.StatusForbidden)
	}
}

func TestHandler_DestroyStack_FailedJob(t *testing.T) {
	// Test that destroy works for failed jobs
	jm := NewJobManager("")
	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

	// Create a job with failed status
	jobID := jm.CreateJob(&LabConfig{
		StackName: "test-stack",
	})
	jm.UpdateJobStatus(jobID, JobStatusFailed)

	form := url.Values{}
	form.Set("job_id", jobID)
	req := httptest.NewRequest("POST", "/api/stack/destroy", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	h.DestroyStack(w, req)

	// Should not return an error for wrong method or missing job
	if w.Code == http.StatusMethodNotAllowed || w.Code == http.StatusBadRequest || w.Code == http.StatusNotFound {
		t.Errorf("DestroyStack() should work for failed jobs, got status %d", w.Code)
	}
	// Note: We can't easily test the full destroy flow without mocking PulumiExecutor
}

func TestHandler_ServeStatic_NotFound(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

	req := httptest.NewRequest("GET", "/static/nonexistent.css", nil)
	w := httptest.NewRecorder()

	h.ServeStatic(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("ServeStatic() status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestHandler_RenderHTMLError(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

	w := httptest.NewRecorder()
	h.renderHTMLError(w, "Test Title", "Test Message")

	body := w.Body.String()
	if !strings.Contains(body, "Test Title") {
		t.Error("renderHTMLError() missing title")
	}
	if !strings.Contains(body, "Test Message") {
		t.Error("renderHTMLError() missing message")
	}
	if !strings.Contains(body, "error-message") {
		t.Error("renderHTMLError() missing error-message class")
	}
}

func TestHandler_RenderHTMLError_WithLink(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

	w := httptest.NewRecorder()
	h.renderHTMLError(w, "Test Title", "Test Message", `<a href="/test">Test Link</a>`)

	body := w.Body.String()
	if !strings.Contains(body, "Test Link") {
		t.Error("renderHTMLError() missing optional link")
	}
}

func TestValidateEmail(t *testing.T) {
	tests := []struct {
		name  string
		email string
		want  bool
	}{
		{"valid simple", "user@example.com", true},
		{"valid subdomain", "u@sub.domain.io", true},
		{"valid plus", "user+tag@example.com", true},
		{"empty", "", false},
		{"no at sign", "userexample.com", false},
		{"no domain", "user@", false},
		{"no tld", "user@example", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := validateEmail(tt.email); got != tt.want {
				t.Errorf("validateEmail(%q) = %v, want %v", tt.email, got, tt.want)
			}
		})
	}
}

func TestValidateURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want bool
	}{
		{"valid http", "http://example.com", true},
		{"valid https", "https://example.com/path", true},
		{"valid with port", "https://host:8080/api", true},
		{"empty", "", false},
		{"no scheme", "example.com", false},
		{"no host", "https://", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := validateURL(tt.url); got != tt.want {
				t.Errorf("validateURL(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}

func TestAtoiForm(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int
	}{
		{"valid int", "42", 42},
		{"zero", "0", 0},
		{"negative", "-5", -5},
		{"empty", "", 0},
		{"non-numeric", "abc", 0},
		{"float", "3.14", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := atoiForm(tt.input); got != tt.want {
				t.Errorf("atoiForm(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestGetFormValue(t *testing.T) {
	tests := []struct {
		name     string
		postForm url.Values
		form     url.Values
		key      string
		want     string
	}{
		{
			name:     "postForm wins",
			postForm: url.Values{"key": []string{"post-value"}},
			form:     url.Values{"key": []string{"form-value"}},
			key:      "key",
			want:     "post-value",
		},
		{
			name:     "falls back to form",
			postForm: url.Values{},
			form:     url.Values{"key": []string{"form-value"}},
			key:      "key",
			want:     "form-value",
		},
		{
			name:     "missing key returns empty",
			postForm: url.Values{},
			form:     url.Values{},
			key:      "missing",
			want:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := &http.Request{
				PostForm: tt.postForm,
				Form:     tt.form,
			}
			if got := getFormValue(r, tt.key); got != tt.want {
				t.Errorf("getFormValue(%q) = %q, want %q", tt.key, got, tt.want)
			}
		})
	}
}

func TestDeriveEncryptionKey(t *testing.T) {
	key := deriveEncryptionKey("user@example.com", "password123")
	if len(key) != 32 {
		t.Errorf("deriveEncryptionKey() key length = %d, want 32", len(key))
	}

	key2 := deriveEncryptionKey("user@example.com", "password123")
	for i := range key {
		if key[i] != key2[i] {
			t.Error("deriveEncryptionKey() is not deterministic")
			break
		}
	}

	keyDiff := deriveEncryptionKey("other@example.com", "password123")
	same := true
	for i := range key {
		if key[i] != keyDiff[i] {
			same = false
			break
		}
	}
	if same {
		t.Error("deriveEncryptionKey() same key for different emails")
	}
}

func TestEncryptWorkspacePassword(t *testing.T) {
	plaintext := "secret-workspace-pw"
	email := "user@example.com"
	studentPw := "student-pw-123"

	ciphertext, err := encryptWorkspacePassword(plaintext, email, studentPw)
	if err != nil {
		t.Fatalf("encryptWorkspacePassword() error = %v", err)
	}
	if ciphertext == "" {
		t.Error("encryptWorkspacePassword() returned empty ciphertext")
	}
	if ciphertext == plaintext {
		t.Error("encryptWorkspacePassword() ciphertext equals plaintext")
	}

	// Two encryptions of the same value should differ (GCM uses random nonce)
	ciphertext2, err := encryptWorkspacePassword(plaintext, email, studentPw)
	if err != nil {
		t.Fatalf("encryptWorkspacePassword() second call error = %v", err)
	}
	if ciphertext == ciphertext2 {
		t.Error("encryptWorkspacePassword() should produce different ciphertexts (random nonce)")
	}
}

func TestHandler_LaunchLab_DryRunCompleted(t *testing.T) {
	jm := NewJobManager(t.TempDir())
	pe := NewPulumiExecutor(jm, t.TempDir())
	id := jm.CreateJob(&LabConfig{StackName: "test"})
	jm.UpdateJobStatus(id, JobStatusDryRunCompleted)

	h := NewHandler(jm, pe, NewCredentialsManager(), nil, nil, nil)
	form := url.Values{}
	form.Set("job_id", id)
	req := httptest.NewRequest("POST", "/api/labs/launch", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.LaunchLab(w, req)
	assert.Contains(t, w.Body.String(), "Deployment Launched")

	// Wait for background goroutine to finish before t.TempDir cleanup runs.
	waitForJobsTerminal(jm, 5*time.Second)
}

func TestHandler_RequestWorkspace_NoEmail(t *testing.T) {
	// POST but no email in context → 401
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	form := url.Values{}
	form.Set("lab_id", "some-lab")
	req := httptest.NewRequest("POST", "/api/workspace/request", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.RequestWorkspace(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestHandler_RequestWorkspace_NoLabID(t *testing.T) {
	// POST with email in context but no lab_id → 400
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("POST", "/api/workspace/request", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// Inject student email into context using the auth package's context key (same package)
	ctx := context.WithValue(req.Context(), studentEmailContextKey, "student@example.com")
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	h.RequestWorkspace(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandler_RecreateLab_NotDestroyed(t *testing.T) {
	jm := NewJobManager("")
	id := jm.CreateJob(&LabConfig{StackName: "test"})
	// Status is pending, not destroyed
	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	form := url.Values{}
	form.Set("job_id", id)
	req := httptest.NewRequest("POST", "/api/labs/recreate", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.RecreateLab(w, req)
	assert.Contains(t, w.Body.String(), "not destroyed")
}

func TestParseRecreateDeletionDate(t *testing.T) {
	t.Parallel()

	future := time.Now().Add(48 * time.Hour)
	past := time.Now().Add(-48 * time.Hour)

	tests := []struct {
		name    string
		date    string
		time    string
		wantNil bool
		wantErr bool
	}{
		{name: "blank disables deletion", date: "", wantNil: true},
		{name: "future date accepted", date: future.Format("2006-01-02"), time: "23:59"},
		{name: "future date without time defaults to end of day", date: future.Format("2006-01-02")},
		{name: "past date rejected", date: past.Format("2006-01-02"), time: "23:59", wantErr: true},
		{name: "malformed date rejected", date: "not-a-date", wantErr: true},
		{name: "malformed time rejected", date: future.Format("2006-01-02"), time: "99:99", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			form := url.Values{}
			if tt.date != "" {
				form.Set("lab_deletion_date", tt.date)
			}
			if tt.time != "" {
				form.Set("lab_deletion_time", tt.time)
			}
			req := httptest.NewRequest("POST", "/api/labs/recreate", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

			got, err := parseRecreateDeletionDate(req)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			if tt.wantNil {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.True(t, got.After(time.Now()), "expected a future deletion date")
		})
	}
}

// Recreating a lab whose deletion date is being reset must reject a past date so
// the recreated lab is not destroyed on the very next cleanup tick — the whole
// point of prompting for a new date. The rejection happens before any job is
// created.
func TestHandler_RecreateLab_RejectsPastDeletionDate(t *testing.T) {
	jm := NewJobManager("")
	oldDate := time.Now().Add(-72 * time.Hour)
	id := jm.CreateJob(&LabConfig{StackName: "test", UseExistingCluster: true, LabDeletionDate: &oldDate})
	require.NoError(t, jm.UpdateJobStatus(id, JobStatusDestroyed))

	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	before := len(jm.GetAllJobs())

	form := url.Values{}
	form.Set("job_id", id)
	form.Set("lab_deletion_date", time.Now().Add(-24*time.Hour).Format("2006-01-02"))
	req := httptest.NewRequest("POST", "/api/labs/recreate", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	h.RecreateLab(w, req)

	assert.Contains(t, w.Body.String(), "future")
	assert.Equal(t, before, len(jm.GetAllJobs()), "no new job should be created when the date is invalid")
}

// Leaving the deletion date blank on recreate is the supported way to keep the
// recreated lab running: the reset clears the old (past) date rather than reusing it.
func TestParseRecreateDeletionDate_BlankClearsSchedule(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest("POST", "/api/labs/recreate", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	got, err := parseRecreateDeletionDate(req)
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestHandler_ServeLabsList(t *testing.T) {
	jm := NewJobManager("")
	jm.CreateJob(&LabConfig{StackName: "lab-a"})
	jobID := jm.CreateJob(&LabConfig{StackName: "lab-b", WorkspaceLifetimeHours: 4})
	jm.UpdateJobStatus(jobID, JobStatusCompleted)
	jm.CreateJob(&LabConfig{StackName: "lab-c"})

	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("GET", "/admin/labs", nil)
	w := httptest.NewRecorder()

	h.ServeLabsList(w, req)
	// Template loading will fail in tests (no web/ dir), but the data-building code is exercised.
}

func TestHandler_ServeUI_Root(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	h.ServeUI(w, req)
	// Template will fail to load (no web/ dir), but the root-path branch is covered.
}

func TestHandler_SetAzureADConfigurer(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	called := false
	h.SetAzureADConfigurer(func(clientID, clientSecret, tenantID string) {
		called = true
	})
	if h.azureADConfigurer == nil {
		t.Error("SetAzureADConfigurer() azureADConfigurer is nil")
	}
	h.azureADConfigurer("id", "secret", "tenant")
	if !called {
		t.Error("azureADConfigurer callback was not called")
	}
}

func TestHandler_SetClassicLoginConfigurer(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	called := false
	h.SetClassicLoginConfigurer(func(disabled bool) {
		called = true
	})
	if h.classicLoginConfigurer == nil {
		t.Error("SetClassicLoginConfigurer() classicLoginConfigurer is nil")
	}
	h.classicLoginConfigurer(true)
	if !called {
		t.Error("classicLoginConfigurer callback was not called")
	}
}

func TestHandler_SetAdminGroupIDConfigurer(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	called := false
	h.SetAdminGroupIDConfigurer(func(groupID string) {
		called = true
	})
	if h.adminGroupIDConfigurer == nil {
		t.Error("SetAdminGroupIDConfigurer() adminGroupIDConfigurer is nil")
	}
	h.adminGroupIDConfigurer("group-123")
	if !called {
		t.Error("adminGroupIDConfigurer callback was not called")
	}
}

func TestGetOVHCredentials_NotConfigured(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	w := httptest.NewRecorder()
	creds, err := h.getOVHCredentials(w)
	if err == nil {
		t.Error("getOVHCredentials() should error when not configured")
	}
	if creds != nil {
		t.Error("getOVHCredentials() should return nil creds when not configured")
	}
	// Should have rendered HTML error
	if !strings.Contains(w.Body.String(), "error-message") {
		t.Error("getOVHCredentials() should render HTML error")
	}
}

func TestGetOVHCredentials_Configured(t *testing.T) {
	cm := NewCredentialsManager()
	cm.SetCredentials(&OVHCredentials{
		ApplicationKey:    "key",
		ApplicationSecret: "secret",
		ConsumerKey:       "consumer",
		ServiceName:       "service",
		Endpoint:          "ovh-eu",
	})
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, cm, nil, nil, nil)
	w := httptest.NewRecorder()
	creds, err := h.getOVHCredentials(w)
	if err != nil {
		t.Fatalf("getOVHCredentials() error = %v", err)
	}
	if creds == nil {
		t.Fatal("getOVHCredentials() returned nil creds")
	}
	if creds.ApplicationKey != "key" {
		t.Errorf("ApplicationKey = %q, want key", creds.ApplicationKey)
	}
}

func TestCreateLabConfigFromForm_BasicOVH(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("POST", "/", nil)
	req.Form = map[string][]string{
		"stack_name":        {"my-stack"},
		"template_0_name":   {"my-template"},
		"template_0_source": {"git"},
		"coder_admin_email": {"admin@example.com"},
		"provider":          {"ovh"},
		"network_region":    {"GRA7"},
	}
	creds := &OVHCredentials{
		ApplicationKey:    "key",
		ApplicationSecret: "secret",
		ConsumerKey:       "consumer",
		ServiceName:       "service",
		Endpoint:          "ovh-eu",
	}
	cfg := h.createLabConfigFromForm(req, creds)
	if cfg.StackName != "my-stack" {
		t.Errorf("StackName = %q, want my-stack", cfg.StackName)
	}
	if cfg.OvhApplicationKey != "key" {
		t.Errorf("OvhApplicationKey = %q, want key", cfg.OvhApplicationKey)
	}
}

func TestCreateLabConfigFromForm_BYOK(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("POST", "/", nil)
	req.Form = map[string][]string{
		"stack_name":           {"byok-stack"},
		"use_existing_cluster": {"true"},
		"template_0_name":      {"tmpl"},
	}
	cfg := h.createLabConfigFromForm(req, nil)
	if !cfg.UseExistingCluster {
		t.Error("UseExistingCluster should be true for BYOK")
	}
}

func TestCreateLabConfigFromForm_AzureProvider(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("POST", "/", nil)
	req.Form = map[string][]string{
		"stack_name":      {"azure-stack"},
		"provider":        {"azure"},
		"azure_location":  {"eastus"},
		"template_0_name": {"tmpl"},
	}
	creds := &AzureCredentials{
		ClientID:       "client",
		ClientSecret:   "secret",
		TenantID:       "tenant",
		SubscriptionID: "sub",
	}
	cfg := h.createLabConfigFromForm(req, creds)
	if cfg.AzureClientID != "client" {
		t.Errorf("AzureClientID = %q, want client", cfg.AzureClientID)
	}
	if cfg.AzureLocation != "eastus" {
		t.Errorf("AzureLocation = %q, want eastus", cfg.AzureLocation)
	}
}

func TestCreateLabConfigFromForm_DefaultStackName(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("POST", "/", nil)
	req.Form = map[string][]string{
		"template_0_name": {"tmpl"},
	}
	cfg := h.createLabConfigFromForm(req, nil)
	if cfg.StackName != "dev" {
		t.Errorf("default StackName = %q, want dev", cfg.StackName)
	}
}

func TestCreateLabConfigFromForm_WorkspaceLifetimeDays(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("POST", "/", nil)
	req.Form = map[string][]string{
		"stack_name":               {"s"},
		"template_0_name":          {"tmpl"},
		"workspace_lifetime_hours": {"3"},
		"workspace_lifetime_unit":  {"days"},
	}
	cfg := h.createLabConfigFromForm(req, nil)
	if cfg.WorkspaceLifetimeHours != 72 {
		t.Errorf("WorkspaceLifetimeHours = %d, want 72 (3 days)", cfg.WorkspaceLifetimeHours)
	}
}

func TestCreateLabConfigFromForm_LabDeletionDate_Valid(t *testing.T) {
	t.Parallel()
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("POST", "/", nil)
	req.Form = map[string][]string{
		"stack_name":        {"s"},
		"template_0_name":   {"tmpl"},
		"lab_deletion_date": {"2030-12-31"},
	}
	cfg := h.createLabConfigFromForm(req, nil)
	require.NotNil(t, cfg.LabDeletionDate, "LabDeletionDate should be set for a valid date")
	assert.Equal(t, 2030, cfg.LabDeletionDate.Year())
	assert.Equal(t, 12, int(cfg.LabDeletionDate.Month()))
	assert.Equal(t, 31, cfg.LabDeletionDate.Day())
	// No time provided: defaults to 23:59
	assert.Equal(t, 23, cfg.LabDeletionDate.Hour())
	assert.Equal(t, 59, cfg.LabDeletionDate.Minute())
}

func TestCreateLabConfigFromForm_LabDeletionDate_WithTime(t *testing.T) {
	t.Parallel()
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("POST", "/", nil)
	req.Form = map[string][]string{
		"stack_name":        {"s"},
		"template_0_name":   {"tmpl"},
		"lab_deletion_date": {"2030-06-15"},
		"lab_deletion_time": {"14:30"},
	}
	cfg := h.createLabConfigFromForm(req, nil)
	require.NotNil(t, cfg.LabDeletionDate)
	assert.Equal(t, 2030, cfg.LabDeletionDate.Year())
	assert.Equal(t, 6, int(cfg.LabDeletionDate.Month()))
	assert.Equal(t, 15, cfg.LabDeletionDate.Day())
	assert.Equal(t, 14, cfg.LabDeletionDate.Hour())
	assert.Equal(t, 30, cfg.LabDeletionDate.Minute())
}

func TestCreateLabConfigFromForm_LabDeletionDate_InvalidTime(t *testing.T) {
	t.Parallel()
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("POST", "/", nil)
	req.Form = map[string][]string{
		"stack_name":        {"s"},
		"template_0_name":   {"tmpl"},
		"lab_deletion_date": {"2030-06-15"},
		"lab_deletion_time": {"not-a-time"},
	}
	cfg := h.createLabConfigFromForm(req, nil)
	require.NotNil(t, cfg.LabDeletionDate, "LabDeletionDate should still be set when only time is invalid")
	// Falls back to end-of-day default
	assert.Equal(t, 23, cfg.LabDeletionDate.Hour())
	assert.Equal(t, 59, cfg.LabDeletionDate.Minute())
}

func TestCreateLabConfigFromForm_LabDeletionDate_TimeWithoutDate(t *testing.T) {
	t.Parallel()
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("POST", "/", nil)
	req.Form = map[string][]string{
		"stack_name":        {"s"},
		"template_0_name":   {"tmpl"},
		"lab_deletion_time": {"09:00"},
	}
	cfg := h.createLabConfigFromForm(req, nil)
	assert.Nil(t, cfg.LabDeletionDate, "LabDeletionDate should be nil when date is absent even if time is set")
}

func TestCreateLabConfigFromForm_LabDeletionDate_Empty(t *testing.T) {
	t.Parallel()
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("POST", "/", nil)
	req.Form = map[string][]string{
		"stack_name":      {"s"},
		"template_0_name": {"tmpl"},
	}
	cfg := h.createLabConfigFromForm(req, nil)
	assert.Nil(t, cfg.LabDeletionDate, "LabDeletionDate should be nil when field is absent")
}

func TestCreateLabConfigFromForm_LabDeletionDate_Invalid(t *testing.T) {
	t.Parallel()
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("POST", "/", nil)
	req.Form = map[string][]string{
		"stack_name":        {"s"},
		"template_0_name":   {"tmpl"},
		"lab_deletion_date": {"not-a-date"},
	}
	cfg := h.createLabConfigFromForm(req, nil)
	assert.Nil(t, cfg.LabDeletionDate, "LabDeletionDate should be nil when date is invalid")
}

func TestParseWorkspaceTemplatesFromForm_NoTemplates(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.Form = make(map[string][]string)
	templates := parseWorkspaceTemplatesFromForm(req)
	assert.Empty(t, templates)
}

func TestParseWorkspaceTemplatesFromForm_OneTemplate(t *testing.T) {
	req := httptest.NewRequest("POST", "/", nil)
	req.Form = map[string][]string{
		"template_0_name":      {"my-template"},
		"template_0_image":     {"codercom/code-server:latest"},
		"template_0_git_repo":  {"https://github.com/example/repo"},
		"template_0_cpu":       {"500m"},
		"template_0_memory":    {"1Gi"},
		"template_0_disk_size": {"5Gi"},
	}
	templates := parseWorkspaceTemplatesFromForm(req)
	assert.Len(t, templates, 1)
	assert.Equal(t, "my-template", templates[0].Name)
	assert.Equal(t, "codercom/code-server:latest", templates[0].Image)
	assert.Equal(t, "https://github.com/example/repo", templates[0].GitRepo)
	assert.Equal(t, "500m", templates[0].CPU)
	assert.Equal(t, "1Gi", templates[0].Memory)
	assert.Equal(t, "5Gi", templates[0].DiskSize)
}

func TestParseWorkspaceTemplatesFromForm_WithEnv(t *testing.T) {
	req := httptest.NewRequest("POST", "/", nil)
	req.Form = map[string][]string{
		"template_0_name":      {"tmpl"},
		"template_0_env_name":  {"KEY1", "KEY2"},
		"template_0_env_value": {"val1", "val2"},
	}
	templates := parseWorkspaceTemplatesFromForm(req)
	assert.Len(t, templates, 1)
	assert.Equal(t, "val1", templates[0].Env["KEY1"])
	assert.Equal(t, "val2", templates[0].Env["KEY2"])
}

func TestParseWorkspaceTemplatesFromForm_GitAuthSecret(t *testing.T) {
	req := httptest.NewRequest("POST", "/", nil)
	req.Form = map[string][]string{
		"template_0_name":            {"tmpl"},
		"template_0_git_repo":        {"https://gitlab.com/o/r.git"},
		"template_0_git_auth_secret": {"gitcred"},
	}
	templates := parseWorkspaceTemplatesFromForm(req)
	assert.Len(t, templates, 1)
	assert.Equal(t, "gitcred", templates[0].GitAuthSecret)
}

func TestParseWorkspaceTemplatesFromForm_MultipleTemplates(t *testing.T) {
	req := httptest.NewRequest("POST", "/", nil)
	req.Form = map[string][]string{
		"template_0_name": {"tmpl-a"},
		"template_1_name": {"tmpl-b"},
	}
	templates := parseWorkspaceTemplatesFromForm(req)
	assert.Len(t, templates, 2)
}

func TestParseWorkspaceTemplatesFromForm_RichFields(t *testing.T) {
	req := httptest.NewRequest("POST", "/", nil)
	req.Form = map[string][]string{
		"template_0_name":           {"full"},
		"template_0_ide":            {"code-server"},
		"template_0_git_branch":     {"dev"},
		"template_0_git_folder":     {"backend"},
		"template_0_startup_script": {"sudo apt-get install -y jq"},
		"template_0_dotfiles_repo":  {"https://github.com/you/dotfiles"},
		"template_0_extensions":     {"golang.go, ms-python.python"},
		// two sidecars, index-aligned
		"template_0_sidecar_name":         {"db", "docker"},
		"template_0_sidecar_image":        {"postgres:16", "docker:dind"},
		"template_0_sidecar_ports":        {"5432", "2375"},
		"template_0_sidecar_env":          {"POSTGRES_PASSWORD=x", "DOCKER_TLS_CERTDIR="},
		"template_0_sidecar_privileged":   {"false", "true"},
		"template_0_sidecar_capabilities": {"", "SYS_ADMIN"},
		// one mount
		"template_0_mount_type": {"secret"},
		"template_0_mount_name": {"tls-cert"},
		"template_0_mount_path": {"/etc/tls"},
	}
	templates := parseWorkspaceTemplatesFromForm(req)
	assert.Len(t, templates, 1)
	tmpl := templates[0]
	assert.Equal(t, "code-server", tmpl.IDE)
	assert.Equal(t, "dev", tmpl.GitBranch)
	assert.Equal(t, "backend", tmpl.GitFolder)
	assert.Equal(t, "sudo apt-get install -y jq", tmpl.StartupScript)
	assert.Equal(t, "https://github.com/you/dotfiles", tmpl.DotfilesRepo)
	assert.Equal(t, []string{"golang.go", "ms-python.python"}, tmpl.Extensions)

	assert.Len(t, tmpl.Sidecars, 2)
	assert.Equal(t, "db", tmpl.Sidecars[0].Name)
	assert.Equal(t, "postgres:16", tmpl.Sidecars[0].Image)
	assert.Equal(t, []int{5432}, tmpl.Sidecars[0].Ports)
	assert.Equal(t, "x", tmpl.Sidecars[0].Env["POSTGRES_PASSWORD"])
	assert.False(t, tmpl.Sidecars[0].Privileged)
	assert.Equal(t, "docker", tmpl.Sidecars[1].Name)
	assert.True(t, tmpl.Sidecars[1].Privileged)
	assert.Equal(t, []string{"SYS_ADMIN"}, tmpl.Sidecars[1].Capabilities)

	assert.Len(t, tmpl.Mounts, 1)
	assert.Equal(t, "secret", tmpl.Mounts[0].Type)
	assert.Equal(t, "tls-cert", tmpl.Mounts[0].Name)
	assert.Equal(t, "/etc/tls", tmpl.Mounts[0].Path)
}

func TestGetPostFormKeys(t *testing.T) {
	tests := []struct {
		name     string
		postForm url.Values
		want     int // number of expected keys
	}{
		{"empty", url.Values{}, 0},
		{"one key", url.Values{"key1": []string{"v1"}}, 1},
		{"two keys", url.Values{"k1": []string{"v1"}, "k2": []string{"v2"}}, 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := &http.Request{PostForm: tt.postForm}
			got := getPostFormKeys(r)
			if len(got) != tt.want {
				t.Errorf("getPostFormKeys() len = %d, want %d", len(got), tt.want)
			}
		})
	}
}

func TestHandler_GetProjectStats_MissingParam(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("GET", "/api/admin/stats", nil)
	w := httptest.NewRecorder()
	h.GetProjectStats(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("GetProjectStats() missing param status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandler_GetProjectStats_WithData(t *testing.T) {
	jm := NewJobManager("")
	id1 := jm.CreateJob(&LabConfig{StackName: "project-a"})
	jm.UpdateJobStatus(id1, JobStatusCompleted)
	id2 := jm.CreateJob(&LabConfig{StackName: "project-a"})
	jm.UpdateJobStatus(id2, JobStatusFailed)

	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("GET", "/api/admin/stats?project=project-a", nil)
	w := httptest.NewRecorder()
	h.GetProjectStats(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("GetProjectStats() status = %d, want %d", w.Code, http.StatusOK)
	}
	if w.Header().Get("Content-Type") != "application/json" {
		t.Error("GetProjectStats() should return application/json")
	}
}

func TestHandler_GetProjectStats_AllProjects(t *testing.T) {
	jm := NewJobManager("")
	id1 := jm.CreateJob(&LabConfig{StackName: "proj-a"})
	jm.UpdateJobStatus(id1, JobStatusCompleted)
	id2 := jm.CreateJob(&LabConfig{StackName: "proj-b"})
	jm.UpdateJobStatus(id2, JobStatusDestroyed)

	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("GET", "/api/admin/stats?project=__all__", nil)
	w := httptest.NewRecorder()
	h.GetProjectStats(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("GetProjectStats(__all__) status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestHandler_ServeAdminStats(t *testing.T) {
	jm := NewJobManager("")
	jm.CreateJob(&LabConfig{StackName: "my-stack"})

	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("GET", "/admin/stats", nil)
	w := httptest.NewRecorder()
	h.ServeAdminStats(w, req)
	// Template will fail (no web/ dir in tests), but the project-list building is exercised.
}

func TestHandler_UpdateJobConfig(t *testing.T) {
	jm := NewJobManager("")
	id := jm.CreateJob(&LabConfig{StackName: "original"})
	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

	h.updateJobConfig(id, func(cfg *LabConfig) {
		cfg.StackName = "updated"
	})

	job, exists := jm.GetJob(id)
	if !exists {
		t.Fatal("job not found after updateJobConfig")
	}
	job.mu.RLock()
	got := job.Config.StackName
	job.mu.RUnlock()
	if got != "updated" {
		t.Errorf("updateJobConfig() StackName = %q, want %q", got, "updated")
	}
}

func TestHandler_UpdateJobConfig_NotFound(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	// Should not panic for non-existent job
	h.updateJobConfig("nonexistent", func(cfg *LabConfig) {
		cfg.StackName = "updated"
	})
}

func TestHandler_ReadKubeconfigFromForm_TextArea(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("POST", "/", strings.NewReader(url.Values{
		"kubeconfig_content": []string{"apiVersion: v1"},
	}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.ParseForm()

	content, err := h.readKubeconfigFromForm(req)
	if err != nil {
		t.Fatalf("readKubeconfigFromForm() error = %v", err)
	}
	if content != "apiVersion: v1" {
		t.Errorf("readKubeconfigFromForm() = %q, want %q", content, "apiVersion: v1")
	}
}

func TestHandler_SetClassicAdminLoginConfigurer(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	called := false
	h.SetClassicAdminLoginConfigurer(func(disabled bool) {
		called = true
	})
	if h.classicAdminLoginConfigurer == nil {
		t.Error("SetClassicAdminLoginConfigurer() classicAdminLoginConfigurer is nil")
	}
	h.classicAdminLoginConfigurer(false)
	if !called {
		t.Error("classicAdminLoginConfigurer callback was not called")
	}
}

func TestValidateDNSConfig(t *testing.T) {
	tests := []struct {
		name    string
		config  *LabConfig
		wantErr bool
	}{
		{
			name:    "no DNS provider is always valid",
			config:  &LabConfig{DNSProvider: "", DNSZone: "", Domain: ""},
			wantErr: false,
		},
		{
			name:    "provider with empty zone is rejected",
			config:  &LabConfig{DNSProvider: "ovh", DNSZone: "", Domain: "ai-bb.yodamad.fr"},
			wantErr: true,
		},
		{
			name:    "provider with whitespace-only zone is rejected",
			config:  &LabConfig{DNSProvider: "ovh", DNSZone: "   ", Domain: "ai-bb.yodamad.fr"},
			wantErr: true,
		},
		{
			name:    "domain outside the zone is rejected",
			config:  &LabConfig{DNSProvider: "ovh", DNSZone: "yodamad.fr", Domain: "ai-bb.example.com"},
			wantErr: true,
		},
		{
			name:    "subdomain inside the zone is valid",
			config:  &LabConfig{DNSProvider: "ovh", DNSZone: "yodamad.fr", Domain: "ai-bb.yodamad.fr"},
			wantErr: false,
		},
		{
			name:    "domain equal to the zone is valid",
			config:  &LabConfig{DNSProvider: "ovh", DNSZone: "yodamad.fr", Domain: "yodamad.fr"},
			wantErr: false,
		},
		{
			name:    "provider and zone with no domain is valid",
			config:  &LabConfig{DNSProvider: "ovh", DNSZone: "yodamad.fr", Domain: ""},
			wantErr: false,
		},
		{
			name:    "suffix match that is not a subdomain boundary is rejected",
			config:  &LabConfig{DNSProvider: "ovh", DNSZone: "yodamad.fr", Domain: "notyodamad.fr"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateDNSConfig(tt.config)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestHostFromWorkspaceURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{name: "empty url", url: "", want: ""},
		{name: "https host", url: "https://ws-admin-11f346ae.ai-bb.yodamad.fr/", want: "ws-admin-11f346ae.ai-bb.yodamad.fr"},
		{name: "http host", url: "http://141.95.239.107.nip.io/", want: "141.95.239.107.nip.io"},
		{name: "host with port", url: "https://foo.example.com:8443/", want: "foo.example.com"},
		{name: "unparseable", url: "https://exa mple.com/", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, hostFromWorkspaceURL(tt.url))
		})
	}
}

// fakeHostResolver is a deterministic hostResolver for testing workspaceDNSReady.
type fakeHostResolver struct {
	addrs []string
	err   error
}

func (f fakeHostResolver) LookupHost(_ context.Context, _ string) ([]string, error) {
	return f.addrs, f.err
}

func TestWorkspaceDNSReady(t *testing.T) {
	// These cases swap the package-level resolver, so they must not run in parallel.
	orig := workspaceDNSResolver
	t.Cleanup(func() { workspaceDNSResolver = orig })

	now := time.Now()
	nxdomain := fakeHostResolver{err: errors.New("no such host")}
	resolves := fakeHostResolver{addrs: []string{"141.95.239.107"}}

	tests := []struct {
		name     string
		resolver hostResolver
		ws       workspace.Workspace
		want     bool
	}{
		{
			name:     "no public host is always ready",
			resolver: nxdomain, // must not matter — never consulted
			ws:       workspace.Workspace{URL: "", UpdatedAt: now},
			want:     true,
		},
		{
			name:     "host resolves within grace is ready",
			resolver: resolves,
			ws:       workspace.Workspace{URL: "https://x.ai-bb.yodamad.fr/", UpdatedAt: now},
			want:     true,
		},
		{
			name:     "host not resolving within grace is not ready",
			resolver: nxdomain,
			ws:       workspace.Workspace{URL: "https://x.ai-bb.yodamad.fr/", UpdatedAt: now},
			want:     false,
		},
		{
			name:     "not resolving but past grace fails open",
			resolver: nxdomain,
			ws:       workspace.Workspace{URL: "https://x.ai-bb.yodamad.fr/", UpdatedAt: now.Add(-dnsPropagationGrace - time.Minute)},
			want:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workspaceDNSResolver = tt.resolver
			assert.Equal(t, tt.want, workspaceDNSReady(context.Background(), tt.ws))
		})
	}
}

func TestBuildTemplateStatus(t *testing.T) {
	templates := []WorkspaceTemplate{
		{Name: "python", Image: "python:3.12"},
		{Name: "go", IDE: "openvscode"},
		{Name: "devc", Devcontainer: &DevcontainerConfig{}},
	}

	tests := []struct {
		name             string
		templates        []WorkspaceTemplate
		workspaces       []workspace.Workspace
		wantCounts       map[string]int
		wantHasRunning   map[string]bool
		wantUnattributed int
	}{
		{
			name:             "no workspaces",
			templates:        templates,
			workspaces:       nil,
			wantCounts:       map[string]int{"python": 0, "go": 0, "devc": 0},
			wantHasRunning:   map[string]bool{"python": false, "go": false, "devc": false},
			wantUnattributed: 0,
		},
		{
			name:      "multiple per template",
			templates: templates,
			workspaces: []workspace.Workspace{
				{ID: "ws-1", Template: "python"},
				{ID: "ws-2", Template: "python"},
				{ID: "ws-3", Template: "go"},
			},
			wantCounts:       map[string]int{"python": 2, "go": 1, "devc": 0},
			wantHasRunning:   map[string]bool{"python": true, "go": true, "devc": false},
			wantUnattributed: 0,
		},
		{
			name:      "unattributed workspaces",
			templates: templates,
			workspaces: []workspace.Workspace{
				{ID: "ws-1", Template: "python"},
				{ID: "ws-2", Template: ""},        // pre-attribution workspace
				{ID: "ws-3", Template: "removed"}, // template no longer configured
			},
			wantCounts:       map[string]int{"python": 1, "go": 0, "devc": 0},
			wantHasRunning:   map[string]bool{"python": true, "go": false, "devc": false},
			wantUnattributed: 2,
		},
		{
			name:             "no templates, workspaces all unattributed",
			templates:        nil,
			workspaces:       []workspace.Workspace{{ID: "ws-1", Template: "x"}},
			wantCounts:       map[string]int{},
			wantHasRunning:   map[string]bool{},
			wantUnattributed: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			statuses, unattributed := buildTemplateStatus(tt.templates, tt.workspaces)

			require.Len(t, statuses, len(tt.templates))
			assert.Equal(t, tt.wantUnattributed, unattributed)

			for _, s := range statuses {
				assert.Equal(t, tt.wantCounts[s.Name], s.RunningCount, "count for %s", s.Name)
				assert.Equal(t, tt.wantHasRunning[s.Name], s.HasRunning, "hasRunning for %s", s.Name)
				assert.NotEmpty(t, s.IDE, "IDE should default for %s", s.Name)
			}
		})
	}
}

func TestBuildTemplateStatus_DisplayFields(t *testing.T) {
	statuses, _ := buildTemplateStatus([]WorkspaceTemplate{
		{Name: "python", Image: "python:3.12"},
		{Name: "legacy-ide", IDE: "openvscode"},
		{Name: "devc", Devcontainer: &DevcontainerConfig{}},
	}, nil)

	require.Len(t, statuses, 3)
	// Explicit image is preserved; code-server is the normalized IDE.
	assert.Equal(t, "python:3.12", statuses[0].Image)
	assert.Equal(t, workspace.DefaultIDEKind, statuses[0].IDE)
	// Legacy openvscode is normalized to code-server.
	assert.Equal(t, workspace.DefaultIDEKind, statuses[1].IDE)
	// A devcontainer template with no image is labelled "devcontainer".
	assert.Equal(t, "devcontainer", statuses[2].Image)
}
