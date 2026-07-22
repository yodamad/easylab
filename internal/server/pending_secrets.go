package server

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"

	"easylab/internal/providers/workspace"
)

// Credentials captured during lab creation, held until the lab's cluster exists
// to receive them.
//
// A credential Secret lives in the lab's cluster, and at the moment the admin
// fills the wizard there is no cluster — Pulumi takes roughly a quarter of an
// hour to make one. So the token waits here, in memory, and is written the
// instant the cluster is up.
//
// It is deliberately never persisted. That keeps the invariant the rest of the
// design rests on — a token is never at rest on disk, so it cannot reach the job
// file, the jobs API or the templates export — at the price of losing pending
// credentials if the server restarts mid-provision. That loss is made visible
// rather than silent: pendingCredentialNames reports what a lab's templates
// reference but its cluster does not have, so the admin is told instead of the
// first student discovering it.

// pendingSecret is one credential waiting for its cluster.
type pendingSecret struct {
	Kind     string // workspace.AuthSecretRegistry | workspace.AuthSecretGit
	Name     string
	Server   string // registry only
	Username string
	Token    string
}

// pendingSecretStore holds pending credentials per job.
type pendingSecretStore struct {
	mu      sync.RWMutex
	pending map[string][]pendingSecret
}

func newPendingSecretStore() *pendingSecretStore {
	return &pendingSecretStore{pending: make(map[string][]pendingSecret)}
}

// Put records the credentials to apply once jobID's cluster is up. Putting an
// empty list stores nothing, so callers need not special-case a lab with no
// credentials.
func (s *pendingSecretStore) Put(jobID string, secrets []pendingSecret) {
	if len(secrets) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending[jobID] = secrets
}

// Take returns a job's pending credentials and forgets them, so the token is
// held for exactly as long as it takes to write it to the cluster.
func (s *pendingSecretStore) Take(jobID string) []pendingSecret {
	s.mu.Lock()
	defer s.mu.Unlock()
	secrets := s.pending[jobID]
	delete(s.pending, jobID)
	return secrets
}

// Discard drops a job's pending credentials unapplied — for a lab that is being
// deleted, whose cluster will never exist to receive them.
func (s *pendingSecretStore) Discard(jobID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.pending, jobID)
}

// Has reports whether a job has credentials waiting. Used to tell "the admin
// supplied none" apart from "they were supplied and lost".
func (s *pendingSecretStore) Has(jobID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.pending[jobID]) > 0
}

// applyPendingSecrets writes a lab's captured credentials to its freshly
// provisioned cluster. It runs before the lab is marked completed, so a lab that
// reports itself ready has the credentials its templates reference.
//
// A failure here does not fail the lab: the infrastructure is up and correct, and
// only the credentials are missing. It is logged, and pendingCredentialNames
// surfaces the gap on the lab's Workspaces page.
func (h *Handler) applyPendingSecrets(jobID string) {
	secrets := h.pendingSecrets.Take(jobID)
	if len(secrets) == 0 {
		return
	}

	job, exists := h.jobManager.GetJob(jobID)
	if !exists {
		log.Printf("Cannot apply credentials for lab %s: lab not found", jobID)
		return
	}
	job.mu.RLock()
	kubeconfig := extractStringFromConfigValue(job.Kubeconfig)
	namespace := job.workspaceNamespace()
	job.mu.RUnlock()

	if kubeconfig == "" {
		log.Printf("Cannot apply credentials for lab %s: no kubeconfig", jobID)
		return
	}

	backend, err := h.newWorkspaceBackend(kubeconfig, namespace)
	if err != nil {
		log.Printf("Cannot apply credentials for lab %s: %v", jobID, err)
		return
	}
	sm, ok := backend.(workspace.SecretManager)
	if !ok {
		log.Printf("Cannot apply credentials for lab %s: backend cannot manage secrets", jobID)
		return
	}

	ctx := context.Background()
	for _, sec := range secrets {
		var err error
		switch sec.Kind {
		case workspace.AuthSecretRegistry:
			err = sm.EnsureRegistrySecret(ctx, sec.Name, sec.Server, sec.Username, sec.Token)
		case workspace.AuthSecretGit:
			err = sm.EnsureGitAuthSecret(ctx, sec.Name, sec.Username, sec.Token)
		default:
			err = nil
		}
		if err != nil {
			// Names the credential, never the token: this goes to the lab's log.
			log.Printf("Failed to apply %s credential %q to lab %s: %v", sec.Kind, sec.Name, jobID, err)
			h.jobManager.AppendOutput(jobID, "WARNING: could not create credential "+sec.Name+" — add it from the lab's Workspaces page")
			continue
		}
		log.Printf("Applied %s credential %q to lab %s", sec.Kind, sec.Name, jobID)
		h.jobManager.AppendOutput(jobID, "Created credential "+sec.Name)
	}
}

