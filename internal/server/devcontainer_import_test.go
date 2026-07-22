package server

import (
	"archive/zip"
	"bytes"
	"easylab/internal/devcontainer"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDetectDevcontainer_FromUploadedJSON(t *testing.T) {
	t.Parallel()

	body := `{
		// A workshop devcontainer.
		"name": "Go Workshop",
		"image": "mcr.microsoft.com/devcontainers/go:1.22",
		"hostRequirements": {"cpus": 2, "memory": "4gb"},
		"customizations": {"vscode": {"extensions": ["golang.go"]}},
		"postCreateCommand": "go mod download",
	}`

	got := postDevcontainerUpload(t, "devcontainer.json", []byte(body), url.Values{
		"git_repo":   {"https://gitlab.com/org/workshop.git"},
		"git_branch": {"main"},
		"cache_repo": {"registry.example.com/easylab/cache"},
	})

	assert.Equal(t, "image", got.Base.Kind)
	assert.Equal(t, "mcr.microsoft.com/devcontainers/go:1.22", got.Base.Image)
	assert.Empty(t, got.Warnings)

	// The seeded YAML must be exactly what the editor would accept back.
	assert.Contains(t, got.TemplatesYAML, "name: go-workshop")
	assert.Contains(t, got.TemplatesYAML, "git_repo: https://gitlab.com/org/workshop.git")
	assert.Contains(t, got.TemplatesYAML, "cpu: \"2\"")
	assert.Contains(t, got.TemplatesYAML, "memory: 4Gi")
	assert.Contains(t, got.TemplatesYAML, "- golang.go")
	assert.Contains(t, got.TemplatesYAML, "enabled: true")
	assert.Contains(t, got.TemplatesYAML, "cache_repo: registry.example.com/easylab/cache")

	// envbuilder reads the image and the lifecycle commands from the repo, so they
	// must not be copied into the template.
	assert.NotContains(t, got.TemplatesYAML, "image: mcr.microsoft.com")
	assert.NotContains(t, got.TemplatesYAML, "go mod download")

	templates, err := parseWorkspaceTemplatesYAML(got.TemplatesYAML)
	require.NoError(t, err, "the seeded YAML must survive the editor's own validation")
	require.Len(t, templates, 1)
	require.NotNil(t, templates[0].Devcontainer)
	assert.True(t, templates[0].Devcontainer.Enabled)
}

func TestDetectDevcontainer_GitAuthSecretIsBakedIn(t *testing.T) {
	t.Parallel()

	got := postDevcontainerUpload(t, "devcontainer.json", []byte(`{"name":"Priv","image":"rust:1"}`), url.Values{
		"git_repo":        {"https://gitlab.com/org/private.git"},
		"cache_repo":      {"registry.example.com/cache"},
		"git_auth_secret": {"gitcred"},
	})

	// A private-repo devcontainer workshop clones the repo inside each student's pod,
	// so the generated template must name the credential it clones with.
	assert.Contains(t, got.TemplatesYAML, "git_auth_secret: gitcred")

	templates, err := parseWorkspaceTemplatesYAML(got.TemplatesYAML)
	require.NoError(t, err)
	require.Len(t, templates, 1)
	assert.Equal(t, "gitcred", templates[0].GitAuthSecret)
}

func TestDetectDevcontainer_RegistryAuthSecretIsBakedIn(t *testing.T) {
	t.Parallel()

	got := postDevcontainerUpload(t, "devcontainer.json", []byte(`{"name":"Priv","image":"rust:1"}`), url.Values{
		"git_repo":             {"https://gitlab.com/org/private.git"},
		"cache_repo":           {"registry.example.com/cache"},
		"registry_auth_secret": {"regcred"},
	})

	// A devcontainer that builds from a private base image (or pushes to a private
	// cache) authenticates to the registry inside each student's pod, so the generated
	// template must name the credential envbuilder pulls and pushes with — otherwise
	// the base-image pull falls back to anonymous and the build fails.
	assert.Contains(t, got.TemplatesYAML, "registry_auth_secret: regcred")

	templates, err := parseWorkspaceTemplatesYAML(got.TemplatesYAML)
	require.NoError(t, err)
	require.Len(t, templates, 1)
	require.NotNil(t, templates[0].Devcontainer)
	assert.Equal(t, "regcred", templates[0].Devcontainer.RegistryAuthSecret)
}

func TestDetectDevcontainer_FromUploadedZip(t *testing.T) {
	t.Parallel()

	zipBytes := zipWith(t, map[string]string{
		"workshop-main/README.md":                       "hi",
		"workshop-main/.devcontainer/devcontainer.json": `{"name":"Rust","image":"rust:1"}`,
	})

	got := postDevcontainerUpload(t, "workshop.zip", zipBytes, url.Values{
		"git_repo":   {"https://gitlab.com/org/workshop.git"},
		"cache_repo": {"registry.example.com/cache"},
	})

	assert.Equal(t, "workshop-main/.devcontainer/devcontainer.json", got.Path)
	assert.Equal(t, "rust:1", got.Base.Image)
	assert.Contains(t, got.TemplatesYAML, "name: rust")
}

func TestDetectDevcontainer_Features(t *testing.T) {
	t.Parallel()

	body := `{
		"image": "golang:1.22",
		"features": {
			"ghcr.io/devcontainers/features/docker-in-docker:2": {},
			"ghcr.io/devcontainers/features/node:1": {}
		}
	}`

	got := postDevcontainerUpload(t, "devcontainer.json", []byte(body), url.Values{
		"git_repo":   {"https://gitlab.com/org/workshop.git"},
		"cache_repo": {"registry.example.com/cache"},
	})

	// Features are reported so the admin knows what the build will pull in, but
	// they stay in the repo for envbuilder to resolve.
	assert.Equal(t, []string{
		"ghcr.io/devcontainers/features/docker-in-docker:2",
		"ghcr.io/devcontainers/features/node:1",
	}, got.Features)
	assert.NotContains(t, got.TemplatesYAML, "features")
}

func TestDetectDevcontainer_ComposeIsRejected(t *testing.T) {
	t.Parallel()

	rec := postDevcontainerUploadRaw(t, "devcontainer.json",
		[]byte(`{"dockerComposeFile":"docker-compose.yml","service":"app"}`), url.Values{
			"git_repo":   {"https://gitlab.com/org/workshop.git"},
			"cache_repo": {"registry.example.com/cache"},
		})

	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "docker-compose")
	assert.Contains(t, body, "dockerComposeFile")
}

