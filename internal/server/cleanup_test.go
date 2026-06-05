package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsCoderReachable_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" && r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	assert.True(t, isCoderReachable(srv.URL))
}

func TestIsCoderReachable_Unreachable(t *testing.T) {
	// Non-existent server
	assert.False(t, isCoderReachable("http://localhost:1"))
}

func TestIsCoderReachable_InvalidURL(t *testing.T) {
	assert.False(t, isCoderReachable("://invalid-url"))
}

func TestCleanupExpiredWorkspaces_NoJobs(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	// Should not panic with no jobs
	h.cleanupExpiredWorkspaces()
}

func TestCleanupExpiredWorkspaces_SkipsNonCompleted(t *testing.T) {
	jm := NewJobManager("")
	id := jm.CreateJob(&LabConfig{
		StackName:              "test",
		WorkspaceLifetimeHours: 1,
	})
	// Status is pending, not completed → should be skipped
	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	h.cleanupExpiredWorkspaces()

	job, _ := jm.GetJob(id)
	job.mu.RLock()
	snapshots := job.WorkspaceSnapshots
	job.mu.RUnlock()
	assert.Empty(t, snapshots, "no snapshots should be recorded for non-completed job")
}

func TestCleanupExpiredWorkspaces_SkipsNoCoderURL(t *testing.T) {
	jm := NewJobManager("")
	id := jm.CreateJob(&LabConfig{
		StackName:              "test",
		WorkspaceLifetimeHours: 1,
	})
	jm.UpdateJobStatus(id, JobStatusCompleted)
	// No CoderURL → should be skipped

	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	h.cleanupExpiredWorkspaces()
}

func TestIsCoderReachable_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	// Server returns 500 but it IS reachable - should return true (just checks connectivity)
	result := isCoderReachable(srv.URL)
	// The function just checks if the server responds (any status code means reachable)
	assert.True(t, result)
}

func TestStartWorkspaceCleanup_CancelContext(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	// Override ticker to very short interval - just check that context cancellation works
	done := make(chan struct{})
	go func() {
		h.StartWorkspaceCleanup(ctx)
		close(done)
	}()
	select {
	case <-done:
		// Success - context was cancelled
	case <-time.After(500 * time.Millisecond):
		t.Error("StartWorkspaceCleanup should have returned after context cancellation")
	}
}

func TestCleanupExpiredWorkspaces_CoderReachableNoWorkspaces(t *testing.T) {
	// Set up a Coder mock server that is reachable and returns empty workspaces
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/healthz":
			w.WriteHeader(http.StatusOK)
		case "/api/v2/workspaces":
			// Return empty workspaces list
			type wsResp struct {
				Workspaces []interface{} `json:"workspaces"`
				Count      int           `json:"count"`
			}
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(wsResp{Workspaces: []interface{}{}, Count: 0})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	jm := NewJobManager("")
	id := jm.CreateJob(&LabConfig{
		StackName:              "test",
		WorkspaceLifetimeHours: 1,
	})
	jm.UpdateJobStatus(id, JobStatusCompleted)

	job, exists := jm.GetJob(id)
	require.True(t, exists)
	job.mu.Lock()
	job.CoderURL = srv.URL
	job.CoderSessionToken = "token"
	job.CoderOrganizationID = "00000000-0000-0000-0000-000000000000"
	job.mu.Unlock()

	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	h.cleanupExpiredWorkspaces()

	// RecordWorkspaceSnapshot should have been called with 0
	job.mu.RLock()
	snapshots := job.WorkspaceSnapshots
	job.mu.RUnlock()
	assert.Len(t, snapshots, 1)
	assert.Equal(t, 0, snapshots[0].Count)
}

func TestCleanupExpiredWorkspaces_DeletesExpiredWorkspace(t *testing.T) {
	// Mock Coder server that has one expired workspace and accepts deletion
	wsID := "550e8400-e29b-41d4-a716-446655440000" // fixed UUID for testing
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/healthz":
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/api/v2/workspaces":
			// One workspace created 2 hours ago
			type workspace struct {
				ID        string `json:"id"`
				Name      string `json:"name"`
				CreatedAt string `json:"created_at"`
			}
			type wsResp struct {
				Workspaces []workspace `json:"workspaces"`
				Count      int         `json:"count"`
			}
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(wsResp{
				Workspaces: []workspace{{
					ID:        wsID,
					Name:      "expired-ws",
					CreatedAt: time.Now().Add(-2 * time.Hour).Format(time.RFC3339),
				}},
				Count: 1,
			})
		case r.URL.Path == "/api/v2/workspaces/"+wsID+"/builds" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"id":           "550e8400-e29b-41d4-a716-000000000001",
				"workspace_id": wsID,
				"status":       "pending",
			})
		case r.URL.Path == "/api/v2/users/login":
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"session_token": "token"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	jm := NewJobManager(t.TempDir())
	id := jm.CreateJob(&LabConfig{
		StackName:              "test",
		WorkspaceLifetimeHours: 1, // 1 hour, workspace is 2 hours old
	})
	jm.UpdateJobStatus(id, JobStatusCompleted)

	job, exists := jm.GetJob(id)
	require.True(t, exists)
	job.mu.Lock()
	job.CoderURL = srv.URL
	job.CoderSessionToken = "token"
	job.CoderOrganizationID = "00000000-0000-0000-0000-000000000000"
	job.CoderAdminEmail = "admin@example.com"
	job.CoderAdminPassword = "password"
	job.mu.Unlock()

	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	h.cleanupExpiredWorkspaces()

	// Check that a cleanup event was recorded
	job.mu.RLock()
	events := job.CleanupEvents
	job.mu.RUnlock()
	assert.Len(t, events, 1, "should have 1 cleanup event for the deleted workspace")
}

