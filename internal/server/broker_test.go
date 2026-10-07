package server

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testBrokerSecret = "0123456789abcdefghijklmn"
	testBrokerLab    = "job-lab"
	testPortalURL    = "https://lab.example.com"
	testAdminURL     = "https://admin.example.com"
)

func TestBrokerAssertion(t *testing.T) {
	t.Parallel()
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	claims := brokerClaims{Email: "alice@example.com", Lab: testBrokerLab, Nonce: "nonce-1234567890", Exp: now.Add(brokerAssertionTTL).Unix()}
	valid, err := signBrokerAssertion(testBrokerSecret, claims)
	require.NoError(t, err)
	payload, signature, _ := strings.Cut(valid, ".")

	// The same claims with another identity, keeping the original signature.
	forged := base64.RawURLEncoding.EncodeToString([]byte(`{"email":"mallory@example.com","lab":"job-lab","nonce":"nonce-1234567890","exp":99999999999}`))

	tests := []struct {
		name      string
		secret    string
		assertion string
		at        time.Time
		wantErr   string
	}{
		{name: "valid", secret: testBrokerSecret, assertion: valid, at: now},
		{name: "valid until the last second", secret: testBrokerSecret, assertion: valid, at: now.Add(brokerAssertionTTL)},
		{name: "expired", secret: testBrokerSecret, assertion: valid, at: now.Add(brokerAssertionTTL + time.Second), wantErr: "expired"},
		{name: "signed with another lab's secret", secret: "another-secret", assertion: valid, at: now, wantErr: "signature"},
		{name: "payload swapped under the signature", secret: testBrokerSecret, assertion: forged + "." + signature, at: now, wantErr: "signature"},
		{name: "signature truncated", secret: testBrokerSecret, assertion: payload + "." + signature[:10], at: now, wantErr: "signature"},
		{name: "no signature", secret: testBrokerSecret, assertion: payload, at: now, wantErr: "malformed"},
		{name: "empty", secret: testBrokerSecret, assertion: "", at: now, wantErr: "malformed"},
		{name: "no secret configured", secret: "", assertion: valid, at: now, wantErr: "secret"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := verifyBrokerAssertion(tt.secret, tt.assertion, tt.at)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, claims, got)
		})
	}

	_, err = signBrokerAssertion("", claims)
	require.Error(t, err, "an assertion must never be signed with an empty secret")
}

// sha256Hex is what the login page's JS sends in place of the password.
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// newTestPortalAuth returns the AuthHandler of an in-lab portal for testBrokerLab.
func newTestPortalAuth(state PortalAuthState) *AuthHandler {
	ah := NewStudentAuthHandler()
	ah.ConfigurePortalAuth(testBrokerLab, state)
	return ah
}

func brokeredPortalAuthState() PortalAuthState {
	return PortalAuthState{AdminURL: testAdminURL, BrokerSecret: testBrokerSecret, GitHub: true}
}

func cookieNamed(w *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range w.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// startPortalLogin runs the portal's side of step 1 and returns where it sends
// the student and the nonce cookie it left in their browser.
func startPortalLogin(t *testing.T, portal *AuthHandler, provider string) (*url.URL, *http.Cookie) {
	t.Helper()
	w := httptest.NewRecorder()
	portal.HandlePortalProviderLogin(provider)(w, httptest.NewRequest(http.MethodGet, "/student/auth/"+provider+"/login", nil))
	require.Equal(t, http.StatusFound, w.Code)
	target, err := url.Parse(w.Header().Get("Location"))
	require.NoError(t, err)
	cookie := cookieNamed(w, portalNonceCookieName)
	require.NotNil(t, cookie, "the nonce cookie binds the sign-in to this browser")
	return target, cookie
}

func TestConfigurePortalAuth(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		state           PortalAuthState
		wantAvailable   bool
		wantGitHub      bool
		wantClassicOff  bool
		wantPasswordSet bool
	}{
		{name: "nothing configured closes student sign-in", state: PortalAuthState{}},
		{name: "password only", state: PortalAuthState{StudentPasswordHash: "hash"}, wantAvailable: true, wantPasswordSet: true},
		{name: "brokered provider", state: brokeredPortalAuthState(), wantAvailable: true, wantGitHub: true},
		{
			name:  "provider without a broker is not offered",
			state: PortalAuthState{GitHub: true, StudentPasswordHash: "hash"}, wantAvailable: true, wantPasswordSet: true,
		},
		{
			name:          "password form hidden only while a provider is on offer",
			state:         PortalAuthState{AdminURL: testAdminURL, BrokerSecret: testBrokerSecret, GitHub: true, ClassicLoginDisabled: true, StudentPasswordHash: "hash"},
			wantAvailable: true, wantGitHub: true, wantClassicOff: true, wantPasswordSet: true,
		},
		{
			name:          "password form stays when no provider can replace it",
			state:         PortalAuthState{ClassicLoginDisabled: true, GitHub: true, StudentPasswordHash: "hash"},
			wantAvailable: true, wantPasswordSet: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ah := newTestPortalAuth(tt.state)
			assert.Equal(t, tt.wantAvailable, ah.studentLoginAvailable())
			assert.Equal(t, tt.wantGitHub, ah.GitHubEnabled())
			ah.mu.RLock()
			assert.Equal(t, tt.wantClassicOff, ah.classicStudentLoginDisabledLocked())
			assert.Equal(t, tt.wantPasswordSet, ah.studentPasswordHash != "")
			ah.mu.RUnlock()
		})
	}
}

