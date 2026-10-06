package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/github"
)

const (
	// Cookie binding a GitHub OAuth state to the browser that started the flow.
	githubOAuthStateCookieName = "github_oauth_state"
	// Path the state cookie is scoped to (both the login and callback routes).
	githubOAuthCookiePath = "/student/auth/github"
	// Callback route registered on the GitHub OAuth app.
	githubCallbackPath = "/student/auth/github/callback"
	// Default GitHub REST API base URL (overridable on AuthHandler for tests).
	githubDefaultAPIBase = "https://api.github.com"
	// Domain of the identity given to students who sign in with GitHub.
	githubNoReplyDomain = "users.noreply.github.com"
	// Upper bound for the token exchange plus the GitHub API calls of one callback.
	githubAPITimeout = 10 * time.Second
)

// githubOrgRegex matches a GitHub organization name (lowercased).
var githubOrgRegex = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,37}[a-z0-9])?$`)

// githubLoginRegex matches a GitHub username (lowercased). Underscores appear
// in Enterprise Managed User logins.
var githubLoginRegex = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)

// GitHubAuthConfig holds the GitHub OAuth settings for student login.
type GitHubAuthConfig struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	// AllowedOrgs restricts sign-in to active members of at least one of these
	// organizations. Empty means any GitHub account may sign in.
	AllowedOrgs []string `json:"allowed_orgs,omitempty"`
	// DisableClassicLogin hides the password login form for students (GitHub login must be enabled).
	DisableClassicLogin bool `json:"disable_classic_login,omitempty"`
}

// Enabled reports whether the config is complete enough to offer GitHub login.
func (c GitHubAuthConfig) Enabled() bool {
	return c.ClientID != "" && c.ClientSecret != ""
}

// GitHubAuthStore persists the GitHub OAuth settings to disk. The client
// secret is encrypted at rest with the same key as persisted job files.
type GitHubAuthStore struct {
	config  GitHubAuthConfig
	dataDir string
	mu      sync.RWMutex
}

// NewGitHubAuthStore creates a store and loads persisted config from disk.
func NewGitHubAuthStore(dataDir string) *GitHubAuthStore {
	s := &GitHubAuthStore{dataDir: dataDir}
	if err := s.load(); err != nil {
		log.Printf("[GITHUB-AUTH] Warning: failed to load config: %v", err)
	}
	return s
}

func (s *GitHubAuthStore) configPath() string {
	return filepath.Join(s.dataDir, "github-auth.json")
}

// load reads the config from disk. Missing file is not an error.
func (s *GitHubAuthStore) load() error {
	if s.dataDir == "" {
		return nil
	}
	data, err := os.ReadFile(s.configPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read github-auth config: %w", err)
	}
	var cfg GitHubAuthConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("failed to parse github-auth config: %w", err)
	}
	secret, err := decryptSecret(cfg.ClientSecret)
	if err != nil {
		return fmt.Errorf("failed to decrypt github-auth client secret: %w", err)
	}
	cfg.ClientSecret = secret

	s.mu.Lock()
	s.config = cfg
	s.mu.Unlock()
	return nil
}

// Get returns the current GitHub OAuth configuration.
func (s *GitHubAuthStore) Get() GitHubAuthConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cfg := s.config
	cfg.AllowedOrgs = append([]string(nil), s.config.AllowedOrgs...)
	return cfg
}

// Set updates the GitHub OAuth configuration and persists it.
func (s *GitHubAuthStore) Set(cfg GitHubAuthConfig) error {
	cfg.AllowedOrgs = append([]string(nil), cfg.AllowedOrgs...)

	if s.dataDir != "" {
		onDisk := cfg
		secret, err := encryptSecret(cfg.ClientSecret)
		if err != nil {
			return fmt.Errorf("failed to encrypt github-auth client secret: %w", err)
		}
		onDisk.ClientSecret = secret

		data, err := json.MarshalIndent(onDisk, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal github-auth config: %w", err)
		}
		if err := os.MkdirAll(s.dataDir, 0755); err != nil {
			return fmt.Errorf("failed to create data dir: %w", err)
		}
		if err := os.WriteFile(s.configPath(), data, 0600); err != nil {
			return fmt.Errorf("failed to write github-auth config: %w", err)
		}
	}

	s.mu.Lock()
	s.config = cfg
	s.mu.Unlock()
	return nil
}

// parseGitHubOrgs splits a comma/whitespace separated list of organization
// names, lowercases and de-duplicates them, and rejects invalid names.
func parseGitHubOrgs(raw string) ([]string, error) {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	var orgs []string
	seen := make(map[string]bool)
	for _, f := range fields {
		org := strings.ToLower(strings.TrimPrefix(f, "@"))
		if !githubOrgRegex.MatchString(org) {
			return nil, fmt.Errorf("invalid GitHub organization name %q", f)
		}
		if seen[org] {
			continue
		}
		seen[org] = true
		orgs = append(orgs, org)
	}
	return orgs, nil
}

// ConfigureGitHub updates the GitHub OAuth config at runtime. A config without
// a client ID or client secret disables GitHub login.
func (ah *AuthHandler) ConfigureGitHub(cfg GitHubAuthConfig) {
	ah.mu.Lock()
	defer ah.mu.Unlock()

	if !cfg.Enabled() {
		ah.githubConfig = nil
		ah.githubEnabled = false
		ah.githubAllowedOrgs = nil
		ah.githubClassicLoginDisabled = false
		log.Printf("GitHub student login disabled")
		return
	}

	endpoint := ah.githubEndpoint
	if endpoint.AuthURL == "" {
		endpoint = github.Endpoint
	}
	// Reading the username needs no scope; checking organization membership
	// (including private membership) needs read:org.
	var scopes []string
	if len(cfg.AllowedOrgs) > 0 {
		scopes = []string{"read:org"}
	}
	ah.githubConfig = &oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		Scopes:       scopes,
		Endpoint:     endpoint,
	}
	ah.githubEnabled = true
	ah.githubAllowedOrgs = append([]string(nil), cfg.AllowedOrgs...)
	ah.githubClassicLoginDisabled = cfg.DisableClassicLogin
	log.Printf("GitHub student login configured (allowed orgs: %d)", len(cfg.AllowedOrgs))
}

// GitHubEnabled reports whether GitHub login is currently enabled.
func (ah *AuthHandler) GitHubEnabled() bool {
	ah.mu.RLock()
	defer ah.mu.RUnlock()
	return ah.githubEnabled
}

// githubCallbackURL builds the OAuth callback URL for the host the request came in on.
func githubCallbackURL(r *http.Request) string {
	scheme := "http"
	if isSecureRequest(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host + githubCallbackPath
}

// githubLoginError sends the student back to the login page with a safe message.
func githubLoginError(w http.ResponseWriter, r *http.Request, message string) {
	http.Redirect(w, r, "/student/login?error="+url.QueryEscape(message), http.StatusSeeOther)
}

// clearGitHubStateCookie expires the OAuth state cookie.
func clearGitHubStateCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     githubOAuthStateCookieName,
		Value:    "",
		Path:     githubOAuthCookiePath,
		HttpOnly: true,
		MaxAge:   -1,
	})
}

// HandleGitHubLogin initiates the GitHub OAuth 2.0 flow for student login.
func (ah *AuthHandler) HandleGitHubLogin(w http.ResponseWriter, r *http.Request) {
	state := generateToken()

	ah.mu.Lock()
	cfg := ah.githubConfig
	if ah.githubEnabled && cfg != nil {
		if ah.githubOAuthStates == nil {
			ah.githubOAuthStates = make(map[string]time.Time)
		}
		ah.githubOAuthStates[state] = time.Now().Add(azureOAuthStateExpiry)
	}
	ah.mu.Unlock()

	if cfg == nil {
		githubLoginError(w, r, "GitHub sign-in is not available.")
		return
	}

	// Bind the state to this browser: the callback must present the same value
	// in this cookie, so a state issued to someone else cannot be replayed here.
	// Lax (not Strict) so the cookie is sent on the redirect back from github.com.
	http.SetCookie(w, &http.Cookie{
		Name:     githubOAuthStateCookieName,
		Value:    state,
		Path:     githubOAuthCookiePath,
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(azureOAuthStateExpiry.Seconds()),
	})

	authURL := cfg.AuthCodeURL(state, oauth2.SetAuthURLParam("redirect_uri", githubCallbackURL(r)))
	http.Redirect(w, r, authURL, http.StatusFound)
}

// HandleGitHubCallback handles the OAuth 2.0 callback from GitHub. It exchanges
// the authorization code for a token, reads the GitHub username, checks
// organization membership when a restriction is configured, and creates a
// student session identified as <username>@users.noreply.github.com.
func (ah *AuthHandler) HandleGitHubCallback(w http.ResponseWriter, r *http.Request) {
	ah.mu.RLock()
	cfg := ah.githubConfig
	allowedOrgs := append([]string(nil), ah.githubAllowedOrgs...)
	apiBase := ah.githubAPIBase
	ah.mu.RUnlock()

	if cfg == nil {
		githubLoginError(w, r, "GitHub sign-in is not available.")
		return
	}
	if apiBase == "" {
		apiBase = githubDefaultAPIBase
	}

	clearGitHubStateCookie(w)

	if errParam := r.URL.Query().Get("error"); errParam != "" {
		log.Printf("GitHub OAuth error: %q", errParam)
		if errParam == "access_denied" {
			githubLoginError(w, r, "GitHub sign-in was cancelled.")
			return
		}
		githubLoginError(w, r, "GitHub sign-in failed. Try again, or ask your instructor.")
		return
	}

	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")

	// Validate state to prevent CSRF: known, unexpired, single use, and issued to this browser.
	ah.mu.Lock()
	expiry, valid := ah.githubOAuthStates[state]
	if valid {
		delete(ah.githubOAuthStates, state)
	}
	ah.mu.Unlock()

	stateCookie, cookieErr := r.Cookie(githubOAuthStateCookieName)
	if !valid || time.Now().After(expiry) || cookieErr != nil ||
		subtle.ConstantTimeCompare([]byte(stateCookie.Value), []byte(state)) != 1 {
		log.Printf("GitHub callback: invalid or expired state")
		githubLoginError(w, r, "That sign-in attempt expired. Try again.")
		return
	}

	if code == "" {
		log.Printf("GitHub callback: missing authorization code")
		githubLoginError(w, r, "GitHub sign-in failed. Try again, or ask your instructor.")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), githubAPITimeout)
	defer cancel()

	token, err := cfg.Exchange(ctx, code, oauth2.SetAuthURLParam("redirect_uri", githubCallbackURL(r)))
	if err != nil {
		log.Printf("GitHub token exchange failed: %v", err)
		githubLoginError(w, r, "GitHub sign-in failed. Try again, or ask your instructor.")
		return
	}

	login, err := fetchGitHubLogin(ctx, apiBase, token.AccessToken)
	if err != nil {
		log.Printf("GitHub: failed to read user: %v", err)
		githubLoginError(w, r, "GitHub sign-in failed. Try again, or ask your instructor.")
		return
	}

	if len(allowedOrgs) > 0 {
		member, err := isGitHubOrgMember(ctx, apiBase, token.AccessToken, allowedOrgs)
		if err != nil {
			log.Printf("GitHub: organization membership check failed for %s: %v", login, err)
			githubLoginError(w, r, "GitHub sign-in failed. Try again, or ask your instructor.")
			return
		}
		if !member {
			log.Printf("GitHub login rejected: %s is not an active member of an allowed organization", login)
			githubLoginError(w, r, "Your GitHub account isn't in an organization allowed for this workshop. Ask your instructor for access.")
			return
		}
	}

	sessionToken, csrfToken := ah.createStudentSession(login + "@" + githubNoReplyDomain)

	// SameSite=Lax for the same reason as the Azure AD callback: the navigation
	// that lands on the dashboard originated from another site.
	isSecure := isSecureRequest(r)
	http.SetCookie(w, &http.Cookie{
		Name:     StudentSessionCookieName,
		Value:    sessionToken,
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(SessionExpiry.Seconds()),
	})
	http.SetCookie(w, csrfCookie(StudentCSRFCookieName, csrfToken, isSecure, http.SameSiteLaxMode))

	log.Printf("Successful GitHub student login")
	http.Redirect(w, r, "/student/dashboard", http.StatusSeeOther)
}

// githubAPIGet performs an authenticated GET against the GitHub REST API and
// decodes a 200 response into out. It returns the HTTP status code.
func githubAPIGet(ctx context.Context, apiBase, accessToken, path string, out interface{}) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(apiBase, "/")+path, nil)
	if err != nil {
		return 0, fmt.Errorf("failed to build GitHub request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("failed to call GitHub API: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, fmt.Errorf("failed to read GitHub response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return resp.StatusCode, fmt.Errorf("failed to parse GitHub response: %w", err)
	}
	return resp.StatusCode, nil
}

// fetchGitHubLogin returns the lowercased username of the token's owner.
func fetchGitHubLogin(ctx context.Context, apiBase, accessToken string) (string, error) {
	var user struct {
		Login string `json:"login"`
	}
	status, err := githubAPIGet(ctx, apiBase, accessToken, "/user", &user)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("GitHub /user returned status %d", status)
	}
	login := strings.ToLower(user.Login)
	if !githubLoginRegex.MatchString(login) {
		return "", fmt.Errorf("GitHub returned an unusable login %q", user.Login)
	}
	return login, nil
}

// isGitHubOrgMember reports whether the token's owner is an active member of
// at least one of orgs. A pending invitation does not count.
func isGitHubOrgMember(ctx context.Context, apiBase, accessToken string, orgs []string) (bool, error) {
	for _, org := range orgs {
		var membership struct {
			State string `json:"state"`
		}
		status, err := githubAPIGet(ctx, apiBase, accessToken, "/user/memberships/orgs/"+url.PathEscape(org), &membership)
		if err != nil {
			return false, err
		}
		if status == http.StatusOK && membership.State == "active" {
			return true, nil
		}
		// 404 = not a member; 403 usually means the organization restricts
		// third-party OAuth apps and has not approved this one.
		log.Printf("GitHub: no active membership in %s (status %d, state %q)", org, status, membership.State)
	}
	return false, nil
}

// SetGitHubAuth wires the GitHub login settings store, and a callback so the
// handler can update the live GitHub OAuth config at runtime.
func (h *Handler) SetGitHubAuth(store *GitHubAuthStore, apply func(GitHubAuthConfig)) {
	h.githubAuthStore = store
	h.githubAuthConfigurer = apply
}

// githubAuthView is the data rendered by the GitHub login admin page. It
// deliberately carries no client secret.
type githubAuthView struct {
	Enabled             bool
	ClientID            string
	HasSecret           bool
	AllowedOrgs         string
	DisableClassicLogin bool
	CallbackURL         string
}

// ServeGitHubAuth serves the GitHub login configuration admin page.
func (h *Handler) ServeGitHubAuth(w http.ResponseWriter, r *http.Request) {
	if h.githubAuthStore == nil {
		http.Error(w, "GitHub login settings not available", http.StatusServiceUnavailable)
		return
	}
	cfg := h.githubAuthStore.Get()
	h.serveTemplate(w, "github-auth.html", githubAuthView{
		Enabled:             cfg.Enabled(),
		ClientID:            cfg.ClientID,
		HasSecret:           cfg.ClientSecret != "",
		AllowedOrgs:         strings.Join(cfg.AllowedOrgs, ", "),
		DisableClassicLogin: cfg.DisableClassicLogin,
		CallbackURL:         githubCallbackURL(r),
	})
}

// SaveGitHubAuthConfig handles saving the GitHub login configuration from the admin page.
func (h *Handler) SaveGitHubAuthConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.githubAuthStore == nil {
		http.Error(w, "GitHub login settings not available", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	if err := r.ParseForm(); err != nil {
		fmt.Fprint(w, `<div class="error-message"><p>Failed to parse form</p></div>`)
		return
	}

	clientID := strings.TrimSpace(r.FormValue("github_client_id"))
	clientSecret := strings.TrimSpace(r.FormValue("github_client_secret"))
	disableClassic := r.FormValue("github_disable_classic_login") == "on"

	orgs, err := parseGitHubOrgs(r.FormValue("github_allowed_orgs"))
	if err != nil {
		log.Printf("SaveGitHubAuthConfig: %v", err)
		fmt.Fprint(w, `<div class="error-message"><p>One of the organization names is not valid. Use the name from the organization's GitHub URL, separated by commas.</p></div>`)
		return
	}

	var cfg GitHubAuthConfig
	if clientID != "" {
		// Preserve existing secret when the field is left blank (password fields submit empty on re-display)
		if clientSecret == "" {
			clientSecret = h.githubAuthStore.Get().ClientSecret
		}
		if clientSecret == "" {
			fmt.Fprint(w, `<div class="error-message"><p>Enter the client secret to turn on GitHub login.</p></div>`)
			return
		}
		cfg = GitHubAuthConfig{
			ClientID:            clientID,
			ClientSecret:        clientSecret,
			AllowedOrgs:         orgs,
			DisableClassicLogin: disableClassic,
		}
	}

	if err := h.githubAuthStore.Set(cfg); err != nil {
		log.Printf("SaveGitHubAuthConfig: failed to persist: %v", err)
		fmt.Fprint(w, `<div class="error-message"><p>Failed to save GitHub login settings. Check the server logs for details.</p></div>`)
		return
	}

	if h.githubAuthConfigurer != nil {
		h.githubAuthConfigurer(cfg)
	}

	detail := "disabled"
	if cfg.Enabled() {
		detail = "enabled, any GitHub account"
		if len(cfg.AllowedOrgs) > 0 {
			detail = "enabled, orgs: " + strings.Join(cfg.AllowedOrgs, ", ")
		}
	}
	h.recordAudit(adminActor(r), "admin", "github_auth.update", "", detail)

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", "/admin/github")
		fmt.Fprint(w, `<div class="success-message"><p>`+template.HTMLEscapeString("GitHub login settings saved")+`</p></div>`)
	} else {
		http.Redirect(w, r, "/admin/github", http.StatusSeeOther)
	}
}
