package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// PortalSettings are the things student portals need from this instance that an
// admin can set from the UI rather than the environment. An empty field means
// "use the default": the matching environment variable, then what the build
// itself provides (see portalImage).
type PortalSettings struct {
	// PublicURL is this instance's address as students reach it. Portals send
	// students here for Microsoft / GitHub / GitLab sign-in.
	PublicURL string `json:"public_url,omitempty"`
	// Image is the EasyLab image the portals run.
	Image string `json:"image,omitempty"`

	// StudentPassword is the password for *this instance's own* student area, and
	// StudentPasswordHash the form the login handlers compare against. Empty
	// means fall back to LAB_STUDENT_PASSWORD.
	//
	// It applies to this portal alone: a lab with a portal of its own carries its
	// own password (LabConfig.StudentPortalPassword). Both are kept for the same
	// reasons as there — the plaintext so the admin page can show it again, the
	// hash because bcrypt is salted and must not be recomputed per read.
	StudentPassword     string `json:"student_password,omitempty"`
	StudentPasswordHash string `json:"student_password_hash,omitempty"`
}

// PortalSettingsStore persists PortalSettings to disk. The student password and
// its hash are encrypted at rest, as the sign-in provider stores do with their
// client secrets; the other two values are not secrets.
type PortalSettingsStore struct {
	settings PortalSettings
	dataDir  string
	mu       sync.RWMutex
}

// NewPortalSettingsStore creates a store and loads persisted settings from disk.
func NewPortalSettingsStore(dataDir string) *PortalSettingsStore {
	s := &PortalSettingsStore{dataDir: dataDir}
	if err := s.load(); err != nil {
		log.Printf("[PORTAL-SETTINGS] Warning: failed to load settings: %v", err)
	}
	return s
}

func (s *PortalSettingsStore) path() string {
	return filepath.Join(s.dataDir, "portal-settings.json")
}

// load reads the settings from disk. Missing file is not an error.
func (s *PortalSettingsStore) load() error {
	if s.dataDir == "" {
		return nil
	}
	data, err := os.ReadFile(s.path())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read portal settings: %w", err)
	}
	var settings PortalSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		return fmt.Errorf("failed to parse portal settings: %w", err)
	}
	password, err := decryptSecret(settings.StudentPassword)
	if err != nil {
		return fmt.Errorf("failed to decrypt portal student password: %w", err)
	}
	settings.StudentPassword = password
	hash, err := decryptSecret(settings.StudentPasswordHash)
	if err != nil {
		return fmt.Errorf("failed to decrypt portal student password hash: %w", err)
	}
	settings.StudentPasswordHash = hash

	s.mu.Lock()
	s.settings = settings
	s.mu.Unlock()
	return nil
}

// Get returns the saved settings.
func (s *PortalSettingsStore) Get() PortalSettings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.settings
}

// Set updates the settings and persists them.
func (s *PortalSettingsStore) Set(settings PortalSettings) error {
	if s.dataDir != "" {
		// Only the on-disk copy is encrypted; what the store serves stays plaintext.
		onDisk := settings
		password, err := encryptSecret(settings.StudentPassword)
		if err != nil {
			return fmt.Errorf("failed to encrypt portal student password: %w", err)
		}
		onDisk.StudentPassword = password
		hash, err := encryptSecret(settings.StudentPasswordHash)
		if err != nil {
			return fmt.Errorf("failed to encrypt portal student password hash: %w", err)
		}
		onDisk.StudentPasswordHash = hash

		data, err := json.MarshalIndent(onDisk, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal portal settings: %w", err)
		}
		if err := os.MkdirAll(s.dataDir, 0755); err != nil {
			return fmt.Errorf("failed to create data dir: %w", err)
		}
		if err := os.WriteFile(s.path(), data, 0600); err != nil {
			return fmt.Errorf("failed to write portal settings: %w", err)
		}
	}
	s.mu.Lock()
	s.settings = settings
	s.mu.Unlock()
	return nil
}

// SetPortalSettingsStore wires the store behind the Student portals admin page.
func (h *Handler) SetPortalSettingsStore(store *PortalSettingsStore) {
	h.portalSettings = store
}

// savedPortalSettings returns what the admin saved, empty when there is no store.
func (h *Handler) savedPortalSettings() PortalSettings {
	if h.portalSettings == nil {
		return PortalSettings{}
	}
	return h.portalSettings.Get()
}

// effectivePublicURL is the address in-lab portals send students to: the one
// saved from the admin page, else the one from the environment.
func (h *Handler) effectivePublicURL() string {
	if saved := h.savedPortalSettings().PublicURL; saved != "" {
		return saved
	}
	return h.publicURL
}

