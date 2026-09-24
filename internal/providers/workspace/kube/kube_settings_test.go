package kube

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
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
