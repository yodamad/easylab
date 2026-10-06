package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Brokered sign-in lets a student portal running inside a lab's cluster offer
// Azure AD, GitHub and GitLab sign-in without being registered with any of them.
// Each portal lives on its own host, and an OAuth application only redirects to
// the callbacks registered for it (a GitHub OAuth App accepts a single host), so
// the central instance — whose callback is the registered one — runs the flow on
// the portal's behalf:
//
//  1. the portal sends the student to the central login endpoint, naming its lab
//     and a nonce it also keeps in a cookie (HandlePortalProviderLogin);
//  2. the central instance runs the provider's flow as usual, and remembers who
//     to answer alongside the OAuth state (beginBrokeredLogin);
//  3. instead of opening a session of its own, it sends the student back to the
//     lab's portal with a short-lived assertion signed with a secret only the two
//     share (completeStudentLogin);
//  4. the portal checks the signature, the lab, the expiry and that the nonce is
//     the one in this browser's cookie, then opens the session
//     (HandlePortalBrokerCallback).
//
// The portal's address in step 3 comes from the lab's own configuration, never
// from the request, so the endpoint cannot be used to redirect a signed identity
// somewhere else.

const (
	// brokerAssertionTTL is how long a sign-in assertion is accepted for: the
	// time of one redirect.
	brokerAssertionTTL = 60 * time.Second

	// portalBrokerCallbackPath is where a portal receives the assertion.
	portalBrokerCallbackPath = "/student/auth/broker/callback"
	// portalNonceCookieName binds a brokered sign-in to the browser that started it.
	portalNonceCookieName = "portal_oauth_nonce"
	portalNonceCookiePath = "/student/auth/broker"

	brokerLabParam   = "lab"
	brokerNonceParam = "nonce"
)

// Providers a portal can be brokered a sign-in for, as they appear in the
// /student/auth/<provider>/login path.
const (
	brokerProviderAzure  = "azure"
	brokerProviderGitHub = "github"
	brokerProviderGitLab = "gitlab"
)

// brokerNonceRegex is deliberately strict: the nonce is echoed into a signed
// assertion and a redirect URL.
var brokerNonceRegex = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

// brokerClaims is what the central instance vouches for.
type brokerClaims struct {
	Email string `json:"email"`
	Lab   string `json:"lab"`
	Nonce string `json:"nonce"`
	Exp   int64  `json:"exp"`
}

// signBrokerAssertion renders claims as "<payload>.<signature>", both base64url,
// the signature being an HMAC-SHA256 of the encoded payload.
func signBrokerAssertion(secret string, claims brokerClaims) (string, error) {
	if secret == "" {
		return "", fmt.Errorf("broker secret is empty")
	}
	raw, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("failed to encode assertion: %w", err)
	}
	payload := base64.RawURLEncoding.EncodeToString(raw)
	return payload + "." + brokerSignature(secret, payload), nil
}

