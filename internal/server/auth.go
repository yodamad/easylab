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
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
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
	templates           map[string]*template.Template
	templatesMu         sync.RWMutex
	mu                  sync.RWMutex
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

	return &AuthHandler{
		passwordHash:        passwordHash,
		studentPasswordHash: studentPasswordHash,
		sessions:            make(map[string]*Session),
		studentSessions:     make(map[string]*Session),
		templates:           make(map[string]*template.Template),
	}, nil
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
			http.Redirect(w, r, "/admin", http.StatusSeeOther)
			return
		}
	}

	// Use cached template (lazy loaded)
	tmpl, err := ah.getTemplate("login.html")
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load login template: %v", err), http.StatusInternalServerError)
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

	if err := tmpl.ExecuteTemplate(w, "base", data); err != nil {
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

	// Get password hash from form (client-side SHA-256 hashed)
	passwordHash := r.FormValue("password_hash")
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
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
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

	// Use cached template (lazy loaded)
	tmpl, err := ah.getTemplate("student-login.html")
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load student login template: %v", err), http.StatusInternalServerError)
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

	if err := tmpl.ExecuteTemplate(w, "base", data); err != nil {
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

	// Get password hash from form (client-side SHA-256 hashed)
	passwordHash := r.FormValue("password_hash")
	if passwordHash == "" {
		log.Printf("Failed student login attempt: empty password hash")
		http.Redirect(w, r, "/student/login?error=Invalid+password", http.StatusSeeOther)
		return
	}

	// Compare received SHA-256 hash with stored bcrypt(SHA-256(password)) hash
	if !comparePassword(ah.studentPasswordHash, passwordHash) {
		log.Printf("Failed student login attempt")
		http.Redirect(w, r, "/student/login?error=Invalid+password", http.StatusSeeOther)
		return
	}

	// Create session
	token := ah.createStudentSession()

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