func TestDetectDevcontainer_MissingCacheRepoWarns(t *testing.T) {
	t.Parallel()

	// The cache repo is what makes a devcontainer workshop viable, so its absence
	// must be visible at import, not only when saving.
	got := postDevcontainerUpload(t, "devcontainer.json", []byte(`{"image":"golang:1.22"}`), url.Values{
		"git_repo": {"https://gitlab.com/org/workshop.git"},
	})

	require.Len(t, got.Warnings, 1)
	assert.Equal(t, "cache_repo", got.Warnings[0].Key)
	assert.Contains(t, got.Warnings[0].Message, "rebuilds")
}

func TestDetectDevcontainer_UnsupportedKeysWarn(t *testing.T) {
	t.Parallel()

	body := `{
		"image": "golang:1.22",
		"forwardPorts": [3000],
		"mounts": ["source=x,target=/x,type=bind"],
		"privileged": true
	}`

	got := postDevcontainerUpload(t, "devcontainer.json", []byte(body), url.Values{
		"git_repo":   {"https://gitlab.com/org/workshop.git"},
		"cache_repo": {"registry.example.com/cache"},
	})

	keys := make([]string, 0, len(got.Warnings))
	for _, w := range got.Warnings {
		keys = append(keys, w.Key)
	}
	assert.Contains(t, keys, "forwardPorts")
	assert.Contains(t, keys, "mounts")
	assert.Contains(t, keys, "privileged")
}

