package devcontainer

import (
	"archive/zip"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStripJSONC(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "plain json is untouched",
			input:    `{"image":"golang:1.22"}`,
			expected: `{"image":"golang:1.22"}`,
		},
		{
			name:     "line comment is removed",
			input:    "{\n// pick a base\n\"image\":\"golang\"\n}",
			expected: "{\n\n\"image\":\"golang\"\n}",
		},
		{
			name:     "trailing line comment is removed",
			input:    "{\n\"image\":\"golang\" // the base\n}",
			expected: "{\n\"image\":\"golang\" \n}",
		},
		{
			name:     "block comment is removed",
			input:    `{/* a comment */"image":"golang"}`,
			expected: `{"image":"golang"}`,
		},
		{
			name:     "multiline block comment is removed",
			input:    "{\n/* one\n   two */\n\"image\":\"golang\"\n}",
			expected: "{\n\n\"image\":\"golang\"\n}",
		},
		{
			name:     "url in a string survives",
			input:    `{"git":"https://example.com/x.git"}`,
			expected: `{"git":"https://example.com/x.git"}`,
		},
		{
			name:     "comment markers inside a string survive",
			input:    `{"a":"// not a comment","b":"/* nor this */"}`,
			expected: `{"a":"// not a comment","b":"/* nor this */"}`,
		},
		{
			name:     "escaped quote does not end the string",
			input:    `{"a":"he said \"// hi\"","b":1}`,
			expected: `{"a":"he said \"// hi\"","b":1}`,
		},
		{
			name:     "escaped backslash at string end still ends the string",
			input:    `{"a":"c:\\","b":2}`,
			expected: `{"a":"c:\\","b":2}`,
		},
		{
			name:     "trailing comma in object is removed",
			input:    `{"a":1,}`,
			expected: `{"a":1}`,
		},
		{
			name:     "trailing comma in array is removed",
			input:    `{"a":[1,2,]}`,
			expected: `{"a":[1,2]}`,
		},
		{
			name:     "trailing comma with whitespace is removed",
			input:    "{\"a\":1,\n  }",
			expected: "{\"a\":1\n  }",
		},
		{
			name:     "separating commas are kept",
			input:    `{"a":1,"b":2}`,
			expected: `{"a":1,"b":2}`,
		},
		{
			name:     "comma inside a string is kept",
			input:    `{"a":"x,y"}`,
			expected: `{"a":"x,y"}`,
		},
		{
			name:     "comment then trailing comma",
			input:    "{\n\"a\":1, // note\n}",
			expected: "{\n\"a\":1 \n}",
		},
		{
			name:     "unterminated block comment does not panic",
			input:    `{"a":1} /* dangling`,
			expected: `{"a":1} `,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, string(StripJSONC([]byte(tt.input))))
		})
	}
}

// TestStripJSONC_ProducesValidJSON is the property that actually matters: what
// comes out must parse.
func TestStripJSONC_ProducesValidJSON(t *testing.T) {
	t.Parallel()

	src := `{
	// The base image for the workshop.
	"name": "Go Workshop",
	"image": "mcr.microsoft.com/devcontainers/go:1.22", // pinned
	/* Extensions are installed by EasyLab,
	   not by envbuilder. */
	"customizations": {
		"vscode": {
			"extensions": [
				"golang.go",
			],
		},
	},
}`

	var out map[string]any
	require.NoError(t, json.Unmarshal(StripJSONC([]byte(src)), &out))
	assert.Equal(t, "Go Workshop", out["name"])
}

