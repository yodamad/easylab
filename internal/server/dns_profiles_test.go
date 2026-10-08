package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testOVHDNSCredentials() map[string]string {
	return map[string]string{
		"ovhAppKey":      "dns-app-key",
		"ovhAppSecret":   "dns-app-secret",
		"ovhConsumerKey": "dns-consumer-key",
		"ovhEndpoint":    "ovh-eu",
	}
}

func TestCredentialVault_DNSProfiles(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	v := openTestVault(t, dataDir, testEnvKey(1))

	created, err := v.SaveDNSProfile(DNSProfile{Name: "Workshops", Provider: "ovh", Zone: "example.com", Credentials: testOVHDNSCredentials()})
	require.NoError(t, err)
	require.NotEmpty(t, created.ID)
	assert.Nil(t, created.Credentials, "a saved profile is returned without its credentials")

	// Names are unique whatever their case.
	_, err = v.SaveDNSProfile(DNSProfile{Name: "workshops", Provider: "azure"})
	assert.ErrorIs(t, err, errDNSProfileNameTaken)

	_, err = v.SaveDNSProfile(DNSProfile{ID: "no-such-id", Name: "Other", Provider: "ovh"})
	assert.ErrorIs(t, err, errDNSProfileNotFound)

	second, err := v.SaveDNSProfile(DNSProfile{Name: "Alpha", Provider: "azure", Credentials: map[string]string{"azureClientSecret": "az-secret"}})
	require.NoError(t, err)

	listed := v.ListDNSProfiles()
	require.Len(t, listed, 2)
	assert.Equal(t, []string{"Alpha", "Workshops"}, []string{listed[0].Name, listed[1].Name}, "listed by name")
	for _, p := range listed {
		assert.Nil(t, p.Credentials, "listings never carry credentials")
	}

	raw, err := os.ReadFile(vaultFilePath(dataDir))
	require.NoError(t, err)
	assert.Contains(t, string(raw), "Workshops", "names stay readable so a locked vault can list them")
	for _, secret := range []string{"dns-app-key", "dns-app-secret", "dns-consumer-key", "az-secret"} {
		assert.NotContains(t, string(raw), secret)
	}

	// Survives a restart, credentials included.
	reopened := openTestVault(t, dataDir, testEnvKey(1))
	got, err := reopened.GetDNSProfile(created.ID)
	require.NoError(t, err)
	assert.Equal(t, "Workshops", got.Name)
	assert.Equal(t, "example.com", got.Zone)
	assert.Equal(t, testOVHDNSCredentials(), got.Credentials)

	// Renaming to its own name is not a clash.
	updated := got
	updated.Zone = "labs.example.com"
	_, err = reopened.SaveDNSProfile(updated)
	require.NoError(t, err)
	got, err = reopened.GetDNSProfile(created.ID)
	require.NoError(t, err)
	assert.Equal(t, "labs.example.com", got.Zone)

	require.NoError(t, reopened.DeleteDNSProfile(second.ID))
	assert.ErrorIs(t, reopened.DeleteDNSProfile(second.ID), errDNSProfileNotFound)
	_, err = reopened.GetDNSProfile(second.ID)
	assert.ErrorIs(t, err, errDNSProfileNotFound)
	assert.Len(t, reopened.ListDNSProfiles(), 1)
}

func TestCredentialVault_DNSProfilesWhileLocked(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	v := openTestVault(t, dataDir, nil)
	require.NoError(t, v.SetPassphrase(testVaultPassphrase))
	created, err := v.SaveDNSProfile(DNSProfile{Name: "Workshops", Provider: "ovh", Credentials: testOVHDNSCredentials()})
	require.NoError(t, err)

	locked := openTestVault(t, dataDir, nil)
	require.Len(t, locked.ListDNSProfiles(), 1, "a locked vault still says what it holds")

	_, err = locked.GetDNSProfile(created.ID)
	assert.ErrorIs(t, err, errVaultLocked)
	_, err = locked.SaveDNSProfile(DNSProfile{Name: "New", Provider: "ovh"})
	assert.ErrorIs(t, err, errVaultLocked)
	assert.ErrorIs(t, locked.DeleteDNSProfile(created.ID), errVaultLocked)

	require.NoError(t, locked.Unlock(testVaultPassphrase))
	got, err := locked.GetDNSProfile(created.ID)
	require.NoError(t, err)
	assert.Equal(t, testOVHDNSCredentials(), got.Credentials)
}

