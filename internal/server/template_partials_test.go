package server

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The create-lab wizard's Step 6 and the "Add Template" drawer render the same
// partial (web/partials/template-editor.html). These tests are what stops the two
// drifting again: a field added to the editor shows up on both surfaces, or
// neither.
//
// They run against the real files through getTemplate, so a partial that is not
// wired into ParseFiles fails here rather than at the first page load.

// renderBothSurfaces renders the editor as each surface asks for it. Going
// through getTemplate is deliberate: a partial missing from its ParseFiles list
// fails here rather than at the first page load.
//
// The two defines are executed directly rather than the whole page, so these
// tests stay about the editor and do not need a page's view model fabricated.
// TestTemplateEditorIsWiredIntoBothPages covers the pages actually calling them.
func renderBothSurfaces(t *testing.T) (wizard, drawer string) {
	t.Helper()
	// getTemplate resolves paths relative to the process working directory, which
	// for a package test is the package directory.
	t.Chdir("../..")

	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

	render := func(page, define string) string {
		tmpl, err := h.getTemplate(page)
		require.NoError(t, err, "%s must parse with its partials", page)
		var buf bytes.Buffer
		require.NoError(t, tmpl.ExecuteTemplate(&buf, define, nil))
		return buf.String()
	}

	return render("admin.html", "template-editor-wizard"), render("lab-detail.html", "template-editor-drawer")
}

// The pages have to actually invoke the partial, or the parity above proves
// nothing about what an admin sees.
func TestTemplateEditorIsWiredIntoBothPages(t *testing.T) {
	t.Chdir("../..")

	for page, define := range map[string]string{
		"web/admin.html":      "template-editor-wizard",
		"web/lab-detail.html": "template-editor-drawer",
	} {
		body, err := os.ReadFile(page)
		require.NoError(t, err)
		assert.Contains(t, string(body), `{{template "`+define+`"`, "%s must render the shared editor", page)
	}
}

// Every field parseWorkspaceTemplatesFromForm reads that the form builder is meant
// to expose. Anything added to the parser and to the wizard but forgotten in the
// drawer used to slip through — that is exactly the bug this file exists for.
var formBuilderFields = []string{
	"template_0_name",
	"template_0_description",
	"template_0_git_repo",
	"template_0_git_branch",
	"template_0_git_folder",
	"template_0_git_auth_secret",
	"template_0_image",
	"template_0_cpu",
	"template_0_cpu_limit",
	"template_0_memory",
	"template_0_memory_limit",
	"template_0_disk_size",
	"template_0_ephemeral",
	"template_0_storage_class",
	"template_0_startup_script",
	"template_0_dotfiles_repo",
	"template_0_extensions",
}

// The devcontainer importer's controls, by element id.
var devcontainerControls = []string{
	"devcontainer_template_name",
	"devcontainer_template_description",
	"devcontainer_git_repo",
	"devcontainer_git_branch",
	"devcontainer_file",
	"devcontainer_config_repo",
	"devcontainer_config_branch",
	"devcontainer_config_auth_secret",
	"devcontainer_dir",
	"devcontainer_cache_repo",
	"devcontainer_use_in_cluster_cache",
	"devcontainer_cpu",
	"devcontainer_cpu_limit",
	"devcontainer_memory",
	"devcontainer_memory_limit",
	"devcontainer_git_auth_secret",
	"devcontainer_registry_auth_secret",
}

// The containers the repeating rows (env, sidecars, mounts, node selectors) are
// appended into. Without these the "+ Add ..." buttons have nowhere to put a row.
var repeatingRowContainers = []string{
	"template-variables-container",
	"template-sidecars-container",
	"template-mounts-container",
	"template-nodeselector-container",
}

func TestTemplateEditorFieldsOnBothSurfaces(t *testing.T) {
	wizard, drawer := renderBothSurfaces(t)

	surfaces := []struct {
		name string
		html string
	}{
		{"wizard step 6", wizard},
		{"add template drawer", drawer},
	}

	for _, s := range surfaces {
		t.Run(s.name, func(t *testing.T) {
			for _, field := range formBuilderFields {
				assert.Contains(t, s.html, `name="`+field+`"`, "form builder field %q missing", field)
			}
			for _, id := range devcontainerControls {
				assert.Contains(t, s.html, `id="`+id+`"`, "devcontainer control %q missing", id)
			}
			for _, class := range repeatingRowContainers {
				assert.Contains(t, s.html, class, "repeating-row container %q missing", class)
			}
			// The three modes and the hidden input the server reads.
			for _, id := range []string{"templates-mode-form", "templates-mode-devcontainer", "templates-mode-yaml"} {
				assert.Contains(t, s.html, `id="`+id+`"`, "mode button %q missing", id)
			}
			assert.Contains(t, s.html, `name="templates_mode"`)
			assert.Contains(t, s.html, `name="templates_yaml"`)
		})
	}
}

// The two surfaces are the same editor, but not the same page: the wizard defines
// credentials that do not exist yet and can hold several templates, while the
// drawer picks from a lab's existing credentials and appends one template.
func TestTemplateEditorSurfaceDifferences(t *testing.T) {
	wizard, drawer := renderBothSurfaces(t)

	t.Run("credential rows are wizard-only", func(t *testing.T) {
		assert.Contains(t, wizard, `name="secret_name"`)
		assert.Contains(t, wizard, `id="credential-row-tmpl"`)
		// The lab detail page has its own Credentials panel writing straight to the
		// cluster; a second, in-drawer credential form would write nowhere.
		assert.NotContains(t, drawer, `id="credential-row-tmpl"`)
		assert.NotContains(t, drawer, `id="wizard-credentials-container"`)
	})

	t.Run("multi-template controls are wizard-only", func(t *testing.T) {
		assert.Contains(t, wizard, `id="btn-add-template"`)
		assert.Contains(t, wizard, `id="template-row-tmpl"`)
		assert.Contains(t, wizard, `name="template_count"`)
		assert.NotContains(t, drawer, `id="btn-add-template"`)
		assert.NotContains(t, drawer, `id="template-row-tmpl"`)
	})

	t.Run("both carry exactly one template row", func(t *testing.T) {
		assert.Equal(t, 1, strings.Count(drawer, `name="template_0_name"`))
		// The wizard has two: the live first row, and the hidden clone source.
		assert.Equal(t, 2, strings.Count(wizard, `name="template_0_name"`))
	})
}
