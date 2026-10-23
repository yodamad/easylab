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
)

// A portal password is read out to a room, so it must stay free of the
// characters people mistake for one another — while still being a password.
func TestGeneratePortalPassword(t *testing.T) {
	t.Parallel()

	const forbidden = "OoIil01SZsz"
	seen := make(map[string]bool, 200)
	for i := 0; i < 200; i++ {
		password, err := GeneratePortalPassword()
		require.NoError(t, err)

		assert.Len(t, password, 17, "three groups of five plus two dashes")
		assert.Equal(t, strings.ToUpper(password), password, "uppercase only")
		for _, c := range password {
			assert.NotContains(t, forbidden, string(c), "easily misread character in %q", password)
		}
		assert.Equal(t, 2, strings.Count(password, "-"), "grouped for reading aloud: %q", password)
		for _, group := range strings.Split(password, "-") {
			assert.Len(t, group, 5)
		}
		seen[password] = true
	}
	assert.Greater(t, len(seen), 190, "passwords must not repeat")
}

// The browser sends SHA-256(password), so the stored hash has to be bcrypt over
// that and not over the password itself.
func TestHashStudentPassword(t *testing.T) {
	t.Parallel()

	assert.Empty(t, mustHashStudentPassword(t, ""), "no password means no sign-in")

	hash := mustHashStudentPassword(t, "correct horse")
	require.NotEmpty(t, hash)
	assert.True(t, comparePassword(hash, sha256Hex("correct horse")), "the browser's hash verifies")
	assert.False(t, comparePassword(hash, sha256Hex("wrong horse")))
	assert.False(t, comparePassword(hash, "correct horse"), "the raw password must not verify")

	// Salted: the same password hashes differently every time, which is why the
	// hash is stored rather than recomputed per portal sync.
	assert.NotEqual(t, hash, mustHashStudentPassword(t, "correct horse"))
}

// A lab's portal password and its hash are both secrets: students read a
// sanitized job through ListLabs, and a bcrypt hash is worth cracking offline.
//
// Not parallel: InitDataEncryption is package-level state.
func TestSanitizedCopy_PortalPassword(t *testing.T) {
	require.NoError(t, InitDataEncryption(bytes.Repeat([]byte{9}, 32)))
	t.Cleanup(func() { _ = InitDataEncryption(nil) })

	const (
		password = "ABCDE-FGHJK-LMNPQ"
		hash     = "$2a$10$fakebcrypthashvalue"
	)
	jm := NewJobManager("")
	id := jm.CreateJob(&LabConfig{
		StackName:                 "workshop",
		StudentPortalPassword:     password,
		StudentPortalPasswordHash: hash,
	})
	job, _ := jm.GetJob(id)

	// What a student is served: neither value, at all.
	job.mu.RLock()
	api, err := job.sanitizedCopy(false)
	job.mu.RUnlock()
	require.NoError(t, err)
	assert.Empty(t, api.Config.StudentPortalPassword)
	assert.Empty(t, api.Config.StudentPortalPasswordHash)

	// What goes to disk: both, encrypted, and readable again.
	job.mu.RLock()
	onDisk, err := job.sanitizedCopy(true)
	job.mu.RUnlock()
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(onDisk.Config.StudentPortalPassword, encPrefix), "the password is encrypted at rest")
	assert.True(t, strings.HasPrefix(onDisk.Config.StudentPortalPasswordHash, encPrefix), "the hash is encrypted at rest")
	assert.Equal(t, password, mustDecrypt(t, onDisk.Config.StudentPortalPassword))
	assert.Equal(t, hash, mustDecrypt(t, onDisk.Config.StudentPortalPasswordHash))
}

// Generating once and only once is what keeps reconcilePortals from rewriting a
// lab's portal Secret on every cleanup tick.
func TestEnsurePortalPassword_Idempotent(t *testing.T) {
	t.Parallel()
	h := newPortalPasswordHandler(t)
	id := h.jobManager.CreateJob(&LabConfig{StackName: "workshop"})
	job, _ := h.jobManager.GetJob(id)

	generated, err := h.ensurePortalPassword(job)
	require.NoError(t, err)
	assert.True(t, generated, "the first call fills in a password")

	job.mu.RLock()
	password, hash := job.Config.StudentPortalPassword, job.Config.StudentPortalPasswordHash
	job.mu.RUnlock()
	require.NotEmpty(t, password)
	require.NotEmpty(t, hash)
	assert.True(t, comparePassword(hash, sha256Hex(password)), "the hash matches the password")

	generated, err = h.ensurePortalPassword(job)
	require.NoError(t, err)
	assert.False(t, generated, "a second call changes nothing")

	job.mu.RLock()
	assert.Equal(t, password, job.Config.StudentPortalPassword)
	assert.Equal(t, hash, job.Config.StudentPortalPasswordHash)
	job.mu.RUnlock()
}

