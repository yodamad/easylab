package devcontainer

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTranslate_Compose(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
	}{
		{name: "single compose file", input: `{"dockerComposeFile":"docker-compose.yml","service":"app"}`},
		{name: "list of compose files", input: `{"dockerComposeFile":["base.yml","dev.yml"],"service":"app"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := Parse([]byte(tt.input))
			require.NoError(t, err)

			_, err = Translate(cfg)
			require.ErrorIs(t, err, ErrComposeUnsupported)
			// The admin needs to know which key sank the import.
			assert.Contains(t, err.Error(), "dockerComposeFile")
		})
	}
}

func TestTranslate_Base(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected Base
	}{
		{
			name:     "image",
			input:    `{"image":"mcr.microsoft.com/devcontainers/go:1.22"}`,
			expected: Base{Kind: BaseImage, Image: "mcr.microsoft.com/devcontainers/go:1.22"},
		},
		{
			name:     "dockerfile",
			input:    `{"build":{"dockerfile":"Dockerfile"}}`,
			expected: Base{Kind: BaseDockerfile, Dockerfile: "Dockerfile"},
		},
		{
			name:     "legacy dockerFile spelling",
			input:    `{"dockerFile":"Dockerfile"}`,
			expected: Base{Kind: BaseDockerfile, Dockerfile: "Dockerfile"},
		},
		{
			name:     "dockerfile wins over image",
			input:    `{"image":"golang","build":{"dockerfile":"Dockerfile"}}`,
			expected: Base{Kind: BaseDockerfile, Dockerfile: "Dockerfile"},
		},
		{
			name:     "features only, no base",
			input:    `{"features":{"ghcr.io/devcontainers/features/go:1":{}}}`,
			expected: Base{Kind: BaseNone},
		},
		{
			name:     "empty",
			input:    `{}`,
			expected: Base{Kind: BaseNone},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := Parse([]byte(tt.input))
			require.NoError(t, err)

			res, err := Translate(cfg)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, res.Base)
		})
	}
}

func TestTranslate_MapsWhatEnvbuilderIgnores(t *testing.T) {
	t.Parallel()

	src := `{
		"name": "Go Workshop!",
		"image": "mcr.microsoft.com/devcontainers/go:1.22",
		"workspaceFolder": "exercises",
		"hostRequirements": {"cpus": 4, "memory": "8gb", "storage": "32gb"},
		"customizations": {"vscode": {"extensions": ["golang.go", "ms-python.python"]}},
		"features": {
			"ghcr.io/devcontainers/features/node:1": {},
			"ghcr.io/devcontainers/features/docker-in-docker:2": {}
		}
	}`

	cfg, err := Parse([]byte(src))
	require.NoError(t, err)

	res, err := Translate(cfg)
	require.NoError(t, err)

	assert.Equal(t, "go-workshop", res.Name)
	assert.Equal(t, "4", res.CPU)
	assert.Equal(t, "8Gi", res.Memory)
	assert.Equal(t, "32Gi", res.DiskSize)
	assert.Equal(t, "exercises", res.GitFolder)
	assert.Equal(t, []string{"golang.go", "ms-python.python"}, res.Extensions)
	// Features are reported, not copied: envbuilder reads them from the repo.
	assert.Equal(t, []string{
		"ghcr.io/devcontainers/features/docker-in-docker:2",
		"ghcr.io/devcontainers/features/node:1",
	}, res.Features)
	// A clean devcontainer imports silently.
	assert.Empty(t, res.Warnings)
}

func TestTranslate_WorkspaceFolder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		input       string
		wantFolder  string
		wantWarning bool
	}{
		{
			name:       "relative folder carries across",
			input:      `{"image":"golang","workspaceFolder":"exercises"}`,
			wantFolder: "exercises",
		},
		{
			name:       "dot slash prefix is trimmed",
			input:      `{"image":"golang","workspaceFolder":"./exercises"}`,
			wantFolder: "exercises",
		},
		{
			// An absolute path assumes a layout EasyLab does not reproduce, so it
			// must not be guessed into git_folder.
			name:        "absolute folder warns instead of guessing",
			input:       `{"image":"golang","workspaceFolder":"/workspaces/repo/src"}`,
			wantFolder:  "",
			wantWarning: true,
		},
		{
			name:       "absent",
			input:      `{"image":"golang"}`,
			wantFolder: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := Parse([]byte(tt.input))
			require.NoError(t, err)

			res, err := Translate(cfg)
			require.NoError(t, err)

			assert.Equal(t, tt.wantFolder, res.GitFolder)
			if tt.wantWarning {
				assert.Contains(t, warningKeys(res.Warnings), "workspaceFolder")
			} else {
				assert.NotContains(t, warningKeys(res.Warnings), "workspaceFolder")
			}
		})
	}
}

func TestTranslate_Warnings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		input       string
		wantKey     string
		wantMessage string
	}{
		{
			name:    "forwardPorts",
			input:   `{"image":"golang","forwardPorts":[3000,"8080:8080"]}`,
			wantKey: "forwardPorts",
		},
		{
			name:    "mounts as strings",
			input:   `{"image":"golang","mounts":["source=x,target=/x,type=bind"]}`,
			wantKey: "mounts",
		},
		{
			name:    "mounts as objects",
			input:   `{"image":"golang","mounts":[{"source":"x","target":"/x","type":"bind"}]}`,
			wantKey: "mounts",
		},
		{
			name:    "workspaceMount",
			input:   `{"image":"golang","workspaceMount":"source=.,target=/w,type=bind"}`,
			wantKey: "workspaceMount",
		},
		{
			name:    "privileged",
			input:   `{"image":"golang","privileged":true}`,
			wantKey: "privileged",
		},
		{
			name:    "capAdd",
			input:   `{"image":"golang","capAdd":["SYS_PTRACE"]}`,
			wantKey: "capAdd",
		},
		{
			name:    "init",
			input:   `{"image":"golang","init":true}`,
			wantKey: "init",
		},
		{
			name:    "remoteUser",
			input:   `{"image":"golang","remoteUser":"vscode"}`,
			wantKey: "remoteUser",
		},
		{
			name:    "postStartCommand does not run",
			input:   `{"image":"golang","postStartCommand":"echo started"}`,
			wantKey: "postStartCommand",
		},
		{
			name:    "unparseable memory",
			input:   `{"image":"golang","hostRequirements":{"memory":"heaps"}}`,
			wantKey: "hostRequirements.memory",
		},
		{
			name:    "unparseable storage",
			input:   `{"image":"golang","hostRequirements":{"storage":"loads"}}`,
			wantKey: "hostRequirements.storage",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := Parse([]byte(tt.input))
			require.NoError(t, err)

			res, err := Translate(cfg)
			require.NoError(t, err)

			require.NotEmpty(t, res.Warnings, "expected a warning for %s", tt.wantKey)
			assert.Contains(t, warningKeys(res.Warnings), tt.wantKey)
		})
	}
}

// TestTranslate_SupportedKeysDoNotWarn guards against warning fatigue: keys
// envbuilder handles itself must stay silent, or admins learn to ignore the list.
func TestTranslate_SupportedKeysDoNotWarn(t *testing.T) {
	t.Parallel()

	src := `{
		"image": "golang:1.22",
		"containerEnv": {"FOO": "bar"},
		"remoteEnv": {"BAZ": "qux"},
		"postCreateCommand": "go mod download",
		"onCreateCommand": ["echo", "hi"],
		"updateContentCommand": {"deps": "go mod download"},
		"build": {"args": {"VERSION": "1.22"}}
	}`

	cfg, err := Parse([]byte(src))
	require.NoError(t, err)

	res, err := Translate(cfg)
	require.NoError(t, err)
	assert.Empty(t, res.Warnings)
}

func TestSlugify(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "simple", input: "Go Workshop", expected: "go-workshop"},
		{name: "punctuation", input: "Go Workshop!", expected: "go-workshop"},
		{name: "already a slug", input: "go-workshop", expected: "go-workshop"},
		{name: "leading and trailing junk", input: "  --Go--  ", expected: "go"},
		{name: "dots and slashes", input: "node.js / react", expected: "node-js-react"},
		{name: "empty", input: "", expected: ""},
		{name: "only punctuation", input: "!!!", expected: ""},
		{
			name:     "long names are truncated without a trailing dash",
			input:    "a very long workshop name that goes on and on and on forever",
			expected: "a-very-long-workshop-name-that-goes-on-a",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := slugify(tt.input)
			assert.Equal(t, tt.expected, got)
			assert.LessOrEqual(t, len(got), 40)
		})
	}
}

func warningKeys(ws []Warning) []string {
	out := make([]string, 0, len(ws))
	for _, w := range ws {
		out = append(out, w.Key)
	}
	return out
}
