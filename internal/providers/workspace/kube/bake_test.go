package kube

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"easylab/internal/providers/workspace"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func bakeRequest() workspace.BakeRequest {
	return workspace.BakeRequest{
		LabID:     "job-1",
		Template:  "go-workshop",
		GitRepo:   "https://gitlab.com/org/workshop.git",
		GitBranch: "main",
		Devcontainer: &workspace.DevcontainerSpec{
			Dir: ".devcontainer",
		},
	}
}

func TestEnsureBakeJob(t *testing.T) {
	b, cs := newTestBackend()
	ctx := context.Background()

	require.NoError(t, b.EnsureBakeJob(ctx, bakeRequest()))

	job, err := cs.BatchV1().Jobs("workshops").Get(ctx, bakeJobName("job-1", "go-workshop"), metav1.GetOptions{})
	require.NoError(t, err)
	require.Len(t, job.Spec.Template.Spec.Containers, 1)
	c := job.Spec.Template.Spec.Containers[0]
	assert.Equal(t, envbuilderImage, c.Image)
	assert.Equal(t, corev1.RestartPolicyNever, job.Spec.Template.Spec.RestartPolicy)
	require.NotNil(t, job.Spec.BackoffLimit)
	assert.Equal(t, int32(0), *job.Spec.BackoffLimit)
	require.NotNil(t, job.Spec.TTLSecondsAfterFinished)

	env := map[string]string{}
	for _, e := range c.Env {
		env[e.Name] = e.Value
	}
	assert.Equal(t, "https://gitlab.com/org/workshop.git#refs/heads/main", env["ENVBUILDER_GIT_URL"])
	assert.Equal(t, "true", env["ENVBUILDER_PUSH_IMAGE"])
	// A real IDE-exec line would exec() and never terminate, so the Job would never
	// reach Complete — the init script must be a trivial, immediately-exiting no-op.
	assert.Equal(t, "true", env["ENVBUILDER_INIT_SCRIPT"])
	internalRepo, _ := b.BakedImageRepo("job-1", "go-workshop", "")
	assert.Equal(t, internalRepo, env["ENVBUILDER_CACHE_REPO"])

	// Must never carry easylab.io/lab-id, or it would be listed as a workspace.
	_, hasLabID := job.Labels[labelLabID]
	assert.False(t, hasLabID)
}

func TestEnsureBakeJob_ConfigRepoClonesSeparately(t *testing.T) {
	b, cs := newTestBackend()
	ctx := context.Background()

	req := bakeRequest()
	req.Devcontainer.ConfigRepo = "https://gitlab.com/org/devcontainer-config.git"

	require.NoError(t, b.EnsureBakeJob(ctx, req))

	job, err := cs.BatchV1().Jobs("workshops").Get(ctx, bakeJobName("job-1", "go-workshop"), metav1.GetOptions{})
	require.NoError(t, err)
	require.Len(t, job.Spec.Template.Spec.InitContainers, 1)
	assert.Contains(t, job.Spec.Template.Spec.InitContainers[0].Command[len(job.Spec.Template.Spec.InitContainers[0].Command)-1], "devcontainer-config.git")
}

func TestEnsureBakeJob_RebuildDeletesPreviousJob(t *testing.T) {
	b, cs := newTestBackend()
	ctx := context.Background()

	require.NoError(t, b.EnsureBakeJob(ctx, bakeRequest()))

	// A Job's pod template is immutable: the fake clientset (like a real apiserver)
	// rejects a second Create for the same name outright. A rebuild only succeeds
	// at all if EnsureBakeJob deletes the previous Job first — so a plain no-error
	// here already proves that happened. Changing the branch and confirming it
	// lands makes the replacement directly observable too, not just inferred.
	req := bakeRequest()
	req.GitBranch = "feature/rebuilt"
	require.NoError(t, b.EnsureBakeJob(ctx, req))

	job, err := cs.BatchV1().Jobs("workshops").Get(ctx, bakeJobName("job-1", "go-workshop"), metav1.GetOptions{})
	require.NoError(t, err)
	env := map[string]string{}
	for _, e := range job.Spec.Template.Spec.Containers[0].Env {
		env[e.Name] = e.Value
	}
	assert.Contains(t, env["ENVBUILDER_GIT_URL"], "feature/rebuilt")
}

func TestBakeJobStatus(t *testing.T) {
	b, cs := newTestBackend()
	ctx := context.Background()

	t.Run("no job yet", func(t *testing.T) {
		state, err := b.BakeJobStatus(ctx, "job-1", "go-workshop")
		require.NoError(t, err)
		assert.Equal(t, workspace.BakeStateNone, state)
	})

	require.NoError(t, b.EnsureBakeJob(ctx, bakeRequest()))

	t.Run("still running", func(t *testing.T) {
		state, err := b.BakeJobStatus(ctx, "job-1", "go-workshop")
		require.NoError(t, err)
		assert.Equal(t, workspace.BakeStateBuilding, state)
	})

	name := bakeJobName("job-1", "go-workshop")
	setJobCondition(ctx, t, cs, name, batchv1.JobComplete)

	t.Run("complete", func(t *testing.T) {
		state, err := b.BakeJobStatus(ctx, "job-1", "go-workshop")
		require.NoError(t, err)
		assert.Equal(t, workspace.BakeStateComplete, state)
	})

	setJobCondition(ctx, t, cs, name, batchv1.JobFailed)

	t.Run("failed", func(t *testing.T) {
		state, err := b.BakeJobStatus(ctx, "job-1", "go-workshop")
		require.NoError(t, err)
		assert.Equal(t, workspace.BakeStateFailed, state)
	})
}

