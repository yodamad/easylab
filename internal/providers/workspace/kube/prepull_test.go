package kube

import (
	"context"
	"strings"
	"testing"

	"easylab/internal/providers/workspace"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"
)

func getPrepull(t *testing.T, b *Backend, labID string) *appsv1.DaemonSet {
	t.Helper()
	ds, err := b.client.AppsV1().DaemonSets("workshops").Get(context.Background(), prepullName(labID), metav1.GetOptions{})
	require.NoError(t, err)
	return ds
}

// pulledImages returns the images the DaemonSet's per-image init containers pull,
// skipping the busybox bootstrap container.
func pulledImages(ds *appsv1.DaemonSet) []string {
	var out []string
	for _, c := range ds.Spec.Template.Spec.InitContainers {
		if strings.HasPrefix(c.Name, "pull-") {
			out = append(out, c.Image)
		}
	}
	return out
}

func TestPrepullName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, labID, expected string
	}{
		{name: "simple", labID: "job-123", expected: "easylab-prepull-job-123"},
		{name: "sanitized", labID: "Job_ABC", expected: "easylab-prepull-job-abc"},
		{name: "truncated without trailing dash", labID: strings.Repeat("a", 46) + "-" + strings.Repeat("b", 20), expected: "easylab-prepull-" + strings.Repeat("a", 46)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := prepullName(tt.labID)
			assert.Equal(t, tt.expected, got)
			assert.LessOrEqual(t, len(got), 63)
		})
	}
}

func TestEnsurePrepull_Create(t *testing.T) {
	t.Parallel()
	b, _ := newTestBackend()

	err := b.EnsurePrepull(context.Background(), "lab-1", workspace.PrepullRequest{
		Images:      []string{"codercom/code-server:latest", " alpine/git:latest ", "codercom/code-server:latest", ""},
		PullSecrets: []string{"regcred"},
	})
	require.NoError(t, err)

	ds := getPrepull(t, b, "lab-1")
	assert.Equal(t, managedByValue, ds.Labels[labelManagedBy])
	assert.Equal(t, "lab-1", ds.Labels[labelLabID])
	assert.NotEmpty(t, ds.Annotations[annotationPrepullHash])

	// Deduplicated, trimmed and sorted.
	assert.Equal(t, []string{"alpine/git:latest", "codercom/code-server:latest"}, pulledImages(ds))

	spec := ds.Spec.Template.Spec
	require.NotEmpty(t, spec.InitContainers)
	assert.Equal(t, prepullBinImage, spec.InitContainers[0].Image, "the static busybox is copied in first")
	for _, c := range spec.InitContainers[1:] {
		assert.Equal(t, []string{"/prepull/busybox", "true"}, c.Command, "image %s must not rely on its own shell", c.Image)
	}
	require.Len(t, spec.Containers, 1)
	assert.Equal(t, prepullPauseImage, spec.Containers[0].Image)
	require.NotNil(t, spec.AutomountServiceAccountToken)
	assert.False(t, *spec.AutomountServiceAccountToken)
	require.Len(t, spec.ImagePullSecrets, 1)
	assert.Equal(t, "regcred", spec.ImagePullSecrets[0].Name)
	assert.Nil(t, spec.NodeSelector)

	require.NotNil(t, ds.Spec.UpdateStrategy.RollingUpdate)
	assert.Equal(t, "100%", ds.Spec.UpdateStrategy.RollingUpdate.MaxUnavailable.String())
}

func TestEnsurePrepull_PullSecretsAndNodeSelector(t *testing.T) {
	t.Parallel()
	ownImage := registryCacheName + ".workshops.svc.cluster.local:5000/baked/lab-1/tmpl:latest"
	tests := []struct {
		name            string
		req             workspace.PrepullRequest
		expectedSecrets []string
	}{
		{
			name:            "no secrets",
			req:             workspace.PrepullRequest{Images: []string{"codercom/code-server:latest"}},
			expectedSecrets: nil,
		},
		{
			name:            "own registry adds its credentials",
			req:             workspace.PrepullRequest{Images: []string{ownImage}, PullSecrets: []string{"regcred"}},
			expectedSecrets: []string{registryAuthSecretName, "regcred"},
		},
		{
			name:            "duplicate secrets collapse",
			req:             workspace.PrepullRequest{Images: []string{"a/b:1"}, PullSecrets: []string{"x", "x", " "}},
			expectedSecrets: []string{"x"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			b, _ := newTestBackend()
			tt.req.NodeSelector = map[string]string{"pool": "workspaces"}
			require.NoError(t, b.EnsurePrepull(context.Background(), "lab-1", tt.req))

			spec := getPrepull(t, b, "lab-1").Spec.Template.Spec
			var got []string
			for _, s := range spec.ImagePullSecrets {
				got = append(got, s.Name)
			}
			assert.Equal(t, tt.expectedSecrets, got)
			assert.Equal(t, map[string]string{"pool": "workspaces"}, spec.NodeSelector)
		})
	}
}

