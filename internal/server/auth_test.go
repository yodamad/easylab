package server

import (
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

	// Check session was created
	if _, exists := ah.sessions[token]; !exists {
		t.Error("createSession() did not add session to map")
	}

	// Check session expiry is in the future
	session := ah.sessions[token]
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

	// Create an expired session
	token := "expired-token"
	ah.sessions[token] = &Session{
		Token:     token,
		ExpiresAt: time.Now().Add(-1 * time.Hour), // Already expired
	}

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

	// Session should be gone
	if _, exists := ah.sessions[token]; exists {
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

