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

func TestNotifyExtensionStep(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		spec     workspace.Spec
		profile  ideProfile
		wantStep bool
	}{
		{
			name:     "nowhere to poll",
			spec:     workspace.Spec{},
			profile:  codeServerProfile,
			wantStep: false,
		},
		{
			name:     "a blank URL is nowhere too",
			spec:     workspace.Spec{NotifyURL: "   ", NotifyToken: "t"},
			profile:  codeServerProfile,
			wantStep: false,
		},
		{
			name:     "code-server gets the extension",
			spec:     workspace.Spec{NotifyURL: "https://lab.example.com/api/workspace/notifications?lab_id=job-1", NotifyToken: "t"},
			profile:  codeServerProfile,
			wantStep: true,
		},
		{
			name: "an IDE without extensions is left alone",
			spec: workspace.Spec{NotifyURL: "https://lab.example.com/api/workspace/notifications?lab_id=job-1", NotifyToken: "t"},
			// The notification channel is a VS Code extension, so an IDE with no
			// notion of extensions skips it rather than writing files it ignores.
			profile:  ideProfile{kind: "other", supportsExtensions: false},
			wantStep: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			step := notifyExtensionStep(tt.spec, tt.profile)
			if !tt.wantStep {
				assert.Empty(t, step)
				return
			}
			assert.Contains(t, step, notifyExtensionDir)
			assert.Contains(t, step, "package.json")
			assert.Contains(t, step, "extension.js")
			// Best-effort like every other bootstrap step: a workspace must start
			// even when this cannot be written.
			assert.Contains(t, step, "|| true")
		})
	}
}

// What matters is the files bash actually writes, so run the generated step: the
// manifest has to be valid JSON and the extension source has to survive
// shell-quoting with its own quotes and $ intact.
func TestNotifyExtensionStep_RunsInShell(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}

	spec := workspace.Spec{
		NotifyURL:   "https://lab.example.com/api/workspace/notifications?lab_id=job-1",
		NotifyToken: "alice@example.com.abc123",
	}
	step := notifyExtensionStep(spec, codeServerProfile)
	require.NotEmpty(t, step)

	home := t.TempDir()
	cmd := exec.Command("bash", "-c", step)
	cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))

	dir := filepath.Join(home, ".local/share/code-server/extensions", notifyExtensionDir)

	manifest, err := os.ReadFile(filepath.Join(dir, "package.json"))
	require.NoError(t, err)
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(manifest, &parsed), string(manifest))
	assert.Equal(t, "./extension.js", parsed["main"])
	assert.Equal(t, notifyExtensionDir, parsed["name"])

	// The IDE's cache of this directory is cleared, so it scans the folder again
	// and finds an extension that was dropped in behind its back.
	_, err = os.Stat(filepath.Join(home, ".local/share/code-server/extensions/extensions.json"))
	assert.True(t, os.IsNotExist(err), "the extensions cache must be gone")

	source, err := os.ReadFile(filepath.Join(dir, "extension.js"))
	require.NoError(t, err)
	// The configuration is read from the environment, never baked into the file:
	// a restarted pod picks up a new URL or token without rewriting it.
	assert.Contains(t, string(source), "EASYLAB_NOTIFY_URL")
	assert.Contains(t, string(source), "EASYLAB_NOTIFY_TOKEN")
	assert.NotContains(t, string(source), "alice@example.com.abc123")
	assert.Contains(t, string(source), "module.exports")
	// Quotes and metacharacters came through the shell unscathed.
	assert.Contains(t, string(source), "'warning'")
	assert.Contains(t, string(source), "process.env")

	// Written again on every start, since the files are EasyLab's rather than the
	// student's — unlike settings.json, which is left alone once it exists.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "extension.js"), []byte("stale"), 0o644))
	cmd = exec.Command("bash", "-c", step)
	cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	source, err = os.ReadFile(filepath.Join(dir, "extension.js"))
	require.NoError(t, err)
	assert.Contains(t, string(source), "module.exports")
}

// The URL and token reach the container as environment variables, and only when
// there is somewhere to poll.
func TestEnsureWorkspace_NotifyEnvironment(t *testing.T) {
	b, cs := newTestBackend()
	ws, err := b.EnsureWorkspace(context.Background(), workspace.Spec{
		LabID: "job-1", Owner: "alice", Domain: "d", Token: "t",
		NotifyURL:   "https://lab.example.com/api/workspace/notifications?lab_id=job-1",
		NotifyToken: "alice@example.com.abc123",
	})
	require.NoError(t, err)

	c := ideContainer(t, cs, ws.ID)
	env := map[string]string{}
	for _, e := range c.Env {
		env[e.Name] = e.Value
	}
	assert.Equal(t, "https://lab.example.com/api/workspace/notifications?lab_id=job-1", env[envNotifyURL])
	assert.Equal(t, "alice@example.com.abc123", env[envNotifyToken])

	// The extension is installed by the same bootstrap, so the start command is
	// wrapped even though the template asked for no setup of its own.
	require.Len(t, c.Command, 3)
	assert.Contains(t, c.Command[2], notifyExtensionDir)
	assert.Contains(t, c.Command[2], "exec ")
}

func TestEnsureWorkspace_NoNotifyEnvironmentWithoutURL(t *testing.T) {
	b, cs := newTestBackend()
	ws, err := b.EnsureWorkspace(context.Background(), workspace.Spec{
		LabID: "job-1", Owner: "bob", Domain: "d", Token: "t",
	})
	require.NoError(t, err)

	c := ideContainer(t, cs, ws.ID)
	for _, e := range c.Env {
		assert.NotEqual(t, envNotifyURL, e.Name)
		assert.NotEqual(t, envNotifyToken, e.Name)
	}
	// Nothing to set up, so the IDE runs under the image's own entrypoint as before.
	assert.Empty(t, c.Command)
}
