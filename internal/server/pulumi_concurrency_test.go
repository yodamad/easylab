package server

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- pulumiExecutionConcurrency tests ---

func TestPulumiExecutionConcurrency_Default(t *testing.T) {
	assert.Equal(t, 5, pulumiExecutionConcurrency())
}

func TestPulumiExecutionConcurrency_EnvOverride(t *testing.T) {
	t.Setenv("PULUMI_EXECUTION_CONCURRENCY", "3")
	assert.Equal(t, 3, pulumiExecutionConcurrency())
}

func TestPulumiExecutionConcurrency_InvalidEnvFallsBackToDefault(t *testing.T) {
	t.Setenv("PULUMI_EXECUTION_CONCURRENCY", "not-a-number")
	assert.Equal(t, 5, pulumiExecutionConcurrency())
}

func TestPulumiExecutionConcurrency_ZeroFallsBackToDefault(t *testing.T) {
	t.Setenv("PULUMI_EXECUTION_CONCURRENCY", "0")
	assert.Equal(t, 5, pulumiExecutionConcurrency())
}

// TestPulumiExecSem_BoundsConcurrentExecutions proves the pulumiExecSem safety
// property directly against the channel NewHandler sizes: however many
// goroutines race to acquire it, no more than PULUMI_EXECUTION_CONCURRENCY run
// at once. The 7 production call sites (executeLabJobWithID, LaunchLab,
// DestroyStack, RecreateLab, RetryJob, RetryJobWithConfig,
// cleanupExpiredLabs) all follow the identical acquire/defer-release shape
// this test exercises; PulumiExecutor has no test seam for driving concurrency
// through those call sites directly (it always runs real Pulumi Automation API
// calls), so this test targets the shared semaphore itself.
func TestPulumiExecSem_BoundsConcurrentExecutions(t *testing.T) {
	const limit = 2
	const jobs = 6
	t.Setenv("PULUMI_EXECUTION_CONCURRENCY", "2")

	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

	var inFlight, maxSeen int32
	var wg sync.WaitGroup
	wg.Add(jobs)
	for i := 0; i < jobs; i++ {
		go func() {
			defer wg.Done()
			h.pulumiExecSem <- struct{}{}
			defer func() { <-h.pulumiExecSem }()

			n := atomic.AddInt32(&inFlight, 1)
			for {
				cur := atomic.LoadInt32(&maxSeen)
				if n <= cur || atomic.CompareAndSwapInt32(&maxSeen, cur, n) {
					break
				}
			}
			time.Sleep(20 * time.Millisecond)
			atomic.AddInt32(&inFlight, -1)
		}()
	}
	wg.Wait()

	t.Logf("observed max concurrent pulumiExecSem holders: %d (limit %d)", maxSeen, limit)
	require.LessOrEqual(t, maxSeen, int32(limit), "pulumiExecSem must never let more than the configured limit run at once")
}
