package server

import (
	"bytes"
	"encoding/json"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

const testGitHubSecret = "gh-test-client-secret-value"

// fakeGitHub is an httptest stand-in for github.com's token endpoint and the
// two REST API calls the callback makes.
type fakeGitHub struct {
	login       string
	memberships map[string]string // org -> membership state
	tokenStatus int
	userStatus  int
}

func (f *fakeGitHub) start(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		if f.tokenStatus != 0 {
			http.Error(w, "bad code", f.tokenStatus)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"access_token": "gho_test", "token_type": "bearer"})
	})
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer gho_test" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if f.userStatus != 0 {
			http.Error(w, "boom", f.userStatus)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"login": f.login})
	})
	mux.HandleFunc("/user/memberships/orgs/", func(w http.ResponseWriter, r *http.Request) {
		org := strings.TrimPrefix(r.URL.Path, "/user/memberships/orgs/")
		state, ok := f.memberships[org]
		if !ok {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"state": state})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// newGitHubTestAuthHandler returns an AuthHandler with GitHub login enabled and
// pointed at srv instead of github.com.
func newGitHubTestAuthHandler(srv *httptest.Server, orgs []string) *AuthHandler {
	ah := createTestAuthHandler()
	ah.githubEndpoint = oauth2.Endpoint{
		AuthURL:  srv.URL + "/login/oauth/authorize",
		TokenURL: srv.URL + "/login/oauth/access_token",
	}
	ah.githubAPIBase = srv.URL
	ah.ConfigureGitHub(GitHubAuthConfig{ClientID: "client-id", ClientSecret: testGitHubSecret, AllowedOrgs: orgs})
	return ah
}

// startGitHubFlow runs the login redirect and returns the issued state and its cookie.
func startGitHubFlow(t *testing.T, ah *AuthHandler) (string, *http.Cookie) {
	t.Helper()
	w := httptest.NewRecorder()
	ah.HandleGitHubLogin(w, httptest.NewRequest("GET", "/student/auth/github/login", nil))
	require.Equal(t, http.StatusFound, w.Code)

	loc, err := url.Parse(w.Header().Get("Location"))
	require.NoError(t, err)
	state := loc.Query().Get("state")
	require.NotEmpty(t, state)

	for _, c := range w.Result().Cookies() {
		if c.Name == githubOAuthStateCookieName {
			return state, c
		}
	}
	t.Fatal("state cookie not set")
	return "", nil
}

func studentSessionEmails(ah *AuthHandler) []string {
	ah.mu.RLock()
	defer ah.mu.RUnlock()
	var emails []string
	for _, s := range ah.studentSessions {
		emails = append(emails, s.Email)
	}
	return emails
}

func TestParseGitHubOrgs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    string
		expected []string
		wantErr  bool
	}{
		{name: "empty", input: "", expected: nil},
		{name: "single", input: "my-org", expected: []string{"my-org"}},
		{name: "commas and spaces", input: " my-org, Other-Org ,third", expected: []string{"my-org", "other-org", "third"}},
		{name: "newlines and duplicates", input: "a\nb\nA", expected: []string{"a", "b"}},
		{name: "leading at sign", input: "@my-org", expected: []string{"my-org"}},
		{name: "slash rejected", input: "my-org/team", wantErr: true},
		{name: "leading hyphen rejected", input: "-org", wantErr: true},
		{name: "markup rejected", input: "<script>", wantErr: true},
		{name: "too long rejected", input: strings.Repeat("a", 40), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseGitHubOrgs(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestGitHubAuthStore_RoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := GitHubAuthConfig{ClientID: "id", ClientSecret: testGitHubSecret, AllowedOrgs: []string{"a", "b"}, DisableClassicLogin: true}

	require.NoError(t, NewGitHubAuthStore(dir).Set(cfg))

	info, err := os.Stat(filepath.Join(dir, "github-auth.json"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())

	assert.Equal(t, cfg, NewGitHubAuthStore(dir).Get())
}

func TestGitHubAuthStore_MissingFileAndEmptyDir(t *testing.T) {
	t.Parallel()
	assert.False(t, NewGitHubAuthStore(t.TempDir()).Get().Enabled())

	// No data dir: nothing is persisted, but the value is still held in memory.
	s := NewGitHubAuthStore("")
	require.NoError(t, s.Set(GitHubAuthConfig{ClientID: "id", ClientSecret: "s"}))
	assert.True(t, s.Get().Enabled())
}

func TestGitHubAuthStore_GetReturnsCopy(t *testing.T) {
	t.Parallel()
	s := NewGitHubAuthStore("")
	require.NoError(t, s.Set(GitHubAuthConfig{ClientID: "id", ClientSecret: "s", AllowedOrgs: []string{"a"}}))
	s.Get().AllowedOrgs[0] = "mutated"
	assert.Equal(t, []string{"a"}, s.Get().AllowedOrgs)
}

// Not parallel: toggles the package-level at-rest encryption key.
func TestGitHubAuthStore_SecretEncryptedAtRest(t *testing.T) {
	require.NoError(t, InitDataEncryption(bytes.Repeat([]byte{7}, 32)))
	t.Cleanup(func() { _ = InitDataEncryption(nil) })

	dir := t.TempDir()
	require.NoError(t, NewGitHubAuthStore(dir).Set(GitHubAuthConfig{ClientID: "id", ClientSecret: testGitHubSecret}))

	raw, err := os.ReadFile(filepath.Join(dir, "github-auth.json"))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), testGitHubSecret)
	assert.Contains(t, string(raw), encPrefix)

	assert.Equal(t, testGitHubSecret, NewGitHubAuthStore(dir).Get().ClientSecret)
}

func TestAuthHandler_ConfigureGitHub(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		cfg         GitHubAuthConfig
		wantEnabled bool
		wantScopes  []string
	}{
		{name: "complete without orgs", cfg: GitHubAuthConfig{ClientID: "id", ClientSecret: "s"}, wantEnabled: true, wantScopes: nil},
		{name: "complete with orgs", cfg: GitHubAuthConfig{ClientID: "id", ClientSecret: "s", AllowedOrgs: []string{"a"}}, wantEnabled: true, wantScopes: []string{"read:org"}},
		{name: "missing secret", cfg: GitHubAuthConfig{ClientID: "id"}, wantEnabled: false},
		{name: "missing client id", cfg: GitHubAuthConfig{ClientSecret: "s"}, wantEnabled: false},
		{name: "empty", cfg: GitHubAuthConfig{}, wantEnabled: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ah := createTestAuthHandler()
			ah.ConfigureGitHub(tt.cfg)
			assert.Equal(t, tt.wantEnabled, ah.GitHubEnabled())
			if !tt.wantEnabled {
				assert.Nil(t, ah.githubConfig)
				return
			}
			require.NotNil(t, ah.githubConfig)
			assert.Equal(t, tt.wantScopes, ah.githubConfig.Scopes)
			assert.Contains(t, ah.githubConfig.Endpoint.AuthURL, "github.com")
		})
	}
}

