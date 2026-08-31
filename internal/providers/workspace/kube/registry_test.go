package kube

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestEnsureBuildCache(t *testing.T) {
	b, cs := newTestBackend()
	ctx := context.Background()

	repo, err := b.EnsureBuildCache(ctx)
	require.NoError(t, err)
	assert.Equal(t, "easylab-registry-cache.workshops.svc.cluster.local:5000/cache", repo)

	dep, err := cs.AppsV1().Deployments("workshops").Get(ctx, registryCacheName, metav1.GetOptions{})
	require.NoError(t, err)
	require.Len(t, dep.Spec.Template.Spec.Containers, 1)
	assert.Equal(t, registryCacheImage, dep.Spec.Template.Spec.Containers[0].Image)

	_, err = cs.CoreV1().Services("workshops").Get(ctx, registryCacheName, metav1.GetOptions{})
	require.NoError(t, err)

	_, err = cs.CoreV1().PersistentVolumeClaims("workshops").Get(ctx, registryCacheName, metav1.GetOptions{})
	require.NoError(t, err)

	// The registry must never carry the label workspace listing selects on, or
	// it would show up as a workspace in a lab's list.
	labels := dep.Labels
	if _, ok := labels[labelLabID]; ok {
		t.Fatalf("registry cache deployment must not carry %s, it would be listed as a workspace", labelLabID)
	}
}

func TestEnsureBuildCache_Idempotent(t *testing.T) {
	b, _ := newTestBackend()
	ctx := context.Background()

	repo1, err := b.EnsureBuildCache(ctx)
	require.NoError(t, err)
	repo2, err := b.EnsureBuildCache(ctx)
	require.NoError(t, err)

	assert.Equal(t, repo1, repo2)
}

// TestBakedImageRepo_InternalIsBare guards a real bug found in production: an
// explicit :latest suffix on the internal (ENVBUILDER_CACHE_REPO push) repo broke
// envbuilder's own tag resolution for the whole-image push ("repository can only
// contain the characters ...", from a real bake's logs) — the push failed
// silently while the Job still exited 0, leaving the registry with nothing ever
// pushed to it. envbuilder appends its own tag; the internal repo must be bare.
func TestBakedImageRepo_InternalIsBare(t *testing.T) {
	b, _ := newTestBackend()

	internal, external := b.BakedImageRepo("job-1", "go-workshop", "lab.example.com")

	assert.NotContains(t, internal, ":latest", "ENVBUILDER_CACHE_REPO must be a bare repository, no tag")
	assert.Equal(t, "easylab-registry-cache.workshops.svc.cluster.local:5000/baked/job-1/go-workshop", internal)
	// The external (pull) reference is unaffected — kubelet pulls/manifest
	// lookups expect an explicit tag, which is where this bug did not occur.
	assert.Equal(t, "easylab-registry-cache.lab.example.com/baked/job-1/go-workshop:latest", external)
}
