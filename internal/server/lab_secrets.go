package server

import (
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strings"
	"time"

	"easylab/internal/providers/workspace"
)

// Admin management of the credential Secrets workspace templates reference by
// name: registry credentials for pulling private images, and git credentials for
// cloning private workshop repositories.
//
// The token is written straight to the lab's cluster and then dropped. EasyLab
// holds it for the lifetime of the request and no longer, which is what keeps it
// out of the lab config, the job file, and the templates export — and what makes
// a server restart irrelevant to it.
//
// Because the Secret lives in the cluster, these routes only work once the lab is
// up: the cluster has to exist before anything can be written to it.

// labSecretManager resolves the lab's workspace backend and its optional
// SecretManager, applying the same gates as ServeLabWorkspaces: the lab must
// exist, have finished provisioning, and have a kubeconfig.
//
// It writes the HTTP error itself and reports ok=false, so callers can return
// immediately.
func (h *Handler) labSecretManager(w http.ResponseWriter, r *http.Request) (sm workspace.SecretManager, labID string, ok bool) {
	labID = labIDFromPath(r.URL.Path)
	if labID == "" {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return nil, "", false
	}

	job, exists := h.jobManager.GetJob(labID)
	if !exists {
		http.Error(w, "Lab not found", http.StatusNotFound)
		return nil, "", false
	}

	job.mu.RLock()
	status := job.Status
	kubeconfig := extractStringFromConfigValue(job.Kubeconfig)
	namespace := job.workspaceNamespace()
	job.mu.RUnlock()

	if status != JobStatusCompleted {
		http.Error(w, "Lab is not ready yet", http.StatusBadRequest)
		return nil, "", false
	}
	if kubeconfig == "" {
		http.Error(w, "Lab cluster configuration not available", http.StatusInternalServerError)
		return nil, "", false
	}

	backend, err := h.newWorkspaceBackend(kubeconfig, namespace)
	if err != nil {
		log.Printf("Lab secrets: failed to build backend for lab %s: %v", labID, err)
		http.Error(w, "Failed to reach lab cluster", http.StatusInternalServerError)
		return nil, "", false
	}

	sm, supported := backend.(workspace.SecretManager)
	if !supported {
		http.Error(w, "This lab's workspace backend cannot manage secrets", http.StatusNotImplemented)
		return nil, "", false
	}
	return sm, labID, true
}

// labIDFromPath pulls the lab ID out of /api/labs/{id}/secrets (and the
// backward-compatible /api/jobs/{id}/... prefix), including the /secrets/delete
// suffix.
func labIDFromPath(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	// api / {labs|jobs} / {id} / secrets ...
	if len(parts) < 4 || parts[0] != "api" {
		return ""
	}
	return parts[2]
}

// ServeLabSecrets renders the lab's credential Secrets panel.
//
//	GET /api/labs/{id}/secrets
func (h *Handler) ServeLabSecrets(w http.ResponseWriter, r *http.Request) {
	sm, labID, ok := h.labSecretManager(w, r)
	if !ok {
		return
	}

	secrets, err := sm.ListAuthSecrets(r.Context())
	if err != nil {
		log.Printf("Failed to list auth secrets for lab %s: %v", labID, err)
		http.Error(w, "Failed to list credentials", http.StatusInternalServerError)
		return
	}

	// Credentials a template references but the cluster does not have — a lost
	// restart, a name typo, or a Secret deleted by hand all surface here.
	var templates []WorkspaceTemplate
	if job, ok := h.jobManager.GetJob(labID); ok {
		job.mu.RLock()
		if job.Config != nil {
			templates = job.Config.GetWorkspaceTemplates()
		}
		job.mu.RUnlock()
	}
	missing := pendingCredentialNames(secrets, templates)

	w.Header().Set("Content-Type", "text/html")
	fmt.Fprint(w, renderLabSecrets(labID, secrets, missing))
}