func TestCleanupExpiredWorkspaces_CoderUnreachable(t *testing.T) {
	jm := NewJobManager("")
	id := jm.CreateJob(&LabConfig{
		StackName:              "test",
		WorkspaceLifetimeHours: 1,
	})
	jm.UpdateJobStatus(id, JobStatusCompleted)
	// Set CoderURL to unreachable server
	job, exists := jm.GetJob(id)
	require.True(t, exists)
	job.mu.Lock()
	job.CoderURL = "http://localhost:1" // unreachable
	job.mu.Unlock()

	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	h.cleanupExpiredWorkspaces()
	// Should skip because Coder is unreachable
}

// --- cleanupInterval tests ---

func TestCleanupInterval_Default(t *testing.T) {
	t.Setenv("CLEANUP_INTERVAL_MINUTES", "")
	assert.Equal(t, 5*time.Minute, cleanupInterval())
}

func TestCleanupInterval_EnvVar(t *testing.T) {
	t.Setenv("CLEANUP_INTERVAL_MINUTES", "15")
	assert.Equal(t, 15*time.Minute, cleanupInterval())
}

func TestCleanupInterval_InvalidEnvVar(t *testing.T) {
	t.Setenv("CLEANUP_INTERVAL_MINUTES", "not-a-number")
	assert.Equal(t, 5*time.Minute, cleanupInterval())
}

func TestCleanupInterval_ZeroEnvVar(t *testing.T) {
	t.Setenv("CLEANUP_INTERVAL_MINUTES", "0")
	assert.Equal(t, 5*time.Minute, cleanupInterval())
}

// --- deletionRetryMaxRetries tests ---

func TestDeletionRetryMaxRetries_Default(t *testing.T) {
	t.Setenv("CLEANUP_DELETE_MAX_RETRIES", "")
	assert.Equal(t, 3, deletionRetryMaxRetries())
}

func TestDeletionRetryMaxRetries_EnvVar(t *testing.T) {
	t.Setenv("CLEANUP_DELETE_MAX_RETRIES", "5")
	assert.Equal(t, 5, deletionRetryMaxRetries())
}

func TestDeletionRetryMaxRetries_Invalid(t *testing.T) {
	t.Setenv("CLEANUP_DELETE_MAX_RETRIES", "not-a-number")
	assert.Equal(t, 3, deletionRetryMaxRetries())
}

func TestDeletionRetryMaxRetries_Zero(t *testing.T) {
	t.Setenv("CLEANUP_DELETE_MAX_RETRIES", "0")
	assert.Equal(t, 3, deletionRetryMaxRetries())
}

// --- deletionRetryInterval tests ---

func TestDeletionRetryInterval_Default(t *testing.T) {
	t.Setenv("CLEANUP_DELETE_RETRY_INTERVAL_HOURS", "")
	assert.Equal(t, 2*time.Hour, deletionRetryInterval())
}

func TestDeletionRetryInterval_EnvVar(t *testing.T) {
	t.Setenv("CLEANUP_DELETE_RETRY_INTERVAL_HOURS", "4")
	assert.Equal(t, 4*time.Hour, deletionRetryInterval())
}

func TestDeletionRetryInterval_Invalid(t *testing.T) {
	t.Setenv("CLEANUP_DELETE_RETRY_INTERVAL_HOURS", "bad")
	assert.Equal(t, 2*time.Hour, deletionRetryInterval())
}

func TestDeletionRetryInterval_Zero(t *testing.T) {
	t.Setenv("CLEANUP_DELETE_RETRY_INTERVAL_HOURS", "0")
	assert.Equal(t, 2*time.Hour, deletionRetryInterval())
}

// --- cleanup retry behaviour tests ---

