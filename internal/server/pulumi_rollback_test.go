package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHandleFailedUp covers the job-state side of rolling back a failed
// deployment. The Pulumi side (rollbackStack) is injected as a fake:
// PulumiExecutor has no seam for driving a real stack in unit tests.
func TestHandleFailedUp(t *testing.T) {
	t.Parallel()

	const kubeconfig = "apiVersion: v1\nkind: Config"

	tests := []struct {
		name               string
		useExistingCluster bool
		rollbackErr        error
		expectedError      string
		expectedKubeconfig string
		expectedOutput     []string
		unexpectedOutput   []string
	}{
		{
			name:               "rollback succeeds on a dedicated cluster",
			expectedError:      "pulumi up failed: boom",
			expectedKubeconfig: "",
			expectedOutput:     []string{"Rolling back", "Rollback completed"},
			unexpectedOutput:   []string{"left in place", "Destroy Stack"},
		},
		{
			name:               "rollback succeeds on an existing cluster",
			useExistingCluster: true,
			expectedError:      "pulumi up failed: boom",
			expectedKubeconfig: kubeconfig,
			expectedOutput:     []string{"Rolling back", "Rollback completed", "left in place"},
			unexpectedOutput:   []string{"Destroy Stack"},
		},
		{
			name:               "rollback fails",
			rollbackErr:        errors.New("pulumi destroy failed: still attached"),
			expectedError:      "pulumi up failed: boom; automatic rollback failed: pulumi destroy failed: still attached",
			expectedKubeconfig: kubeconfig,
			expectedOutput:     []string{"Rolling back", "rollback failed", "Destroy Stack"},
			unexpectedOutput:   []string{"Rollback completed"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			jm := NewJobManager("")
			pe := NewPulumiExecutor(jm, t.TempDir())
			jobID := jm.CreateJob(&LabConfig{StackName: "lab", UseExistingCluster: tt.useExistingCluster})
			require.NoError(t, jm.UpdateJobStatus(jobID, JobStatusRunning))
			require.NoError(t, jm.SetKubeconfig(jobID, kubeconfig))

			stateDir := filepath.Join(pe.GetWorkDir(), jobID, ".pulumi")
			require.NoError(t, os.MkdirAll(stateDir, 0755))

			job, exists := jm.GetJob(jobID)
			require.True(t, exists)

			var statusDuringRollback JobStatus
			rollback := func() error {
				job.mu.RLock()
				statusDuringRollback = job.Status
				job.mu.RUnlock()
				return tt.rollbackErr
			}

			// Non-empty outputs keep the failed-rollback path on the outputs it was
			// given rather than refreshing a stack this test does not have.
			upResult := auto.UpResult{Outputs: auto.OutputMap{"unrelated": {Value: "x"}}}
			prep := &JobPreparation{Context: context.Background()}

			pe.handleFailedUp(jobID, prep, upResult, errors.New("boom"), rollback)

			assert.Equal(t, JobStatusRunning, statusDuringRollback, "the job must not look failed (retryable) while the rollback runs")

			job.mu.RLock()
			status, jobErr, gotKubeconfig := job.Status, job.Error, job.Kubeconfig
			output := strings.Join(job.Output, "\n")
			job.mu.RUnlock()

			assert.Equal(t, JobStatusFailed, status)
			assert.Equal(t, tt.expectedError, jobErr)
			assert.Equal(t, tt.expectedKubeconfig, gotKubeconfig)
			for _, want := range tt.expectedOutput {
				assert.Contains(t, output, want)
			}
			for _, unwanted := range tt.unexpectedOutput {
				assert.NotContains(t, output, unwanted)
			}
			if tt.rollbackErr != nil {
				assert.DirExists(t, stateDir, "stack state must survive a failed rollback so Destroy can finish it")
			}
		})
	}
}

func TestRollbackStack_JobNotFound(t *testing.T) {
	t.Parallel()

	pe := NewPulumiExecutor(NewJobManager(""), t.TempDir())

	err := pe.rollbackStack("job-missing", auto.Stack{}, &jobOutputWriter{})

	require.Error(t, err)
}
