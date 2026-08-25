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
