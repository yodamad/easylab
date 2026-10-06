package server

import (
	"bytes"
	"encoding/json"
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
)

const testGitLabSecret = "gl-test-application-secret-value"

// fakeGitLab is an httptest stand-in for a GitLab instance's token and
// userinfo endpoints. Because the instance URL is configurable, tests simply
// point the config at it.
type fakeGitLab struct {
	username       string
	groups         []string
	tokenStatus    int
	userinfoStatus int
}

func (f *fakeGitLab) start(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		if f.tokenStatus != 0 {
			http.Error(w, "bad code", f.tokenStatus)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"access_token": "glpat_test", "token_type": "Bearer"})
	})
	mux.HandleFunc("/oauth/userinfo", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer glpat_test" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if f.userinfoStatus != 0 {
			http.Error(w, "boom", f.userinfoStatus)
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"nickname": f.username, "groups": f.groups})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// newGitLabTestAuthHandler returns an AuthHandler with GitLab login enabled
// against srv, as if srv were a self-managed instance.
func newGitLabTestAuthHandler(srv *httptest.Server, groups []string) *AuthHandler {
	ah := createTestAuthHandler()
	ah.ConfigureGitLab(GitLabAuthConfig{BaseURL: srv.URL, ClientID: "app-id", ClientSecret: testGitLabSecret, AllowedGroups: groups})
	return ah
}

// startGitLabFlow runs the login redirect and returns the issued state and its cookie.
func startGitLabFlow(t *testing.T, ah *AuthHandler) (string, *http.Cookie) {
	t.Helper()
	w := httptest.NewRecorder()
	ah.HandleGitLabLogin(w, httptest.NewRequest("GET", "/student/auth/gitlab/login", nil))
	require.Equal(t, http.StatusFound, w.Code)

	loc, err := url.Parse(w.Header().Get("Location"))
	require.NoError(t, err)
	state := loc.Query().Get("state")
	require.NotEmpty(t, state)

	for _, c := range w.Result().Cookies() {
		if c.Name == gitlabOAuthStateCookieName {
			return state, c
		}
	}
	t.Fatal("state cookie not set")
	return "", nil
}

func TestNormalizeGitLabURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    string
		expected string
		wantErr  bool
	}{
		{name: "empty defaults to gitlab.com", input: "", expected: "https://gitlab.com"},
		{name: "blank defaults to gitlab.com", input: "   ", expected: "https://gitlab.com"},
		{name: "gitlab.com", input: "https://gitlab.com", expected: "https://gitlab.com"},
		{name: "trailing slash removed", input: "https://gitlab.example.org/", expected: "https://gitlab.example.org"},
		{name: "host lowercased", input: "https://GitLab.Example.org", expected: "https://gitlab.example.org"},
		{name: "port kept", input: "https://gitlab.example.org:8443", expected: "https://gitlab.example.org:8443"},
		{name: "path prefix kept", input: "https://example.org/gitlab/", expected: "https://example.org/gitlab"},
		{name: "http accepted", input: "http://gitlab.internal", expected: "http://gitlab.internal"},
		{name: "missing scheme", input: "gitlab.example.org", wantErr: true},
		{name: "other scheme", input: "ftp://gitlab.example.org", wantErr: true},
		{name: "javascript scheme", input: "javascript:alert(1)", wantErr: true},
		{name: "no host", input: "https://", wantErr: true},
		{name: "credentials", input: "https://user:pass@gitlab.example.org", wantErr: true},
		{name: "query", input: "https://gitlab.example.org/?a=b", wantErr: true},
		{name: "fragment", input: "https://gitlab.example.org/#x", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := normalizeGitLabURL(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestParseGitLabGroups(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    string
		expected []string
		wantErr  bool
	}{
		{name: "empty", input: "", expected: nil},
		{name: "top-level group", input: "my-group", expected: []string{"my-group"}},
		{name: "nested and mixed case", input: "My-Group/Sub.Group, other_group", expected: []string{"my-group/sub.group", "other_group"}},
		{name: "surrounding slashes and duplicates", input: "/a/b/\na/b", expected: []string{"a/b"}},
		{name: "empty segment rejected", input: "a//b", wantErr: true},
		{name: "leading hyphen rejected", input: "-group", wantErr: true},
		{name: "markup rejected", input: "<script>", wantErr: true},
		{name: "url rejected", input: "https://gitlab.com/my-group", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseGitLabGroups(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestGitLabAuthStore_RoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := GitLabAuthConfig{BaseURL: "https://gitlab.example.org", ClientID: "id", ClientSecret: testGitLabSecret, AllowedGroups: []string{"a", "a/b"}, DisableClassicLogin: true}

	require.NoError(t, NewGitLabAuthStore(dir).Set(cfg))

	info, err := os.Stat(filepath.Join(dir, "gitlab-auth.json"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())

	assert.Equal(t, cfg, NewGitLabAuthStore(dir).Get())
}

func TestGitLabAuthStore_MissingFileAndEmptyDir(t *testing.T) {
	t.Parallel()
	empty := NewGitLabAuthStore(t.TempDir()).Get()
	assert.False(t, empty.Enabled())
	assert.Equal(t, "https://gitlab.com", empty.EffectiveBaseURL())

	s := NewGitLabAuthStore("")
	require.NoError(t, s.Set(GitLabAuthConfig{ClientID: "id", ClientSecret: "s", AllowedGroups: []string{"a"}}))
	s.Get().AllowedGroups[0] = "mutated"
	assert.Equal(t, []string{"a"}, s.Get().AllowedGroups)
}

// Not parallel: toggles the package-level at-rest encryption key.
func TestGitLabAuthStore_SecretEncryptedAtRest(t *testing.T) {
	require.NoError(t, InitDataEncryption(bytes.Repeat([]byte{9}, 32)))
	t.Cleanup(func() { _ = InitDataEncryption(nil) })

	dir := t.TempDir()
	require.NoError(t, NewGitLabAuthStore(dir).Set(GitLabAuthConfig{ClientID: "id", ClientSecret: testGitLabSecret}))

	raw, err := os.ReadFile(filepath.Join(dir, "gitlab-auth.json"))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), testGitLabSecret)
	assert.Contains(t, string(raw), encPrefix)

	assert.Equal(t, testGitLabSecret, NewGitLabAuthStore(dir).Get().ClientSecret)
}

func TestAuthHandler_ConfigureGitLab(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		cfg          GitLabAuthConfig
		wantEnabled  bool
		wantAuthURL  string
		wantTokenURL string
	}{
		{
			name:         "gitlab.com by default",
			cfg:          GitLabAuthConfig{ClientID: "id", ClientSecret: "s"},
			wantEnabled:  true,
			wantAuthURL:  "https://gitlab.com/oauth/authorize",
			wantTokenURL: "https://gitlab.com/oauth/token",
		},
		{
			name:         "self-managed with path prefix",
			cfg:          GitLabAuthConfig{BaseURL: "https://example.org/gitlab", ClientID: "id", ClientSecret: "s", AllowedGroups: []string{"a"}},
			wantEnabled:  true,
			wantAuthURL:  "https://example.org/gitlab/oauth/authorize",
			wantTokenURL: "https://example.org/gitlab/oauth/token",
		},
		{name: "missing secret", cfg: GitLabAuthConfig{ClientID: "id"}},
		{name: "missing application id", cfg: GitLabAuthConfig{ClientSecret: "s"}},
		{name: "empty", cfg: GitLabAuthConfig{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ah := createTestAuthHandler()
			ah.ConfigureGitLab(tt.cfg)
			assert.Equal(t, tt.wantEnabled, ah.GitLabEnabled())
			if !tt.wantEnabled {
				assert.Nil(t, ah.gitlabConfig)
				return
			}
			require.NotNil(t, ah.gitlabConfig)
			assert.Equal(t, []string{"openid"}, ah.gitlabConfig.Scopes)
			assert.Equal(t, tt.wantAuthURL, ah.gitlabConfig.Endpoint.AuthURL)
			assert.Equal(t, tt.wantTokenURL, ah.gitlabConfig.Endpoint.TokenURL)
		})
	}
}

func TestAuthHandler_ConfigureGitLab_DisableClearsState(t *testing.T) {
	t.Parallel()
	ah := createTestAuthHandler()
	ah.ConfigureGitLab(GitLabAuthConfig{ClientID: "id", ClientSecret: "s", AllowedGroups: []string{"a"}, DisableClassicLogin: true})
	ah.ConfigureGitLab(GitLabAuthConfig{})

	assert.False(t, ah.GitLabEnabled())
	assert.Empty(t, ah.gitlabAllowedGroups)
	assert.Empty(t, ah.gitlabBaseURL)
	assert.False(t, ah.gitlabClassicLoginDisabled)
}

func TestHandleGitLabLogin(t *testing.T) {
	t.Parallel()

	t.Run("disabled redirects to login with an error", func(t *testing.T) {
		t.Parallel()
		ah := createTestAuthHandler()
		w := httptest.NewRecorder()
		ah.HandleGitLabLogin(w, httptest.NewRequest("GET", "/student/auth/gitlab/login", nil))

		assert.Equal(t, http.StatusSeeOther, w.Code)
		assert.Contains(t, w.Header().Get("Location"), "/student/login?error=")
		assert.Empty(t, w.Result().Cookies())
	})

	tests := []struct {
		name     string
		baseURL  string
		wantHost string
		wantPath string
	}{
		{name: "gitlab.com", baseURL: "", wantHost: "gitlab.com", wantPath: "/oauth/authorize"},
		{name: "self-managed", baseURL: "https://gitlab.example.org:8443", wantHost: "gitlab.example.org:8443", wantPath: "/oauth/authorize"},
		{name: "self-managed under a path", baseURL: "https://example.org/gitlab", wantHost: "example.org", wantPath: "/gitlab/oauth/authorize"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ah := createTestAuthHandler()
			ah.ConfigureGitLab(GitLabAuthConfig{BaseURL: tt.baseURL, ClientID: "app-id", ClientSecret: testGitLabSecret})

			req := httptest.NewRequest("GET", "http://lab.example.org/student/auth/gitlab/login", nil)
			req.Header.Set("X-Forwarded-Proto", "https")
			w := httptest.NewRecorder()
			ah.HandleGitLabLogin(w, req)

			require.Equal(t, http.StatusFound, w.Code)
			loc, err := url.Parse(w.Header().Get("Location"))
			require.NoError(t, err)
			assert.Equal(t, tt.wantHost, loc.Host)
			assert.Equal(t, tt.wantPath, loc.Path)
			q := loc.Query()
			assert.Equal(t, "app-id", q.Get("client_id"))
			assert.Equal(t, "openid", q.Get("scope"))
			assert.Equal(t, "https://lab.example.org/student/auth/gitlab/callback", q.Get("redirect_uri"))
			assert.NotContains(t, w.Header().Get("Location"), testGitLabSecret)

			cookies := w.Result().Cookies()
			require.Len(t, cookies, 1)
			assert.Equal(t, gitlabOAuthStateCookieName, cookies[0].Name)
			assert.Equal(t, q.Get("state"), cookies[0].Value)
			assert.True(t, cookies[0].HttpOnly)
			assert.True(t, cookies[0].Secure)
			assert.Equal(t, http.SameSiteLaxMode, cookies[0].SameSite)
		})
	}
}

func TestHandleGitLabCallback(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		gitlab        fakeGitLab
		allowed       []string
		mutate        func(q url.Values, cookie *http.Cookie)
		omitCookie    bool
		wantUser      string // empty: no session must be created
		wantOwner     string
		wantErrorText string
	}{
		{
			name:      "any account when no group restriction",
			gitlab:    fakeGitLab{username: "tanuki"},
			wantUser:  "tanuki",
			wantOwner: "tanuki",
		},
		{
			name:      "username is lowercased and dots become hyphens in the owner",
			gitlab:    fakeGitLab{username: "Jane.Doe"},
			wantUser:  "jane.doe",
			wantOwner: "jane-doe",
		},
		{
			name:      "member of the allowed group",
			gitlab:    fakeGitLab{username: "tanuki", groups: []string{"other", "my-group"}},
			allowed:   []string{"my-group"},
			wantUser:  "tanuki",
			wantOwner: "tanuki",
		},
		{
			name:      "member of an allowed subgroup, case-insensitive",
			gitlab:    fakeGitLab{username: "tanuki", groups: []string{"My-Group/Workshop"}},
			allowed:   []string{"nope", "my-group/workshop"},
			wantUser:  "tanuki",
			wantOwner: "tanuki",
		},
		{
			name:          "not a member",
			gitlab:        fakeGitLab{username: "tanuki", groups: []string{"other"}},
			allowed:       []string{"my-group"},
			wantErrorText: "isn't in a group allowed",
		},
		{
			name:          "member of the parent only when a subgroup is required",
			gitlab:        fakeGitLab{username: "tanuki", groups: []string{"my-group"}},
			allowed:       []string{"my-group/workshop"},
			wantErrorText: "isn't in a group allowed",
		},
		{
			name:          "no groups at all",
			gitlab:        fakeGitLab{username: "tanuki"},
			allowed:       []string{"my-group"},
			wantErrorText: "isn't in a group allowed",
		},
		{
			name:          "unknown state",
			gitlab:        fakeGitLab{username: "tanuki"},
			mutate:        func(q url.Values, c *http.Cookie) { q.Set("state", "forged"); c.Value = "forged" },
			wantErrorText: "expired",
		},
		{
			name:          "state cookie mismatch",
			gitlab:        fakeGitLab{username: "tanuki"},
			mutate:        func(q url.Values, c *http.Cookie) { c.Value = "someone-elses-browser" },
			wantErrorText: "expired",
		},
		{
			name:          "state cookie missing",
			gitlab:        fakeGitLab{username: "tanuki"},
			omitCookie:    true,
			wantErrorText: "expired",
		},
		{
			name:          "student cancelled on GitLab",
			gitlab:        fakeGitLab{username: "tanuki"},
			mutate:        func(q url.Values, c *http.Cookie) { q.Set("error", "access_denied") },
			wantErrorText: "cancelled",
		},
		{
			name:          "other OAuth error",
			gitlab:        fakeGitLab{username: "tanuki"},
			mutate:        func(q url.Values, c *http.Cookie) { q.Set("error", "invalid_scope") },
			wantErrorText: "GitLab sign-in failed",
		},
		{
			name:          "missing code",
			gitlab:        fakeGitLab{username: "tanuki"},
			mutate:        func(q url.Values, c *http.Cookie) { q.Del("code") },
			wantErrorText: "GitLab sign-in failed",
		},
		{
			name:          "token exchange fails",
			gitlab:        fakeGitLab{username: "tanuki", tokenStatus: http.StatusBadRequest},
			wantErrorText: "GitLab sign-in failed",
		},
		{
			name:          "userinfo fails",
			gitlab:        fakeGitLab{username: "tanuki", userinfoStatus: http.StatusInternalServerError},
			wantErrorText: "GitLab sign-in failed",
		},
		{
			name:          "unusable username",
			gitlab:        fakeGitLab{username: "evil@example.com"},
			wantErrorText: "GitLab sign-in failed",
		},
		{
			name:          "empty username",
			gitlab:        fakeGitLab{username: ""},
			wantErrorText: "GitLab sign-in failed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gl := tt.gitlab
			srv := gl.start(t)
			ah := newGitLabTestAuthHandler(srv, tt.allowed)
			state, cookie := startGitLabFlow(t, ah)

			q := url.Values{"state": {state}, "code": {"the-code"}}
			if tt.mutate != nil {
				tt.mutate(q, cookie)
			}
			req := httptest.NewRequest("GET", "/student/auth/gitlab/callback?"+q.Encode(), nil)
			if !tt.omitCookie {
				req.AddCookie(cookie)
			}
			w := httptest.NewRecorder()
			ah.HandleGitLabCallback(w, req)

			require.Equal(t, http.StatusSeeOther, w.Code)
			location, err := url.QueryUnescape(w.Header().Get("Location"))
			require.NoError(t, err)

			if tt.wantUser == "" {
				assert.Contains(t, location, "/student/login?error=")
				assert.Contains(t, location, tt.wantErrorText)
				assert.NotContains(t, location, srv.URL, "error must not expose internal details")
				assert.Empty(t, studentSessionEmails(ah))
				return
			}

			// The fake instance listens on 127.0.0.1, so that is the identity's host.
			wantEmail := tt.wantUser + "@users.noreply.127.0.0.1"
			assert.Equal(t, "/student/dashboard", location)
			assert.Equal(t, []string{wantEmail}, studentSessionEmails(ah))
			assert.Equal(t, tt.wantOwner, usernameFromEmail(wantEmail))

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

func TestGitLabHost(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "gitlab.com", input: "https://gitlab.com", expected: "gitlab.com"},
		{name: "port dropped", input: "https://gitlab.example.org:8443", expected: "gitlab.example.org"},
		{name: "path dropped", input: "https://example.org/gitlab", expected: "example.org"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, gitlabHost(tt.input))
		})
	}
}

