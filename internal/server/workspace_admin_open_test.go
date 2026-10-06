package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"easylab/internal/providers/workspace"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An admin opening a student's workspace gets the same self-submitting login form
// as the student, whoever owns the workspace; anything else yields no page and
// never leaks the token.
func TestHandler_OpenWorkspaceAsAdmin(t *testing.T) {
	ready := workspace.Workspace{
		ID: "ws-student", Name: "ws-student", Owner: "student", OpenURL: "https://ws.example.com/", Token: "secret-token-123",
	}
	noURL := ready
	noURL.OpenURL = ""

	tests := []struct {
		name         string
		method       string
		path         string // "%s" is replaced by the lab created for the test
		noKubeconfig bool
		backend      *fakeBackend
		wantStatus   int
		wantForm     bool
	}{
		{
			name: "opens a student's workspace", method: http.MethodPost, path: "/api/labs/%s/workspaces/ws-student/open",
			backend: &fakeBackend{getWS: &ready}, wantStatus: http.StatusOK, wantForm: true,
		},
		{
			name: "legacy jobs prefix", method: http.MethodPost, path: "/api/jobs/%s/workspaces/ws-student/open",
			backend: &fakeBackend{getWS: &ready}, wantStatus: http.StatusOK, wantForm: true,
		},
		{
			name: "wrong method", method: http.MethodGet, path: "/api/labs/%s/workspaces/ws-student/open",
			backend: &fakeBackend{getWS: &ready}, wantStatus: http.StatusMethodNotAllowed,
		},
		{
			name: "malformed path", method: http.MethodPost, path: "/api/labs/%s/workspaces/open",
			backend: &fakeBackend{getWS: &ready}, wantStatus: http.StatusBadRequest,
		},
		{
			name: "unknown lab", method: http.MethodPost, path: "/api/labs/job-missing/workspaces/ws-student/open",
			backend: &fakeBackend{getWS: &ready}, wantStatus: http.StatusNotFound,
		},
		{
			name: "lab without a cluster", method: http.MethodPost, path: "/api/labs/%s/workspaces/ws-student/open",
			noKubeconfig: true, backend: &fakeBackend{getWS: &ready}, wantStatus: http.StatusServiceUnavailable,
		},
		{
			name: "unknown workspace", method: http.MethodPost, path: "/api/labs/%s/workspaces/ws-gone/open",
			backend: &fakeBackend{getErr: fmt.Errorf("connection refused to 10.0.0.1")}, wantStatus: http.StatusServiceUnavailable,
		},
		{
			name: "workspace without a URL", method: http.MethodPost, path: "/api/labs/%s/workspaces/ws-student/open",
			backend: &fakeBackend{getWS: &noURL}, wantStatus: http.StatusServiceUnavailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			jm := NewJobManager("")
			id := jm.CreateJob(&LabConfig{StackName: "test", WorkspaceNamespace: "workshops"})
			jm.UpdateJobStatus(id, JobStatusCompleted)
			if !tt.noKubeconfig {
				job, _ := jm.GetJob(id)
				job.mu.Lock()
				job.Kubeconfig = "fake-kubeconfig"
				job.mu.Unlock()
			}
			h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
			useFakeBackend(h, tt.backend)

			path := tt.path
			if strings.Contains(path, "%s") {
				path = fmt.Sprintf(path, id)
			}
			w := httptest.NewRecorder()
			h.OpenWorkspaceAsAdmin(w, httptest.NewRequest(tt.method, path, nil))

			require.Equal(t, tt.wantStatus, w.Code)
			body := w.Body.String()
			_, recorded := jm.GetWorkspaceAccess(id, "ws-student")
			if !tt.wantForm {
				assert.NotContains(t, body, "secret-token-123")
				// Internal error details stay server-side.
				assert.NotContains(t, body, "10.0.0.1")
				assert.False(t, recorded, "a refused open must not be reported to the student")
				return
			}
			assert.Contains(t, w.Header().Get("Content-Type"), "text/html")
			assert.Contains(t, body, `action="https://ws.example.com/login"`)
			assert.Contains(t, body, `method="POST"`)
			assert.Contains(t, body, `name="password" value="secret-token-123"`)
			// The token must never end up in a URL (query string), only in the POST body.
			assert.NotContains(t, body, "login?")
			assert.True(t, recorded, "the open must be recorded for the student")
		})
	}
}