func ovhProfileForm(name string) url.Values {
	return url.Values{
		"profile_name":            {name},
		"profile_provider":        {"ovh"},
		"profile_zone":            {"Example.COM."},
		"dns_cred_ovhAppKey":      {"dns-app-key"},
		"dns_cred_ovhAppSecret":   {"dns-app-secret"},
		"dns_cred_ovhConsumerKey": {"dns-consumer-key"},
		"dns_cred_ovhEndpoint":    {"ovh-eu"},
	}
}

func TestSaveDNSProfile_Validation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(url.Values)
		want   string
	}{
		{name: "missing name", mutate: func(f url.Values) { f.Set("profile_name", "  ") }, want: "Give the profile a name"},
		{name: "name too long", mutate: func(f url.Values) { f.Set("profile_name", strings.Repeat("n", maxDNSProfileNameLength+1)) }, want: "Give the profile a name"},
		{name: "unknown provider", mutate: func(f url.Values) { f.Set("profile_provider", "route53") }, want: "Choose a DNS provider"},
		{name: "missing secret", mutate: func(f url.Values) { f.Del("dns_cred_ovhAppSecret") }, want: "OVH Application Secret is required"},
		{name: "unknown profile id", mutate: func(f url.Values) { f.Set("profile_id", "no-such-id") }, want: "no longer exists"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h, v := vaultTestHandler(t, t.TempDir(), testEnvKey(1))
			form := ovhProfileForm("Workshops")
			tt.mutate(form)

			rec := postVaultForm(h.SaveDNSProfile, "/api/dns-profiles", form)
			assert.Contains(t, rec.Body.String(), "error-message")
			assert.Contains(t, rec.Body.String(), tt.want)
			assert.Empty(t, rec.Header().Get("HX-Redirect"))
			assert.Empty(t, v.ListDNSProfiles())
		})
	}
}

func TestSaveDNSProfile_CreateUpdateDelete(t *testing.T) {
	t.Parallel()
	h, v := vaultTestHandler(t, t.TempDir(), testEnvKey(1))

	rec := postVaultForm(h.SaveDNSProfile, "/api/dns-profiles", ovhProfileForm("Workshops"))
	require.Equal(t, "/admin/dns", rec.Header().Get("HX-Redirect"), "body: %s", rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "dns-app-secret")

	profiles := v.ListDNSProfiles()
	require.Len(t, profiles, 1)
	assert.Equal(t, "example.com", profiles[0].Zone, "zone is normalised")
	id := profiles[0].ID

	// A second profile cannot take the name.
	rec = postVaultForm(h.SaveDNSProfile, "/api/dns-profiles", ovhProfileForm("WORKSHOPS"))
	assert.Contains(t, rec.Body.String(), "already uses this name")

	// Editing with the secret fields left blank keeps them; the rest is replaced.
	edit := url.Values{
		"profile_id":           {id},
		"profile_name":         {"Workshops EU"},
		"profile_provider":     {"ovh"},
		"dns_cred_ovhAppKey":   {"new-app-key"},
		"dns_cred_ovhEndpoint": {""},
	}
	rec = postVaultForm(h.SaveDNSProfile, "/api/dns-profiles", edit)
	require.Equal(t, "/admin/dns", rec.Header().Get("HX-Redirect"), "body: %s", rec.Body.String())

	got, err := v.GetDNSProfile(id)
	require.NoError(t, err)
	assert.Equal(t, "Workshops EU", got.Name)
	assert.Empty(t, got.Zone)
	assert.Equal(t, map[string]string{
		"ovhAppKey":      "new-app-key",
		"ovhAppSecret":   "dns-app-secret",
		"ovhConsumerKey": "dns-consumer-key",
	}, got.Credentials)

	// Switching provider carries no secret over, so the new ones are required.
	toAzure := url.Values{"profile_id": {id}, "profile_name": {"Workshops EU"}, "profile_provider": {"azure"}}
	rec = postVaultForm(h.SaveDNSProfile, "/api/dns-profiles", toAzure)
	assert.Contains(t, rec.Body.String(), "Client Secret is required")

	rec = postVaultForm(h.DeleteDNSProfile, "/api/dns-profiles/delete", url.Values{"profile_id": {id}})
	assert.Equal(t, "/admin/dns", rec.Header().Get("HX-Redirect"))
	assert.Empty(t, v.ListDNSProfiles())

	rec = postVaultForm(h.DeleteDNSProfile, "/api/dns-profiles/delete", url.Values{"profile_id": {id}})
	assert.Contains(t, rec.Body.String(), "no longer exists")
}

