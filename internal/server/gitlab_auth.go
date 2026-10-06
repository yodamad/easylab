package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
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
)

const (
	// GitLab instance used when no URL is configured.
	gitlabDefaultBaseURL = "https://gitlab.com"
	// Cookie binding a GitLab OAuth state to the browser that started the flow.
	gitlabOAuthStateCookieName = "gitlab_oauth_state"
	// Path the state cookie is scoped to (both the login and callback routes).
	gitlabOAuthCookiePath = "/student/auth/gitlab"
	// Callback route registered on the GitLab application.
	gitlabCallbackPath = "/student/auth/gitlab/callback"
	// Upper bound for the token exchange plus the userinfo call of one callback.
	gitlabAPITimeout = 10 * time.Second
)

// gitlabGroupSegmentRegex matches one segment of a GitLab group path (lowercased).
var gitlabGroupSegmentRegex = regexp.MustCompile(`^[a-z0-9_][a-z0-9_.-]*$`)

// gitlabUsernameRegex matches a GitLab username (lowercased).
var gitlabUsernameRegex = regexp.MustCompile(`^[a-z0-9_.-]{1,255}$`)

// GitLabAuthConfig holds the GitLab OAuth settings for student login.
type GitLabAuthConfig struct {
	// BaseURL is the GitLab instance, e.g. https://gitlab.example.org. Empty means gitlab.com.
	BaseURL string `json:"base_url,omitempty"`
	// ClientID is the "Application ID" of the GitLab application.
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	// AllowedGroups restricts sign-in to members of at least one of these
	// groups (full paths). Empty means any account on the instance may sign in.
	AllowedGroups []string `json:"allowed_groups,omitempty"`
	// DisableClassicLogin hides the password login form for students (GitLab login must be enabled).
	DisableClassicLogin bool `json:"disable_classic_login,omitempty"`
}

// Enabled reports whether the config is complete enough to offer GitLab login.
func (c GitLabAuthConfig) Enabled() bool {
	return c.ClientID != "" && c.ClientSecret != ""
}

// EffectiveBaseURL returns the configured instance URL, or gitlab.com.
func (c GitLabAuthConfig) EffectiveBaseURL() string {
	if c.BaseURL == "" {
		return gitlabDefaultBaseURL
	}
	return c.BaseURL
}

// GitLabAuthStore persists the GitLab OAuth settings to disk. The client
// secret is encrypted at rest with the same key as persisted job files.
type GitLabAuthStore struct {
	config  GitLabAuthConfig
	dataDir string
	mu      sync.RWMutex
}

// NewGitLabAuthStore creates a store and loads persisted config from disk.
func NewGitLabAuthStore(dataDir string) *GitLabAuthStore {
	s := &GitLabAuthStore{dataDir: dataDir}
	if err := s.load(); err != nil {
		log.Printf("[GITLAB-AUTH] Warning: failed to load config: %v", err)
	}
	return s
}

func (s *GitLabAuthStore) configPath() string {
	return filepath.Join(s.dataDir, "gitlab-auth.json")
}

// load reads the config from disk. Missing file is not an error.
func (s *GitLabAuthStore) load() error {
	if s.dataDir == "" {
		return nil
	}
	data, err := os.ReadFile(s.configPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read gitlab-auth config: %w", err)
	}
	var cfg GitLabAuthConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("failed to parse gitlab-auth config: %w", err)
	}
	secret, err := decryptSecret(cfg.ClientSecret)
	if err != nil {
		return fmt.Errorf("failed to decrypt gitlab-auth client secret: %w", err)
	}
	cfg.ClientSecret = secret

	s.mu.Lock()
	s.config = cfg
	s.mu.Unlock()
	return nil
}

// Get returns the current GitLab OAuth configuration.
func (s *GitLabAuthStore) Get() GitLabAuthConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cfg := s.config
	cfg.AllowedGroups = append([]string(nil), s.config.AllowedGroups...)
	return cfg
}

