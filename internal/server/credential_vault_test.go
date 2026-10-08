package server

import (
	"bytes"
	"encoding/json"
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

const testVaultPassphrase = "correct horse battery"

// testEnvKey is a stand-in for an explicitly configured LAB_DATA_ENCRYPTION_KEY.
func testEnvKey(fill byte) []byte {
	return bytes.Repeat([]byte{fill}, 32)
}

// openTestVault opens a vault with a cheap KDF so tests do not pay for 600k
// PBKDF2 rounds. A vault reopened from disk keeps the rounds it was saved with.
func openTestVault(t *testing.T, dataDir string, envKey []byte) *CredentialVault {
	t.Helper()
	v := NewCredentialVault(dataDir, envKey)
	v.kdfIterations = 10
	return v
}

func testOVHCredentials() *OVHCredentials {
	return &OVHCredentials{
		ApplicationKey:    "app-key-plain",
		ApplicationSecret: "app-secret-plain",
		ConsumerKey:       "consumer-key-plain",
		ServiceName:       "service-plain",
		Endpoint:          "ovh-eu",
	}
}

func vaultFilePath(dataDir string) string {
	return filepath.Join(dataDir, credentialVaultFileName)
}

func TestCredentialVault_InitialState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		withDataDir   bool
		envKey        []byte
		wantState     VaultState
		wantKeySource string
	}{
		{name: "no data dir", wantState: VaultDisabled},
		{name: "no data dir ignores env key", envKey: testEnvKey(1), wantState: VaultDisabled},
		{name: "data dir without key stays in memory", withDataDir: true, wantState: VaultUninitialized},
		{name: "data dir with env key saves", withDataDir: true, envKey: testEnvKey(1), wantState: VaultUnlocked, wantKeySource: vaultKeySourceEnv},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dataDir := ""
			if tt.withDataDir {
				dataDir = t.TempDir()
			}
			status := openTestVault(t, dataDir, tt.envKey).Status()
			assert.Equal(t, tt.wantState, status.State)
			assert.Equal(t, tt.wantKeySource, status.KeySource)
		})
	}
}

// Without a key from outside the data directory nothing may reach the disk.
func TestCredentialVault_MemoryOnlyWritesNothing(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	v := openTestVault(t, dataDir, nil)

	require.NoError(t, v.PutProviderCredentials("ovh", testOVHCredentials()))
	_, err := v.SaveDNSProfile(DNSProfile{Name: "zone", Provider: "ovh", Credentials: map[string]string{"ovhAppSecret": "s"}})
	require.NoError(t, err)

	var got OVHCredentials
	found, err := v.GetProviderCredentials("ovh", &got)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, *testOVHCredentials(), got)

	_, statErr := os.Stat(vaultFilePath(dataDir))
	assert.True(t, os.IsNotExist(statErr), "memory-only vault must not create a file")

	// A restart loses it.
	found, err = openTestVault(t, dataDir, nil).GetProviderCredentials("ovh", &got)
	require.NoError(t, err)
	assert.False(t, found)
}