func TestParse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		input  string
		assert func(t *testing.T, cfg *Config)
	}{
		{
			name:  "image based",
			input: `{"name":"Go","image":"golang:1.22"}`,
			assert: func(t *testing.T, cfg *Config) {
				assert.Equal(t, "Go", cfg.Name)
				assert.Equal(t, "golang:1.22", cfg.Image)
			},
		},
		{
			name:  "build based",
			input: `{"build":{"dockerfile":"Dockerfile","context":".."}}`,
			assert: func(t *testing.T, cfg *Config) {
				require.NotNil(t, cfg.Build)
				assert.Equal(t, "Dockerfile", cfg.Build.Dockerfile)
				assert.Equal(t, "..", cfg.Build.Context)
			},
		},
		{
			name:  "legacy root dockerFile",
			input: `{"dockerFile":"Dockerfile"}`,
			assert: func(t *testing.T, cfg *Config) {
				assert.Equal(t, "Dockerfile", cfg.LegacyDockerfile)
			},
		},
		{
			name:  "features",
			input: `{"features":{"ghcr.io/devcontainers/features/docker-in-docker:2":{}}}`,
			assert: func(t *testing.T, cfg *Config) {
				assert.Len(t, cfg.Features, 1)
				assert.Contains(t, cfg.Features, "ghcr.io/devcontainers/features/docker-in-docker:2")
			},
		},
		{
			name:  "extensions",
			input: `{"customizations":{"vscode":{"extensions":["golang.go","ms-python.python"]}}}`,
			assert: func(t *testing.T, cfg *Config) {
				require.NotNil(t, cfg.Customizations)
				require.NotNil(t, cfg.Customizations.VSCode)
				assert.Equal(t, []string{"golang.go", "ms-python.python"}, cfg.Customizations.VSCode.Extensions)
			},
		},
		{
			name:  "unknown keys are ignored",
			input: `{"image":"golang","somethingNew":{"a":1},"postCreateCommand":"go mod download"}`,
			assert: func(t *testing.T, cfg *Config) {
				assert.Equal(t, "golang", cfg.Image)
			},
		},
		{
			name:  "jsonc with comments and trailing commas",
			input: "{\n// base\n\"image\":\"golang\",\n}",
			assert: func(t *testing.T, cfg *Config) {
				assert.Equal(t, "golang", cfg.Image)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := Parse([]byte(tt.input))
			require.NoError(t, err)
			tt.assert(t, cfg)
		})
	}
}

func TestParse_Invalid(t *testing.T) {
	t.Parallel()

	_, err := Parse([]byte(`{"image": }`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse devcontainer.json")
}

func TestFlexString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "number", input: `{"cpus":4}`, expected: "4"},
		{name: "string", input: `{"cpus":"2"}`, expected: "2"},
		{name: "float keeps literal form", input: `{"cpus":1.5}`, expected: "1.5"},
		{name: "absent", input: `{}`, expected: ""},
		{name: "null", input: `{"cpus":null}`, expected: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var hr HostRequirements
			require.NoError(t, json.Unmarshal([]byte(tt.input), &hr))
			assert.Equal(t, tt.expected, string(hr.CPUs))
		})
	}
}

func TestStringOrSlice(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{name: "single string", input: `{"dockerComposeFile":"compose.yml"}`, expected: []string{"compose.yml"}},
		{name: "list", input: `{"dockerComposeFile":["a.yml","b.yml"]}`, expected: []string{"a.yml", "b.yml"}},
		{name: "absent", input: `{}`, expected: nil},
		{name: "null", input: `{"dockerComposeFile":null}`, expected: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var cfg Config
			require.NoError(t, json.Unmarshal([]byte(tt.input), &cfg))
			assert.Equal(t, tt.expected, []string(cfg.DockerComposeFile))
		})
	}
}

func TestToK8sQuantity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "gb", input: "4gb", expected: "4Gi"},
		{name: "mb", input: "512mb", expected: "512Mi"},
		{name: "tb", input: "1tb", expected: "1Ti"},
		{name: "kb", input: "256kb", expected: "256Ki"},
		{name: "uppercase", input: "8GB", expected: "8Gi"},
		{name: "spaced", input: "8 gb", expected: "8Gi"},
		{name: "fractional", input: "1.5gb", expected: "1.5Gi"},
		{name: "bare number", input: "4", expected: "4"},
		{name: "unparseable", input: "lots", expected: ""},
		{name: "empty", input: "", expected: ""},
		{name: "k8s style is not devcontainer style", input: "4Gi", expected: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, toK8sQuantity(tt.input))
		})
	}
}

