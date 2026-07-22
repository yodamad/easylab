package server

import (
	"easylab/internal/devcontainer"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

// defaultDevcontainerDir is where the Dev Container spec puts devcontainer.json.
const defaultDevcontainerDir = ".devcontainer"

// fallbackDevcontainerTemplateName is used when the devcontainer has no name to
// slugify — a template must always have one.
const fallbackDevcontainerTemplateName = "workshop"

// clientError marks an error whose message was written for the admin reading the
// import dialog and carries no internal detail, so it is safe to return in an
// HTTP response. Anything not marked is treated as internal: logged in full and
// replaced with a generic message. Marking is opt-in by construction rather than
// a list of known-safe errors, so a new error added here cannot leak by default.
type clientError struct{ msg string }

func (e clientError) Error() string { return e.msg }

func clientErrorf(format string, a ...any) error {
	return clientError{msg: fmt.Sprintf(format, a...)}
}

// Errors surfaced to the admin. The underlying cause is logged server-side. A
// clone failure in particular must never reach the client, since a repo URL can
// carry credentials.
var (
	errDevcontainerClone = clientError{"could not clone the repository — check the URL and branch, and the access token if the repository is private"}
	errDevcontainerRead  = clientError{"could not read the uploaded file"}
)

// devcontainerImportResponse is what the admin's import dialog renders.
type devcontainerImportResponse struct {
	// Path is the devcontainer.json that was used, relative to the repo root.
	// Reported because a repo may offer several variants and only one is picked.
	Path string `json:"path"`
	// Base and Features describe what envbuilder will build. They are
	// informational: envbuilder reads them from the repo itself at build time, so
	// they are not copied into the template.
	Base     devcontainer.Base      `json:"base"`
	Features []string               `json:"features,omitempty"`
	Warnings []devcontainer.Warning `json:"warnings,omitempty"`
	// TemplatesYAML seeds the templates editor, rendered by the same marshaller
	// the wizard's seed path uses so the document looks identical either way.
	TemplatesYAML string `json:"templates_yaml"`
}

// DetectDevcontainer reads a workshop's devcontainer.json — by cloning the repo
// or from an upload — and turns it into a workspace template the admin can
// review and edit before it is saved.
//
// The translation is deliberately done here, at authoring time, rather than at
// the student's first workspace request: it makes what a student will get
// visible and editable, and it surfaces the keys envbuilder cannot honour while
// the admin is still in the editor.
func (h *Handler) DetectDevcontainer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var (
		cfg  *devcontainer.Config
		path string
		err  error
	)

	switch getFormValue(r, "source") {
	case "upload":
		cfg, path, err = h.detectDevcontainerFromUpload(r)
	case "git":
		cfg, path, err = h.detectDevcontainerFromGit(r)
	default:
		writeJSONError(w, http.StatusBadRequest, "invalid source: must be 'upload' or 'git'")
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusUnprocessableEntity, devcontainerClientError(err))
		return
	}

	result, err := devcontainer.Translate(cfg)
	if err != nil {
		// Compose lands here. It is a hard stop rather than a warning: envbuilder
		// builds an image and cannot orchestrate compose services, so there is no
		// degraded mode to offer.
		writeJSONError(w, http.StatusUnprocessableEntity, devcontainerClientError(err))
		return
	}

	tmpl := devcontainerTemplate(result, r)
	warnings := append(result.Warnings, cacheRepoWarnings(tmpl)...)
	warnings = append(warnings, gitCredentialWarnings([]WorkspaceTemplate{tmpl})...)

	yamlDoc, err := marshalWorkspaceTemplatesYAML([]WorkspaceTemplate{tmpl})
	if err != nil {
		log.Printf("Failed to render devcontainer template YAML: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "failed to render the workspace template")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(devcontainerImportResponse{
		Path:          path,
		Base:          result.Base,
		Features:      result.Features,
		Warnings:      warnings,
		TemplatesYAML: yamlDoc,
	}); err != nil {
		log.Printf("Failed to encode devcontainer import response: %v", err)
	}
}

