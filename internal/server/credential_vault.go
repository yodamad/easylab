package server

import (
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// The credential vault keeps cloud provider credentials and saved DNS profiles
// across restarts, encrypted, without ever leaving the key that opens them next
// to the data. That rules out the auto-generated <dataDir>/.encryption_key, which
// protects job files but sits beside them. The vault is written to disk only
// under a key that comes from somewhere else:
//
//   - LAB_DATA_ENCRYPTION_KEY set explicitly in the environment, or
//   - a passphrase an admin sets in the UI, held in memory only and entered
//     again once after each restart.
//
// With neither, the vault still works but only in memory, under a throwaway key
// — the behaviour provider credentials always had.

const (
	credentialVaultFileName = "credential-vault.json"

	vaultKeySourceEnv        = "env"
	vaultKeySourcePassphrase = "passphrase"

	// PBKDF2-HMAC-SHA256 work factor for a passphrase-derived key.
	vaultKDFIterations       = 600_000
	vaultSaltSize            = 16
	vaultMinPassphraseLength = 12

	// A run of wrong passphrases pauses unlocking, so the admin session is not an
	// unlimited guessing oracle.
	vaultMaxFailedUnlocks = 5
	vaultLockoutDuration  = time.Minute

	// Domain-separates the vault key from the data key it is derived from.
	vaultEnvKeyInfo = "easylab/credential-vault/v1"

	vaultCheckPlaintext = "easylab-credential-vault"
	vaultCheckAAD       = "check"

	// What an admin types to confirm wiping the vault.
	vaultResetConfirmation = "RESET"
)

// VaultState is what the credential vault can currently do.
type VaultState string

const (
	// VaultDisabled: no data directory, so nothing can be saved. Memory only.
	VaultDisabled VaultState = "disabled"
	// VaultUninitialized: nothing saved yet and no key to save under. Memory only
	// until a passphrase is set.
	VaultUninitialized VaultState = "uninitialized"
	// VaultLocked: saved under a passphrase that has not been entered since startup.
	VaultLocked VaultState = "locked"
	// VaultUnlocked: saved to disk and readable.
	VaultUnlocked VaultState = "unlocked"
	// VaultUnreadable: a saved vault exists but its key is gone (or the file is
	// damaged). It can only be reset.
	VaultUnreadable VaultState = "unreadable"
)

var (
	errVaultLocked             = errors.New("credential storage is locked")
	errVaultUnreadable         = errors.New("credential storage cannot be read")
	errVaultWrongPassphrase    = errors.New("wrong passphrase")
	errVaultLockedOut          = errors.New("too many wrong passphrases")
	errVaultPassphraseTooShort = errors.New("passphrase is too short")
	errVaultNotAllowed         = errors.New("not available in the current credential storage state")
)

// vaultFile is the on-disk form. Everything secret in it is sealed; names,
// providers and zones stay readable so a locked vault can still say what it holds.
type vaultFile struct {
	Version     int               `json:"version"`
	KeySource   string            `json:"key_source"`
	Salt        string            `json:"salt,omitempty"`
	Iterations  int               `json:"iterations,omitempty"`
	Check       string            `json:"check"`
	Providers   map[string]string `json:"providers,omitempty"`
	DNSProfiles []vaultDNSProfile `json:"dns_profiles,omitempty"`
}

// vaultDNSProfile is a saved DNS profile with its credential values sealed.
type vaultDNSProfile struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Provider    string            `json:"provider"`
	Zone        string            `json:"zone,omitempty"`
	UpdatedAt   time.Time         `json:"updated_at"`
	Credentials map[string]string `json:"credentials,omitempty"`
}

// vaultContents is everything a key change has to swap as one unit, so a failed
// write can put the previous state back.
type vaultContents struct {
	aead       cipher.AEAD // nil while locked or unreadable
	persisted  bool        // written to disk (false: memory only, throwaway key)
	keySource  string
	salt       []byte
	iterations int
	check      string
	providers  map[string]string
	profiles   []vaultDNSProfile
}