func TestHandler_OpenWorkspaceAsAdmin_RecordsAudit(t *testing.T) {
	as, err := NewAuditStore(t.TempDir())
	require.NoError(t, err)
	h, id := openWorkspaceHandler(t, workspace.Workspace{
		ID: "ws-student", Name: "ws-student", Owner: "student", OpenURL: "https://ws.example.com/", Token: "secret-token-123",
	})
	h.SetAuditStore(as)

	w := httptest.NewRecorder()
	h.OpenWorkspaceAsAdmin(w, httptest.NewRequest(http.MethodPost, "/api/labs/"+id+"/workspaces/ws-student/open", nil))
	require.Equal(t, http.StatusOK, w.Code)

	entries := waitForAuditEntry(t, as, "workspace.open")
	assert.Equal(t, id, entries[0].LabID)
	assert.Equal(t, "admin", entries[0].Role)
	assert.Equal(t, "ws-student", entries[0].Detail)
}

// Only the workspace's owner is told a teacher opened it; anyone else gets the
// same answer as for a workspace that was never opened.
func TestHandler_WorkspaceTeacherAccess(t *testing.T) {
	tests := []struct {
		name          string
		method        string
		email         string
		labID         string // "" means the lab created for the test
		workspaceName string
		wantStatus    int
		wantAccessed  bool
	}{
		{name: "owner sees the access", method: http.MethodGet, email: "student@example.com", workspaceName: "ws-student", wantStatus: http.StatusOK, wantAccessed: true},
		{name: "another student does not", method: http.MethodGet, email: "other@example.com", workspaceName: "ws-student", wantStatus: http.StatusOK},
		{name: "workspace never opened", method: http.MethodGet, email: "student@example.com", workspaceName: "ws-untouched", wantStatus: http.StatusOK},
		{name: "unknown lab", method: http.MethodGet, email: "student@example.com", labID: "job-missing", workspaceName: "ws-student", wantStatus: http.StatusOK},
		{name: "missing workspace name", method: http.MethodGet, email: "student@example.com", wantStatus: http.StatusBadRequest},
		{name: "no authenticated student", method: http.MethodGet, workspaceName: "ws-student", wantStatus: http.StatusBadRequest},
		{name: "wrong method", method: http.MethodPost, email: "student@example.com", workspaceName: "ws-student", wantStatus: http.StatusMethodNotAllowed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			jm := NewJobManager("")
			id := jm.CreateJob(&LabConfig{StackName: "test"})
			require.NoError(t, jm.RecordWorkspaceAccess(id, "ws-student", "student"))
			h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

			labID := tt.labID
			if labID == "" {
				labID = id
			}
			req := httptest.NewRequest(tt.method, "/api/student/workspace/access?lab_id="+labID+"&workspace_name="+tt.workspaceName, nil)
			if tt.email != "" {
				req = req.WithContext(context.WithValue(req.Context(), studentEmailContextKey, tt.email))
			}
			w := httptest.NewRecorder()
			h.WorkspaceTeacherAccess(w, req)

			require.Equal(t, tt.wantStatus, w.Code)
			if tt.wantStatus != http.StatusOK {
				return
			}
			var body map[string]string
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
			if !tt.wantAccessed {
				assert.Empty(t, body["teacher_accessed_at"])
				return
			}
			at, err := time.Parse(time.RFC3339, body["teacher_accessed_at"])
			require.NoError(t, err)
			assert.WithinDuration(t, time.Now(), at, time.Minute)
		})
	}
}

