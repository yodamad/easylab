package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"math/big"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/microsoft"
)

const (
	// Environment variable for the admin password
	EnvAdminPassword = "LAB_ADMIN_PASSWORD"
	// Environment variable for the student password
	EnvStudentPassword = "LAB_STUDENT_PASSWORD"
	// Environment variables for Azure AD OAuth student login
	EnvAzureADClientID     = "AZURE_AD_CLIENT_ID"
	EnvAzureADClientSecret = "AZURE_AD_CLIENT_SECRET"
	EnvAzureADTenantID     = "AZURE_AD_TENANT_ID"
	// EnvAzureADAdminGroupID restricts admin Azure AD login to direct members of this group.
	EnvAzureADAdminGroupID = "AZURE_AD_ADMIN_GROUP_ID"
	// Cookie name for session
	SessionCookieName = "lab_session"
	// Cookie name for student session
	StudentSessionCookieName = "lab_student_session"
	// Session expiry duration
	SessionExpiry = 24 * time.Hour
	// OAuth state expiry (short-lived, only valid during the redirect round-trip)
	azureOAuthStateExpiry = 5 * time.Minute
)

// contextKey is a type for context keys in this package
type contextKey string

// studentEmailContextKey is the context key for the student email
const studentEmailContextKey contextKey = "studentEmail"

// Session represents a user session
type Session struct {
	Token     string
	Email     string
	ExpiresAt time.Time
}

// AuthHandler handles authentication
type AuthHandler struct {
	passwordHash              string
	studentPasswordHash       string
	sessions                  map[string]*Session
	studentSessions           map[string]*Session
	azureADEnabled            bool
	azureADConfig             *oauth2.Config
	azureOAuthStates          map[string]time.Time
	classicLoginDisabled      bool
	adminGroupID              string
	classicAdminLoginDisabled bool
	templates                 map[string]*template.Template
	templatesMu               sync.RWMutex
	mu                        sync.RWMutex
}

// hashPassword creates a bcrypt hash of the password
func hashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("failed to hash password: %w", err)
	}
	return string(hash), nil
}

// comparePassword compares a plaintext password with a bcrypt hash
func comparePassword(hashedPassword, plainPassword string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hashedPassword), []byte(plainPassword))
	return err == nil
}

// NewAuthHandler creates a new auth handler
func NewAuthHandler() (*AuthHandler, error) {
	password := os.Getenv(EnvAdminPassword)
	if password == "" {
		return nil, fmt.Errorf("%s environment variable not set. Admin password is required", EnvAdminPassword)
	}

	// Hash password with SHA-256 first, then bcrypt for secure storage
	sha256Hash := sha256.Sum256([]byte(password))
	passwordHash, err := hashPassword(hex.EncodeToString(sha256Hash[:]))
	if err != nil {
		return nil, fmt.Errorf("failed to hash admin password: %w", err)
	}
	log.Printf("Admin password hash initialized")

	// Initialize student password
	studentPassword := os.Getenv(EnvStudentPassword)
	var studentPasswordHash string
	if studentPassword == "" {
		log.Printf("WARNING: %s environment variable not set. Student login will be disabled.", EnvStudentPassword)
		studentPasswordHash = ""
	} else {
		// Hash password with SHA-256 first, then bcrypt for secure storage
		sha256Hash := sha256.Sum256([]byte(studentPassword))
		studentPasswordHash, err = hashPassword(hex.EncodeToString(sha256Hash[:]))
		if err != nil {
			return nil, fmt.Errorf("failed to hash student password: %w", err)
		}
		log.Printf("Student password hash initialized")
	}

	// Initialize Azure AD OAuth for student login
	azureClientID := os.Getenv(EnvAzureADClientID)
	azureClientSecret := os.Getenv(EnvAzureADClientSecret)
	azureTenantID := os.Getenv(EnvAzureADTenantID)

	var azureADEnabled bool
	var azureADConfig *oauth2.Config
	if azureClientID != "" && azureClientSecret != "" && azureTenantID != "" {
		azureADConfig = &oauth2.Config{
			ClientID:     azureClientID,
			ClientSecret: azureClientSecret,
			Scopes:       []string{"openid", "email", "profile"},
			Endpoint:     microsoft.AzureADEndpoint(azureTenantID),
		}
		azureADEnabled = true
		log.Printf("Azure AD student login enabled (tenant: %s)", azureTenantID)
	}

	adminGroupID := os.Getenv(EnvAzureADAdminGroupID)
	if adminGroupID != "" && azureADEnabled {
		log.Printf("Azure AD admin login enabled (group: %s)", adminGroupID)
	}

	ah := &AuthHandler{
		passwordHash:        passwordHash,
		studentPasswordHash: studentPasswordHash,
		sessions:            make(map[string]*Session),
		studentSessions:     make(map[string]*Session),
		azureADEnabled:      azureADEnabled,
		azureADConfig:       azureADConfig,
		azureOAuthStates:    make(map[string]time.Time),
		adminGroupID:        adminGroupID,
		templates:           make(map[string]*template.Template),
	}

	// Periodically evict expired sessions and OAuth states so the maps don't grow unboundedly.
	go func() {
		ticker := time.NewTicker(15 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			now := time.Now()
			ah.mu.Lock()
			for tok, s := range ah.sessions {
				if now.After(s.ExpiresAt) {
					delete(ah.sessions, tok)
				}
			}
			for tok, s := range ah.studentSessions {
				if now.After(s.ExpiresAt) {
					delete(ah.studentSessions, tok)
				}
			}
			for state, expiry := range ah.azureOAuthStates {
				if now.After(expiry) {
					delete(ah.azureOAuthStates, state)
				}
			}
			ah.mu.Unlock()
		}
	}()

	return ah, nil
}