// A portal has no admin side: no admin session it holds can ever be valid.
func TestNewStudentAuthHandler_HasNoAdminSide(t *testing.T) {
	t.Parallel()
	ah := NewStudentAuthHandler()
	called := false
	protected := ah.RequireAuth(func(http.ResponseWriter, *http.Request) { called = true })

	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "anything"})
	w := httptest.NewRecorder()
	protected(w, req)

	assert.False(t, called)
	assert.Equal(t, http.StatusSeeOther, w.Code)
}

func TestStudentAuthSnapshot(t *testing.T) {
	t.Parallel()
	ah := createTestAuthHandler()
	ah.ConfigureGitHub(GitHubAuthConfig{ClientID: "id", ClientSecret: testGitHubSecret, DisableClassicLogin: true})

	snapshot := ah.StudentAuthSnapshot()
	assert.NotEmpty(t, snapshot.StudentPasswordHash)
	assert.True(t, snapshot.GitHub)
	assert.False(t, snapshot.AzureAD)
	assert.False(t, snapshot.GitLab)
	assert.True(t, snapshot.ClassicLoginDisabled)
}

func TestHandlePortalProviderLogin(t *testing.T) {
	t.Parallel()

	t.Run("sends the student to the central instance", func(t *testing.T) {
		t.Parallel()
		target, cookie := startPortalLogin(t, newTestPortalAuth(brokeredPortalAuthState()), brokerProviderGitHub)

		assert.Equal(t, "admin.example.com", target.Host)
		assert.Equal(t, "/student/auth/github/login", target.Path)
		assert.Equal(t, testBrokerLab, target.Query().Get(brokerLabParam))
		assert.Equal(t, cookie.Value, target.Query().Get(brokerNonceParam))
		assert.Regexp(t, brokerNonceRegex, cookie.Value)
		assert.True(t, cookie.HttpOnly)
		assert.Equal(t, http.SameSiteLaxMode, cookie.SameSite)
	})

	unavailable := []struct {
		name     string
		portal   *AuthHandler
		provider string
	}{
		{name: "provider not offered", portal: newTestPortalAuth(brokeredPortalAuthState()), provider: brokerProviderGitLab},
		{name: "unknown provider", portal: newTestPortalAuth(brokeredPortalAuthState()), provider: "facebook"},
		{name: "no broker", portal: newTestPortalAuth(PortalAuthState{GitHub: true}), provider: brokerProviderGitHub},
		{name: "state not loaded yet", portal: NewStudentAuthHandler(), provider: brokerProviderGitHub},
	}
	for _, tt := range unavailable {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			w := httptest.NewRecorder()
			tt.portal.HandlePortalProviderLogin(tt.provider)(w, httptest.NewRequest(http.MethodGet, "/student/auth/"+tt.provider+"/login", nil))
			require.Equal(t, http.StatusSeeOther, w.Code)
			assert.True(t, strings.HasPrefix(w.Header().Get("Location"), "/student/login?error="))
			assert.Nil(t, cookieNamed(w, portalNonceCookieName))
		})
	}
}

// brokerResolver resolves testBrokerLab to the test portal and nothing else.
func brokerResolver(labID string) (string, string, bool) {
	if labID != testBrokerLab {
		return "", "", false
	}
	return testPortalURL, testBrokerSecret, true
}

