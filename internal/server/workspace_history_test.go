package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"easylab/internal/providers/workspace"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- RequestWorkspace: workspace creation history ---

func TestRequestWorkspace_RecordsCreatedEvent(t *testing.T) {
	jm := NewJobManager("")
	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	useFakeBackend(h, &fakeBackend{reachable: true})

	labID := completedLabWithKubeconfig(jm, 0)
	job, _ := jm.GetJob(labID)
	job.mu.Lock()
	job.Config.WorkspaceTemplates = []WorkspaceTemplate{{Name: "default"}}
	job.mu.Unlock()

	form := url.Values{"lab_id": {labID}}
	req := postForm(t, "/api/workspace/request", form)
	req = req.WithContext(context.WithValue(req.Context(), studentEmailContextKey, "alice@example.com"))

	rec := httptest.NewRecorder()
	h.RequestWorkspace(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	job.mu.RLock()
	events := job.WorkspaceEvents
	job.mu.RUnlock()

	require.Len(t, events, 1, "a freshly created workspace should record one history event")
	assert.Equal(t, WorkspaceEventCreated, events[0].Action)
	assert.Equal(t, "alice@example.com", events[0].Owner, "history should show the student's email, not the sanitized username")
	assert.Equal(t, "default", events[0].Template)
}

func TestRequestWorkspace_NoEventWhenAlreadyExisting(t *testing.T) {
	jm := NewJobManager("")
	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	// EnsureWorkspace returns a pre-populated (i.e. already existing) workspace —
	// Created defaults to false, mirroring the real backend's idempotent return.
	useFakeBackend(h, &fakeBackend{
		reachable: true,
		getWS:     &workspace.Workspace{ID: "ws-bob", Name: "ws-bob", Owner: "bob"},
	})

	labID := completedLabWithKubeconfig(jm, 0)
	job, _ := jm.GetJob(labID)
	job.mu.Lock()
	job.Config.WorkspaceTemplates = []WorkspaceTemplate{{Name: "default"}}
	job.mu.Unlock()

	form := url.Values{"lab_id": {labID}}
	req := postForm(t, "/api/workspace/request", form)
	req = req.WithContext(context.WithValue(req.Context(), studentEmailContextKey, "bob@example.com"))

	rec := httptest.NewRecorder()
	h.RequestWorkspace(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	job.mu.RLock()
	events := job.WorkspaceEvents
	job.mu.RUnlock()
	assert.Empty(t, events, "revisiting an already-running workspace must not add a history event")
}

// --- workspaceOwnedBy: the per-student authorization key ---

// TestWorkspaceOwnedBy is a security regression test. The sanitized username is
// not an identity — usernameFromEmail drops the domain and folds anything a DNS
// label cannot carry into "-" — so authorization compares the full login email.
func TestWorkspaceOwnedBy(t *testing.T) {
	tests := []struct {
		name  string
		ws    workspace.Workspace
		email string
		want  bool
	}{
		{
			name:  "the owner's own email",
			ws:    workspace.Workspace{Owner: "alice", OwnerEmail: "alice@corp.com"},
			email: "alice@corp.com",
			want:  true,
		},
		{
			name:  "case and surrounding space do not matter",
			ws:    workspace.Workspace{Owner: "alice", OwnerEmail: "Alice@Corp.com"},
			email: "  alice@corp.com ",
			want:  true,
		},
		{
			name:  "another student entirely",
			ws:    workspace.Workspace{Owner: "alice", OwnerEmail: "alice@corp.com"},
			email: "bob@corp.com",
			want:  false,
		},
		{
			// The collision this check exists to close: both reduce to "john-doe".
			name:  "a different identity that sanitizes to the same username",
			ws:    workspace.Workspace{Owner: "john-doe", OwnerEmail: "john.doe@corp.com"},
			email: "john-doe@other.org",
			want:  false,
		},
		{
			name:  "same local part, different domain",
			ws:    workspace.Workspace{Owner: "alice", OwnerEmail: "alice@corp.com"},
			email: "alice@other.org",
			want:  false,
		},
		{
			// Nothing better is on record for a workspace created before the
			// email was tracked, so it keeps the check it has always had.
			name:  "legacy workspace with no email falls back to the username",
			ws:    workspace.Workspace{Owner: "alice"},
			email: "alice@corp.com",
			want:  true,
		},
		{
			name:  "legacy workspace rejects a different username",
			ws:    workspace.Workspace{Owner: "alice"},
			email: "bob@corp.com",
			want:  false,
		},
		{
			name:  "no session email owns nothing",
			ws:    workspace.Workspace{Owner: "alice", OwnerEmail: "alice@corp.com"},
			email: "",
			want:  false,
		},
		{
			name:  "an empty workspace owner matches nobody",
			ws:    workspace.Workspace{},
			email: "alice@corp.com",
			want:  false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, workspaceOwnedBy(tt.ws, tt.email))
		})
	}
}