func TestHandleGitLabCallback_StateIsSingleUse(t *testing.T) {
	t.Parallel()
	gl := fakeGitLab{username: "tanuki"}
	ah := newGitLabTestAuthHandler(gl.start(t), nil)
	state, cookie := startGitLabFlow(t, ah)

	call := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/student/auth/gitlab/callback?state="+state+"&code=c", nil)
		req.AddCookie(cookie)
		w := httptest.NewRecorder()
		ah.HandleGitLabCallback(w, req)
		return w
	}

	assert.Equal(t, "/student/dashboard", call().Header().Get("Location"))
	assert.Contains(t, call().Header().Get("Location"), "/student/login?error=")
	assert.Len(t, studentSessionEmails(ah), 1)
}

func TestHandleGitLabCallback_ExpiredState(t *testing.T) {
	t.Parallel()
	gl := fakeGitLab{username: "tanuki"}
	ah := newGitLabTestAuthHandler(gl.start(t), nil)
	state, cookie := startGitLabFlow(t, ah)

	ah.mu.Lock()
	ah.gitlabOAuthStates[state] = time.Now().Add(-time.Minute)
	ah.mu.Unlock()

	req := httptest.NewRequest("GET", "/student/auth/gitlab/callback?state="+state+"&code=c", nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	ah.HandleGitLabCallback(w, req)

	assert.Contains(t, w.Header().Get("Location"), "/student/login?error=")
	assert.Empty(t, studentSessionEmails(ah))
}

// A state issued by the GitHub flow must not be accepted by the GitLab callback.
func TestHandleGitLabCallback_RejectsGitHubState(t *testing.T) {
	t.Parallel()
	gl := fakeGitLab{username: "tanuki"}
	ah := newGitLabTestAuthHandler(gl.start(t), nil)
	ah.ConfigureGitHub(GitHubAuthConfig{ClientID: "id", ClientSecret: "s"})
	state, githubCookie := startGitHubFlow(t, ah)

	req := httptest.NewRequest("GET", "/student/auth/gitlab/callback?state="+state+"&code=c", nil)
	req.AddCookie(&http.Cookie{Name: gitlabOAuthStateCookieName, Value: githubCookie.Value})
	w := httptest.NewRecorder()
	ah.HandleGitLabCallback(w, req)

	assert.Contains(t, w.Header().Get("Location"), "/student/login?error=")
	assert.Empty(t, studentSessionEmails(ah))
}

func TestHandleGitLabCallback_WhenDisabled(t *testing.T) {
	t.Parallel()
	ah := createTestAuthHandler()
	w := httptest.NewRecorder()
	ah.HandleGitLabCallback(w, httptest.NewRequest("GET", "/student/auth/gitlab/callback?state=x&code=y", nil))

	assert.Equal(t, http.StatusSeeOther, w.Code)
	assert.Contains(t, w.Header().Get("Location"), "/student/login?error=")
}

func TestRequireStudentAuth_GitLabOnlyEnabled(t *testing.T) {
	t.Parallel()
	ah := createTestAuthHandler()
	ah.studentPasswordHash = ""

	called := false
	next := ah.RequireStudentAuth(func(w http.ResponseWriter, r *http.Request) { called = true })

	w := httptest.NewRecorder()
	next(w, createStudentAuthenticatedRequest("GET", "/student/dashboard", ah))
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.False(t, called)

	ah.ConfigureGitLab(GitLabAuthConfig{ClientID: "id", ClientSecret: "s"})
	w = httptest.NewRecorder()
	next(w, createStudentAuthenticatedRequest("GET", "/student/dashboard", ah))
	assert.Equal(t, http.StatusOK, w.Code)
	assert.True(t, called)
}

func TestHandleStudentLogin_ClassicLoginDisabledByGitLab(t *testing.T) {
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
	ah.ConfigureGitLab(GitLabAuthConfig{ClientID: "id", ClientSecret: "s", DisableClassicLogin: true})
	assert.Contains(t, post(ah), "Password login is disabled")

	// Turning GitLab login off must give the password form back, or nobody could sign in.
	ah.ConfigureGitLab(GitLabAuthConfig{})
	assert.Contains(t, post(ah), "Invalid password")
}

// newHandlerWithGitLabAuth returns a Handler wired to a GitLab settings store,
// plus a pointer to the last config pushed to the (fake) auth handler.
func newHandlerWithGitLabAuth(t *testing.T) (*Handler, *GitLabAuthConfig) {
	t.Helper()
	h := newHandlerWithOptions(t)
	applied := &GitLabAuthConfig{}
	h.SetGitLabAuth(NewGitLabAuthStore(t.TempDir()), func(cfg GitLabAuthConfig) { *applied = cfg })
	return h, applied
}

func postGitLabAuthForm(h *Handler, form url.Values, htmx bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/api/gitlab-auth-config", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if htmx {
		req.Header.Set("HX-Request", "true")
	}
	w := httptest.NewRecorder()
	h.SaveGitLabAuthConfig(w, req)
	return w
}

