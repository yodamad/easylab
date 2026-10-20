package kube

import (
	"context"
	"strings"
	"testing"

	"easylab/internal/providers/workspace"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// openChamberImage is what a lab must name for itself: OpenChamber publishes no
// official image, so the profile has no default (see TestProfileFor).
const openChamberImage = "registry.example.com/easylab/openchamber:1.11"

func openChamberSpec(owner string) workspace.Spec {
	return workspace.Spec{
		LabID:  "job-1",
		Owner:  owner,
		IDE:    workspace.IDEOpenChamber,
		Image:  openChamberImage,
		Domain: "lab.example.com",
		Token:  "tok123",
	}
}

// Env assertions below go through envOf (kube_devcontainer_test.go), which
// indexes by name and so reports the last value for a duplicated one — the same
// one the kubelet gives the container.

func TestProfileFor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		ide      string
		expected string
	}{
		{name: "empty is the default", ide: "", expected: workspace.IDECodeServer},
		{name: "code-server", ide: workspace.IDECodeServer, expected: workspace.IDECodeServer},
		{name: "the retired openvscode value", ide: workspace.IDEOpenVSCode, expected: workspace.IDECodeServer},
		{name: "an unknown kind falls back rather than failing", ide: "emacs", expected: workspace.IDECodeServer},
		{name: "openchamber", ide: workspace.IDEOpenChamber, expected: workspace.IDEOpenChamber},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, profileFor(tt.ide).kind)
		})
	}
}

// TestProfileInvariants is the structural contract every IDE profile must hold,
// so a third IDE added later cannot half-work: it needs an auth variable, a
// port, a home its workspace directory lives under, and either a relocatable
// bundle or an admission that it does not support devcontainer builds.
func TestProfileInvariants(t *testing.T) {
	t.Parallel()

	for _, p := range []ideProfile{codeServerProfile, openChamberProfile} {
		t.Run(p.kind, func(t *testing.T) {
			t.Parallel()
			assert.NotEmpty(t, p.kind)
			assert.NotEmpty(t, p.authEnv, "the IDE needs a variable to read its password from")
			assert.NotZero(t, p.port)
			assert.NotEmpty(t, p.homeDir)
			assert.True(t, strings.HasPrefix(p.workspaceDir, p.homeDir+"/"),
				"workspaceDir %q must live under homeDir %q, which is what the volume is mounted at", p.workspaceDir, p.homeDir)
			if p.supportsDevcontainer {
				assert.NotEmpty(t, p.bundleRoot, "devcontainer mode copies bundleRoot onto the /ide volume")
				assert.NotEmpty(t, p.bundleBin, "devcontainer mode starts bundleBin from the /ide volume")
			}
		})
	}
}

// TestProfileForSpec_HomeOverride covers the consequence of OpenChamber having
// no official image: the home is a property of whichever image the lab brings,
// so a template may name it. The project directory follows the home — the clone
// has to land inside the volume, wherever that is mounted.
func TestProfileForSpec_HomeOverride(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		ide         string
		homeDir     string
		expectHome  string
		expectWSDir string
	}{
		{
			name:        "openchamber defaults to upstream's home",
			ide:         workspace.IDEOpenChamber,
			expectHome:  "/home/openchamber",
			expectWSDir: "/home/openchamber/project",
		},
		{
			name:        "an image on the node base keeps its own home",
			ide:         workspace.IDEOpenChamber,
			homeDir:     "/home/node",
			expectHome:  "/home/node",
			expectWSDir: "/home/node/project",
		},
		{
			name:        "a trailing slash does not produce a doubled path",
			ide:         workspace.IDEOpenChamber,
			homeDir:     "/home/node/",
			expectHome:  "/home/node",
			expectWSDir: "/home/node/project",
		},
		{
			name:        "code-server ignores it: its image, and so its home, is ours",
			ide:         workspace.IDECodeServer,
			homeDir:     "/home/node",
			expectHome:  "/home/coder",
			expectWSDir: "/home/coder/project",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := profileForSpec(workspace.Spec{IDE: tt.ide, HomeDir: tt.homeDir})
			assert.Equal(t, tt.expectHome, p.homeDir)
			assert.Equal(t, tt.expectWSDir, p.workspaceDir)
		})
	}
}