// renderLabSecrets builds the panel's HTML fragment. Every value interpolated
// here comes from the cluster rather than the template author, but it is escaped
// all the same: a Secret name is attacker-influenced input the moment anyone else
// can write to the namespace.
func renderLabSecrets(labID string, secrets []workspace.AuthSecret, missing []string) string {
	var b strings.Builder

	if len(missing) > 0 {
		escaped := make([]string, len(missing))
		for i, name := range missing {
			escaped[i] = template.HTMLEscapeString(name)
		}
		fmt.Fprintf(&b,
			`<div class="secrets-pending">Referenced by a template but not present: <strong>%s</strong>. Workspaces needing these will fail until they are added below.</div>`,
			strings.Join(escaped, ", "))
	}

	b.WriteString(`<div class="secrets-list">`)
	if len(secrets) == 0 {
		b.WriteString(`<p class="secrets-empty">No credentials yet. Add one below, then reference it from a workspace template.</p>`)
	}
	for _, s := range secrets {
		kind := "Registry"
		refHint := "image_pull_secrets"
		if s.Type == workspace.AuthSecretGit {
			kind = "Git"
			refHint = "git_auth_secret"
		}

		b.WriteString(`<div class="secret-row">`)
		fmt.Fprintf(&b, `<span class="secret-name">%s</span>`, template.HTMLEscapeString(s.Name))
		fmt.Fprintf(&b, `<span class="secret-kind">%s</span>`, kind)
		if len(s.Servers) > 0 {
			fmt.Fprintf(&b, `<span class="secret-servers">%s</span>`, template.HTMLEscapeString(strings.Join(s.Servers, ", ")))
		}
		if s.Username != "" {
			fmt.Fprintf(&b, `<span class="secret-username">%s</span>`, template.HTMLEscapeString(s.Username))
		}
		fmt.Fprintf(&b, `<code class="secret-ref">%s: %s</code>`, refHint, template.HTMLEscapeString(s.Name))
		fmt.Fprintf(&b,
			`<button class="btn btn-danger btn-sm" hx-post="/api/labs/%s/secrets/delete" hx-vals='{"name": "%s"}' hx-target="#secrets-panel" hx-confirm="Delete credential %s? Workspaces already running keep working; new ones referencing it will fail.">Delete</button>`,
			template.HTMLEscapeString(labID), template.JSEscapeString(s.Name), template.HTMLEscapeString(s.Name))
		b.WriteString(`</div>`)
	}
	b.WriteString(`</div>`)

	return b.String()
}

// ServeRecreateCredentials returns the credential-prompt fragment for recreating
// a lab: one row per credential its templates reference, name and kind fixed,
// token blank. Recreation lands on a new cluster, so the tokens the old one held
// must be supplied again.
//
//	GET /api/labs/{id}/recreate-credentials
//
// An empty response means the lab references no credentials — the caller then
// recreates without prompting.
func (h *Handler) ServeRecreateCredentials(w http.ResponseWriter, r *http.Request) {
	labID := labIDFromPath(r.URL.Path)
	if labID == "" {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}

	job, exists := h.jobManager.GetJob(labID)
	if !exists {
		http.Error(w, "Lab not found", http.StatusNotFound)
		return
	}
	job.mu.RLock()
	var templates []WorkspaceTemplate
	var deletionDate *time.Time
	if job.Config != nil {
		templates = job.Config.GetWorkspaceTemplates()
		deletionDate = job.Config.LabDeletionDate
	}
	job.mu.RUnlock()

	w.Header().Set("Content-Type", "text/html")
	// Credentials first, then the reschedule-deletion section. Either may be empty;
	// an entirely empty response tells the client to recreate without prompting.
	fmt.Fprint(w, renderRecreateCredentials(referencedCredentials(templates))+renderRecreateDeletionDate(deletionDate))
}