// effectivePortalImage is the image in-lab portals run: the one saved from the
// admin page, else the build's default (see portalImage). "" when there is none.
func (h *Handler) effectivePortalImage() string {
	if saved := h.savedPortalSettings().Image; saved != "" {
		return saved
	}
	return portalImage()
}

// effectiveStudentPassword is the password this instance's own student area
// accepts: the one saved from the admin page, else LAB_STUDENT_PASSWORD. "" means
// password sign-in is off here.
//
// It covers this portal alone. A lab with a portal of its own has its own
// password; see LabConfig.StudentPortalPassword and GetLabPortalPassword.
func (h *Handler) effectiveStudentPassword() string {
	if saved := h.savedPortalSettings().StudentPassword; saved != "" {
		return saved
	}
	return os.Getenv(EnvStudentPassword)
}

// applyStudentPassword pushes the effective password into the auth handler, so a
// password saved on the admin page takes effect without a restart. Called at
// startup — the saved one has to beat the environment the auth handler read —
// and after every save.
func (h *Handler) applyStudentPassword() {
	if h.studentPasswordConfigurer == nil {
		return
	}
	saved := h.savedPortalSettings()
	hash := saved.StudentPasswordHash
	if saved.StudentPassword == "" {
		// Nothing saved: back to whatever the environment says, which may be "".
		var err error
		hash, err = HashStudentPassword(os.Getenv(EnvStudentPassword))
		if err != nil {
			log.Printf("Failed to hash %s: %v", EnvStudentPassword, err)
			return
		}
	}
	h.studentPasswordConfigurer(hash)
}

// ApplyStudentPassword applies a password saved on the admin page at startup.
// Set up the store and the configurer first.
//
// Nothing saved is a no-op rather than a fallback: the auth handler already
// hashed LAB_STUDENT_PASSWORD when it was built, and hashing it again would cost
// a bcrypt and log a change that did not happen (bcrypt is salted, so the second
// hash never equals the first).
func (h *Handler) ApplyStudentPassword() {
	if h.savedPortalSettings().StudentPassword == "" {
		return
	}
	h.applyStudentPassword()
}

// normalizePublicURL validates the public address an admin typed and returns
// it without a trailing slash. It has to be a bare origin: portals append their
// own paths to it, and students' browsers are redirected there.
func normalizePublicURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("not a valid URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("must start with http:// or https://")
	}
	if u.Host == "" || u.User != nil {
		return "", fmt.Errorf("must name a host")
	}
	if strings.Trim(u.Path, "/") != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("must be the address alone, without a path")
	}
	return u.Scheme + "://" + u.Host, nil
}