// ConfigureAzureAD updates the Azure AD OAuth config at runtime. Passing empty strings disables it.
func (ah *AuthHandler) ConfigureAzureAD(clientID, clientSecret, tenantID string) {
	ah.mu.Lock()
	defer ah.mu.Unlock()

	if clientID != "" && clientSecret != "" && tenantID != "" {
		ah.azureADConfig = &oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			Scopes:       []string{"openid", "email", "profile"},
			Endpoint:     microsoft.AzureADEndpoint(tenantID),
		}
		ah.azureADEnabled = true
		log.Printf("Azure AD student login configured (tenant: %s)", tenantID)
	} else {
		ah.azureADConfig = nil
		ah.azureADEnabled = false
		log.Printf("Azure AD student login disabled")
	}
}

// AzureADEnabled reports whether Azure AD login is currently enabled.
func (ah *AuthHandler) AzureADEnabled() bool {
	ah.mu.RLock()
	defer ah.mu.RUnlock()
	return ah.azureADEnabled
}

// SetClassicLoginDisabled enables or disables the password-based student login form.
// Classic login can only be disabled when Azure AD is enabled; the flag is ignored otherwise.
func (ah *AuthHandler) SetClassicLoginDisabled(disabled bool) {
	ah.mu.Lock()
	defer ah.mu.Unlock()
	ah.classicLoginDisabled = disabled
}

// SetAdminGroupID sets the Azure AD group ID that admin users must directly belong to.
// Passing an empty string disables admin Azure AD login.
func (ah *AuthHandler) SetAdminGroupID(groupID string) {
	ah.mu.Lock()
	defer ah.mu.Unlock()
	ah.adminGroupID = groupID
	if groupID != "" {
		log.Printf("Azure AD admin login configured (group: %s)", groupID)
	} else {
		log.Printf("Azure AD admin login disabled (group ID cleared)")
	}
}

// AdminAzureADEnabled reports whether admin Azure AD login is currently configured.
func (ah *AuthHandler) AdminAzureADEnabled() bool {
	ah.mu.RLock()
	defer ah.mu.RUnlock()
	return ah.azureADEnabled && ah.adminGroupID != ""
}