// Set updates the GitLab OAuth configuration and persists it.
func (s *GitLabAuthStore) Set(cfg GitLabAuthConfig) error {
	cfg.AllowedGroups = append([]string(nil), cfg.AllowedGroups...)

	if s.dataDir != "" {
		onDisk := cfg
		secret, err := encryptSecret(cfg.ClientSecret)
		if err != nil {
			return fmt.Errorf("failed to encrypt gitlab-auth client secret: %w", err)
		}
		onDisk.ClientSecret = secret

		data, err := json.MarshalIndent(onDisk, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal gitlab-auth config: %w", err)
		}
		if err := os.MkdirAll(s.dataDir, 0755); err != nil {
			return fmt.Errorf("failed to create data dir: %w", err)
		}
		if err := os.WriteFile(s.configPath(), data, 0600); err != nil {
			return fmt.Errorf("failed to write gitlab-auth config: %w", err)
		}
	}

	s.mu.Lock()
	s.config = cfg
	s.mu.Unlock()
	return nil
}

// normalizeGitLabURL validates a GitLab instance URL and returns it without a
// trailing slash. Empty input means gitlab.com. A path prefix is kept, for
// instances served under a relative URL.
func normalizeGitLabURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return gitlabDefaultBaseURL, nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid GitLab URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("GitLab URL must start with http:// or https://")
	}
	if u.Hostname() == "" {
		return "", fmt.Errorf("GitLab URL has no host")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("GitLab URL must not contain credentials, a query or a fragment")
	}
	return u.Scheme + "://" + strings.ToLower(u.Host) + strings.TrimRight(u.EscapedPath(), "/"), nil
}

// gitlabHost returns the lowercased hostname (no port) of a normalized GitLab URL.
func gitlabHost(baseURL string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// parseGitLabGroups splits a comma/whitespace separated list of group paths,
// lowercases and de-duplicates them, and rejects invalid paths.
func parseGitLabGroups(raw string) ([]string, error) {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	var groups []string
	seen := make(map[string]bool)
	for _, f := range fields {
		group := strings.ToLower(strings.Trim(f, "/"))
		if group == "" || len(group) > 255 {
			return nil, fmt.Errorf("invalid GitLab group path %q", f)
		}
		for _, segment := range strings.Split(group, "/") {
			if !gitlabGroupSegmentRegex.MatchString(segment) {
				return nil, fmt.Errorf("invalid GitLab group path %q", f)
			}
		}
		if seen[group] {
			continue
		}
		seen[group] = true
		groups = append(groups, group)
	}
	return groups, nil
}

// ConfigureGitLab updates the GitLab OAuth config at runtime. A config without
// an application ID or secret disables GitLab login.
func (ah *AuthHandler) ConfigureGitLab(cfg GitLabAuthConfig) {
	ah.mu.Lock()
	defer ah.mu.Unlock()

	if !cfg.Enabled() {
		ah.gitlabConfig = nil
		ah.gitlabEnabled = false
		ah.gitlabBaseURL = ""
		ah.gitlabAllowedGroups = nil
		ah.gitlabClassicLoginDisabled = false
		log.Printf("GitLab student login disabled")
		return
	}

	baseURL := cfg.EffectiveBaseURL()
	ah.gitlabConfig = &oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		// openid is enough for /oauth/userinfo, which carries both the
		// username and the user's groups — no API scope is requested.
		Scopes: []string{"openid"},
		Endpoint: oauth2.Endpoint{
			AuthURL:   baseURL + "/oauth/authorize",
			TokenURL:  baseURL + "/oauth/token",
			AuthStyle: oauth2.AuthStyleInParams,
		},
	}
	ah.gitlabEnabled = true
	ah.gitlabBaseURL = baseURL
	ah.gitlabAllowedGroups = append([]string(nil), cfg.AllowedGroups...)
	ah.gitlabClassicLoginDisabled = cfg.DisableClassicLogin
	log.Printf("GitLab student login configured (instance: %s, allowed groups: %d)", baseURL, len(cfg.AllowedGroups))
}