// devcontainerClientError maps an import failure onto a message that is safe to
// return. Everything unrecognised collapses to a generic message — the detail is
// already in the server log.
func devcontainerClientError(err error) string {
	if ce, ok := errors.AsType[clientError](err); ok {
		return ce.Error()
	}

	switch {
	case errors.Is(err, devcontainer.ErrNotFound):
		return "no devcontainer.json found — looked for .devcontainer/devcontainer.json, .devcontainer.json, and .devcontainer/*/devcontainer.json"
	case errors.Is(err, devcontainer.ErrComposeUnsupported):
		// Names the offending key so the admin knows why the import stopped.
		return err.Error()
	case errors.Is(err, devcontainer.ErrParse):
		// Describes the admin's own file, and is what tells them where to look.
		return err.Error()
	}

	log.Printf("Devcontainer import failed: %v", err)
	return "could not read the devcontainer configuration"
}

// devcontainerTemplate builds the workspace template suggested to the admin.
//
// It carries only what envbuilder will not do itself: the repo to build from,
// the sizing and extensions envbuilder ignores, and the cache settings. The
// image, Dockerfile and features stay in the repo, where envbuilder reads them.
func devcontainerTemplate(res devcontainer.Result, r *http.Request) WorkspaceTemplate {
	// The admin's own name wins over the devcontainer's. A devcontainer's "name" is
	// a display string, often left at whatever the repo was scaffolded with, so
	// deriving the template name from it alone gives every import in a lab the same
	// name — and names must be unique within a lab.
	name := devcontainer.Slugify(getFormValue(r, "template_name"))
	if name == "" {
		name = res.Name
	}
	if name == "" {
		name = fallbackDevcontainerTemplateName
	}

	dir := strings.TrimSpace(getFormValue(r, "devcontainer_dir"))
	if dir == "" {
		dir = defaultDevcontainerDir
	}

	return WorkspaceTemplate{
		Name: name,
		// The IDE is injected onto a volume the build leaves alone, so it is the
		// admin's choice rather than anything the devcontainer.json dictates.
		IDE:        strings.TrimSpace(getFormValue(r, "ide")),
		GitRepo:    strings.TrimSpace(getFormValue(r, "git_repo")),
		GitBranch:  strings.TrimSpace(getFormValue(r, "git_branch")),
		GitFolder:  res.GitFolder,
		CPU:        res.CPU,
		Memory:     res.Memory,
		DiskSize:   res.DiskSize,
		Extensions: res.Extensions,
		// The credential the students' workspaces clone a private repo with —
		// distinct from the request-scoped token that read the devcontainer here (see
		// gitCloneAuth). Baked into the generated template so a private-repo workshop
		// is not silently left cloning anonymously.
		GitAuthSecret: strings.TrimSpace(getFormValue(r, "git_auth_secret")),
		Devcontainer: &DevcontainerConfig{
			Enabled:            true,
			Dir:                dir,
			CacheRepo:          strings.TrimSpace(getFormValue(r, "cache_repo")),
			RegistryAuthSecret: strings.TrimSpace(getFormValue(r, "registry_auth_secret")),
		},
	}
}

// cacheRepoWarnings flags a missing cache repo at import rather than letting the
// admin discover it when saving. Validation rejects it either way; this just
// moves the message to where it can still be acted on cheaply.
func cacheRepoWarnings(t WorkspaceTemplate) []devcontainer.Warning {
	if t.Devcontainer == nil || strings.TrimSpace(t.Devcontainer.CacheRepo) != "" {
		return nil
	}
	return []devcontainer.Warning{{
		Key:     "cache_repo",
		Message: "set devcontainer.cache_repo before saving — without a layer cache registry every student rebuilds the devcontainer from scratch on first start",
	}}
}

