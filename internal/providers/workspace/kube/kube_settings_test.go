package kube

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"easylab/internal/providers/workspace"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUserSettingsStep(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		settings map[string]any
		wantLine bool
	}{
		{name: "nil settings", settings: nil, wantLine: false},
		{name: "empty settings", settings: map[string]any{}, wantLine: false},
		{name: "settings", settings: map[string]any{"editor.fontSize": 14}, wantLine: true},
		{name: "unencodable settings are skipped", settings: map[string]any{"bad": func() {}}, wantLine: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			line := userSettingsStep(tt.settings)
			if tt.wantLine {
				assert.Contains(t, line, "code-server/User")
				assert.Contains(t, line, "settings.json")
			} else {
				assert.Empty(t, line)
			}
		})
	}
}

// TestUserSettingsStep_RunsInShell executes the generated line, since what
// matters is the file bash actually writes: valid JSON, quotes and shell
// metacharacters intact, and a student's existing file left alone.
func TestUserSettingsStep_RunsInShell(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}

	settings := map[string]any{
		"chat.disableAIFeatures":           true,
		"security.workspace.trust.enabled": false,
		"terminal.integrated.env.linux":    map[string]any{"PS1": `it's $HOME "quoted" \n`},
		"files.exclude":                    map[string]any{"**/.git": true},
	}
	line := userSettingsStep(settings)
	require.NotEmpty(t, line)

	run := func(home string) {
		cmd := exec.Command("bash", "-c", line)
		cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}

	t.Run("writes valid JSON on first start", func(t *testing.T) {
		home := t.TempDir()
		run(home)

		content, err := os.ReadFile(filepath.Join(home, ".local/share/code-server/User/settings.json"))
		require.NoError(t, err)
		var got map[string]any
		require.NoError(t, json.Unmarshal(content, &got), string(content))
		assert.Equal(t, map[string]any{
			"chat.disableAIFeatures":           true,
			"security.workspace.trust.enabled": false,
			"terminal.integrated.env.linux":    map[string]any{"PS1": `it's $HOME "quoted" \n`},
			"files.exclude":                    map[string]any{"**/.git": true},
		}, got)
	})

	t.Run("keeps an existing settings file", func(t *testing.T) {
		home := t.TempDir()
		dir := filepath.Join(home, ".local/share/code-server/User")
		require.NoError(t, os.MkdirAll(dir, 0o755))
		existing := []byte(`{"editor.fontSize": 20}`)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "settings.json"), existing, 0o644))

		run(home)

		content, err := os.ReadFile(filepath.Join(dir, "settings.json"))
		require.NoError(t, err)
		assert.Equal(t, existing, content)
	})
}

func TestEnsureWorkspace_VSCodeSettingsWrapCommand(t *testing.T) {
	b, cs := newTestBackend()
	ws, err := b.EnsureWorkspace(context.Background(), workspace.Spec{
		LabID: "job-1", Owner: "frank", Domain: "d", Token: "t",
		VSCodeSettings: map[string]any{"chat.disableAIFeatures": true},
	})
	require.NoError(t, err)

	c := ideContainer(t, cs, ws.ID)
	require.Len(t, c.Command, 3, "settings alone must wrap the IDE start")
	assert.Contains(t, c.Command[2], "code-server/User")
	assert.Contains(t, c.Command[2], `"chat.disableAIFeatures": true`)
	assert.Contains(t, c.Command[2], "exec ")
}

// TestEnsureWorkspace_PrebuiltImageWritesVSCodeSettings covers a baked
// devcontainer: the bake itself runs no setup steps, so the settings must be
// written when the baked image starts — inside the su that drops to remoteUser,
// so they land in that user's home rather than root's.
func TestEnsureWorkspace_PrebuiltImageWritesVSCodeSettings(t *testing.T) {
	b, cs := newTestBackend()

	spec := prebuiltDevcontainerSpec()
	spec.Devcontainer.RemoteUser = "vscode"
	spec.VSCodeSettings = map[string]any{"chat.disableAIFeatures": true, "note": "it's quoted"}
	ws, err := b.EnsureWorkspace(context.Background(), spec)
	require.NoError(t, err)

	c := ideContainer(t, cs, ws.ID)
	require.Len(t, c.Command, 3)
	script := c.Command[2]
	assert.Contains(t, script, "exec su -s /bin/bash 'vscode' -c ")
	assert.Contains(t, script, "code-server/User")
	assert.Contains(t, script, "chat.disableAIFeatures")

	// The settings are quoted inside the su -c argument, itself quoted: check
	// the doubly-quoted result is still a well-formed shell script.
	if _, err := exec.LookPath("bash"); err == nil {
		out, err := exec.Command("bash", "-n", "-c", script).CombinedOutput()
		require.NoError(t, err, string(out))
	}
}

