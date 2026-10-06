package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"easylab/internal/providers/workspace"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// studentWorkspacesResponse mirrors the JSON written by ListStudentWorkspaces.
type studentWorkspacesResponse struct {
	Workspaces []studentWorkspace `json:"workspaces"`
	Incomplete bool               `json:"incomplete"`
}

// labWithBackend creates a completed lab whose kubeconfig is its stack name, so
// useBackendsByKubeconfig can hand each lab its own fake backend.
func labWithBackend(jm *JobManager, name string, lifetimeHours int) string {
	id := jm.CreateJob(&LabConfig{StackName: name, WorkspaceLifetimeHours: lifetimeHours})
	jm.UpdateJobStatus(id, JobStatusCompleted)
	job, _ := jm.GetJob(id)
	job.mu.Lock()
	job.Kubeconfig = name
	job.mu.Unlock()
	return id
}

// useBackendsByKubeconfig wires a handler to pick the fake backend by kubeconfig.
func useBackendsByKubeconfig(h *Handler, backends map[string]*fakeBackend) {
	h.newWorkspaceBackend = func(kubeconfig, _ string) (workspace.Backend, error) {
		fb, ok := backends[kubeconfig]
		if !ok {
			return nil, fmt.Errorf("no backend for %s", kubeconfig)
		}
		return fb, nil
	}
}

func listStudentWorkspaces(t *testing.T, h *Handler, email string) studentWorkspacesResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/student/workspaces", nil)
	if email != "" {
		req = asStudent(req, email)
	}
	rec := httptest.NewRecorder()
	h.ListStudentWorkspaces(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp studentWorkspacesResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	return resp
}

func TestListStudentWorkspaces(t *testing.T) {
	t.Parallel()

	older := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	aliceGo := workspace.Workspace{ID: "ws-alice-go", Name: "ws-alice-go", Owner: "alice", Template: "go", URL: "https://ws-alice-go.example.com/", Token: "secret-token-123", CreatedAt: older, Ready: true}
	alicePython := workspace.Workspace{ID: "ws-alice-py", Name: "ws-alice-py", Owner: "alice", Template: "python", URL: "https://ws-alice-py.example.com/", Token: "secret-token-456", CreatedAt: newer}
	bobGo := workspace.Workspace{ID: "ws-bob-go", Name: "ws-bob-go", Owner: "bob", Template: "go", Token: "secret-token-789", CreatedAt: newer}

	tests := []struct {
		name           string
		email          string
		backends       map[string]*fakeBackend // keyed by lab name
		wantNames      []string                // in response order
		wantIncomplete bool
	}{
		{
			name:  "only the caller's workspaces, across labs, newest first",
			email: "alice@example.com",
			backends: map[string]*fakeBackend{
				"devoxx":   {workspaces: []workspace.Workspace{aliceGo, bobGo}},
				"snowcamp": {workspaces: []workspace.Workspace{alicePython}},
			},
			wantNames: []string{"ws-alice-py", "ws-alice-go"},
		},
		{
			name:  "same identity whatever the email domain or case",
			email: "Alice@other.org",
			backends: map[string]*fakeBackend{
				"devoxx": {workspaces: []workspace.Workspace{aliceGo, bobGo}},
			},
			wantNames: []string{"ws-alice-go"},
		},
		{
			name:  "student without a workspace gets an empty list",
			email: "carol@example.com",
			backends: map[string]*fakeBackend{
				"devoxx": {workspaces: []workspace.Workspace{aliceGo, bobGo}},
			},
			wantNames: []string{},
		},
		{
			name:  "unreachable lab is skipped and reported",
			email: "alice@example.com",
			backends: map[string]*fakeBackend{
				"devoxx":   {workspaces: []workspace.Workspace{aliceGo}},
				"snowcamp": {listErr: assert.AnError},
			},
			wantNames:      []string{"ws-alice-go"},
			wantIncomplete: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h, jm := newUploadTestHandler(t)
			useBackendsByKubeconfig(h, tt.backends)
			for name := range tt.backends {
				labWithBackend(jm, name, 0)
			}

			req := asStudent(httptest.NewRequest(http.MethodGet, "/api/student/workspaces", nil), tt.email)
			rec := httptest.NewRecorder()
			h.ListStudentWorkspaces(rec, req)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.NotContains(t, rec.Body.String(), "secret-token", "the IDE token never leaves the server here")
			assert.NotContains(t, rec.Body.String(), assert.AnError.Error(), "backend errors stay server-side")

			var resp studentWorkspacesResponse
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
			require.NotNil(t, resp.Workspaces, "an empty list is [] rather than null")
			names := make([]string, 0, len(resp.Workspaces))
			for _, ws := range resp.Workspaces {
				names = append(names, ws.WorkspaceName)
				assert.Equal(t, tt.email, ws.Email)
			}
			assert.Equal(t, tt.wantNames, names)
			assert.Equal(t, tt.wantIncomplete, resp.Incomplete)
		})
	}
}

