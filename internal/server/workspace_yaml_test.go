package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// postForm builds a urlencoded POST request the handlers can parse.
func postForm(t *testing.T, target string, values url.Values) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

func TestParseWorkspaceTemplatesYAML(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected []WorkspaceTemplate
	}{
		{
			name: "minimal template",
			input: `workspace_templates:
  - name: default`,
			expected: []WorkspaceTemplate{{Name: "default"}},
		},
		{
			name: "bare list without the workspace_templates key",
			input: `- name: default
  image: my/image:1`,
			expected: []WorkspaceTemplate{{Name: "default", Image: "my/image:1"}},
		},
		{
			name: "full template",
			input: `workspace_templates:
  - name: go-workshop
    ide: code-server
    image: codercom/enterprise-base:ubuntu
    git_repo: https://github.com/org/workshop
    git_branch: main
    git_folder: exercises
    cpu: "2"
    memory: 4Gi
    disk_size: 10Gi
    startup_script: |
      apt-get update
    dotfiles_repo: https://github.com/you/dotfiles
    extensions:
      - golang.go
    env:
      FOO: bar`,
			expected: []WorkspaceTemplate{{
				Name:          "go-workshop",
				IDE:           "code-server",
				Image:         "codercom/enterprise-base:ubuntu",
				GitRepo:       "https://github.com/org/workshop",
				GitBranch:     "main",
				GitFolder:     "exercises",
				CPU:           "2",
				Memory:        "4Gi",
				DiskSize:      "10Gi",
				StartupScript: "apt-get update\n",
				DotfilesRepo:  "https://github.com/you/dotfiles",
				Extensions:    []string{"golang.go"},
				Env:           map[string]string{"FOO": "bar"},
			}},
		},
		{
			name: "sidecars and mounts",
			input: `workspace_templates:
  - name: with-db
    sidecars:
      - name: db
        image: postgres:16
        ports: [5432, 5433]
        env:
          POSTGRES_PASSWORD: postgres
        privileged: true
        capabilities: [SYS_ADMIN]
    mounts:
      - type: secret
        name: my-secret
        path: /etc/secret`,
			expected: []WorkspaceTemplate{{
				Name: "with-db",
				Sidecars: []WorkspaceSidecar{{
					Name:         "db",
					Image:        "postgres:16",
					Ports:        []int{5432, 5433},
					Env:          map[string]string{"POSTGRES_PASSWORD": "postgres"},
					Privileged:   true,
					Capabilities: []string{"SYS_ADMIN"},
				}},
				Mounts: []WorkspaceMount{{Type: "secret", Name: "my-secret", Path: "/etc/secret"}},
			}},
		},
		{
			// Both IDEs are injected onto a volume the build leaves alone, so
			// code-server is as valid here as the default.
			name: "devcontainer with code-server",
			input: `workspace_templates:
  - name: default
    ide: code-server
    git_repo: https://gitlab.com/org/workshop.git
    devcontainer:
      enabled: true
      cache_repo: registry.example.com/cache`,
			expected: []WorkspaceTemplate{{
				Name:    "default",
				IDE:     "code-server",
				GitRepo: "https://gitlab.com/org/workshop.git",
				Devcontainer: &DevcontainerConfig{
					Enabled:   true,
					CacheRepo: "registry.example.com/cache",
				},
			}},
		},
		{
			name: "devcontainer block",
			input: `workspace_templates:
  - name: go-workshop
    git_repo: https://gitlab.com/org/workshop.git
    git_branch: main
    cpu: "2"
    memory: 4Gi
    disk_size: 20Gi
    extensions:
      - golang.go
    devcontainer:
      enabled: true
      dir: .devcontainer
      cache_repo: registry.example.com/easylab/cache
      registry_auth_secret: regcred
      fallback_image: gitpod/openvscode-server:latest
      insecure: false`,
			expected: []WorkspaceTemplate{{
				Name:       "go-workshop",
				GitRepo:    "https://gitlab.com/org/workshop.git",
				GitBranch:  "main",
				CPU:        "2",
				Memory:     "4Gi",
				DiskSize:   "20Gi",
				Extensions: []string{"golang.go"},
				Devcontainer: &DevcontainerConfig{
					Enabled:            true,
					Dir:                ".devcontainer",
					CacheRepo:          "registry.example.com/easylab/cache",
					RegistryAuthSecret: "regcred",
					FallbackImage:      "gitpod/openvscode-server:latest",
				},
			}},
		},
		{
			name: "devcontainer block disabled is inert",
			input: `workspace_templates:
  - name: plain
    image: golang:1.22
    devcontainer:
      enabled: false`,
			expected: []WorkspaceTemplate{{
				Name:         "plain",
				Image:        "golang:1.22",
				Devcontainer: &DevcontainerConfig{},
			}},
		},
		{
			name: "multiple templates",
			input: `workspace_templates:
  - name: one
  - name: two`,
			expected: []WorkspaceTemplate{{Name: "one"}, {Name: "two"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseWorkspaceTemplatesYAML(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestParseWorkspaceTemplatesYAML_Errors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     string
		wantInErr string
	}{
		{
			name:      "empty document",
			input:     "   \n  ",
			wantInErr: "empty",
		},
		{
			name: "malformed YAML",
			input: `workspace_templates:
  - name: default
   image: broken-indent`,
			wantInErr: "invalid YAML",
		},
		{
			name: "unknown key is rejected, not ignored",
			input: `workspace_templates:
  - name: default
    imagee: typo/image:1`,
			wantInErr: `unknown field "imagee"`,
		},
		{
			name: "wrong scalar type",
			input: `workspace_templates:
  - name: default
    cpu: 2`,
			wantInErr: "cannot unmarshal",
		},
		{
			name:      "no templates",
			input:     `workspace_templates: []`,
			wantInErr: "at least one template",
		},
		{
			name: "missing name",
			input: `workspace_templates:
  - image: my/image:1`,
			wantInErr: "name is required",
		},
		{
			name: "duplicate names",
			input: `workspace_templates:
  - name: dup
  - name: dup`,
			wantInErr: `duplicate template name "dup"`,
		},
		{
			name: "unknown IDE",
			input: `workspace_templates:
  - name: default
    ide: emacs`,
			wantInErr: "ide must be",
		},
		{
			name: "invalid git repo URL",
			input: `workspace_templates:
  - name: default
    git_repo: not-a-url`,
			wantInErr: "is not a valid URL",
		},
		{
			name: "sidecar without image",
			input: `workspace_templates:
  - name: default
    sidecars:
      - name: db`,
			wantInErr: "image is required",
		},
		{
			name: "sidecar port out of range",
			input: `workspace_templates:
  - name: default
    sidecars:
      - name: db
        image: postgres:16
        ports: [99999]`,
			wantInErr: "out of range",
		},
		{
			name: "unknown mount type would silently become a configmap",
			input: `workspace_templates:
  - name: default
    mounts:
      - type: cofigmap
        name: my-config
        path: /etc/config`,
			wantInErr: `type must be "configmap" or "secret"`,
		},
		{
			name: "mount without path is dropped at runtime",
			input: `workspace_templates:
  - name: default
    mounts:
      - type: configmap
        name: my-config`,
			wantInErr: "path is required",
		},
		{
			name: "devcontainer without git_repo has nothing to build from",
			input: `workspace_templates:
  - name: default
    devcontainer:
      enabled: true
      cache_repo: registry.example.com/cache`,
			wantInErr: "requires git_repo",
		},
		{
			name: "devcontainer with an image would silently ignore the image",
			input: `workspace_templates:
  - name: default
    image: golang:1.22
    git_repo: https://gitlab.com/org/workshop.git
    devcontainer:
      enabled: true
      cache_repo: registry.example.com/cache`,
			wantInErr: "conflicts with image",
		},
		{
			name: "devcontainer without a cache repo rebuilds per student",
			input: `workspace_templates:
  - name: default
    git_repo: https://gitlab.com/org/workshop.git
    devcontainer:
      enabled: true`,
			wantInErr: "cache_repo is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := parseWorkspaceTemplatesYAML(tt.input)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantInErr)
			assert.NotContains(t, err.Error(), "json:", "decoder errors should be reworded for admins")
		})
	}
}

func TestParseWorkspaceTemplatesYAML_MountTypeCaseInsensitive(t *testing.T) {
	t.Parallel()

	// The workspace backend lowercases the type, so the editor must not reject
	// what would actually work.
	for _, mountType := range []string{"configmap", "ConfigMap", "SECRET", ""} {
		got, err := parseWorkspaceTemplatesYAML(`workspace_templates:
  - name: default
    mounts:
      - type: "` + mountType + `"
        name: my-config
        path: /etc/config`)
		require.NoError(t, err, "mount type %q", mountType)
		require.Len(t, got, 1)
	}
}

func TestMarshalWorkspaceTemplatesYAML_RoundTrip(t *testing.T) {
	t.Parallel()

	original := []WorkspaceTemplate{
		{
			Name:          "go-workshop",
			IDE:           "code-server",
			Image:         "codercom/enterprise-base:ubuntu",
			GitRepo:       "https://github.com/org/workshop",
			GitBranch:     "main",
			CPU:           "2",
			Memory:        "4Gi",
			StartupScript: "apt-get update\n",
			Extensions:    []string{"golang.go", "ms-python.python"},
			Env:           map[string]string{"FOO": "bar"},
			Sidecars: []WorkspaceSidecar{{
				Name:  "db",
				Image: "postgres:16",
				Ports: []int{5432},
				Env:   map[string]string{"POSTGRES_PASSWORD": "postgres"},
			}},
			Mounts: []WorkspaceMount{{Type: "configmap", Name: "cfg", Path: "/etc/config"}},
		},
		{Name: "minimal"},
	}

	rendered, err := marshalWorkspaceTemplatesYAML(original)
	require.NoError(t, err)

	got, err := parseWorkspaceTemplatesYAML(rendered)
	require.NoError(t, err)
	assert.Equal(t, original, got, "templates must survive a marshal/parse round trip")
}

func TestMarshalWorkspaceTemplatesYAML_RoundTripDevcontainer(t *testing.T) {
	t.Parallel()

	// Devcontainer templates get their own round trip: they cannot carry an image,
	// so they cannot ride along in the general round-trip fixture.
	original := []WorkspaceTemplate{{
		Name:       "go-workshop",
		GitRepo:    "https://gitlab.com/org/workshop.git",
		GitBranch:  "main",
		GitFolder:  "exercises",
		CPU:        "2",
		Memory:     "4Gi",
		DiskSize:   "20Gi",
		Extensions: []string{"golang.go"},
		Devcontainer: &DevcontainerConfig{
			Enabled:            true,
			Dir:                ".devcontainer",
			CacheRepo:          "registry.example.com/easylab/cache",
			RegistryAuthSecret: "regcred",
			FallbackImage:      "gitpod/openvscode-server:latest",
			Insecure:           true,
		},
	}}

	rendered, err := marshalWorkspaceTemplatesYAML(original)
	require.NoError(t, err)

	got, err := parseWorkspaceTemplatesYAML(rendered)
	require.NoError(t, err)
	assert.Equal(t, original, got, "devcontainer templates must survive a marshal/parse round trip")
}

// TestMarshalWorkspaceTemplatesYAML_ExportOmitsRegistryCredentials pins the rule
// that the export carries a Secret reference, never secret material.
func TestMarshalWorkspaceTemplatesYAML_ExportOmitsRegistryCredentials(t *testing.T) {
	t.Parallel()

	rendered, err := marshalWorkspaceTemplatesYAML([]WorkspaceTemplate{{
		Name:    "go-workshop",
		GitRepo: "https://gitlab.com/org/workshop.git",
		Devcontainer: &DevcontainerConfig{
			Enabled:            true,
			CacheRepo:          "registry.example.com/easylab/cache",
			RegistryAuthSecret: "regcred",
		},
	}})
	require.NoError(t, err)

	assert.Contains(t, rendered, "registry_auth_secret: regcred")
	assert.NotContains(t, rendered, "dockerconfigjson")
	assert.NotContains(t, rendered, "password")
}

func TestMarshalWorkspaceTemplatesYAML_KeepsStructFieldOrder(t *testing.T) {
	t.Parallel()

	// Keys must follow WorkspaceTemplate's field order, not the alphabetical order
	// a map would impose — "name" identifies the entry and belongs first.
	rendered, err := marshalWorkspaceTemplatesYAML([]WorkspaceTemplate{{
		Name:  "go-workshop",
		Image: "my/image:1",
		CPU:   "2",
		IDE:   "code-server",
	}})
	require.NoError(t, err)

	assert.Equal(t, `workspace_templates:
  - name: go-workshop
    image: my/image:1
    cpu: "2"
    ide: code-server
`, rendered)
}

func TestMarshalWorkspaceTemplatesYAML_ScalarRendering(t *testing.T) {
	t.Parallel()

	rendered, err := marshalWorkspaceTemplatesYAML([]WorkspaceTemplate{{
		Name:          "t",
		CPU:           "2",
		StartupScript: "apt-get update\napt-get install -y jq\n",
		Sidecars:      []WorkspaceSidecar{{Name: "db", Image: "postgres:16", Ports: []int{5432}, Privileged: true}},
	}})
	require.NoError(t, err)

	assert.Contains(t, rendered, `cpu: "2"`, "strings that look like numbers must stay quoted")
	assert.Contains(t, rendered, "startup_script: |", "multi-line scripts should be literal blocks")
	assert.Contains(t, rendered, "- 5432", "ports must stay unquoted ints")
	assert.Contains(t, rendered, "privileged: true", "booleans must stay unquoted")
}

func TestMarshalWorkspaceTemplatesYAML_OmitsEmptyFields(t *testing.T) {
	t.Parallel()

	rendered, err := marshalWorkspaceTemplatesYAML([]WorkspaceTemplate{{Name: "default"}})
	require.NoError(t, err)

	assert.Contains(t, rendered, "name: default")
	for _, key := range []string{"image:", "git_repo:", "sidecars:", "mounts:", "env:"} {
		assert.NotContains(t, rendered, key, "unset optional fields should stay out of the editor")
	}
}

func TestWorkspaceTemplatesYAMLSkeleton_IsValid(t *testing.T) {
	t.Parallel()

	// The skeleton is handed straight to admins, so it must parse as-is.
	got, err := parseWorkspaceTemplatesYAML(workspaceTemplatesYAMLSkeleton)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "default", got[0].Name)
}

func TestWorkspaceTemplatesYAMLSkeleton_DocumentsEverySupportedKey(t *testing.T) {
	t.Parallel()

	// Guards against the skeleton going stale as WorkspaceTemplate grows: every
	// json key on the struct should appear in the commented schema.
	keys := []string{
		"name", "ide", "image", "git_repo", "git_branch", "git_folder", "cpu",
		"memory", "disk_size", "startup_script", "dotfiles_repo", "extensions",
		"env", "sidecars", "mounts", "devcontainer",
		"git_auth_secret", "image_pull_secrets",
	}
	for _, key := range keys {
		assert.Contains(t, workspaceTemplatesYAMLSkeleton, key+":", "skeleton should document %q", key)
	}
}

func TestMarshalWorkspaceTemplatesYAML_Empty(t *testing.T) {
	t.Parallel()

	rendered, err := marshalWorkspaceTemplatesYAML(nil)
	require.NoError(t, err)
	assert.Equal(t, "workspace_templates: null\n", strings.TrimSpace(rendered)+"\n")
}

func TestWorkspaceTemplatesFromRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		form     url.Values
		expected []WorkspaceTemplate
	}{
		{
			name: "form mode reads the wizard fields",
			form: url.Values{
				"templates_mode":   {"form"},
				"template_0_name":  {"from-form"},
				"template_0_image": {"form/image:1"},
			},
			expected: []WorkspaceTemplate{{Name: "from-form", Image: "form/image:1"}},
		},
		{
			name: "no mode defaults to the wizard",
			form: url.Values{
				"template_0_name": {"from-form"},
			},
			expected: []WorkspaceTemplate{{Name: "from-form"}},
		},
		{
			name: "yaml mode wins over the wizard fields still in the DOM",
			form: url.Values{
				"templates_mode":  {"yaml"},
				"template_0_name": {"stale-form-value"},
				"templates_yaml":  {"workspace_templates:\n  - name: from-yaml\n    image: yaml/image:1"},
			},
			expected: []WorkspaceTemplate{{Name: "from-yaml", Image: "yaml/image:1"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := postForm(t, "/api/labs", tt.form)
			require.NoError(t, req.ParseForm())

			got, err := workspaceTemplatesFromRequest(req)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestWorkspaceTemplatesFromRequest_InvalidYAMLErrors(t *testing.T) {
	t.Parallel()

	req := postForm(t, "/api/labs", url.Values{
		"templates_mode": {"yaml"},
		"templates_yaml": {"workspace_templates:\n  - name: default\n    imagee: typo"},
	})
	require.NoError(t, req.ParseForm())

	_, err := workspaceTemplatesFromRequest(req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "imagee")
}

func newYAMLTestHandler() *Handler {
	return NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
}

func TestValidateWorkspaceTemplatesYAMLHandler(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		yaml      string
		wantClass string
		wantInMsg string
	}{
		{
			name:      "valid YAML reports the template names",
			yaml:      "workspace_templates:\n  - name: one\n  - name: two",
			wantClass: "toast-success",
			wantInMsg: "one, two",
		},
		{
			name:      "invalid YAML reports the reason",
			yaml:      "workspace_templates:\n  - name: default\n    imagee: typo",
			wantClass: "toast-error",
			wantInMsg: "imagee",
		},
		{
			name:      "empty YAML is rejected",
			yaml:      "",
			wantClass: "toast-error",
			wantInMsg: "empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := newYAMLTestHandler()
			req := postForm(t, "/api/labs/templates/yaml/validate", url.Values{"templates_yaml": {tt.yaml}})
			rec := httptest.NewRecorder()

			h.ValidateWorkspaceTemplatesYAML(rec, req)

			require.Equal(t, http.StatusOK, rec.Code)
			assert.Contains(t, rec.Body.String(), tt.wantClass)
			assert.Contains(t, rec.Body.String(), tt.wantInMsg)
		})
	}
}

func TestValidateWorkspaceTemplatesYAMLHandler_EscapesYAMLInErrors(t *testing.T) {
	t.Parallel()

	// The toast helper does not escape, and this message is built from admin input.
	h := newYAMLTestHandler()
	req := postForm(t, "/api/labs/templates/yaml/validate", url.Values{
		"templates_yaml": {"workspace_templates:\n  - name: \"<img src=x onerror=alert(1)>\"\n    ide: bad"},
	})
	rec := httptest.NewRecorder()

	h.ValidateWorkspaceTemplatesYAML(rec, req)

	assert.NotContains(t, rec.Body.String(), "<img src=x")
	assert.Contains(t, rec.Body.String(), "&lt;img")
}

func TestValidateWorkspaceTemplatesYAMLHandler_RejectsGET(t *testing.T) {
	t.Parallel()

	h := newYAMLTestHandler()
	rec := httptest.NewRecorder()
	h.ValidateWorkspaceTemplatesYAML(rec, httptest.NewRequest(http.MethodGet, "/api/labs/templates/yaml/validate", nil))
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

func TestServeWorkspaceTemplatesYAML_SeedFromForm(t *testing.T) {
	t.Parallel()

	h := newYAMLTestHandler()
	req := postForm(t, "/api/labs/templates/yaml", url.Values{
		"template_0_name":     {"seeded"},
		"template_0_image":    {"my/image:1"},
		"template_0_git_repo": {"https://github.com/org/repo"},
	})
	rec := httptest.NewRecorder()

	h.ServeWorkspaceTemplatesYAML(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/x-yaml", rec.Header().Get("Content-Type"))

	// The seed must be valid input for the editor it feeds.
	got, err := parseWorkspaceTemplatesYAML(rec.Body.String())
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "seeded", got[0].Name)
	assert.Equal(t, "my/image:1", got[0].Image)
}

func TestServeWorkspaceTemplatesYAML_SeedWithNoTemplatesReturnsSkeleton(t *testing.T) {
	t.Parallel()

	h := newYAMLTestHandler()
	rec := httptest.NewRecorder()

	h.ServeWorkspaceTemplatesYAML(rec, postForm(t, "/api/labs/templates/yaml", url.Values{}))

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, workspaceTemplatesYAMLSkeleton, rec.Body.String())
}

func TestServeWorkspaceTemplatesYAML_Export(t *testing.T) {
	t.Parallel()

	h := newYAMLTestHandler()
	labID := h.jobManager.CreateJob(&LabConfig{
		StackName: "my-workshop",
		WorkspaceTemplates: []WorkspaceTemplate{
			{Name: "exported", Image: "my/image:1"},
		},
	})

	rec := httptest.NewRecorder()
	h.ServeWorkspaceTemplatesYAML(rec, httptest.NewRequest(http.MethodGet, "/api/labs/templates/yaml?lab_id="+labID+"&download=1", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "attachment; filename=workspace-templates-my-workshop.yaml", rec.Header().Get("Content-Disposition"))

	got, err := parseWorkspaceTemplatesYAML(rec.Body.String())
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "exported", got[0].Name)
}

func TestServeWorkspaceTemplatesYAML_ExportSanitizesFilename(t *testing.T) {
	t.Parallel()

	// A stack name flows into a response header, so it must not be able to inject one.
	h := newYAMLTestHandler()
	labID := h.jobManager.CreateJob(&LabConfig{
		StackName:          "evil\r\nX-Injected: yes",
		WorkspaceTemplates: []WorkspaceTemplate{{Name: "t"}},
	})

	rec := httptest.NewRecorder()
	h.ServeWorkspaceTemplatesYAML(rec, httptest.NewRequest(http.MethodGet, "/api/labs/templates/yaml?lab_id="+labID+"&download=1", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, rec.Header().Get("X-Injected"))
	assert.Equal(t, "attachment; filename=workspace-templates-evilX-Injectedyes.yaml", rec.Header().Get("Content-Disposition"))
}

func TestServeWorkspaceTemplatesYAML_ExportErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		target     string
		wantStatus int
	}{
		{"missing lab_id", "/api/labs/templates/yaml", http.StatusBadRequest},
		{"unknown lab", "/api/labs/templates/yaml?lab_id=job-does-not-exist", http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := newYAMLTestHandler()
			rec := httptest.NewRecorder()
			h.ServeWorkspaceTemplatesYAML(rec, httptest.NewRequest(http.MethodGet, tt.target, nil))
			assert.Equal(t, tt.wantStatus, rec.Code)
		})
	}
}