func setJobCondition(ctx context.Context, t *testing.T, cs *fake.Clientset, name string, condType batchv1.JobConditionType) {
	t.Helper()
	job, err := cs.BatchV1().Jobs("workshops").Get(ctx, name, metav1.GetOptions{})
	require.NoError(t, err)
	job.Status.Conditions = []batchv1.JobCondition{{Type: condType, Status: corev1.ConditionTrue}}
	_, err = cs.BatchV1().Jobs("workshops").UpdateStatus(ctx, job, metav1.UpdateOptions{})
	require.NoError(t, err)
}

func TestBakeJobStatus_NotFoundIsNotAnError(t *testing.T) {
	b, _ := newTestBackend()
	state, err := b.BakeJobStatus(context.Background(), "job-1", "does-not-exist")
	require.NoError(t, err)
	assert.Equal(t, workspace.BakeStateNone, state)
}

func TestBakeRemoteUser(t *testing.T) {
	configDigest := "sha256:deadbeef"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/manifests/latest"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"config": map[string]string{"digest": configDigest},
			})
		case strings.HasSuffix(r.URL.Path, "/blobs/"+configDigest):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"config": map[string]any{
					"Labels": map[string]string{
						devcontainerMetadataLabel: `[{"id":"a"},{"remoteUser":"vscode"}]`,
					},
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	b, _ := newTestBackend()
	host := strings.TrimPrefix(srv.URL, "http://")

	user, err := b.BakeRemoteUser(context.Background(), host+"/baked/job-1/go-workshop:latest", true, "")
	require.NoError(t, err)
	assert.Equal(t, "vscode", user)
}

func TestBakeRemoteUser_NoLabelMeansNoUser(t *testing.T) {
	configDigest := "sha256:cafef00d"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/manifests/latest"):
			_ = json.NewEncoder(w).Encode(map[string]any{"config": map[string]string{"digest": configDigest}})
		case strings.HasSuffix(r.URL.Path, "/blobs/"+configDigest):
			_ = json.NewEncoder(w).Encode(map[string]any{"config": map[string]any{"Labels": map[string]string{}}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	b, _ := newTestBackend()
	host := strings.TrimPrefix(srv.URL, "http://")

	user, err := b.BakeRemoteUser(context.Background(), host+"/baked/job-1/go-workshop:latest", true, "")
	require.NoError(t, err)
	assert.Empty(t, user)
}

// TestBakeRemoteUser_AuthenticatesWithRegistryAuthSecret guards the bug where a
// pull-verification GET was always anonymous, even against a registry that requires
// auth for reads (not just writes) — a correctly-pushed image was reported as
// permanently unpullable because BakeRemoteUser never sent the credentials that
// envbuilder's push itself used.
func TestBakeRemoteUser_AuthenticatesWithRegistryAuthSecret(t *testing.T) {
	configDigest := "sha256:deadbeef"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Basic dXNlcjpwYXNz" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/manifests/latest"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"config": map[string]string{"digest": configDigest},
			})
		case strings.HasSuffix(r.URL.Path, "/blobs/"+configDigest):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"config": map[string]any{
					"Labels": map[string]string{
						devcontainerMetadataLabel: `[{"remoteUser":"vscode"}]`,
					},
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	b, cs := newTestBackend()
	host := strings.TrimPrefix(srv.URL, "http://")
	repoRef := host + "/baked/job-1/go-workshop:latest"

	t.Run("anonymous request is rejected", func(t *testing.T) {
		_, err := b.BakeRemoteUser(context.Background(), repoRef, true, "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "401")
	})

	dockerConfig := fmt.Sprintf(`{"auths":{%q:{"auth":"dXNlcjpwYXNz"}}}`, host)
	_, err := cs.CoreV1().Secrets("workshops").Create(context.Background(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "regcred", Namespace: "workshops"},
		Type:       corev1.SecretTypeDockerConfigJson,
		Data:       map[string][]byte{corev1.DockerConfigJsonKey: []byte(dockerConfig)},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Run("authenticated request using the same secret the push used succeeds", func(t *testing.T) {
		user, err := b.BakeRemoteUser(context.Background(), repoRef, true, "regcred")
		require.NoError(t, err)
		assert.Equal(t, "vscode", user)
	})
}

func TestSplitImageRef(t *testing.T) {
	tests := []struct {
		name     string
		ref      string
		wantHost string
		wantRepo string
		wantTag  string
	}{
		{"with tag", "registry.example.com/baked/lab/tmpl:latest", "registry.example.com", "baked/lab/tmpl", "latest"},
		{"without tag", "registry.example.com/baked/lab/tmpl", "registry.example.com", "baked/lab/tmpl", "latest"},
		{"no repo path", "registry.example.com", "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host, repo, tag := splitImageRef(tt.ref)
			assert.Equal(t, tt.wantHost, host)
			assert.Equal(t, tt.wantRepo, repo)
			assert.Equal(t, tt.wantTag, tag)
		})
	}
}