// SetClassicAdminLoginDisabled enables or disables the password-based admin login form.
// Only takes effect when AdminAzureADEnabled() is true; the flag is ignored otherwise.
func (ah *AuthHandler) SetClassicAdminLoginDisabled(disabled bool) {
	ah.mu.Lock()
	defer ah.mu.Unlock()
	ah.classicAdminLoginDisabled = disabled
}

// generateToken creates a secure random token
func generateToken() string {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		panic(fmt.Sprintf("crypto/rand unavailable: %v", err))
	}
	return hex.EncodeToString(bytes)
}

// createSession creates a new session and returns the token
func (ah *AuthHandler) createSession() string {
	ah.mu.Lock()
	defer ah.mu.Unlock()

	token := generateToken()
	ah.sessions[token] = &Session{
		Token:     token,
		ExpiresAt: time.Now().Add(SessionExpiry),
	}

	return token
}

// validateSession checks if a session token is valid
func (ah *AuthHandler) validateSession(token string) bool {
	ah.mu.RLock()
	defer ah.mu.RUnlock()

	session, exists := ah.sessions[token]
	if !exists {
		return false
	}

	if time.Now().After(session.ExpiresAt) {
		// Session expired, clean it up
		go ah.deleteSession(token)
		return false
	}

	return true
}

// deleteSession removes a session
func (ah *AuthHandler) deleteSession(token string) {
	ah.mu.Lock()
	defer ah.mu.Unlock()
	delete(ah.sessions, token)
}

// getTemplate retrieves a cached template by filename, loading it lazily if needed
func (ah *AuthHandler) getTemplate(filename string) (*template.Template, error) {
	// Fast path: check cache first
	ah.templatesMu.RLock()
	tmpl, ok := ah.templates[filename]
	ah.templatesMu.RUnlock()
	if ok {
		return tmpl, nil
	}

	// Slow path: load template
	ah.templatesMu.Lock()
	defer ah.templatesMu.Unlock()

	// Double-check after acquiring write lock
	if tmpl, ok := ah.templates[filename]; ok {
		return tmpl, nil
	}

	// Map filename to full path
	templatePaths := map[string]string{
		"login.html":         "web/login.html",
		"student-login.html": "web/student-login.html",
	}

	tmplPath, ok := templatePaths[filename]
	if !ok {
		return nil, fmt.Errorf("template %s not found", filename)
	}

	var err error
	// Parse base template and page template together
	tmpl, err = template.ParseFiles("web/base.html", tmplPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load template %s: %w", tmplPath, err)
	}

	// Execute the base template which will include the page-specific blocks
	ah.templates[filename] = tmpl
	return tmpl, nil
}

