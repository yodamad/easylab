package server

import (
	"context"
	"testing"
	"time"

	"easylab/internal/providers/workspace"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func completedLabWithKubeconfig(jm *JobManager, lifetimeHours int) string {
	id := jm.CreateJob(&LabConfig{StackName: "test", WorkspaceLifetimeHours: lifetimeHours})
	jm.UpdateJobStatus(id, JobStatusCompleted)
	job, _ := jm.GetJob(id)
	job.mu.Lock()
	job.Kubeconfig = "fake-kubeconfig"
	job.mu.Unlock()
	return id
}

func TestCleanupExpiredWorkspaces_NoJobs(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	// Should not panic with no jobs
	h.cleanupExpiredWorkspaces()
}

func TestCleanupExpiredWorkspaces_SkipsNonCompleted(t *testing.T) {
	jm := NewJobManager("")
	id := jm.CreateJob(&LabConfig{StackName: "test", WorkspaceLifetimeHours: 1})
	// Status is pending, not completed → should be skipped
	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	h.cleanupExpiredWorkspaces()

	job, _ := jm.GetJob(id)
	job.mu.RLock()
	snapshots := job.WorkspaceSnapshots
	job.mu.RUnlock()
	assert.Empty(t, snapshots, "no snapshots should be recorded for non-completed job")
}

func TestCleanupExpiredWorkspaces_SkipsNoKubeconfig(t *testing.T) {
	jm := NewJobManager("")
	id := jm.CreateJob(&LabConfig{StackName: "test", WorkspaceLifetimeHours: 1})
	jm.UpdateJobStatus(id, JobStatusCompleted)
	// No kubeconfig → should be skipped

	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	h.cleanupExpiredWorkspaces()

	job, _ := jm.GetJob(id)
	job.mu.RLock()
	snapshots := job.WorkspaceSnapshots
	job.mu.RUnlock()
	assert.Empty(t, snapshots)
}

func TestStartWorkspaceCleanup_CancelContext(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
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

func TestCleanupExpiredWorkspaces_ReachableNoWorkspaces(t *testing.T) {
	jm := NewJobManager("")
	id := completedLabWithKubeconfig(jm, 1)

	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	useFakeBackend(h, &fakeBackend{reachable: true, workspaces: nil})
	h.cleanupExpiredWorkspaces()

	job, _ := jm.GetJob(id)
	job.mu.RLock()
	snapshots := job.WorkspaceSnapshots
	job.mu.RUnlock()
	require.Len(t, snapshots, 1)
	assert.Equal(t, 0, snapshots[0].Count)
}

func TestCleanupExpiredWorkspaces_DeletesExpiredWorkspace(t *testing.T) {
	jm := NewJobManager(t.TempDir())
	id := completedLabWithKubeconfig(jm, 1) // 1h lifetime

	fb := &fakeBackend{
		reachable: true,
		workspaces: []workspace.Workspace{
			{ID: "ws-alice", Name: "ws-alice", CreatedAt: time.Now().Add(-2 * time.Hour)},
		},
	}
	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	useFakeBackend(h, fb)
	h.cleanupExpiredWorkspaces()

	assert.Equal(t, []string{"ws-alice"}, fb.DeleteCalls)
	job, _ := jm.GetJob(id)
	job.mu.RLock()
	events := job.CleanupEvents
	job.mu.RUnlock()
	assert.Len(t, events, 1, "should have 1 cleanup event for the deleted workspace")
}

func TestCleanupExpiredWorkspaces_KeepsYoungWorkspace(t *testing.T) {
	jm := NewJobManager("")
	completedLabWithKubeconfig(jm, 5) // 5h lifetime

	fb := &fakeBackend{
		reachable: true,
		workspaces: []workspace.Workspace{
			{ID: "ws-bob", Name: "ws-bob", CreatedAt: time.Now().Add(-1 * time.Hour)},
		},
	}
	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	useFakeBackend(h, fb)
	h.cleanupExpiredWorkspaces()

	assert.Empty(t, fb.DeleteCalls, "workspace younger than lifetime must not be deleted")
}

func TestCleanupExpiredWorkspaces_Unreachable(t *testing.T) {
	jm := NewJobManager("")
	id := completedLabWithKubeconfig(jm, 1)

	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	useFakeBackend(h, &fakeBackend{reachable: false})
	h.cleanupExpiredWorkspaces()

	job, _ := jm.GetJob(id)
	job.mu.RLock()
	snapshots := job.WorkspaceSnapshots
	job.mu.RUnlock()
	assert.Empty(t, snapshots, "unreachable cluster should skip cleanup entirely")
}

func TestCleanupExpiredWorkspaces_SkipsWorkspaceWithGiveUp(t *testing.T) {
	jm := NewJobManager("")
	id := completedLabWithKubeconfig(jm, 1)

	fb := &fakeBackend{
		reachable: true,
		workspaces: []workspace.Workspace{
			{ID: "ws-old", Name: "ws-old", CreatedAt: time.Now().Add(-2 * time.Hour)},
		},
	}
	job, _ := jm.GetJob(id)
	job.mu.Lock()
	job.DeletionRetries["ws-old"] = &WorkspaceDeletionRetry{WorkspaceID: "ws-old", Attempts: 3, GiveUp: true, LastAttempt: time.Now().Add(-3 * time.Hour)}
	job.mu.Unlock()

	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	useFakeBackend(h, fb)
	h.cleanupExpiredWorkspaces()

	assert.Empty(t, fb.DeleteCalls, "delete should not be called for workspace with GiveUp=true")
}

func TestCleanupExpiredWorkspaces_SkipsWorkspaceBeforeRetryInterval(t *testing.T) {
	t.Setenv("CLEANUP_DELETE_RETRY_INTERVAL_HOURS", "2")
	jm := NewJobManager("")
	id := completedLabWithKubeconfig(jm, 1)

	fb := &fakeBackend{
		reachable: true,
		workspaces: []workspace.Workspace{
			{ID: "ws-old", Name: "ws-old", CreatedAt: time.Now().Add(-2 * time.Hour)},
		},
	}
	job, _ := jm.GetJob(id)
	job.mu.Lock()
	job.DeletionRetries["ws-old"] = &WorkspaceDeletionRetry{WorkspaceID: "ws-old", Attempts: 1, LastAttempt: time.Now().Add(-30 * time.Minute)}
	job.mu.Unlock()

	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	useFakeBackend(h, fb)
	h.cleanupExpiredWorkspaces()

	assert.Empty(t, fb.DeleteCalls, "delete should not be called before retry interval elapses")
}

func TestCleanupExpiredWorkspaces_RecordsDeletionFailure(t *testing.T) {
	jm := NewJobManager("")
	id := completedLabWithKubeconfig(jm, 1)

	fb := &fakeBackend{
		reachable: true,
		deleteErr: assertErr("boom"),
		workspaces: []workspace.Workspace{
			{ID: "ws-old", Name: "ws-old", CreatedAt: time.Now().Add(-2 * time.Hour)},
		},
	}
	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	useFakeBackend(h, fb)
	h.cleanupExpiredWorkspaces()

	job, _ := jm.GetJob(id)
	job.mu.RLock()
	r := job.DeletionRetries["ws-old"]
	job.mu.RUnlock()
	require.NotNil(t, r, "expected deletion retry record after failed deletion")
	assert.Equal(t, 1, r.Attempts)
	assert.False(t, r.GiveUp)
}

type assertErr string

func (e assertErr) Error() string { return string(e) }

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

// --- cleanupExpiredLabs tests ---

func TestCleanupExpiredLabs_NoJobs(t *testing.T) {
	t.Parallel()
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	h.cleanupExpiredLabs()
}

func TestCleanupExpiredLabs_SkipsNonCompleted(t *testing.T) {
	t.Parallel()
	past := time.Now().Add(-24 * time.Hour)
	jm := NewJobManager("")
	id := jm.CreateJob(&LabConfig{StackName: "test", LabDeletionDate: &past})

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

	h := NewHandler(jm, &PulumiExecutor{jobManager: jm}, NewCredentialsManager(), nil, nil, nil)
	h.cleanupExpiredLabs()

	job, _ := jm.GetJob(id)
	job.mu.RLock()
	status := job.Status
	job.mu.RUnlock()
	assert.Equal(t, JobStatusRunning, status, "job past deletion date should be marked Running immediately")
}