// Workspace names are deterministic, so an access must not outlive the workspace
// it was recorded for: a recreated workspace starts with a clean slate.
func TestRecordWorkspaceAccess(t *testing.T) {
	tests := []struct {
		name       string
		event      string // workspace event recorded after the access, "" for none
		eventWS    string
		wantAccess bool
	}{
		{name: "access is kept", wantAccess: true},
		{name: "cleared when the workspace is deleted", event: WorkspaceEventDeleted, eventWS: "ws-student"},
		{name: "kept when another workspace is deleted", event: WorkspaceEventDeleted, eventWS: "ws-other", wantAccess: true},
		{name: "kept on a creation event", event: WorkspaceEventCreated, eventWS: "ws-student", wantAccess: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			jm := NewJobManager("")
			id := jm.CreateJob(&LabConfig{StackName: "test"})
			require.NoError(t, jm.RecordWorkspaceAccess(id, "ws-student", "student"))
			if tt.event != "" {
				require.NoError(t, jm.RecordWorkspaceEvent(id, tt.event, tt.eventWS, tt.eventWS, "student", "go"))
			}

			access, ok := jm.GetWorkspaceAccess(id, "ws-student")
			assert.Equal(t, tt.wantAccess, ok)
			if tt.wantAccess {
				assert.Equal(t, "student", access.Owner)
				assert.False(t, access.At.IsZero())
			}
		})
	}

	t.Run("unknown job", func(t *testing.T) {
		t.Parallel()
		jm := NewJobManager("")
		require.Error(t, jm.RecordWorkspaceAccess("job-missing", "ws-student", "student"))
		_, ok := jm.GetWorkspaceAccess("job-missing", "ws-student")
		assert.False(t, ok)
	})
}

// Accesses are persisted with the job but kept out of API responses, which
// students can read for every lab.
func TestSanitizedCopy_WorkspaceAccesses(t *testing.T) {
	tests := []struct {
		name           string
		encryptSecrets bool
		wantCopied     bool
	}{
		{name: "persisted to disk", encryptSecrets: true, wantCopied: true},
		{name: "left out of API responses", encryptSecrets: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			jm := NewJobManager("")
			id := jm.CreateJob(&LabConfig{StackName: "test"})
			require.NoError(t, jm.RecordWorkspaceAccess(id, "ws-student", "student"))
			job, _ := jm.GetJob(id)

			job.mu.RLock()
			cp, err := job.sanitizedCopy(tt.encryptSecrets)
			job.mu.RUnlock()
			require.NoError(t, err)
			assert.Equal(t, tt.wantCopied, len(cp.WorkspaceAccesses) == 1)
		})
	}
}

// The lab detail page offers to open a workspace only once it is up and has a URL.
func TestLabDetail_OpenWorkspaceControl(t *testing.T) {
	t.Chdir("../..") // getTemplate resolves web/ relative to the working directory

	tests := []struct {
		name     string
		ws       workspace.Workspace
		wantOpen bool
	}{
		{
			name:     "ready workspace can be opened",
			ws:       workspace.Workspace{ID: "ws-alice", Name: "ws-alice", Owner: "alice", Ready: true, OpenURL: "https://ws-alice.example.com/"},
			wantOpen: true,
		},
		{
			name: "workspace still starting cannot",
			ws:   workspace.Workspace{ID: "ws-alice", Name: "ws-alice", Owner: "alice", OpenURL: "https://ws-alice.example.com/"},
		},
		{
			name: "workspace without a URL cannot",
			ws:   workspace.Workspace{ID: "ws-alice", Name: "ws-alice", Owner: "alice", Ready: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jm := NewJobManager("")
			h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
			useFakeBackend(h, &fakeBackend{reachable: true, workspaces: []workspace.Workspace{tt.ws}})
			labID := completedLabWithKubeconfig(jm, 0)

			rec := httptest.NewRecorder()
			h.ServeLabDetail(rec, httptest.NewRequest(http.MethodGet, "/labs/"+labID, nil))
			require.Equal(t, http.StatusOK, rec.Code)
			body := rec.Body.String()
			require.Contains(t, body, `data-workspace-id="ws-alice"`, "the workspace row must render")

			assert.Equal(t, tt.wantOpen, strings.Contains(body, `action="/api/labs/`+labID+`/workspaces/ws-alice/open"`))
		})
	}
}