// A lab carrying a password but no hash — however it got there — must come out
// with a hash of that password, not a new password.
func TestEnsurePortalPassword_BackfillsMissingHash(t *testing.T) {
	t.Parallel()
	h := newPortalPasswordHandler(t)
	id := h.jobManager.CreateJob(&LabConfig{StackName: "workshop", StudentPortalPassword: "KEEP-THIS-ONE"})
	job, _ := h.jobManager.GetJob(id)

	generated, err := h.ensurePortalPassword(job)
	require.NoError(t, err)
	assert.True(t, generated)

	job.mu.RLock()
	defer job.mu.RUnlock()
	assert.Equal(t, "KEEP-THIS-ONE", job.Config.StudentPortalPassword, "an existing password is kept")
	assert.True(t, comparePassword(job.Config.StudentPortalPasswordHash, sha256Hex("KEEP-THIS-ONE")))
}

func TestGetLabPortalPassword(t *testing.T) {
	t.Parallel()
	h := newPortalPasswordHandler(t)
	id := h.jobManager.CreateJob(&LabConfig{StackName: "workshop", StudentPortalPassword: "ABCDE-FGHJK-LMNPQ"})

	tests := []struct {
		name         string
		path         string
		method       string
		wantStatus   int
		wantPassword string
		wantSet      bool
	}{
		{name: "the lab's password", path: "/api/labs/" + id + "/portal/password", method: http.MethodGet, wantStatus: http.StatusOK, wantPassword: "ABCDE-FGHJK-LMNPQ", wantSet: true},
		{name: "legacy jobs prefix", path: "/api/jobs/" + id + "/portal/password", method: http.MethodGet, wantStatus: http.StatusOK, wantPassword: "ABCDE-FGHJK-LMNPQ", wantSet: true},
		{name: "unknown lab", path: "/api/labs/job-nope/portal/password", method: http.MethodGet, wantStatus: http.StatusNotFound},
		{name: "malformed path", path: "/api/labs/portal/password", method: http.MethodGet, wantStatus: http.StatusBadRequest},
		{name: "wrong method", path: "/api/labs/" + id + "/portal/password", method: http.MethodDelete, wantStatus: http.StatusMethodNotAllowed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			w := httptest.NewRecorder()
			h.GetLabPortalPassword(w, httptest.NewRequest(tt.method, tt.path, nil))
			require.Equal(t, tt.wantStatus, w.Code)
			if tt.wantStatus != http.StatusOK {
				return
			}
			var got struct {
				Password string `json:"password"`
				Set      bool   `json:"set"`
			}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
			assert.Equal(t, tt.wantPassword, got.Password)
			assert.Equal(t, tt.wantSet, got.Set)
		})
	}
}