// TestWorkspaceAccessBelongsTo covers the teacher-access records, which are
// persisted: entries written before ownership moved to the login email hold the
// sanitized username and must keep resolving.
func TestWorkspaceAccessBelongsTo(t *testing.T) {
	tests := []struct {
		name   string
		access WorkspaceAccess
		email  string
		want   bool
	}{
		{name: "recorded by email", access: WorkspaceAccess{Owner: "alice@corp.com"}, email: "alice@corp.com", want: true},
		{name: "recorded by email, different case", access: WorkspaceAccess{Owner: "Alice@Corp.com"}, email: "alice@corp.com", want: true},
		{name: "legacy record by username", access: WorkspaceAccess{Owner: "alice"}, email: "alice@corp.com", want: true},
		{name: "another student", access: WorkspaceAccess{Owner: "alice@corp.com"}, email: "bob@corp.com", want: false},
		{name: "nothing recorded", access: WorkspaceAccess{}, email: "alice@corp.com", want: false},
		{name: "no session email", access: WorkspaceAccess{Owner: "alice@corp.com"}, email: "", want: false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, workspaceAccessBelongsTo(tt.access, tt.email))
		})
	}
}

// --- ownerDisplayName ---

func TestOwnerDisplayName(t *testing.T) {
	tests := []struct {
		name string
		ws   workspace.Workspace
		want string
	}{
		{
			name: "prefers email when known",
			ws:   workspace.Workspace{Owner: "alice", OwnerEmail: "alice@example.com"},
			want: "alice@example.com",
		},
		{
			name: "falls back to sanitized owner when email is unknown",
			ws:   workspace.Workspace{Owner: "bob"},
			want: "bob",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ownerDisplayName(tt.ws))
		})
	}
}

// --- DeleteWorkspace: workspace deletion history ---

func TestDeleteWorkspace_RecordsDeletedEvent(t *testing.T) {
	jm := NewJobManager("")
	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	useFakeBackend(h, &fakeBackend{
		reachable: true,
		getWS:     &workspace.Workspace{ID: "ws-alice", Name: "ws-alice", Owner: "alice", Template: "default"},
	})

	labID := completedLabWithKubeconfig(jm, 0)

	form := url.Values{"lab_id": {labID}, "workspace_id": {"ws-alice"}}
	req := postForm(t, "/api/workspaces/delete", form)
	rec := httptest.NewRecorder()
	h.DeleteWorkspace(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	job, _ := jm.GetJob(labID)
	job.mu.RLock()
	events := job.WorkspaceEvents
	job.mu.RUnlock()

	require.Len(t, events, 1)
	assert.Equal(t, WorkspaceEventDeleted, events[0].Action)
	assert.Equal(t, "ws-alice", events[0].WorkspaceName)
	assert.Equal(t, "alice", events[0].Owner)
	assert.Equal(t, "default", events[0].Template)
}

func TestDeleteWorkspace_RecordsEmailWhenAvailable(t *testing.T) {
	jm := NewJobManager("")
	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	useFakeBackend(h, &fakeBackend{
		reachable: true,
		getWS:     &workspace.Workspace{ID: "ws-alice", Name: "ws-alice", Owner: "alice", OwnerEmail: "alice@example.com", Template: "default"},
	})

	labID := completedLabWithKubeconfig(jm, 0)

	form := url.Values{"lab_id": {labID}, "workspace_id": {"ws-alice"}}
	req := postForm(t, "/api/workspaces/delete", form)
	rec := httptest.NewRecorder()
	h.DeleteWorkspace(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	job, _ := jm.GetJob(labID)
	job.mu.RLock()
	events := job.WorkspaceEvents
	job.mu.RUnlock()

	require.Len(t, events, 1)
	assert.Equal(t, "alice@example.com", events[0].Owner, "history should show email when the annotation recorded one, even at deletion time")
}

func TestDeleteWorkspace_FailedDeleteRecordsNoEvent(t *testing.T) {
	jm := NewJobManager("")
	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	useFakeBackend(h, &fakeBackend{
		reachable: true,
		getWS:     &workspace.Workspace{ID: "ws-alice", Name: "ws-alice", Owner: "alice"},
		deleteErr: assert.AnError,
	})

	labID := completedLabWithKubeconfig(jm, 0)

	form := url.Values{"lab_id": {labID}, "workspace_id": {"ws-alice"}}
	req := postForm(t, "/api/workspaces/delete", form)
	rec := httptest.NewRecorder()
	h.DeleteWorkspace(rec, req)
	require.Equal(t, http.StatusInternalServerError, rec.Code)

	job, _ := jm.GetJob(labID)
	job.mu.RLock()
	events := job.WorkspaceEvents
	job.mu.RUnlock()
	assert.Empty(t, events, "a failed deletion must not be recorded as history")
}

func TestDeleteWorkspace_Bulk_RecordsDeletedEvents(t *testing.T) {
	jm := NewJobManager("")
	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	useFakeBackend(h, &fakeBackend{
		reachable: true,
		getWS:     &workspace.Workspace{ID: "ws-x", Name: "ws-x", Owner: "carol", Template: "go"},
	})

	labID := completedLabWithKubeconfig(jm, 0)

	form := url.Values{"lab_id": {labID}, "workspace_ids": {`["ws-a","ws-b"]`}}
	req := postForm(t, "/api/workspaces/delete", form)
	rec := httptest.NewRecorder()
	h.DeleteWorkspace(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	job, _ := jm.GetJob(labID)
	job.mu.RLock()
	events := job.WorkspaceEvents
	job.mu.RUnlock()

	require.Len(t, events, 2)
	for _, e := range events {
		assert.Equal(t, WorkspaceEventDeleted, e.Action)
		assert.Equal(t, "carol", e.Owner)
	}
}