// CredentialVault stores provider credentials and DNS profiles, sealed.
type CredentialVault struct {
	mu sync.RWMutex
	vaultContents

	dataDir    string
	envKey     []byte // vault key derived from an explicit LAB_DATA_ENCRYPTION_KEY; nil without one
	unreadable bool

	failedUnlocks int
	lockedUntil   time.Time

	// kdfIterations and now are fixed in production; tests lower the former and
	// drive the latter.
	kdfIterations int
	now           func() time.Time

	onUnlock []func()
}

// VaultStatus is a snapshot of the vault for display.
type VaultStatus struct {
	State     VaultState
	KeySource string
}

// NewCredentialVault opens the vault under dataDir. envDataKey is the data
// encryption key only when the operator set it explicitly in the environment —
// never the auto-generated one — and nil otherwise.
func NewCredentialVault(dataDir string, envDataKey []byte) *CredentialVault {
	v := &CredentialVault{
		dataDir:       dataDir,
		kdfIterations: vaultKDFIterations,
		now:           time.Now,
	}
	if len(envDataKey) > 0 && dataDir != "" {
		key, err := hkdf.Key(sha256.New, envDataKey, nil, vaultEnvKeyInfo, 32)
		if err != nil {
			log.Printf("[VAULT] Warning: failed to derive vault key: %v", err)
		} else {
			v.envKey = key
		}
	}
	if err := v.open(); err != nil {
		log.Printf("[VAULT] Warning: saved credentials cannot be read: %v", err)
		v.unreadable = true
		v.vaultContents = vaultContents{}
	}
	return v
}

func (v *CredentialVault) filePath() string {
	return filepath.Join(v.dataDir, credentialVaultFileName)
}

// open loads the saved vault, or starts an empty one under the best key available.
func (v *CredentialVault) open() error {
	if v.dataDir == "" {
		return v.startEmpty()
	}
	data, err := os.ReadFile(v.filePath())
	if err != nil {
		if os.IsNotExist(err) {
			return v.startEmpty()
		}
		return fmt.Errorf("failed to read credential vault: %w", err)
	}
	var f vaultFile
	if err := json.Unmarshal(data, &f); err != nil {
		return fmt.Errorf("failed to parse credential vault: %w", err)
	}

	contents := vaultContents{
		persisted: true,
		keySource: f.KeySource,
		check:     f.Check,
		providers: f.Providers,
		profiles:  f.DNSProfiles,
	}
	if contents.providers == nil {
		contents.providers = make(map[string]string)
	}

	switch f.KeySource {
	case vaultKeySourceEnv:
		if v.envKey == nil {
			return fmt.Errorf("credential vault was saved under LAB_DATA_ENCRYPTION_KEY, which is not set")
		}
		aead, err := newAES256GCM(v.envKey)
		if err != nil {
			return err
		}
		if _, err := openWithAEAD(aead, f.Check, vaultCheckAAD); err != nil {
			return fmt.Errorf("credential vault was saved under a different LAB_DATA_ENCRYPTION_KEY")
		}
		contents.aead = aead
	case vaultKeySourcePassphrase:
		salt, err := base64.StdEncoding.DecodeString(f.Salt)
		if err != nil || len(salt) == 0 || f.Iterations <= 0 {
			return fmt.Errorf("credential vault has invalid passphrase parameters")
		}
		contents.salt = salt
		contents.iterations = f.Iterations
		// Stays locked until Unlock.
	default:
		return fmt.Errorf("credential vault has unknown key source %q", f.KeySource)
	}

	v.vaultContents = contents
	return nil
}

// startEmpty begins an empty vault: saved under the environment key when there
// is one, otherwise in memory under a throwaway key.
func (v *CredentialVault) startEmpty() error {
	contents := vaultContents{providers: make(map[string]string)}
	key := v.envKey
	if key != nil {
		contents.persisted = true
		contents.keySource = vaultKeySourceEnv
	} else {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return fmt.Errorf("failed to generate session key: %w", err)
		}
	}
	aead, err := newAES256GCM(key)
	if err != nil {
		return err
	}
	check, err := sealWithAEAD(aead, vaultCheckPlaintext, vaultCheckAAD)
	if err != nil {
		return err
	}
	contents.aead = aead
	contents.check = check
	v.vaultContents = contents
	return nil
}

