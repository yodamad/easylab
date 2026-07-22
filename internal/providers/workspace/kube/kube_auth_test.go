package kube

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"easylab/internal/providers/workspace"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// envRefOf indexes a container's secret-backed env vars by name. envOf cannot be
// reused: it reads e.Value, which is empty for every var sourced from a
// secretKeyRef — the whole point of them.
func envRefOf(c corev1.Container) map[string]*corev1.SecretKeySelector {
	out := make(map[string]*corev1.SecretKeySelector, len(c.Env))
	for _, e := range c.Env {
		if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil {
			out[e.Name] = e.ValueFrom.SecretKeyRef
		}
	}
	return out
}

// gitAuthSecret writes a basic-auth Secret the way EnsureGitAuthSecret does.
func gitAuthSecret(t *testing.T, cs *fake.Clientset, name, user, pass string) {
	t.Helper()
	_, err := cs.CoreV1().Secrets("workshops").Create(context.Background(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "workshops"},
		Type:       corev1.SecretTypeBasicAuth,
		Data: map[string][]byte{
			corev1.BasicAuthUsernameKey: []byte(user),
			corev1.BasicAuthPasswordKey: []byte(pass),
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
}

// plainGitSpec is a non-devcontainer workspace that clones a repo, so it gets a
// git-clone init container (which requires a PVC).
func plainGitSpec() workspace.Spec {
	return workspace.Spec{
		LabID:     "job-1",
		Owner:     "alice",
		Template:  "default",
		Domain:    "lab.example.com",
		Token:     "tok123",
		GitRepo:   "https://gitlab.com/org/private.git",
		GitBranch: "main",
		DiskSize:  "5Gi",
	}
}

func TestImagePullSecrets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input []string
		want  []corev1.LocalObjectReference
	}{
		{name: "nil when none", input: nil, want: nil},
		{name: "nil when all blank", input: []string{"", "   "}, want: nil},
		{
			name:  "one",
			input: []string{"regcred"},
			want:  []corev1.LocalObjectReference{{Name: "regcred"}},
		},
		{
			name:  "several, order preserved",
			input: []string{"regcred", "otherreg"},
			want:  []corev1.LocalObjectReference{{Name: "regcred"}, {Name: "otherreg"}},
		},
		{
			name:  "blanks skipped, rest kept",
			input: []string{"regcred", "  ", "otherreg"},
			want:  []corev1.LocalObjectReference{{Name: "regcred"}, {Name: "otherreg"}},
		},
		{
			name:  "whitespace trimmed",
			input: []string{"  regcred  "},
			want:  []corev1.LocalObjectReference{{Name: "regcred"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, imagePullSecrets(tt.input))
		})
	}
}

func TestEnsureWorkspace_ImagePullSecretsOnPodSpec(t *testing.T) {
	b, cs := newTestBackend()

	spec := plainGitSpec()
	spec.ImagePullSecrets = []string{"regcred", "otherreg"}
	ws, err := b.EnsureWorkspace(context.Background(), spec)
	require.NoError(t, err)

	dep, err := cs.AppsV1().Deployments("workshops").Get(context.Background(), ws.ID, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t,
		[]corev1.LocalObjectReference{{Name: "regcred"}, {Name: "otherreg"}},
		dep.Spec.Template.Spec.ImagePullSecrets)
}