// TestEnsureWorkspace_DevcontainerPersistsIDEState pins the volume layout that
// keeps a student's IDE settings in devcontainer mode: the home directory is not
// persisted there, so the volume is split between the project folder and a
// directory code-server's data is moved onto.
func TestEnsureWorkspace_DevcontainerPersistsIDEState(t *testing.T) {
	tests := []struct {
		name string
		spec workspace.Spec
	}{
		{name: "envbuilder build", spec: devcontainerSpec()},
		{name: "baked image", spec: prebuiltDevcontainerSpec()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			b, cs := newTestBackend()
			ws, err := b.EnsureWorkspace(context.Background(), tt.spec)
			require.NoError(t, err)

			c := ideContainer(t, cs, ws.ID)
			subPaths := map[string]string{}
			for _, m := range c.VolumeMounts {
				if m.Name == workspaceVolumeName {
					subPaths[m.MountPath] = m.SubPath
				}
			}
			assert.Equal(t, map[string]string{
				codeServerProfile.workspaceDir: workspaceProjectSubPath,
				ideStateMountPath:              workspaceIDEStateSubPath,
			}, subPaths)

			dirs, ok := initContainerNamed(t, cs, ws.ID, "workspace-dirs")
			require.True(t, ok, "the kubelet would create the sub-directories root-owned and unwritable")
			require.Len(t, dirs.VolumeMounts, 1)
			assert.Equal(t, workspaceRootMountPath, dirs.VolumeMounts[0].MountPath)
			assert.Empty(t, dirs.VolumeMounts[0].SubPath)

			// Whichever way the init script reaches the container, it must redirect
			// the IDE's data directory before anything is written to it.
			script := envOf(c)["ENVBUILDER_INIT_SCRIPT"]
			if script == "" {
				script = c.Command[len(c.Command)-1]
			}
			assert.Contains(t, script, ideStateStep())
			if ignore, ok := envOf(c)["ENVBUILDER_IGNORE_PATHS"]; ok {
				assert.Contains(t, ignore, ideStateMountPath, "the build must not wipe the mounted IDE state")
			}
		})
	}
}

// TestEnsureWorkspace_IDEStateMountOnlyForPersistentDevcontainer covers the two
// neighbours of the case above: a plain workspace persists its whole home
// instead, and an Ephemeral devcontainer has no volume to keep anything on.
func TestEnsureWorkspace_IDEStateMountOnlyForPersistentDevcontainer(t *testing.T) {
	ephemeral := devcontainerSpec()
	ephemeral.DiskSize = ""
	ephemeral.Ephemeral = true

	tests := []struct {
		name string
		spec workspace.Spec
	}{
		{name: "plain workspace", spec: workspace.Spec{LabID: "job-1", Owner: "frank", Domain: "d", Token: "t"}},
		{name: "ephemeral devcontainer", spec: ephemeral},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			b, cs := newTestBackend()
			ws, err := b.EnsureWorkspace(context.Background(), tt.spec)
			require.NoError(t, err)

			for _, m := range ideContainer(t, cs, ws.ID).VolumeMounts {
				assert.NotEqual(t, ideStateMountPath, m.MountPath)
				assert.Empty(t, m.SubPath)
			}
			_, ok := initContainerNamed(t, cs, ws.ID, "workspace-dirs")
			assert.False(t, ok)
		})
	}
}

// TestIDEStateStep_RunsInShell executes the generated line against a stand-in for
// the mounted volume, since what matters is where code-server's data ends up.
func TestIDEStateStep_RunsInShell(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}

	run := func(t *testing.T, home, state string) {
		t.Helper()
		line := strings.Replace(ideStateStep(), "s="+ideStateMountPath+";", "s="+state+";", 1)
		require.Contains(t, line, state)
		cmd := exec.Command("bash", "-c", line+userSettingsStep(map[string]any{"editor.fontSize": 14}))
		cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	dataDir := func(home string) string { return filepath.Join(home, ".local/share/code-server") }

	t.Run("settings land on the volume", func(t *testing.T) {
		home, state := t.TempDir(), t.TempDir()
		run(t, home, state)

		target, err := os.Readlink(dataDir(home))
		require.NoError(t, err, "the data directory must be a link onto the volume")
		assert.Equal(t, state, target)
		assert.FileExists(t, filepath.Join(state, "User/settings.json"))
	})

	t.Run("a fresh container keeps the student's settings", func(t *testing.T) {
		state := t.TempDir()
		run(t, t.TempDir(), state)
		mine := []byte(`{"editor.fontSize": 20}`)
		require.NoError(t, os.WriteFile(filepath.Join(state, "User/settings.json"), mine, 0o644))

		// A restart: a new container filesystem, the same volume.
		home := t.TempDir()
		run(t, home, state)

		content, err := os.ReadFile(filepath.Join(dataDir(home), "User/settings.json"))
		require.NoError(t, err)
		assert.Equal(t, mine, content)
	})

	t.Run("data the image ships is carried over", func(t *testing.T) {
		home, state := t.TempDir(), t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(dataDir(home), "extensions/some.ext"), 0o755))
		run(t, home, state)

		assert.DirExists(t, filepath.Join(state, "extensions/some.ext"))
		_, err := os.Readlink(dataDir(home))
		assert.NoError(t, err)
	})

	t.Run("no volume leaves the home directory in use", func(t *testing.T) {
		home := t.TempDir()
		run(t, home, filepath.Join(t.TempDir(), "not-mounted"))

		info, err := os.Lstat(dataDir(home))
		require.NoError(t, err)
		assert.True(t, info.IsDir(), "without a volume the data directory stays a real directory")
		assert.FileExists(t, filepath.Join(dataDir(home), "User/settings.json"))
	})
}
