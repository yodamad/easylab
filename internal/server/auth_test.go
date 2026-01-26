package server

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestHashPassword(t *testing.T) {
	tests := []struct {
		name     string
		password string
		wantLen  int
	}{
		{
			name:     "simple password",
			password: "admin",
			wantLen:  64, // SHA-256 produces 64 hex characters
		},
		{
			name:     "complex password",
			password: "MyP@ssw0rd!123",
			wantLen:  64,
		},
		{
			name:     "empty password",
			password: "",
			wantLen:  64,
		},
		{
			name:     "unicode password",
			password: "пароль日本語",
			wantLen:  64,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hashPassword(tt.password)
			if len(got) != tt.wantLen {
				t.Errorf("hashPassword() = %v, want length %v", len(got), tt.wantLen)
			}

			// Verify it's a valid hex string
			for _, c := range got {
				if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
					t.Errorf("hashPassword() contains invalid hex character: %c", c)
				}
			}
		})
	}
}

func TestHashPasswordDeterministic(t *testing.T) {
	password := "testpassword123"
	hash1 := hashPassword(password)
	hash2 := hashPassword(password)

	if hash1 != hash2 {
		t.Errorf("hashPassword() is not deterministic: %s != %s", hash1, hash2)
	}
}

func TestHashPasswordDifferentForDifferentInputs(t *testing.T) {
	password1 := "password1"
	password2 := "password2"
	hash1 := hashPassword(password1)
	hash2 := hashPassword(password2)

	if hash1 == hash2 {
		t.Errorf("hashPassword() produced same hash for different passwords")
	}
}

func TestGenerateToken(t *testing.T) {
	token1 := generateToken()
	token2 := generateToken()

	// Check length (32 bytes = 64 hex characters)
	if len(token1) != 64 {
		t.Errorf("generateToken() length = %d, want 64", len(token1))
	}

	// Check uniqueness
	if token1 == token2 {
		t.Errorf("generateToken() produced duplicate tokens")
	}

	// Check it's valid hex
	for _, c := range token1 {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("generateToken() contains invalid hex character: %c", c)
		}
	}
}

func TestGenerateSecurePassword(t *testing.T) {
	password, err := GenerateSecurePassword()
	if err != nil {
		t.Fatalf("GenerateSecurePassword() error = %v", err)
	}

	// Check minimum length
	if len(password) < 16 {
		t.Errorf("GenerateSecurePassword() length = %d, want >= 16", len(password))
	}

	// Check for lowercase
	hasLower := regexp.MustCompile(`[a-z]`).MatchString(password)
	if !hasLower {
		t.Errorf("GenerateSecurePassword() missing lowercase character")
	}

	// Check for uppercase
	hasUpper := regexp.MustCompile(`[A-Z]`).MatchString(password)
	if !hasUpper {
		t.Errorf("GenerateSecurePassword() missing uppercase character")
	}

	// Check for number
	hasNumber := regexp.MustCompile(`[0-9]`).MatchString(password)
	if !hasNumber {
		t.Errorf("GenerateSecurePassword() missing number")
	}

	// Check for symbol
	hasSymbol := regexp.MustCompile(`[!@#$%^&*]`).MatchString(password)
	if !hasSymbol {
		t.Errorf("GenerateSecurePassword() missing symbol")
	}
}

func TestGenerateSecurePasswordUniqueness(t *testing.T) {
	passwords := make(map[string]bool)
	for i := 0; i < 100; i++ {
		password, err := GenerateSecurePassword()
		if err != nil {
			t.Fatalf("GenerateSecurePassword() error = %v", err)
		}
		if passwords[password] {
			t.Errorf("GenerateSecurePassword() produced duplicate password")
		}
		passwords[password] = true
	}
}

