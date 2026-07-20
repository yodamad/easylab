package server

import (
	"bytes"
	"easylab/internal/devcontainer"
	"easylab/internal/providers/workspace"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// workspaceTemplatesYAMLSkeleton seeds the YAML editor when there is nothing to
// seed it from: every supported key, commented out, as inline schema docs.
const workspaceTemplatesYAMLSkeleton = `# Workspace templates for this lab. Each entry is a workspace flavor students
# can pick on their dashboard. Only "name" is required — uncomment what you need.
workspace_templates:
  - name: default
    # ide: openvscode                            # openvscode (default) | code-server
    # image: gitpod/openvscode-server:latest     # overrides the IDE's default image
    # git_repo: https://gitlab.com/user/repo.git # cloned into the workspace on first start
    # git_branch: main
    # git_folder: exercises                      # subfolder opened in the IDE
    # git_auth_secret: gitcred                   # basic-auth Secret for a private git_repo (http(s) only)
    # image_pull_secrets:                        # dockerconfigjson Secrets the kubelet pulls images with
    #   - regcred
    # cpu: 500m
    # memory: 1Gi
    # disk_size: 5Gi
    # startup_script: |                          # runs (best-effort) before the IDE starts
    #   apt-get update && apt-get install -y jq
    # dotfiles_repo: https://github.com/you/dotfiles
    # extensions:                                # VS Code extension IDs or .vsix URLs
    #   - golang.go
    #   - ms-python.python
    # env:                                       # passed to the workspace container
    #   FOO: bar
    # sidecars:                                  # extra containers, reachable at localhost:<port>
    #   - name: db
    #     image: postgres:16
    #     ports: [5432]
    #     env:
    #       POSTGRES_PASSWORD: postgres
    #     privileged: false                      # needed by docker-in-docker; escalates to the node
    #     capabilities: [SYS_ADMIN]
    # mounts:                                    # ConfigMaps/Secrets already in the workspace namespace
    #   - type: configmap                        # configmap | secret
    #     name: my-config
    #     path: /etc/config
    # devcontainer:                              # build the image from the repo's devcontainer.json
    #   enabled: true                            # requires git_repo; conflicts with image
    #   dir: .devcontainer                       # folder holding devcontainer.json
    #   cache_repo: registry.example.com/cache   # REQUIRED — without it every student rebuilds
    #   registry_auth_secret: regcred            # existing dockerconfigjson Secret in the namespace
    #   fallback_image: gitpod/openvscode-server:latest
    #   insecure: false                          # bypass registry/git TLS verification
`

// workspaceTemplatesDoc is the YAML document admins edit. It carries no yaml
// tags on purpose: parsing and marshalling bridge through JSON so the existing
// json tags on WorkspaceTemplate are the single source of truth for the schema.
// A field added to WorkspaceTemplate is therefore editable in YAML immediately,
// under the same key it already has in the persisted job file.
type workspaceTemplatesDoc struct {
	WorkspaceTemplates []WorkspaceTemplate `json:"workspace_templates"`
}

// parseWorkspaceTemplatesYAML decodes the workspace templates document typed
// into the admin YAML editor. Unknown keys are rejected rather than ignored: a
// typo like "imagee" must fail in the editor, not silently ship a lab whose
// workspaces are missing their image.
func parseWorkspaceTemplatesYAML(s string) ([]WorkspaceTemplate, error) {
	if strings.TrimSpace(s) == "" {
		return nil, fmt.Errorf("the YAML is empty: define at least one workspace template")
	}

	var raw interface{}
	if err := yaml.Unmarshal([]byte(s), &raw); err != nil {
		return nil, fmt.Errorf("invalid YAML: %s", cleanYAMLError(err))
	}

	// Accept a bare list of templates as well as a full workspace_templates: mapping,
	// so pasting just the list works.
	if list, ok := raw.([]interface{}); ok {
		raw = map[string]interface{}{"workspace_templates": list}
	}

	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("unsupported YAML structure: %w", err)
	}

	dec := json.NewDecoder(bytes.NewReader(encoded))
	dec.DisallowUnknownFields()
	var doc workspaceTemplatesDoc
	if err := dec.Decode(&doc); err != nil {
		return nil, errors.New(cleanDecodeError(err))
	}

	if err := validateWorkspaceTemplates(doc.WorkspaceTemplates); err != nil {
		return nil, err
	}
	return doc.WorkspaceTemplates, nil
}

