package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newUploadTestHandler builds a Handler backed by an in-memory JobManager.
func newUploadTestHandler(t *testing.T) (*Handler, *JobManager) {
	t.Helper()
	jm := NewJobManager("")
	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	return h, jm
}

// completedLab creates a completed lab, optionally pre-seeded with templates, and
// returns its id.
func completedLab(t *testing.T, jm *JobManager, templates ...WorkspaceTemplate) string {
	t.Helper()
	id := jm.CreateJob(&LabConfig{StackName: "test", WorkspaceTemplates: templates})
	jm.UpdateJobStatus(id, JobStatusCompleted)
	return id
}

// postUpload drives UploadTemplateToLab with a urlencoded body.
func postUpload(h *Handler, id, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/labs/"+id+"/templates/upload", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.UploadTemplateToLab(rec, req)
	return rec
}

// labTemplateNames returns the names of the templates currently on a lab.
func labTemplateNames(t *testing.T, jm *JobManager, id string) []string {
	t.Helper()
	job, ok := jm.GetJob(id)
	require.True(t, ok, "job %s should exist", id)
	job.mu.RLock()
	defer job.mu.RUnlock()
	names := make([]string, 0, len(job.Config.WorkspaceTemplates))
	for _, tmpl := range job.Config.WorkspaceTemplates {
		names = append(names, tmpl.Name)
	}
	return names
}

func TestUploadTemplateToLab_FormMode(t *testing.T) {
	h, jm := newUploadTestHandler(t)
	id := completedLab(t, jm)

	body := url.Values{
		"templates_mode":       {"form"},
		"template_0_name":      {"docker"},
		"template_0_image":     {"codercom/code-server:latest"},
		"template_0_git_repo":  {"https://gitlab.com/o/r.git"},
		"template_0_cpu":       {"500m"},
		"template_0_env_name":  {"FOO"},
		"template_0_env_value": {"bar"},
	}.Encode()

	rec := postUpload(h, id, body)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	assert.JSONEq(t, `{"status":"ok","template":"docker","templates":["docker"]}`, rec.Body.String())

	job, _ := jm.GetJob(id)
	job.mu.RLock()
	defer job.mu.RUnlock()
	require.Len(t, job.Config.WorkspaceTemplates, 1)
	got := job.Config.WorkspaceTemplates[0]
	assert.Equal(t, "docker", got.Name)
	assert.Equal(t, "codercom/code-server:latest", got.Image)
	assert.Equal(t, "500m", got.CPU)
	assert.Equal(t, "bar", got.Env["FOO"])
}

func TestUploadTemplateToLab_YamlModeMultiple(t *testing.T) {
	h, jm := newUploadTestHandler(t)
	id := completedLab(t, jm)

	yaml := "workspace_templates:\n  - name: one\n  - name: two\n"
	body := url.Values{
		"templates_mode": {"yaml"},
		"templates_yaml": {yaml},
	}.Encode()

	rec := postUpload(h, id, body)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	assert.Equal(t, []string{"one", "two"}, labTemplateNames(t, jm, id))
}

func TestUploadTemplateToLab_LegacyFlatFields(t *testing.T) {
	h, jm := newUploadTestHandler(t)
	id := completedLab(t, jm)

	body := url.Values{
		"template_name":  {"legacy"},
		"template_image": {"img:2"},
	}.Encode()

	rec := postUpload(h, id, body)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	assert.Equal(t, []string{"legacy"}, labTemplateNames(t, jm, id))
}

func TestUploadTemplateToLab_DuplicateName(t *testing.T) {
	h, jm := newUploadTestHandler(t)
	id := completedLab(t, jm, WorkspaceTemplate{Name: "default"})

	body := url.Values{
		"templates_mode":  {"form"},
		"template_0_name": {"default"},
	}.Encode()

	rec := postUpload(h, id, body)
	require.Equal(t, http.StatusConflict, rec.Code)
	// The original template is untouched.
	assert.Equal(t, []string{"default"}, labTemplateNames(t, jm, id))
}

func TestUploadTemplateToLab_ValidationError(t *testing.T) {
	h, jm := newUploadTestHandler(t)
	id := completedLab(t, jm)

	body := url.Values{
		"templates_mode":  {"form"},
		"template_0_name": {"bad-ide"},
		"template_0_ide":  {"emacs"}, // only code-server is accepted
	}.Encode()

	rec := postUpload(h, id, body)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Empty(t, labTemplateNames(t, jm, id))
}

func TestUploadTemplateToLab_NotCompleted(t *testing.T) {
	h, jm := newUploadTestHandler(t)
	id := jm.CreateJob(&LabConfig{StackName: "test"}) // left in default (non-completed) status

	body := url.Values{"templates_mode": {"form"}, "template_0_name": {"x"}}.Encode()
	rec := postUpload(h, id, body)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "not ready")
}

func TestUploadTemplateToLab_UnknownLab(t *testing.T) {
	h, _ := newUploadTestHandler(t)

	body := url.Values{"templates_mode": {"form"}, "template_0_name": {"x"}}.Encode()
	rec := postUpload(h, "does-not-exist", body)
	require.Equal(t, http.StatusNotFound, rec.Code)
}