// renderRecreateCredentials builds the recreate prompt's rows. Names come from
// the admin's own template YAML, so they are escaped: the panel echoes them back
// into HTML.
func renderRecreateCredentials(creds []referencedCredential) string {
	if len(creds) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString(`<p class="wizard-credentials-intro">Recreating builds a new cluster, so the credentials the old one held are gone. Supply the tokens again — leaving one blank skips it, and it can be added later from the lab's Workspaces page.</p>`)
	for _, c := range creds {
		name := template.HTMLEscapeString(c.Name)
		isRegistry := c.Kind == workspace.AuthSecretRegistry
		kindLabel := "Git"
		if isRegistry {
			kindLabel = "Registry"
		}

		b.WriteString(`<div class="credential-row credential-row-stacked">`)
		fmt.Fprintf(&b, `<input type="hidden" name="secret_kind" value="%s">`, template.HTMLEscapeString(c.Kind))
		fmt.Fprintf(&b, `<input type="hidden" name="secret_name" value="%s">`, name)
		fmt.Fprintf(&b, `<div class="credential-row-title"><span class="secret-name">%s</span><span class="secret-kind">%s</span></div>`, name, kindLabel)
		if isRegistry {
			b.WriteString(`<div class="form-group"><label>Server</label><input type="text" name="secret_server" placeholder="registry.example.com"></div>`)
			b.WriteString(`<div class="form-group"><label>Username</label><input type="text" name="secret_username" placeholder="user" autocomplete="off"></div>`)
		} else {
			// A git row still submits an (empty) secret_server so the parsed form
			// arrays stay index-aligned with the registry rows — parseWizardSecrets
			// zips them positionally, one entry per row.
			b.WriteString(`<input type="hidden" name="secret_server" value="">`)
			// Git username defaults to oauth2 server-side; keep it editable but not required.
			b.WriteString(`<div class="form-group"><label>Username</label><input type="text" name="secret_username" placeholder="oauth2" autocomplete="off"></div>`)
		}
		b.WriteString(`<div class="form-group"><label>Token</label><input type="password" name="secret_token" autocomplete="off"></div>`)
		b.WriteString(`</div>`)
	}
	return b.String()
}

// renderRecreateDeletionDate builds the recreate prompt's "reschedule deletion"
// section, shown only when the destroyed lab had a scheduled deletion date. That
// date is now in the past, and reusing it would delete the recreated lab on the
// next cleanup tick, so the admin must pick a new one (or leave it blank to keep
// the lab running). Field names match the creation wizard so RecreateLab parses
// them the same way. An empty return means the lab had no deletion date.
func renderRecreateDeletionDate(old *time.Time) string {
	if old == nil {
		return ""
	}
	today := time.Now().Format("2006-01-02")
	prev := template.HTMLEscapeString(old.Format("Jan 02, 2006 at 15:04"))

	var b strings.Builder
	b.WriteString(`<div class="credential-row credential-row-stacked">`)
	b.WriteString(`<div class="credential-row-title"><span class="secret-name">Scheduled deletion</span></div>`)
	fmt.Fprintf(&b, `<p class="recreate-deletion-note">This lab was set to auto-delete on %s, which has passed. Choose a new date, or leave it blank to keep the lab running.</p>`, prev)
	fmt.Fprintf(&b, `<div class="form-group"><label>Deletion date</label><input type="date" name="lab_deletion_date" min="%s"></div>`, today)
	b.WriteString(`<div class="form-group"><label>Deletion time</label><input type="time" name="lab_deletion_time" placeholder="23:59"></div>`)
	b.WriteString(`</div>`)
	return b.String()
}