// newCentralGitHubAuth returns a central AuthHandler with GitHub login pointed at
// a fake GitHub and able to broker for testBrokerLab.
func newCentralGitHubAuth(t *testing.T, github fakeGitHub) *AuthHandler {
	t.Helper()
	central := newGitHubTestAuthHandler(github.start(t), nil)
	central.SetBrokerLabResolver(brokerResolver)
	return central
}

// runCentralGitHub plays the central instance's part of a brokered sign-in: the
// login request the portal redirected to, then GitHub's callback. It returns the
// final response.
func runCentralGitHub(t *testing.T, central *AuthHandler, loginTarget *url.URL) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	central.HandleGitHubLogin(w, httptest.NewRequest(http.MethodGet, loginTarget.RequestURI(), nil))
	require.Equal(t, http.StatusFound, w.Code)
	authorize, err := url.Parse(w.Header().Get("Location"))
	require.NoError(t, err)
	stateCookie := cookieNamed(w, githubOAuthStateCookieName)
	require.NotNil(t, stateCookie)

	callback := httptest.NewRequest(http.MethodGet, "/student/auth/github/callback?"+url.Values{
		"state": {authorize.Query().Get("state")},
		"code":  {"abc"},
	}.Encode(), nil)
	callback.AddCookie(stateCookie)
	w = httptest.NewRecorder()
	central.HandleGitHubCallback(w, callback)
	return w
}

// The whole round trip: portal -> central -> GitHub -> central -> portal.
func TestBrokeredSignIn_EndToEnd(t *testing.T) {
	t.Parallel()
	portal := newTestPortalAuth(brokeredPortalAuthState())
	central := newCentralGitHubAuth(t, fakeGitHub{login: "octocat"})

	loginTarget, nonceCookie := startPortalLogin(t, portal, brokerProviderGitHub)
	w := runCentralGitHub(t, central, loginTarget)

	// The central instance vouches for the student and opens no session itself.
	require.Equal(t, http.StatusSeeOther, w.Code)
	back, err := url.Parse(w.Header().Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, "lab.example.com", back.Host)
	assert.Equal(t, portalBrokerCallbackPath, back.Path)
	assert.Empty(t, studentSessionEmails(central))
	assert.Nil(t, cookieNamed(w, StudentSessionCookieName))

	// The portal opens the session from the assertion and this browser's nonce.
	req := httptest.NewRequest(http.MethodGet, back.RequestURI(), nil)
	req.AddCookie(nonceCookie)
	w = httptest.NewRecorder()
	portal.HandlePortalBrokerCallback(w, req)

	require.Equal(t, http.StatusSeeOther, w.Code)
	assert.Equal(t, "/student/dashboard", w.Header().Get("Location"))
	assert.Equal(t, []string{"octocat@users.noreply.github.com"}, studentSessionEmails(portal))
	session := cookieNamed(w, StudentSessionCookieName)
	require.NotNil(t, session)
	assert.True(t, session.HttpOnly)
	spent := cookieNamed(w, portalNonceCookieName)
	require.NotNil(t, spent)
	assert.Negative(t, spent.MaxAge, "the nonce is single use")

	// The session works on the portal's protected routes.
	reached := false
	protected := portal.RequireStudentAuth(func(_ http.ResponseWriter, r *http.Request) {
		reached = true
		assert.Equal(t, "octocat@users.noreply.github.com", studentEmailFromContext(r))
	})
	dashboard := httptest.NewRequest(http.MethodGet, "/student/dashboard", nil)
	dashboard.AddCookie(session)
	protected(httptest.NewRecorder(), dashboard)
	assert.True(t, reached)
}

// A sign-in the provider refuses goes back to the portal's login page, not the
// central one — which an admin-only instance does not even have.
func TestBrokeredSignIn_ErrorsReturnToThePortal(t *testing.T) {
	t.Parallel()
	portal := newTestPortalAuth(brokeredPortalAuthState())
	central := newCentralGitHubAuth(t, fakeGitHub{login: "octocat", tokenStatus: http.StatusBadRequest})

	loginTarget, _ := startPortalLogin(t, portal, brokerProviderGitHub)
	w := runCentralGitHub(t, central, loginTarget)

	require.Equal(t, http.StatusSeeOther, w.Code)
	location := w.Header().Get("Location")
	assert.True(t, strings.HasPrefix(location, testPortalURL+"/student/login?error="), "got %s", location)
	assert.NotContains(t, location, "assertion")
}