// GitLabEnabled reports whether GitLab login is currently enabled.
func (ah *AuthHandler) GitLabEnabled() bool {
	ah.mu.RLock()
	defer ah.mu.RUnlock()
	return ah.gitlabEnabled
}

// gitlabCallbackURL builds the OAuth callback URL for the host the request came in on.
func gitlabCallbackURL(r *http.Request) string {
	scheme := "http"
	if isSecureRequest(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host + gitlabCallbackPath
}

// gitlabLoginError sends the student back to the login page with a safe message.
func gitlabLoginError(w http.ResponseWriter, r *http.Request, message string) {
	http.Redirect(w, r, studentLoginURL(r)+"?error="+url.QueryEscape(message), http.StatusSeeOther)
}

// HandleGitLabLogin initiates the GitLab OAuth 2.0 flow for student login.
func (ah *AuthHandler) HandleGitLabLogin(w http.ResponseWriter, r *http.Request) {
	state := generateToken()

	ah.mu.Lock()
	cfg := ah.gitlabConfig
	if ah.gitlabEnabled && cfg != nil {
		if ah.gitlabOAuthStates == nil {
			ah.gitlabOAuthStates = make(map[string]time.Time)
		}
		ah.gitlabOAuthStates[state] = time.Now().Add(azureOAuthStateExpiry)
	}
	ah.mu.Unlock()

	if cfg == nil {
		gitlabLoginError(w, r, "GitLab sign-in is not available.")
		return
	}
	// A sign-in run for an in-lab student portal ends there, not here.
	if !ah.beginBrokeredLogin(w, r, state) {
		return
	}

	// Bind the state to this browser: the callback must present the same value
	// in this cookie, so a state issued to someone else cannot be replayed here.
	// Lax (not Strict) so the cookie is sent on the redirect back from GitLab.
	http.SetCookie(w, &http.Cookie{
		Name:     gitlabOAuthStateCookieName,
		Value:    state,
		Path:     gitlabOAuthCookiePath,
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(azureOAuthStateExpiry.Seconds()),
	})

	authURL := cfg.AuthCodeURL(state, oauth2.SetAuthURLParam("redirect_uri", gitlabCallbackURL(r)))
	http.Redirect(w, r, authURL, http.StatusFound)
}

// HandleGitLabCallback handles the OAuth 2.0 callback from GitLab. It exchanges
// the authorization code for a token, reads the username and groups from the
// userinfo endpoint, checks group membership when a restriction is configured,
// and creates a student session identified as <username>@users.noreply.<gitlab host>.
func (ah *AuthHandler) HandleGitLabCallback(w http.ResponseWriter, r *http.Request) {
	// A sign-in run for an in-lab student portal answers that portal, errors included.
	r = ah.resumeBrokeredLogin(r)

	ah.mu.RLock()
	cfg := ah.gitlabConfig
	baseURL := ah.gitlabBaseURL
	allowedGroups := append([]string(nil), ah.gitlabAllowedGroups...)
	ah.mu.RUnlock()

	if cfg == nil {
		gitlabLoginError(w, r, "GitLab sign-in is not available.")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     gitlabOAuthStateCookieName,
		Value:    "",
		Path:     gitlabOAuthCookiePath,
		HttpOnly: true,
		MaxAge:   -1,
	})

	if errParam := r.URL.Query().Get("error"); errParam != "" {
		log.Printf("GitLab OAuth error: %q", errParam)
		if errParam == "access_denied" {
			gitlabLoginError(w, r, "GitLab sign-in was cancelled.")
			return
		}
		gitlabLoginError(w, r, "GitLab sign-in failed. Try again, or ask your instructor.")
		return
	}

	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")

	// Validate state to prevent CSRF: known, unexpired, single use, and issued to this browser.
	ah.mu.Lock()
	expiry, valid := ah.gitlabOAuthStates[state]
	if valid {
		delete(ah.gitlabOAuthStates, state)
	}
	ah.mu.Unlock()

	stateCookie, cookieErr := r.Cookie(gitlabOAuthStateCookieName)
	if !valid || time.Now().After(expiry) || cookieErr != nil ||
		subtle.ConstantTimeCompare([]byte(stateCookie.Value), []byte(state)) != 1 {
		log.Printf("GitLab callback: invalid or expired state")
		gitlabLoginError(w, r, "That sign-in attempt expired. Try again.")
		return
	}

	if code == "" {
		log.Printf("GitLab callback: missing authorization code")
		gitlabLoginError(w, r, "GitLab sign-in failed. Try again, or ask your instructor.")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), gitlabAPITimeout)
	defer cancel()

	token, err := cfg.Exchange(ctx, code, oauth2.SetAuthURLParam("redirect_uri", gitlabCallbackURL(r)))
	if err != nil {
		log.Printf("GitLab token exchange failed: %v", err)
		gitlabLoginError(w, r, "GitLab sign-in failed. Try again, or ask your instructor.")
		return
	}

	username, groups, err := fetchGitLabUserInfo(ctx, baseURL, token.AccessToken)
	if err != nil {
		log.Printf("GitLab: failed to read user info: %v", err)
		gitlabLoginError(w, r, "GitLab sign-in failed. Try again, or ask your instructor.")
		return
	}

	if len(allowedGroups) > 0 && !inAllowedGitLabGroup(groups, allowedGroups) {
		log.Printf("GitLab login rejected: %s is not a member of an allowed group", username)
		gitlabLoginError(w, r, "Your GitLab account isn't in a group allowed for this workshop. Ask your instructor for access.")
		return
	}

	ah.completeStudentLogin(w, r, username+"@users.noreply."+gitlabHost(baseURL), "GitLab")
}

