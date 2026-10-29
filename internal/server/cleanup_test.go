package server

import (
	"context"
	"fmt"
	"strings"
	"sync"
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
			{ID: "ws-alice", Name: "ws-alice", Owner: "alice", Template: "default", CreatedAt: time.Now().Add(-2 * time.Hour)},
		},
	}
	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	useFakeBackend(h, fb)
	h.cleanupExpiredWorkspaces()

	assert.Equal(t, []string{"ws-alice"}, fb.DeleteCalls)
	job, _ := jm.GetJob(id)
	job.mu.RLock()
	events := job.CleanupEvents
	wsEvents := job.WorkspaceEvents
	job.mu.RUnlock()
	assert.Len(t, events, 1, "should have 1 cleanup event for the deleted workspace")
	require.Len(t, wsEvents, 1, "should have 1 workspace history event for the deleted workspace")
	assert.Equal(t, WorkspaceEventDeleted, wsEvents[0].Action)
	assert.Equal(t, "alice", wsEvents[0].Owner)
	assert.Equal(t, "default", wsEvents[0].Template)
}

// timeoutCheckingBackend wraps fakeBackend and records whether
// ListWorkspaces/DeleteWorkspace were called with a context that carries a
// deadline. This is a regression test for a bug where both calls used
// context.Background() (no deadline at all), so an unresponsive cluster could
// stall the whole cleanup tick indefinitely.
type timeoutCheckingBackend struct {
	*fakeBackend
	listHadDeadline   bool
	deleteHadDeadline bool
}

func (b *timeoutCheckingBackend) ListWorkspaces(ctx context.Context, id string) ([]workspace.Workspace, error) {
	_, b.listHadDeadline = ctx.Deadline()
	return b.fakeBackend.ListWorkspaces(ctx, id)
}

func (b *timeoutCheckingBackend) DeleteWorkspace(ctx context.Context, labID, wsID string) error {
	_, b.deleteHadDeadline = ctx.Deadline()
	return b.fakeBackend.DeleteWorkspace(ctx, labID, wsID)
}

