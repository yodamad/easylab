package server

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Each starting point fills the wizard form, and whatever the form holds is then
// put through validateWorkspaceTemplates on submit. A preset that cannot survive
// that would hand a beginner a lab the server refuses — the one thing the feature
// must never do — so every one of them is held to the same checks here.
func TestWorkspacePresets_AreValidTemplates(t *testing.T) {
	t.Parallel()

	for _, preset := range workspacePresets() {
		t.Run(preset.ID, func(t *testing.T) {
			t.Parallel()
			require.NotEmpty(t, preset.Title, "a card with no title is unclickable")
			require.NotEmpty(t, preset.Description)
			require.NotEmpty(t, preset.Icon)
			require.NotEmpty(t, preset.Template.Name, "students pick a template by name")

			require.NoError(t, validateWorkspaceTemplates([]WorkspaceTemplate{preset.Template}))
		})
	}
}

// The cards carry their template as JSON and template-editor.js reads it back, so
// the round trip has to survive the same marshalling the YAML editor uses too.
func TestWorkspacePresets_RoundTripThroughYAML(t *testing.T) {
	t.Parallel()

	for _, preset := range workspacePresets() {
		t.Run(preset.ID, func(t *testing.T) {
			t.Parallel()

			rendered, err := marshalWorkspaceTemplatesYAML([]WorkspaceTemplate{preset.Template})
			require.NoError(t, err)

			parsed, err := parseWorkspaceTemplatesYAML(rendered)
			require.NoError(t, err)
			require.Len(t, parsed, 1)
			assert.Equal(t, preset.Template, parsed[0])
		})
	}
}

func TestWorkspacePresets_TemplateJSON(t *testing.T) {
	t.Parallel()

	for _, preset := range workspacePresets() {
		t.Run(preset.ID, func(t *testing.T) {
			t.Parallel()

			var decoded WorkspaceTemplate
			require.NoError(t, json.Unmarshal([]byte(preset.TemplateJSON()), &decoded))
			assert.Equal(t, preset.Template, decoded)
		})
	}
}

// IDs address a card in the DOM and in the docs; two cards sharing one would make
// the second unreachable.
func TestWorkspacePresets_IDsAreUnique(t *testing.T) {
	t.Parallel()

	seen := map[string]bool{}
	for _, preset := range workspacePresets() {
		require.NotEmpty(t, preset.ID)
		assert.False(t, seen[preset.ID], "duplicate preset id %q", preset.ID)
		seen[preset.ID] = true
	}
}