// countUpdates counts DaemonSet update calls the fake client has seen.
func countUpdates(actions []k8stesting.Action) int {
	n := 0
	for _, a := range actions {
		if a.GetVerb() == "update" && a.GetResource().Resource == "daemonsets" {
			n++
		}
	}
	return n
}

func TestEnsurePrepull_UpdatesOnlyOnChange(t *testing.T) {
	t.Parallel()
	b, cs := newTestBackend()
	ctx := context.Background()
	req := workspace.PrepullRequest{Images: []string{"a/one:1", "a/two:1"}}

	require.NoError(t, b.EnsurePrepull(ctx, "lab-1", req))
	firstHash := getPrepull(t, b, "lab-1").Annotations[annotationPrepullHash]

	// Same set in another order: no update.
	require.NoError(t, b.EnsurePrepull(ctx, "lab-1", workspace.PrepullRequest{Images: []string{"a/two:1", "a/one:1"}}))
	assert.Equal(t, 0, countUpdates(cs.Actions()))

	// New image: updated in place.
	require.NoError(t, b.EnsurePrepull(ctx, "lab-1", workspace.PrepullRequest{Images: []string{"a/one:1", "a/two:1", "a/three:1"}}))
	assert.Equal(t, 1, countUpdates(cs.Actions()))
	ds := getPrepull(t, b, "lab-1")
	assert.NotEqual(t, firstHash, ds.Annotations[annotationPrepullHash])
	assert.Equal(t, []string{"a/one:1", "a/three:1", "a/two:1"}, pulledImages(ds))
}

func TestEnsurePrepull_EmptyRemoves(t *testing.T) {
	t.Parallel()
	b, _ := newTestBackend()
	ctx := context.Background()

	require.NoError(t, b.EnsurePrepull(ctx, "lab-1", workspace.PrepullRequest{Images: []string{"a/one:1"}}))
	require.NoError(t, b.EnsurePrepull(ctx, "lab-1", workspace.PrepullRequest{Images: []string{" "}}))

	list, err := b.client.AppsV1().DaemonSets("workshops").List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	assert.Empty(t, list.Items)
}

func TestRemovePrepull(t *testing.T) {
	t.Parallel()
	b, _ := newTestBackend()
	ctx := context.Background()

	// Absent is not an error.
	require.NoError(t, b.RemovePrepull(ctx, "lab-1"))

	require.NoError(t, b.EnsurePrepull(ctx, "lab-1", workspace.PrepullRequest{Images: []string{"a/one:1"}}))
	require.NoError(t, b.EnsurePrepull(ctx, "lab-2", workspace.PrepullRequest{Images: []string{"a/one:1"}}))
	require.NoError(t, b.RemovePrepull(ctx, "lab-1"))

	list, err := b.client.AppsV1().DaemonSets("workshops").List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, list.Items, 1, "only the removed lab's DaemonSet goes")
	assert.Equal(t, prepullName("lab-2"), list.Items[0].Name)
}

func TestEnsurePrepull_NotAWorkspace(t *testing.T) {
	t.Parallel()
	b, _ := newTestBackend()
	ctx := context.Background()

	require.NoError(t, b.EnsurePrepull(ctx, "lab-1", workspace.PrepullRequest{Images: []string{"a/one:1"}}))
	ws, err := b.ListWorkspaces(ctx, "lab-1")
	require.NoError(t, err)
	assert.Empty(t, ws, "the pre-pull DaemonSet must not show up as a student workspace")
}

func TestEnsurePrepull_CreateErrorWrapped(t *testing.T) {
	t.Parallel()
	b, cs := newTestBackend()
	cs.PrependReactor("create", "daemonsets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, assert.AnError
	})

	err := b.EnsurePrepull(context.Background(), "lab-1", workspace.PrepullRequest{Images: []string{"a/one:1"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create pre-pull daemonset")
}