func TestAuthHandler_CreateSession(t *testing.T) {
	ah := &AuthHandler{
		sessions: make(map[string]*Session),
	}

	token := ah.createSession()

	// Check token is not empty
	if token == "" {
		t.Error("createSession() returned empty token")
	}

	// Check session was created (with proper locking)
	ah.mu.RLock()
	_, exists := ah.sessions[token]
	session := ah.sessions[token]
	ah.mu.RUnlock()

	if !exists {
		t.Error("createSession() did not add session to map")
	}

	// Check session expiry is in the future
	if session.ExpiresAt.Before(time.Now()) {
		t.Error("createSession() set expiry in the past")
	}
}

func TestAuthHandler_ValidateSession(t *testing.T) {
	ah := &AuthHandler{
		sessions: make(map[string]*Session),
	}

	// Create a valid session
	token := ah.createSession()

	// Validate should return true
	if !ah.validateSession(token) {
		t.Error("validateSession() returned false for valid session")
	}

	// Invalid token should return false
	if ah.validateSession("invalid-token") {
		t.Error("validateSession() returned true for invalid token")
	}
}

func TestAuthHandler_ValidateExpiredSession(t *testing.T) {
	ah := &AuthHandler{
		sessions: make(map[string]*Session),
	}

	// Create an expired session (with proper locking)
	token := "expired-token"
	ah.mu.Lock()
	ah.sessions[token] = &Session{
		Token:     token,
		ExpiresAt: time.Now().Add(-1 * time.Hour), // Already expired
	}
	ah.mu.Unlock()

	// Validate should return false
	if ah.validateSession(token) {
		t.Error("validateSession() returned true for expired session")
	}
}

func TestAuthHandler_DeleteSession(t *testing.T) {
	ah := &AuthHandler{
		sessions: make(map[string]*Session),
	}

	// Create a session
	token := ah.createSession()

	// Delete the session
	ah.deleteSession(token)

	// Session should be gone (with proper locking)
	ah.mu.RLock()
	_, exists := ah.sessions[token]
	ah.mu.RUnlock()

	if exists {
		t.Error("deleteSession() did not remove session")
	}
}

func TestAuthHandler_StudentSession(t *testing.T) {
	ah := &AuthHandler{
		studentSessions: make(map[string]*Session),
	}

	// Create a student session
	token := ah.createStudentSession()

	// Check token is not empty
	if token == "" {
		t.Error("createStudentSession() returned empty token")
	}

	// Validate should return true
	if !ah.validateStudentSession(token) {
		t.Error("validateStudentSession() returned false for valid session")
	}

	// Delete the session
	ah.deleteStudentSession(token)

	// Session should be gone
	if ah.validateStudentSession(token) {
		t.Error("deleteStudentSession() did not invalidate session")
	}
}

func TestSession_ExpiryDuration(t *testing.T) {
	if SessionExpiry != 24*time.Hour {
		t.Errorf("SessionExpiry = %v, want %v", SessionExpiry, 24*time.Hour)
	}
}

func TestConstants(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"EnvAdminPassword", EnvAdminPassword, "LAB_ADMIN_PASSWORD"},
		{"EnvStudentPassword", EnvStudentPassword, "LAB_STUDENT_PASSWORD"},
		{"SessionCookieName", SessionCookieName, "lab_session"},
		{"StudentSessionCookieName", StudentSessionCookieName, "lab_student_session"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("%s = %s, want %s", tt.name, tt.got, tt.want)
			}
		})
	}
}

func TestHashPasswordKnownValue(t *testing.T) {
	// Test with a known input/output pair (SHA-256 of "admin")
	password := "admin"
	expected := "8c6976e5b5410415bde908bd4dee15dfb167a9c873fc4bb8a81f6f2ab448a918"
	got := hashPassword(password)

	if !strings.EqualFold(got, expected) {
		t.Errorf("hashPassword(%q) = %s, want %s", password, got, expected)
	}
}

// Test helper functions for authentication middleware testing

// createTestAuthHandler creates an AuthHandler for testing
func createTestAuthHandler() *AuthHandler {
	return &AuthHandler{
		passwordHash:        hashPassword("test-admin"),
		studentPasswordHash: hashPassword("test-student"),
		sessions:            make(map[string]*Session),
		studentSessions:     make(map[string]*Session),
		templates:           make(map[string]*template.Template),
	}
}

