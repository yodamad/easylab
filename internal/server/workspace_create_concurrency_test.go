package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"easylab/internal/providers/workspace"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// concurrencyTrackingBackend is a minimal, goroutine-safe workspace.Backend used
// only to observe how many EnsureWorkspace calls are in flight at once. Unlike
// fakeBackend (workspace_fake_test.go), it is safe to drive from many goroutines
// simultaneously, which is what testing a concurrency bound requires.
type concurrencyTrackingBackend struct {
	inFlight int32
	maxSeen  int32
	hold     time.Duration
}

func (b *concurrencyTrackingBackend) EnsureWorkspace(_ context.Context, spec workspace.Spec) (workspace.Workspace, error) {
	n := atomic.AddInt32(&b.inFlight, 1)
	defer atomic.AddInt32(&b.inFlight, -1)
	for {
		cur := atomic.LoadInt32(&b.maxSeen)
		if n <= cur || atomic.CompareAndSwapInt32(&b.maxSeen, cur, n) {
			break
		}
	}
	if b.hold > 0 {
		time.Sleep(b.hold)
	}
	return workspace.Workspace{ID: "ws-" + spec.Owner, Name: "ws-" + spec.Owner, Owner: spec.Owner, OwnerEmail: spec.OwnerEmail, Token: spec.Token, Created: true}, nil
}

func (b *concurrencyTrackingBackend) GetWorkspace(context.Context, string) (workspace.Workspace, error) {
	return workspace.Workspace{}, nil
}
func (b *concurrencyTrackingBackend) ListWorkspaces(context.Context, string) ([]workspace.Workspace, error) {
	return nil, nil
}
func (b *concurrencyTrackingBackend) DeleteWorkspace(context.Context, string, string) error {
	return nil
}
func (b *concurrencyTrackingBackend) Reachable(context.Context) bool                   { return true }
func (b *concurrencyTrackingBackend) Routing(context.Context, string) (string, string) { return "", "" }

// TestRequestWorkspace_BoundsConcurrentCreations proves the workspaceCreateSem
// safety property: however many students hit RequestWorkspace at once, no more
// than WORKSPACE_CREATE_CONCURRENCY EnsureWorkspace calls run at the same time.
func TestRequestWorkspace_BoundsConcurrentCreations(t *testing.T) {
	const limit = 2
	const students = 6
	t.Setenv("WORKSPACE_CREATE_CONCURRENCY", "2")

	jm := NewJobManager("")
	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

	backend := &concurrencyTrackingBackend{hold: 30 * time.Millisecond}
	h.newWorkspaceBackend = func(_, _ string) (workspace.Backend, error) { return backend, nil }

	labID := completedLabWithKubeconfig(jm, 0)
	job, _ := jm.GetJob(labID)
	job.mu.Lock()
	job.Config.WorkspaceTemplates = []WorkspaceTemplate{{Name: "default"}}
	job.mu.Unlock()

	var wg sync.WaitGroup
	codes := make([]int, students)
	wg.Add(students)
	for i := 0; i < students; i++ {
		go func(n int) {
			defer wg.Done()
			form := url.Values{"lab_id": {labID}}
			req := postForm(t, "/api/workspace/request", form)
			req = req.WithContext(context.WithValue(req.Context(), studentEmailContextKey, fmt.Sprintf("student%d@example.com", n)))
			rec := httptest.NewRecorder()
			h.RequestWorkspace(rec, req)
			codes[n] = rec.Code
		}(i)
	}
	wg.Wait()

	for i, code := range codes {
		assert.Equal(t, http.StatusOK, code, "student %d request should succeed", i)
	}

	maxSeen := atomic.LoadInt32(&backend.maxSeen)
	t.Logf("observed max concurrent EnsureWorkspace calls: %d (limit %d)", maxSeen, limit)
	require.LessOrEqual(t, maxSeen, int32(limit), "workspaceCreateSem must never let more than the configured limit run at once")
}