func TestDetectDevcontainer_Errors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		filename   string
		content    string
		form       url.Values
		wantStatus int
		wantInBody string
	}{
		{
			name:       "malformed JSON points at the file",
			filename:   "devcontainer.json",
			content:    `{"image": }`,
			wantStatus: http.StatusUnprocessableEntity,
			wantInBody: "failed to parse devcontainer.json",
		},
		{
			name:       "zip without a devcontainer",
			filename:   "workshop.zip",
			wantStatus: http.StatusUnprocessableEntity,
			wantInBody: "no devcontainer.json found",
		},
		{
			name:       "unsupported file type",
			filename:   "devcontainer.yaml",
			content:    `image: golang`,
			wantStatus: http.StatusUnprocessableEntity,
			wantInBody: "unsupported file type",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			content := []byte(tt.content)
			if tt.filename == "workshop.zip" {
				content = zipWith(t, map[string]string{"repo/README.md": "hi"})
			}

			rec := postDevcontainerUploadRaw(t, tt.filename, content, tt.form)
			require.Equal(t, tt.wantStatus, rec.Code)
			assert.Contains(t, rec.Body.String(), tt.wantInBody)
		})
	}
}

func TestDetectDevcontainer_InvalidSource(t *testing.T) {
	t.Parallel()

	h := newYAMLTestHandler()
	req := postForm(t, "/api/templates/detect-devcontainer", url.Values{"source": {"carrier-pigeon"}})
	rec := httptest.NewRecorder()

	h.DetectDevcontainer(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "must be 'upload' or 'git'")
}

func TestDetectDevcontainer_RejectsGET(t *testing.T) {
	t.Parallel()

	h := newYAMLTestHandler()
	req := httptest.NewRequest(http.MethodGet, "/api/templates/detect-devcontainer", nil)
	rec := httptest.NewRecorder()

	h.DetectDevcontainer(rec, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

func TestDetectDevcontainer_GitRequiresRepo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		form       url.Values
		wantInBody string
	}{
		{
			name:       "missing repo",
			form:       url.Values{"source": {"git"}},
			wantInBody: "git_repo is required",
		},
		{
			name:       "invalid repo URL is rejected before any clone",
			form:       url.Values{"source": {"git"}, "git_repo": {"not-a-url"}},
			wantInBody: "not a valid URL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := newYAMLTestHandler()
			req := postForm(t, "/api/templates/detect-devcontainer", tt.form)
			rec := httptest.NewRecorder()

			h.DetectDevcontainer(rec, req)

			require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
			assert.Contains(t, rec.Body.String(), tt.wantInBody)
		})
	}
}

// TestDevcontainerClientError pins the rule that only errors deliberately
// written for the admin reach the client; anything else collapses to a generic
// message so internal detail cannot leak by default.
func TestDevcontainerClientError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		err      error
		expected string
	}{
		{
			name:     "marked client errors pass through",
			err:      clientErrorf("git_repo is required"),
			expected: "git_repo is required",
		},
		{
			name:     "clone failures are worded for the admin",
			err:      errDevcontainerClone,
			expected: errDevcontainerClone.Error(),
		},
		{
			name:     "not found explains where it looked",
			err:      devcontainer.ErrNotFound,
			expected: "no devcontainer.json found — looked for .devcontainer/devcontainer.json, .devcontainer.json, and .devcontainer/*/devcontainer.json",
		},
		{
			name:     "unmarked errors do not leak",
			err:      errors.New("dial tcp 10.0.0.1:5432: connect: connection refused"),
			expected: "could not read the devcontainer configuration",
		},
		{
			name:     "wrapped unmarked errors do not leak",
			err:      fmt.Errorf("reading %s: %w", "/etc/internal/path", errors.New("permission denied")),
			expected: "could not read the devcontainer configuration",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, devcontainerClientError(tt.err))
		})
	}
}

func TestDevcontainerClientError_ParseErrorsPointAtTheFile(t *testing.T) {
	t.Parallel()

	_, err := devcontainer.Parse([]byte(`{"image": }`))
	require.Error(t, err)

	got := devcontainerClientError(err)
	assert.Contains(t, got, "failed to parse devcontainer.json")
}