// SaveLabSecret writes a credential Secret into the lab's workspace namespace.
//
//	POST /api/labs/{id}/secrets
//
// Writing over an existing name rotates it, which is the supported way to replace
// an expired token.
func (h *Handler) SaveLabSecret(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sm, labID, ok := h.labSecretManager(w, r)
	if !ok {
		return
	}
	if err := h.parseForm(w, r, 0); err != nil {
		log.Printf("Failed to parse form while saving a secret for lab %s: %v", labID, err)
		writeToast(w, false, "Could not read the submitted form.")
		return
	}

	var (
		kind     = getFormValue(r, "kind")
		name     = strings.TrimSpace(getFormValue(r, "name"))
		username = strings.TrimSpace(getFormValue(r, "username"))
		server   = strings.TrimSpace(getFormValue(r, "server"))
		token    = strings.TrimSpace(getFormValue(r, "token"))
	)

	if err := validateSecretName(name); err != nil {
		writeToast(w, false, template.HTMLEscapeString(err.Error()))
		return
	}
	if token == "" {
		writeToast(w, false, "A token is required.")
		return
	}

	var err error
	switch kind {
	case workspace.AuthSecretRegistry:
		if server == "" {
			writeToast(w, false, "A registry server is required (for example registry.example.com).")
			return
		}
		err = sm.EnsureRegistrySecret(r.Context(), name, server, username, token)
	case workspace.AuthSecretGit:
		// Any non-empty username works with a GitLab or GitHub token; oauth2 is the
		// one GitLab documents, so it is a safe default for an admin who pasted only
		// a token.
		if username == "" {
			username = defaultGitAuthUsername
		}
		err = sm.EnsureGitAuthSecret(r.Context(), name, username, token)
	default:
		writeToast(w, false, "Unknown credential type.")
		return
	}
	if err != nil {
		// The error can name the secret and the cluster; the admin gets a generic
		// message and the detail goes to the log.
		log.Printf("Failed to save %s secret %q for lab %s: %v", kind, name, labID, err)
		writeToast(w, false, "Could not save the credential. Check the server logs.")
		return
	}

	log.Printf("Saved %s credential %q for lab %s", kind, name, labID)
	h.ServeLabSecrets(w, r)
}

// DeleteLabSecret removes a credential Secret from the lab's workspace namespace.
//
//	POST /api/labs/{id}/secrets/delete
func (h *Handler) DeleteLabSecret(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sm, labID, ok := h.labSecretManager(w, r)
	if !ok {
		return
	}
	if err := h.parseForm(w, r, 0); err != nil {
		log.Printf("Failed to parse form while deleting a secret for lab %s: %v", labID, err)
		writeToast(w, false, "Could not read the submitted form.")
		return
	}

	name := strings.TrimSpace(getFormValue(r, "name"))
	if name == "" {
		writeToast(w, false, "A credential name is required.")
		return
	}
	if err := sm.DeleteAuthSecret(r.Context(), name); err != nil {
		log.Printf("Failed to delete secret %q for lab %s: %v", name, labID, err)
		writeToast(w, false, "Could not delete the credential. Check the server logs.")
		return
	}

	log.Printf("Deleted credential %q for lab %s", name, labID)
	h.ServeLabSecrets(w, r)
}

// defaultGitAuthUsername is what GitLab expects alongside a personal access
// token, and what GitHub ignores.
const defaultGitAuthUsername = "oauth2"

// validateSecretName rejects names Kubernetes would reject anyway, so the admin
// gets a message about their input rather than a generic save failure.
func validateSecretName(name string) error {
	if name == "" {
		return fmt.Errorf("a credential name is required")
	}
	if len(name) > 253 {
		return fmt.Errorf("the credential name is too long (maximum 253 characters)")
	}
	if sanitizeDNSName(name) != name {
		return fmt.Errorf("the credential name %q is not valid: use lowercase letters, digits, '-' and '.'", name)
	}
	return nil
}

// sanitizeDNSName reduces a string to what a Kubernetes object name allows. It is
// used to test names rather than to rewrite them: silently renaming an admin's
// secret would leave their template referencing a name that does not exist.
func sanitizeDNSName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '.':
			b.WriteRune(r)
		}
	}
	return strings.Trim(b.String(), "-.")
}