// detectDevcontainerFromUpload reads a devcontainer.json, or a repository .zip
// containing one.
func (h *Handler) detectDevcontainerFromUpload(r *http.Request) (*devcontainer.Config, string, error) {
	if err := r.ParseMultipartForm(50 << 20); err != nil {
		log.Printf("Failed to parse devcontainer upload form: %v", err)
		return nil, "", errDevcontainerRead
	}

	file, header, err := r.FormFile("devcontainer_file")
	if err != nil {
		return nil, "", clientErrorf("no file uploaded: attach a devcontainer.json or a repository .zip")
	}
	defer file.Close()

	tmpDir, err := os.MkdirTemp("", "detect-devcontainer-*")
	if err != nil {
		log.Printf("Failed to create temp dir for devcontainer upload: %v", err)
		return nil, "", errDevcontainerRead
	}
	defer os.RemoveAll(tmpDir)

	// Only the extension is honoured; the client-supplied name never reaches the
	// filesystem path.
	filename := strings.ToLower(filepath.Base(header.Filename))
	tmpPath := filepath.Join(tmpDir, "upload")

	out, err := os.Create(tmpPath)
	if err != nil {
		log.Printf("Failed to create temp file for devcontainer upload: %v", err)
		return nil, "", errDevcontainerRead
	}
	if _, err := io.Copy(out, file); err != nil {
		out.Close()
		log.Printf("Failed to write temp file for devcontainer upload: %v", err)
		return nil, "", errDevcontainerRead
	}
	out.Close()

	switch {
	case strings.HasSuffix(filename, ".zip"):
		return devcontainer.ParseFromZip(tmpPath)
	case strings.HasSuffix(filename, ".json"):
		cfg, err := devcontainer.ParseFromFile(tmpPath)
		return cfg, filename, err
	}
	return nil, "", clientErrorf("unsupported file type: upload a devcontainer.json or a repository .zip")
}

// detectDevcontainerFromGit shallow-clones the workshop repo and reads its
// devcontainer.json.
func (h *Handler) detectDevcontainerFromGit(r *http.Request) (*devcontainer.Config, string, error) {
	repoURL := strings.TrimSpace(getFormValue(r, "git_repo"))
	branch := strings.TrimSpace(getFormValue(r, "git_branch"))

	if repoURL == "" {
		return nil, "", clientErrorf("git_repo is required")
	}
	if !validateURL(repoURL) {
		return nil, "", clientErrorf("git_repo is not a valid URL")
	}

	tmpDir, err := os.MkdirTemp("", "detect-devcontainer-git-*")
	if err != nil {
		log.Printf("Failed to create temp dir for devcontainer clone: %v", err)
		return nil, "", errDevcontainerClone
	}
	defer os.RemoveAll(tmpDir)

	opts := &git.CloneOptions{
		URL:          repoURL,
		SingleBranch: true,
		Depth:        1,
		// nil means anonymous, so a public repo needs no special case here.
		Auth: gitCloneAuth(getFormValue(r, "git_username"), getFormValue(r, "git_token")),
	}
	// An empty branch means "whatever the remote's HEAD is". Defaulting to a name
	// here would break every repo that does not use it.
	if branch != "" {
		opts.ReferenceName = plumbing.NewBranchReferenceName(branch)
	}

	if _, err := git.PlainClone(tmpDir, false, opts); err != nil {
		log.Printf("Failed to clone %s for devcontainer detection: %v", redactURL(repoURL), err)
		return nil, "", errDevcontainerClone
	}

	found, err := devcontainer.FindInDir(tmpDir)
	if err != nil {
		return nil, "", err
	}

	cfg, err := devcontainer.ParseFromFile(found)
	if err != nil {
		return nil, "", err
	}

	rel, err := filepath.Rel(tmpDir, found)
	if err != nil {
		rel = filepath.Base(found)
	}
	return cfg, filepath.ToSlash(rel), nil
}

// gitCloneAuth builds the credential for an import clone of a private repo. The
// token is request-scoped: it authenticates this one clone and is never
// persisted — the template the import produces references a Secret by name
// instead, and that Secret is what the students' workspaces clone with.
//
// The two are deliberately separate. At import time the admin is still in the
// creation wizard and the lab's cluster does not exist yet, so there is nowhere
// to put a Secret; the import token cannot become the lab's git_auth_secret.
//
// A nil AuthMethod is how go-git spells "anonymous", so an empty token needs no
// special case at the call site.
func gitCloneAuth(username, token string) transport.AuthMethod {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil
	}
	if username = strings.TrimSpace(username); username == "" {
		// GitLab wants this exact name alongside a personal access token; GitHub
		// accepts any non-empty username.
		username = defaultGitAuthUsername
	}
	return &githttp.BasicAuth{Username: username, Password: token}
}

// redactURL strips any userinfo from a URL so credentials embedded in a git
// remote never reach the log.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "[unparseable url]"
	}
	if u.User != nil {
		u.User = url.User("redacted")
	}
	return u.String()
}