func TestHandler_SaveGitLabAuthConfig(t *testing.T) {
	t.Parallel()
	saved := GitLabAuthConfig{BaseURL: "https://gitlab.example.org", ClientID: "id", ClientSecret: testGitLabSecret}
	tests := []struct {
		name         string
		existing     *GitLabAuthConfig
		form         url.Values
		expected     GitLabAuthConfig
		wantErrorMsg string
	}{
		{
			name:     "URL left empty means gitlab.com",
			form:     url.Values{"gitlab_client_id": {" id "}, "gitlab_client_secret": {testGitLabSecret}},
			expected: GitLabAuthConfig{BaseURL: "https://gitlab.com", ClientID: "id", ClientSecret: testGitLabSecret},
		},
		{
			name: "self-managed with groups and password sign-in off",
			form: url.Values{
				"gitlab_url": {"https://GitLab.example.org/"}, "gitlab_client_id": {"id"}, "gitlab_client_secret": {testGitLabSecret},
				"gitlab_allowed_groups": {"My-Group, my-group/sub"}, "gitlab_disable_classic_login": {"on"},
			},
			expected: GitLabAuthConfig{
				BaseURL: "https://gitlab.example.org", ClientID: "id", ClientSecret: testGitLabSecret,
				AllowedGroups: []string{"my-group", "my-group/sub"}, DisableClassicLogin: true,
			},
		},
		{
			name:     "blank secret keeps the saved one",
			existing: &saved,
			form:     url.Values{"gitlab_url": {"https://gitlab.example.org"}, "gitlab_client_id": {"id"}, "gitlab_allowed_groups": {"g"}},
			expected: GitLabAuthConfig{BaseURL: "https://gitlab.example.org", ClientID: "id", ClientSecret: testGitLabSecret, AllowedGroups: []string{"g"}},
		},
		{
			name:     "empty application ID turns it off and clears everything",
			existing: &saved,
			form:     url.Values{"gitlab_url": {"https://gitlab.example.org"}, "gitlab_client_id": {""}, "gitlab_allowed_groups": {"g"}},
			expected: GitLabAuthConfig{},
		},
		{
			name:         "application ID without any secret",
			form:         url.Values{"gitlab_client_id": {"id"}},
			expected:     GitLabAuthConfig{},
			wantErrorMsg: "Enter the secret",
		},
		{
			name:         "invalid URL leaves saved config untouched",
			existing:     &saved,
			form:         url.Values{"gitlab_url": {"ftp://gitlab.example.org"}, "gitlab_client_id": {"id"}},
			expected:     saved,
			wantErrorMsg: "GitLab URL is not valid",
		},
		{
			name:         "URL with credentials rejected",
			existing:     &saved,
			form:         url.Values{"gitlab_url": {"https://user:pw@gitlab.example.org"}, "gitlab_client_id": {"id"}},
			expected:     saved,
			wantErrorMsg: "GitLab URL is not valid",
		},
		{
			name:         "invalid group leaves saved config untouched",
			existing:     &saved,
			form:         url.Values{"gitlab_url": {"https://gitlab.example.org"}, "gitlab_client_id": {"id"}, "gitlab_allowed_groups": {"<script>alert(1)</script>"}},
			expected:     saved,
			wantErrorMsg: "not valid",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h, applied := newHandlerWithGitLabAuth(t)
			if tt.existing != nil {
				require.NoError(t, h.gitlabAuthStore.Set(*tt.existing))
				*applied = *tt.existing
			}

			w := postGitLabAuthForm(h, tt.form, true)
			body := w.Body.String()

			assert.NotContains(t, body, testGitLabSecret)
			assert.NotContains(t, body, "<script>")
			assert.NotContains(t, body, "user:pw")
			assert.Equal(t, tt.expected, h.gitlabAuthStore.Get())
			assert.Equal(t, tt.expected, *applied)

			if tt.wantErrorMsg != "" {
				assert.Contains(t, body, "error-message")
				assert.Contains(t, body, tt.wantErrorMsg)
				assert.Empty(t, w.Header().Get("HX-Redirect"))
				return
			}
			assert.Equal(t, "/admin/gitlab", w.Header().Get("HX-Redirect"))
			assert.Contains(t, body, "saved")
		})
	}
}