func TestServeWorkspaceTemplatesYAML_ExportDefaultsToTheDefaultTemplate(t *testing.T) {
	t.Parallel()

	// A lab created before templates existed still has to export something usable.
	h := newYAMLTestHandler()
	labID := h.jobManager.CreateJob(&LabConfig{StackName: "legacy"})

	rec := httptest.NewRecorder()
	h.ServeWorkspaceTemplatesYAML(rec, httptest.NewRequest(http.MethodGet, "/api/labs/templates/yaml?lab_id="+labID, nil))

	require.Equal(t, http.StatusOK, rec.Code)
	got, err := parseWorkspaceTemplatesYAML(rec.Body.String())
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "default", got[0].Name)
}

func TestServeWorkspaceTemplatesYAML_RejectsPut(t *testing.T) {
	t.Parallel()

	h := newYAMLTestHandler()
	rec := httptest.NewRecorder()
	h.ServeWorkspaceTemplatesYAML(rec, httptest.NewRequest(http.MethodPut, "/api/labs/templates/yaml", nil))
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

func TestValidateAuthSecrets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		template WorkspaceTemplate
		wantErr  string
	}{
		{
			name:     "no credentials at all",
			template: WorkspaceTemplate{Name: "a"},
		},
		{
			name:     "pull secrets without a repo are fine",
			template: WorkspaceTemplate{Name: "a", ImagePullSecrets: []string{"regcred"}},
		},
		{
			name:     "git auth with an https repo",
			template: WorkspaceTemplate{Name: "a", GitRepo: "https://gitlab.com/o/r.git", GitAuthSecret: "gitcred"},
		},
		{
			name:     "git auth with an http repo",
			template: WorkspaceTemplate{Name: "a", GitRepo: "http://git.internal/o/r.git", GitAuthSecret: "gitcred"},
		},
		{
			name:     "blank pull secret entry",
			template: WorkspaceTemplate{Name: "a", ImagePullSecrets: []string{"regcred", "  "}},
			wantErr:  "image_pull_secrets 2: name must not be empty",
		},
		{
			name:     "git auth without a repo",
			template: WorkspaceTemplate{Name: "a", GitAuthSecret: "gitcred"},
			wantErr:  "git_auth_secret requires git_repo",
		},
		{
			// Basic auth is not how ssh authenticates: the secret would be ignored and
			// the clone would fail on the key instead.
			name:     "git auth over ssh",
			template: WorkspaceTemplate{Name: "a", GitRepo: "ssh://git@gitlab.com/o/r.git", GitAuthSecret: "gitcred"},
			wantErr:  "git_auth_secret needs an http(s) git_repo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateAuthSecrets("template \"a\"", tt.template)
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestGitCredentialWarnings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		template WorkspaceTemplate
		wantKeys []string
	}{
		{
			name:     "clean url",
			template: WorkspaceTemplate{Name: "a", GitRepo: "https://gitlab.com/o/r.git"},
		},
		{
			// A bare username is the ordinary way to write an ssh remote and carries
			// no secret; warning here would train admins to ignore the warning.
			name:     "ssh remote with a username",
			template: WorkspaceTemplate{Name: "a", GitRepo: "ssh://git@gitlab.com/o/r.git"},
		},
		{
			name:     "https username with no password",
			template: WorkspaceTemplate{Name: "a", GitRepo: "https://bob@gitlab.com/o/r.git"},
		},
		{
			name:     "token in the url",
			template: WorkspaceTemplate{Name: "a", GitRepo: "https://oauth2:glpat-x@gitlab.com/o/r.git"},
			wantKeys: []string{"git_repo"},
		},
		{
			name:     "token in the dotfiles url",
			template: WorkspaceTemplate{Name: "a", DotfilesRepo: "https://oauth2:glpat-x@github.com/me/dots.git"},
			wantKeys: []string{"dotfiles_repo"},
		},
		{
			// The URL's own credential wins, so the secret is dead config the admin
			// would otherwise believe is in force.
			name: "token in the url alongside a git_auth_secret",
			template: WorkspaceTemplate{
				Name: "a", GitRepo: "https://oauth2:glpat-x@gitlab.com/o/r.git", GitAuthSecret: "gitcred",
			},
			wantKeys: []string{"git_repo", "git_auth_secret"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := gitCredentialWarnings([]WorkspaceTemplate{tt.template})

			keys := make([]string, 0, len(got))
			for _, w := range got {
				keys = append(keys, w.Key)
			}
			assert.ElementsMatch(t, tt.wantKeys, keys)

			// A warning about a leaking token must not itself quote the token.
			for _, w := range got {
				assert.NotContains(t, w.Message, "glpat-x")
			}
		})
	}
}

