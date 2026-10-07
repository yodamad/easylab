package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"easylab/internal/providers/workspace"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bakeableTemplate is a bakeable template: devcontainer mode with an external
// cache registry, which needs no domain on the lab.
func bakeableTemplate(name string) WorkspaceTemplate {
	return WorkspaceTemplate{
		Name:         name,
		GitRepo:      "https://gitlab.com/org/" + name + ".git",
		Devcontainer: &DevcontainerConfig{Enabled: true, CacheRepo: "registry.example.com/cache"},
	}
}

// useCapturingBakeBackend wires a backend that records the bakes it is asked for. Its
// Job reports failed, which ends each background awaitBake on its first poll.
func useCapturingBakeBackend(h *Handler) *capturingBakeBackend {
	cb := &capturingBakeBackend{
		fakeBackend:      &fakeBackend{reachable: true},
		fakeBakeProvider: &fakeBakeProvider{jobStatus: workspace.BakeStateFailed},
	}
	h.newWorkspaceBackend = func(_, _ string) (workspace.Backend, error) { return cb, nil }
	return cb
}

// bakedTemplates returns the names of the templates the backend was asked to bake.
func (c *capturingBakeBackend) bakedTemplates() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	names := make([]string, 0, len(c.requests))
	for _, req := range c.requests {
		names = append(names, req.Template)
	}
	return names
}

func TestDevcontainerTemplateNames(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		templates []WorkspaceTemplate
		expected  []string
	}{
		{name: "no templates", templates: nil, expected: nil},
		{name: "plain template", templates: []WorkspaceTemplate{{Name: "plain", Image: "golang:1.22"}}, expected: nil},
		{name: "disabled devcontainer block", templates: []WorkspaceTemplate{{Name: "inert", Devcontainer: &DevcontainerConfig{}}}, expected: nil},
		{
			name:      "only the devcontainer ones, in order",
			templates: []WorkspaceTemplate{bakeableTemplate("go"), {Name: "plain"}, bakeableTemplate("python")},
			expected:  []string{"go", "python"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, devcontainerTemplateNames(tt.templates))
		})
	}
}

func TestCreateLabConfigFromForm_AutoBake(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		value    string
		expected bool
	}{
		{name: "checked", value: "true", expected: true},
		{name: "unchecked", value: "", expected: false},
		{name: "anything else", value: "on", expected: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			form := url.Values{"stack_name": {"workshop"}}
			if tt.value != "" {
				form.Set("auto_bake", tt.value)
			}
			req := httptest.NewRequest(http.MethodPost, "/api/labs", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			require.NoError(t, req.ParseForm())

			h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
			config := h.createLabConfigFromForm(req, nil)
			assert.Equal(t, tt.expected, config.AutoBake)
		})
	}
}

// A lab created with the option on has every devcontainer template baked once its
// cluster is up — and only those: a plain template has nothing to bake.
func TestAutoBakeLab(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		autoBake bool
		expected []string
	}{
		{name: "option on", autoBake: true, expected: []string{"go", "python"}},
		{name: "option off", autoBake: false, expected: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h, jm := newUploadTestHandler(t)
			cb := useCapturingBakeBackend(h)
			id := bakeLab(t, jm, bakeableTemplate("go"), WorkspaceTemplate{Name: "plain"}, bakeableTemplate("python"))
			h.updateJobConfig(id, func(c *LabConfig) { c.AutoBake = tt.autoBake })

			h.autoBakeLab(id)

			assert.ElementsMatch(t, tt.expected, cb.bakedTemplates())
		})
	}
}

// The bake that follows a lab's creation runs from afterProvision, just before the
// lab is marked completed — it must not be turned away for the lab being "not ready".
func TestAutoBakeLab_RunsBeforeLabIsCompleted(t *testing.T) {
	t.Parallel()
	h, jm := newUploadTestHandler(t)
	cb := useCapturingBakeBackend(h)
	id := jm.CreateJob(&LabConfig{StackName: "test", AutoBake: true, WorkspaceTemplates: []WorkspaceTemplate{bakeableTemplate("go")}})
	jm.UpdateJobStatus(id, JobStatusRunning)
	job, _ := jm.GetJob(id)
	job.mu.Lock()
	job.Kubeconfig = "fake-kubeconfig"
	job.mu.Unlock()

	h.autoBakeLab(id)

	assert.Equal(t, []string{"go"}, cb.bakedTemplates())
	assert.Contains(t, h.renderBakeStatus(id, "go"), "building")
}

// An automatic bake that cannot start must not be silent: the reason lands on the
// template's card, exactly where a failed manual bake would show.
func TestAutoBakeTemplates_RecordsWhyABakeCouldNotStart(t *testing.T) {
	t.Parallel()
	h, jm := newUploadTestHandler(t)
	cb := useCapturingBakeBackend(h)
	id := bakeLab(t, jm,
		WorkspaceTemplate{Name: "no-cache", GitRepo: "https://gitlab.com/org/r.git", Devcontainer: &DevcontainerConfig{Enabled: true}},
		bakeableTemplate("go"),
	)

	h.autoBakeTemplates(id, []string{"no-cache", "go"}, "system", "system")

	assert.Equal(t, []string{"go"}, cb.bakedTemplates(), "one template failing to start must not stop the next")
	status := h.renderBakeStatus(id, "no-cache")
	assert.Contains(t, status, "status-failed")
	assert.Contains(t, status, "cache registry")
}

