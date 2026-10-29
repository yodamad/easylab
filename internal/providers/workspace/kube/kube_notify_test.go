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
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
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

func TestSetEnvValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		env         []corev1.EnvVar
		value       string
		wantChanged bool
		wantEnv     []corev1.EnvVar
	}{
		{
			name:        "sets a missing variable",
			env:         []corev1.EnvVar{{Name: "OTHER", Value: "x"}},
			value:       "v",
			wantChanged: true,
			wantEnv:     []corev1.EnvVar{{Name: "OTHER", Value: "x"}, {Name: "K", Value: "v"}},
		},
		{
			name:        "sets on a nil slice",
			env:         nil,
			value:       "v",
			wantChanged: true,
			wantEnv:     []corev1.EnvVar{{Name: "K", Value: "v"}},
		},
		{
			name:        "updates a differing value",
			env:         []corev1.EnvVar{{Name: "K", Value: "old"}, {Name: "OTHER", Value: "x"}},
			value:       "v",
			wantChanged: true,
			wantEnv:     []corev1.EnvVar{{Name: "K", Value: "v"}, {Name: "OTHER", Value: "x"}},
		},
		{
			name:        "identical value reports no change",
			env:         []corev1.EnvVar{{Name: "K", Value: "v"}},
			value:       "v",
			wantChanged: false,
			wantEnv:     []corev1.EnvVar{{Name: "K", Value: "v"}},
		},
		{
			name:        "duplicates collapse to the last entry",
			env:         []corev1.EnvVar{{Name: "K", Value: "first"}, {Name: "K", Value: "second"}},
			value:       "v",
			wantChanged: true,
			// The kubelet applies the last duplicate, so that is the one kept and
			// updated; the earlier one is left as the template wrote it.
			wantEnv: []corev1.EnvVar{{Name: "K", Value: "first"}, {Name: "K", Value: "v"}},
		},
		{
			name:        "duplicates whose last entry already matches are unchanged",
			env:         []corev1.EnvVar{{Name: "K", Value: "first"}, {Name: "K", Value: "v"}},
			value:       "v",
			wantChanged: false,
			wantEnv:     []corev1.EnvVar{{Name: "K", Value: "first"}, {Name: "K", Value: "v"}},
		},
		{
			name: "valueFrom is replaced by a literal",
			env: []corev1.EnvVar{{Name: "K", ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{Key: "k"},
			}}},
			value:       "",
			wantChanged: true,
			wantEnv:     []corev1.EnvVar{{Name: "K", Value: ""}},
		},
		{
			name: "valueFrom is replaced even when a literal differs",
			env: []corev1.EnvVar{{Name: "K", ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"},
			}}},
			value:       "v",
			wantChanged: true,
			wantEnv:     []corev1.EnvVar{{Name: "K", Value: "v"}},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := append([]corev1.EnvVar(nil), tt.env...)
			changed := setEnvValue(&env, "K", tt.value)
			assert.Equal(t, tt.wantChanged, changed)
			assert.Equal(t, tt.wantEnv, env)
		})
	}
}

// seedDeployment stores a workspace Deployment with the given containers.
func seedDeployment(t *testing.T, cs *fake.Clientset, name string, containers ...corev1.Container) {
	t.Helper()
	_, err := cs.AppsV1().Deployments("workshops").Create(context.Background(), &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "workshops"},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: containers}},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
}

func containerEnvByName(t *testing.T, cs *fake.Clientset, name, container string) map[string]string {
	t.Helper()
	dep, err := cs.AppsV1().Deployments("workshops").Get(context.Background(), name, metav1.GetOptions{})
	require.NoError(t, err)
	for _, c := range dep.Spec.Template.Spec.Containers {
		if c.Name == container {
			out := map[string]string{}
			for _, e := range c.Env {
				out[e.Name] = e.Value
			}
			return out
		}
	}
	t.Fatalf("container %q not found", container)
	return nil
}

func deploymentUpdates(cs *fake.Clientset) int {
	n := 0
	for _, a := range cs.Actions() {
		if a.Matches("update", "deployments") {
			n++
		}
	}
	return n
}