// A workspace that names no pull secrets must not acquire one. This is the
// regression guard for the behaviour every existing lab depends on.
func TestEnsureWorkspace_NoImagePullSecretsByDefault(t *testing.T) {
	b, cs := newTestBackend()

	ws, err := b.EnsureWorkspace(context.Background(), plainGitSpec())
	require.NoError(t, err)

	dep, err := cs.AppsV1().Deployments("workshops").Get(context.Background(), ws.ID, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Empty(t, dep.Spec.Template.Spec.ImagePullSecrets)
}

func TestGitCloneInit_WithoutAuth(t *testing.T) {
	t.Parallel()

	c := gitCloneInit("https://gitlab.com/org/public.git", "main", "/home/workspace", "")

	assert.Empty(t, c.Env, "an anonymous clone needs no credentials in its environment")
	require.Len(t, c.Command, 3)
	assert.NotContains(t, c.Command[2], "credential.helper")
	assert.Contains(t, c.Command[2], "git clone")
}

func TestGitCloneInit_WithAuth(t *testing.T) {
	t.Parallel()

	c := gitCloneInit("https://gitlab.com/org/private.git", "main", "/home/workspace", "gitcred")

	refs := envRefOf(c)
	require.Contains(t, refs, "GIT_USERNAME")
	require.Contains(t, refs, "GIT_PASSWORD")
	assert.Equal(t, "gitcred", refs["GIT_USERNAME"].Name)
	assert.Equal(t, corev1.BasicAuthUsernameKey, refs["GIT_USERNAME"].Key)
	assert.Equal(t, "gitcred", refs["GIT_PASSWORD"].Name)
	assert.Equal(t, corev1.BasicAuthPasswordKey, refs["GIT_PASSWORD"].Key)

	// Values must arrive by reference, never inline.
	for _, e := range c.Env {
		assert.Empty(t, e.Value, "env %q carries a literal value", e.Name)
		require.NotNil(t, e.ValueFrom, "env %q is not sourced from a secret", e.Name)
	}

	require.Len(t, c.Command, 3)
	script := c.Command[2]
	// The list is reset before ours is set; without the reset an inherited helper
	// would answer first and win.
	assert.Contains(t, script, "-c credential.helper= -c credential.helper=")
	assert.Contains(t, script, "${GIT_USERNAME}")
	assert.Contains(t, script, "${GIT_PASSWORD}")
}

// The clone script is assembled by string formatting through two levels of shell
// quoting, so its syntax is otherwise only verified by eye.
func TestGitCloneInit_ScriptIsValidSh(t *testing.T) {
	t.Parallel()

	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not available")
	}

	tests := []struct {
		name                     string
		repo, branch, authSecret string
	}{
		{name: "anonymous", repo: "https://gitlab.com/org/p.git", branch: "main"},
		{name: "authenticated", repo: "https://gitlab.com/org/p.git", branch: "main", authSecret: "gitcred"},
		{name: "no branch", repo: "https://gitlab.com/org/p.git", authSecret: "gitcred"},
		// Shell metacharacters in admin-supplied values must not break out.
		{name: "quote in branch", repo: "https://gitlab.com/org/p.git", branch: `it's; rm -rf /`, authSecret: "gitcred"},
		{name: "quote in repo", repo: `https://gitlab.com/org/p.git'; touch /pwned; '`, branch: "main"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := gitCloneInit(tt.repo, tt.branch, "/home/workspace", tt.authSecret)
			require.Len(t, c.Command, 3)

			cmd := exec.Command(sh, "-n", "-c", c.Command[2])
			out, err := cmd.CombinedOutput()
			assert.NoError(t, err, "generated script is not valid sh: %s\nscript: %s", out, c.Command[2])
		})
	}
}

func TestEnsureWorkspace_GitAuthSecretWiredIntoCloneInit(t *testing.T) {
	b, cs := newTestBackend()
	gitAuthSecret(t, cs, "gitcred", "oauth2", "glpat-example")

	spec := plainGitSpec()
	spec.GitAuthSecret = "gitcred"
	ws, err := b.EnsureWorkspace(context.Background(), spec)
	require.NoError(t, err)

	c, found := initContainerNamed(t, cs, ws.ID, "git-clone")
	require.True(t, found, "git-clone init container missing")
	assert.Contains(t, envRefOf(c), "GIT_PASSWORD")
}

// The test this whole feature exists to pass: whatever else changes, the token
// must not be readable from the Deployment. Mirrors the templates-export guard in
// internal/server/workspace_yaml_test.go.
func TestEnsureWorkspace_GitAuthSecretNeverInPodSpec(t *testing.T) {
	const token = "s3cr3t-glpat-do-not-leak"

	for _, mode := range []string{"plain", "devcontainer"} {
		t.Run(mode, func(t *testing.T) {
			b, cs := newTestBackend()
			gitAuthSecret(t, cs, "gitcred", "oauth2", token)

			spec := plainGitSpec()
			if mode == "devcontainer" {
				spec = devcontainerSpec()
			}
			spec.GitAuthSecret = "gitcred"

			ws, err := b.EnsureWorkspace(context.Background(), spec)
			require.NoError(t, err)

			dep, err := cs.AppsV1().Deployments("workshops").Get(context.Background(), ws.ID, metav1.GetOptions{})
			require.NoError(t, err)

			encoded, err := json.Marshal(dep)
			require.NoError(t, err)
			assert.NotContains(t, string(encoded), token,
				"the git token is readable from the Deployment — anyone with read access to the namespace can recover it")
		})
	}
}

