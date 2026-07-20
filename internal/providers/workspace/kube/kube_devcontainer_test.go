package kube

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"easylab/internal/providers/workspace"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// devcontainerSpec is a workable devcontainer workspace spec; tests tweak a copy.
func devcontainerSpec() workspace.Spec {
	return workspace.Spec{
		LabID:     "job-1",
		Owner:     "alice",
		Template:  "go-workshop",
		Domain:    "lab.example.com",
		Token:     "tok123",
		GitRepo:   "https://gitlab.com/org/workshop.git",
		GitBranch: "main",
		DiskSize:  "20Gi",
		Devcontainer: &workspace.DevcontainerSpec{
			Dir:       ".devcontainer",
			CacheRepo: "registry.example.com/easylab/cache",
		},
	}
}

// envOf indexes a container's env vars by name.
func envOf(c corev1.Container) map[string]string {
	out := make(map[string]string, len(c.Env))
	for _, e := range c.Env {
		out[e.Name] = e.Value
	}
	return out
}

func initContainerNamed(t *testing.T, cs *fake.Clientset, depName, container string) (corev1.Container, bool) {
	t.Helper()
	dep, err := cs.AppsV1().Deployments("workshops").Get(context.Background(), depName, metav1.GetOptions{})
	require.NoError(t, err)
	for _, c := range dep.Spec.Template.Spec.InitContainers {
		if c.Name == container {
			return c, true
		}
	}
	return corev1.Container{}, false
}

func TestEnsureWorkspace_DevcontainerRunsEnvbuilder(t *testing.T) {
	b, cs := newTestBackend()

	ws, err := b.EnsureWorkspace(context.Background(), devcontainerSpec())
	require.NoError(t, err)

	c := ideContainer(t, cs, ws.ID)
	assert.Equal(t, envbuilderImage, c.Image, "devcontainer mode must run envbuilder, not the IDE image")

	// envbuilder is configured entirely through env; its own entrypoint must run.
	assert.Empty(t, c.Command)
	assert.Empty(t, c.Args)

	// It builds into the container filesystem, which requires root.
	require.NotNil(t, c.SecurityContext)
	require.NotNil(t, c.SecurityContext.RunAsUser)
	assert.Equal(t, int64(0), *c.SecurityContext.RunAsUser)

	// Routing is unchanged: still the IDE's port, so the student contract holds.
	assert.Equal(t, int32(3000), c.Ports[0].ContainerPort)
	assert.Equal(t, workspace.IDEOpenVSCode, ws.IDE)
	assert.True(t, strings.HasSuffix(ws.OpenURL, "?tkn=tok123"))
}

func TestEnsureWorkspace_DevcontainerEnv(t *testing.T) {
	b, cs := newTestBackend()

	ws, err := b.EnsureWorkspace(context.Background(), devcontainerSpec())
	require.NoError(t, err)
	env := envOf(ideContainer(t, cs, ws.ID))

	// The branch rides on the URL as a fragment — envbuilder has no branch option.
	assert.Equal(t, "https://gitlab.com/org/workshop.git#refs/heads/main", env["ENVBUILDER_GIT_URL"])
	assert.Equal(t, ".devcontainer", env["ENVBUILDER_DEVCONTAINER_DIR"])
	assert.Equal(t, "registry.example.com/easylab/cache", env["ENVBUILDER_CACHE_REPO"])
	// Pushing is what lets the second student skip the build.
	assert.Equal(t, "true", env["ENVBUILDER_PUSH_IMAGE"])
	// A failed build must not quietly hand over a workspace missing its tools.
	assert.Equal(t, "true", env["ENVBUILDER_EXIT_ON_BUILD_FAILURE"])
	// Clone where a plain workspace clones, so git_folder means the same thing.
	assert.Equal(t, "/home/workspace", env["ENVBUILDER_WORKSPACE_FOLDER"])

	// The build wipes the filesystem; the IDE and the student's files must survive.
	assert.Contains(t, env["ENVBUILDER_IGNORE_PATHS"], ideMountPath)
	assert.Contains(t, env["ENVBUILDER_IGNORE_PATHS"], "/home/workspace")

	// The init script starts the injected IDE, not one from the image. Args are
	// shell-quoted because the script is handed to a shell, not exec'd directly.
	assert.Contains(t, env["ENVBUILDER_INIT_SCRIPT"], "exec "+ideMountPath+"/bin/openvscode-server")
	assert.Contains(t, env["ENVBUILDER_INIT_SCRIPT"], "'--connection-token' 'tok123'")
}