func TestCredentialVault_PassphraseLifecycle(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	v := openTestVault(t, dataDir, nil)

	// Entered before a passphrase exists: saved along with it.
	require.NoError(t, v.PutProviderCredentials("ovh", testOVHCredentials()))
	require.NoError(t, v.SetPassphrase(testVaultPassphrase))
	assert.Equal(t, VaultStatus{State: VaultUnlocked, KeySource: vaultKeySourcePassphrase}, v.Status())

	info, err := os.Stat(vaultFilePath(dataDir))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())

	raw, err := os.ReadFile(vaultFilePath(dataDir))
	require.NoError(t, err)
	for _, secret := range []string{"app-key-plain", "app-secret-plain", "consumer-key-plain", "service-plain", testVaultPassphrase} {
		assert.NotContains(t, string(raw), secret, "vault file must not hold plaintext")
	}

	// Setting it twice is not how a passphrase is changed.
	assert.ErrorIs(t, v.SetPassphrase(testVaultPassphrase), errVaultNotAllowed)

	// After a restart it is locked until the passphrase is entered.
	reopened := openTestVault(t, dataDir, nil)
	assert.Equal(t, VaultLocked, reopened.Status().State)

	var got OVHCredentials
	_, err = reopened.GetProviderCredentials("ovh", &got)
	assert.ErrorIs(t, err, errVaultLocked)
	assert.ErrorIs(t, reopened.PutProviderCredentials("ovh", testOVHCredentials()), errVaultLocked)

	assert.ErrorIs(t, reopened.Unlock("not the passphrase"), errVaultWrongPassphrase)
	assert.Equal(t, VaultLocked, reopened.Status().State)

	unlocked := 0
	reopened.OnUnlock(func() { unlocked++ })
	require.NoError(t, reopened.Unlock(testVaultPassphrase))
	assert.Equal(t, 1, unlocked)
	assert.Equal(t, VaultUnlocked, reopened.Status().State)

	found, err := reopened.GetProviderCredentials("ovh", &got)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, *testOVHCredentials(), got)

	// Lock forgets the key again.
	require.NoError(t, reopened.Lock())
	assert.Equal(t, VaultLocked, reopened.Status().State)
	_, err = reopened.GetProviderCredentials("ovh", &got)
	assert.ErrorIs(t, err, errVaultLocked)
}

func TestCredentialVault_PassphraseTooShort(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	v := openTestVault(t, dataDir, nil)

	assert.ErrorIs(t, v.SetPassphrase("short"), errVaultPassphraseTooShort)
	assert.Equal(t, VaultUninitialized, v.Status().State)
	_, statErr := os.Stat(vaultFilePath(dataDir))
	assert.True(t, os.IsNotExist(statErr))
}

func TestCredentialVault_LockoutAfterWrongPassphrases(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	require.NoError(t, openTestVault(t, dataDir, nil).SetPassphrase(testVaultPassphrase))

	v := openTestVault(t, dataDir, nil)
	clock := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	v.now = func() time.Time { return clock }

	for i := 0; i < vaultMaxFailedUnlocks; i++ {
		require.ErrorIs(t, v.Unlock("wrong passphrase"), errVaultWrongPassphrase)
	}
	// Even the right passphrase waits out the pause.
	assert.ErrorIs(t, v.Unlock(testVaultPassphrase), errVaultLockedOut)

	clock = clock.Add(vaultLockoutDuration + time.Second)
	assert.NoError(t, v.Unlock(testVaultPassphrase))
}

func TestCredentialVault_ChangePassphrase(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	v := openTestVault(t, dataDir, nil)
	require.NoError(t, v.SetPassphrase(testVaultPassphrase))
	require.NoError(t, v.PutProviderCredentials("ovh", testOVHCredentials()))

	const next = "another long passphrase"
	assert.ErrorIs(t, v.ChangePassphrase("not the current one", next), errVaultWrongPassphrase)
	assert.ErrorIs(t, v.ChangePassphrase(testVaultPassphrase, "short"), errVaultPassphraseTooShort)
	require.NoError(t, v.ChangePassphrase(testVaultPassphrase, next))

	reopened := openTestVault(t, dataDir, nil)
	assert.ErrorIs(t, reopened.Unlock(testVaultPassphrase), errVaultWrongPassphrase)
	require.NoError(t, reopened.Unlock(next))

	var got OVHCredentials
	found, err := reopened.GetProviderCredentials("ovh", &got)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, *testOVHCredentials(), got)
}

func TestCredentialVault_EnvKey(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	v := openTestVault(t, dataDir, testEnvKey(1))
	require.NoError(t, v.PutProviderCredentials("ovh", testOVHCredentials()))

	// A passphrase has no place next to an environment key.
	assert.ErrorIs(t, v.SetPassphrase(testVaultPassphrase), errVaultNotAllowed)
	assert.ErrorIs(t, v.Lock(), errVaultNotAllowed)

	raw, err := os.ReadFile(vaultFilePath(dataDir))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "app-secret-plain")

	tests := []struct {
		name      string
		envKey    []byte
		wantState VaultState
	}{
		{name: "same key opens by itself", envKey: testEnvKey(1), wantState: VaultUnlocked},
		{name: "missing key", envKey: nil, wantState: VaultUnreadable},
		{name: "different key", envKey: testEnvKey(2), wantState: VaultUnreadable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reopened := openTestVault(t, dataDir, tt.envKey)
			assert.Equal(t, tt.wantState, reopened.Status().State)

			var got OVHCredentials
			found, err := reopened.GetProviderCredentials("ovh", &got)
			if tt.wantState == VaultUnlocked {
				require.NoError(t, err)
				require.True(t, found)
				assert.Equal(t, *testOVHCredentials(), got)
				return
			}
			assert.ErrorIs(t, err, errVaultUnreadable)
			assert.ErrorIs(t, reopened.PutProviderCredentials("ovh", testOVHCredentials()), errVaultUnreadable)
		})
	}
}