func TestCleanupExpiredWorkspaces_SkipsWorkspaceWithGiveUp(t *testing.T) {
	// Mock Coder that returns one expired workspace; deletion call should never be made.
	deleteCalled := false
	wsID := "550e8400-e29b-41d4-a716-446655440001"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/healthz":
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/api/v2/workspaces":
			type workspace struct {
				ID        string `json:"id"`
				Name      string `json:"name"`
				CreatedAt string `json:"created_at"`
			}
			type wsResp struct {
				Workspaces []workspace `json:"workspaces"`
				Count      int         `json:"count"`
			}
			json.NewEncoder(w).Encode(wsResp{
				Workspaces: []workspace{{ID: wsID, Name: "old-ws", CreatedAt: time.Now().Add(-2 * time.Hour).Format(time.RFC3339)}},
				Count:      1,
			})
		case r.URL.Path == "/api/v2/workspaces/"+wsID+"/builds" && r.Method == http.MethodPost:
			deleteCalled = true
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]interface{}{"id": "00000000-0000-0000-0000-000000000001", "workspace_id": wsID, "status": "pending"})
		case r.URL.Path == "/api/v2/users/login":
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"session_token": "token"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	jm := NewJobManager("")
	id := jm.CreateJob(&LabConfig{StackName: "test", WorkspaceLifetimeHours: 1})
	jm.UpdateJobStatus(id, JobStatusCompleted)

	job, _ := jm.GetJob(id)
	job.mu.Lock()
	job.CoderURL = srv.URL
	job.CoderSessionToken = "token"
	job.CoderOrganizationID = "00000000-0000-0000-0000-000000000000"
	job.CoderAdminEmail = "admin@example.com"
	job.CoderAdminPassword = "password"
	// Pre-populate a GiveUp retry record
	job.DeletionRetries[wsID] = &WorkspaceDeletionRetry{
		WorkspaceID: wsID, Attempts: 3, GiveUp: true, LastAttempt: time.Now().Add(-3 * time.Hour),
	}
	job.mu.Unlock()

	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	h.cleanupExpiredWorkspaces()

	assert.False(t, deleteCalled, "delete should not be called for workspace with GiveUp=true")
}

func TestCleanupExpiredWorkspaces_SkipsWorkspaceBeforeRetryInterval(t *testing.T) {
	// Workspace failed deletion recently; retry interval not yet elapsed.
	deleteCalled := false
	wsID := "550e8400-e29b-41d4-a716-446655440002"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/healthz":
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/api/v2/workspaces":
			type workspace struct {
				ID        string `json:"id"`
				Name      string `json:"name"`
				CreatedAt string `json:"created_at"`
			}
			type wsResp struct {
				Workspaces []workspace `json:"workspaces"`
				Count      int         `json:"count"`
			}
			json.NewEncoder(w).Encode(wsResp{
				Workspaces: []workspace{{ID: wsID, Name: "old-ws", CreatedAt: time.Now().Add(-2 * time.Hour).Format(time.RFC3339)}},
				Count:      1,
			})
		case r.URL.Path == "/api/v2/workspaces/"+wsID+"/builds" && r.Method == http.MethodPost:
			deleteCalled = true
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]interface{}{"id": "00000000-0000-0000-0000-000000000002", "workspace_id": wsID, "status": "pending"})
		case r.URL.Path == "/api/v2/users/login":
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"session_token": "token"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	t.Setenv("CLEANUP_DELETE_RETRY_INTERVAL_HOURS", "2")

	jm := NewJobManager("")
	id := jm.CreateJob(&LabConfig{StackName: "test", WorkspaceLifetimeHours: 1})
	jm.UpdateJobStatus(id, JobStatusCompleted)

	job, _ := jm.GetJob(id)
	job.mu.Lock()
	job.CoderURL = srv.URL
	job.CoderSessionToken = "token"
	job.CoderOrganizationID = "00000000-0000-0000-0000-000000000000"
	job.CoderAdminEmail = "admin@example.com"
	job.CoderAdminPassword = "password"
	// Last attempt was 30 minutes ago — interval is 2h, so not ready yet
	job.DeletionRetries[wsID] = &WorkspaceDeletionRetry{
		WorkspaceID: wsID, Attempts: 1, GiveUp: false, LastAttempt: time.Now().Add(-30 * time.Minute),
	}
	job.mu.Unlock()

	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	h.cleanupExpiredWorkspaces()

	assert.False(t, deleteCalled, "delete should not be called before retry interval elapses")
}