// portalImageRegex accepts a container image reference: a first component
// (registry host, optionally with a port, or the first path segment), further
// path segments, then an optional tag and/or digest. Deliberately loose about
// which parts are present — the cluster is the judge of whether it can be
// pulled — but strict about the characters, since the value ends up in a pod spec.
var portalImageRegex = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*(:[0-9]{1,5})?(/[a-zA-Z0-9][a-zA-Z0-9._-]*)*(:[a-zA-Z0-9_][a-zA-Z0-9._-]{0,127})?(@sha256:[a-f0-9]{64})?$`)

// normalizePortalImage validates the image reference an admin typed.
func normalizePortalImage(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if len(raw) > 512 || !portalImageRegex.MatchString(raw) {
		return "", fmt.Errorf("not a valid image reference")
	}
	return raw, nil
}

// portalSettingsView is the data rendered by the Student portals admin page.
type portalSettingsView struct {
	// PublicURL and Image are what the admin saved ("" when using the default).
	PublicURL string
	Image     string
	// DefaultPublicURL and DefaultImage are what applies when the field is empty.
	DefaultPublicURL string
	DefaultImage     string
	// EffectivePublicURL and EffectiveImage are what portals actually get.
	EffectivePublicURL string
	EffectiveImage     string
	// DetectedURL is the address this page was reached on: a likely right answer
	// for the public address, offered as a suggestion.
	DetectedURL string
	// PortalLabs counts the labs with a portal, which a save redeploys.
	PortalLabs int
	// StudentPasswordSet and StudentPasswordFromEnv say where this portal's
	// password comes from. The password itself is deliberately not here: it is
	// fetched on demand through GetStudentPortalPassword, which audits the read,
	// so it stays out of the page source.
	StudentPasswordSet     bool
	StudentPasswordFromEnv bool
}

// ServePortalSettings serves the Student portals admin page.
func (h *Handler) ServePortalSettings(w http.ResponseWriter, r *http.Request) {
	if h.portalSettings == nil {
		http.Error(w, "Student portal settings not available", http.StatusServiceUnavailable)
		return
	}
	saved := h.portalSettings.Get()

	scheme := "http"
	if isSecureRequest(r) {
		scheme = "https"
	}
	portalLabs := 0
	for _, job := range h.jobManager.GetAllJobs() {
		if portalTargetFor(job).enabled {
			portalLabs++
		}
	}

	h.serveTemplate(w, "portal-settings.html", portalSettingsView{
		PublicURL:              saved.PublicURL,
		Image:                  saved.Image,
		DefaultPublicURL:       h.publicURL,
		DefaultImage:           portalImage(),
		EffectivePublicURL:     h.effectivePublicURL(),
		EffectiveImage:         h.effectivePortalImage(),
		DetectedURL:            scheme + "://" + r.Host,
		PortalLabs:             portalLabs,
		StudentPasswordSet:     h.effectiveStudentPassword() != "",
		StudentPasswordFromEnv: saved.StudentPassword == "" && os.Getenv(EnvStudentPassword) != "",
	})
}

// SavePortalSettings handles saving the Student portals admin page. Existing
// portals are brought in line in the background: a new image redeploys them, a
// new public address reaches them as a state sync.
func (h *Handler) SavePortalSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.portalSettings == nil {
		http.Error(w, "Student portal settings not available", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	if err := r.ParseForm(); err != nil {
		fmt.Fprint(w, `<div class="error-message"><p>Failed to parse form</p></div>`)
		return
	}

	publicURL, err := normalizePublicURL(r.FormValue("portal_public_url"))
	if err != nil {
		log.Printf("SavePortalSettings: invalid public URL: %v", err)
		fmt.Fprint(w, `<div class="error-message"><p>The public address is not valid. Enter the address alone, like <code>https://easylab.example.com</code> — no path after it.</p></div>`)
		return
	}
	image, err := normalizePortalImage(r.FormValue("portal_image"))
	if err != nil {
		log.Printf("SavePortalSettings: invalid image: %v", err)
		fmt.Fprint(w, `<div class="error-message"><p>The portal image is not valid. Enter an image reference, like <code>docker.io/yodamad/easylab:v1.2.3</code>.</p></div>`)
		return
	}

	// The password of this instance's own student area. An empty field leaves it
	// as it is — this form is mostly saved for the other two settings — so
	// clearing it back to LAB_STUDENT_PASSWORD takes the explicit checkbox.
	saved := h.portalSettings.Get()
	password := saved.StudentPassword
	passwordHash := saved.StudentPasswordHash
	passwordDetail := "unchanged"
	if r.FormValue("portal_student_password_reset") == "on" {
		password, passwordHash, passwordDetail = "", "", "reset to "+EnvStudentPassword
	} else if typed := strings.TrimSpace(r.FormValue("portal_student_password")); typed != "" {
		if len(typed) < minPortalPasswordLength {
			fmt.Fprintf(w, `<div class="error-message"><p>The student password must be at least %d characters.</p></div>`, minPortalPasswordLength)
			return
		}
		hash, hashErr := HashStudentPassword(typed)
		if hashErr != nil {
			log.Printf("SavePortalSettings: failed to hash student password: %v", hashErr)
			fmt.Fprint(w, `<div class="error-message"><p>Failed to save student portal settings. Check the server logs for details.</p></div>`)
			return
		}
		password, passwordHash, passwordDetail = typed, hash, "updated"
	}

	if err := h.portalSettings.Set(PortalSettings{
		PublicURL:           publicURL,
		Image:               image,
		StudentPassword:     password,
		StudentPasswordHash: passwordHash,
	}); err != nil {
		log.Printf("SavePortalSettings: failed to persist: %v", err)
		fmt.Fprint(w, `<div class="error-message"><p>Failed to save student portal settings. Check the server logs for details.</p></div>`)
		return
	}

	// In force immediately: this instance's own student area is served by this
	// process, so there is nothing to reconcile for it.
	h.applyStudentPassword()

	// Existing portals run the old image and know the old address until reconciled.
	go h.reconcilePortals()

	detail := fmt.Sprintf("public URL: %s, image: %s, student password: %s", orDefault(publicURL), orDefault(image), passwordDetail)
	h.recordAudit(adminActor(r), "admin", "portal_settings.update", "", detail)

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", "/admin/student-portals")
		fmt.Fprint(w, `<div class="success-message"><p>Student portal settings saved</p></div>`)
	} else {
		http.Redirect(w, r, "/admin/student-portals", http.StatusSeeOther)
	}
}

func orDefault(value string) string {
	if value == "" {
		return "default"
	}
	return value
}