func TestCredentialVault_ResetRecoversUnreadable(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	require.NoError(t, openTestVault(t, dataDir, testEnvKey(1)).PutProviderCredentials("ovh", testOVHCredentials()))

	v := openTestVault(t, dataDir, nil)
	require.Equal(t, VaultUnreadable, v.Status().State)

	require.NoError(t, v.Reset())
	assert.Equal(t, VaultUninitialized, v.Status().State)
	_, statErr := os.Stat(vaultFilePath(dataDir))
	assert.True(t, os.IsNotExist(statErr))

	var got OVHCredentials
	found, err := v.GetProviderCredentials("ovh", &got)
	require.NoError(t, err)
	assert.False(t, found)
}

// A vault saved under a passphrase moves under LAB_DATA_ENCRYPTION_KEY the first
// time it is unlocked once the operator has provided one.
func TestCredentialVault_PassphraseVaultMovesToEnvKey(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	v := openTestVault(t, dataDir, nil)
	require.NoError(t, v.SetPassphrase(testVaultPassphrase))
	require.NoError(t, v.PutProviderCredentials("ovh", testOVHCredentials()))

	withKey := openTestVault(t, dataDir, testEnvKey(1))
	require.Equal(t, VaultLocked, withKey.Status().State)
	require.NoError(t, withKey.Unlock(testVaultPassphrase))
	assert.Equal(t, VaultStatus{State: VaultUnlocked, KeySource: vaultKeySourceEnv}, withKey.Status())

	next := openTestVault(t, dataDir, testEnvKey(1))
	assert.Equal(t, VaultUnlocked, next.Status().State)
	var got OVHCredentials
	found, err := next.GetProviderCredentials("ovh", &got)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, *testOVHCredentials(), got)
}

// Sealed values are bound to their slot: one moved to another slot does not open.
func TestCredentialVault_RejectsSwappedValues(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	v := openTestVault(t, dataDir, testEnvKey(1))
	require.NoError(t, v.PutProviderCredentials("ovh", testOVHCredentials()))
	require.NoError(t, v.PutProviderCredentials("azure", &AzureCredentials{ClientID: "c", ClientSecret: "s", TenantID: "t", SubscriptionID: "sub"}))

	raw, err := os.ReadFile(vaultFilePath(dataDir))
	require.NoError(t, err)
	var f vaultFile
	require.NoError(t, json.Unmarshal(raw, &f))
	f.Providers["ovh"], f.Providers["azure"] = f.Providers["azure"], f.Providers["ovh"]
	tampered, err := json.Marshal(f)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(vaultFilePath(dataDir), tampered, 0600))

	reopened := openTestVault(t, dataDir, testEnvKey(1))
	var got OVHCredentials
	_, err = reopened.GetProviderCredentials("ovh", &got)
	assert.Error(t, err)
}

func TestCredentialVault_DeleteProviderCredentials(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	v := openTestVault(t, dataDir, testEnvKey(1))
	require.NoError(t, v.PutProviderCredentials("ovh", testOVHCredentials()))
	require.NoError(t, v.DeleteProviderCredentials("ovh"))
	require.NoError(t, v.DeleteProviderCredentials("ovh"), "deleting what is not there is not an error")

	var got OVHCredentials
	found, err := openTestVault(t, dataDir, testEnvKey(1)).GetProviderCredentials("ovh", &got)
	require.NoError(t, err)
	assert.False(t, found)
}