func brokerSignature(secret, payload string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// verifyBrokerAssertion checks an assertion's signature and expiry and returns
// its claims. The caller still has to check the lab and the nonce.
func verifyBrokerAssertion(secret, assertion string, now time.Time) (brokerClaims, error) {
	var claims brokerClaims
	if secret == "" {
		return claims, fmt.Errorf("broker secret is empty")
	}
	payload, signature, ok := strings.Cut(assertion, ".")
	if !ok || payload == "" || signature == "" {
		return claims, fmt.Errorf("malformed assertion")
	}
	if subtle.ConstantTimeCompare([]byte(signature), []byte(brokerSignature(secret, payload))) != 1 {
		return claims, fmt.Errorf("invalid assertion signature")
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return claims, fmt.Errorf("malformed assertion payload: %w", err)
	}
	if err := json.Unmarshal(raw, &claims); err != nil {
		return claims, fmt.Errorf("malformed assertion payload: %w", err)
	}
	if now.Unix() > claims.Exp {
		return claims, fmt.Errorf("assertion expired")
	}
	return claims, nil
}

// brokerReturn is where a brokered sign-in ends: the portal that asked for it.
type brokerReturn struct {
	labID     string
	nonce     string
	portalURL string
	secret    string
	expiresAt time.Time
}

// brokerReturnContextKey carries the brokerReturn of the sign-in a callback
// request completes, when there is one.
const brokerReturnContextKey contextKey = "brokerReturn"

// SetBrokerLabResolver wires how the central instance finds a lab's portal: its
// public URL and the secret it shares with it. ok is false for a lab that has no
// portal to sign students in to.
func (ah *AuthHandler) SetBrokerLabResolver(resolve func(labID string) (portalURL, secret string, ok bool)) {
	ah.mu.Lock()
	defer ah.mu.Unlock()
	ah.brokerLabResolver = resolve
}

// SetBrokerOnly makes the student sign-in endpoints serve in-lab portals only: on
// an admin-only instance there is no student session to open here.
func (ah *AuthHandler) SetBrokerOnly(brokerOnly bool) {
	ah.mu.Lock()
	defer ah.mu.Unlock()
	ah.brokerOnly = brokerOnly
}

// beginBrokeredLogin is called by the provider login handlers once they have an
// OAuth state. When the request comes from an in-lab portal it records who to
// answer under that state. It reports false — having written the response — when
// the sign-in must not go ahead.
func (ah *AuthHandler) beginBrokeredLogin(w http.ResponseWriter, r *http.Request, state string) bool {
	labID := r.URL.Query().Get(brokerLabParam)

	ah.mu.RLock()
	resolve := ah.brokerLabResolver
	brokerOnly := ah.brokerOnly
	ah.mu.RUnlock()

	if labID == "" {
		if brokerOnly {
			http.NotFound(w, r)
			return false
		}
		return true
	}

	nonce := r.URL.Query().Get(brokerNonceParam)
	if resolve == nil || !brokerNonceRegex.MatchString(nonce) {
		http.Error(w, "Invalid sign-in request", http.StatusBadRequest)
		return false
	}
	portalURL, secret, ok := resolve(labID)
	if !ok || portalURL == "" || secret == "" {
		log.Printf("Brokered sign-in refused: lab %q has no student portal", labID)
		http.Error(w, "Invalid sign-in request", http.StatusBadRequest)
		return false
	}

	ah.mu.Lock()
	if ah.brokerReturns == nil {
		ah.brokerReturns = make(map[string]brokerReturn)
	}
	ah.brokerReturns[state] = brokerReturn{
		labID:     labID,
		nonce:     nonce,
		portalURL: portalURL,
		secret:    secret,
		expiresAt: time.Now().Add(azureOAuthStateExpiry),
	}
	ah.mu.Unlock()
	return true
}

// resumeBrokeredLogin is called first thing by the provider callbacks. It takes
// the brokerReturn recorded for the request's state, if any, and attaches it to
// the request so that both the success path and every error path answer the
// portal rather than this instance's own login page.
func (ah *AuthHandler) resumeBrokeredLogin(r *http.Request) *http.Request {
	state := r.URL.Query().Get("state")
	if state == "" {
		return r
	}
	ah.mu.Lock()
	ret, ok := ah.brokerReturns[state]
	if ok {
		delete(ah.brokerReturns, state)
	}
	ah.mu.Unlock()
	if !ok || time.Now().After(ret.expiresAt) {
		return r
	}
	return r.WithContext(context.WithValue(r.Context(), brokerReturnContextKey, ret))
}

func brokerReturnFromContext(r *http.Request) (brokerReturn, bool) {
	ret, ok := r.Context().Value(brokerReturnContextKey).(brokerReturn)
	return ret, ok
}

// studentLoginURL is the student login page a sign-in attempt goes back to: this
// instance's own, or the portal's when the attempt was brokered for one.
func studentLoginURL(r *http.Request) string {
	if ret, ok := brokerReturnFromContext(r); ok {
		return ret.portalURL + "/student/login"
	}
	return "/student/login"
}

// completeStudentLogin finishes a provider sign-in for the student identified by
// email: by opening a session here, or — for a brokered sign-in — by handing the
// lab's portal a signed assertion to open its own from.
func (ah *AuthHandler) completeStudentLogin(w http.ResponseWriter, r *http.Request, email, provider string) {
	if ret, ok := brokerReturnFromContext(r); ok {
		assertion, err := signBrokerAssertion(ret.secret, brokerClaims{
			Email: email,
			Lab:   ret.labID,
			Nonce: ret.nonce,
			Exp:   time.Now().Add(brokerAssertionTTL).Unix(),
		})
		if err != nil {
			log.Printf("Brokered %s sign-in for lab %s: %v", provider, ret.labID, err)
			http.Redirect(w, r, studentLoginURL(r)+"?error="+url.QueryEscape("Sign-in failed. Try again, or ask your instructor."), http.StatusSeeOther)
			return
		}
		log.Printf("Successful %s student login, brokered for lab %s", provider, ret.labID)
		http.Redirect(w, r, ret.portalURL+portalBrokerCallbackPath+"?assertion="+url.QueryEscape(assertion), http.StatusSeeOther)
		return
	}

	ah.mu.RLock()
	brokerOnly := ah.brokerOnly
	ah.mu.RUnlock()
	if brokerOnly {
		http.NotFound(w, r)
		return
	}

	ah.openStudentSession(w, r, email)
	log.Printf("Successful %s student login", provider)
	http.Redirect(w, r, "/student/dashboard", http.StatusSeeOther)
}

// openStudentSession creates a student session and sets its cookies after a
// sign-in that came back from another site.
//
// SameSite=Lax (not Strict) so the cookies are included when the browser follows
// the redirect out of the callback: the overall navigation is cross-site (it
// originated from the identity provider, or from the central instance), and
// Strict would keep the cookie off that first same-site hop in some browsers.
func (ah *AuthHandler) openStudentSession(w http.ResponseWriter, r *http.Request, email string) {
	sessionToken, csrfToken := ah.createStudentSession(email)
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
}

// portalAuthSettings is what an in-lab portal needs to have a sign-in brokered.
type portalAuthSettings struct {
	labID        string
	adminURL     string
	brokerSecret string
}

// NewStudentAuthHandler creates the AuthHandler of an in-lab student portal. It
// has no admin side at all — no admin password, no admin session can ever be
// valid — and starts with student sign-in closed until ConfigurePortalAuth
// applies the settings the admin synced.
func NewStudentAuthHandler() *AuthHandler {
	ah := &AuthHandler{
		sessions:          make(map[string]*Session),
		studentSessions:   make(map[string]*Session),
		azureOAuthStates:  make(map[string]time.Time),
		githubOAuthStates: make(map[string]time.Time),
		gitlabOAuthStates: make(map[string]time.Time),
		templates:         make(map[string]*template.Template),
		loginAttempts:     make(map[string]*loginAttemptState),
	}
	ah.startEviction()
	return ah
}

// ConfigurePortalAuth applies the sign-in settings an in-lab portal mirrors from
// the admin. The provider flags only decide which buttons the login page shows
// and whether student sign-in is open at all: the flows themselves run on the
// admin instance (see HandlePortalProviderLogin).
func (ah *AuthHandler) ConfigurePortalAuth(labID string, s PortalAuthState) {
	ah.mu.Lock()
	defer ah.mu.Unlock()

	brokered := s.AdminURL != "" && s.BrokerSecret != ""
	ah.studentPasswordHash = s.StudentPasswordHash
	ah.azureADEnabled = brokered && s.AzureAD
	ah.githubEnabled = brokered && s.GitHub
	ah.gitlabEnabled = brokered && s.GitLab
	// Never without a provider to use instead: that would lock every student out.
	ah.portalClassicLoginDisabled = s.ClassicLoginDisabled && (ah.azureADEnabled || ah.githubEnabled || ah.gitlabEnabled)
	ah.portalAuth = &portalAuthSettings{
		labID:        labID,
		adminURL:     strings.TrimRight(s.AdminURL, "/"),
		brokerSecret: s.BrokerSecret,
	}
}

// StudentAuthSnapshot reports the student sign-in settings an in-lab portal
// mirrors from this instance.
func (ah *AuthHandler) StudentAuthSnapshot() StudentAuthSnapshot {
	ah.mu.RLock()
	defer ah.mu.RUnlock()
	return StudentAuthSnapshot{
		StudentPasswordHash:  ah.studentPasswordHash,
		ClassicLoginDisabled: ah.classicStudentLoginDisabledLocked(),
		AzureAD:              ah.azureADEnabled,
		GitHub:               ah.githubEnabled,
		GitLab:               ah.gitlabEnabled,
	}
}

// portalProviderEnabledLocked reports whether the portal offers provider.
// Callers hold ah.mu.
func (ah *AuthHandler) portalProviderEnabledLocked(provider string) bool {
	switch provider {
	case brokerProviderAzure:
		return ah.azureADEnabled
	case brokerProviderGitHub:
		return ah.githubEnabled
	case brokerProviderGitLab:
		return ah.gitlabEnabled
	default:
		return false
	}
}

// portalLoginError sends the student back to the portal's login page with a
// safe message.
func portalLoginError(w http.ResponseWriter, r *http.Request, message string) {
	http.Redirect(w, r, "/student/login?error="+url.QueryEscape(message), http.StatusSeeOther)
}

// HandlePortalProviderLogin starts a brokered sign-in from an in-lab portal: it
// replaces the provider login handlers on /student/auth/<provider>/login there.
func (ah *AuthHandler) HandlePortalProviderLogin(provider string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ah.mu.RLock()
		settings := ah.portalAuth
		enabled := ah.portalProviderEnabledLocked(provider)
		ah.mu.RUnlock()

		if settings == nil || !enabled || settings.adminURL == "" {
			portalLoginError(w, r, "That sign-in method is not available.")
			return
		}

		// Bind the attempt to this browser: the assertion that comes back must
		// carry the same nonce as this cookie, so one obtained in another browser
		// cannot be replayed here. Lax so it is sent on the redirect back.
		nonce := generateToken()
		http.SetCookie(w, &http.Cookie{
			Name:     portalNonceCookieName,
			Value:    nonce,
			Path:     portalNonceCookiePath,
			HttpOnly: true,
			Secure:   isSecureRequest(r),
			SameSite: http.SameSiteLaxMode,
			MaxAge:   int(azureOAuthStateExpiry.Seconds()),
		})

		target := settings.adminURL + "/student/auth/" + provider + "/login?" + url.Values{
			brokerLabParam:   {settings.labID},
			brokerNonceParam: {nonce},
		}.Encode()
		http.Redirect(w, r, target, http.StatusFound)
	}
}