// TestEnsureWorkspace_OpenChamberHomeOverride is the same override seen from the
// pod: a home_dir that reached the volume mount but not the clone (or the other
// way round) would put the student's repo outside the persistent volume, which
// is exactly the silent data loss the field exists to prevent.
func TestEnsureWorkspace_OpenChamberHomeOverride(t *testing.T) {
	t.Parallel()

	b, cs := newTestBackend()
	spec := openChamberSpec("alice")
	spec.HomeDir = "/home/node"
	spec.GitRepo = "https://example.com/workshop.git"
	ws, err := b.EnsureWorkspace(context.Background(), spec)
	require.NoError(t, err)

	c := ideContainer(t, cs, ws.ID)
	var mounted bool
	for _, m := range c.VolumeMounts {
		if m.Name == workspaceVolumeName && m.MountPath == "/home/node" {
			mounted = true
		}
	}
	assert.True(t, mounted, "the volume must cover the image's real home, got %v", c.VolumeMounts)

	dep, err := cs.AppsV1().Deployments("workshops").Get(context.Background(), ws.ID, metav1.GetOptions{})
	require.NoError(t, err)
	var seeded, cloned string
	for _, ic := range dep.Spec.Template.Spec.InitContainers {
		script := strings.Join(ic.Command, " ") + strings.Join(ic.Args, " ")
		switch ic.Name {
		case "git-clone":
			cloned = script
		case "seed-home":
			seeded = script
		}
	}
	assert.Contains(t, cloned, "/home/node/project")
	assert.NotContains(t, cloned, "/home/openchamber")
	require.NotEmpty(t, seeded, "the home still has to be seeded from the image")
	assert.Contains(t, seeded, "/home/node")
}

func TestDefaultImageForIDE(t *testing.T) {
	t.Parallel()

	assert.Equal(t, DefaultImage, DefaultImageForIDE(workspace.IDECodeServer))
	assert.Equal(t, DefaultImage, DefaultImageForIDE(""))
	assert.Empty(t, DefaultImageForIDE(workspace.IDEOpenChamber),
		"OpenChamber publishes no official image, so the lab must name one")
}

func TestEnsureWorkspace_OpenChamber(t *testing.T) {
	t.Parallel()

	b, cs := newTestBackend()
	ws, err := b.EnsureWorkspace(context.Background(), openChamberSpec("alice"))
	require.NoError(t, err)

	assert.Equal(t, workspace.IDEOpenChamber, ws.IDE, "the workspace must report the IDE it actually runs")
	assert.Equal(t, ws.URL, ws.OpenURL)

	c := ideContainer(t, cs, ws.ID)
	assert.Equal(t, openChamberImage, c.Image)
	require.Len(t, c.Ports, 1)
	assert.Equal(t, int32(3000), c.Ports[0].ContainerPort)
	assert.Empty(t, c.Args, "OpenChamber takes no start flags: it runs under the image's entrypoint")
	assert.Empty(t, c.Command)

	env := envOf(c)
	assert.Equal(t, "tok123", env["OPENCHAMBER_UI_PASSWORD"])
	assert.NotContains(t, env, "PASSWORD", "PASSWORD is code-server's variable and means nothing here")
	// Without this the CLI binds 127.0.0.1, the kubelet's TCP readiness probe
	// never succeeds, and the student waits on a pod that will never be Ready.
	assert.Equal(t, "0.0.0.0", env["OPENCHAMBER_HOST"])
	assert.NotEmpty(t, env["OPENCODE_JWT_SECRET"], "a stable signing secret keeps the student signed in across a pause/resume")
	assert.NotEqual(t, ws.Token, env["OPENCODE_JWT_SECRET"], "the session signing secret must not be the password handed to the student")

	// The readiness probe follows the profile's port, so the pod only goes Ready
	// once OpenChamber is actually listening.
	require.NotNil(t, c.ReadinessProbe)
	require.NotNil(t, c.ReadinessProbe.TCPSocket)
	assert.Equal(t, 3000, c.ReadinessProbe.TCPSocket.Port.IntValue())

	svc, err := cs.CoreV1().Services("workshops").Get(context.Background(), ws.ID, metav1.GetOptions{})
	require.NoError(t, err)
	require.Len(t, svc.Spec.Ports, 1)
	assert.Equal(t, int32(80), svc.Spec.Ports[0].Port, "the Service port is the same for every IDE")
	assert.Equal(t, 3000, svc.Spec.Ports[0].TargetPort.IntValue())
}

// TestEnsureWorkspace_OpenChamberSessionSecret pins that the secret the server
// needs to mint a session reaches the Workspace, both straight from
// EnsureWorkspace and read back from the stored Deployment by GetWorkspace.
func TestEnsureWorkspace_OpenChamberSessionSecret(t *testing.T) {
	t.Parallel()

	b, cs := newTestBackend()
	ws, err := b.EnsureWorkspace(context.Background(), openChamberSpec("alice"))
	require.NoError(t, err)

	want := envOf(ideContainer(t, cs, ws.ID))[openChamberSessionSecretEnv]
	require.NotEmpty(t, want)
	assert.Equal(t, want, ws.SessionSecret)
	assert.NotEqual(t, ws.Token, ws.SessionSecret, "the session secret must not be the student's password")

	got, err := b.GetWorkspace(context.Background(), ws.ID)
	require.NoError(t, err)
	assert.Equal(t, ws.SessionSecret, got.SessionSecret)
	assert.Equal(t, ws.Token, got.Token)
}

