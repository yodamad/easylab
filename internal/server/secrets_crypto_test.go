package server

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testKey is a fixed 32-byte AES-256 key for deterministic test setup.
var testKey = []byte("0123456789abcdef0123456789abcdef")

func TestInitDataEncryption_KeyValidation(t *testing.T) {
	tests := []struct {
		name    string
		key     []byte
		wantErr bool
	}{
		{"valid 32 bytes", testKey, false},
		{"empty disables", nil, false},
		{"too short", []byte("short"), true},
		{"too long", []byte("0123456789abcdef0123456789abcdef!"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := InitDataEncryption(tt.key)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
	// Leave encryption disabled for other tests in the package.
	require.NoError(t, InitDataEncryption(nil))
}

func TestEncryptDecryptSecret_RoundTrip(t *testing.T) {
	require.NoError(t, InitDataEncryption(testKey))
	defer InitDataEncryption(nil)

	inputs := []string{"hello", "apiVersion: v1\nkind: Config\n", "秘密", strings.Repeat("x", 5000)}
	for _, in := range inputs {
		enc, err := encryptSecret(in)
		require.NoError(t, err)
		assert.True(t, strings.HasPrefix(enc, encPrefix), "ciphertext must carry the version prefix")
		assert.NotContains(t, enc, in, "plaintext must not appear in ciphertext")

		dec, err := decryptSecret(enc)
		require.NoError(t, err)
		assert.Equal(t, in, dec)
	}
}

func TestEncryptSecret_EmptyStaysEmpty(t *testing.T) {
	require.NoError(t, InitDataEncryption(testKey))
	defer InitDataEncryption(nil)

	enc, err := encryptSecret("")
	require.NoError(t, err)
	assert.Equal(t, "", enc)
}

func TestDecryptSecret_PlaintextPassthrough(t *testing.T) {
	require.NoError(t, InitDataEncryption(testKey))
	defer InitDataEncryption(nil)

	// A value with no enc: prefix (e.g. a pre-existing plaintext job file) is
	// returned unchanged.
	got, err := decryptSecret("plain kubeconfig text")
	require.NoError(t, err)
	assert.Equal(t, "plain kubeconfig text", got)
}

func TestEncryptSecret_NoKeyIsPassthrough(t *testing.T) {
	require.NoError(t, InitDataEncryption(nil))

	enc, err := encryptSecret("secret")
	require.NoError(t, err)
	assert.Equal(t, "secret", enc, "with no key configured, encryptSecret must not alter the value")
}

func TestDecryptSecret_Tampered(t *testing.T) {
	require.NoError(t, InitDataEncryption(testKey))
	defer InitDataEncryption(nil)

	enc, err := encryptSecret("secret")
	require.NoError(t, err)

	// Flip a character in the base64 body to force an authentication failure.
	tampered := enc[:len(enc)-2] + "AA"
	_, err = decryptSecret(tampered)
	require.Error(t, err)
}

func TestDeriveEncryptionKey(t *testing.T) {
	key := deriveEncryptionKey("user@example.com", "password123")
	if len(key) != 32 {
		t.Errorf("deriveEncryptionKey() key length = %d, want 32", len(key))
	}

	key2 := deriveEncryptionKey("user@example.com", "password123")
	for i := range key {
		if key[i] != key2[i] {
			t.Error("deriveEncryptionKey() is not deterministic")
			break
		}
	}

	keyDiff := deriveEncryptionKey("other@example.com", "password123")
	same := true
	for i := range key {
		if key[i] != keyDiff[i] {
			same = false
			break
		}
	}
	if same {
		t.Error("deriveEncryptionKey() same key for different emails")
	}
}

func TestEncryptWorkspacePassword(t *testing.T) {
	plaintext := "secret-workspace-pw"
	email := "user@example.com"
	studentPw := "student-pw-123"

	ciphertext, err := encryptWorkspacePassword(plaintext, email, studentPw)
	if err != nil {
		t.Fatalf("encryptWorkspacePassword() error = %v", err)
	}
	if ciphertext == "" {
		t.Error("encryptWorkspacePassword() returned empty ciphertext")
	}
	if ciphertext == plaintext {
		t.Error("encryptWorkspacePassword() ciphertext equals plaintext")
	}

	// Two encryptions of the same value should differ (GCM uses random nonce)
	ciphertext2, err := encryptWorkspacePassword(plaintext, email, studentPw)
	if err != nil {
		t.Fatalf("encryptWorkspacePassword() second call error = %v", err)
	}
	if ciphertext == ciphertext2 {
		t.Error("encryptWorkspacePassword() should produce different ciphertexts (random nonce)")
	}
}