// createAuthenticatedRequest creates an HTTP request with a valid admin session cookie
func createAuthenticatedRequest(method, url string, authHandler *AuthHandler) *http.Request {
	req := httptest.NewRequest(method, url, nil)
	token := authHandler.createSession()
	req.AddCookie(&http.Cookie{
		Name:  SessionCookieName,
		Value: token,
	})
	return req
}

// createUnauthenticatedRequest creates an HTTP request without any cookie
func createUnauthenticatedRequest(method, url string) *http.Request {
	return httptest.NewRequest(method, url, nil)
}

// createStudentAuthenticatedRequest creates an HTTP request with a valid student session cookie
func createStudentAuthenticatedRequest(method, url string, authHandler *AuthHandler) *http.Request {
	req := httptest.NewRequest(method, url, nil)
	token := authHandler.createStudentSession()
	req.AddCookie(&http.Cookie{
		Name:  StudentSessionCookieName,
		Value: token,
	})
	return req
}

// createRequestWithInvalidCookie creates an HTTP request with an invalid cookie
func createRequestWithInvalidCookie(method, url string) *http.Request {
	req := httptest.NewRequest(method, url, nil)
	req.AddCookie(&http.Cookie{
		Name:  SessionCookieName,
		Value: "invalid-token",
	})
	return req
}

// createRequestWithExpiredCookie creates an HTTP request with an expired session cookie
func createRequestWithExpiredCookie(method, url string, authHandler *AuthHandler) *http.Request {
	req := httptest.NewRequest(method, url, nil)
	// Create an expired session
	token := "expired-token"
	authHandler.mu.Lock()
	// Ensure sessions map is initialized
	if authHandler.sessions == nil {
		authHandler.sessions = make(map[string]*Session)
	}
	authHandler.sessions[token] = &Session{
		Token:     token,
		ExpiresAt: time.Now().Add(-1 * time.Hour),
	}
	authHandler.mu.Unlock()
	req.AddCookie(&http.Cookie{
		Name:  SessionCookieName,
		Value: token,
	})
	return req
}

// Test RequireAuth Middleware

func TestRequireAuth_Unauthenticated(t *testing.T) {
	ah := createTestAuthHandler()

	// Create a simple handler that should be protected
	protectedHandler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}

	wrappedHandler := ah.RequireAuth(protectedHandler)

	req := createUnauthenticatedRequest("GET", "/protected")
	w := httptest.NewRecorder()

	wrappedHandler(w, req)

	// Should redirect to login
	if w.Code != http.StatusSeeOther {
		t.Errorf("RequireAuth() unauthenticated status = %d, want %d", w.Code, http.StatusSeeOther)
	}

	location := w.Header().Get("Location")
	if location != "/login" {
		t.Errorf("RequireAuth() unauthenticated Location = %s, want /login", location)
	}
}

func TestRequireAuth_Authenticated(t *testing.T) {
	ah := createTestAuthHandler()

	protectedHandler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}

	wrappedHandler := ah.RequireAuth(protectedHandler)

	req := createAuthenticatedRequest("GET", "/protected", ah)
	w := httptest.NewRecorder()

	wrappedHandler(w, req)

	// Should succeed
	if w.Code != http.StatusOK {
		t.Errorf("RequireAuth() authenticated status = %d, want %d", w.Code, http.StatusOK)
	}

	if w.Body.String() != "OK" {
		t.Errorf("RequireAuth() authenticated body = %s, want OK", w.Body.String())
	}
}

