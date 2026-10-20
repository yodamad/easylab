package kube

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"easylab/internal/providers/workspace"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestParseHealthz(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	hourAgo := now.Add(-time.Hour)

	tests := []struct {
		name     string
		input    string
		expected time.Time
		wantErr  bool
	}{
		{name: "alive", input: `{"status":"alive","lastHeartbeat":1}`, expected: now},
		{
			// A pod clock running behind must not make an in-use workspace look idle.
			name:     "alive wins over a stale timestamp",
			input:    `{"status":"alive","lastHeartbeat":1000}`,
			expected: now,
		},
		{
			name:     "expired reports the last heartbeat",
			input:    `{"status":"expired","lastHeartbeat":` + strconv.FormatInt(hourAgo.UnixMilli(), 10) + `}`,
			expected: hourAgo,
		},
		{name: "never used", input: `{"status":"expired","lastHeartbeat":0}`, expected: time.Time{}},
		{name: "not json", input: `<html>404</html>`, wantErr: true},
		{name: "empty body", input: ``, wantErr: true},
		{name: "json that is not a healthz payload", input: `{"ok":true}`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseHealthz([]byte(tt.input), now)

			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.True(t, tt.expected.Equal(got), "got %v, want %v", got, tt.expected)
		})
	}
}

func TestReadHealthz(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "real payload", input: `{"status":"alive","lastHeartbeat":1}`},
		{name: "empty", input: ""},
		{name: "exactly the limit", input: strings.Repeat("a", healthzMaxBytes)},
		{name: "one byte over the limit is refused", input: strings.Repeat("a", healthzMaxBytes+1), wantErr: true},
		{name: "far over the limit is refused", input: strings.Repeat("a", 1<<20), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := readHealthz(strings.NewReader(tt.input))

			if tt.wantErr {
				require.Error(t, err)
				assert.Nil(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.input, string(got))
		})
	}
}

func TestWorkspaceLastActivity(t *testing.T) {
	t.Parallel()

	// The probe resolves the workspace's IDE from its Deployment, so every case
	// here needs one to exist.
	ensure := func(t *testing.T, ide string) (*Backend, workspace.Workspace) {
		t.Helper()
		b, _ := newTestBackend()
		spec := workspace.Spec{LabID: "job-1", Owner: "alice", IDE: ide, DiskSize: "5Gi", Domain: "lab.example.com", Token: "secret-token"}
		if ide == workspace.IDEOpenChamber {
			spec.Image = "registry.example.com/openchamber:1"
		}
		ws, err := b.EnsureWorkspace(context.Background(), spec)
		require.NoError(t, err)
		return b, ws
	}

	t.Run("reads the workspace's own healthz", func(t *testing.T) {
		t.Parallel()
		b, ws := ensure(t, workspace.IDECodeServer)
		var asked string
		b.healthz = func(_ context.Context, name string) ([]byte, error) {
			asked = name
			return []byte(`{"status":"alive","lastHeartbeat":1}`), nil
		}

		got, err := b.WorkspaceLastActivity(context.Background(), ws.ID)

		require.NoError(t, err)
		assert.Equal(t, ws.ID, asked)
		assert.WithinDuration(t, time.Now(), got, time.Minute)
	})

	t.Run("a failed probe is an error, not idleness", func(t *testing.T) {
		t.Parallel()
		b, ws := ensure(t, workspace.IDECodeServer)
		b.healthz = func(context.Context, string) ([]byte, error) { return nil, errors.New("no endpoints") }

		_, err := b.WorkspaceLastActivity(context.Background(), ws.ID)

		require.Error(t, err)
	})

	t.Run("an IDE with no activity endpoint is reported as unsupported, unprobed", func(t *testing.T) {
		t.Parallel()
		b, ws := ensure(t, workspace.IDEOpenChamber)
		probed := false
		b.healthz = func(context.Context, string) ([]byte, error) {
			probed = true
			return []byte(`{"status":"alive"}`), nil
		}

		got, err := b.WorkspaceLastActivity(context.Background(), ws.ID)

		require.ErrorIs(t, err, workspace.ErrActivityUnsupported)
		assert.True(t, got.IsZero())
		assert.False(t, probed, "an IDE with no activity endpoint must not be probed at one")
	})

	t.Run("a missing workspace is an error", func(t *testing.T) {
		t.Parallel()
		b, _ := newTestBackend()

		_, err := b.WorkspaceLastActivity(context.Background(), "ws-gone")

		require.Error(t, err)
		assert.NotErrorIs(t, err, workspace.ErrActivityUnsupported)
	})
}