// marshalWorkspaceTemplatesYAML renders templates back to the editor's document
// shape, for seeding from the wizard and for exporting an existing lab.
func marshalWorkspaceTemplatesYAML(templates []WorkspaceTemplate) (string, error) {
	encoded, err := json.Marshal(workspaceTemplatesDoc{WorkspaceTemplates: templates})
	if err != nil {
		return "", fmt.Errorf("failed to encode workspace templates: %w", err)
	}

	dec := json.NewDecoder(bytes.NewReader(encoded))
	dec.UseNumber()
	node, err := jsonToYAMLNode(dec)
	if err != nil {
		return "", fmt.Errorf("failed to encode workspace templates: %w", err)
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(node); err != nil {
		return "", fmt.Errorf("failed to render YAML: %w", err)
	}
	if err := enc.Close(); err != nil {
		return "", fmt.Errorf("failed to render YAML: %w", err)
	}
	return buf.String(), nil
}

// jsonToYAMLNode converts JSON into a YAML node tree, keeping the key order
// json.Marshal emitted — which is struct field order, so "name" leads every
// template. Handing yaml.v3 a decoded map instead would sort keys alphabetically
// and bury the name in the middle of each entry.
func jsonToYAMLNode(dec *json.Decoder) (*yaml.Node, error) {
	token, err := dec.Token()
	if err != nil {
		return nil, err
	}

	delim, ok := token.(json.Delim)
	if !ok {
		return yamlScalarNode(token)
	}

	switch delim {
	case '{':
		node := &yaml.Node{Kind: yaml.MappingNode}
		for dec.More() {
			keyToken, err := dec.Token()
			if err != nil {
				return nil, err
			}
			key, err := yamlScalarNode(keyToken)
			if err != nil {
				return nil, err
			}
			value, err := jsonToYAMLNode(dec)
			if err != nil {
				return nil, err
			}
			node.Content = append(node.Content, key, value)
		}
		_, err := dec.Token() // closing brace
		return node, err
	case '[':
		node := &yaml.Node{Kind: yaml.SequenceNode}
		for dec.More() {
			value, err := jsonToYAMLNode(dec)
			if err != nil {
				return nil, err
			}
			node.Content = append(node.Content, value)
		}
		_, err := dec.Token() // closing bracket
		return node, err
	}
	return nil, fmt.Errorf("unexpected JSON delimiter %q", delim)
}

// yamlScalarNode leaves scalar rendering to yaml.v3, so strings that look like
// numbers stay quoted ("2") and multi-line scripts become literal blocks.
func yamlScalarNode(token json.Token) (*yaml.Node, error) {
	if token == nil {
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}, nil
	}

	value := token
	// UseNumber keeps JSON numbers exact, but json.Number is a string underneath
	// and would render quoted.
	if num, ok := token.(json.Number); ok {
		if i, err := num.Int64(); err == nil {
			value = i
		} else {
			f, err := num.Float64()
			if err != nil {
				return nil, fmt.Errorf("invalid number %q: %w", num.String(), err)
			}
			value = f
		}
	}

	node := &yaml.Node{}
	if err := node.Encode(value); err != nil {
		return nil, fmt.Errorf("failed to encode %v: %w", value, err)
	}
	return node, nil
}