func TestCleanupExpiredWorkspaces_RecordsDeletionFailure(t *testing.T) {
	// Deletion fails; RecordDeletionFailure should be called.
	wsID := "550e8400-e29b-41d4-a716-446655440003"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/healthz":
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/api/v2/workspaces":
			type workspace struct {
				ID        string `json:"id"`
				Name      string `json:"name"`
				CreatedAt string `json:"created_at"`
			}
			type wsResp struct {
				Workspaces []workspace `json:"workspaces"`
				Count      int         `json:"count"`
			}
			json.NewEncoder(w).Encode(wsResp{
				Workspaces: []workspace{{ID: wsID, Name: "old-ws", CreatedAt: time.Now().Add(-2 * time.Hour).Format(time.RFC3339)}},
				Count:      1,
			})
		case r.URL.Path == "/api/v2/users/login":
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"session_token": "token"})
		case r.URL.Path == "/api/v2/workspaces/"+wsID+"/builds" && r.Method == http.MethodPost:
			// Return 500 to simulate deletion failure
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"message": "internal error"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	jm := NewJobManager("")
	id := jm.CreateJob(&LabConfig{StackName: "test", WorkspaceLifetimeHours: 1})
	jm.UpdateJobStatus(id, JobStatusCompleted)

	job, _ := jm.GetJob(id)
	job.mu.Lock()
	job.CoderURL = srv.URL
	job.CoderSessionToken = "token"
	job.CoderOrganizationID = "00000000-0000-0000-0000-000000000000"
	job.CoderAdminEmail = "admin@example.com"
	job.CoderAdminPassword = "password"
	job.mu.Unlock()

	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	h.cleanupExpiredWorkspaces()

	job.mu.RLock()
	r := job.DeletionRetries[wsID]
	job.mu.RUnlock()

	require.NotNil(t, r, "expected deletion retry record to be created after failed deletion")
	assert.Equal(t, 1, r.Attempts)
	assert.False(t, r.GiveUp, "GiveUp should be false after first failed attempt (max retries is 3)")
}

// --- cleanupExpiredLabs tests ---

func TestCleanupExpiredLabs_NoJobs(t *testing.T) {
	t.Parallel()
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	// Should not panic with no jobs
	h.cleanupExpiredLabs()
}

func TestCleanupExpiredLabs_SkipsNonCompleted(t *testing.T) {
	t.Parallel()
	past := time.Now().Add(-24 * time.Hour)
	jm := NewJobManager("")
	id := jm.CreateJob(&LabConfig{StackName: "test", LabDeletionDate: &past})
	// Status is pending, not completed → should be skipped

	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	h.cleanupExpiredLabs()

	job, _ := jm.GetJob(id)
	job.mu.RLock()
	status := job.Status
	job.mu.RUnlock()
	assert.Equal(t, JobStatusPending, status, "non-completed job should not be touched")
}

func TestCleanupExpiredLabs_SkipsNoDeletionDate(t *testing.T) {
	t.Parallel()
	jm := NewJobManager("")
	id := jm.CreateJob(&LabConfig{StackName: "test"})
	jm.UpdateJobStatus(id, JobStatusCompleted)

	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	h.cleanupExpiredLabs()

	job, _ := jm.GetJob(id)
	job.mu.RLock()
	status := job.Status
	job.mu.RUnlock()
	assert.Equal(t, JobStatusCompleted, status, "job without deletion date should not be touched")
}

func TestCleanupExpiredLabs_SkipsFutureDate(t *testing.T) {
	t.Parallel()
	future := time.Now().Add(24 * time.Hour)
	jm := NewJobManager("")
	id := jm.CreateJob(&LabConfig{StackName: "test", LabDeletionDate: &future})
	jm.UpdateJobStatus(id, JobStatusCompleted)

	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	h.cleanupExpiredLabs()

	job, _ := jm.GetJob(id)
	job.mu.RLock()
	status := job.Status
	job.mu.RUnlock()
	assert.Equal(t, JobStatusCompleted, status, "job with future deletion date should not be touched")
}

func TestCleanupExpiredLabs_MarksRunningForPastDate(t *testing.T) {
	t.Parallel()
	past := time.Now().Add(-1 * time.Hour)
	jm := NewJobManager("")
	id := jm.CreateJob(&LabConfig{StackName: "test", LabDeletionDate: &past})
	jm.UpdateJobStatus(id, JobStatusCompleted)

	// Use a PulumiExecutor with no workDir so Destroy will fail fast;
	// we only care that the status was flipped to Running before the goroutine fires.
	h := NewHandler(jm, &PulumiExecutor{jobManager: jm}, NewCredentialsManager(), nil, nil, nil)
	h.cleanupExpiredLabs()

	job, _ := jm.GetJob(id)
	job.mu.RLock()
	status := job.Status
	job.mu.RUnlock()
	// Status is flipped to Running immediately (before the async Destroy goroutine).
	assert.Equal(t, JobStatusRunning, status, "job past deletion date should be marked Running immediately")
}