func TestStopAndStartWorkspace(t *testing.T) {
	t.Parallel()

	b, cs := newTestBackend()
	ctx := context.Background()
	ws, err := b.EnsureWorkspace(ctx, workspace.Spec{LabID: "job-1", Owner: "alice", DiskSize: "5Gi", Domain: "lab.example.com", Token: "secret-token"})
	require.NoError(t, err)

	require.NoError(t, b.StopWorkspace(ctx, ws.ID))

	dep, err := cs.AppsV1().Deployments("workshops").Get(ctx, ws.ID, metav1.GetOptions{})
	require.NoError(t, err)
	require.NotNil(t, dep.Spec.Replicas)
	assert.Equal(t, int32(0), *dep.Spec.Replicas)
	_, err = cs.CoreV1().PersistentVolumeClaims("workshops").Get(ctx, ws.ID, metav1.GetOptions{})
	assert.NoError(t, err, "the student's volume must survive a pause")
	_, err = cs.CoreV1().Services("workshops").Get(ctx, ws.ID, metav1.GetOptions{})
	assert.NoError(t, err)
	_, err = cs.NetworkingV1().Ingresses("workshops").Get(ctx, ws.ID, metav1.GetOptions{})
	assert.NoError(t, err)

	paused, err := b.GetWorkspace(ctx, ws.ID)
	require.NoError(t, err)
	assert.Equal(t, workspace.PhaseStopped, paused.Phase)
	assert.False(t, paused.Ready)
	assert.Equal(t, "secret-token", paused.Token, "a resumed workspace keeps its token")

	require.NoError(t, b.StopWorkspace(ctx, ws.ID), "stopping twice is not an error")

	require.NoError(t, b.StartWorkspace(ctx, ws.ID))
	dep, err = cs.AppsV1().Deployments("workshops").Get(ctx, ws.ID, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, int32(1), *dep.Spec.Replicas)
	resumed, err := b.GetWorkspace(ctx, ws.ID)
	require.NoError(t, err)
	assert.NotEqual(t, workspace.PhaseStopped, resumed.Phase)

	require.NoError(t, b.StartWorkspace(ctx, ws.ID), "starting twice is not an error")
}

func TestListWorkspaces_ReportsEphemeral(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		spec      workspace.Spec
		ephemeral bool
	}{
		{
			name: "volume-backed workspace can be paused",
			spec: workspace.Spec{LabID: "job-1", Owner: "alice", DiskSize: "5Gi"},
		},
		{
			name:      "ephemeral workspace would lose its files",
			spec:      workspace.Spec{LabID: "job-1", Owner: "bob", Ephemeral: true},
			ephemeral: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			b, _ := newTestBackend()
			ctx := context.Background()
			created, err := b.EnsureWorkspace(ctx, tt.spec)
			require.NoError(t, err)

			got, err := b.GetWorkspace(ctx, created.ID)
			require.NoError(t, err)
			assert.Equal(t, tt.ephemeral, got.Ephemeral)

			list, err := b.ListWorkspaces(ctx, tt.spec.LabID)
			require.NoError(t, err)
			require.Len(t, list, 1)
			assert.Equal(t, tt.ephemeral, list[0].Ephemeral)
		})
	}
}

func TestStopWorkspace_Refusals(t *testing.T) {
	t.Parallel()

	t.Run("ephemeral workspace would lose its files", func(t *testing.T) {
		t.Parallel()
		b, cs := newTestBackend()
		ctx := context.Background()
		ws, err := b.EnsureWorkspace(ctx, workspace.Spec{LabID: "job-1", Owner: "alice", Ephemeral: true, Domain: "lab.example.com"})
		require.NoError(t, err)

		require.Error(t, b.StopWorkspace(ctx, ws.ID))

		dep, err := cs.AppsV1().Deployments("workshops").Get(ctx, ws.ID, metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, int32(1), *dep.Spec.Replicas)
	})

	t.Run("unknown workspace", func(t *testing.T) {
		t.Parallel()
		b, _ := newTestBackend()
		require.Error(t, b.StopWorkspace(context.Background(), "nope"))
		require.Error(t, b.StartWorkspace(context.Background(), "nope"))
	})
}

func TestDeploymentReadiness_StoppedWinsOverStaleReadyReplicas(t *testing.T) {
	t.Parallel()

	b, cs := newTestBackend()
	ctx := context.Background()
	ws, err := b.EnsureWorkspace(ctx, workspace.Spec{LabID: "job-1", Owner: "alice", DiskSize: "5Gi"})
	require.NoError(t, err)
	require.NoError(t, b.StopWorkspace(ctx, ws.ID))

	// Right after a scale-down the old pod can still be counted as ready.
	dep, err := cs.AppsV1().Deployments("workshops").Get(ctx, ws.ID, metav1.GetOptions{})
	require.NoError(t, err)
	dep.Status.ReadyReplicas = 1

	phase, ready := deploymentReadiness(dep)
	assert.Equal(t, workspace.PhaseStopped, phase)
	assert.False(t, ready)
}
