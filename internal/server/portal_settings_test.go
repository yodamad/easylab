package server

import (
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

func TestNormalizePublicURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    string
		expected string
		wantErr  bool
	}{
		{name: "empty means default", input: "", expected: ""},
		{name: "blank means default", input: "   ", expected: ""},
		{name: "https origin", input: "https://easylab.example.com", expected: "https://easylab.example.com"},
		{name: "trailing slash is dropped", input: "https://easylab.example.com/", expected: "https://easylab.example.com"},
		{name: "port is kept", input: " http://localhost:8081 ", expected: "http://localhost:8081"},
		{name: "path is refused", input: "https://easylab.example.com/admin", wantErr: true},
		{name: "query is refused", input: "https://easylab.example.com?x=1", wantErr: true},
		{name: "fragment is refused", input: "https://easylab.example.com#x", wantErr: true},
		{name: "credentials are refused", input: "https://user:pw@easylab.example.com", wantErr: true},
		{name: "no scheme", input: "easylab.example.com", wantErr: true},
		{name: "other scheme", input: "javascript://easylab.example.com", wantErr: true},
		{name: "no host", input: "https://", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := normalizePublicURL(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestNormalizePortalImage(t *testing.T) {
	t.Parallel()
	digest := "sha256:" + strings.Repeat("a", 64)
	tests := []struct {
		name     string
		input    string
		expected string
		wantErr  bool
	}{
		{name: "empty means default", input: "", expected: ""},
		{name: "repository and tag", input: " docker.io/yodamad/easylab:v1.2.3 ", expected: "docker.io/yodamad/easylab:v1.2.3"},
		{name: "registry with port", input: "registry.local:5000/easylab:dev", expected: "registry.local:5000/easylab:dev"},
		{name: "no tag", input: "yodamad/easylab", expected: "yodamad/easylab"},
		{name: "digest", input: "ghcr.io/me/easylab@" + digest, expected: "ghcr.io/me/easylab@" + digest},
		{name: "tag and digest", input: "ghcr.io/me/easylab:v1@" + digest, expected: "ghcr.io/me/easylab:v1@" + digest},
		{name: "space inside", input: "yodamad/easylab :v1", wantErr: true},
		{name: "shell characters", input: "yodamad/easylab:v1;rm -rf /", wantErr: true},
		{name: "starts with a dash", input: "-yodamad/easylab", wantErr: true},
		{name: "bad digest", input: "yodamad/easylab@sha256:abc", wantErr: true},
		{name: "too long", input: strings.Repeat("a", 600), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := normalizePortalImage(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestPortalSettingsStore_Persists(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := NewPortalSettingsStore(dir)
	assert.Equal(t, PortalSettings{}, store.Get())

	saved := PortalSettings{PublicURL: "https://easylab.example.com", Image: "registry.local/easylab:dev"}
	require.NoError(t, store.Set(saved))
	assert.Equal(t, saved, NewPortalSettingsStore(dir).Get(), "settings survive a restart")

	// Clearing a field removes it rather than keeping the old value.
	require.NoError(t, store.Set(PortalSettings{}))
	assert.Equal(t, PortalSettings{}, NewPortalSettingsStore(dir).Get())

	// A corrupt file is reported at load and leaves the defaults in place.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "portal-settings.json"), []byte("{nope"), 0600))
	assert.Equal(t, PortalSettings{}, NewPortalSettingsStore(dir).Get())

	// Without a data dir nothing is written, but the settings still apply.
	inMemory := NewPortalSettingsStore("")
	require.NoError(t, inMemory.Set(saved))
	assert.Equal(t, saved, inMemory.Get())
}

// What the admin saved wins over the environment; an empty field falls back to it.
func TestEffectivePortalSettings(t *testing.T) {
	tests := []struct {
		name          string
		saved         *PortalSettings // nil: no store wired at all
		envImage      string
		envPublicURL  string
		wantImage     string
		wantPublicURL string
	}{
		{name: "no store: environment", envImage: testPortalImage, envPublicURL: "https://env.example.com", wantImage: testPortalImage, wantPublicURL: "https://env.example.com"},
		{name: "nothing saved: environment", saved: &PortalSettings{}, envImage: testPortalImage, envPublicURL: "https://env.example.com", wantImage: testPortalImage, wantPublicURL: "https://env.example.com"},
		{
			name: "saved values win", saved: &PortalSettings{PublicURL: "https://saved.example.com", Image: "registry.local/easylab:dev"},
			envImage: testPortalImage, envPublicURL: "https://env.example.com",
			wantImage: "registry.local/easylab:dev", wantPublicURL: "https://saved.example.com",
		},
		{
			name: "each field falls back on its own", saved: &PortalSettings{Image: "registry.local/easylab:dev"},
			envPublicURL: "https://env.example.com",
			wantImage:    "registry.local/easylab:dev", wantPublicURL: "https://env.example.com",
		},
		{name: "nothing anywhere on a development build", saved: &PortalSettings{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(EnvPortalImage, tt.envImage)
			previous := Version
			Version = "dev"
			t.Cleanup(func() { Version = previous })

			h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
			h.SetPortalAuth(tt.envPublicURL, nil)
			if tt.saved != nil {
				store := NewPortalSettingsStore("")
				require.NoError(t, store.Set(*tt.saved))
				h.SetPortalSettingsStore(store)
			}

			assert.Equal(t, tt.wantImage, h.effectivePortalImage())
			assert.Equal(t, tt.wantPublicURL, h.effectivePublicURL())
		})
	}
}

func postPortalSettings(h *Handler, form url.Values, htmx bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/portal-settings", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if htmx {
		req.Header.Set("HX-Request", "true")
	}
	w := httptest.NewRecorder()
	h.SavePortalSettings(w, req)
	return w
}

func TestSavePortalSettings(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		form      url.Values
		wantSaved PortalSettings
		wantError string // "" means the save succeeds
	}{
		{
			name:      "both values",
			form:      url.Values{"portal_public_url": {"https://easylab.example.com/"}, "portal_image": {"registry.local/easylab:dev"}},
			wantSaved: PortalSettings{PublicURL: "https://easylab.example.com", Image: "registry.local/easylab:dev"},
		},
		{name: "empty form resets to the defaults", form: url.Values{}, wantSaved: PortalSettings{}},
		{
			name:      "invalid address keeps what was saved",
			form:      url.Values{"portal_public_url": {"easylab.example.com/admin"}, "portal_image": {"registry.local/easylab:dev"}},
			wantSaved: PortalSettings{PublicURL: "https://before.example.com"},
			wantError: "public address is not valid",
		},
		{
			name:      "invalid image keeps what was saved",
			form:      url.Values{"portal_public_url": {"https://easylab.example.com"}, "portal_image": {"not an image"}},
			wantSaved: PortalSettings{PublicURL: "https://before.example.com"},
			wantError: "portal image is not valid",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := NewPortalSettingsStore(t.TempDir())
			require.NoError(t, store.Set(PortalSettings{PublicURL: "https://before.example.com"}))
			h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
			h.SetPortalSettingsStore(store)

			w := postPortalSettings(h, tt.form, true)

			require.Equal(t, http.StatusOK, w.Code)
			assert.Equal(t, tt.wantSaved, store.Get())
			if tt.wantError != "" {
				assert.Contains(t, w.Body.String(), "error-message")
				assert.Contains(t, w.Body.String(), tt.wantError)
				assert.Empty(t, w.Header().Get("HX-Redirect"))
				return
			}
			assert.Contains(t, w.Body.String(), "success-message")
			assert.Equal(t, "/admin/student-portals", w.Header().Get("HX-Redirect"))
		})
	}
}

func TestSavePortalSettings_Plumbing(t *testing.T) {
	t.Parallel()
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

	// No store wired: neither endpoint pretends to work.
	w := postPortalSettings(h, url.Values{}, true)
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	w = httptest.NewRecorder()
	h.ServePortalSettings(w, httptest.NewRequest(http.MethodGet, "/admin/student-portals", nil))
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)

	h.SetPortalSettingsStore(NewPortalSettingsStore(""))
	w = httptest.NewRecorder()
	h.SavePortalSettings(w, httptest.NewRequest(http.MethodGet, "/api/portal-settings", nil))
	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)

	// Without HTMX the form still works, as a plain redirect.
	w = postPortalSettings(h, url.Values{"portal_image": {"registry.local/easylab:dev"}}, false)
	assert.Equal(t, http.StatusSeeOther, w.Code)
	assert.Equal(t, "/admin/student-portals", w.Header().Get("Location"))
}

// Saving is not just stored: labs that already have a portal are redeployed with
// the new image and told the new address, without the admin touching each one.
func TestSavePortalSettings_UpdatesExistingPortals(t *testing.T) {
	t.Setenv(EnvPortalImage, testPortalImage)
	h, id, fb := newPortalLab(t, nil)
	h.SetPortalSettingsStore(NewPortalSettingsStore(""))
	h.reconcilePortal(id)
	require.Len(t, fb.ensuredSpecs(), 1)
	assert.Equal(t, testPortalImage, fb.ensuredSpecs()[0].Image)

	w := postPortalSettings(h, url.Values{
		"portal_public_url": {"https://easylab.example.com"},
		"portal_image":      {"registry.local/easylab:dev"},
	}, true)
	require.Equal(t, http.StatusOK, w.Code)

	require.Eventually(t, func() bool { return len(fb.ensuredSpecs()) == 2 }, 2*time.Second, 10*time.Millisecond)
	spec := fb.ensuredSpecs()[1]
	assert.Equal(t, "registry.local/easylab:dev", spec.Image)
	state, err := decodePortalState(spec.Config)
	require.NoError(t, err)
	assert.Equal(t, "https://easylab.example.com", state.Auth.AdminURL)
}

// The page says what portals will actually get, and what is missing.
func TestServePortalSettings(t *testing.T) {
	t.Chdir("../..")

	tests := []struct {
		name        string
		saved       PortalSettings
		envImage    string
		envURL      string
		contains    []string
		notContains []string
	}{
		{
			name:     "development build with nothing set",
			contains: []string{"cannot be deployed", "No default on a development build", "http://example.com"},
		},
		{
			name: "image but no address", envImage: testPortalImage,
			contains:    []string{"password sign-in only", "Default: <code>" + testPortalImage + "</code>"},
			notContains: []string{"cannot be deployed"},
		},
		{
			name:        "saved values are shown and win",
			saved:       PortalSettings{PublicURL: "https://saved.example.com", Image: "registry.local/easylab:dev"},
			envImage:    testPortalImage,
			envURL:      "https://env.example.com",
			contains:    []string{`value="https://saved.example.com"`, `value="registry.local/easylab:dev"`, "Portals run <strong>registry.local/easylab:dev</strong>", "from <code>EASYLAB_PUBLIC_URL</code>"},
			notContains: []string{"cannot be deployed", "password sign-in only"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(EnvPortalImage, tt.envImage)
			previous := Version
			Version = "dev"
			t.Cleanup(func() { Version = previous })

			h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
			h.SetPortalAuth(tt.envURL, nil)
			store := NewPortalSettingsStore("")
			require.NoError(t, store.Set(tt.saved))
			h.SetPortalSettingsStore(store)

			w := httptest.NewRecorder()
			h.ServePortalSettings(w, httptest.NewRequest(http.MethodGet, "/admin/student-portals", nil))

			require.Equal(t, http.StatusOK, w.Code)
			body := w.Body.String()
			for _, want := range tt.contains {
				assert.Contains(t, body, want)
			}
			for _, unwanted := range tt.notContains {
				assert.NotContains(t, body, unwanted)
			}
		})
	}
}

// With an image saved from the page, a development build can deploy portals and
// the wizard offers it by default — no environment variable needed.
func TestPortalSettings_ImageUnlocksDevelopmentBuilds(t *testing.T) {
	t.Chdir("../..")
	t.Setenv(EnvPortalImage, "")
	previous := Version
	Version = "dev"
	t.Cleanup(func() { Version = previous })

	h, id, fb := newPortalLab(t, nil)
	store := NewPortalSettingsStore("")
	h.SetPortalSettingsStore(store)

	h.reconcilePortal(id)
	assert.Empty(t, fb.ensuredSpecs())
	assert.Contains(t, h.portalError(id), "Student portals settings page")

	require.NoError(t, store.Set(PortalSettings{Image: "registry.local/easylab:dev"}))
	h.reconcilePortal(id)
	require.Len(t, fb.ensuredSpecs(), 1)
	assert.Equal(t, "registry.local/easylab:dev", fb.ensuredSpecs()[0].Image)
	assert.Empty(t, h.portalError(id))

	w := httptest.NewRecorder()
	h.ServeAdminUI(w, httptest.NewRequest(http.MethodGet, "/admin", nil))
	assert.Contains(t, w.Body.String(), `name="student_portal" value="true" checked>`)
}