// A lab whose portal has no password yet reports that rather than an empty one,
// so the admin page can explain instead of showing a blank field.
func TestGetLabPortalPassword_NotSetYet(t *testing.T) {
	t.Parallel()
	h := newPortalPasswordHandler(t)
	id := h.jobManager.CreateJob(&LabConfig{StackName: "workshop"})

	w := httptest.NewRecorder()
	h.GetLabPortalPassword(w, httptest.NewRequest(http.MethodGet, "/api/labs/"+id+"/portal/password", nil))
	require.Equal(t, http.StatusOK, w.Code)

	var got struct {
		Password string `json:"password"`
		Set      bool   `json:"set"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Empty(t, got.Password)
	assert.False(t, got.Set)
}

func TestSetLabPortalPassword(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		form         url.Values
		wantStatus   int
		wantPassword string // "" with a 200 means "generated, so unpredictable"
	}{
		{name: "an admin's own password", form: url.Values{"password": {"workshop-2026"}}, wantStatus: http.StatusOK, wantPassword: "workshop-2026"},
		{name: "surrounding spaces are trimmed", form: url.Values{"password": {"  workshop-2026  "}}, wantStatus: http.StatusOK, wantPassword: "workshop-2026"},
		{name: "no password rotates to a generated one", form: url.Values{}, wantStatus: http.StatusOK},
		{name: "too short is refused", form: url.Values{"password": {"short"}}, wantStatus: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := newPortalPasswordHandler(t)
			id := h.jobManager.CreateJob(&LabConfig{StackName: "workshop", StudentPortalPassword: "OLD-PASSWORD-HERE"})

			w := postLabPortalPassword(h, id, tt.form)
			require.Equal(t, tt.wantStatus, w.Code)

			job, _ := h.jobManager.GetJob(id)
			job.mu.RLock()
			password, hash := job.Config.StudentPortalPassword, job.Config.StudentPortalPasswordHash
			job.mu.RUnlock()

			if tt.wantStatus != http.StatusOK {
				assert.Equal(t, "OLD-PASSWORD-HERE", password, "a refused change leaves the password alone")
				return
			}
			if tt.wantPassword != "" {
				assert.Equal(t, tt.wantPassword, password)
			} else {
				assert.NotEqual(t, "OLD-PASSWORD-HERE", password, "rotating replaces the password")
				assert.NotEmpty(t, password)
			}
			// The hash always has to follow the password, or the portal refuses
			// every sign-in.
			assert.True(t, comparePassword(hash, sha256Hex(password)))

			var got struct {
				Password string `json:"password"`
			}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
			assert.Equal(t, password, got.Password, "the response hands back the new password to show")
		})
	}
}

func TestSetLabPortalPassword_UnknownLab(t *testing.T) {
	t.Parallel()
	h := newPortalPasswordHandler(t)
	w := postLabPortalPassword(h, "job-nope", url.Values{"password": {"workshop-2026"}})
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// The password this instance's own student area accepts: what an admin saved,
// else LAB_STUDENT_PASSWORD.
func TestEffectiveStudentPassword(t *testing.T) {
	tests := []struct {
		name  string
		saved *PortalSettings // nil: no store wired at all
		env   string
		want  string
	}{
		{name: "no store: environment", env: "env-password", want: "env-password"},
		{name: "nothing saved: environment", saved: &PortalSettings{}, env: "env-password", want: "env-password"},
		{name: "saved wins", saved: &PortalSettings{StudentPassword: "saved-password"}, env: "env-password", want: "saved-password"},
		{name: "saved with no environment", saved: &PortalSettings{StudentPassword: "saved-password"}, want: "saved-password"},
		{name: "nothing anywhere disables password sign-in", saved: &PortalSettings{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(EnvStudentPassword, tt.env)
			h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
			if tt.saved != nil {
				store := NewPortalSettingsStore("")
				require.NoError(t, store.Set(*tt.saved))
				h.SetPortalSettingsStore(store)
			}
			assert.Equal(t, tt.want, h.effectiveStudentPassword())
		})
	}
}

// The saved password has to beat the one the auth handler hashed out of the
// environment when it was built, and reach it without a restart.
func TestApplyStudentPassword(t *testing.T) {
	tests := []struct {
		name  string
		saved PortalSettings
		env   string
		// wantVerifies is the password that must sign a student in afterwards;
		// "" means the auth handler must be left untouched.
		wantVerifies string
	}{
		{name: "saved password applies", saved: PortalSettings{StudentPassword: "saved-password", StudentPasswordHash: "placeholder"}, env: "env-password", wantVerifies: "saved-password"},
		// Nothing saved must leave the auth handler alone: it already hashed the
		// environment variable when it was built.
		{name: "nothing saved is a no-op", env: "env-password"},
		{name: "nothing anywhere is a no-op too"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(EnvStudentPassword, tt.env)

			var applied string
			h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
			h.SetStudentPasswordConfigurer(func(hash string) { applied = hash })

			saved := tt.saved
			if saved.StudentPassword != "" {
				saved.StudentPasswordHash = mustHashStudentPassword(t, saved.StudentPassword)
			}
			store := NewPortalSettingsStore("")
			require.NoError(t, store.Set(saved))
			h.SetPortalSettingsStore(store)

			h.ApplyStudentPassword()

			if tt.wantVerifies == "" {
				assert.Empty(t, applied)
				return
			}
			require.NotEmpty(t, applied)
			assert.True(t, comparePassword(applied, sha256Hex(tt.wantVerifies)))
		})
	}
}

// Saving the page for its other settings must not wipe the password, and the
// checkbox must be the only thing that does.
func TestSavePortalSettings_KeepsOrResetsStudentPassword(t *testing.T) {
	tests := []struct {
		name         string
		form         url.Values
		wantPassword string
		wantApplied  string // the password the auth handler must end up accepting
	}{
		{
			name:         "saving the other settings keeps it",
			form:         url.Values{"portal_public_url": {"https://easylab.example.com"}},
			wantPassword: "saved-password", wantApplied: "saved-password",
		},
		{
			name:         "a new password replaces it",
			form:         url.Values{"portal_student_password": {"workshop-2026"}},
			wantPassword: "workshop-2026", wantApplied: "workshop-2026",
		},
		{
			name:        "the checkbox resets to the environment",
			form:        url.Values{"portal_student_password_reset": {"on"}},
			wantApplied: "env-password",
		},
		{
			name:         "the checkbox wins over a typed password",
			form:         url.Values{"portal_student_password": {"workshop-2026"}, "portal_student_password_reset": {"on"}},
			wantApplied:  "env-password",
			wantPassword: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(EnvStudentPassword, "env-password")

			var applied string
			h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
			h.SetStudentPasswordConfigurer(func(hash string) { applied = hash })
			store := NewPortalSettingsStore("")
			require.NoError(t, store.Set(PortalSettings{
				StudentPassword:     "saved-password",
				StudentPasswordHash: mustHashStudentPassword(t, "saved-password"),
			}))
			h.SetPortalSettingsStore(store)

			w := postPortalSettings(h, tt.form, false)
			require.NotContains(t, w.Body.String(), "error-message")

			assert.Equal(t, tt.wantPassword, store.Get().StudentPassword)
			require.NotEmpty(t, applied, "the new password must reach the auth handler")
			assert.True(t, comparePassword(applied, sha256Hex(tt.wantApplied)))
		})
	}
}

func TestSavePortalSettings_RejectsShortStudentPassword(t *testing.T) {
	t.Parallel()
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	store := NewPortalSettingsStore("")
	require.NoError(t, store.Set(PortalSettings{StudentPassword: "saved-password"}))
	h.SetPortalSettingsStore(store)

	w := postPortalSettings(h, url.Values{"portal_student_password": {"short"}}, false)
	assert.Contains(t, w.Body.String(), "error-message")
	assert.Equal(t, "saved-password", store.Get().StudentPassword, "a refused save changes nothing")
}

// The student password is a secret, so it must not sit in portal-settings.json
// in the clear the way the public URL and image do.
func TestPortalSettingsStore_EncryptsStudentPassword(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, InitDataEncryption(bytes.Repeat([]byte{7}, 32)))
	t.Cleanup(func() { _ = InitDataEncryption(nil) })

	store := NewPortalSettingsStore(dir)
	saved := PortalSettings{
		PublicURL:           "https://easylab.example.com",
		StudentPassword:     "saved-password",
		StudentPasswordHash: "$2a$10$fakebcrypthashvalue",
	}
	require.NoError(t, store.Set(saved))

	raw, err := os.ReadFile(filepath.Join(dir, "portal-settings.json"))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "saved-password")
	assert.NotContains(t, string(raw), "fakebcrypthashvalue")
	assert.Contains(t, string(raw), "easylab.example.com", "the public address is not a secret")

	// And it comes back readable on the next start.
	assert.Equal(t, saved, NewPortalSettingsStore(dir).Get())
}

// SetStudentPassword is how a password saved on the Student portals page reaches
// a running instance, so it has to govern whether students can sign in at all.
func TestSetStudentPassword(t *testing.T) {
	t.Parallel()
	ah := &AuthHandler{
		passwordHash:     "hash",
		sessions:         make(map[string]*Session),
		studentSessions:  make(map[string]*Session),
		azureOAuthStates: make(map[string]time.Time),
		templates:        make(map[string]*template.Template),
	}
	require.False(t, ah.studentLoginAvailable(), "no password and no provider: nowhere to sign in")

	hash := mustHashStudentPassword(t, "workshop-2026")
	ah.SetStudentPassword(hash)
	assert.True(t, ah.studentLoginAvailable())
	assert.True(t, comparePassword(ah.studentPasswordHash, sha256Hex("workshop-2026")))

	// Setting the same hash again is a no-op rather than a spurious change.
	ah.SetStudentPassword(hash)
	assert.Equal(t, hash, ah.studentPasswordHash)

	ah.SetStudentPassword("")
	assert.False(t, ah.studentLoginAvailable(), "clearing it turns password sign-in off")
}

// --- helpers ---------------------------------------------------------------

func newPortalPasswordHandler(t *testing.T) *Handler {
	t.Helper()
	return NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
}

func postLabPortalPassword(h *Handler, labID string, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/labs/"+labID+"/portal/password", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.SetLabPortalPassword(w, req)
	return w
}

func mustHashStudentPassword(t *testing.T, password string) string {
	t.Helper()
	hash, err := HashStudentPassword(password)
	require.NoError(t, err)
	return hash
}

func mustDecrypt(t *testing.T, value string) string {
	t.Helper()
	plaintext, err := decryptSecret(value)
	require.NoError(t, err)
	return plaintext
}