func TestDevcontainerEnv_GitAuthByReference(t *testing.T) {
	b, cs := newTestBackend()
	gitAuthSecret(t, cs, "gitcred", "oauth2", "glpat-example")

	spec := devcontainerSpec()
	spec.GitAuthSecret = "gitcred"
	ws, err := b.EnsureWorkspace(context.Background(), spec)
	require.NoError(t, err)

	dep, err := cs.AppsV1().Deployments("workshops").Get(context.Background(), ws.ID, metav1.GetOptions{})
	require.NoError(t, err)

	var workspaceContainer corev1.Container
	for _, c := range dep.Spec.Template.Spec.Containers {
		if c.Name == workspaceContainerName {
			workspaceContainer = c
		}
	}
	refs := envRefOf(workspaceContainer)
	require.Contains(t, refs, "ENVBUILDER_GIT_USERNAME")
	require.Contains(t, refs, "ENVBUILDER_GIT_PASSWORD")
	assert.Equal(t, "gitcred", refs["ENVBUILDER_GIT_PASSWORD"].Name)
	assert.Equal(t, corev1.BasicAuthPasswordKey, refs["ENVBUILDER_GIT_PASSWORD"].Key)

	// The URL must stay clean: envbuilder takes the credentials from the env.
	env := envOf(workspaceContainer)
	assert.NotContains(t, env["ENVBUILDER_GIT_URL"], "@gitlab.com")
}

func TestDevcontainerEnv_NoGitAuthWhenUnset(t *testing.T) {
	b, cs := newTestBackend()

	ws, err := b.EnsureWorkspace(context.Background(), devcontainerSpec())
	require.NoError(t, err)

	dep, err := cs.AppsV1().Deployments("workshops").Get(context.Background(), ws.ID, metav1.GetOptions{})
	require.NoError(t, err)
	for _, c := range dep.Spec.Template.Spec.Containers {
		if c.Name != workspaceContainerName {
			continue
		}
		refs := envRefOf(c)
		assert.NotContains(t, refs, "ENVBUILDER_GIT_USERNAME")
		assert.NotContains(t, refs, "ENVBUILDER_GIT_PASSWORD")
	}
}

// A bad reference must fail where the admin sees it, not as a pod stuck in
// CreateContainerConfigError. Mirrors the registry-auth behaviour asserted in
// kube_devcontainer_test.go.
func TestEnsureWorkspace_GitAuthSecretFailuresAreLoud(t *testing.T) {
	tests := []struct {
		name    string
		seed    func(*testing.T, *fake.Clientset)
		wantErr string
	}{
		{
			name:    "missing secret",
			seed:    func(*testing.T, *fake.Clientset) {},
			wantErr: `failed to read git auth secret "gitcred"`,
		},
		{
			name: "secret without password key",
			seed: func(t *testing.T, cs *fake.Clientset) {
				_, err := cs.CoreV1().Secrets("workshops").Create(context.Background(), &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{Name: "gitcred", Namespace: "workshops"},
					Type:       corev1.SecretTypeBasicAuth,
					Data:       map[string][]byte{corev1.BasicAuthUsernameKey: []byte("oauth2")},
				}, metav1.CreateOptions{})
				require.NoError(t, err)
			},
			wantErr: `git auth secret "gitcred" has no "password" key`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, cs := newTestBackend()
			tt.seed(t, cs)

			spec := plainGitSpec()
			spec.GitAuthSecret = "gitcred"
			_, err := b.EnsureWorkspace(context.Background(), spec)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// A template with no repo has nothing to authenticate to, so a stale secret
// reference must not block the workspace.
func TestEnsureWorkspace_GitAuthSecretIgnoredWithoutRepo(t *testing.T) {
	b, _ := newTestBackend()

	spec := plainGitSpec()
	spec.GitRepo = ""
	spec.GitAuthSecret = "does-not-exist"

	_, err := b.EnsureWorkspace(context.Background(), spec)
	assert.NoError(t, err)
}

func TestVerifyGitAuthSecret_EmptyNameIsAnonymous(t *testing.T) {
	b, _ := newTestBackend()
	assert.NoError(t, b.verifyGitAuthSecret(context.Background(), "  "))
}

// The helper is a shell function git evaluates; it must echo the protocol's
// key=value lines and read them from the environment.
func TestGitCredentialHelper_Shape(t *testing.T) {
	t.Parallel()

	assert.True(t, strings.HasPrefix(gitCredentialHelper, "!"),
		"git treats a helper as a shell snippet only when it starts with !")
	assert.Contains(t, gitCredentialHelper, "username=${GIT_USERNAME}")
	assert.Contains(t, gitCredentialHelper, "password=${GIT_PASSWORD}")
}