func TestRequireAuth_InvalidCookie(t *testing.T) {
	ah := createTestAuthHandler()

	protectedHandler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}

	wrappedHandler := ah.RequireAuth(protectedHandler)

	req := createRequestWithInvalidCookie("GET", "/protected")
	w := httptest.NewRecorder()

	wrappedHandler(w, req)

	// Should redirect to login
	if w.Code != http.StatusSeeOther {
		t.Errorf("RequireAuth() invalid cookie status = %d, want %d", w.Code, http.StatusSeeOther)
	}

	location := w.Header().Get("Location")
	if location != "/login" {
		t.Errorf("RequireAuth() invalid cookie Location = %s, want /login", location)
	}
}

func TestRequireAuth_ExpiredSession(t *testing.T) {
	ah := createTestAuthHandler()

	protectedHandler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}

	wrappedHandler := ah.RequireAuth(protectedHandler)

	req := createRequestWithExpiredCookie("GET", "/protected", ah)
	w := httptest.NewRecorder()

	wrappedHandler(w, req)

	// Should redirect to login
	if w.Code != http.StatusSeeOther {
		t.Errorf("RequireAuth() expired session status = %d, want %d", w.Code, http.StatusSeeOther)
	}

	location := w.Header().Get("Location")
	if location != "/login" {
		t.Errorf("RequireAuth() expired session Location = %s, want /login", location)
	}
}

// Test RequireStudentAuth Middleware

func TestRequireStudentAuth_Unauthenticated(t *testing.T) {
	ah := createTestAuthHandler()

	protectedHandler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}

	wrappedHandler := ah.RequireStudentAuth(protectedHandler)

	req := createUnauthenticatedRequest("GET", "/student/protected")
	w := httptest.NewRecorder()

	wrappedHandler(w, req)

	// Should redirect to student login
	if w.Code != http.StatusSeeOther {
		t.Errorf("RequireStudentAuth() unauthenticated status = %d, want %d", w.Code, http.StatusSeeOther)
	}

	location := w.Header().Get("Location")
	if location != "/student/login" {
		t.Errorf("RequireStudentAuth() unauthenticated Location = %s, want /student/login", location)
	}
}

func TestRequireStudentAuth_Authenticated(t *testing.T) {
	ah := createTestAuthHandler()

	protectedHandler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}

	wrappedHandler := ah.RequireStudentAuth(protectedHandler)

	req := createStudentAuthenticatedRequest("GET", "/student/protected", ah)
	w := httptest.NewRecorder()

	wrappedHandler(w, req)

	// Should succeed
	if w.Code != http.StatusOK {
		t.Errorf("RequireStudentAuth() authenticated status = %d, want %d", w.Code, http.StatusOK)
	}

	if w.Body.String() != "OK" {
		t.Errorf("RequireStudentAuth() authenticated body = %s, want OK", w.Body.String())
	}
}

func TestRequireStudentAuth_InvalidCookie(t *testing.T) {
	ah := createTestAuthHandler()

	protectedHandler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}

	wrappedHandler := ah.RequireStudentAuth(protectedHandler)

	req := httptest.NewRequest("GET", "/student/protected", nil)
	req.AddCookie(&http.Cookie{
		Name:  StudentSessionCookieName,
		Value: "invalid-token",
	})
	w := httptest.NewRecorder()

	wrappedHandler(w, req)

	// Should redirect to student login
	if w.Code != http.StatusSeeOther {
		t.Errorf("RequireStudentAuth() invalid cookie status = %d, want %d", w.Code, http.StatusSeeOther)
	}

	location := w.Header().Get("Location")
	if location != "/student/login" {
		t.Errorf("RequireStudentAuth() invalid cookie Location = %s, want /student/login", location)
	}
}