func TestEnsureWorkspace_DevcontainerInjectsIDE(t *testing.T) {
	b, cs := newTestBackend()

	ws, err := b.EnsureWorkspace(context.Background(), devcontainerSpec())
	require.NoError(t, err)

	inject, ok := initContainerNamed(t, cs, ws.ID, "ide-inject")
	require.True(t, ok, "devcontainer mode must inject an IDE: the built image has none")
	assert.Contains(t, inject.Image, "openvscode-server")

	script := inject.Command[len(inject.Command)-1]
	assert.Contains(t, script, ideMountPath)
	// The init script runs as the devcontainer's own user, not the copier's uid.
	assert.Contains(t, script, "chmod -R a+rX")

	// The copy runs as root: the emptyDir mount root is root-owned, so cp and chmod
	// against it fail with EPERM under the image's default uid 1000.
	require.NotNil(t, inject.SecurityContext, "ide-inject needs a security context to run as root")
	require.NotNil(t, inject.SecurityContext.RunAsUser, "ide-inject must pin RunAsUser")
	assert.Equal(t, int64(0), *inject.SecurityContext.RunAsUser, "ide-inject must copy as root")

	// The bundle must be mounted into the workspace container too, or the init
	// script has nothing to exec.
	c := ideContainer(t, cs, ws.ID)
	var mounted bool
	for _, m := range c.VolumeMounts {
		if m.Name == ideVolumeName && m.MountPath == ideMountPath {
			mounted = true
		}
	}
	assert.True(t, mounted, "IDE volume must be mounted into the workspace container")
}

// TestEnsureWorkspace_DevcontainerSkipsGitClone pins that envbuilder owns the
// clone: a git-clone init container would race it for the same directory.
func TestEnsureWorkspace_DevcontainerSkipsGitClone(t *testing.T) {
	b, cs := newTestBackend()

	ws, err := b.EnsureWorkspace(context.Background(), devcontainerSpec())
	require.NoError(t, err)

	_, ok := initContainerNamed(t, cs, ws.ID, "git-clone")
	assert.False(t, ok, "envbuilder clones the repo itself")
}

// TestEnsureWorkspace_PlainModeStillClones is the counterpart: the devcontainer
// branch must not have changed the ordinary path.
func TestEnsureWorkspace_PlainModeStillClones(t *testing.T) {
	b, cs := newTestBackend()

	ws, err := b.EnsureWorkspace(context.Background(), workspace.Spec{
		LabID: "job-1", Owner: "bob", Domain: "d", Token: "t",
		GitRepo: "https://gitlab.com/org/workshop.git", DiskSize: "5Gi",
	})
	require.NoError(t, err)

	_, ok := initContainerNamed(t, cs, ws.ID, "git-clone")
	assert.True(t, ok)

	c := ideContainer(t, cs, ws.ID)
	assert.Contains(t, c.Image, "openvscode-server")
	assert.NotEqual(t, envbuilderImage, c.Image)
}

func TestEnsureWorkspace_DevcontainerAppliesSetupSteps(t *testing.T) {
	b, cs := newTestBackend()

	spec := devcontainerSpec()
	spec.StartupScript = "echo hello"
	spec.Extensions = []string{"golang.go"}
	spec.GitFolder = "exercises"

	ws, err := b.EnsureWorkspace(context.Background(), spec)
	require.NoError(t, err)

	script := envOf(ideContainer(t, cs, ws.ID))["ENVBUILDER_INIT_SCRIPT"]

	// A devcontainer workspace gets the same provisioning as a plain one — the
	// extensions especially, since envbuilder only partially supports them.
	assert.Contains(t, script, "echo hello")
	// Extensions install via the injected bundle, not a binary from the image.
	assert.Contains(t, script, ideMountPath+"/bin/openvscode-server --install-extension 'golang.go'")
}

func TestEnsureWorkspace_DevcontainerRegistryAuth(t *testing.T) {
	b, cs := newTestBackend()

	dockerConfig := `{"auths":{"registry.example.com":{"auth":"dXNlcjpwYXNz"}}}`
	_, err := cs.CoreV1().Secrets("workshops").Create(context.Background(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "regcred", Namespace: "workshops"},
		Type:       corev1.SecretTypeDockerConfigJson,
		Data:       map[string][]byte{corev1.DockerConfigJsonKey: []byte(dockerConfig)},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	spec := devcontainerSpec()
	spec.Devcontainer.RegistryAuthSecret = "regcred"

	ws, err := b.EnsureWorkspace(context.Background(), spec)
	require.NoError(t, err)

	got := envOf(ideContainer(t, cs, ws.ID))["ENVBUILDER_DOCKER_CONFIG_BASE64"]
	assert.Equal(t, base64.StdEncoding.EncodeToString([]byte(dockerConfig)), got)
}

func TestEnsureWorkspace_DevcontainerRegistryAuthErrors(t *testing.T) {
	tests := []struct {
		name       string
		secret     *corev1.Secret
		secretName string
		wantInErr  string
	}{
		{
			name:       "missing secret",
			secretName: "absent",
			wantInErr:  "failed to read registry auth secret",
		},
		{
			name:       "secret without a docker config key",
			secretName: "regcred",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "regcred", Namespace: "workshops"},
				Data:       map[string][]byte{"username": []byte("alice")},
			},
			wantInErr: "has no \".dockerconfigjson\" key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, cs := newTestBackend()
			if tt.secret != nil {
				_, err := cs.CoreV1().Secrets("workshops").Create(context.Background(), tt.secret, metav1.CreateOptions{})
				require.NoError(t, err)
			}

			spec := devcontainerSpec()
			spec.Devcontainer.RegistryAuthSecret = tt.secretName

			_, err := b.EnsureWorkspace(context.Background(), spec)
			require.Error(t, err, "a broken registry secret must fail loudly, not build uncached")
			assert.Contains(t, err.Error(), tt.wantInErr)
		})
	}
}