// fetchGitLabUserInfo returns the lowercased username of the token's owner and
// the full paths of the groups they belong to (directly or through a parent group).
func fetchGitLabUserInfo(ctx context.Context, baseURL, accessToken string) (string, []string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/oauth/userinfo", nil)
	if err != nil {
		return "", nil, fmt.Errorf("failed to build GitLab request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", nil, fmt.Errorf("failed to call GitLab userinfo: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", nil, fmt.Errorf("failed to read GitLab userinfo: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("GitLab userinfo returned status %d", resp.StatusCode)
	}

	var info struct {
		Nickname string   `json:"nickname"`
		Groups   []string `json:"groups"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return "", nil, fmt.Errorf("failed to parse GitLab userinfo: %w", err)
	}
	username := strings.ToLower(info.Nickname)
	if !gitlabUsernameRegex.MatchString(username) {
		return "", nil, fmt.Errorf("GitLab returned an unusable username %q", info.Nickname)
	}
	return username, info.Groups, nil
}

// inAllowedGitLabGroup reports whether any of the user's groups is one of the
// allowed group paths. GitLab paths are case-insensitive.
func inAllowedGitLabGroup(userGroups, allowed []string) bool {
	for _, g := range userGroups {
		for _, a := range allowed {
			if strings.EqualFold(g, a) {
				return true
			}
		}
	}
	return false
}

// SetGitLabAuth wires the GitLab login settings store, and a callback so the
// handler can update the live GitLab OAuth config at runtime.
func (h *Handler) SetGitLabAuth(store *GitLabAuthStore, apply func(GitLabAuthConfig)) {
	h.gitlabAuthStore = store
	h.gitlabAuthConfigurer = apply
}

// gitlabAuthView is the data rendered by the GitLab login admin page. It
// deliberately carries no client secret.
type gitlabAuthView struct {
	Enabled             bool
	BaseURL             string
	Host                string
	Insecure            bool
	ClientID            string
	HasSecret           bool
	AllowedGroups       string
	DisableClassicLogin bool
	CallbackURL         string
}

// ServeGitLabAuth serves the GitLab login configuration admin page.
func (h *Handler) ServeGitLabAuth(w http.ResponseWriter, r *http.Request) {
	if h.gitlabAuthStore == nil {
		http.Error(w, "GitLab login settings not available", http.StatusServiceUnavailable)
		return
	}
	cfg := h.gitlabAuthStore.Get()
	baseURL := cfg.EffectiveBaseURL()
	host := baseURL
	if u, err := url.Parse(baseURL); err == nil {
		host = u.Host + u.Path
	}
	h.serveTemplate(w, "gitlab-auth.html", gitlabAuthView{
		Enabled:             cfg.Enabled(),
		BaseURL:             baseURL,
		Host:                host,
		Insecure:            cfg.Enabled() && strings.HasPrefix(baseURL, "http://"),
		ClientID:            cfg.ClientID,
		HasSecret:           cfg.ClientSecret != "",
		AllowedGroups:       strings.Join(cfg.AllowedGroups, ", "),
		DisableClassicLogin: cfg.DisableClassicLogin,
		CallbackURL:         gitlabCallbackURL(r),
	})
}

// SaveGitLabAuthConfig handles saving the GitLab login configuration from the admin page.
func (h *Handler) SaveGitLabAuthConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.gitlabAuthStore == nil {
		http.Error(w, "GitLab login settings not available", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	if err := r.ParseForm(); err != nil {
		fmt.Fprint(w, `<div class="error-message"><p>Failed to parse form</p></div>`)
		return
	}

	clientID := strings.TrimSpace(r.FormValue("gitlab_client_id"))
	clientSecret := strings.TrimSpace(r.FormValue("gitlab_client_secret"))
	disableClassic := r.FormValue("gitlab_disable_classic_login") == "on"

	baseURL, err := normalizeGitLabURL(r.FormValue("gitlab_url"))
	if err != nil {
		log.Printf("SaveGitLabAuthConfig: %v", err)
		fmt.Fprint(w, `<div class="error-message"><p>The GitLab URL is not valid. Enter the address of the instance, starting with https://, for example https://gitlab.example.org.</p></div>`)
		return
	}

	groups, err := parseGitLabGroups(r.FormValue("gitlab_allowed_groups"))
	if err != nil {
		log.Printf("SaveGitLabAuthConfig: %v", err)
		fmt.Fprint(w, `<div class="error-message"><p>One of the group paths is not valid. Use the path from the group's GitLab URL (for example my-group/sub-group), separated by commas.</p></div>`)
		return
	}

	var cfg GitLabAuthConfig
	if clientID != "" {
		// Preserve existing secret when the field is left blank (password fields submit empty on re-display)
		if clientSecret == "" {
			clientSecret = h.gitlabAuthStore.Get().ClientSecret
		}
		if clientSecret == "" {
			fmt.Fprint(w, `<div class="error-message"><p>Enter the secret to turn on GitLab login.</p></div>`)
			return
		}
		cfg = GitLabAuthConfig{
			BaseURL:             baseURL,
			ClientID:            clientID,
			ClientSecret:        clientSecret,
			AllowedGroups:       groups,
			DisableClassicLogin: disableClassic,
		}
	}

	if err := h.gitlabAuthStore.Set(cfg); err != nil {
		log.Printf("SaveGitLabAuthConfig: failed to persist: %v", err)
		fmt.Fprint(w, `<div class="error-message"><p>Failed to save GitLab login settings. Check the server logs for details.</p></div>`)
		return
	}

	if h.gitlabAuthConfigurer != nil {
		h.gitlabAuthConfigurer(cfg)
	}
	// In-lab student portals mirror these settings.
	go h.syncAllPortals()

	detail := "disabled"
	if cfg.Enabled() {
		detail = "enabled, " + gitlabHost(cfg.BaseURL) + ", any account"
		if len(cfg.AllowedGroups) > 0 {
			detail = "enabled, " + gitlabHost(cfg.BaseURL) + ", groups: " + strings.Join(cfg.AllowedGroups, ", ")
		}
	}
	h.recordAudit(adminActor(r), "admin", "gitlab_auth.update", "", detail)

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", "/admin/gitlab")
		fmt.Fprint(w, `<div class="success-message"><p>GitLab login settings saved</p></div>`)
	} else {
		http.Redirect(w, r, "/admin/gitlab", http.StatusSeeOther)
	}
}