func TestSealWithAEAD(t *testing.T) {
	t.Parallel()
	aead, err := newAES256GCM(testEnvKey(7))
	require.NoError(t, err)

	tests := []struct {
		name    string
		value   string
		sealAAD string
		openAAD string
		wantErr bool
	}{
		{name: "round trip", value: "secret", sealAAD: "slot", openAAD: "slot"},
		{name: "empty stays empty", value: "", sealAAD: "slot", openAAD: "slot"},
		{name: "wrong slot", value: "secret", sealAAD: "slot", openAAD: "other", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sealed, err := sealWithAEAD(aead, tt.value, tt.sealAAD)
			require.NoError(t, err)
			if tt.value != "" {
				assert.True(t, strings.HasPrefix(sealed, encPrefix))
			}
			opened, err := openWithAEAD(aead, sealed, tt.openAAD)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.value, opened)
		})
	}

	_, err = openWithAEAD(aead, "plaintext", "slot")
	assert.Error(t, err, "a value that was never sealed must be rejected")
	_, err = newAES256GCM([]byte("too short"))
	assert.Error(t, err)
}

// vaultTestHandler builds a Handler over a vault in dataDir.
func vaultTestHandler(t *testing.T, dataDir string, envKey []byte) (*Handler, *CredentialVault) {
	t.Helper()
	v := openTestVault(t, dataDir, envKey)
	cm := NewCredentialsManager()
	cm.AttachVault(v)
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, cm, nil, nil, nil)
	h.SetCredentialVault(v)
	return h, v
}

func postVaultForm(handler http.HandlerFunc, target string, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

func TestVaultHandlers(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	h, v := vaultTestHandler(t, dataDir, nil)

	// Mismatched confirmation changes nothing.
	rec := postVaultForm(h.SetVaultPassphrase, "/api/credential-vault/passphrase",
		url.Values{"passphrase": {testVaultPassphrase}, "passphrase_confirm": {"something else entirely"}})
	assert.Contains(t, rec.Body.String(), "error-message")
	assert.Equal(t, VaultUninitialized, v.Status().State)

	rec = postVaultForm(h.SetVaultPassphrase, "/api/credential-vault/passphrase",
		url.Values{"passphrase": {"short"}, "passphrase_confirm": {"short"}})
	assert.Contains(t, rec.Body.String(), "at least")
	assert.Equal(t, VaultUninitialized, v.Status().State)

	rec = postVaultForm(h.SetVaultPassphrase, "/api/credential-vault/passphrase",
		url.Values{"passphrase": {testVaultPassphrase}, "passphrase_confirm": {testVaultPassphrase}})
	assert.Equal(t, "true", rec.Header().Get("HX-Refresh"))
	assert.Equal(t, VaultUnlocked, v.Status().State)

	rec = postVaultForm(h.LockVault, "/api/credential-vault/lock", url.Values{})
	assert.Equal(t, "true", rec.Header().Get("HX-Refresh"))
	assert.Equal(t, VaultLocked, v.Status().State)

	rec = postVaultForm(h.UnlockVault, "/api/credential-vault/unlock", url.Values{"passphrase": {"not the passphrase"}})
	assert.Contains(t, rec.Body.String(), "not correct")
	assert.NotContains(t, rec.Body.String(), "not the passphrase", "the attempt must not be echoed")
	assert.Equal(t, VaultLocked, v.Status().State)

	rec = postVaultForm(h.UnlockVault, "/api/credential-vault/unlock", url.Values{"passphrase": {testVaultPassphrase}})
	assert.Equal(t, "true", rec.Header().Get("HX-Refresh"))
	assert.Equal(t, VaultUnlocked, v.Status().State)

	// Changing needs the current passphrase.
	const next = "another long passphrase"
	rec = postVaultForm(h.SetVaultPassphrase, "/api/credential-vault/passphrase",
		url.Values{"current_passphrase": {"wrong current one"}, "passphrase": {next}, "passphrase_confirm": {next}})
	assert.Contains(t, rec.Body.String(), "not correct")
	rec = postVaultForm(h.SetVaultPassphrase, "/api/credential-vault/passphrase",
		url.Values{"current_passphrase": {testVaultPassphrase}, "passphrase": {next}, "passphrase_confirm": {next}})
	assert.Equal(t, "true", rec.Header().Get("HX-Refresh"))

	// Reset needs the typed confirmation.
	rec = postVaultForm(h.ResetVault, "/api/credential-vault/reset", url.Values{"confirm": {"yes"}})
	assert.Contains(t, rec.Body.String(), "error-message")
	assert.Equal(t, VaultUnlocked, v.Status().State)
	rec = postVaultForm(h.ResetVault, "/api/credential-vault/reset", url.Values{"confirm": {vaultResetConfirmation}})
	assert.Equal(t, "true", rec.Header().Get("HX-Refresh"))
	assert.Equal(t, VaultUninitialized, v.Status().State)
}

func TestVaultHandlers_RejectBadRequests(t *testing.T) {
	t.Parallel()
	h, _ := vaultTestHandler(t, t.TempDir(), nil)
	noVault := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

	tests := []struct {
		name    string
		handler http.HandlerFunc
		method  string
		want    int
	}{
		{name: "GET set passphrase", handler: h.SetVaultPassphrase, method: http.MethodGet, want: http.StatusMethodNotAllowed},
		{name: "GET unlock", handler: h.UnlockVault, method: http.MethodGet, want: http.StatusMethodNotAllowed},
		{name: "GET lock", handler: h.LockVault, method: http.MethodGet, want: http.StatusMethodNotAllowed},
		{name: "GET reset", handler: h.ResetVault, method: http.MethodGet, want: http.StatusMethodNotAllowed},
		{name: "no vault wired", handler: noVault.UnlockVault, method: http.MethodPost, want: http.StatusServiceUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			tt.handler(rec, httptest.NewRequest(tt.method, "/", nil))
			assert.Equal(t, tt.want, rec.Code)
		})
	}
}