// validateWorkspaceTemplates rejects templates that would deploy into something
// broken or surprising. It deliberately catches cases the runtime would
// otherwise swallow — an unknown mount type silently becomes a ConfigMap, and a
// mount missing its name or path is dropped from the pod.
func validateWorkspaceTemplates(templates []WorkspaceTemplate) error {
	if len(templates) == 0 {
		return fmt.Errorf("no workspace templates defined: workspace_templates must list at least one template")
	}

	seen := make(map[string]bool, len(templates))
	for i, t := range templates {
		where := fmt.Sprintf("template %d", i+1)
		if strings.TrimSpace(t.Name) == "" {
			return fmt.Errorf("%s: name is required", where)
		}
		where = fmt.Sprintf("template %q", t.Name)

		if seen[t.Name] {
			return fmt.Errorf("duplicate template name %q: names must be unique within a lab", t.Name)
		}
		seen[t.Name] = true

		if t.IDE != "" && t.IDE != workspace.IDEOpenVSCode && t.IDE != workspace.IDECodeServer {
			return fmt.Errorf("%s: ide must be %q or %q, got %q", where, workspace.IDEOpenVSCode, workspace.IDECodeServer, t.IDE)
		}
		if t.GitRepo != "" && !validateURL(t.GitRepo) {
			return fmt.Errorf("%s: git_repo %q is not a valid URL", where, t.GitRepo)
		}
		if t.DotfilesRepo != "" && !validateURL(t.DotfilesRepo) {
			return fmt.Errorf("%s: dotfiles_repo %q is not a valid URL", where, t.DotfilesRepo)
		}

		if err := validateSidecars(where, t.Sidecars); err != nil {
			return err
		}
		if err := validateMounts(where, t.Mounts); err != nil {
			return err
		}
		if err := validateAuthSecrets(where, t); err != nil {
			return err
		}
		if err := validateDevcontainer(where, t); err != nil {
			return err
		}
	}
	return nil
}

// validateAuthSecrets rejects credential references that would be silently
// ignored at the student's pod rather than fail visibly here.
func validateAuthSecrets(where string, t WorkspaceTemplate) error {
	for i, name := range t.ImagePullSecrets {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("%s: image_pull_secrets %d: name must not be empty", where, i+1)
		}
	}

	secret := strings.TrimSpace(t.GitAuthSecret)
	if secret == "" {
		return nil
	}
	if strings.TrimSpace(t.GitRepo) == "" {
		return fmt.Errorf("%s: git_auth_secret requires git_repo — there is nothing to authenticate to without a repository", where)
	}
	// The credentials are basic auth, which only an HTTP(S) remote sends. Over ssh
	// the secret would be mounted, ignored, and the clone would fail on the key
	// instead — a confusing way to learn the field did nothing.
	if u, err := url.Parse(strings.TrimSpace(t.GitRepo)); err == nil && u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%s: git_auth_secret needs an http(s) git_repo — %q uses %q, which does not authenticate with a username and password", where, t.GitRepo, u.Scheme)
	}
	return nil
}

// gitCredentialWarnings flags a token embedded in a repo URL. It warns rather
// than rejects: an inline credential works, and refusing it would break labs that
// already rely on it. What it costs is confidentiality — the URL is persisted to
// the job file, served by the jobs API and included in the templates export — so
// the admin is told, and decides.
func gitCredentialWarnings(templates []WorkspaceTemplate) []devcontainer.Warning {
	var out []devcontainer.Warning
	for _, t := range templates {
		where := strings.TrimSpace(t.Name)
		if where == "" {
			where = "template"
		}

		for key, raw := range map[string]string{"git_repo": t.GitRepo, "dotfiles_repo": t.DotfilesRepo} {
			if !urlEmbedsCredential(raw) {
				continue
			}
			out = append(out, devcontainer.Warning{
				Key: key,
				Message: fmt.Sprintf("%s: %s embeds a credential in the URL. It is stored in the lab's job file, returned by the jobs API and included in the templates export. Use git_auth_secret instead.",
					where, key),
			})
			// A token in the URL is what git uses, so the secret is dead config the
			// admin will otherwise believe is in force.
			if key == "git_repo" && strings.TrimSpace(t.GitAuthSecret) != "" {
				out = append(out, devcontainer.Warning{
					Key:     "git_auth_secret",
					Message: fmt.Sprintf("%s: git_auth_secret is ignored — the git_repo URL carries its own credential, which git uses in preference.", where),
				})
			}
		}
	}
	return out
}

// urlEmbedsCredential reports whether a git URL carries a secret in its userinfo.
// A bare username is not one: "ssh://git@host/repo.git" is the ordinary way to
// write an ssh remote, and warning about it would teach admins to ignore the
// warning that matters.
func urlEmbedsCredential(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.User == nil {
		return false
	}
	_, hasPassword := u.User.Password()
	return hasPassword
}