func TestAuthHandler_ConfigureGitHub_DisableClearsState(t *testing.T) {
	t.Parallel()
	ah := createTestAuthHandler()
	ah.ConfigureGitHub(GitHubAuthConfig{ClientID: "id", ClientSecret: "s", AllowedOrgs: []string{"a"}, DisableClassicLogin: true})
	ah.ConfigureGitHub(GitHubAuthConfig{})

	assert.False(t, ah.GitHubEnabled())
	assert.Empty(t, ah.githubAllowedOrgs)
	assert.False(t, ah.githubClassicLoginDisabled)
}

func TestHandleGitHubLogin(t *testing.T) {
	t.Parallel()

	t.Run("disabled redirects to login with an error", func(t *testing.T) {
		t.Parallel()
		ah := createTestAuthHandler()
		w := httptest.NewRecorder()
		ah.HandleGitHubLogin(w, httptest.NewRequest("GET", "/student/auth/github/login", nil))

		assert.Equal(t, http.StatusSeeOther, w.Code)
		assert.Contains(t, w.Header().Get("Location"), "/student/login?error=")
		assert.Empty(t, w.Result().Cookies())
	})

	t.Run("enabled redirects to GitHub with state and callback", func(t *testing.T) {
		t.Parallel()
		ah := createTestAuthHandler()
		ah.ConfigureGitHub(GitHubAuthConfig{ClientID: "client-id", ClientSecret: testGitHubSecret, AllowedOrgs: []string{"a"}})

		req := httptest.NewRequest("GET", "http://lab.example.org/student/auth/github/login", nil)
		req.Header.Set("X-Forwarded-Proto", "https")
		w := httptest.NewRecorder()
		ah.HandleGitHubLogin(w, req)

		require.Equal(t, http.StatusFound, w.Code)
		loc, err := url.Parse(w.Header().Get("Location"))
		require.NoError(t, err)
		assert.Equal(t, "github.com", loc.Host)
		q := loc.Query()
		assert.Equal(t, "client-id", q.Get("client_id"))
		assert.Equal(t, "read:org", q.Get("scope"))
		assert.Equal(t, "https://lab.example.org/student/auth/github/callback", q.Get("redirect_uri"))
		assert.NotContains(t, w.Header().Get("Location"), testGitHubSecret)

		cookies := w.Result().Cookies()
		require.Len(t, cookies, 1)
		assert.Equal(t, githubOAuthStateCookieName, cookies[0].Name)
		assert.Equal(t, q.Get("state"), cookies[0].Value)
		assert.True(t, cookies[0].HttpOnly)
		assert.True(t, cookies[0].Secure)
		assert.Equal(t, http.SameSiteLaxMode, cookies[0].SameSite)
	})
}