func TestDNSProfileFieldsHTML(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		provider string
		existing map[string]string
		want     []string
		notWant  []string
	}{
		{
			name:     "ovh, new profile",
			provider: "ovh",
			want: []string{
				`name="dns_cred_ovhAppKey"`,
				`type="password" id="profile_cred_ovhAppSecret" name="dns_cred_ovhAppSecret"`,
				`type="password" id="profile_cred_ovhConsumerKey" name="dns_cred_ovhConsumerKey"`,
				`name="dns_cred_ovhEndpoint"`,
			},
			notWant: []string{"saved — enter new value"},
		},
		{
			name:     "azure, editing",
			provider: "azure",
			existing: map[string]string{"azureTenantId": `tenant"><script>`, "azureClientSecret": "az-secret"},
			want: []string{
				`name="dns_cred_azureClientSecret" placeholder="(saved — enter new value to change)"`,
				`value="tenant&#34;&gt;&lt;script&gt;"`,
			},
			notWant: []string{"az-secret", "<script>"},
		},
		{name: "unknown provider renders nothing", provider: "route53"},
		{name: "no provider renders nothing", provider: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			html := string(dnsProfileFieldsHTML(tt.provider, tt.existing))
			if len(tt.want) == 0 {
				assert.Empty(t, html)
			}
			for _, want := range tt.want {
				assert.Contains(t, html, want)
			}
			for _, notWant := range tt.notWant {
				assert.NotContains(t, html, notWant)
			}
		})
	}
}

func TestDNSProfileFields_Handler(t *testing.T) {
	t.Parallel()
	h, _ := vaultTestHandler(t, t.TempDir(), nil)

	rec := httptest.NewRecorder()
	h.DNSProfileFields(rec, httptest.NewRequest(http.MethodGet, "/api/dns-profiles/fields?profile_provider=ovh", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `name="dns_cred_ovhAppSecret"`)

	rec = httptest.NewRecorder()
	h.DNSProfileFields(rec, httptest.NewRequest(http.MethodPost, "/api/dns-profiles/fields", nil))
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

func TestApplyDNSProfile(t *testing.T) {
	t.Parallel()
	h, v := vaultTestHandler(t, t.TempDir(), testEnvKey(1))
	profile, err := v.SaveDNSProfile(DNSProfile{Name: "Workshops", Provider: "ovh", Zone: "example.com", Credentials: testOVHDNSCredentials()})
	require.NoError(t, err)

	lockedDir := t.TempDir()
	lockedSource := openTestVault(t, lockedDir, nil)
	require.NoError(t, lockedSource.SetPassphrase(testVaultPassphrase))
	lockedProfile, err := lockedSource.SaveDNSProfile(DNSProfile{Name: "Locked", Provider: "ovh", Credentials: testOVHDNSCredentials()})
	require.NoError(t, err)
	lockedHandler, _ := vaultTestHandler(t, lockedDir, nil)

	noVault := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

	tests := []struct {
		name        string
		handler     *Handler
		profileID   string
		config      LabConfig
		wantErr     error
		wantAnyErr  bool
		wantProfile string
		wantZone    string
		wantCreds   map[string]string
	}{
		{
			name:      "none picked leaves the config alone",
			handler:   h,
			config:    LabConfig{DNSProvider: "ovh", DNSZone: "typed.example", DNSCredentials: map[string]string{"ovhAppKey": "typed"}},
			wantZone:  "typed.example",
			wantCreds: map[string]string{"ovhAppKey": "typed"},
		},
		{
			name:        "profile fills provider, zone and credentials",
			handler:     h,
			profileID:   profile.ID,
			config:      LabConfig{},
			wantProfile: "Workshops",
			wantZone:    "example.com",
			wantCreds:   testOVHDNSCredentials(),
		},
		{
			name:        "a typed zone is kept",
			handler:     h,
			profileID:   profile.ID,
			config:      LabConfig{DNSProvider: "ovh", DNSZone: "labs.example.com"},
			wantProfile: "Workshops",
			wantZone:    "labs.example.com",
			wantCreds:   testOVHDNSCredentials(),
		},
		{name: "provider mismatch", handler: h, profileID: profile.ID, config: LabConfig{DNSProvider: "azure"}, wantAnyErr: true},
		{name: "unknown profile", handler: h, profileID: "no-such-id", wantErr: errDNSProfileNotFound},
		{name: "locked vault", handler: lockedHandler, profileID: lockedProfile.ID, wantErr: errVaultLocked},
		{name: "no vault wired", handler: noVault, profileID: profile.ID, wantErr: errDNSProfileNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			form := url.Values{}
			if tt.profileID != "" {
				form.Set("dns_profile", tt.profileID)
			}
			req := httptest.NewRequest(http.MethodPost, "/api/labs", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

			config := tt.config
			err := tt.handler.applyDNSProfile(req, &config)
			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
				return
			}
			if tt.wantAnyErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "ovh", config.DNSProvider)
			assert.Equal(t, tt.wantProfile, config.DNSProfile)
			assert.Equal(t, tt.wantZone, config.DNSZone)
			assert.Equal(t, tt.wantCreds, config.DNSCredentials)
		})
	}
}