// validateDevcontainer rejects devcontainer templates that would fail at the
// student's pod rather than in the editor. Each rule here is a failure that
// otherwise only shows up minutes into a workshop.
func validateDevcontainer(where string, t WorkspaceTemplate) error {
	dc := t.Devcontainer
	if dc == nil || !dc.Enabled {
		return nil
	}
	if strings.TrimSpace(t.GitRepo) == "" {
		return fmt.Errorf("%s: devcontainer.enabled requires git_repo — the devcontainer.json is read from the workshop repository", where)
	}
	if strings.TrimSpace(t.Image) != "" {
		return fmt.Errorf("%s: devcontainer.enabled conflicts with image — the workspace image is built from the repository's devcontainer.json, so image would be ignored", where)
	}
	if strings.TrimSpace(dc.CacheRepo) == "" {
		return fmt.Errorf("%s: devcontainer.cache_repo is required — without a layer cache registry every student rebuilds the devcontainer from scratch on first start", where)
	}
	// The IDE is injected into the image envbuilder builds; only the OpenVSCode
	// bundle is relocatable enough for that today.
	if t.IDE != "" && t.IDE != workspace.IDEOpenVSCode {
		return fmt.Errorf("%s: devcontainer.enabled supports ide %q only, got %q", where, workspace.IDEOpenVSCode, t.IDE)
	}
	return nil
}

func validateSidecars(where string, sidecars []WorkspaceSidecar) error {
	for i, sc := range sidecars {
		if strings.TrimSpace(sc.Name) == "" {
			return fmt.Errorf("%s: sidecar %d: name is required", where, i+1)
		}
		if strings.TrimSpace(sc.Image) == "" {
			return fmt.Errorf("%s: sidecar %q: image is required", where, sc.Name)
		}
		for _, p := range sc.Ports {
			if p < 1 || p > 65535 {
				return fmt.Errorf("%s: sidecar %q: port %d is out of range (1-65535)", where, sc.Name, p)
			}
		}
	}
	return nil
}

func validateMounts(where string, mounts []WorkspaceMount) error {
	for i, m := range mounts {
		if strings.TrimSpace(m.Name) == "" {
			return fmt.Errorf("%s: mount %d: name is required", where, i+1)
		}
		if strings.TrimSpace(m.Path) == "" {
			return fmt.Errorf("%s: mount %q: path is required", where, m.Name)
		}
		// Empty means configmap, matching the workspace backend's default.
		switch strings.ToLower(strings.TrimSpace(m.Type)) {
		case "", "configmap", "secret":
		default:
			return fmt.Errorf("%s: mount %q: type must be \"configmap\" or \"secret\", got %q", where, m.Name, m.Type)
		}
	}
	return nil
}

// workspaceTemplatesFromRequest resolves a lab's workspace templates from
// whichever editor the admin was using. The YAML editor wins whenever it is the
// active mode: the wizard's inputs are only hidden, not removed, so preferring
// them here would silently deploy something other than what the admin sees.
func workspaceTemplatesFromRequest(r *http.Request) ([]WorkspaceTemplate, error) {
	if getFormValue(r, "templates_mode") != "yaml" {
		return parseWorkspaceTemplatesFromForm(r), nil
	}
	return parseWorkspaceTemplatesYAML(getFormValue(r, "templates_yaml"))
}

// filenameSafe strips anything that could break out of a Content-Disposition header.
var filenameSafe = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