func TestReconcileNotifyEnv(t *testing.T) {
	t.Parallel()

	const url = "https://lab.example.com/api/workspace/notifications?lab_id=job-1"

	tests := []struct {
		name        string
		containers  []corev1.Container
		spec        workspace.Spec
		wantUpdates int
		wantIDE     map[string]string
		wantSidecar map[string]string
	}{
		{
			name:        "adds both variables when there are none",
			containers:  []corev1.Container{{Name: workspaceContainerName}},
			spec:        workspace.Spec{NotifyURL: url, NotifyToken: "tok"},
			wantUpdates: 1,
			wantIDE:     map[string]string{envNotifyURL: url, envNotifyToken: "tok"},
		},
		{
			name:        "empty URL is a no-op",
			containers:  []corev1.Container{{Name: workspaceContainerName}},
			spec:        workspace.Spec{NotifyToken: "tok"},
			wantUpdates: 0,
			wantIDE:     map[string]string{},
		},
		{
			name:        "blank URL is a no-op",
			containers:  []corev1.Container{{Name: workspaceContainerName}},
			spec:        workspace.Spec{NotifyURL: "  ", NotifyToken: "tok"},
			wantUpdates: 0,
			wantIDE:     map[string]string{},
		},
		{
			name: "empty URL never strips existing variables",
			containers: []corev1.Container{{Name: workspaceContainerName, Env: []corev1.EnvVar{
				{Name: envNotifyURL, Value: url}, {Name: envNotifyToken, Value: "tok"},
			}}},
			spec:        workspace.Spec{},
			wantUpdates: 0,
			wantIDE:     map[string]string{envNotifyURL: url, envNotifyToken: "tok"},
		},
		{
			name: "matching values issue no update",
			containers: []corev1.Container{{Name: workspaceContainerName, Env: []corev1.EnvVar{
				{Name: envNotifyURL, Value: url}, {Name: envNotifyToken, Value: "tok"},
			}}},
			spec:        workspace.Spec{NotifyURL: url, NotifyToken: "tok"},
			wantUpdates: 0,
			wantIDE:     map[string]string{envNotifyURL: url, envNotifyToken: "tok"},
		},
		{
			name: "a stale token is replaced in one update",
			containers: []corev1.Container{{Name: workspaceContainerName, Env: []corev1.EnvVar{
				{Name: envNotifyURL, Value: url}, {Name: envNotifyToken, Value: "old"},
			}}},
			spec:        workspace.Spec{NotifyURL: url, NotifyToken: "new"},
			wantUpdates: 1,
			wantIDE:     map[string]string{envNotifyURL: url, envNotifyToken: "new"},
		},
		{
			name: "only the workspace container is touched",
			containers: []corev1.Container{
				{Name: "sidecar", Env: []corev1.EnvVar{{Name: "KEEP", Value: "me"}}},
				{Name: workspaceContainerName},
			},
			spec:        workspace.Spec{NotifyURL: url, NotifyToken: "tok"},
			wantUpdates: 1,
			wantIDE:     map[string]string{envNotifyURL: url, envNotifyToken: "tok"},
			wantSidecar: map[string]string{"KEEP": "me"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			b, cs := newTestBackend()
			seedDeployment(t, cs, "ws-1", tt.containers...)
			cs.ClearActions()

			require.NoError(t, b.reconcileNotifyEnv(context.Background(), "ws-1", tt.spec))

			assert.Equal(t, tt.wantUpdates, deploymentUpdates(cs))
			assert.Equal(t, tt.wantIDE, containerEnvByName(t, cs, "ws-1", workspaceContainerName))
			if tt.wantSidecar != nil {
				assert.Equal(t, tt.wantSidecar, containerEnvByName(t, cs, "ws-1", "sidecar"))
			}
		})
	}
}

func TestReconcileNotifyEnv_MissingDeployment(t *testing.T) {
	t.Parallel()

	b, cs := newTestBackend()
	err := b.reconcileNotifyEnv(context.Background(), "absent", workspace.Spec{NotifyURL: "https://lab.example.com", NotifyToken: "t"})
	require.NoError(t, err)
	assert.Zero(t, deploymentUpdates(cs))
}

// A second pass with the same spec converges: one restart, then nothing.
func TestReconcileNotifyEnv_Converges(t *testing.T) {
	t.Parallel()

	b, cs := newTestBackend()
	seedDeployment(t, cs, "ws-1", corev1.Container{Name: workspaceContainerName})
	spec := workspace.Spec{NotifyURL: "https://lab.example.com/x", NotifyToken: "t"}

	require.NoError(t, b.reconcileNotifyEnv(context.Background(), "ws-1", spec))
	require.NoError(t, b.reconcileNotifyEnv(context.Background(), "ws-1", spec))
	assert.Equal(t, 1, deploymentUpdates(cs))
}

// ReconcileNotify is the exported entry point the background pass uses; it must
// satisfy the optional interface and reach the same reconcile.
func TestBackend_ReconcileNotify(t *testing.T) {
	t.Parallel()

	var _ workspace.NotifyReconciler = (*Backend)(nil)

	spec := workspace.Spec{NotifyURL: "https://lab.example.com/x", NotifyToken: "t"}

	t.Run("sets both variables on a deployment without them", func(t *testing.T) {
		t.Parallel()
		b, cs := newTestBackend()
		seedDeployment(t, cs, "ws-1", corev1.Container{Name: workspaceContainerName})
		cs.ClearActions()

		require.NoError(t, b.ReconcileNotify(context.Background(), "ws-1", spec))

		assert.Equal(t, 1, deploymentUpdates(cs))
		assert.Equal(t,
			map[string]string{envNotifyURL: spec.NotifyURL, envNotifyToken: spec.NotifyToken},
			containerEnvByName(t, cs, "ws-1", workspaceContainerName))
	})

	t.Run("issues no update when already matching", func(t *testing.T) {
		t.Parallel()
		b, cs := newTestBackend()
		seedDeployment(t, cs, "ws-1", corev1.Container{Name: workspaceContainerName, Env: []corev1.EnvVar{
			{Name: envNotifyURL, Value: spec.NotifyURL}, {Name: envNotifyToken, Value: spec.NotifyToken},
		}})
		cs.ClearActions()

		require.NoError(t, b.ReconcileNotify(context.Background(), "ws-1", spec))
		assert.Zero(t, deploymentUpdates(cs))
	})
}