func (v *CredentialVault) stateLocked() VaultState {
	switch {
	case v.unreadable:
		return VaultUnreadable
	case v.aead == nil:
		return VaultLocked
	case v.persisted:
		return VaultUnlocked
	case v.dataDir == "":
		return VaultDisabled
	default:
		return VaultUninitialized
	}
}

// Status reports the vault's current state.
func (v *CredentialVault) Status() VaultStatus {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return VaultStatus{State: v.stateLocked(), KeySource: v.keySource}
}

// usableLocked reports whether values can be sealed and opened right now.
func (v *CredentialVault) usableLocked() error {
	if v.unreadable {
		return errVaultUnreadable
	}
	if v.aead == nil {
		return errVaultLocked
	}
	return nil
}

// saveLocked writes the vault to disk when it is one that is kept on disk.
func (v *CredentialVault) saveLocked() error {
	if !v.persisted {
		return nil
	}
	f := vaultFile{
		Version:     1,
		KeySource:   v.keySource,
		Iterations:  v.iterations,
		Check:       v.check,
		Providers:   v.providers,
		DNSProfiles: v.profiles,
	}
	if len(v.salt) > 0 {
		f.Salt = base64.StdEncoding.EncodeToString(v.salt)
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal credential vault: %w", err)
	}
	if err := os.MkdirAll(v.dataDir, 0755); err != nil {
		return fmt.Errorf("failed to create data dir: %w", err)
	}
	// Write-then-rename so a crash mid-write cannot leave a truncated vault.
	tmp := v.filePath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("failed to write credential vault: %w", err)
	}
	if err := os.Rename(tmp, v.filePath()); err != nil {
		return fmt.Errorf("failed to replace credential vault: %w", err)
	}
	return nil
}

func vaultProviderAAD(provider string) string { return "provider:" + provider }

func vaultDNSFieldAAD(profileID, field string) string { return "dns:" + profileID + ":" + field }

// rewrapLocked returns the current contents re-sealed under aead. The receiver
// is not modified, so the caller can still fall back to what it had.
func (v *CredentialVault) rewrapLocked(aead cipher.AEAD) (vaultContents, error) {
	out := vaultContents{
		aead:      aead,
		providers: make(map[string]string, len(v.providers)),
		profiles:  make([]vaultDNSProfile, 0, len(v.profiles)),
	}
	reseal := func(value, aad string) (string, error) {
		plain, err := openWithAEAD(v.aead, value, aad)
		if err != nil {
			return "", err
		}
		return sealWithAEAD(aead, plain, aad)
	}
	for name, value := range v.providers {
		sealed, err := reseal(value, vaultProviderAAD(name))
		if err != nil {
			return vaultContents{}, fmt.Errorf("failed to re-encrypt %s credentials: %w", name, err)
		}
		out.providers[name] = sealed
	}
	for _, p := range v.profiles {
		cp := p
		cp.Credentials = make(map[string]string, len(p.Credentials))
		for field, value := range p.Credentials {
			sealed, err := reseal(value, vaultDNSFieldAAD(p.ID, field))
			if err != nil {
				return vaultContents{}, fmt.Errorf("failed to re-encrypt DNS profile %q: %w", p.Name, err)
			}
			cp.Credentials[field] = sealed
		}
		out.profiles = append(out.profiles, cp)
	}
	check, err := sealWithAEAD(aead, vaultCheckPlaintext, vaultCheckAAD)
	if err != nil {
		return vaultContents{}, err
	}
	out.check = check
	return out, nil
}

func (v *CredentialVault) passphraseAEAD(passphrase string, salt []byte, iterations int) (cipher.AEAD, error) {
	key, err := pbkdf2.Key(sha256.New, passphrase, salt, iterations, 32)
	if err != nil {
		return nil, fmt.Errorf("failed to derive key: %w", err)
	}
	return newAES256GCM(key)
}

