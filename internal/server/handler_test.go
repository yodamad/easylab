package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
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

	h := NewHandler(jm, pe, cm)

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
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager())

	req := httptest.NewRequest("GET", "/notroot", nil)
	w := httptest.NewRecorder()

	h.ServeUI(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("ServeUI() status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestHandler_CreateLab_WrongMethod(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager())

	req := httptest.NewRequest("GET", "/api/labs", nil)
	w := httptest.NewRecorder()

	h.CreateLab(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("CreateLab() status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandler_DryRunLab_WrongMethod(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager())

	req := httptest.NewRequest("GET", "/api/labs/dry-run", nil)
	w := httptest.NewRecorder()

	h.DryRunLab(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("DryRunLab() status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandler_LaunchLab_WrongMethod(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager())

	req := httptest.NewRequest("GET", "/api/labs/launch", nil)
	w := httptest.NewRecorder()

	h.LaunchLab(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("LaunchLab() status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandler_LaunchLab_MissingJobID(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager())

	req := httptest.NewRequest("POST", "/api/labs/launch", nil)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	h.LaunchLab(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("LaunchLab() status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandler_LaunchLab_JobNotFound(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager())

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
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager())

	req := httptest.NewRequest("GET", "/invalid/path", nil)
	w := httptest.NewRecorder()

	h.GetJobStatus(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("GetJobStatus() status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandler_GetJobStatus_JobNotFound(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager())

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

	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager())

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
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager())

	req := httptest.NewRequest("GET", "/invalid/path", nil)
	w := httptest.NewRecorder()

	h.GetJobStatusJSON(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("GetJobStatusJSON() status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandler_GetJobStatusJSON_JobNotFound(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager())

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

	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager())

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
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager())

	req := httptest.NewRequest("GET", "/invalid/path", nil)
	w := httptest.NewRecorder()

	h.DownloadKubeconfig(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("DownloadKubeconfig() status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandler_DownloadKubeconfig_JobNotFound(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager())

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

	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager())

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

	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager())

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

	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager())

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

	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager())

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

func TestHandler_ListLabs(t *testing.T) {
	jm := NewJobManager("")

	// Create some jobs with different statuses
	config := &LabConfig{StackName: "test"}
	jobID1 := jm.CreateJob(config)
	jm.UpdateJobStatus(jobID1, JobStatusCompleted)

	jm.CreateJob(config) // Pending job - should not be in list

	jobID3 := jm.CreateJob(config)
	jm.UpdateJobStatus(jobID3, JobStatusCompleted)

	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager())

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
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager())

	req := httptest.NewRequest("GET", "/api/workspace/request", nil)
	w := httptest.NewRecorder()

	h.RequestWorkspace(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("RequestWorkspace() status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandler_SetOVHCredentials_WrongMethod(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager())

	req := httptest.NewRequest("GET", "/api/ovh-credentials", nil)
	w := httptest.NewRecorder()

	h.SetOVHCredentials(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("SetOVHCredentials() status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandler_GetOVHCredentials_WrongMethod(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager())

	req := httptest.NewRequest("POST", "/api/ovh-credentials", nil)
	w := httptest.NewRecorder()

	h.GetOVHCredentials(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GetOVHCredentials() status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandler_GetOVHCredentials_NotConfigured(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager())

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

	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, cm)

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
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager())

	req := httptest.NewRequest("GET", "/api/stack/destroy", nil)
	w := httptest.NewRecorder()

	h.DestroyStack(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("DestroyStack() status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandler_DestroyStack_MissingJobID(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager())

	req := httptest.NewRequest("POST", "/api/stack/destroy", nil)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	h.DestroyStack(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("DestroyStack() status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandler_DestroyStack_JobNotFound(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager())

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
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager())

	req := httptest.NewRequest("GET", "/api/labs/recreate", nil)
	w := httptest.NewRecorder()

	h.RecreateLab(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("RecreateLab() status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandler_RecreateLab_MissingJobID(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager())

	req := httptest.NewRequest("POST", "/api/labs/recreate", nil)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	h.RecreateLab(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("RecreateLab() status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandler_RecreateLab_JobNotFound(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager())

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
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager())

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
	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager())

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
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager())

	req := httptest.NewRequest("GET", "/static/nonexistent.css", nil)
	w := httptest.NewRecorder()

	h.ServeStatic(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("ServeStatic() status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestHandler_RenderHTMLError(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager())

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
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager())

	w := httptest.NewRecorder()
	h.renderHTMLError(w, "Test Title", "Test Message", `<a href="/test">Test Link</a>`)

	body := w.Body.String()
	if !strings.Contains(body, "Test Link") {
		t.Error("renderHTMLError() missing optional link")
	}
}