// notifyExtensionSource lives in a Go raw string literal, which a backtick ends.
func TestNotifyExtensionSource_NoBacktick(t *testing.T) {
	t.Parallel()
	assert.NotContains(t, notifyExtensionSource, "`",
		"notifyExtensionSource is a Go raw string literal: a backtick inside it would silently truncate the extension. Use quotes or concatenation instead of template literals.")
}

func notifyManifestCommands(t *testing.T) []map[string]any {
	t.Helper()
	contributes, ok := notifyExtensionManifest()["contributes"].(map[string]any)
	require.True(t, ok, "manifest must have contributes")
	commands, ok := contributes["commands"].([]map[string]any)
	require.True(t, ok, "contributes.commands must be a list")
	require.NotEmpty(t, commands)
	return commands
}

func TestNotifyExtensionManifest(t *testing.T) {
	t.Parallel()

	t.Run("contributes the board command", func(t *testing.T) {
		t.Parallel()
		cmd := notifyManifestCommands(t)[0]
		assert.Equal(t, "easylab.showMessages", cmd["command"])
		assert.Equal(t, "Show workshop messages", cmd["title"])
		assert.Equal(t, "EasyLab", cmd["category"])
	})

	t.Run("round-trips through JSON", func(t *testing.T) {
		t.Parallel()
		raw, err := json.Marshal(notifyExtensionManifest())
		require.NoError(t, err)
		var parsed map[string]any
		require.NoError(t, json.Unmarshal(raw, &parsed))
		assert.Equal(t, "./extension.js", parsed["main"])
		contributes, ok := parsed["contributes"].(map[string]any)
		require.True(t, ok)
		commands, ok := contributes["commands"].([]any)
		require.True(t, ok)
		require.Len(t, commands, 1)
		assert.Equal(t, "easylab.showMessages", commands[0].(map[string]any)["command"])
	})

	t.Run("source registers the id the manifest contributes", func(t *testing.T) {
		t.Parallel()
		id, ok := notifyManifestCommands(t)[0]["command"].(string)
		require.True(t, ok)
		require.NotEmpty(t, id)
		assert.Contains(t, notifyExtensionSource, id,
			"the manifest and the extension source disagree on the command id: the palette entry and the status bar click would break with no build error")
	})
}

func TestNotifyExtensionSource_Guards(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		want string
	}{
		{"reads the URL from the environment", envNotifyURL},
		{"reads the token from the environment", envNotifyToken},
		{"webview CSP denies by default", "default-src 'none'"},
		{"webview CSP allows only the nonce'd script", "script-src 'nonce-"},
		{"script tag carries the nonce", `<script nonce="`},
		{"board panel type", "easylabMessages"},
		{"board does not take focus", "preserveFocus: true"},
		{"status bar sits on the left", "StatusBarAlignment.Left"},
		{"board text is set via textContent", "textContent = item.message"},
		{"exports the entry points", "module.exports = { activate, deactivate }"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Contains(t, notifyExtensionSource, tt.want)
		})
	}

	t.Run("message text is never interpolated into the board HTML", func(t *testing.T) {
		t.Parallel()
		assert.NotContains(t, notifyExtensionSource, "innerHTML")
		assert.NotContains(t, notifyExtensionSource, "new vscode.MarkdownString")
	})
}

func TestNotifyExtensionStep_WritesBothFiles(t *testing.T) {
	t.Parallel()

	spec := workspace.Spec{NotifyURL: "https://lab.example.com/n", NotifyToken: "t"}
	step := notifyExtensionStep(spec, codeServerProfile)
	require.NotEmpty(t, step)
	assert.Contains(t, step, `> "$d/package.json"`)
	assert.Contains(t, step, `> "$d/extension.js"`)
	assert.Contains(t, step, notifyExtensionDir)
	assert.Contains(t, step, "easylab.showMessages", "the manifest's contributed command must be in the emitted bootstrap")

	assert.Empty(t, notifyExtensionStep(workspace.Spec{}, codeServerProfile))
	assert.Empty(t, notifyExtensionStep(spec, ideProfile{kind: "other", supportsExtensions: false}))
}