// rekeyToPassphraseLocked re-seals the contents under a key derived from
// passphrase with a fresh salt, and saves them.
func (v *CredentialVault) rekeyToPassphraseLocked(passphrase string) error {
	if len(passphrase) < vaultMinPassphraseLength {
		return errVaultPassphraseTooShort
	}
	salt := make([]byte, vaultSaltSize)
	if _, err := rand.Read(salt); err != nil {
		return fmt.Errorf("failed to generate salt: %w", err)
	}
	aead, err := v.passphraseAEAD(passphrase, salt, v.kdfIterations)
	if err != nil {
		return err
	}
	next, err := v.rewrapLocked(aead)
	if err != nil {
		return err
	}
	next.persisted = true
	next.keySource = vaultKeySourcePassphrase
	next.salt = salt
	next.iterations = v.kdfIterations

	previous := v.vaultContents
	v.vaultContents = next
	if err := v.saveLocked(); err != nil {
		v.vaultContents = previous
		return err
	}
	return nil
}

// SetPassphrase starts saving the vault to disk under a passphrase. Whatever it
// already holds in memory is saved with it.
func (v *CredentialVault) SetPassphrase(passphrase string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.stateLocked() != VaultUninitialized {
		return errVaultNotAllowed
	}
	return v.rekeyToPassphraseLocked(passphrase)
}

// ChangePassphrase re-encrypts an unlocked, passphrase-protected vault under a new one.
func (v *CredentialVault) ChangePassphrase(current, next string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.stateLocked() != VaultUnlocked || v.keySource != vaultKeySourcePassphrase {
		return errVaultNotAllowed
	}
	if _, err := v.verifyPassphraseLocked(current); err != nil {
		return err
	}
	return v.rekeyToPassphraseLocked(next)
}

// verifyPassphraseLocked checks passphrase against the saved vault and returns
// the AEAD it opens. Wrong guesses count towards a temporary lockout.
func (v *CredentialVault) verifyPassphraseLocked(passphrase string) (cipher.AEAD, error) {
	if v.now().Before(v.lockedUntil) {
		return nil, errVaultLockedOut
	}
	aead, err := v.passphraseAEAD(passphrase, v.salt, v.iterations)
	if err != nil {
		return nil, err
	}
	if _, err := openWithAEAD(aead, v.check, vaultCheckAAD); err != nil {
		v.failedUnlocks++
		if v.failedUnlocks >= vaultMaxFailedUnlocks {
			v.failedUnlocks = 0
			v.lockedUntil = v.now().Add(vaultLockoutDuration)
		}
		return nil, errVaultWrongPassphrase
	}
	v.failedUnlocks = 0
	return aead, nil
}

// Unlock opens a passphrase-protected vault for the rest of this process's life.
func (v *CredentialVault) Unlock(passphrase string) error {
	v.mu.Lock()
	if v.stateLocked() != VaultLocked {
		v.mu.Unlock()
		return errVaultNotAllowed
	}
	aead, err := v.verifyPassphraseLocked(passphrase)
	if err != nil {
		v.mu.Unlock()
		return err
	}
	v.aead = aead

	// An operator who has since provided LAB_DATA_ENCRYPTION_KEY wants the vault to
	// open by itself from now on: move it under that key. Failing to is not fatal —
	// the vault is open either way.
	if v.envKey != nil {
		if err := v.rekeyToEnvLocked(); err != nil {
			log.Printf("[VAULT] Warning: could not move saved credentials under LAB_DATA_ENCRYPTION_KEY: %v", err)
		}
	}
	callbacks := append([]func(){}, v.onUnlock...)
	v.mu.Unlock()

	for _, fn := range callbacks {
		fn()
	}
	return nil
}

func (v *CredentialVault) rekeyToEnvLocked() error {
	aead, err := newAES256GCM(v.envKey)
	if err != nil {
		return err
	}
	next, err := v.rewrapLocked(aead)
	if err != nil {
		return err
	}
	next.persisted = true
	next.keySource = vaultKeySourceEnv

	previous := v.vaultContents
	v.vaultContents = next
	if err := v.saveLocked(); err != nil {
		v.vaultContents = previous
		return err
	}
	return nil
}

// Lock forgets the passphrase-derived key. Saved DNS profiles become unusable
// until the next Unlock; provider credentials already loaded into the
// CredentialsManager stay in memory until the process restarts.
func (v *CredentialVault) Lock() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.stateLocked() != VaultUnlocked || v.keySource != vaultKeySourcePassphrase {
		return errVaultNotAllowed
	}
	v.aead = nil
	return nil
}