func TestEnsureWorkspace_CodeServerHasNoSessionSecret(t *testing.T) {
	t.Parallel()

	b, _ := newTestBackend()
	spec := openChamberSpec("alice")
	spec.IDE = workspace.IDECodeServer
	spec.Image = ""
	ws, err := b.EnsureWorkspace(context.Background(), spec)
	require.NoError(t, err)
	assert.Empty(t, ws.SessionSecret)

	got, err := b.GetWorkspace(context.Background(), ws.ID)
	require.NoError(t, err)
	assert.Empty(t, got.SessionSecret)
}

func TestSessionSecretFromDeployment(t *testing.T) {
	t.Parallel()

	dep := func(containers ...corev1.Container) *appsv1.Deployment {
		d := &appsv1.Deployment{}
		d.Spec.Template.Spec.Containers = containers
		return d
	}
	env := func(kv ...string) []corev1.EnvVar {
		var out []corev1.EnvVar
		for i := 0; i+1 < len(kv); i += 2 {
			out = append(out, corev1.EnvVar{Name: kv[i], Value: kv[i+1]})
		}
		return out
	}
	tests := []struct {
		name     string
		dep      *appsv1.Deployment
		expected string
	}{
		{"no containers", dep(), ""},
		{"no secret env", dep(corev1.Container{Name: workspaceContainerName, Env: env("A", "b")}), ""},
		{"secret on the workspace container", dep(corev1.Container{Name: workspaceContainerName, Env: env(openChamberSessionSecretEnv, "s1")}), "s1"},
		{"last duplicate wins", dep(corev1.Container{Name: workspaceContainerName, Env: env(openChamberSessionSecretEnv, "s1", openChamberSessionSecretEnv, "s2")}), "s2"},
		{"other containers are ignored", dep(corev1.Container{Name: "sidecar", Env: env(openChamberSessionSecretEnv, "nope")}), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, sessionSecretFromDeployment(tt.dep))
		})
	}
}

func TestEnsureWorkspace_OpenChamberSecretsArePerWorkspace(t *testing.T) {
	t.Parallel()

	b, cs := newTestBackend()
	first, err := b.EnsureWorkspace(context.Background(), openChamberSpec("alice"))
	require.NoError(t, err)
	second, err := b.EnsureWorkspace(context.Background(), openChamberSpec("bob"))
	require.NoError(t, err)

	a := envOf(ideContainer(t, cs, first.ID))["OPENCODE_JWT_SECRET"]
	bb := envOf(ideContainer(t, cs, second.ID))["OPENCODE_JWT_SECRET"]
	require.NotEmpty(t, a)
	assert.NotEqual(t, a, bb, "one student's session secret must not sign another's tokens")
}

// TestEnsureWorkspace_OpenChamberTemplateEnvOverrides pins the ordering in
// createDeployment: a template may override the IDE's own environment, but not
// the password the student was handed.
func TestEnsureWorkspace_OpenChamberTemplateEnvOverrides(t *testing.T) {
	t.Parallel()

	b, cs := newTestBackend()
	spec := openChamberSpec("alice")
	spec.Env = map[string]string{
		"OPENCHAMBER_HOST":        "::",
		"OPENCHAMBER_UI_PASSWORD": "not-the-students-password",
		"ANTHROPIC_API_KEY":       "sk-test",
	}
	ws, err := b.EnsureWorkspace(context.Background(), spec)
	require.NoError(t, err)

	env := envOf(ideContainer(t, cs, ws.ID))
	assert.Equal(t, "::", env["OPENCHAMBER_HOST"], "a template may override the IDE's environment")
	assert.Equal(t, "sk-test", env["ANTHROPIC_API_KEY"])
	assert.Equal(t, "tok123", env["OPENCHAMBER_UI_PASSWORD"], "the access secret is applied last and wins")
}