// ServeWorkspaceTemplatesYAML backs the admin YAML editor.
//
//	GET  ?lab_id=<id>[&download=1]  exports an existing lab's templates, for cloning
//	POST (wizard form fields)       seeds the editor from what the wizard currently holds
func (h *Handler) ServeWorkspaceTemplatesYAML(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.exportWorkspaceTemplatesYAML(w, r)
	case http.MethodPost:
		h.seedWorkspaceTemplatesYAML(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// exportWorkspaceTemplatesYAML renders an existing lab's workspace templates.
// Only the templates are exported — a lab's credentials never leave the server
// through this route.
func (h *Handler) exportWorkspaceTemplatesYAML(w http.ResponseWriter, r *http.Request) {
	labID := r.URL.Query().Get("lab_id")
	if labID == "" {
		http.Error(w, "lab_id is required", http.StatusBadRequest)
		return
	}

	job, exists := h.jobManager.GetJob(labID)
	if !exists {
		http.Error(w, "Lab not found", http.StatusNotFound)
		return
	}

	job.mu.RLock()
	var templates []WorkspaceTemplate
	stackName := ""
	if job.Config != nil {
		templates = job.Config.GetWorkspaceTemplates()
		stackName = job.Config.StackName
	}
	job.mu.RUnlock()

	rendered, err := marshalWorkspaceTemplatesYAML(templates)
	if err != nil {
		log.Printf("Failed to render workspace templates YAML for lab %s: %v", labID, err)
		http.Error(w, "Failed to export workspace templates", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/x-yaml")
	if r.URL.Query().Get("download") == "1" {
		name := filenameSafe.ReplaceAllString(stackName, "")
		if name == "" {
			name = filenameSafe.ReplaceAllString(labID, "")
		}
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=workspace-templates-%s.yaml", name))
	}
	fmt.Fprint(w, rendered)
}

// seedWorkspaceTemplatesYAML turns the wizard's current fields into YAML, so
// switching to the editor never starts from a blank page. With nothing filled in
// yet the admin gets the commented skeleton instead.
func (h *Handler) seedWorkspaceTemplatesYAML(w http.ResponseWriter, r *http.Request) {
	if err := h.parseForm(w, r, 0); err != nil {
		log.Printf("Failed to parse form while seeding templates YAML: %v", err)
		http.Error(w, "Failed to read the form", http.StatusBadRequest)
		return
	}

	templates := parseWorkspaceTemplatesFromForm(r)
	w.Header().Set("Content-Type", "application/x-yaml")
	if len(templates) == 0 {
		fmt.Fprint(w, workspaceTemplatesYAMLSkeleton)
		return
	}

	rendered, err := marshalWorkspaceTemplatesYAML(templates)
	if err != nil {
		log.Printf("Failed to render workspace templates YAML from form: %v", err)
		http.Error(w, "Failed to build YAML from the form", http.StatusInternalServerError)
		return
	}
	fmt.Fprint(w, rendered)
}

// ValidateWorkspaceTemplatesYAML checks the editor's contents without creating a
// lab, so admins can find their typos before a deployment does.
func (h *Handler) ValidateWorkspaceTemplatesYAML(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := h.parseForm(w, r, 0); err != nil {
		log.Printf("Failed to parse form while validating templates YAML: %v", err)
		writeToast(w, false, "Could not read the submitted YAML.")
		return
	}

	templates, err := parseWorkspaceTemplatesYAML(getFormValue(r, "templates_yaml"))
	if err != nil {
		// This error describes the admin's own YAML rather than server internals,
		// so surfacing it is the whole point of the endpoint.
		writeToast(w, false, template.HTMLEscapeString(err.Error()))
		return
	}

	names := make([]string, 0, len(templates))
	for _, t := range templates {
		names = append(names, t.Name)
	}
	msg := fmt.Sprintf("Valid — %d template(s): %s", len(templates), strings.Join(names, ", "))

	// Warnings ride the success toast: these are things that work but cost the
	// admin something they would not otherwise be told about.
	for _, warn := range gitCredentialWarnings(templates) {
		msg += " — " + warn.Message
	}
	writeToast(w, true, template.HTMLEscapeString(msg))
}

// cleanYAMLError flattens yaml.v3's multi-line errors into one line an admin can
// read in a toast.
func cleanYAMLError(err error) string {
	msg := strings.TrimPrefix(err.Error(), "yaml: ")
	msg = strings.TrimPrefix(msg, "unmarshal errors:\n")
	fields := strings.Split(msg, "\n")
	for i, f := range fields {
		fields[i] = strings.TrimSpace(f)
	}
	return strings.Join(fields, "; ")
}

// cleanDecodeError rewords the JSON decoder's struct-oriented errors in terms of
// the YAML keys the admin actually typed.
func cleanDecodeError(err error) string {
	msg := strings.TrimPrefix(err.Error(), "json: ")
	for _, prefix := range []string{
		"Go struct field workspaceTemplatesDoc.",
		"Go struct field WorkspaceTemplate.",
		"Go struct field WorkspaceSidecar.",
		"Go struct field WorkspaceMount.",
	} {
		msg = strings.ReplaceAll(msg, prefix, "")
	}
	msg = strings.ReplaceAll(msg, "Go value of type server.workspaceTemplatesDoc", "a workspace_templates: list")
	return msg
}