func TestBeginBrokeredLogin_Refusals(t *testing.T) {
	t.Parallel()
	validNonce := strings.Repeat("a", 32)

	tests := []struct {
		name       string
		query      string
		brokerOnly bool
		noResolver bool
		wantStatus int
	}{
		{name: "plain sign-in on a combined instance", query: "", wantStatus: http.StatusFound},
		{name: "plain sign-in on an admin-only instance", query: "", brokerOnly: true, wantStatus: http.StatusNotFound},
		{name: "brokered sign-in", query: "lab=" + testBrokerLab + "&nonce=" + validNonce, brokerOnly: true, wantStatus: http.StatusFound},
		{name: "unknown lab", query: "lab=job-other&nonce=" + validNonce, wantStatus: http.StatusBadRequest},
		{name: "missing nonce", query: "lab=" + testBrokerLab, wantStatus: http.StatusBadRequest},
		{name: "nonce too short", query: "lab=" + testBrokerLab + "&nonce=abc", wantStatus: http.StatusBadRequest},
		{name: "nonce with unsafe characters", query: "lab=" + testBrokerLab + "&nonce=" + url.QueryEscape(strings.Repeat("a", 20)+"<script>"), wantStatus: http.StatusBadRequest},
		{name: "no resolver wired", query: "lab=" + testBrokerLab + "&nonce=" + validNonce, noResolver: true, wantStatus: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			central := newGitHubTestAuthHandler((&fakeGitHub{login: "octocat"}).start(t), nil)
			if !tt.noResolver {
				central.SetBrokerLabResolver(brokerResolver)
			}
			central.SetBrokerOnly(tt.brokerOnly)

			w := httptest.NewRecorder()
			central.HandleGitHubLogin(w, httptest.NewRequest(http.MethodGet, "/student/auth/github/login?"+tt.query, nil))

			assert.Equal(t, tt.wantStatus, w.Code)
			if tt.wantStatus != http.StatusFound {
				assert.Nil(t, cookieNamed(w, githubOAuthStateCookieName), "a refused sign-in must not start an OAuth flow")
			}
		})
	}
}

// An admin-only instance never opens a student session, even if a callback
// reaches it without the portal that should have asked for it.
func TestCompleteStudentLogin_BrokerOnlyOpensNoSession(t *testing.T) {
	t.Parallel()
	central := createTestAuthHandler()
	central.SetBrokerOnly(true)

	w := httptest.NewRecorder()
	central.completeStudentLogin(w, httptest.NewRequest(http.MethodGet, "/student/auth/github/callback", nil), "alice@example.com", "GitHub")

	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Empty(t, studentSessionEmails(central))
}

// The broker return is taken with the state: it cannot be replayed, and expires.
func TestResumeBrokeredLogin(t *testing.T) {
	t.Parallel()
	central := createTestAuthHandler()
	central.SetBrokerLabResolver(brokerResolver)

	begin := func(state string) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/student/auth/github/login?lab="+testBrokerLab+"&nonce="+strings.Repeat("a", 32), nil)
		require.True(t, central.beginBrokeredLogin(w, req, state))
	}
	callback := func(state string) *http.Request {
		return central.resumeBrokeredLogin(httptest.NewRequest(http.MethodGet, "/student/auth/github/callback?state="+state, nil))
	}

	begin("state-1")
	resumed := callback("state-1")
	ret, ok := brokerReturnFromContext(resumed)
	require.True(t, ok)
	assert.Equal(t, testPortalURL, ret.portalURL)
	assert.Equal(t, testPortalURL+"/student/login", studentLoginURL(resumed))

	_, ok = brokerReturnFromContext(callback("state-1"))
	assert.False(t, ok, "single use")
	_, ok = brokerReturnFromContext(callback("state-unknown"))
	assert.False(t, ok)
	assert.Equal(t, "/student/login", studentLoginURL(callback("state-unknown")))

	begin("state-2")
	central.mu.Lock()
	expired := central.brokerReturns["state-2"]
	expired.expiresAt = time.Now().Add(-time.Second)
	central.brokerReturns["state-2"] = expired
	central.mu.Unlock()
	_, ok = brokerReturnFromContext(callback("state-2"))
	assert.False(t, ok, "expired")
}