// TestRedactURL guards the one path where a repo URL reaches the log: a URL may
// carry credentials, and a clone failure is exactly when it gets logged.
func TestRedactURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "credentials are stripped",
			input:    "https://user:s3cret@gitlab.com/org/repo.git",
			expected: "https://redacted@gitlab.com/org/repo.git",
		},
		{
			name:     "token as username is stripped",
			input:    "https://glpat-xxxxxxxx@gitlab.com/org/repo.git",
			expected: "https://redacted@gitlab.com/org/repo.git",
		},
		{
			name:     "plain URL is untouched",
			input:    "https://gitlab.com/org/repo.git",
			expected: "https://gitlab.com/org/repo.git",
		},
		{
			name:     "unparseable URL does not leak",
			input:    "http://[::1]:namedport/x",
			expected: "[unparseable url]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := redactURL(tt.input)
			assert.Equal(t, tt.expected, got)
			assert.NotContains(t, got, "s3cret")
			assert.NotContains(t, got, "glpat-")
		})
	}
}

// postDevcontainerUpload posts an upload and requires a 200, returning the
// decoded response.
func postDevcontainerUpload(t *testing.T, filename string, content []byte, form url.Values) devcontainerImportResponse {
	t.Helper()

	rec := postDevcontainerUploadRaw(t, filename, content, form)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var got devcontainerImportResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&got))
	return got
}

func postDevcontainerUploadRaw(t *testing.T, filename string, content []byte, form url.Values) *httptest.ResponseRecorder {
	t.Helper()

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	require.NoError(t, mw.WriteField("source", "upload"))
	for k, vs := range form {
		for _, v := range vs {
			require.NoError(t, mw.WriteField(k, v))
		}
	}
	part, err := mw.CreateFormFile("devcontainer_file", filename)
	require.NoError(t, err)
	_, err = part.Write(content)
	require.NoError(t, err)
	require.NoError(t, mw.Close())

	req := httptest.NewRequest(http.MethodPost, "/api/templates/detect-devcontainer", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	rec := httptest.NewRecorder()
	newYAMLTestHandler().DetectDevcontainer(rec, req)
	return rec
}

func zipWith(t *testing.T, entries map[string]string) []byte {
	t.Helper()

	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, content := range entries {
		e, err := w.Create(name)
		require.NoError(t, err)
		_, err = e.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
	return buf.Bytes()
}

func TestGitCloneAuth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		username     string
		token        string
		wantNil      bool
		wantUsername string
	}{
		{
			// go-git spells "anonymous" as a nil AuthMethod, so a public repo needs no
			// special case at the call site.
			name:    "no token is anonymous",
			token:   "",
			wantNil: true,
		},
		{
			name:    "blank token is anonymous",
			token:   "   ",
			wantNil: true,
		},
		{
			name:         "token without a username defaults to oauth2",
			token:        "glpat-x",
			wantUsername: "oauth2",
		},
		{
			name:         "explicit username is honoured",
			username:     "bob",
			token:        "glpat-x",
			wantUsername: "bob",
		},
		{
			name:         "blank username falls back to the default",
			username:     "  ",
			token:        "glpat-x",
			wantUsername: "oauth2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := gitCloneAuth(tt.username, tt.token)
			if tt.wantNil {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			basic, ok := got.(*githttp.BasicAuth)
			require.True(t, ok, "expected basic auth, got %T", got)
			assert.Equal(t, tt.wantUsername, basic.Username)
			assert.Equal(t, "glpat-x", basic.Password)
		})
	}
}

// The clone failure message must not tell admins the repo has to be public now
// that a token can be supplied.
func TestErrDevcontainerClone_MentionsTheToken(t *testing.T) {
	t.Parallel()

	assert.NotContains(t, errDevcontainerClone.Error(), "without credentials")
	assert.Contains(t, errDevcontainerClone.Error(), "access token")
}

