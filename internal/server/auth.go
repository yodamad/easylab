package server

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html/template"
	"log"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	// Environment variable for the admin password
	EnvAdminPassword = "LAB_ADMIN_PASSWORD"
	// Environment variable for the student password
	EnvStudentPassword = "LAB_STUDENT_PASSWORD"
	// Cookie name for session
	SessionCookieName = "lab_session"
	// Cookie name for student session
	StudentSessionCookieName = "lab_student_session"
	// Session expiry duration
	SessionExpiry = 24 * time.Hour
)

// Session represents a user session
type Session struct {
	Token     string
	ExpiresAt time.Time
}

// AuthHandler handles authentication
type AuthHandler struct {
	passwordHash        string
	studentPasswordHash string
	sessions            map[string]*Session
	studentSessions     map[string]*Session
	mu                  sync.RWMutex
}

// hashPassword creates a SHA-256 hash of the password
func hashPassword(password string) string {
	hash := sha256.Sum256([]byte(password))
	return hex.EncodeToString(hash[:])
}

// NewAuthHandler creates a new auth handler
func NewAuthHandler() *AuthHandler {
	password := os.Getenv(EnvAdminPassword)
	if password == "" {
		log.Printf("WARNING: %s environment variable not set. Using default password 'admin'", EnvAdminPassword)
		password = "admin"
	}

	// Store the hash of the password
	passwordHash := hashPassword(password)
	log.Printf("Admin password hash initialized")

	// Initialize student password
	studentPassword := os.Getenv(EnvStudentPassword)
	var studentPasswordHash string
	if studentPassword == "" {
		log.Printf("WARNING: %s environment variable not set. Student login will be disabled.", EnvStudentPassword)
		studentPasswordHash = ""
	} else {
		studentPasswordHash = hashPassword(studentPassword)
		log.Printf("Student password hash initialized")
	}

	return &AuthHandler{
		passwordHash:        passwordHash,
		studentPasswordHash: studentPasswordHash,
		sessions:            make(map[string]*Session),
		studentSessions:     make(map[string]*Session),
	}
}

// generateToken creates a secure random token
func generateToken() string {
	bytes := make([]byte, 32)
	rand.Read(bytes)
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

// ServeLogin serves the login page
func (ah *AuthHandler) ServeLogin(w http.ResponseWriter, r *http.Request) {
	// Check if already logged in
	if cookie, err := r.Cookie(SessionCookieName); err == nil {
		if ah.validateSession(cookie.Value) {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
	}

	tmplPath := filepath.Join("web", "login.html")
	tmpl, err := template.ParseFiles(tmplPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load template: %v", err), http.StatusInternalServerError)
		return
	}

	// Prevent caching
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	data := map[string]interface{}{
		"Error": r.URL.Query().Get("error"),
	}

	if err := tmpl.Execute(w, data); err != nil {
		http.Error(w, fmt.Sprintf("Failed to execute template: %v", err), http.StatusInternalServerError)
	}
}

// HandleLogin processes login form submission
func (ah *AuthHandler) HandleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/login?error=Invalid+request", http.StatusSeeOther)
		return
	}

	// The client sends the password already hashed
	receivedHash := r.FormValue("password_hash")

	// Compare hashes
	if receivedHash != ah.passwordHash {
		log.Printf("Failed login attempt")
		http.Redirect(w, r, "/login?error=Invalid+password", http.StatusSeeOther)
		return
	}

	// Create session
	token := ah.createSession()

	// Set cookie
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   false, // Set to true in production with HTTPS
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(SessionExpiry.Seconds()),
	})

	log.Printf("Successful login")
	http.Redirect(w, r, "/", http.StatusSeeOther)
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

	http.Redirect(w, r, "/login", http.StatusSeeOther)
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
func (ah *AuthHandler) createStudentSession() string {
	ah.mu.Lock()
	defer ah.mu.Unlock()

	token := generateToken()
	ah.studentSessions[token] = &Session{
		Token:     token,
		ExpiresAt: time.Now().Add(SessionExpiry),
	}

	return token
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

	tmplPath := filepath.Join("web", "student-login.html")
	tmpl, err := template.ParseFiles(tmplPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load template: %v", err), http.StatusInternalServerError)
		return
	}

	// Prevent caching
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	data := map[string]interface{}{
		"Error": r.URL.Query().Get("error"),
	}

	if err := tmpl.Execute(w, data); err != nil {
		http.Error(w, fmt.Sprintf("Failed to execute template: %v", err), http.StatusInternalServerError)
	}
}

// HandleStudentLogin processes student login form submission
func (ah *AuthHandler) HandleStudentLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/student/login", http.StatusSeeOther)
		return
	}

	if ah.studentPasswordHash == "" {
		http.Redirect(w, r, "/student/login?error=Student+login+disabled", http.StatusSeeOther)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/student/login?error=Invalid+request", http.StatusSeeOther)
		return
	}

	// The client sends the password already hashed
	receivedHash := r.FormValue("password_hash")

	// Compare hashes
	if receivedHash != ah.studentPasswordHash {
		log.Printf("Failed student login attempt")
		http.Redirect(w, r, "/student/login?error=Invalid+password", http.StatusSeeOther)
		return
	}

	// Create session
	token := ah.createStudentSession()

	// Set cookie
	http.SetCookie(w, &http.Cookie{
		Name:     StudentSessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   false, // Set to true in production with HTTPS
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

	http.Redirect(w, r, "/student/login", http.StatusSeeOther)
}

// RequireStudentAuth is middleware that requires student authentication
func (ah *AuthHandler) RequireStudentAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if ah.studentPasswordHash == "" {
			http.Error(w, "Student login is disabled", http.StatusForbidden)
			return
		}
		cookie, err := r.Cookie(StudentSessionCookieName)
		if err != nil || !ah.validateStudentSession(cookie.Value) {
			http.Redirect(w, r, "/student/login", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
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