func TestFindInDir(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		files    []string
		expected string
		wantErr  bool
	}{
		{
			name:     "canonical location",
			files:    []string{".devcontainer/devcontainer.json"},
			expected: ".devcontainer/devcontainer.json",
		},
		{
			name:     "root level",
			files:    []string{".devcontainer.json"},
			expected: ".devcontainer.json",
		},
		{
			name:     "canonical wins over root level",
			files:    []string{".devcontainer.json", ".devcontainer/devcontainer.json"},
			expected: ".devcontainer/devcontainer.json",
		},
		{
			name:     "nested variant",
			files:    []string{".devcontainer/go/devcontainer.json"},
			expected: ".devcontainer/go/devcontainer.json",
		},
		{
			name:     "lexically first nested variant wins",
			files:    []string{".devcontainer/zig/devcontainer.json", ".devcontainer/go/devcontainer.json"},
			expected: ".devcontainer/go/devcontainer.json",
		},
		{
			name:     "canonical wins over nested",
			files:    []string{".devcontainer/go/devcontainer.json", ".devcontainer/devcontainer.json"},
			expected: ".devcontainer/devcontainer.json",
		},
		{
			name:    "nothing to find",
			files:   []string{"README.md", "main.go"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			for _, f := range tt.files {
				p := filepath.Join(root, f)
				require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
				require.NoError(t, os.WriteFile(p, []byte(`{"image":"golang"}`), 0o644))
			}

			got, err := FindInDir(root)
			if tt.wantErr {
				require.ErrorIs(t, err, ErrNotFound)
				return
			}
			require.NoError(t, err)
			rel, relErr := filepath.Rel(root, got)
			require.NoError(t, relErr)
			assert.Equal(t, filepath.FromSlash(tt.expected), rel)
		})
	}
}

func TestParseFromFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	p := filepath.Join(root, "devcontainer.json")
	require.NoError(t, os.WriteFile(p, []byte("{\n// hi\n\"image\":\"golang\",\n}"), 0o644))

	cfg, err := ParseFromFile(p)
	require.NoError(t, err)
	assert.Equal(t, "golang", cfg.Image)

	_, err = ParseFromFile(filepath.Join(root, "missing.json"))
	require.Error(t, err)
}

func TestParseFromZip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		entries  map[string]string
		want     string
		wantPath string
		wantErr  error
	}{
		{
			name:     "canonical location",
			entries:  map[string]string{".devcontainer/devcontainer.json": `{"image":"golang"}`},
			want:     "golang",
			wantPath: ".devcontainer/devcontainer.json",
		},
		{
			name: "forge zip wraps in a top level directory",
			entries: map[string]string{
				"workshop-main/README.md":                       "hi",
				"workshop-main/.devcontainer/devcontainer.json": `{"image":"rust"}`,
			},
			want:     "rust",
			wantPath: "workshop-main/.devcontainer/devcontainer.json",
		},
		{
			name:     "root level fallback",
			entries:  map[string]string{"workshop-main/.devcontainer.json": `{"image":"node"}`},
			want:     "node",
			wantPath: "workshop-main/.devcontainer.json",
		},
		{
			name: "canonical wins over nested variant",
			entries: map[string]string{
				"repo/.devcontainer/go/devcontainer.json": `{"image":"wrong"}`,
				"repo/.devcontainer/devcontainer.json":    `{"image":"right"}`,
			},
			want:     "right",
			wantPath: "repo/.devcontainer/devcontainer.json",
		},
		{
			name:     "nested variant when there is no canonical",
			entries:  map[string]string{"repo/.devcontainer/go/devcontainer.json": `{"image":"go"}`},
			want:     "go",
			wantPath: "repo/.devcontainer/go/devcontainer.json",
		},
		{
			name:    "no devcontainer",
			entries: map[string]string{"repo/README.md": "hi"},
			wantErr: ErrNotFound,
		},
		{
			name:    "similarly named file is not a devcontainer",
			entries: map[string]string{"repo/my.devcontainer.json": `{"image":"nope"}`},
			wantErr: ErrNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			zipPath := writeZip(t, tt.entries)

			cfg, gotPath, err := ParseFromZip(zipPath)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, cfg.Image)
			// The path is reported so the admin can see which variant was picked.
			assert.Equal(t, tt.wantPath, gotPath)
		})
	}
}

func writeZip(t *testing.T, entries map[string]string) string {
	t.Helper()

	zipPath := filepath.Join(t.TempDir(), "repo.zip")
	f, err := os.Create(zipPath)
	require.NoError(t, err)
	defer f.Close()

	w := zip.NewWriter(f)
	// Sorted so the archive's byte order is stable and does not silently become
	// what picks the candidate.
	for _, name := range sortedKeys(entries) {
		e, err := w.Create(name)
		require.NoError(t, err)
		_, err = e.Write([]byte(entries[name]))
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
	return zipPath
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// Reverse-ish order is irrelevant; pickCandidate sorts internally.
	return out
}