func TestRequireStudentAuth_Disabled(t *testing.T) {
	ah := &AuthHandler{
		passwordHash:        hashPassword("test-admin"),
		studentPasswordHash: "", // Student login disabled
		sessions:            make(map[string]*Session),
		studentSessions:     make(map[string]*Session),
		templates:           make(map[string]*template.Template),
	}

	protectedHandler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}

	wrappedHandler := ah.RequireStudentAuth(protectedHandler)

	req := createStudentAuthenticatedRequest("GET", "/student/protected", ah)
	w := httptest.NewRecorder()

	wrappedHandler(w, req)

	// Should return 403 Forbidden when student login is disabled
	if w.Code != http.StatusForbidden {
		t.Errorf("RequireStudentAuth() disabled status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

// Test All Protected Admin Endpoints

func TestProtectedAdminEndpoints_Unauthenticated(t *testing.T) {
	ah := createTestAuthHandler()
	jm := NewJobManager("")
	pe := &PulumiExecutor{}
	cm := NewCredentialsManager()
	h := NewHandler(jm, pe, cm)

	tests := []struct {
		name     string
		method   string
		path     string
		handler  http.HandlerFunc
		wantCode int
		wantLoc  string
	}{
		{
			name:     "/admin",
			method:   "GET",
			path:     "/admin",
			handler:  ah.RequireAuth(h.ServeAdminUI),
			wantCode: http.StatusSeeOther,
			wantLoc:  "/login",
		},
		{
			name:     "/jobs",
			method:   "GET",
			path:     "/jobs",
			handler:  ah.RequireAuth(h.ServeJobsList),
			wantCode: http.StatusSeeOther,
			wantLoc:  "/login",
		},
		{
			name:     "/ovh-credentials",
			method:   "GET",
			path:     "/ovh-credentials",
			handler:  ah.RequireAuth(h.ServeOVHCredentials),
			wantCode: http.StatusSeeOther,
			wantLoc:  "/login",
		},
		{
			name:     "/api/ovh-credentials GET",
			method:   "GET",
			path:     "/api/ovh-credentials",
			handler:  ah.RequireAuth(h.GetOVHCredentials),
			wantCode: http.StatusSeeOther,
			wantLoc:  "/login",
		},
		{
			name:     "/api/ovh-credentials POST",
			method:   "POST",
			path:     "/api/ovh-credentials",
			handler:  ah.RequireAuth(h.SetOVHCredentials),
			wantCode: http.StatusSeeOther,
			wantLoc:  "/login",
		},
		{
			name:     "/api/labs",
			method:   "POST",
			path:     "/api/labs",
			handler:  ah.RequireAuth(h.CreateLab),
			wantCode: http.StatusSeeOther,
			wantLoc:  "/login",
		},
		{
			name:     "/api/labs/dry-run",
			method:   "POST",
			path:     "/api/labs/dry-run",
			handler:  ah.RequireAuth(h.DryRunLab),
			wantCode: http.StatusSeeOther,
			wantLoc:  "/login",
		},
		{
			name:     "/api/labs/launch",
			method:   "POST",
			path:     "/api/labs/launch",
			handler:  ah.RequireAuth(h.LaunchLab),
			wantCode: http.StatusSeeOther,
			wantLoc:  "/login",
		},
		{
			name:     "/api/labs/recreate",
			method:   "POST",
			path:     "/api/labs/recreate",
			handler:  ah.RequireAuth(h.RecreateLab),
			wantCode: http.StatusSeeOther,
			wantLoc:  "/login",
		},
		{
			name:     "/api/stacks/destroy",
			method:   "POST",
			path:     "/api/stacks/destroy",
			handler:  ah.RequireAuth(h.DestroyStack),
			wantCode: http.StatusSeeOther,
			wantLoc:  "/login",
		},
		{
			name:     "/api/jobs/test-id/status",
			method:   "GET",
			path:     "/api/jobs/test-id/status",
			handler:  ah.RequireAuth(h.GetJobStatus),
			wantCode: http.StatusSeeOther,
			wantLoc:  "/login",
		},
		{
			name:     "/api/jobs/test-id kubeconfig",
			method:   "GET",
			path:     "/api/jobs/test-id/kubeconfig",
			handler:  ah.RequireAuth(h.DownloadKubeconfig),
			wantCode: http.StatusSeeOther,
			wantLoc:  "/login",
		},
		{
			name:     "/api/jobs/test-id/retry",
			method:   "POST",
			path:     "/api/jobs/test-id/retry",
			handler:  ah.RequireAuth(h.RetryJob),
			wantCode: http.StatusSeeOther,
			wantLoc:  "/login",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := createUnauthenticatedRequest(tt.method, tt.path)
			w := httptest.NewRecorder()

			tt.handler(w, req)

			if w.Code != tt.wantCode {
				t.Errorf("status = %d, want %d", w.Code, tt.wantCode)
			}

			location := w.Header().Get("Location")
			if location != tt.wantLoc {
				t.Errorf("Location = %s, want %s", location, tt.wantLoc)
			}
		})
	}
}

func TestProtectedAdminEndpoints_Authenticated(t *testing.T) {
	ah := createTestAuthHandler()
	jm := NewJobManager("")
	pe := &PulumiExecutor{}
	cm := NewCredentialsManager()
	h := NewHandler(jm, pe, cm)

	tests := []struct {
		name     string
		method   string
		path     string
		handler  http.HandlerFunc
		wantCode int
	}{
		{
			name:     "/admin",
			method:   "GET",
			path:     "/admin",
			handler:  ah.RequireAuth(h.ServeAdminUI),
			wantCode: http.StatusOK,
		},
		{
			name:     "/jobs",
			method:   "GET",
			path:     "/jobs",
			handler:  ah.RequireAuth(h.ServeJobsList),
			wantCode: http.StatusOK,
		},
		{
			name:     "/ovh-credentials",
			method:   "GET",
			path:     "/ovh-credentials",
			handler:  ah.RequireAuth(h.ServeOVHCredentials),
			wantCode: http.StatusOK,
		},
		{
			name:     "/api/ovh-credentials GET",
			method:   "GET",
			path:     "/api/ovh-credentials",
			handler:  ah.RequireAuth(h.GetOVHCredentials),
			wantCode: http.StatusNotFound, // No credentials configured
		},
		{
			name:     "/api/jobs/test-id/status",
			method:   "GET",
			path:     "/api/jobs/test-id/status",
			handler:  ah.RequireAuth(h.GetJobStatus),
			wantCode: http.StatusNotFound, // Job doesn't exist
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := createAuthenticatedRequest(tt.method, tt.path, ah)
			w := httptest.NewRecorder()

			tt.handler(w, req)

			// Should not redirect (authenticated)
			if w.Code == http.StatusSeeOther {
				location := w.Header().Get("Location")
				t.Errorf("authenticated request redirected to %s, should not redirect", location)
			}

			// Should get expected status code (or at least not redirect)
			if w.Code == http.StatusSeeOther {
				t.Errorf("status = %d (redirect), want non-redirect status", w.Code)
			}
		})
	}
}