func TestHandlePortalBrokerCallback_Rejects(t *testing.T) {
	t.Parallel()
	const nonce = "0123456789abcdef0123456789abcdef"
	assertion := func(t *testing.T, secret string, claims brokerClaims) string {
		t.Helper()
		if claims.Exp == 0 {
			claims.Exp = time.Now().Add(brokerAssertionTTL).Unix()
		}
		signed, err := signBrokerAssertion(secret, claims)
		require.NoError(t, err)
		return signed
	}
	good := brokerClaims{Email: "alice@example.com", Lab: testBrokerLab, Nonce: nonce}

	tests := []struct {
		name      string
		portal    func() *AuthHandler
		assertion func(t *testing.T) string
		cookie    string // "" means no nonce cookie
	}{
		{
			name:      "no nonce cookie: assertion obtained in another browser",
			assertion: func(t *testing.T) string { return assertion(t, testBrokerSecret, good) },
		},
		{
			name:      "nonce of another sign-in attempt",
			assertion: func(t *testing.T) string { return assertion(t, testBrokerSecret, good) },
			cookie:    "ffffffffffffffffffffffffffffffff",
		},
		{
			name: "assertion issued for another lab",
			assertion: func(t *testing.T) string {
				claims := good
				claims.Lab = "job-other"
				return assertion(t, testBrokerSecret, claims)
			},
			cookie: nonce,
		},
		{
			name:      "signed with another secret",
			assertion: func(t *testing.T) string { return assertion(t, "another-lab-secret", good) },
			cookie:    nonce,
		},
		{
			name: "expired",
			assertion: func(t *testing.T) string {
				claims := good
				claims.Exp = time.Now().Add(-time.Second).Unix()
				return assertion(t, testBrokerSecret, claims)
			},
			cookie: nonce,
		},
		{
			name: "no usable identity",
			assertion: func(t *testing.T) string {
				claims := good
				claims.Email = "not-an-email"
				return assertion(t, testBrokerSecret, claims)
			},
			cookie: nonce,
		},
		{
			name: "empty nonce on both sides",
			assertion: func(t *testing.T) string {
				claims := good
				claims.Nonce = ""
				return assertion(t, testBrokerSecret, claims)
			},
		},
		{name: "no assertion", assertion: func(*testing.T) string { return "" }, cookie: nonce},
		{
			name:      "portal without a broker",
			portal:    func() *AuthHandler { return newTestPortalAuth(PortalAuthState{StudentPasswordHash: "hash"}) },
			assertion: func(t *testing.T) string { return assertion(t, testBrokerSecret, good) },
			cookie:    nonce,
		},
		{
			name:      "state not loaded yet",
			portal:    NewStudentAuthHandler,
			assertion: func(t *testing.T) string { return assertion(t, testBrokerSecret, good) },
			cookie:    nonce,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			portal := newTestPortalAuth(brokeredPortalAuthState())
			if tt.portal != nil {
				portal = tt.portal()
			}
			req := httptest.NewRequest(http.MethodGet, portalBrokerCallbackPath+"?assertion="+url.QueryEscape(tt.assertion(t)), nil)
			if tt.cookie != "" {
				req.AddCookie(&http.Cookie{Name: portalNonceCookieName, Value: tt.cookie})
			}
			w := httptest.NewRecorder()
			portal.HandlePortalBrokerCallback(w, req)

			require.Equal(t, http.StatusSeeOther, w.Code)
			assert.True(t, strings.HasPrefix(w.Header().Get("Location"), "/student/login?error="))
			assert.Empty(t, studentSessionEmails(portal))
			assert.Nil(t, cookieNamed(w, StudentSessionCookieName))
		})
	}
}

// Password login on a portal checks the hash the admin synced, locally.
func TestPortal_PasswordLogin(t *testing.T) {
	t.Parallel()
	central := createTestAuthHandler()
	portal := newTestPortalAuth(PortalAuthState{StudentPasswordHash: central.StudentAuthSnapshot().StudentPasswordHash})

	login := func(password string) *httptest.ResponseRecorder {
		form := url.Values{"email": {"alice@example.com"}, "password_hash": {sha256Hex(password)}}
		req := httptest.NewRequest(http.MethodPost, "/student/login", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		portal.HandleStudentLogin(w, req)
		return w
	}

	w := login("wrong-password")
	assert.Contains(t, w.Header().Get("Location"), "error=")
	assert.Empty(t, studentSessionEmails(portal))

	w = login("test-student")
	assert.Equal(t, "/student/dashboard", w.Header().Get("Location"))
	assert.Equal(t, []string{"alice@example.com"}, studentSessionEmails(portal))
}