func TestHandleGitHubCallback(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		github fakeGitHub
		orgs   []string
		// mutate adjusts the callback request built from a valid flow.
		mutate        func(q url.Values, cookie *http.Cookie)
		omitCookie    bool
		wantEmail     string // empty: no session must be created
		wantErrorText string
	}{
		{
			name:      "any account when no org restriction",
			github:    fakeGitHub{login: "octocat"},
			wantEmail: "octocat@users.noreply.github.com",
		},
		{
			name:      "login is lowercased",
			github:    fakeGitHub{login: "OctoCat"},
			wantEmail: "octocat@users.noreply.github.com",
		},
		{
			name:      "active member of the allowed org",
			github:    fakeGitHub{login: "octocat", memberships: map[string]string{"my-org": "active"}},
			orgs:      []string{"my-org"},
			wantEmail: "octocat@users.noreply.github.com",
		},
		{
			name:      "member of the second allowed org",
			github:    fakeGitHub{login: "octocat", memberships: map[string]string{"other": "active"}},
			orgs:      []string{"my-org", "other"},
			wantEmail: "octocat@users.noreply.github.com",
		},
		{
			name:          "not a member",
			github:        fakeGitHub{login: "octocat"},
			orgs:          []string{"my-org"},
			wantErrorText: "isn't in an organization allowed",
		},
		{
			name:          "pending invitation does not count",
			github:        fakeGitHub{login: "octocat", memberships: map[string]string{"my-org": "pending"}},
			orgs:          []string{"my-org"},
			wantErrorText: "isn't in an organization allowed",
		},
		{
			name:          "unknown state",
			github:        fakeGitHub{login: "octocat"},
			mutate:        func(q url.Values, c *http.Cookie) { q.Set("state", "forged"); c.Value = "forged" },
			wantErrorText: "expired",
		},
		{
			name:          "state cookie mismatch",
			github:        fakeGitHub{login: "octocat"},
			mutate:        func(q url.Values, c *http.Cookie) { c.Value = "someone-elses-browser" },
			wantErrorText: "expired",
		},
		{
			name:          "state cookie missing",
			github:        fakeGitHub{login: "octocat"},
			omitCookie:    true,
			wantErrorText: "expired",
		},
		{
			name:          "student cancelled on GitHub",
			github:        fakeGitHub{login: "octocat"},
			mutate:        func(q url.Values, c *http.Cookie) { q.Set("error", "access_denied") },
			wantErrorText: "cancelled",
		},
		{
			name:          "other OAuth error",
			github:        fakeGitHub{login: "octocat"},
			mutate:        func(q url.Values, c *http.Cookie) { q.Set("error", "redirect_uri_mismatch") },
			wantErrorText: "GitHub sign-in failed",
		},
		{
			name:          "missing code",
			github:        fakeGitHub{login: "octocat"},
			mutate:        func(q url.Values, c *http.Cookie) { q.Del("code") },
			wantErrorText: "GitHub sign-in failed",
		},
		{
			name:          "token exchange fails",
			github:        fakeGitHub{login: "octocat", tokenStatus: http.StatusBadRequest},
			wantErrorText: "GitHub sign-in failed",
		},
		{
			name:          "user lookup fails",
			github:        fakeGitHub{login: "octocat", userStatus: http.StatusInternalServerError},
			wantErrorText: "GitHub sign-in failed",
		},
		{
			name:          "unusable login",
			github:        fakeGitHub{login: "evil@example.com"},
			wantErrorText: "GitHub sign-in failed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gh := tt.github
			srv := gh.start(t)
			ah := newGitHubTestAuthHandler(srv, tt.orgs)
			state, cookie := startGitHubFlow(t, ah)

			q := url.Values{"state": {state}, "code": {"the-code"}}
			if tt.mutate != nil {
				tt.mutate(q, cookie)
			}
			req := httptest.NewRequest("GET", "/student/auth/github/callback?"+q.Encode(), nil)
			if !tt.omitCookie {
				req.AddCookie(cookie)
			}
			w := httptest.NewRecorder()
			ah.HandleGitHubCallback(w, req)

			require.Equal(t, http.StatusSeeOther, w.Code)
			location, err := url.QueryUnescape(w.Header().Get("Location"))
			require.NoError(t, err)

			if tt.wantEmail == "" {
				assert.Contains(t, location, "/student/login?error=")
				assert.Contains(t, location, tt.wantErrorText)
				assert.NotContains(t, location, srv.URL, "error must not expose internal details")
				assert.Empty(t, studentSessionEmails(ah))
				return
			}

			assert.Equal(t, "/student/dashboard", location)
			assert.Equal(t, []string{tt.wantEmail}, studentSessionEmails(ah))
			assert.Equal(t, "octocat", usernameFromEmail(tt.wantEmail))

			var sessionCookie *http.Cookie
			for _, c := range w.Result().Cookies() {
				if c.Name == StudentSessionCookieName {
					sessionCookie = c
				}
			}
			require.NotNil(t, sessionCookie)
			assert.True(t, sessionCookie.HttpOnly)
			assert.True(t, ah.validateStudentSession(sessionCookie.Value))
		})
	}
}