// Adding a template bakes it when asked to. The drawer always says which it wants;
// a caller that does not falls back to the lab's own setting.
func TestUploadTemplateToLab_AutoBake(t *testing.T) {
	t.Parallel()
	const devcontainerYAML = "workspace_templates:\n  - name: go\n    git_repo: https://gitlab.com/o/r.git\n    devcontainer:\n      enabled: true\n      cache_repo: registry.example.com/cache\n"
	const plainYAML = "workspace_templates:\n  - name: plain\n"

	tests := []struct {
		name        string
		labAutoBake bool
		formValue   string // "" leaves auto_bake out of the request
		yaml        string
		expected    []string
	}{
		{name: "asked for", formValue: "true", yaml: devcontainerYAML, expected: []string{"go"}},
		{name: "not asked for", formValue: "false", yaml: devcontainerYAML, expected: nil},
		{name: "left out, lab bakes automatically", labAutoBake: true, yaml: devcontainerYAML, expected: []string{"go"}},
		{name: "left out, lab does not", yaml: devcontainerYAML, expected: nil},
		{name: "unchecked overrides the lab's setting", labAutoBake: true, formValue: "false", yaml: devcontainerYAML, expected: nil},
		{name: "a plain template has nothing to bake", formValue: "true", yaml: plainYAML, expected: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h, jm := newUploadTestHandler(t)
			cb := useCapturingBakeBackend(h)
			id := bakeLab(t, jm, WorkspaceTemplate{Name: "existing"})
			h.updateJobConfig(id, func(c *LabConfig) { c.AutoBake = tt.labAutoBake })

			form := url.Values{"templates_mode": {"yaml"}, "templates_yaml": {tt.yaml}}
			if tt.formValue != "" {
				form.Set("auto_bake", tt.formValue)
			}
			rec := postUpload(h, id, form.Encode())
			require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

			var resp struct {
				Baking []string `json:"baking"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
			assert.Equal(t, tt.expected, resp.Baking)

			// The bake itself starts off the response's path.
			if len(tt.expected) > 0 {
				require.Eventually(t, func() bool { return len(cb.bakedTemplates()) == len(tt.expected) }, 2*time.Second, 10*time.Millisecond)
			} else {
				time.Sleep(50 * time.Millisecond)
			}
			assert.ElementsMatch(t, tt.expected, cb.bakedTemplates())
		})
	}
}

// The drawer reloads the page as soon as the addition is answered, and a template's
// card asks for its bake status once, on load: the bake must already read as building
// by then, however long it takes to actually start.
func TestUploadTemplateToLab_AutoBake_BuildingBeforeTheBakeStarts(t *testing.T) {
	t.Parallel()
	const yaml = "workspace_templates:\n  - name: go\n    git_repo: https://gitlab.com/o/r.git\n    devcontainer:\n      enabled: true\n      cache_repo: registry.example.com/cache\n"

	h, jm := newUploadTestHandler(t)
	cb := useCapturingBakeBackend(h)
	id := bakeLab(t, jm, WorkspaceTemplate{Name: "existing"})

	// Holding the backend's lock keeps EnsureBakeJob — and so startBake — from
	// completing until the status has been read.
	cb.mu.Lock()
	form := url.Values{"templates_mode": {"yaml"}, "templates_yaml": {yaml}, "auto_bake": {"true"}}
	rec := postUpload(h, id, form.Encode())
	status := h.renderBakeStatus(id, "go")
	cb.mu.Unlock()

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	assert.Contains(t, status, "status-running")
	assert.Contains(t, status, "bake-status", "the card must keep polling the bake")

	require.Eventually(t, func() bool { return len(cb.bakedTemplates()) == 1 }, 2*time.Second, 10*time.Millisecond)
}

// The wizard offers the option unchecked; the drawer opens on the lab's own setting.
func TestAutoBakeOption_Rendered(t *testing.T) {
	t.Chdir("../..")

	t.Run("wizard: off by default", func(t *testing.T) {
		h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
		w := httptest.NewRecorder()
		h.ServeAdminUI(w, httptest.NewRequest(http.MethodGet, "/admin", nil))
		require.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), `name="auto_bake" value="true">`)
	})

	tests := []struct {
		name     string
		autoBake bool
		expected string
	}{
		{name: "drawer: lab bakes automatically", autoBake: true, expected: `name="auto_bake" value="true" checked>`},
		{name: "drawer: lab does not", autoBake: false, expected: `name="auto_bake" value="true">`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, jm := newUploadTestHandler(t)
			useFakeBackend(h, &fakeBackend{reachable: true})
			id := bakeLab(t, jm, WorkspaceTemplate{Name: "go"})
			h.updateJobConfig(id, func(c *LabConfig) { c.AutoBake = tt.autoBake })

			rec := httptest.NewRecorder()
			h.ServeLabDetail(rec, httptest.NewRequest(http.MethodGet, "/labs/"+id, nil))
			require.Equal(t, http.StatusOK, rec.Code)
			assert.Contains(t, rec.Body.String(), tt.expected)
		})
	}
}