func TestCleanupExpiredWorkspaces_UsesTimeoutContextForListAndDelete(t *testing.T) {
	jm := NewJobManager("")
	completedLabWithKubeconfig(jm, 1)

	fb := &timeoutCheckingBackend{fakeBackend: &fakeBackend{
		reachable: true,
		workspaces: []workspace.Workspace{
			{ID: "ws-old", Name: "ws-old", CreatedAt: time.Now().Add(-2 * time.Hour)},
		},
	}}

	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	h.newWorkspaceBackend = func(_, _ string) (workspace.Backend, error) { return fb, nil }
	h.cleanupExpiredWorkspaces()

	assert.True(t, fb.listHadDeadline, "ListWorkspaces must be called with a bounded context, not context.Background()")
	assert.True(t, fb.deleteHadDeadline, "DeleteWorkspace must be called with a bounded context, not context.Background()")
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

// --- idleCheckInterval tests ---

func TestIdleCheckInterval(t *testing.T) {
	tests := []struct {
		name     string
		env      string
		expected time.Duration
	}{
		{name: "default", env: "", expected: time.Minute},
		{name: "env var", env: "3", expected: 3 * time.Minute},
		{name: "invalid env var falls back", env: "not-a-number", expected: time.Minute},
		{name: "zero falls back", env: "0", expected: time.Minute},
		{name: "negative falls back", env: "-2", expected: time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("IDLE_CHECK_INTERVAL_MINUTES", tt.env)
			assert.Equal(t, tt.expected, idleCheckInterval())
		})
	}
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

// --- cleanupDeleteConcurrency tests ---

func TestCleanupDeleteConcurrency_Default(t *testing.T) {
	assert.Equal(t, 5, cleanupDeleteConcurrency())
}

func TestCleanupDeleteConcurrency_EnvVar(t *testing.T) {
	t.Setenv("CLEANUP_DELETE_CONCURRENCY", "3")
	assert.Equal(t, 3, cleanupDeleteConcurrency())
}

func TestCleanupDeleteConcurrency_Invalid(t *testing.T) {
	t.Setenv("CLEANUP_DELETE_CONCURRENCY", "bad")
	assert.Equal(t, 5, cleanupDeleteConcurrency())
}

func TestCleanupDeleteConcurrency_Zero(t *testing.T) {
	t.Setenv("CLEANUP_DELETE_CONCURRENCY", "0")
	assert.Equal(t, 5, cleanupDeleteConcurrency())
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

	// Provider credentials are re-fetched from the credential store before an
	// auto-destroy, so they must be configured for the lab to be scheduled.
	cm := NewCredentialsManager()
	require.NoError(t, cm.SetCredentials(&OVHCredentials{
		ApplicationKey: "ak", ApplicationSecret: "as", ConsumerKey: "ck",
		ServiceName: "svc", Endpoint: "ovh-eu",
	}))
	h := NewHandler(jm, &PulumiExecutor{jobManager: jm}, cm, nil, nil, nil)
	h.cleanupExpiredLabs()

	job, _ := jm.GetJob(id)
	job.mu.RLock()
	status := job.Status
	job.mu.RUnlock()
	assert.Equal(t, JobStatusRunning, status, "job past deletion date should be marked Running immediately")
}

func TestCleanupExpiredLabs_SkipsWhenNoCredentials(t *testing.T) {
	t.Parallel()
	past := time.Now().Add(-1 * time.Hour)
	jm := NewJobManager("")
	// An OVH lab whose provider credentials are not available (e.g. entered via
	// the UI and lost on restart) must not be auto-destroyed without them.
	id := jm.CreateJob(&LabConfig{StackName: "test", Provider: "ovh", LabDeletionDate: &past})
	jm.UpdateJobStatus(id, JobStatusCompleted)

	h := NewHandler(jm, &PulumiExecutor{jobManager: jm}, NewCredentialsManager(), nil, nil, nil)
	h.cleanupExpiredLabs()

	job, _ := jm.GetJob(id)
	job.mu.RLock()
	status := job.Status
	job.mu.RUnlock()
	assert.Equal(t, JobStatusCompleted, status, "job must be left for retry, not marked Running, when credentials are missing")
}

// --- reconcileWorkspaceNotifications tests ---

// notifyReconcileCall is one ReconcileNotify invocation seen by the fake.
type notifyReconcileCall struct {
	WorkspaceID string
	Spec        workspace.Spec
}

// notifyBackend is a fakeBackend that also implements workspace.NotifyReconciler.
type notifyBackend struct {
	*fakeBackend
	notifyErrFor map[string]error // workspace ID -> error to return

	notifyMu    sync.Mutex
	notifyCalls []notifyReconcileCall
}

func (b *notifyBackend) ReconcileNotify(_ context.Context, workspaceID string, spec workspace.Spec) error {
	b.notifyMu.Lock()
	b.notifyCalls = append(b.notifyCalls, notifyReconcileCall{WorkspaceID: workspaceID, Spec: spec})
	b.notifyMu.Unlock()
	return b.notifyErrFor[workspaceID]
}

func (b *notifyBackend) calls() []notifyReconcileCall {
	b.notifyMu.Lock()
	defer b.notifyMu.Unlock()
	return append([]notifyReconcileCall(nil), b.notifyCalls...)
}

// newNotifyReconcileHandler returns a handler with one completed lab that has a
// kubeconfig and a reachable public address, wired to a notifyBackend.
func newNotifyReconcileHandler(t *testing.T, workspaces []workspace.Workspace) (*Handler, *notifyBackend, string) {
	t.Helper()
	jm := NewJobManager("")
	labID := completedLabWithKubeconfig(jm, 1)
	nb := &notifyBackend{fakeBackend: &fakeBackend{reachable: true, workspaces: workspaces}}
	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	h.publicURL = "https://easylab.example.com"
	h.newWorkspaceBackend = func(_, _ string) (workspace.Backend, error) { return nb, nil }
	return h, nb, labID
}

func TestReconcileWorkspaceNotifications_PerWorkspace(t *testing.T) {
	t.Parallel()

	h, nb, labID := newNotifyReconcileHandler(t, []workspace.Workspace{
		{ID: "ws-alice", OwnerEmail: "alice@example.com", Owner: "alice"},
		{ID: "ws-bob", OwnerEmail: "bob@example.com", Owner: "bob"},
	})
	h.reconcileWorkspaceNotifications()

	calls := nb.calls()
	require.Len(t, calls, 2)
	assert.Equal(t, "ws-alice", calls[0].WorkspaceID)
	assert.Equal(t, "ws-bob", calls[1].WorkspaceID)
	for _, c := range calls {
		assert.True(t, strings.HasPrefix(c.Spec.NotifyURL, "https://easylab.example.com/api/workspace/notifications?lab_id="+labID), c.Spec.NotifyURL)
		assert.NotEmpty(t, c.Spec.NotifyToken)
	}
	assert.NotEqual(t, calls[0].Spec.NotifyToken, calls[1].Spec.NotifyToken, "the token is per owner")
}

// TestReconcileWorkspaceNotifications_ConcurrentJobWrites is a race regression
// test, and only means anything under -race. The pass runs on the cleanup
// goroutine and reads Status, Kubeconfig and Config, all of which an admin
// retrying or re-provisioning a lab writes from a request goroutine — so it has
// to take the job's read lock. A plain run cannot catch that; this drives a
// writer alongside it so the detector can.
func TestReconcileWorkspaceNotifications_ConcurrentJobWrites(t *testing.T) {
	t.Parallel()

	h, _, labID := newNotifyReconcileHandler(t, []workspace.Workspace{
		{ID: "ws-alice", OwnerEmail: "alice@example.com", Owner: "alice"},
	})
	job, ok := h.jobManager.GetJob(labID)
	require.True(t, ok)

	done := make(chan struct{})
	var writers sync.WaitGroup
	writers.Add(1)
	go func() {
		defer writers.Done()
		for i := 0; ; i++ {
			select {
			case <-done:
				return
			default:
			}
			// The three fields the pass reads, written the way the handlers do.
			job.mu.Lock()
			job.Status = JobStatusCompleted
			job.Kubeconfig = fmt.Sprintf("kubeconfig-%d", i)
			job.Config = &LabConfig{StackName: "workshop", WorkspaceNamespace: fmt.Sprintf("ns-%d", i)}
			job.mu.Unlock()
		}
	}()

	for i := 0; i < 50; i++ {
		h.reconcileWorkspaceNotifications()
	}
	close(done)
	writers.Wait()
}

func TestReconcileWorkspaceNotifications_OwnerFallback(t *testing.T) {
	t.Parallel()

	h, nb, _ := newNotifyReconcileHandler(t, []workspace.Workspace{
		{ID: "ws-email", OwnerEmail: "alice@example.com", Owner: "alice"},
		{ID: "ws-legacy", Owner: "alice"},
		{ID: "ws-anon"},
	})
	h.reconcileWorkspaceNotifications()

	calls := nb.calls()
	require.Len(t, calls, 2, "a workspace with neither owner is skipped")
	assert.Equal(t, "ws-email", calls[0].WorkspaceID)
	assert.Equal(t, "ws-legacy", calls[1].WorkspaceID)
	// The legacy workspace is minted over its username, not the email.
	assert.NotEqual(t, calls[0].Spec.NotifyToken, calls[1].Spec.NotifyToken)
	_, wantToken := h.workspaceNotifyAccess(calls[1].Spec.NotifyURL[strings.Index(calls[1].Spec.NotifyURL, "lab_id=")+len("lab_id="):], "alice")
	assert.Equal(t, wantToken, calls[1].Spec.NotifyToken)
}

func TestReconcileWorkspaceNotifications_ContinuesAfterError(t *testing.T) {
	t.Parallel()

	h, nb, _ := newNotifyReconcileHandler(t, []workspace.Workspace{
		{ID: "ws-1", OwnerEmail: "a@example.com"},
		{ID: "ws-2", OwnerEmail: "b@example.com"},
		{ID: "ws-3", OwnerEmail: "c@example.com"},
	})
	nb.notifyErrFor = map[string]error{"ws-1": assertErr("boom")}
	h.reconcileWorkspaceNotifications()

	assert.Len(t, nb.calls(), 3, "one failing workspace must not stop the pass")
}

func TestReconcileWorkspaceNotifications_Skips(t *testing.T) {
	t.Parallel()

	two := []workspace.Workspace{
		{ID: "ws-1", OwnerEmail: "a@example.com"},
		{ID: "ws-2", OwnerEmail: "b@example.com"},
	}

	tests := []struct {
		name  string
		setup func(h *Handler, nb *notifyBackend, labID string)
	}{
		{"loopback public URL and no portal URL", func(h *Handler, _ *notifyBackend, _ string) {
			h.publicURL = "http://localhost:8081"
		}},
		{"no public URL and no portal URL", func(h *Handler, _ *notifyBackend, _ string) {
			h.publicURL = ""
		}},
		{"loopback portal URL and loopback public URL", func(h *Handler, _ *notifyBackend, labID string) {
			h.publicURL = "http://127.0.0.1:8081"
			h.setPortalURL(labID, "http://localhost:9000")
		}},
		{"job not completed", func(h *Handler, _ *notifyBackend, labID string) {
			h.jobManager.UpdateJobStatus(labID, JobStatusRunning)
		}},
		{"job without kubeconfig", func(h *Handler, _ *notifyBackend, labID string) {
			job, _ := h.jobManager.GetJob(labID)
			job.mu.Lock()
			job.Kubeconfig = ""
			job.mu.Unlock()
		}},
		{"cluster unreachable", func(_ *Handler, nb *notifyBackend, _ string) {
			nb.reachable = false
		}},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h, nb, labID := newNotifyReconcileHandler(t, two)
			tt.setup(h, nb, labID)

			h.reconcileWorkspaceNotifications()

			assert.Empty(t, nb.calls())
			assert.Zero(t, nb.ListCalls, "a skipped lab must not cost a ListWorkspaces call")
		})
	}
}

func TestReconcileWorkspaceNotifications_BackendWithoutReconciler(t *testing.T) {
	t.Parallel()

	jm := NewJobManager("")
	completedLabWithKubeconfig(jm, 1)
	fb := &fakeBackend{reachable: true, workspaces: []workspace.Workspace{{ID: "ws-1", OwnerEmail: "a@example.com"}}}
	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	h.publicURL = "https://easylab.example.com"
	useFakeBackend(h, fb)

	require.NotPanics(t, h.reconcileWorkspaceNotifications)
	assert.Zero(t, fb.ListCalls, "a backend that cannot reconcile is skipped before listing")
}

func TestReconcileWorkspaceNotifications_BackendBuildError(t *testing.T) {
	t.Parallel()

	jm := NewJobManager("")
	completedLabWithKubeconfig(jm, 1)
	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	h.publicURL = "https://easylab.example.com"
	h.newWorkspaceBackend = func(_, _ string) (workspace.Backend, error) { return nil, assertErr("no cluster") }

	require.NotPanics(t, h.reconcileWorkspaceNotifications)
}