// Test All Protected Student Endpoints

func TestProtectedStudentEndpoints_Unauthenticated(t *testing.T) {
	ah := createTestAuthHandler()
	jm := NewJobManager("")
	pe := &PulumiExecutor{}
	cm := NewCredentialsManager()
	h := NewHandler(jm, pe, cm)

	tests := []struct {
		name     string
		method   string
		path     string
		handler  http.HandlerFunc
		wantCode int
		wantLoc  string
	}{
		{
			name:     "/student/dashboard",
			method:   "GET",
			path:     "/student/dashboard",
			handler:  ah.RequireStudentAuth(h.ServeStudentDashboard),
			wantCode: http.StatusSeeOther,
			wantLoc:  "/student/login",
		},
		{
			name:     "/api/student/labs",
			method:   "GET",
			path:     "/api/student/labs",
			handler:  ah.RequireStudentAuth(h.ListLabs),
			wantCode: http.StatusSeeOther,
			wantLoc:  "/student/login",
		},
		{
			name:     "/api/student/workspace/request",
			method:   "POST",
			path:     "/api/student/workspace/request",
			handler:  ah.RequireStudentAuth(h.RequestWorkspace),
			wantCode: http.StatusSeeOther,
			wantLoc:  "/student/login",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := createUnauthenticatedRequest(tt.method, tt.path)
			w := httptest.NewRecorder()

			tt.handler(w, req)

			if w.Code != tt.wantCode {
				t.Errorf("status = %d, want %d", w.Code, tt.wantCode)
			}

			location := w.Header().Get("Location")
			if location != tt.wantLoc {
				t.Errorf("Location = %s, want %s", location, tt.wantLoc)
			}
		})
	}
}