// ServeLogin serves the login page
func (ah *AuthHandler) ServeLogin(w http.ResponseWriter, r *http.Request) {
	// Check if already logged in
	if cookie, err := r.Cookie(SessionCookieName); err == nil {
		if ah.validateSession(cookie.Value) {
			http.Redirect(w, r, "/labs", http.StatusSeeOther)
			return
		}
	}

	// Use cached template (lazy loaded)
	tmpl, err := ah.getTemplate("login.html")
	if err != nil {
		log.Printf("Failed to load login template: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Prevent caching
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	ah.mu.RLock()
	azureAdminEnabled := ah.azureADEnabled && ah.adminGroupID != ""
	classicAdminDisabled := ah.classicAdminLoginDisabled && azureAdminEnabled
	ah.mu.RUnlock()

	data := map[string]interface{}{
		"Error":                r.URL.Query().Get("error"),
		"AzureADAdminEnabled":  azureAdminEnabled,
		"ClassicLoginDisabled": classicAdminDisabled,
	}

	if err := tmpl.ExecuteTemplate(w, "base", data); err != nil {
		log.Printf("Failed to execute login template: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// HandleLogin processes login form submission
func (ah *AuthHandler) HandleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	ah.mu.RLock()
	classicAdminDisabled := ah.classicAdminLoginDisabled && ah.azureADEnabled && ah.adminGroupID != ""
	ah.mu.RUnlock()

	if classicAdminDisabled {
		http.Redirect(w, r, "/login?error=Password+login+is+disabled%2C+please+use+Microsoft+login", http.StatusSeeOther)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/login?error=Invalid+request", http.StatusSeeOther)
		return
	}

	// Get password hash from form (client-side SHA-256 hashed)
	passwordHash := getFormValue(r, "password_hash")
	if passwordHash == "" {
		log.Printf("Failed login attempt: empty password hash")
		http.Redirect(w, r, "/login?error=Invalid+password", http.StatusSeeOther)
		return
	}

	// Compare received SHA-256 hash with stored bcrypt(SHA-256(password)) hash
	if !comparePassword(ah.passwordHash, passwordHash) {
		log.Printf("Failed login attempt")
		http.Redirect(w, r, "/login?error=Invalid+password", http.StatusSeeOther)
		return
	}

	// Create session
	token := ah.createSession()

	// Determine if HTTPS is being used
	isSecure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"

	// Set cookie
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(SessionExpiry.Seconds()),
	})

	log.Printf("Successful login")
	http.Redirect(w, r, "/labs", http.StatusSeeOther)
}

// HandleLogout logs out the user
func (ah *AuthHandler) HandleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(SessionCookieName); err == nil {
		ah.deleteSession(cookie.Value)
	}

	// Clear cookie
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// RequireAuth is middleware that requires authentication
func (ah *AuthHandler) RequireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(SessionCookieName)
		if err != nil || !ah.validateSession(cookie.Value) {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

// createStudentSession creates a new student session and returns the token
func (ah *AuthHandler) createStudentSession(email string) string {
	ah.mu.Lock()
	defer ah.mu.Unlock()

	token := generateToken()
	ah.studentSessions[token] = &Session{
		Token:     token,
		Email:     email,
		ExpiresAt: time.Now().Add(SessionExpiry),
	}

	return token
}

// getStudentSessionEmail returns the email associated with a student session token
func (ah *AuthHandler) getStudentSessionEmail(token string) string {
	ah.mu.RLock()
	defer ah.mu.RUnlock()

	session, exists := ah.studentSessions[token]
	if !exists {
		return ""
	}
	return session.Email
}

// studentEmailFromContext retrieves the student email stored in the request context
func studentEmailFromContext(r *http.Request) string {
	email, _ := r.Context().Value(studentEmailContextKey).(string)
	return email
}

// validateStudentSession checks if a student session token is valid
func (ah *AuthHandler) validateStudentSession(token string) bool {
	ah.mu.RLock()
	defer ah.mu.RUnlock()

	session, exists := ah.studentSessions[token]
	if !exists {
		return false
	}

	if time.Now().After(session.ExpiresAt) {
		// Session expired, clean it up
		go ah.deleteStudentSession(token)
		return false
	}

	return true
}

// deleteStudentSession removes a student session
func (ah *AuthHandler) deleteStudentSession(token string) {
	ah.mu.Lock()
	defer ah.mu.Unlock()
	delete(ah.studentSessions, token)
}

// ServeStudentLogin serves the student login page
func (ah *AuthHandler) ServeStudentLogin(w http.ResponseWriter, r *http.Request) {
	// Check if already logged in
	if cookie, err := r.Cookie(StudentSessionCookieName); err == nil {
		if ah.validateStudentSession(cookie.Value) {
			http.Redirect(w, r, "/student/dashboard", http.StatusSeeOther)
			return
		}
	}

	// Use cached template (lazy loaded)
	tmpl, err := ah.getTemplate("student-login.html")
	if err != nil {
		log.Printf("Failed to load student login template: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Prevent caching
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	ah.mu.RLock()
	azureADEnabled := ah.azureADEnabled
	classicDisabled := ah.classicLoginDisabled && ah.azureADEnabled
	ah.mu.RUnlock()

	data := map[string]interface{}{
		"Error":                r.URL.Query().Get("error"),
		"AzureADEnabled":       azureADEnabled,
		"ClassicLoginDisabled": classicDisabled,
	}

	if err := tmpl.ExecuteTemplate(w, "base", data); err != nil {
		log.Printf("Failed to execute student login template: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// HandleStudentLogin processes student login form submission
func (ah *AuthHandler) HandleStudentLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/student/login", http.StatusSeeOther)
		return
	}

	ah.mu.RLock()
	storedHash := ah.studentPasswordHash
	classicDisabled := ah.classicLoginDisabled && ah.azureADEnabled
	ah.mu.RUnlock()

	if storedHash == "" {
		http.Redirect(w, r, "/student/login?error=Student+login+disabled", http.StatusSeeOther)
		return
	}
	if classicDisabled {
		http.Redirect(w, r, "/student/login?error=Password+login+is+disabled%2C+please+use+Microsoft+login", http.StatusSeeOther)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/student/login?error=Invalid+request", http.StatusSeeOther)
		return
	}

	// Get password hash from form (client-side SHA-256 hashed)
	passwordHash := getFormValue(r, "password_hash")
	if passwordHash == "" {
		log.Printf("Failed student login attempt: empty password hash")
		http.Redirect(w, r, "/student/login?error=Invalid+password", http.StatusSeeOther)
		return
	}

	// Compare received SHA-256 hash with stored bcrypt(SHA-256(password)) hash
	if !comparePassword(storedHash, passwordHash) {
		log.Printf("Failed student login attempt")
		http.Redirect(w, r, "/student/login?error=Invalid+password", http.StatusSeeOther)
		return
	}

	// Get and validate email from form
	email := getFormValue(r, "email")
	if email == "" || !strings.Contains(email, "@") {
		http.Redirect(w, r, "/student/login?error=Invalid+email", http.StatusSeeOther)
		return
	}

	// Create session
	token := ah.createStudentSession(email)

	// Determine if HTTPS is being used
	isSecure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"

	// Set cookie
	http.SetCookie(w, &http.Cookie{
		Name:     StudentSessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(SessionExpiry.Seconds()),
	})

	log.Printf("Successful student login")
	http.Redirect(w, r, "/student/dashboard", http.StatusSeeOther)
}

// HandleStudentLogout logs out the student
func (ah *AuthHandler) HandleStudentLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(StudentSessionCookieName); err == nil {
		ah.deleteStudentSession(cookie.Value)
	}

	// Clear cookie
	http.SetCookie(w, &http.Cookie{
		Name:     StudentSessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// RequireStudentAuth is middleware that requires student authentication
func (ah *AuthHandler) RequireStudentAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if ah.studentPasswordHash == "" && !ah.azureADEnabled {
			http.Error(w, "Student login is disabled", http.StatusForbidden)
			return
		}
		cookie, err := r.Cookie(StudentSessionCookieName)
		if err != nil || !ah.validateStudentSession(cookie.Value) {
			http.Redirect(w, r, "/student/login", http.StatusSeeOther)
			return
		}
		email := ah.getStudentSessionEmail(cookie.Value)
		ctx := context.WithValue(r.Context(), studentEmailContextKey, email)
		next(w, r.WithContext(ctx))
	}
}

// HandleAzureADLogin initiates the Azure AD OAuth 2.0 flow for student login.
func (ah *AuthHandler) HandleAzureADLogin(w http.ResponseWriter, r *http.Request) {
	if !ah.azureADEnabled {
		http.Redirect(w, r, "/student/login?error=Azure+AD+login+not+configured", http.StatusSeeOther)
		return
	}

	state := generateToken()
	ah.mu.Lock()
	ah.azureOAuthStates[state] = time.Now().Add(azureOAuthStateExpiry)
	ah.mu.Unlock()

	isSecure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
	scheme := "http"
	if isSecure {
		scheme = "https"
	}
	redirectURI := scheme + "://" + r.Host + "/student/auth/azure/callback"

	authURL := ah.azureADConfig.AuthCodeURL(state, oauth2.SetAuthURLParam("redirect_uri", redirectURI))
	http.Redirect(w, r, authURL, http.StatusFound)
}

// HandleAzureADCallback handles the OAuth 2.0 callback from Azure AD.
// It exchanges the authorization code for tokens, extracts the student email,
// and creates a local session — identical to the password-based login flow.
func (ah *AuthHandler) HandleAzureADCallback(w http.ResponseWriter, r *http.Request) {
	if !ah.azureADEnabled {
		http.Redirect(w, r, "/student/login?error=Azure+AD+login+not+configured", http.StatusSeeOther)
		return
	}

	errParam := r.URL.Query().Get("error")
	if errParam != "" {
		log.Printf("Azure AD OAuth error: %s", errParam)
		http.Redirect(w, r, "/student/login?error=Azure+AD+authentication+failed", http.StatusSeeOther)
		return
	}

	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")

	// Validate state to prevent CSRF
	ah.mu.Lock()
	expiry, valid := ah.azureOAuthStates[state]
	if valid {
		delete(ah.azureOAuthStates, state)
	}
	ah.mu.Unlock()

	if !valid || time.Now().After(expiry) {
		log.Printf("Azure AD callback: invalid or expired state")
		http.Redirect(w, r, "/student/login?error=Invalid+authentication+state", http.StatusSeeOther)
		return
	}

	if code == "" {
		http.Redirect(w, r, "/student/login?error=Missing+authorization+code", http.StatusSeeOther)
		return
	}

	isSecure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
	scheme := "http"
	if isSecure {
		scheme = "https"
	}
	redirectURI := scheme + "://" + r.Host + "/student/auth/azure/callback"

	token, err := ah.azureADConfig.Exchange(r.Context(), code, oauth2.SetAuthURLParam("redirect_uri", redirectURI))
	if err != nil {
		log.Printf("Azure AD token exchange failed: %v", err)
		http.Redirect(w, r, "/student/login?error=Authentication+failed", http.StatusSeeOther)
		return
	}

	email, err := extractEmailFromIDToken(token)
	if err != nil {
		log.Printf("Azure AD: failed to extract email: %v", err)
		http.Redirect(w, r, "/student/login?error=Could+not+retrieve+email+from+Azure+AD", http.StatusSeeOther)
		return
	}

	if !strings.Contains(email, "@") {
		log.Printf("Azure AD: invalid email format: %s", email)
		http.Redirect(w, r, "/student/login?error=Invalid+email+from+Azure+AD", http.StatusSeeOther)
		return
	}

	sessionToken := ah.createStudentSession(email)

	// Use SameSite=Lax (not Strict) so the cookie is included when the browser
	// follows the 303 redirect from our callback — the overall navigation context
	// is cross-site (originated from login.microsoftonline.com), and Strict would
	// prevent the cookie from being sent on that first same-site hop in some browsers.
	http.SetCookie(w, &http.Cookie{
		Name:     StudentSessionCookieName,
		Value:    sessionToken,
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(SessionExpiry.Seconds()),
	})

	log.Printf("Successful Azure AD student login")
	http.Redirect(w, r, "/student/dashboard", http.StatusSeeOther)
}

// HandleAdminAzureADLogin initiates the Azure AD OAuth 2.0 flow for admin login.
// The resulting access token must be usable against the Microsoft Graph API to verify group membership.
func (ah *AuthHandler) HandleAdminAzureADLogin(w http.ResponseWriter, r *http.Request) {
	if !ah.AdminAzureADEnabled() {
		http.Redirect(w, r, "/login?error=Azure+AD+admin+login+not+configured", http.StatusSeeOther)
		return
	}

	state := generateToken()
	ah.mu.Lock()
	ah.azureOAuthStates[state] = time.Now().Add(azureOAuthStateExpiry)
	ah.mu.Unlock()

	isSecure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
	scheme := "http"
	if isSecure {
		scheme = "https"
	}
	redirectURI := scheme + "://" + r.Host + "/admin/auth/azure/callback"

	// Request Graph API User.Read scope so the access token can be used for group membership check.
	authURL := ah.azureADConfig.AuthCodeURL(state,
		oauth2.SetAuthURLParam("redirect_uri", redirectURI),
		oauth2.SetAuthURLParam("scope", "openid email profile https://graph.microsoft.com/User.Read"),
	)
	http.Redirect(w, r, authURL, http.StatusFound)
}

// HandleAdminAzureADCallback handles the OAuth 2.0 callback for admin login.
// It verifies the user is a direct member of the required Azure AD group before creating an admin session.
func (ah *AuthHandler) HandleAdminAzureADCallback(w http.ResponseWriter, r *http.Request) {
	if !ah.AdminAzureADEnabled() {
		http.Redirect(w, r, "/login?error=Azure+AD+admin+login+not+configured", http.StatusSeeOther)
		return
	}

	if errParam := r.URL.Query().Get("error"); errParam != "" {
		log.Printf("Azure AD admin OAuth error: %s", errParam)
		http.Redirect(w, r, "/login?error=Azure+AD+authentication+failed", http.StatusSeeOther)
		return
	}

	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")

	ah.mu.Lock()
	expiry, valid := ah.azureOAuthStates[state]
	if valid {
		delete(ah.azureOAuthStates, state)
	}
	ah.mu.Unlock()

	if !valid || time.Now().After(expiry) {
		log.Printf("Azure AD admin callback: invalid or expired state")
		http.Redirect(w, r, "/login?error=Invalid+authentication+state", http.StatusSeeOther)
		return
	}

	if code == "" {
		http.Redirect(w, r, "/login?error=Missing+authorization+code", http.StatusSeeOther)
		return
	}

	isSecure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
	scheme := "http"
	if isSecure {
		scheme = "https"
	}
	redirectURI := scheme + "://" + r.Host + "/admin/auth/azure/callback"

	token, err := ah.azureADConfig.Exchange(r.Context(), code, oauth2.SetAuthURLParam("redirect_uri", redirectURI))
	if err != nil {
		log.Printf("Azure AD admin token exchange failed: %v", err)
		http.Redirect(w, r, "/login?error=Authentication+failed", http.StatusSeeOther)
		return
	}

	email, err := extractEmailFromIDToken(token)
	if err != nil {
		log.Printf("Azure AD admin: failed to extract email: %v", err)
		http.Redirect(w, r, "/login?error=Could+not+retrieve+email+from+Azure+AD", http.StatusSeeOther)
		return
	}

	ah.mu.RLock()
	groupID := ah.adminGroupID
	ah.mu.RUnlock()

	member, err := checkDirectGroupMembership(token.AccessToken, groupID)
	if err != nil {
		log.Printf("Azure AD admin: group membership check failed for %s: %v", email, err)
		http.Redirect(w, r, "/login?error=Could+not+verify+group+membership", http.StatusSeeOther)
		return
	}
	if !member {
		log.Printf("Azure AD admin: user %s is not a member of group %s — access denied", email, groupID)
		http.Redirect(w, r, "/login?error=Not+authorized%3A+you+are+not+a+member+of+the+required+admin+group", http.StatusSeeOther)
		return
	}

	sessionToken := ah.createSession()

	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    sessionToken,
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(SessionExpiry.Seconds()),
	})

	log.Printf("Successful Azure AD admin login: %s", email)
	http.Redirect(w, r, "/labs", http.StatusSeeOther)
}

// checkDirectGroupMembership calls the Microsoft Graph API to verify that the signed-in user
// is a direct member of the given group ID. It returns true only if the group is found in /me/memberOf.
func checkDirectGroupMembership(accessToken, groupID string) (bool, error) {
	req, err := http.NewRequest(http.MethodGet,
		"https://graph.microsoft.com/v1.0/me/memberOf?$select=id&$top=999",
		nil)
	if err != nil {
		return false, fmt.Errorf("failed to build Graph API request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("Graph API request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, fmt.Errorf("failed to read Graph API response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("Graph API returned %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Value []struct {
			ID string `json:"id"`
		} `json:"value"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return false, fmt.Errorf("failed to parse Graph API response: %w", err)
	}

	for _, g := range result.Value {
		if strings.EqualFold(g.ID, groupID) {
			return true, nil
		}
	}
	return false, nil
}

// extractEmailFromIDToken decodes the JWT id_token and returns the student's email.
// The token is already obtained from Microsoft's HTTPS endpoint so we trust the claims.
func extractEmailFromIDToken(token *oauth2.Token) (string, error) {
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return "", fmt.Errorf("id_token not present in token response")
	}

	parts := strings.Split(rawIDToken, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("malformed id_token: expected 3 segments, got %d", len(parts))
	}

	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("failed to decode id_token claims: %w", err)
	}

	var claims struct {
		Email             string `json:"email"`
		PreferredUsername string `json:"preferred_username"`
		UPN               string `json:"upn"`
	}
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		return "", fmt.Errorf("failed to parse id_token claims: %w", err)
	}

	if claims.Email != "" {
		return claims.Email, nil
	}
	if strings.Contains(claims.PreferredUsername, "@") {
		return claims.PreferredUsername, nil
	}
	if strings.Contains(claims.UPN, "@") {
		return claims.UPN, nil
	}

	return "", fmt.Errorf("no email found in id_token claims")
}

// GenerateSecurePassword generates a secure random password
// Password is at least 16 characters with mixed case, numbers, and symbols
func GenerateSecurePassword() (string, error) {
	const (
		lowercase = "abcdefghijklmnopqrstuvwxyz"
		uppercase = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
		numbers   = "0123456789"
		symbols   = "!@#$%^&*"
		allChars  = lowercase + uppercase + numbers + symbols
		minLength = 16
	)

	// Ensure we have at least one of each type
	password := make([]byte, minLength)

	// Get one lowercase
	idx, err := rand.Int(rand.Reader, big.NewInt(int64(len(lowercase))))
	if err != nil {
		return "", fmt.Errorf("failed to generate random lowercase character: %w", err)
	}
	password[0] = lowercase[idx.Int64()]

	// Get one uppercase
	idx, err = rand.Int(rand.Reader, big.NewInt(int64(len(uppercase))))
	if err != nil {
		return "", fmt.Errorf("failed to generate random uppercase character: %w", err)
	}
	password[1] = uppercase[idx.Int64()]

	// Get one number
	idx, err = rand.Int(rand.Reader, big.NewInt(int64(len(numbers))))
	if err != nil {
		return "", fmt.Errorf("failed to generate random number character: %w", err)
	}
	password[2] = numbers[idx.Int64()]

	// Get one symbol
	idx, err = rand.Int(rand.Reader, big.NewInt(int64(len(symbols))))
	if err != nil {
		return "", fmt.Errorf("failed to generate random symbol character: %w", err)
	}
	password[3] = symbols[idx.Int64()]

	// Fill the rest randomly
	for i := 4; i < minLength; i++ {
		idx, err := rand.Int(rand.Reader, big.NewInt(int64(len(allChars))))
		if err != nil {
			return "", fmt.Errorf("failed to generate random character: %w", err)
		}
		password[i] = allChars[idx.Int64()]
	}

	// Shuffle the password
	for i := len(password) - 1; i > 0; i-- {
		j, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if err != nil {
			return "", fmt.Errorf("failed to shuffle password: %w", err)
		}
		password[i], password[j.Int64()] = password[j.Int64()], password[i]
	}

	return string(password), nil
}

// GenerateWorkspaceToken generates a strong random token for a workspace's IDE
// connection. Unlike GenerateSecurePassword it stays within [0-9a-zA-Z]: this same
// value is passed to openvscode-server's --connection-token, which rejects any
// character outside 0-9, a-z, A-Z or '-' and exits, and it is also the secret the
// student signs in with. 24 alphanumerics keep well over 128 bits of entropy.
func GenerateWorkspaceToken() (string, error) {
	const (
		alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
		length   = 24
	)

	token := make([]byte, length)
	for i := range token {
		idx, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return "", fmt.Errorf("failed to generate workspace token: %w", err)
		}
		token[i] = alphabet[idx.Int64()]
	}

	return string(token), nil
}