func TestHandleGitHubCallback_StateIsSingleUse(t *testing.T) {
	t.Parallel()
	gh := fakeGitHub{login: "octocat"}
	ah := newGitHubTestAuthHandler(gh.start(t), nil)
	state, cookie := startGitHubFlow(t, ah)

	call := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/student/auth/github/callback?state="+state+"&code=c", nil)
		req.AddCookie(cookie)
		w := httptest.NewRecorder()
		ah.HandleGitHubCallback(w, req)
		return w
	}

	assert.Equal(t, "/student/dashboard", call().Header().Get("Location"))
	assert.Contains(t, call().Header().Get("Location"), "/student/login?error=")
	assert.Len(t, studentSessionEmails(ah), 1)
}

func TestHandleGitHubCallback_ExpiredState(t *testing.T) {
	t.Parallel()
	gh := fakeGitHub{login: "octocat"}
	ah := newGitHubTestAuthHandler(gh.start(t), nil)
	state, cookie := startGitHubFlow(t, ah)

	ah.mu.Lock()
	ah.githubOAuthStates[state] = time.Now().Add(-time.Minute)
	ah.mu.Unlock()

	req := httptest.NewRequest("GET", "/student/auth/github/callback?state="+state+"&code=c", nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	ah.HandleGitHubCallback(w, req)

	assert.Contains(t, w.Header().Get("Location"), "/student/login?error=")
	assert.Empty(t, studentSessionEmails(ah))
}

func TestHandleGitHubCallback_WhenDisabled(t *testing.T) {
	t.Parallel()
	ah := createTestAuthHandler()
	w := httptest.NewRecorder()
	ah.HandleGitHubCallback(w, httptest.NewRequest("GET", "/student/auth/github/callback?state=x&code=y", nil))

	assert.Equal(t, http.StatusSeeOther, w.Code)
	assert.Contains(t, w.Header().Get("Location"), "/student/login?error=")
}

func TestRequireStudentAuth_GitHubOnlyEnabled(t *testing.T) {
	t.Parallel()
	ah := createTestAuthHandler()
	ah.studentPasswordHash = ""

	called := false
	next := ah.RequireStudentAuth(func(w http.ResponseWriter, r *http.Request) { called = true })

	// No login method at all: the portal is closed.
	w := httptest.NewRecorder()
	next(w, createStudentAuthenticatedRequest("GET", "/student/dashboard", ah))
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.False(t, called)

	ah.ConfigureGitHub(GitHubAuthConfig{ClientID: "id", ClientSecret: "s"})
	w = httptest.NewRecorder()
	next(w, createStudentAuthenticatedRequest("GET", "/student/dashboard", ah))
	assert.Equal(t, http.StatusOK, w.Code)
	assert.True(t, called)
}

func TestHandleStudentLogin_ClassicLoginDisabledByGitHub(t *testing.T) {
	t.Parallel()
	post := func(ah *AuthHandler) string {
		form := url.Values{"password_hash": {"any-hash"}, "email": {"s@example.com"}}
		req := httptest.NewRequest("POST", "/student/login", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		ah.HandleStudentLogin(w, req)
		loc, _ := url.QueryUnescape(w.Header().Get("Location"))
		return loc
	}

	ah := createTestAuthHandler()
	ah.ConfigureGitHub(GitHubAuthConfig{ClientID: "id", ClientSecret: "s", DisableClassicLogin: true})
	assert.Contains(t, post(ah), "Password login is disabled")

	// Turning GitHub login off must give the password form back, or nobody could sign in.
	ah.ConfigureGitHub(GitHubAuthConfig{})
	assert.Contains(t, post(ah), "Invalid password")
}

// newHandlerWithGitHubAuth returns a Handler wired to a GitHub settings store,
// plus a pointer to the last config pushed to the (fake) auth handler.
func newHandlerWithGitHubAuth(t *testing.T) (*Handler, *GitHubAuthConfig) {
	t.Helper()
	h := newHandlerWithOptions(t)
	applied := &GitHubAuthConfig{}
	h.SetGitHubAuth(NewGitHubAuthStore(t.TempDir()), func(cfg GitHubAuthConfig) { *applied = cfg })
	return h, applied
}

func postGitHubAuthForm(h *Handler, form url.Values, htmx bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/api/github-auth-config", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if htmx {
		req.Header.Set("HX-Request", "true")
	}
	w := httptest.NewRecorder()
	h.SaveGitHubAuthConfig(w, req)
	return w
}

func TestHandler_SaveGitHubAuthConfig(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		// existing is saved before the request, to test updates.
		existing     *GitHubAuthConfig
		form         url.Values
		expected     GitHubAuthConfig
		wantErrorMsg string
	}{
		{
			name:     "turn on for any account",
			form:     url.Values{"github_client_id": {" id "}, "github_client_secret": {testGitHubSecret}},
			expected: GitHubAuthConfig{ClientID: "id", ClientSecret: testGitHubSecret},
		},
		{
			name: "turn on with orgs and password sign-in off",
			form: url.Values{
				"github_client_id": {"id"}, "github_client_secret": {testGitHubSecret},
				"github_allowed_orgs": {"My-Org, other"}, "github_disable_classic_login": {"on"},
			},
			expected: GitHubAuthConfig{ClientID: "id", ClientSecret: testGitHubSecret, AllowedOrgs: []string{"my-org", "other"}, DisableClassicLogin: true},
		},
		{
			name:     "blank secret keeps the saved one",
			existing: &GitHubAuthConfig{ClientID: "id", ClientSecret: testGitHubSecret},
			form:     url.Values{"github_client_id": {"id"}, "github_allowed_orgs": {"my-org"}},
			expected: GitHubAuthConfig{ClientID: "id", ClientSecret: testGitHubSecret, AllowedOrgs: []string{"my-org"}},
		},
		{
			name:     "empty client ID turns it off and clears everything",
			existing: &GitHubAuthConfig{ClientID: "id", ClientSecret: testGitHubSecret, DisableClassicLogin: true},
			form:     url.Values{"github_client_id": {""}, "github_allowed_orgs": {"my-org"}, "github_disable_classic_login": {"on"}},
			expected: GitHubAuthConfig{},
		},
		{
			name:         "client ID without any secret",
			form:         url.Values{"github_client_id": {"id"}},
			expected:     GitHubAuthConfig{},
			wantErrorMsg: "Enter the client secret",
		},
		{
			name:         "invalid organization name leaves saved config untouched",
			existing:     &GitHubAuthConfig{ClientID: "id", ClientSecret: testGitHubSecret},
			form:         url.Values{"github_client_id": {"id"}, "github_allowed_orgs": {"<script>alert(1)</script>"}},
			expected:     GitHubAuthConfig{ClientID: "id", ClientSecret: testGitHubSecret},
			wantErrorMsg: "not valid",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h, applied := newHandlerWithGitHubAuth(t)
			if tt.existing != nil {
				require.NoError(t, h.githubAuthStore.Set(*tt.existing))
				*applied = *tt.existing
			}

			w := postGitHubAuthForm(h, tt.form, true)
			body := w.Body.String()

			assert.NotContains(t, body, testGitHubSecret)
			assert.NotContains(t, body, "<script>")
			assert.Equal(t, tt.expected, h.githubAuthStore.Get())
			assert.Equal(t, tt.expected, *applied)

			if tt.wantErrorMsg != "" {
				assert.Contains(t, body, "error-message")
				assert.Contains(t, body, tt.wantErrorMsg)
				assert.Empty(t, w.Header().Get("HX-Redirect"))
				return
			}
			assert.Equal(t, "/admin/github", w.Header().Get("HX-Redirect"))
			assert.Contains(t, body, "saved")
		})
	}
}

