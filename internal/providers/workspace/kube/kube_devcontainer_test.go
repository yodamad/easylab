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
	assert.Equal(t, int32(8080), c.Ports[0].ContainerPort)
	assert.Equal(t, workspace.IDECodeServer, ws.IDE)
	assert.NotContains(t, ws.OpenURL, "tkn=", "code-server authenticates on its own login page")
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
	assert.Equal(t, "/home/coder/project", env["ENVBUILDER_WORKSPACE_FOLDER"])

	// The build wipes the filesystem; the IDE and the student's files must survive.
	assert.Contains(t, env["ENVBUILDER_IGNORE_PATHS"], ideMountPath)
	assert.Contains(t, env["ENVBUILDER_IGNORE_PATHS"], "/home/coder/project")

	// PASSWORD is what --auth password reads, and it is appended to the container's
	// env before the devcontainer branch rewrites the container. That ordering is
	// the only thing carrying it into envbuilder's init script: envbuilder's own
	// UnsetEnv strips ENVBUILDER_* names but leaves this one. If a refactor moved
	// the append after applyDevcontainer, or assigned c.Env instead of appending,
	// every student would be locked out with no other test failing.
	assert.Equal(t, "tok123", env["PASSWORD"])

	// The init script starts the injected IDE, not one from the image. Args are
	// shell-quoted because the script is handed to a shell, not exec'd directly.
	assert.Contains(t, env["ENVBUILDER_INIT_SCRIPT"], "exec "+ideMountPath+"/bin/code-server")
	assert.Contains(t, env["ENVBUILDER_INIT_SCRIPT"], "'--bind-addr' '0.0.0.0:8080'")
	assert.Contains(t, env["ENVBUILDER_INIT_SCRIPT"], "'--auth' 'password'")
}