func TestWizardDNSProfiles(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	h, v := vaultTestHandler(t, dataDir, nil)

	profiles, locked := h.wizardDNSProfiles()
	assert.Empty(t, profiles)
	assert.False(t, locked)

	require.NoError(t, v.SetPassphrase(testVaultPassphrase))
	_, err := v.SaveDNSProfile(DNSProfile{Name: "Workshops", Provider: "ovh", Zone: "example.com", Credentials: testOVHDNSCredentials()})
	require.NoError(t, err)

	profiles, locked = h.wizardDNSProfiles()
	require.Len(t, profiles, 1)
	assert.False(t, locked)
	assert.Equal(t, "OVH DNS", profiles[0].ProviderLabel)
	assert.Equal(t, "example.com", profiles[0].Zone)

	// After a restart the wizard offers none, and says why.
	restarted, _ := vaultTestHandler(t, dataDir, nil)
	profiles, locked = restarted.wizardDNSProfiles()
	assert.Empty(t, profiles)
	assert.True(t, locked)

	noVault := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	profiles, locked = noVault.wizardDNSProfiles()
	assert.Empty(t, profiles)
	assert.False(t, locked)
}

// The DNS page lists profiles and never prints a secret, even when editing one.
func TestServeDNSProfiles(t *testing.T) {
	t.Chdir("../..") // getTemplate resolves web/ relative to the working directory

	h, v := vaultTestHandler(t, t.TempDir(), testEnvKey(1))
	profile, err := v.SaveDNSProfile(DNSProfile{Name: "Workshops <b>", Provider: "ovh", Zone: "example.com", Credentials: testOVHDNSCredentials()})
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	h.ServeDNSProfiles(rec, httptest.NewRequest(http.MethodGet, "/admin/dns", nil))
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	body := rec.Body.String()
	assert.Contains(t, body, "Workshops &lt;b&gt;")
	assert.NotContains(t, body, "Workshops <b>")
	assert.Contains(t, body, "Add a DNS profile")
	assert.Contains(t, body, "OVH DNS")

	rec = httptest.NewRecorder()
	h.ServeDNSProfiles(rec, httptest.NewRequest(http.MethodGet, "/admin/dns?edit="+profile.ID, nil))
	require.Equal(t, http.StatusOK, rec.Code)
	body = rec.Body.String()
	assert.Contains(t, body, "Save changes")
	assert.Contains(t, body, `value="dns-app-key"`, "non-secret fields are shown for editing")
	assert.Contains(t, body, "(saved — enter new value to change)")
	for _, secret := range []string{"dns-app-secret", "dns-consumer-key"} {
		assert.NotContains(t, body, secret)
	}

	noVault := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	rec = httptest.NewRecorder()
	noVault.ServeDNSProfiles(rec, httptest.NewRequest(http.MethodGet, "/admin/dns", nil))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

// The wizard offers saved profiles, or explains that they are locked.
func TestServeAdminUI_OffersDNSProfiles(t *testing.T) {
	t.Chdir("../..") // getTemplate resolves web/ relative to the working directory

	dataDir := t.TempDir()
	h, v := vaultTestHandler(t, dataDir, nil)
	require.NoError(t, v.SetPassphrase(testVaultPassphrase))
	profile, err := v.SaveDNSProfile(DNSProfile{Name: "Workshops", Provider: "ovh", Zone: "example.com", Credentials: testOVHDNSCredentials()})
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	h.ServeAdminUI(rec, httptest.NewRequest(http.MethodGet, "/admin", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, `<option value="`+profile.ID+`" data-name="Workshops" data-provider="ovh" data-zone="example.com">Workshops (OVH DNS)</option>`)
	assert.NotContains(t, body, "Saved DNS profiles are locked")
	assert.NotContains(t, body, "dns-app-secret")

	restarted, _ := vaultTestHandler(t, dataDir, nil)
	rec = httptest.NewRecorder()
	restarted.ServeAdminUI(rec, httptest.NewRequest(http.MethodGet, "/admin", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	body = rec.Body.String()
	assert.Contains(t, body, "Saved DNS profiles are locked")
	assert.NotContains(t, body, profile.ID)
}

// On a retry with an edited config, a picked profile wins; otherwise blank
// credential fields keep what the lab already had.
func TestHandler_RetryJobWithConfig_DNSProfile(t *testing.T) {
	original := map[string]string{"ovhAppKey": "orig-key", "ovhAppSecret": "orig-secret", "ovhConsumerKey": "orig-consumer"}

	tests := []struct {
		name        string
		profile     string // "saved", "unknown" or "" for none
		wantError   string
		wantProfile string
		wantCreds   map[string]string
	}{
		{name: "no profile keeps the lab's credentials", wantProfile: "Original", wantCreds: original},
		{name: "picked profile replaces them", profile: "saved", wantProfile: "Workshops", wantCreds: testOVHDNSCredentials()},
		{name: "unknown profile is rejected", profile: "unknown", wantError: "DNS Profile Error", wantProfile: "Original", wantCreds: original},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jm := NewJobManager("")
			id := jm.CreateJob(&LabConfig{
				StackName:          "test",
				UseExistingCluster: true,
				ExternalKubeconfig: "orig-kubeconfig",
				Domain:             "labs.example.com",
				DNSProvider:        "ovh",
				DNSZone:            "example.com",
				DNSProfile:         "Original",
				DNSCredentials:     original,
			})
			require.NoError(t, jm.UpdateJobStatus(id, JobStatusFailed))

			vault := openTestVault(t, t.TempDir(), testEnvKey(1))
			saved, err := vault.SaveDNSProfile(DNSProfile{Name: "Workshops", Provider: "ovh", Credentials: testOVHDNSCredentials()})
			require.NoError(t, err)
			h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
			h.SetCredentialVault(vault)

			form := url.Values{
				"use_existing_cluster": {"true"},
				"domain":               {"labs.example.com"},
				"dns_provider":         {"ovh"},
				"dns_zone":             {"example.com"},
			}
			switch tt.profile {
			case "saved":
				form.Set("dns_profile", saved.ID)
			case "unknown":
				form.Set("dns_profile", "no-such-id")
			}

			w := httptest.NewRecorder()
			h.RetryJobWithConfig(w, retryWithConfigRequest(id, form))
			if tt.wantError != "" {
				assert.Contains(t, w.Body.String(), tt.wantError)
			} else {
				assert.NotContains(t, w.Body.String(), "error-message")
			}

			job, _ := jm.GetJob(id)
			job.mu.RLock()
			defer job.mu.RUnlock()
			require.NotNil(t, job.Config)
			assert.Equal(t, tt.wantProfile, job.Config.DNSProfile)
			assert.Equal(t, tt.wantCreds, job.Config.DNSCredentials)
		})
	}
}