// TestEnsureWorkspace_DevcontainerAnonymousCache covers a public cache registry:
// no secret is a valid configuration, not an error.
func TestEnsureWorkspace_DevcontainerAnonymousCache(t *testing.T) {
	b, cs := newTestBackend()

	ws, err := b.EnsureWorkspace(context.Background(), devcontainerSpec())
	require.NoError(t, err)

	_, ok := envOf(ideContainer(t, cs, ws.ID))["ENVBUILDER_DOCKER_CONFIG_BASE64"]
	assert.False(t, ok, "no auth secret should mean no docker config env")
}

func TestEnsureWorkspace_DevcontainerOptionalEnv(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*workspace.DevcontainerSpec)
		env     string
		want    string
		present bool
	}{
		{
			name:    "fallback image",
			mutate:  func(d *workspace.DevcontainerSpec) { d.FallbackImage = "gitpod/openvscode-server:latest" },
			env:     "ENVBUILDER_FALLBACK_IMAGE",
			want:    "gitpod/openvscode-server:latest",
			present: true,
		},
		{
			name:    "insecure",
			mutate:  func(d *workspace.DevcontainerSpec) { d.Insecure = true },
			env:     "ENVBUILDER_INSECURE",
			want:    "true",
			present: true,
		},
		{
			name:    "insecure off is omitted",
			mutate:  func(d *workspace.DevcontainerSpec) { d.Insecure = false },
			env:     "ENVBUILDER_INSECURE",
			present: false,
		},
		{
			name:    "dir defaults to the builder's own",
			mutate:  func(d *workspace.DevcontainerSpec) { d.Dir = "" },
			env:     "ENVBUILDER_DEVCONTAINER_DIR",
			present: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, cs := newTestBackend()

			spec := devcontainerSpec()
			tt.mutate(spec.Devcontainer)

			ws, err := b.EnsureWorkspace(context.Background(), spec)
			require.NoError(t, err)

			got, ok := envOf(ideContainer(t, cs, ws.ID))[tt.env]
			assert.Equal(t, tt.present, ok)
			if tt.present {
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

func TestGitURLWithRef(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		repo     string
		branch   string
		expected string
	}{
		{
			name:     "branch becomes a ref fragment",
			repo:     "https://gitlab.com/org/repo.git",
			branch:   "main",
			expected: "https://gitlab.com/org/repo.git#refs/heads/main",
		},
		{
			name:     "no branch leaves the remote HEAD to decide",
			repo:     "https://gitlab.com/org/repo.git",
			expected: "https://gitlab.com/org/repo.git",
		},
		{
			name:     "branch with a slash",
			repo:     "https://gitlab.com/org/repo.git",
			branch:   "feat/step-1",
			expected: "https://gitlab.com/org/repo.git#refs/heads/feat/step-1",
		},
		{
			name:     "whitespace is trimmed",
			repo:     "  https://gitlab.com/org/repo.git  ",
			branch:   "  main  ",
			expected: "https://gitlab.com/org/repo.git#refs/heads/main",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, gitURLWithRef(tt.repo, tt.branch))
		})
	}
}

// A devcontainer build relocates the whole filesystem and cannot remount the
// projected service-account token, so the pod must opt out of it. Plain
// workspaces keep the cluster default.
func TestEnsureWorkspace_DevcontainerDropsServiceAccountToken(t *testing.T) {
	b, cs := newTestBackend()

	ws, err := b.EnsureWorkspace(context.Background(), devcontainerSpec())
	require.NoError(t, err)
	dep, err := cs.AppsV1().Deployments("workshops").Get(context.Background(), ws.ID, metav1.GetOptions{})
	require.NoError(t, err)
	require.NotNil(t, dep.Spec.Template.Spec.AutomountServiceAccountToken,
		"a devcontainer pod must explicitly opt out of the service-account token")
	assert.False(t, *dep.Spec.Template.Spec.AutomountServiceAccountToken)

	ws2, err := b.EnsureWorkspace(context.Background(), plainGitSpec())
	require.NoError(t, err)
	dep2, err := cs.AppsV1().Deployments("workshops").Get(context.Background(), ws2.ID, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Nil(t, dep2.Spec.Template.Spec.AutomountServiceAccountToken,
		"a plain workspace must keep the cluster default")
}