// Warn-but-allow: an inline credential is reported, and still accepted.
func TestValidateWorkspaceTemplatesYAML_InlineCredsWarnOnSuccessToast(t *testing.T) {
	h := newYAMLTestHandler()
	rec := httptest.NewRecorder()

	yamlDoc := "workspace_templates:\n  - name: a\n    git_repo: https://oauth2:glpat-x@gitlab.com/o/r.git\n"
	h.ValidateWorkspaceTemplatesYAML(rec, postForm(t, "/api/labs/templates/validate", url.Values{
		"templates_yaml": {yamlDoc},
	}))

	body := rec.Body.String()
	assert.Contains(t, body, "toast-success", "an inline credential must not block validation")
	assert.Contains(t, body, "embeds a credential in the URL")
}

func TestParseWorkspaceTemplatesYAML_AuthSecretsRoundTrip(t *testing.T) {
	t.Parallel()

	got, err := parseWorkspaceTemplatesYAML(
		"workspace_templates:\n" +
			"  - name: a\n" +
			"    git_repo: https://gitlab.com/o/r.git\n" +
			"    git_auth_secret: gitcred\n" +
			"    image_pull_secrets:\n" +
			"      - regcred\n" +
			"      - otherreg\n")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "gitcred", got[0].GitAuthSecret)
	assert.Equal(t, []string{"regcred", "otherreg"}, got[0].ImagePullSecrets)
}

// The export references credentials by name; the names are safe to emit, the
// material they point at never appears because it is not in the template.
func TestMarshalWorkspaceTemplatesYAML_EmitsAuthSecretNamesOnly(t *testing.T) {
	t.Parallel()

	rendered, err := marshalWorkspaceTemplatesYAML([]WorkspaceTemplate{{
		Name:             "go-workshop",
		GitRepo:          "https://gitlab.com/o/r.git",
		GitAuthSecret:    "gitcred",
		ImagePullSecrets: []string{"regcred"},
	}})
	require.NoError(t, err)

	assert.Contains(t, rendered, "git_auth_secret: gitcred")
	assert.Contains(t, rendered, "- regcred")
	assert.NotContains(t, rendered, "glpat-")
	assert.NotContains(t, rendered, "password")
}