func TestHandler_SaveGitHubAuthConfig_WrongMethod(t *testing.T) {
	t.Parallel()
	h, _ := newHandlerWithGitHubAuth(t)
	w := httptest.NewRecorder()
	h.SaveGitHubAuthConfig(w, httptest.NewRequest("GET", "/api/github-auth-config", nil))
	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

func TestHandler_GitHubAuth_NoStore(t *testing.T) {
	t.Parallel()
	h := newHandlerWithOptions(t)

	w := postGitHubAuthForm(h, url.Values{"github_client_id": {"id"}}, true)
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)

	w = httptest.NewRecorder()
	h.ServeGitHubAuth(w, httptest.NewRequest("GET", "/admin/github", nil))
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestHandler_SaveGitHubAuthConfig_NonHTMXRedirects(t *testing.T) {
	t.Parallel()
	h, _ := newHandlerWithGitHubAuth(t)
	w := postGitHubAuthForm(h, url.Values{"github_client_id": {"id"}, "github_client_secret": {"s"}}, false)
	assert.Equal(t, http.StatusSeeOther, w.Code)
	assert.Equal(t, "/admin/github", w.Header().Get("Location"))
}

func TestHandler_SaveGitHubAuthConfig_DoesNotLeakInternalError(t *testing.T) {
	t.Parallel()
	blockedPath := filepath.Join(t.TempDir(), "blocked-by-a-file")
	require.NoError(t, os.WriteFile(blockedPath, []byte("x"), 0644))

	h := newHandlerWithOptions(t)
	applied := false
	h.SetGitHubAuth(NewGitHubAuthStore(blockedPath), func(GitHubAuthConfig) { applied = true })

	w := postGitHubAuthForm(h, url.Values{"github_client_id": {"id"}, "github_client_secret": {testGitHubSecret}}, true)
	body := w.Body.String()

	assert.Contains(t, body, "Failed to save GitHub login settings")
	assert.NotContains(t, body, blockedPath)
	assert.NotContains(t, body, testGitHubSecret)
	assert.False(t, applied, "a config that could not be saved must not go live")
}

func TestHandler_SaveGitHubAuthConfig_RecordsAudit(t *testing.T) {
	t.Parallel()
	h, _ := newHandlerWithGitHubAuth(t)
	audit, err := NewAuditStore(t.TempDir())
	require.NoError(t, err)
	h.SetAuditStore(audit)

	postGitHubAuthForm(h, url.Values{
		"github_client_id": {"id"}, "github_client_secret": {testGitHubSecret}, "github_allowed_orgs": {"my-org"},
	}, true)

	require.Eventually(t, func() bool {
		entries, err := audit.Recent(10)
		return err == nil && len(entries) == 1
	}, 2*time.Second, 10*time.Millisecond)

	entries, err := audit.Recent(10)
	require.NoError(t, err)
	assert.Equal(t, "github_auth.update", entries[0].Action)
	assert.Equal(t, "admin", entries[0].Role)
	assert.Equal(t, "enabled, orgs: my-org", entries[0].Detail)
	assert.NotContains(t, entries[0].Detail, testGitHubSecret)
}

// renderWebTemplate executes a page template from web/ the way the server does.
func renderWebTemplate(t *testing.T, page string, data interface{}) string {
	t.Helper()
	tmpl, err := template.New("base.html").Funcs(templateFuncMap).ParseFiles("../../web/base.html", "../../web/"+page)
	require.NoError(t, err)
	var buf bytes.Buffer
	require.NoError(t, tmpl.ExecuteTemplate(&buf, "base", data))
	return buf.String()
}

func TestGitHubAuthTemplate_Status(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		view        githubAuthView
		contains    []string
		notContains []string
	}{
		{
			name:        "off",
			view:        githubAuthView{CallbackURL: "https://lab.example.org/student/auth/github/callback"},
			contains:    []string{"GitHub login is <strong>off</strong>", "https://lab.example.org/student/auth/github/callback"},
			notContains: []string{"github-auth-turn-off", "github_disable_classic_login", "(saved"},
		},
		{
			name:        "on for any account warns",
			view:        githubAuthView{Enabled: true, ClientID: "id", HasSecret: true},
			contains:    []string{`class="warning-message"`, "any GitHub account", "github-auth-turn-off", "(saved"},
			notContains: []string{"Only members of <strong>"},
		},
		{
			name:        "on with orgs",
			view:        githubAuthView{Enabled: true, ClientID: "id", HasSecret: true, AllowedOrgs: "my-org, other", DisableClassicLogin: true},
			contains:    []string{"Only members of <strong>my-org, other</strong>", "checked"},
			notContains: []string{`class="warning-message"`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			html := renderWebTemplate(t, "github-auth.html", tt.view)
			for _, s := range tt.contains {
				assert.Contains(t, html, s)
			}
			for _, s := range tt.notContains {
				assert.NotContains(t, html, s)
			}
		})
	}
}