func TestListStudentWorkspaces_Entry(t *testing.T) {
	t.Parallel()

	created := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	h, jm := newUploadTestHandler(t)
	labID := labWithBackend(jm, "devoxx", 4)
	useBackendsByKubeconfig(h, map[string]*fakeBackend{"devoxx": {workspaces: []workspace.Workspace{
		{ID: "ws-alice-go", Name: "ws-alice-go", Owner: "alice", Template: "go", URL: "https://ws-alice-go.example.com/", CreatedAt: created, Ready: true},
		{ID: "ws-alice-py", Name: "ws-alice-py", Owner: "alice", Template: "python", CreatedAt: created},
	}}})
	// An access recorded for someone else's workspace of the same name is not Alice's.
	require.NoError(t, jm.RecordWorkspaceAccess(labID, "ws-alice-go", "alice"))
	require.NoError(t, jm.RecordWorkspaceAccess(labID, "ws-alice-py", "bob"))

	resp := listStudentWorkspaces(t, h, "alice@example.com")
	require.Len(t, resp.Workspaces, 2)

	first := resp.Workspaces[0]
	assert.Equal(t, labID, first.LabID)
	assert.Equal(t, "devoxx", first.LabName)
	assert.Equal(t, "ws-alice-go", first.WorkspaceName)
	assert.Equal(t, "https://ws-alice-go.example.com/", first.WorkspaceURL)
	assert.Equal(t, "go", first.Template)
	assert.True(t, first.Ready)
	assert.Equal(t, created.Format(time.RFC3339), first.CreatedAt)
	assert.Equal(t, created.Add(4*time.Hour).Format(time.RFC3339), first.DeletionAt, "the lab's 4h lifetime counts from the workspace's creation")
	assert.NotEmpty(t, first.TeacherAccessedAt)

	assert.Empty(t, resp.Workspaces[1].TeacherAccessedAt)
}

func TestListStudentWorkspaces_SkipsLabsWithoutCluster(t *testing.T) {
	t.Parallel()

	h, jm := newUploadTestHandler(t)
	fb := &fakeBackend{workspaces: []workspace.Workspace{{ID: "ws-alice", Name: "ws-alice", Owner: "alice"}}}
	useFakeBackend(h, fb)

	// Still provisioning: it has a kubeconfig but is not completed.
	pending := jm.CreateJob(&LabConfig{StackName: "pending"})
	job, _ := jm.GetJob(pending)
	job.mu.Lock()
	job.Kubeconfig = "fake-kubeconfig"
	job.mu.Unlock()
	// Completed but without a kubeconfig (destroyed).
	completedLab(t, jm)

	resp := listStudentWorkspaces(t, h, "alice@example.com")
	assert.Empty(t, resp.Workspaces)
	assert.False(t, resp.Incomplete)
	assert.Zero(t, fb.ListCalls, "a lab without a cluster is never asked")
}

func TestListStudentWorkspaces_Rejects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		method   string
		email    string
		wantCode int
	}{
		{name: "POST is not allowed", method: http.MethodPost, email: "alice@example.com", wantCode: http.StatusMethodNotAllowed},
		{name: "no identity in the session", method: http.MethodGet, wantCode: http.StatusUnauthorized},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h, jm := newUploadTestHandler(t)
			fb := &fakeBackend{}
			useFakeBackend(h, fb)
			completedLabWithKubeconfig(jm, 0)

			req := httptest.NewRequest(tt.method, "/api/student/workspaces", nil)
			if tt.email != "" {
				req = asStudent(req, tt.email)
			}
			rec := httptest.NewRecorder()
			h.ListStudentWorkspaces(rec, req)
			assert.Equal(t, tt.wantCode, rec.Code)
			assert.Zero(t, fb.ListCalls)
		})
	}
}

// A workspace's token is fixed when it is created. Asking for the same workspace
// again — typically from another device — must show that token, not the one
// generated for the new request, and must no longer plant a cookie in the browser.
func TestRequestWorkspace_ShowsExistingToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		existing  *workspace.Workspace
		wantToken string // "" means the freshly generated one
	}{
		{
			name:      "existing workspace keeps its token",
			existing:  &workspace.Workspace{ID: "ws-alice", Name: "ws-alice", Owner: "alice", Template: "go", Token: "original-token-123"},
			wantToken: "original-token-123",
		},
		{name: "new workspace shows the generated token"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h, jm := newUploadTestHandler(t)
			fb := &fakeBackend{getWS: tt.existing}
			useFakeBackend(h, fb)
			labID := closableLab(t, jm, WorkspaceTemplate{Name: "go"})

			form := url.Values{"lab_id": {labID}, "template_id": {"go"}}
			req := asStudent(postForm(t, "/api/student/workspace/request", form), "alice@example.com")
			rec := httptest.NewRecorder()
			h.RequestWorkspace(rec, req)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			require.Len(t, fb.Ensured, 1)
			wantToken := tt.wantToken
			if wantToken == "" {
				wantToken = fb.Ensured[0].Token
			}
			require.NotEmpty(t, wantToken)
			assert.Contains(t, rec.Body.String(), wantToken)
			if tt.wantToken != "" {
				assert.NotContains(t, rec.Body.String(), fb.Ensured[0].Token, "the unused generated token must not be shown")
			}
			assert.Empty(t, rec.Result().Cookies(), "workspaces are no longer saved in the browser")
			assert.NotContains(t, rec.Body.String(), "Encrypt")
		})
	}
}
