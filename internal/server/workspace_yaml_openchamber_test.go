package server

import (
	"testing"

	"easylab/internal/providers/workspace"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseWorkspaceTemplatesYAML_OpenChamber covers the per-IDE rules: a key
// the IDE cannot honour is refused when the template is saved, where the admin
// can still fix it, rather than ignored and discovered mid-workshop.
func TestParseWorkspaceTemplatesYAML_OpenChamber(t *testing.T) {
	t.Parallel()

	const image = "    image: registry.example.com/openchamber:1\n"
	base := "workspace_templates:\n  - name: ai\n    ide: openchamber\n" + image

	tests := []struct {
		name      string
		yaml      string
		wantHome  string
		wantInErr string
	}{
		{
			name: "a valid openchamber template",
			yaml: base,
		},
		{
			name: "with a startup script and a secret mount",
			yaml: base + "    mounts:\n      - type: secret\n        name: ai-provider-key\n        path: /etc/easylab/ai\n" +
				"    startup_script: |\n      export ANTHROPIC_API_KEY=\"$(cat /etc/easylab/ai/key)\"\n",
		},
		{
			name:      "image is required, since OpenChamber publishes none",
			yaml:      "workspace_templates:\n  - name: ai\n    ide: openchamber\n",
			wantInErr: "requires image",
		},
		{
			name:      "extensions are a VS Code concept",
			yaml:      base + "    extensions:\n      - golang.go\n",
			wantInErr: "extensions",
		},
		{
			name:      "vscode_settings are a VS Code concept",
			yaml:      base + "    vscode_settings:\n      editor.fontSize: 14\n",
			wantInErr: "vscode_settings",
		},
		{
			name:      "devcontainer builds need an injectable bundle",
			yaml:      base + "    git_repo: https://example.com/r.git\n    devcontainer:\n      enabled: true\n      cache_repo: r.example.com/c\n",
			wantInErr: "devcontainer",
		},
		{
			name:      "git_folder needs an IDE that opens a folder",
			yaml:      base + "    git_repo: https://example.com/r.git\n    git_folder: exercises\n",
			wantInErr: "git_folder",
		},
		{
			name:      "the password variable belongs to EasyLab",
			yaml:      base + "    env:\n      OPENCHAMBER_UI_PASSWORD: hunter2\n",
			wantInErr: "OPENCHAMBER_UI_PASSWORD",
		},
		{
			// A template's env would win over the generated secret, so every
			// workspace built from it would sign interchangeable sessions.
			name:      "the session signing secret belongs to EasyLab",
			yaml:      base + "    env:\n      OPENCODE_JWT_SECRET: shared-across-students\n",
			wantInErr: "OPENCODE_JWT_SECRET",
		},
		{
			name:      "the session signing secret is refused whatever its case",
			yaml:      base + "    env:\n      opencode_jwt_secret: shared-across-students\n",
			wantInErr: "OPENCODE_JWT_SECRET",
		},
		{
			name:      "an unknown ide is still refused, and names both values",
			yaml:      "workspace_templates:\n  - name: ai\n    ide: emacs\n",
			wantInErr: "openchamber",
		},
		{
			name:     "an image on another base names its own home",
			yaml:     base + "    home_dir: /home/node\n",
			wantHome: "/home/node",
		},
		{
			name:      "a relative home_dir cannot be a mount path",
			yaml:      base + "    home_dir: home/node\n",
			wantInErr: "absolute",
		},
		{
			name:      "an unclean home_dir is refused with the path meant",
			yaml:      base + "    home_dir: /home/node/\n",
			wantInErr: "/home/node",
		},
		{
			name:      "the volume cannot be mounted over the root filesystem",
			yaml:      base + "    home_dir: /\n",
			wantInErr: "home_dir cannot be /",
		},
		{
			name:      "home_dir is dead config on an ide whose image we control",
			yaml:      "workspace_templates:\n  - name: dev\n    ide: code-server\n    home_dir: /home/node\n",
			wantInErr: "home_dir only applies to",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseWorkspaceTemplatesYAML(tt.yaml)

			if tt.wantInErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantInErr)
				return
			}
			require.NoError(t, err)
			require.Len(t, got, 1)
			assert.Equal(t, workspace.IDEOpenChamber, got[0].IDE)
			assert.Equal(t, tt.wantHome, got[0].HomeDir)
		})
	}
}

// TestValidateIDEFeatures_CodeServerUnaffected pins that the new per-IDE rules
// are additive: a code-server template may keep using every key it always has.
func TestValidateIDEFeatures_CodeServerUnaffected(t *testing.T) {
	t.Parallel()

	for _, ide := range []string{"", workspace.IDECodeServer, workspace.IDEOpenVSCode} {
		t.Run("ide="+ide, func(t *testing.T) {
			t.Parallel()

			err := validateIDEFeatures("template \"t\"", WorkspaceTemplate{
				Name:           "t",
				IDE:            ide,
				Extensions:     []string{"golang.go"},
				VSCodeSettings: map[string]any{"editor.fontSize": 14},
				GitFolder:      "exercises",
				Env:            map[string]string{"OPENCHAMBER_UI_PASSWORD": "irrelevant here"},
			})

			assert.NoError(t, err)

			// home_dir is the exception: it is refused rather than ignored for
			// these IDEs, whose image — and so whose home — is not the lab's.
			assert.Error(t, validateIDEFeatures("template \"t\"", WorkspaceTemplate{Name: "t", IDE: ide, HomeDir: "/home/node"}))
		})
	}
}

func TestProviderKeyWarnings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		env      map[string]string
		wantKeys []string
	}{
		{
			name:     "an API key is flagged",
			env:      map[string]string{"ANTHROPIC_API_KEY": "sk-test"},
			wantKeys: []string{"ANTHROPIC_API_KEY"},
		},
		{
			name:     "several are flagged in a stable order",
			env:      map[string]string{"OPENAI_API_KEY": "sk-openai-secret", "GITHUB_TOKEN": "ghp-secret", "LANG": "C.UTF-8"},
			wantKeys: []string{"GITHUB_TOKEN", "OPENAI_API_KEY"},
		},
		{
			name: "ordinary configuration is not",
			env:  map[string]string{"LANG": "C.UTF-8", "EDITOR": "vim", "TZ": "Europe/Paris"},
		},
		{
			name: "no env at all",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := providerKeyWarnings([]WorkspaceTemplate{{Name: "ai", Env: tt.env}})

			require.Len(t, got, len(tt.wantKeys))
			for i, key := range tt.wantKeys {
				assert.Equal(t, "env", got[i].Key)
				assert.Contains(t, got[i].Message, key)
				// The warning is about the exposure, so it must not quote the value.
				assert.NotContains(t, got[i].Message, tt.env[key])
			}
		})
	}
}