// Reset deletes everything the vault holds, on disk and in memory, and starts
// an empty one. It is the only way out of a forgotten passphrase.
func (v *CredentialVault) Reset() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.dataDir != "" {
		if err := os.Remove(v.filePath()); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to delete credential vault: %w", err)
		}
	}
	if err := v.startEmpty(); err != nil {
		return err
	}
	v.unreadable = false
	v.failedUnlocks = 0
	v.lockedUntil = time.Time{}
	return nil
}

// OnUnlock registers fn to run after each successful Unlock.
func (v *CredentialVault) OnUnlock(fn func()) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.onUnlock = append(v.onUnlock, fn)
}

// PutProviderCredentials seals creds (any JSON-serialisable value) under provider.
func (v *CredentialVault) PutProviderCredentials(provider string, creds any) error {
	data, err := json.Marshal(creds)
	if err != nil {
		return fmt.Errorf("failed to marshal %s credentials: %w", provider, err)
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if err := v.usableLocked(); err != nil {
		return err
	}
	sealed, err := sealWithAEAD(v.aead, string(data), vaultProviderAAD(provider))
	if err != nil {
		return err
	}
	previous, had := v.providers[provider]
	v.providers[provider] = sealed
	if err := v.saveLocked(); err != nil {
		if had {
			v.providers[provider] = previous
		} else {
			delete(v.providers, provider)
		}
		return err
	}
	return nil
}

// GetProviderCredentials opens the credentials saved for provider into out. It
// reports false when none are saved.
func (v *CredentialVault) GetProviderCredentials(provider string, out any) (bool, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if err := v.usableLocked(); err != nil {
		return false, err
	}
	sealed, ok := v.providers[provider]
	if !ok {
		return false, nil
	}
	plain, err := openWithAEAD(v.aead, sealed, vaultProviderAAD(provider))
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal([]byte(plain), out); err != nil {
		return false, fmt.Errorf("failed to parse %s credentials: %w", provider, err)
	}
	return true, nil
}

// DeleteProviderCredentials removes the credentials saved for provider.
func (v *CredentialVault) DeleteProviderCredentials(provider string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if err := v.usableLocked(); err != nil {
		return err
	}
	previous, had := v.providers[provider]
	if !had {
		return nil
	}
	delete(v.providers, provider)
	if err := v.saveLocked(); err != nil {
		v.providers[provider] = previous
		return err
	}
	return nil
}

// SetCredentialVault wires the credential vault. Not required — without it
// provider credentials stay in memory and DNS profiles are unavailable (tests).
func (h *Handler) SetCredentialVault(v *CredentialVault) {
	h.credentialVault = v
}

// credentialStorageView is what the "credential-storage" partial renders.
type credentialStorageView struct {
	State               VaultState
	KeySource           string
	MinPassphraseLength int
	ResetConfirmation   string
}

func (h *Handler) credentialStorageView() credentialStorageView {
	view := credentialStorageView{
		State:               VaultDisabled,
		MinPassphraseLength: vaultMinPassphraseLength,
		ResetConfirmation:   vaultResetConfirmation,
	}
	if h.credentialVault != nil {
		status := h.credentialVault.Status()
		view.State = status.State
		view.KeySource = status.KeySource
	}
	return view
}

// vaultRequest runs the checks every credential-storage action shares and
// reports whether the handler should go on.
func (h *Handler) vaultRequest(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	if h.credentialVault == nil {
		http.Error(w, "Credential storage not available", http.StatusServiceUnavailable)
		return false
	}
	if err := h.parseForm(w, r, maxUploadSizeSmall); err != nil {
		log.Printf("Credential storage - failed to parse form: %v", err)
		h.renderHTMLError(w, "Failed to Parse Form", "Failed to parse form data, please try again.")
		return false
	}
	return true
}

// vaultActionDone answers a successful credential-storage action: the page is
// reloaded so every section reflects the new state.
func vaultActionDone(w http.ResponseWriter, r *http.Request, message string) {
	if isHTMXRequest(r) {
		w.Header().Set("HX-Refresh", "true")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<div class="success-message"><p>%s</p></div>`, escapeHTML(message))
		return
	}
	http.Redirect(w, r, "/admin/dns", http.StatusSeeOther)
}

// vaultErrorMessage turns a vault error into text that is safe to show.
func vaultErrorMessage(err error) string {
	switch {
	case errors.Is(err, errVaultWrongPassphrase):
		return "That passphrase is not correct."
	case errors.Is(err, errVaultLockedOut):
		return "Too many wrong passphrases. Wait a minute, then try again."
	case errors.Is(err, errVaultPassphraseTooShort):
		return fmt.Sprintf("Use a passphrase of at least %d characters.", vaultMinPassphraseLength)
	case errors.Is(err, errVaultLocked):
		return "Credential storage is locked. Unlock it with the passphrase first."
	case errors.Is(err, errVaultUnreadable):
		return "Saved credentials cannot be read. Restore the original LAB_DATA_ENCRYPTION_KEY, or reset credential storage."
	case errors.Is(err, errVaultNotAllowed):
		return "That is not available right now. Reload the page and try again."
	default:
		return "Something went wrong. Check the server logs for details."
	}
}

// SetVaultPassphrase sets the passphrase of a vault that has none, or changes
// it when the current one is supplied.
func (h *Handler) SetVaultPassphrase(w http.ResponseWriter, r *http.Request) {
	if !h.vaultRequest(w, r) {
		return
	}
	passphrase := getFormValue(r, "passphrase")
	if passphrase != getFormValue(r, "passphrase_confirm") {
		h.renderHTMLError(w, "Passphrases Do Not Match", "Type the same passphrase in both fields.")
		return
	}

	var err error
	action := "credential_vault.set_passphrase"
	if h.credentialVault.Status().State == VaultUnlocked {
		action = "credential_vault.change_passphrase"
		err = h.credentialVault.ChangePassphrase(getFormValue(r, "current_passphrase"), passphrase)
	} else {
		err = h.credentialVault.SetPassphrase(passphrase)
	}
	if err != nil {
		log.Printf("Credential storage - failed to set passphrase: %v", err)
		h.renderHTMLError(w, "Passphrase Not Saved", vaultErrorMessage(err))
		return
	}
	h.recordAudit(adminActor(r), "admin", action, "", "")
	vaultActionDone(w, r, "Passphrase saved")
}

// UnlockVault opens a passphrase-protected vault.
func (h *Handler) UnlockVault(w http.ResponseWriter, r *http.Request) {
	if !h.vaultRequest(w, r) {
		return
	}
	if err := h.credentialVault.Unlock(getFormValue(r, "passphrase")); err != nil {
		log.Printf("Credential storage - unlock failed: %v", err)
		h.recordAudit(adminActor(r), "admin", "credential_vault.unlock_failed", "", "")
		h.renderHTMLError(w, "Not Unlocked", vaultErrorMessage(err))
		return
	}
	h.recordAudit(adminActor(r), "admin", "credential_vault.unlock", "", "")
	vaultActionDone(w, r, "Credential storage unlocked")
}

// LockVault forgets the passphrase-derived key until the next unlock.
func (h *Handler) LockVault(w http.ResponseWriter, r *http.Request) {
	if !h.vaultRequest(w, r) {
		return
	}
	if err := h.credentialVault.Lock(); err != nil {
		log.Printf("Credential storage - lock failed: %v", err)
		h.renderHTMLError(w, "Not Locked", vaultErrorMessage(err))
		return
	}
	h.recordAudit(adminActor(r), "admin", "credential_vault.lock", "", "")
	vaultActionDone(w, r, "Credential storage locked")
}

// ResetVault wipes every saved credential and DNS profile.
func (h *Handler) ResetVault(w http.ResponseWriter, r *http.Request) {
	if !h.vaultRequest(w, r) {
		return
	}
	if getFormValue(r, "confirm") != vaultResetConfirmation {
		h.renderHTMLError(w, "Not Reset", fmt.Sprintf("Type %s to confirm.", vaultResetConfirmation))
		return
	}
	if err := h.credentialVault.Reset(); err != nil {
		log.Printf("Credential storage - reset failed: %v", err)
		h.renderHTMLError(w, "Not Reset", vaultErrorMessage(err))
		return
	}
	h.recordAudit(adminActor(r), "admin", "credential_vault.reset", "", "")
	vaultActionDone(w, r, "Credential storage reset")
}