func TestEnsureWorkspace_DevcontainerInjectsIDE(t *testing.T) {
	b, cs := newTestBackend()

	ws, err := b.EnsureWorkspace(context.Background(), devcontainerSpec())
	require.NoError(t, err)

	inject, ok := initContainerNamed(t, cs, ws.ID, "ide-inject")
	require.True(t, ok, "devcontainer mode must inject an IDE: the built image has none")
	assert.Contains(t, inject.Image, "code-server")

	script := inject.Command[len(inject.Command)-1]
	assert.Contains(t, script, `cp -dR "/usr/lib/code-server/."`, "code-server's bundle lives here")
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

// TestEnsureWorkspace_DevcontainerConfigRepoClonesSeparately covers a
// devcontainer.json read from a repo separate from the content repo: a second
// init container must clone it onto its own volume, alongside (not instead of)
// envbuilder's own content-repo clone.
func TestEnsureWorkspace_DevcontainerConfigRepoClonesSeparately(t *testing.T) {
	b, cs := newTestBackend()

	spec := devcontainerSpec()
	spec.Devcontainer.ConfigRepo = "https://gitlab.com/org/devcontainer-config.git"
	spec.Devcontainer.ConfigBranch = "main"

	ws, err := b.EnsureWorkspace(context.Background(), spec)
	require.NoError(t, err)

	c, ok := initContainerNamed(t, cs, ws.ID, "devcontainer-config-clone")
	require.True(t, ok, "a separate config repo must get its own clone init container")
	assert.Contains(t, c.Command[len(c.Command)-1], "https://gitlab.com/org/devcontainer-config.git")
	assert.Contains(t, c.Command[len(c.Command)-1], "--branch 'main'")
	require.Len(t, c.VolumeMounts, 1)
	assert.Equal(t, devcontainerConfigVolumeName, c.VolumeMounts[0].Name)
	assert.Equal(t, devcontainerConfigMountPath, c.VolumeMounts[0].MountPath)

	// envbuilder still owns the content-repo clone.
	_, ok = initContainerNamed(t, cs, ws.ID, "git-clone")
	assert.False(t, ok, "envbuilder clones the content repo itself, config_repo does not change that")

	ideC := ideContainer(t, cs, ws.ID)
	var mounted bool
	for _, m := range ideC.VolumeMounts {
		if m.Name == devcontainerConfigVolumeName && m.MountPath == devcontainerConfigMountPath {
			mounted = true
		}
	}
	assert.True(t, mounted, "envbuilder must be able to read the cloned config at build time")
}

// TestEnsureWorkspace_DevcontainerConfigRepoEnv covers what points envbuilder at
// the separately-cloned config instead of a path inside the content clone.
func TestEnsureWorkspace_DevcontainerConfigRepoEnv(t *testing.T) {
	tests := []struct {
		name    string
		dir     string
		wantDir string
	}{
		{name: "explicit dir", dir: "custom/.devcontainer", wantDir: devcontainerConfigMountPath + "/custom/.devcontainer"},
		{name: "empty dir defaults to .devcontainer", dir: "", wantDir: devcontainerConfigMountPath + "/.devcontainer"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, cs := newTestBackend()

			spec := devcontainerSpec()
			spec.Devcontainer.Dir = tt.dir
			spec.Devcontainer.ConfigRepo = "https://gitlab.com/org/devcontainer-config.git"

			ws, err := b.EnsureWorkspace(context.Background(), spec)
			require.NoError(t, err)
			env := envOf(ideContainer(t, cs, ws.ID))

			assert.Equal(t, tt.wantDir, env["ENVBUILDER_DEVCONTAINER_DIR"])
			// The content repo is still what envbuilder clones — config_repo only
			// changes where devcontainer.json comes from.
			assert.Equal(t, "https://gitlab.com/org/workshop.git#refs/heads/main", env["ENVBUILDER_GIT_URL"])
			assert.Contains(t, env["ENVBUILDER_IGNORE_PATHS"], devcontainerConfigMountPath, "the build must not wipe the config clone")
		})
	}
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
	assert.Contains(t, c.Image, "code-server")
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
	// Extensions install via the injected bundle, not a binary from the image —
	// the built image has no code-server on PATH for a bare name to resolve to.
	assert.Contains(t, script, ideMountPath+"/bin/code-server --install-extension 'golang.go'")
	// code-server takes the folder to open as a positional argument.
	assert.Contains(t, script, "'/home/coder/project/exercises'")
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
			mutate:  func(d *workspace.DevcontainerSpec) { d.FallbackImage = "codercom/code-server:latest" },
			env:     "ENVBUILDER_FALLBACK_IMAGE",
			want:    "codercom/code-server:latest",
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

// TestEnsureWorkspace_DevcontainerLegacyIDEValue pins that a lab saved before
// OpenVSCode support was removed still builds a working code-server workspace
// rather than resolving to a profile that no longer exists.
func TestEnsureWorkspace_DevcontainerLegacyIDEValue(t *testing.T) {
	b, cs := newTestBackend()

	spec := devcontainerSpec()
	spec.IDE = workspace.IDEOpenVSCode

	ws, err := b.EnsureWorkspace(context.Background(), spec)
	require.NoError(t, err)
	c := ideContainer(t, cs, ws.ID)

	assert.Equal(t, int32(8080), c.Ports[0].ContainerPort)
	assert.Equal(t, workspace.IDECodeServer, ws.IDE, "the retired value must not leak back out")

	script := envOf(c)["ENVBUILDER_INIT_SCRIPT"]
	assert.Contains(t, script, "exec "+ideMountPath+"/bin/code-server")
	assert.NotContains(t, script, "openvscode")
}

func TestDevcontainerProfile(t *testing.T) {
	t.Parallel()

	t.Run("retargets the launcher at the injected bundle", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, "/ide/bin/code-server", devcontainerProfile(codeServerProfile).serverBin)
	})

	// Structural invariants the profile must hold, so a second IDE added later with
	// a mismatched bundle fails here rather than as a CrashLoopBackOff mid-workshop.
	t.Run("the profile carries a usable bundle", func(t *testing.T) {
		t.Parallel()
		p := codeServerProfile
		assert.NotEmpty(t, p.bundleRoot, "bundleRoot names the tree copied onto the /ide volume")
		assert.NotEmpty(t, p.bundleBin, "bundleBin names the launcher inside that tree")
		assert.False(t, strings.HasPrefix(p.bundleBin, "/"),
			"bundleBin must be relative to bundleRoot: the launcher resolves its own root from its path, so an absolute path would look outside the copied tree")
		assert.True(t, strings.HasPrefix(devcontainerProfile(p).serverBin, ideMountPath+"/"),
			"in devcontainer mode the launcher must live on the injected volume, which is all the build leaves alone")
	})
}

func TestIdeInjectInit_Script(t *testing.T) {
	t.Parallel()

	c := ideInjectInit(codeServerProfile)
	assert.Equal(t,
		`cp -dR "/usr/lib/code-server/." /ide/ && chmod -R a+rX /ide`,
		c.Command[len(c.Command)-1])
}