// parseWizardSecrets reads the credential rows from a lab-creation form. The
// wizard posts parallel arrays — secret_kind[], secret_name[], secret_server[],
// secret_username[], secret_token[] — one entry per row.
//
// A row with no name and no token is an empty row the admin left behind and is
// skipped. Any other incompleteness is an error the admin can still fix, so it is
// returned rather than silently dropped: a row with a token but no name would
// otherwise create nothing while looking saved.
func parseWizardSecrets(r *http.Request) ([]pendingSecret, error) {
	kinds := r.Form["secret_kind"]
	names := r.Form["secret_name"]
	servers := r.Form["secret_server"]
	usernames := r.Form["secret_username"]
	tokens := r.Form["secret_token"]

	at := func(s []string, i int) string {
		if i < len(s) {
			return strings.TrimSpace(s[i])
		}
		return ""
	}

	var out []pendingSecret
	seen := make(map[string]bool)
	for i := range names {
		kind := at(kinds, i)
		name := at(names, i)
		server := at(servers, i)
		username := at(usernames, i)
		token := at(tokens, i)

		if name == "" && token == "" {
			continue // an empty row
		}
		if name == "" {
			return nil, fmt.Errorf("a credential has a token but no name")
		}
		if err := validateSecretName(name); err != nil {
			return nil, err
		}
		if token == "" {
			return nil, fmt.Errorf("credential %q has no token", name)
		}
		if seen[name] {
			return nil, fmt.Errorf("duplicate credential name %q", name)
		}
		seen[name] = true

		switch kind {
		case workspace.AuthSecretRegistry:
			if server == "" {
				return nil, fmt.Errorf("registry credential %q needs a server", name)
			}
			if username == "" {
				return nil, fmt.Errorf("registry credential %q needs a username", name)
			}
		case workspace.AuthSecretGit:
			if username == "" {
				username = defaultGitAuthUsername
			}
		default:
			return nil, fmt.Errorf("credential %q has an unknown type %q", name, kind)
		}

		out = append(out, pendingSecret{
			Kind: kind, Name: name, Server: server, Username: username, Token: token,
		})
	}
	return out, nil
}

// autolinkGitCredential wires the wizard's git credential into the templates that
// need it, for the form path where there is no per-template field to name one.
//
// It acts only when exactly one git credential was entered: then every template
// that clones a private GitRepo but names no git_auth_secret is pointed at it. Two
// or more git credentials are ambiguous — the admin must choose, with the
// template's Git-credential picker or in the YAML — so those are left untouched, as
// are templates that already name a credential explicitly.
func autolinkGitCredential(templates []WorkspaceTemplate, secrets []pendingSecret) {
	name := ""
	count := 0
	for _, s := range secrets {
		if s.Kind == workspace.AuthSecretGit {
			name = s.Name
			count++
		}
	}
	if count != 1 {
		return
	}
	for i := range templates {
		if templates[i].GitRepo != "" && strings.TrimSpace(templates[i].GitAuthSecret) == "" {
			templates[i].GitAuthSecret = name
		}
	}
}

// referencedCredentialNames returns every credential name a lab's templates name,
// which is what its workspaces will need the cluster to have.
func referencedCredentialNames(templates []WorkspaceTemplate) []string {
	seen := make(map[string]bool)
	var out []string
	add := func(name string) {
		if name = strings.TrimSpace(name); name != "" && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}

	for _, t := range templates {
		add(t.GitAuthSecret)
		for _, n := range t.ImagePullSecrets {
			add(n)
		}
		if t.Devcontainer != nil {
			add(t.Devcontainer.RegistryAuthSecret)
		}
	}
	return out
}

// referencedCredential is a credential a template names, with the kind inferred
// from the field that names it. It carries no secret material — it is used to
// prompt an admin to re-enter a token on recreate, where the old cluster's
// Secrets are gone.
type referencedCredential struct {
	Name string
	Kind string // workspace.AuthSecretRegistry | workspace.AuthSecretGit
}

// referencedCredentials lists the distinct credentials a lab's templates name,
// each tagged with the kind of the field that referenced it. A name referenced as
// both git and registry keeps its first (git) classification — an ambiguity not
// worth a UI for; the admin can pick the right type when re-entering.
func referencedCredentials(templates []WorkspaceTemplate) []referencedCredential {
	seen := make(map[string]bool)
	var out []referencedCredential
	add := func(name, kind string) {
		if name = strings.TrimSpace(name); name != "" && !seen[name] {
			seen[name] = true
			out = append(out, referencedCredential{Name: name, Kind: kind})
		}
	}

	for _, t := range templates {
		add(t.GitAuthSecret, workspace.AuthSecretGit)
		for _, n := range t.ImagePullSecrets {
			add(n, workspace.AuthSecretRegistry)
		}
		if t.Devcontainer != nil {
			add(t.Devcontainer.RegistryAuthSecret, workspace.AuthSecretRegistry)
		}
	}
	return out
}

// pendingCredentialNames reports the credentials a lab's templates reference that
// its cluster does not have.
//
// It is computed rather than remembered, by comparing the templates against the
// cluster. That makes it correct whatever happened in between: credentials lost
// to a restart, a lab recreated onto a new cluster, a name typo'd in the YAML, or
// a Secret someone deleted by hand all show up the same way, and the answer stops
// being wrong the moment the admin fixes it.
func pendingCredentialNames(existing []workspace.AuthSecret, templates []WorkspaceTemplate) []string {
	have := make(map[string]bool, len(existing))
	for _, s := range existing {
		have[s.Name] = true
	}

	var missing []string
	for _, name := range referencedCredentialNames(templates) {
		if !have[name] {
			missing = append(missing, name)
		}
	}
	return missing
}