func TestEnsureWorkspace_OpenChamberVolumeLayout(t *testing.T) {
	t.Parallel()

	b, cs := newTestBackend()
	spec := openChamberSpec("alice")
	spec.GitRepo = "https://example.com/workshop.git"
	ws, err := b.EnsureWorkspace(context.Background(), spec)
	require.NoError(t, err)

	dep, err := cs.AppsV1().Deployments("workshops").Get(context.Background(), ws.ID, metav1.GetOptions{})
	require.NoError(t, err)

	// The whole home is persisted: OpenChamber keeps its own settings, the
	// opencode CLI and the student's projects there.
	c := ideContainer(t, cs, ws.ID)
	var mounted bool
	for _, m := range c.VolumeMounts {
		if m.Name == workspaceVolumeName && m.MountPath == "/home/openchamber" {
			mounted = true
		}
	}
	assert.True(t, mounted, "the volume must cover the whole home, got %v", c.VolumeMounts)

	// The clone lands directly under the home, so the directory it is in is
	// owned by the student rather than by the root init container that made it.
	var cloneScript string
	for _, ic := range dep.Spec.Template.Spec.InitContainers {
		if ic.Name == "git-clone" {
			cloneScript = strings.Join(ic.Command, " ") + strings.Join(ic.Args, " ")
		}
	}
	assert.Contains(t, cloneScript, "/home/openchamber/project")
}

// TestHomeSeedInit_VersionedMarker covers the trap OpenChamber brings: its own
// install lives inside the home the volume mounts over, so a seed marker that
// ignores the image would pin every existing student to the install their home
// was first seeded with.
func TestHomeSeedInit_VersionedMarker(t *testing.T) {
	t.Parallel()

	plain := homeSeedInit("img:1", "/home/coder", false)
	assert.Contains(t, strings.Join(plain.Command, " "), ".easylab-home-seeded")

	first := homeSeedInit("img:1", "/home/openchamber", true)
	same := homeSeedInit("img:1", "/home/openchamber", true)
	bumped := homeSeedInit("img:2", "/home/openchamber", true)

	assert.Equal(t, first.Command, same.Command, "the same image must not re-seed a returning student's home")
	assert.NotEqual(t, first.Command, bumped.Command, "a new image must re-seed, or the student keeps the old install")
}

func TestEnsureWorkspace_OpenChamberRefusals(t *testing.T) {
	t.Parallel()

	t.Run("no image and no default", func(t *testing.T) {
		t.Parallel()
		b, _ := newTestBackend()
		spec := openChamberSpec("alice")
		spec.Image = ""

		_, err := b.EnsureWorkspace(context.Background(), spec)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "image")
	})

	t.Run("devcontainer mode", func(t *testing.T) {
		t.Parallel()
		b, _ := newTestBackend()
		spec := openChamberSpec("alice")
		spec.GitRepo = "https://example.com/workshop.git"
		spec.Devcontainer = &workspace.DevcontainerSpec{CacheRepo: "registry.example.com/cache"}

		_, err := b.EnsureWorkspace(context.Background(), spec)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "devcontainer")
	})
}

// TestSetupSteps_OpenChamberSkipsVSCodeSteps is the backstop for a lab saved
// before validation rejected these keys: the bootstrap must not run code-server
// flags against a binary that has none.
func TestSetupSteps_OpenChamberSkipsVSCodeSteps(t *testing.T) {
	t.Parallel()

	spec := workspace.Spec{
		StartupScript:  "echo hello",
		Extensions:     []string{"golang.go"},
		VSCodeSettings: map[string]any{"editor.fontSize": 14},
	}

	got := setupSteps(spec, openChamberProfile)

	assert.Contains(t, got, "echo hello", "a startup script still runs")
	assert.NotContains(t, got, "--install-extension")
	assert.NotContains(t, got, "settings.json")

	// Same template on code-server keeps doing all three.
	cs := setupSteps(spec, codeServerProfile)
	assert.Contains(t, cs, "--install-extension")
	assert.Contains(t, cs, "settings.json")
}

// TestEnsureWorkspace_OpenChamberWrappedBootstrap covers a template with a
// startup script: the setup lines and the IDE start share one shell, which is
// what lets a script export an API key the agent then inherits.
func TestEnsureWorkspace_OpenChamberWrappedBootstrap(t *testing.T) {
	t.Parallel()

	b, cs := newTestBackend()
	spec := openChamberSpec("alice")
	spec.StartupScript = `export ANTHROPIC_API_KEY="$(cat /etc/easylab/ai/key)"`
	ws, err := b.EnsureWorkspace(context.Background(), spec)
	require.NoError(t, err)

	c := ideContainer(t, cs, ws.ID)
	require.Len(t, c.Command, 3)
	assert.Equal(t, []string{"/bin/bash", "-lc"}, c.Command[:2])
	assert.Contains(t, c.Command[2], "ANTHROPIC_API_KEY")
	assert.Contains(t, c.Command[2], "exec openchamber")
}