func TestHandler_SaveGitLabAuthConfig_WrongMethod(t *testing.T) {
	t.Parallel()
	h, _ := newHandlerWithGitLabAuth(t)
	w := httptest.NewRecorder()
	h.SaveGitLabAuthConfig(w, httptest.NewRequest("GET", "/api/gitlab-auth-config", nil))
	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

func TestHandler_GitLabAuth_NoStore(t *testing.T) {
	t.Parallel()
	h := newHandlerWithOptions(t)

	w := postGitLabAuthForm(h, url.Values{"gitlab_client_id": {"id"}}, true)
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)

	w = httptest.NewRecorder()
	h.ServeGitLabAuth(w, httptest.NewRequest("GET", "/admin/gitlab", nil))
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestHandler_SaveGitLabAuthConfig_NonHTMXRedirects(t *testing.T) {
	t.Parallel()
	h, _ := newHandlerWithGitLabAuth(t)
	w := postGitLabAuthForm(h, url.Values{"gitlab_client_id": {"id"}, "gitlab_client_secret": {"s"}}, false)
	assert.Equal(t, http.StatusSeeOther, w.Code)
	assert.Equal(t, "/admin/gitlab", w.Header().Get("Location"))
}

func TestHandler_SaveGitLabAuthConfig_DoesNotLeakInternalError(t *testing.T) {
	t.Parallel()
	blockedPath := filepath.Join(t.TempDir(), "blocked-by-a-file")
	require.NoError(t, os.WriteFile(blockedPath, []byte("x"), 0644))

	h := newHandlerWithOptions(t)
	applied := false
	h.SetGitLabAuth(NewGitLabAuthStore(blockedPath), func(GitLabAuthConfig) { applied = true })

	w := postGitLabAuthForm(h, url.Values{"gitlab_client_id": {"id"}, "gitlab_client_secret": {testGitLabSecret}}, true)
	body := w.Body.String()

	assert.Contains(t, body, "Failed to save GitLab login settings")
	assert.NotContains(t, body, blockedPath)
	assert.NotContains(t, body, testGitLabSecret)
	assert.False(t, applied, "a config that could not be saved must not go live")
}

func TestHandler_SaveGitLabAuthConfig_RecordsAudit(t *testing.T) {
	t.Parallel()
	h, _ := newHandlerWithGitLabAuth(t)
	audit, err := NewAuditStore(t.TempDir())
	require.NoError(t, err)
	h.SetAuditStore(audit)

	postGitLabAuthForm(h, url.Values{
		"gitlab_url": {"https://gitlab.example.org:8443"}, "gitlab_client_id": {"id"},
		"gitlab_client_secret": {testGitLabSecret}, "gitlab_allowed_groups": {"my-group"},
	}, true)

	require.Eventually(t, func() bool {
		entries, err := audit.Recent(10)
		return err == nil && len(entries) == 1
	}, 2*time.Second, 10*time.Millisecond)

	entries, err := audit.Recent(10)
	require.NoError(t, err)
	assert.Equal(t, "gitlab_auth.update", entries[0].Action)
	assert.Equal(t, "admin", entries[0].Role)
	assert.Equal(t, "enabled, gitlab.example.org, groups: my-group", entries[0].Detail)
}

func TestGitLabAuthTemplate_Status(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		view        gitlabAuthView
		contains    []string
		notContains []string
	}{
		{
			name:        "off shows gitlab.com as the URL",
			view:        gitlabAuthView{BaseURL: "https://gitlab.com", Host: "gitlab.com", CallbackURL: "https://lab.example.org/student/auth/gitlab/callback"},
			contains:    []string{"GitLab login is <strong>off</strong>", `value="https://gitlab.com"`, "https://lab.example.org/student/auth/gitlab/callback"},
			notContains: []string{"gitlab-auth-turn-off", "gitlab_disable_classic_login", "(saved", "gitlab-auth-insecure"},
		},
		{
			name:        "on for any account warns",
			view:        gitlabAuthView{Enabled: true, BaseURL: "https://gitlab.com", Host: "gitlab.com", ClientID: "id", HasSecret: true},
			contains:    []string{`class="warning-message" id="gitlab-auth-status"`, "any account on gitlab.com", "gitlab-auth-turn-off", "(saved"},
			notContains: []string{"Only members of <strong>", "gitlab-auth-insecure"},
		},
		{
			name: "self-managed with groups",
			view: gitlabAuthView{
				Enabled: true, BaseURL: "https://gitlab.example.org", Host: "gitlab.example.org", ClientID: "id", HasSecret: true,
				AllowedGroups: "my-group, my-group/sub", DisableClassicLogin: true,
			},
			contains:    []string{"for <strong>gitlab.example.org</strong>", "Only members of <strong>my-group, my-group/sub</strong>", "checked", `value="https://gitlab.example.org"`},
			notContains: []string{`class="warning-message"`},
		},
		{
			name:     "http instance warns",
			view:     gitlabAuthView{Enabled: true, Insecure: true, BaseURL: "http://gitlab.internal", Host: "gitlab.internal", ClientID: "id", HasSecret: true, AllowedGroups: "g"},
			contains: []string{`id="gitlab-auth-insecure"`, "not encrypted"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			html := renderWebTemplate(t, "gitlab-auth.html", tt.view)
			for _, s := range tt.contains {
				assert.Contains(t, html, s)
			}
			for _, s := range tt.notContains {
				assert.NotContains(t, html, s)
			}
		})
	}
}

func TestStudentLoginTemplate_GitLabButton(t *testing.T) {
	t.Parallel()
	const gitlabHref, githubHref, azureHref, passwordForm = `href="/student/auth/gitlab/login"`, `href="/student/auth/github/login"`, `href="/student/auth/azure/login"`, `id="login-form"`
	tests := []struct {
		name        string
		data        map[string]interface{}
		contains    []string
		notContains []string
	}{
		{
			name:        "GitLab and password",
			data:        map[string]interface{}{"GitLabEnabled": true},
			contains:    []string{gitlabHref, "Sign in with GitLab", passwordForm, "login-divider"},
			notContains: []string{githubHref, azureHref},
		},
		{
			name:     "all three providers",
			data:     map[string]interface{}{"GitLabEnabled": true, "GitHubEnabled": true, "AzureADEnabled": true},
			contains: []string{gitlabHref, githubHref, azureHref, passwordForm},
		},
		{
			name:        "GitLab only still shows errors",
			data:        map[string]interface{}{"GitLabEnabled": true, "ClassicLoginDisabled": true, "Error": "GitLab sign-in was cancelled."},
			contains:    []string{gitlabHref, "GitLab sign-in was cancelled."},
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