// HandlePortalBrokerCallback completes a brokered sign-in on an in-lab portal.
func (ah *AuthHandler) HandlePortalBrokerCallback(w http.ResponseWriter, r *http.Request) {
	ah.mu.RLock()
	settings := ah.portalAuth
	ah.mu.RUnlock()

	// Single use: whatever happens next, this browser's nonce is spent.
	http.SetCookie(w, &http.Cookie{
		Name:     portalNonceCookieName,
		Value:    "",
		Path:     portalNonceCookiePath,
		HttpOnly: true,
		MaxAge:   -1,
	})

	if settings == nil || settings.brokerSecret == "" {
		portalLoginError(w, r, "That sign-in method is not available.")
		return
	}

	claims, err := verifyBrokerAssertion(settings.brokerSecret, r.URL.Query().Get("assertion"), time.Now())
	if err != nil {
		log.Printf("Brokered sign-in rejected: %v", err)
		portalLoginError(w, r, "That sign-in attempt expired. Try again.")
		return
	}
	nonceCookie, cookieErr := r.Cookie(portalNonceCookieName)
	if claims.Lab != settings.labID || cookieErr != nil || claims.Nonce == "" ||
		subtle.ConstantTimeCompare([]byte(nonceCookie.Value), []byte(claims.Nonce)) != 1 {
		log.Printf("Brokered sign-in rejected: assertion was not issued for this lab and browser")
		portalLoginError(w, r, "That sign-in attempt expired. Try again.")
		return
	}
	if !strings.Contains(claims.Email, "@") {
		log.Printf("Brokered sign-in rejected: assertion carries no usable identity")
		portalLoginError(w, r, "Sign-in failed. Try again, or ask your instructor.")
		return
	}

	ah.openStudentSession(w, r, claims.Email)
	log.Printf("Successful brokered student login")
	http.Redirect(w, r, "/student/dashboard", http.StatusSeeOther)
}
