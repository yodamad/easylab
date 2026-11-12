package kube

import (
	"context"
	"testing"

	"easylab/internal/providers/workspace"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
)

func TestEnsureWorkspace_TemplateEnvironment(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		spec     workspace.Spec
		expected string
		present  bool
	}{
		{"notify set with template", workspace.Spec{NotifyURL: "https://lab.example.com/n", NotifyToken: "t", Template: "go"}, "go", true},
		{"notify set without template", workspace.Spec{NotifyURL: "https://lab.example.com/n", NotifyToken: "t"}, "", false},
		{"template without notify", workspace.Spec{Template: "go"}, "", false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			b, cs := newTestBackend()
			spec := tt.spec
			spec.LabID, spec.Owner, spec.Domain, spec.Token = "job-1", "alice", "d", "t"
			ws, err := b.EnsureWorkspace(context.Background(), spec)
			require.NoError(t, err)

			env := map[string]string{}
			for _, e := range ideContainer(t, cs, ws.ID).Env {
				env[e.Name] = e.Value
			}
			v, ok := env[envTemplate]
			assert.Equal(t, tt.present, ok)
			assert.Equal(t, tt.expected, v)
		})
	}
}

// The retrofit must neither add nor strip the template variable: either would
// restart running workspaces mid-workshop.
func TestReconcileNotifyEnv_LeavesTemplateAlone(t *testing.T) {
	t.Parallel()

	spec := workspace.Spec{NotifyURL: "https://lab.example.com/x", NotifyToken: "t", Template: "go"}
	tests := []struct {
		name     string
		env      []corev1.EnvVar
		expected map[string]string
		updates  int
	}{
		{"not added to an older workspace", nil,
			map[string]string{envNotifyURL: spec.NotifyURL, envNotifyToken: "t"}, 1},
		{"kept and not drift when already converged",
			[]corev1.EnvVar{{Name: envNotifyURL, Value: spec.NotifyURL}, {Name: envNotifyToken, Value: "t"}, {Name: envTemplate, Value: "other"}},
			map[string]string{envNotifyURL: spec.NotifyURL, envNotifyToken: "t", envTemplate: "other"}, 0},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			b, cs := newTestBackend()
			seedDeployment(t, cs, "ws-1", corev1.Container{Name: workspaceContainerName, Env: tt.env})
			cs.ClearActions()
			require.NoError(t, b.reconcileNotifyEnv(context.Background(), "ws-1", spec))
			assert.Equal(t, tt.updates, deploymentUpdates(cs))
			assert.Equal(t, tt.expected, containerEnvByName(t, cs, "ws-1", workspaceContainerName))
		})
	}
}

func TestNotifyExtensionSource_Slides(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		want string
	}{
		{"poll URL carries the template", "'&template=' + encodeURIComponent(process.env.EASYLAB_TEMPLATE)"},
		{"slides status bar item", "'$(book) Slides'"},
		{"slides tooltip names the host", "'Open the workshop slides ('"},
		{"slides command registered", "registerCommand(SLIDES_COMMAND, openSlides)"},
		{"slides opened externally", "vscode.env.openExternal(vscode.Uri.parse(slidesLink))"},
		{"only http(s) links are shown", "u.protocol === 'http:' || u.protocol === 'https:'"},
		{"no-link message", "No slides are linked to this workspace."},
		{"first sighting is remembered", "globalState.update(SLIDES_SEEN_KEY, link)"},
		{"first sighting is announced", "Your workshop slides are available."},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Contains(t, notifyExtensionSource, tt.want)
		})
	}
}