func TestDetectDevcontainer_IDEIsCarriedIntoTheTemplate(t *testing.T) {
	t.Parallel()

	got := postDevcontainerUpload(t, "devcontainer.json", []byte(`{"name":"Rust","image":"rust:1"}`), url.Values{
		"git_repo":   {"https://gitlab.com/org/workshop.git"},
		"cache_repo": {"registry.example.com/cache"},
		"ide":        {"code-server"},
	})

	// The IDE is injected onto a volume the build leaves alone, so it is the admin's
	// choice rather than anything devcontainer.json dictates — and the generated
	// template has to carry it, since nothing downstream can infer it.
	assert.Contains(t, got.TemplatesYAML, "ide: code-server")

	templates, err := parseWorkspaceTemplatesYAML(got.TemplatesYAML)
	require.NoError(t, err)
	require.Len(t, templates, 1)
	assert.Equal(t, "code-server", templates[0].IDE)
}

func TestDetectDevcontainer_TemplateNameIsChosenByTheAdmin(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		templateName string
		expected     string
	}{
		// Two devcontainer imports into one lab both carry the repo's own display
		// name, and duplicate names are rejected — so the admin's name has to win.
		{name: "overrides the devcontainer name", templateName: "day-two", expected: "name: day-two"},
		// The name reaches Kubernetes resource names, so what is typed is normalised
		// the same way a name read from the devcontainer is.
		{name: "is slugified", templateName: "Day Two!", expected: "name: day-two"},
		// An older client posts no name at all: the devcontainer's own name still
		// applies rather than the import failing.
		{name: "falls back to the devcontainer name", templateName: "", expected: "name: go-workshop"},
		// Nothing left to slugify falls through to the devcontainer name too.
		{name: "falls back when nothing survives slugification", templateName: "***", expected: "name: go-workshop"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			form := url.Values{
				"git_repo":   {"https://gitlab.com/org/workshop.git"},
				"cache_repo": {"registry.example.com/cache"},
			}
			if tt.templateName != "" {
				form.Set("template_name", tt.templateName)
			}

			got := postDevcontainerUpload(t, "devcontainer.json",
				[]byte(`{"name":"Go Workshop","image":"golang:1.22"}`), form)

			assert.Contains(t, got.TemplatesYAML, tt.expected)

			templates, err := parseWorkspaceTemplatesYAML(got.TemplatesYAML)
			require.NoError(t, err)
			require.Len(t, templates, 1)
			assert.Equal(t, tt.expected, "name: "+templates[0].Name)
		})
	}
}

func TestDetectDevcontainer_UnnamedDevcontainerKeepsTheFallbackName(t *testing.T) {
	t.Parallel()

	// A devcontainer with no name and no name from the admin still has to produce a
	// template: a name is required for the YAML to validate.
	got := postDevcontainerUpload(t, "devcontainer.json", []byte(`{"image":"golang:1.22"}`), url.Values{
		"git_repo":   {"https://gitlab.com/org/workshop.git"},
		"cache_repo": {"registry.example.com/cache"},
	})

	templates, err := parseWorkspaceTemplatesYAML(got.TemplatesYAML)
	require.NoError(t, err)
	require.Len(t, templates, 1)
	assert.Equal(t, fallbackDevcontainerTemplateName, templates[0].Name)
}

func TestDetectDevcontainer_NoIDESelectionStaysUnset(t *testing.T) {
	t.Parallel()

	got := postDevcontainerUpload(t, "devcontainer.json", []byte(`{"name":"Rust","image":"rust:1"}`), url.Values{
		"git_repo":   {"https://gitlab.com/org/workshop.git"},
		"cache_repo": {"registry.example.com/cache"},
	})

	// An older client that posts no ide must still round-trip: an empty value is
	// omitted from the YAML and resolves to the default IDE at the backend.
	templates, err := parseWorkspaceTemplatesYAML(got.TemplatesYAML)
	require.NoError(t, err)
	require.Len(t, templates, 1)
	assert.Empty(t, templates[0].IDE)
}