// The credential storage card renders for every state, on both pages that carry it.
func TestCredentialStoragePartial_RendersEveryState(t *testing.T) {
	t.Chdir("../..") // getTemplate resolves web/ relative to the working directory

	envDir := t.TempDir()
	require.NoError(t, openTestVault(t, envDir, testEnvKey(1)).PutProviderCredentials("ovh", testOVHCredentials()))
	lockedDir := t.TempDir()
	require.NoError(t, openTestVault(t, lockedDir, nil).SetPassphrase(testVaultPassphrase))

	tests := []struct {
		name    string
		dataDir string
		envKey  []byte
		unlock  bool
		want    string
		notWant string
	}{
		{name: "disabled", dataDir: "", want: "No data directory is configured", notWant: "/api/credential-vault/"},
		{name: "uninitialized", dataDir: t.TempDir(), want: "Save credentials encrypted", notWant: "/api/credential-vault/reset"},
		{name: "locked", dataDir: lockedDir, want: "/api/credential-vault/unlock", notWant: "/api/credential-vault/lock"},
		{name: "unlocked by passphrase", dataDir: lockedDir, unlock: true, want: "/api/credential-vault/lock"},
		{name: "unlocked by env key", dataDir: envDir, envKey: testEnvKey(1), want: "LAB_DATA_ENCRYPTION_KEY", notWant: "/api/credential-vault/passphrase"},
		{name: "unreadable", dataDir: envDir, want: "cannot be read", notWant: "/api/credential-vault/unlock"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, v := vaultTestHandler(t, tt.dataDir, tt.envKey)
			if tt.unlock {
				require.NoError(t, v.Unlock(testVaultPassphrase))
			}
			for _, page := range []struct {
				path  string
				serve http.HandlerFunc
			}{
				{"/credentials", h.ServeCredentials},
				{"/admin/dns", h.ServeDNSProfiles},
			} {
				rec := httptest.NewRecorder()
				page.serve(rec, httptest.NewRequest(http.MethodGet, page.path, nil))
				require.Equal(t, http.StatusOK, rec.Code, "%s body: %s", page.path, rec.Body.String())
				body := rec.Body.String()
				assert.Contains(t, body, tt.want, page.path)
				if tt.notWant != "" {
					assert.NotContains(t, body, tt.notWant, page.path)
				}
			}
		})
	}
}