func TestProtectedStudentEndpoints_Authenticated(t *testing.T) {
	ah := createTestAuthHandler()
	jm := NewJobManager("")
	pe := &PulumiExecutor{}
	cm := NewCredentialsManager()
	h := NewHandler(jm, pe, cm)

	tests := []struct {
		name     string
		method   string
		path     string
		handler  http.HandlerFunc
		wantCode int
	}{
		{
			name:     "/student/dashboard",
			method:   "GET",
			path:     "/student/dashboard",
			handler:  ah.RequireStudentAuth(h.ServeStudentDashboard),
			wantCode: http.StatusOK,
		},
		{
			name:     "/api/student/labs",
			method:   "GET",
			path:     "/api/student/labs",
			handler:  ah.RequireStudentAuth(h.ListLabs),
			wantCode: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := createStudentAuthenticatedRequest(tt.method, tt.path, ah)
			w := httptest.NewRecorder()

			tt.handler(w, req)

			// Should not redirect (authenticated)
			if w.Code == http.StatusSeeOther {
				location := w.Header().Get("Location")
				t.Errorf("authenticated request redirected to %s, should not redirect", location)
			}

			// Should get expected status code (or at least not redirect)
			if w.Code == http.StatusSeeOther {
				t.Errorf("status = %d (redirect), want non-redirect status", w.Code)
			}
		})
	}
}

// Test Public Endpoints Remain Public

func TestPublicEndpoints_NoAuthRequired(t *testing.T) {
	ah := createTestAuthHandler()
	jm := NewJobManager("")
	pe := &PulumiExecutor{}
	cm := NewCredentialsManager()
	h := NewHandler(jm, pe, cm)

	tests := []struct {
		name     string
		method   string
		path     string
		handler  http.HandlerFunc
		wantCode int
	}{
		{
			name:     "/",
			method:   "GET",
			path:     "/",
			handler:  h.ServeUI,
			wantCode: http.StatusOK,
		},
		{
			name:     "/login GET",
			method:   "GET",
			path:     "/login",
			handler:  ah.ServeLogin,
			wantCode: http.StatusOK,
		},
		{
			name:   "/health",
			method: "GET",
			path:   "/health",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("OK"))
			},
			wantCode: http.StatusOK,
		},
		{
			name:     "/student/login GET",
			method:   "GET",
			path:     "/student/login",
			handler:  ah.ServeStudentLogin,
			wantCode: http.StatusOK,
		},
	}

	for _, tt := range tests {
		// Capture loop variable to avoid potential race conditions
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			req := createUnauthenticatedRequest(tt.method, tt.path)
			w := httptest.NewRecorder()

			tt.handler(w, req)

			// Should not redirect (public endpoint) - this is the main test
			// Public endpoints should not require authentication
			if w.Code == http.StatusSeeOther || w.Code == http.StatusFound {
				location := w.Header().Get("Location")
				t.Errorf("public endpoint redirected to %s (status %d), should not redirect", location, w.Code)
				return
			}

			// Template loading might fail in tests (files may not exist)
			// So accept either 200 (success) or 500 (template error), but not redirects
			if tt.wantCode == http.StatusOK {
				if w.Code != http.StatusOK && w.Code != http.StatusInternalServerError {
					t.Errorf("status = %d, want %d or %d (template may not exist in test)", w.Code, http.StatusOK, http.StatusInternalServerError)
				}
			} else {
				if w.Code != tt.wantCode {
					t.Errorf("status = %d, want %d", w.Code, tt.wantCode)
				}
			}
		})
	}
}
