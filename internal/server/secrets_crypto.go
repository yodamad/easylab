package server

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"strings"
	"sync"
)

// encPrefix marks a value that has been encrypted at rest by encryptSecret. The
// version tag lets the scheme evolve without ambiguity against pre-existing
// plaintext values.
const encPrefix = "enc:v1:"

var (
	dataAEAD   cipher.AEAD
	dataAEADMu sync.RWMutex
)

// InitDataEncryption configures the package-level AEAD used to encrypt secret
// fields at rest (kubeconfigs, DNS credentials) in persisted job files.
//
// key must be exactly 32 bytes (AES-256). Passing a nil/empty key disables
// encryption, in which case encryptSecret/decryptSecret are pass-throughs — used
// when persistence is disabled and by unit tests.
func InitDataEncryption(key []byte) error {
	if len(key) == 0 {
		dataAEADMu.Lock()
		dataAEAD = nil
		dataAEADMu.Unlock()
		return nil
	}
	if len(key) != 32 {
		return fmt.Errorf("data encryption key must be 32 bytes (AES-256), got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return fmt.Errorf("failed to create AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return fmt.Errorf("failed to create GCM: %w", err)
	}
	dataAEADMu.Lock()
	dataAEAD = gcm
	dataAEADMu.Unlock()
	return nil
}

func currentAEAD() cipher.AEAD {
	dataAEADMu.RLock()
	defer dataAEADMu.RUnlock()
	return dataAEAD
}

// encryptSecret encrypts s with the configured AEAD, returning
// encPrefix + base64(nonce‖ciphertext). Empty input returns empty output (so
// omitempty fields stay omitted). When no key is configured it returns s
// unchanged.
func encryptSecret(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	aead := currentAEAD()
	if aead == nil {
		return s, nil
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("failed to generate nonce: %w", err)
	}
	ciphertext := aead.Seal(nonce, nonce, []byte(s), nil)
	return encPrefix + base64.StdEncoding.EncodeToString(ciphertext), nil
}

// decryptSecret reverses encryptSecret. Values without the encPrefix are returned
// unchanged — this keeps pre-existing plaintext job files loadable and makes the
// call safe to apply unconditionally.
func decryptSecret(s string) (string, error) {
	body, ok := strings.CutPrefix(s, encPrefix)
	if !ok {
		return s, nil
	}
	aead := currentAEAD()
	if aead == nil {
		return "", fmt.Errorf("encrypted value present but no data encryption key is configured")
	}
	raw, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		return "", fmt.Errorf("failed to decode encrypted value: %w", err)
	}
	if len(raw) < aead.NonceSize() {
		return "", fmt.Errorf("encrypted value too short")
	}
	nonce, ciphertext := raw[:aead.NonceSize()], raw[aead.NonceSize():]
	plaintext, err := aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("failed to decrypt value: %w", err)
	}
	return string(plaintext), nil
}
