package server

import (
	"easylab/internal/providers/workspace"
	"encoding/json"
)

// WorkspacePreset is a ready-made starting point offered above the workspace
// template form. Picking one fills the form's fields client-side; nothing is
// submitted from here, so a preset is only ever a head start the admin then
// edits.
//
// Each preset mirrors a recipe in docs/templates.md so the cookbook and the form
// tell one story. workspace_presets_test.go holds every preset to the same
// validation the YAML editor applies, which is what stops a preset filling the
// form with a document the lab would then refuse.
type WorkspacePreset struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	// Icon is a single emoji, rendered as the card's glyph. It matches how the
	// mode buttons next to it already carry theirs.
	Icon string `json:"icon"`
	// Template is what the form is filled from. Only the fields the form builder
	// can express are set; a preset never reaches for a YAML-only key, since the
	// admin would not see it and a later form save would silently drop it.
	Template WorkspaceTemplate `json:"template"`
}

// TemplateJSON renders the preset's template for the card's data-preset
// attribute, which web/static/template-editor.js reads back when the card is
// clicked. Marshalling errors cannot happen for these literals; an empty object
// is returned rather than failing a page render if one ever does.
func (p WorkspacePreset) TemplateJSON() string {
	b, err := json.Marshal(p.Template)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// workspacePresets returns the starting points offered on the templates step and
// in the Add Template drawer, in the order they are shown: plainest first, most
// specialised last.
func workspacePresets() []WorkspacePreset {
	return []WorkspacePreset{
		{
			ID:          "blank",
			Title:       "Blank workspace",
			Description: "VS Code in the browser, nothing cloned. Good for a first lab.",
			Icon:        "\U0001F4BB", // 💻
			Template: WorkspaceTemplate{
				// The same name the lab falls back to when no template is defined
				// at all, and the one docs/templates.md's "Minimal" recipe uses.
				Name:        "default",
				Description: "A plain VS Code workspace.",
			},
		},
		{
			ID:          "git-workshop",
			Title:       "Git workshop",
			Description: "Clone your exercises repo and open a subfolder on first start.",
			Icon:        "\U0001F4DA", // 📚
			Template: WorkspaceTemplate{
				Name:        "workshop",
				Description: "The workshop exercises, already cloned.",
				GitRepo:     "https://gitlab.com/your-org/your-workshop.git",
				GitFolder:   "exercises",
				GitShallow:  true,
			},
		},
		{
			ID:          "workshop-database",
			Title:       "Workshop + database",
			Description: "Adds a PostgreSQL sidecar each student gets their own copy of.",
			Icon:        "\U0001F5C4", // 🗄
			Template: WorkspaceTemplate{
				Name:        "api-workshop",
				Description: "The exercises plus a private PostgreSQL on localhost:5432.",
				GitRepo:     "https://gitlab.com/your-org/your-workshop.git",
				GitShallow:  true,
				Env: map[string]string{
					"DATABASE_URL": "postgres://postgres:postgres@localhost:5432/app",
				},
				Sidecars: []WorkspaceSidecar{{
					Name:  "db",
					Image: "postgres:16",
					Ports: []int{5432},
					Env: map[string]string{
						"POSTGRES_PASSWORD": "postgres",
						"POSTGRES_DB":       "app",
					},
				}},
			},
		},
		{
			ID:          "docker",
			Title:       "Docker in a workspace",
			Description: "A Docker-in-Docker sidecar, for a lab about building images.",
			Icon:        "\U0001F433", // 🐳
			Template: WorkspaceTemplate{
				Name:        "docker-workshop",
				Description: "A workspace with its own Docker daemon.",
				Env: map[string]string{
					"DOCKER_HOST": "tcp://localhost:2375",
				},
				StartupScript: "sudo apt-get update && sudo apt-get install -y docker.io\nuntil docker info >/dev/null 2>&1; do sleep 2; done\n",
				Sidecars: []WorkspaceSidecar{{
					Name:       "docker",
					Image:      "docker:dind",
					Ports:      []int{2375},
					Privileged: true,
					Env: map[string]string{
						"DOCKER_TLS_CERTDIR": "",
					},
				}},
			},
		},
		{
			ID:          "ai-agent",
			Title:       "AI agent lab",
			Description: "OpenChamber instead of VS Code. You supply the image.",
			Icon:        "\U0001F916", // 🤖
			Template: WorkspaceTemplate{
				Name:        "agent-lab",
				Description: "An OpenChamber workspace for working with an AI coding agent.",
				IDE:         workspace.IDEOpenChamber,
				Image:       "registry.example.com/easylab/openchamber:1.11",
				CPU:         "1",
				Memory:      "4Gi",
			},
		},
	}
}