func TestStudentLoginTemplate_SignInButtons(t *testing.T) {
	t.Parallel()
	const githubHref, azureHref, passwordForm = `href="/student/auth/github/login"`, `href="/student/auth/azure/login"`, `id="login-form"`
	tests := []struct {
		name        string
		data        map[string]interface{}
		contains    []string
		notContains []string
	}{
		{
			name:        "password only",
			data:        map[string]interface{}{},
			contains:    []string{passwordForm},
			notContains: []string{githubHref, azureHref, "login-divider"},
		},
		{
			name:        "GitHub and password",
			data:        map[string]interface{}{"GitHubEnabled": true},
			contains:    []string{githubHref, "Sign in with GitHub", passwordForm, "login-divider"},
			notContains: []string{azureHref},
		},
		{
			name:     "GitHub and Microsoft",
			data:     map[string]interface{}{"GitHubEnabled": true, "AzureADEnabled": true},
			contains: []string{githubHref, azureHref, passwordForm},
		},
		{
			name:        "GitHub only still shows errors",
			data:        map[string]interface{}{"GitHubEnabled": true, "ClassicLoginDisabled": true, "Error": "GitHub sign-in was cancelled."},
			contains:    []string{githubHref, "GitHub sign-in was cancelled."},
			notContains: []string{passwordForm, "login-divider"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			html := renderWebTemplate(t, "student-login.html", tt.data)
			for _, s := range tt.contains {
				assert.Contains(t, html, s)
			}
			for _, s := range tt.notContains {
				assert.NotContains(t, html, s)
			}
		})
	}
}
