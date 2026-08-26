package server

import (
	"sync"
	"sync/atomic"
	"testing"

	"easylab/internal/providers/workspace"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- workspaceCreateConcurrency tests ---

func TestWorkspaceCreateConcurrency_Default(t *testing.T) {
	t.Setenv("WORKSPACE_CREATE_CONCURRENCY", "")
	assert.Equal(t, 20, workspaceCreateConcurrency())
}

func TestWorkspaceCreateConcurrency_EnvVar(t *testing.T) {
	t.Setenv("WORKSPACE_CREATE_CONCURRENCY", "5")
	assert.Equal(t, 5, workspaceCreateConcurrency())
}

func TestWorkspaceCreateConcurrency_Invalid(t *testing.T) {
	t.Setenv("WORKSPACE_CREATE_CONCURRENCY", "not-a-number")
	assert.Equal(t, 20, workspaceCreateConcurrency())
}

func TestWorkspaceCreateConcurrency_Zero(t *testing.T) {
	t.Setenv("WORKSPACE_CREATE_CONCURRENCY", "0")
	assert.Equal(t, 20, workspaceCreateConcurrency())
}

// --- workspaceBackendFor tests ---

func TestHandler_WorkspaceBackendFor_CachesPerLab(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

	var builds int32
	fb := &fakeBackend{}
	h.newWorkspaceBackend = func(_, _ string) (workspace.Backend, error) {
		atomic.AddInt32(&builds, 1)
		return fb, nil
	}

	b1, err := h.workspaceBackendFor("lab-1", "kubeconfig-a", "ns-a")
	require.NoError(t, err)
	b2, err := h.workspaceBackendFor("lab-1", "kubeconfig-a", "ns-a")
	require.NoError(t, err)

	assert.Same(t, b1, b2, "expected the cached backend to be reused")
	assert.Equal(t, int32(1), atomic.LoadInt32(&builds), "newWorkspaceBackend should only be called once for a cache hit")
}

func TestHandler_WorkspaceBackendFor_DifferentLabsGetDifferentBackends(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

	var builds int32
	h.newWorkspaceBackend = func(_, _ string) (workspace.Backend, error) {
		atomic.AddInt32(&builds, 1)
		return &fakeBackend{}, nil
	}

	b1, err := h.workspaceBackendFor("lab-1", "kubeconfig-a", "ns-a")
	require.NoError(t, err)
	b2, err := h.workspaceBackendFor("lab-2", "kubeconfig-b", "ns-b")
	require.NoError(t, err)

	assert.NotSame(t, b1, b2, "different labs must not share a cached backend")
	assert.Equal(t, int32(2), atomic.LoadInt32(&builds))
}

func TestHandler_WorkspaceBackendFor_KubeconfigChangeRebuilds(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

	var builds int32
	h.newWorkspaceBackend = func(_, _ string) (workspace.Backend, error) {
		atomic.AddInt32(&builds, 1)
		return &fakeBackend{}, nil
	}

	_, err := h.workspaceBackendFor("lab-1", "kubeconfig-a", "ns-a")
	require.NoError(t, err)
	// Same lab ID, new kubeconfig: simulates RecreateLab handing back a fresh cluster.
	_, err = h.workspaceBackendFor("lab-1", "kubeconfig-b", "ns-a")
	require.NoError(t, err)

	assert.Equal(t, int32(2), atomic.LoadInt32(&builds), "a kubeconfig change must be treated as a cache miss")
}

func TestHandler_WorkspaceBackendFor_BuildErrorNotCached(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

	var builds int32
	h.newWorkspaceBackend = func(_, _ string) (workspace.Backend, error) {
		if atomic.AddInt32(&builds, 1) == 1 {
			return nil, assertErr("boom")
		}
		return &fakeBackend{}, nil
	}

	_, err := h.workspaceBackendFor("lab-1", "kubeconfig-a", "ns-a")
	require.Error(t, err)

	b, err := h.workspaceBackendFor("lab-1", "kubeconfig-a", "ns-a")
	require.NoError(t, err)
	assert.NotNil(t, b)
	assert.Equal(t, int32(2), atomic.LoadInt32(&builds), "a failed build must not be cached")
}

func TestHandler_EvictWorkspaceBackend_ForcesRebuild(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

	var builds int32
	h.newWorkspaceBackend = func(_, _ string) (workspace.Backend, error) {
		atomic.AddInt32(&builds, 1)
		return &fakeBackend{}, nil
	}

	_, err := h.workspaceBackendFor("lab-1", "kubeconfig-a", "ns-a")
	require.NoError(t, err)

	h.evictWorkspaceBackend("lab-1")

	_, err = h.workspaceBackendFor("lab-1", "kubeconfig-a", "ns-a")
	require.NoError(t, err)

	assert.Equal(t, int32(2), atomic.LoadInt32(&builds), "eviction must force a rebuild")
}

// TestHandler_WorkspaceBackendFor_ConcurrentSameLab guards the scenario this
// cache exists for: many students hitting an already-provisioned lab at once
// must all reuse the one cached Kubernetes client rather than each building
// their own from scratch.
func TestHandler_WorkspaceBackendFor_ConcurrentSameLab(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

	var builds int32
	h.newWorkspaceBackend = func(_, _ string) (workspace.Backend, error) {
		atomic.AddInt32(&builds, 1)
		return &fakeBackend{}, nil
	}

	// Prime the cache once, as the first student's request would.
	_, err := h.workspaceBackendFor("lab-1", "kubeconfig-a", "ns-a")
	require.NoError(t, err)

	const concurrency = 50
	var wg sync.WaitGroup
	wg.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		go func() {
			defer wg.Done()
			if _, err := h.workspaceBackendFor("lab-1", "kubeconfig-a", "ns-a"); err != nil {
				t.Errorf("workspaceBackendFor() error = %v", err)
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, int32(1), atomic.LoadInt32(&builds), "a primed cache must serve all concurrent same-lab requests without rebuilding")
}