// prebuiltDevcontainerSpec is a workable baked-workspace spec; tests tweak a copy.
// Unlike devcontainerSpec, CacheRepo/Dir are irrelevant here — there is no build.
func prebuiltDevcontainerSpec() workspace.Spec {
	spec := devcontainerSpec()
	spec.Devcontainer = &workspace.DevcontainerSpec{
		PrebuiltImage: "easylab-registry-cache.lab.example.com/baked/job-1/go-workshop:latest",
	}
	return spec
}

func TestEnsureWorkspace_PrebuiltImageSkipsEnvbuilder(t *testing.T) {
	b, cs := newTestBackend()

	ws, err := b.EnsureWorkspace(context.Background(), prebuiltDevcontainerSpec())
	require.NoError(t, err)

	c := ideContainer(t, cs, ws.ID)
	assert.Equal(t, "easylab-registry-cache.lab.example.com/baked/job-1/go-workshop:latest", c.Image)
	for name := range envOf(c) {
		assert.NotContains(t, name, "ENVBUILDER", "a baked workspace has no envbuilder to configure")
	}

	// No build here, so no reason to force root the way envbuilder needs — run as
	// whatever the image's own default user is, same as the plain image: path.
	assert.Nil(t, c.SecurityContext)

	require.Len(t, c.Command, 3)
	assert.Equal(t, []string{"/bin/bash", "-lc"}, c.Command[:2])
}

func TestEnsureWorkspace_PrebuiltImageInjectsIDE(t *testing.T) {
	b, cs := newTestBackend()

	ws, err := b.EnsureWorkspace(context.Background(), prebuiltDevcontainerSpec())
	require.NoError(t, err)

	_, ok := initContainerNamed(t, cs, ws.ID, "ide-inject")
	assert.True(t, ok, "a baked image has no IDE, so it needs injection just like a live devcontainer build")

	c := ideContainer(t, cs, ws.ID)
	var mounted bool
	for _, m := range c.VolumeMounts {
		if m.Name == ideVolumeName && m.MountPath == ideMountPath {
			mounted = true
		}
	}
	assert.True(t, mounted)
}

func TestEnsureWorkspace_PrebuiltImageClonesGitRepo(t *testing.T) {
	b, cs := newTestBackend()

	ws, err := b.EnsureWorkspace(context.Background(), prebuiltDevcontainerSpec())
	require.NoError(t, err)

	_, ok := initContainerNamed(t, cs, ws.ID, "git-clone")
	assert.True(t, ok, "a baked image has no envbuilder to clone the repo itself, unlike a live devcontainer build")
}

func TestEnsureWorkspace_PrebuiltImageSkipsConfigRepoClone(t *testing.T) {
	b, cs := newTestBackend()

	spec := prebuiltDevcontainerSpec()
	spec.Devcontainer.ConfigRepo = "https://gitlab.com/org/devcontainer-config.git"

	ws, err := b.EnsureWorkspace(context.Background(), spec)
	require.NoError(t, err)

	_, ok := initContainerNamed(t, cs, ws.ID, "devcontainer-config-clone")
	assert.False(t, ok, "nothing reads a separately-cloned devcontainer config once envbuilder is bypassed")
}

func TestEnsureWorkspace_PrebuiltImageDoesNotForceDevcontainerResourceDefaults(t *testing.T) {
	b, cs := newTestBackend()

	ws, err := b.EnsureWorkspace(context.Background(), prebuiltDevcontainerSpec())
	require.NoError(t, err)

	c := ideContainer(t, cs, ws.ID)
	assert.Empty(t, c.Resources.Requests, "a baked workspace is an ordinary pull + IDE start, the plain-path cost profile, not envbuilder's extraction cost")
}

func TestEnsureWorkspace_PrebuiltImageRemoteUserWrapsCommand(t *testing.T) {
	b, cs := newTestBackend()

	spec := prebuiltDevcontainerSpec()
	spec.Devcontainer.RemoteUser = "vscode"

	ws, err := b.EnsureWorkspace(context.Background(), spec)
	require.NoError(t, err)

	c := ideContainer(t, cs, ws.ID)
	script := c.Command[len(c.Command)-1]
	assert.Contains(t, script, "su -s /bin/bash 'vscode' -c", "envbuilder normally drops to the devcontainer's remoteUser itself; bypassing it must replicate that")
}

func TestEnsureWorkspace_PrebuiltImageNoRemoteUserRunsPlainScript(t *testing.T) {
	b, cs := newTestBackend()

	ws, err := b.EnsureWorkspace(context.Background(), prebuiltDevcontainerSpec())
	require.NoError(t, err)

	c := ideContainer(t, cs, ws.ID)
	script := c.Command[len(c.Command)-1]
	assert.NotContains(t, script, "su -s", "no declared remoteUser means run as the image's own default user")
}
