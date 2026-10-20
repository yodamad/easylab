package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func decodeSegment(t *testing.T, seg string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(seg)
	require.NoError(t, err, "segment %q must be unpadded base64url", seg)
	return b
}

func TestOpenChamberSessionToken(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		secret string
		ttl    time.Duration
	}{
		{name: "plain secret", secret: "s3cr3t-value", ttl: openChamberSessionTTL},
		// A hex-looking secret must sign as its UTF-8 bytes, not be hex-decoded.
		{name: "hex-looking secret is not decoded", secret: "deadbeefcafebabe0123456789abcdef", ttl: openChamberSessionTTL},
		{name: "short ttl", secret: "k", ttl: time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			token, err := openChamberSessionToken(tt.secret, now, tt.ttl)
			require.NoError(t, err)

			parts := strings.Split(token, ".")
			require.Len(t, parts, 3)
			for _, p := range parts {
				assert.NotContains(t, p, "=", "segments must be unpadded")
				assert.NotContains(t, p, "+")
				assert.NotContains(t, p, "/")
			}

			assert.Equal(t, `{"alg":"HS256"}`, string(decodeSegment(t, parts[0])))

			var claims struct {
				Type string `json:"type"`
				Iat  int64  `json:"iat"`
				Exp  int64  `json:"exp"`
			}
			require.NoError(t, json.Unmarshal(decodeSegment(t, parts[1]), &claims))
			assert.Equal(t, "ui-session", claims.Type)
			assert.Equal(t, now.Unix(), claims.Iat)
			assert.Equal(t, now.Add(tt.ttl).Unix(), claims.Exp)

			mac := hmac.New(sha256.New, []byte(tt.secret))
			mac.Write([]byte(parts[0] + "." + parts[1]))
			assert.Equal(t, mac.Sum(nil), decodeSegment(t, parts[2]),
				"signature must be HMAC-SHA256 over the raw secret bytes")
		})
	}
}

func TestOpenChamberSessionToken_DifferentSecretsDiffer(t *testing.T) {
	t.Parallel()

	now := time.Now()
	a, err := openChamberSessionToken("secret-a", now, time.Hour)
	require.NoError(t, err)
	b, err := openChamberSessionToken("secret-b", now, time.Hour)
	require.NoError(t, err)
	assert.NotEqual(t, a, b)
}

func TestOpenChamberSessionToken_EmptySecret(t *testing.T) {
	t.Parallel()

	for _, secret := range []string{"", " ", "\t\n  "} {
		token, err := openChamberSessionToken(secret, time.Now(), time.Hour)
		require.Error(t, err, "secret %q", secret)
		assert.Empty(t, token)
	}
}

func TestOpenChamberCookieDomain(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		reqHost    string
		wsHost     string
		wantDomain string
		wantOK     bool
	}{
		{"portal subdomain of the lab domain", "lab.example.com", "alice.example.com", "example.com", true},
		{"request host is the lab domain itself", "example.com", "alice.example.com", "example.com", true},
		{"unrelated request host", "easylab.internal", "alice.example.com", "", false},
		{"lookalike suffix is not a subdomain", "badexample.com", "alice.example.com", "", false},
		{"workspace host without a dot", "lab.example.com", "alice", "", false},
		{"single-label lab domain", "lab.intranet", "alice.intranet", "", false},
		{"port on request host", "lab.example.com:8443", "alice.example.com", "example.com", true},
		{"port on workspace host", "lab.example.com", "alice.example.com:8443", "example.com", true},
		{"ports on both", "lab.example.com:8443", "alice.example.com:443", "example.com", true},
		{"case is normalized", "LAB.Example.COM", "Alice.EXAMPLE.com", "example.com", true},
		{"nip.io fallback", "lab.10.0.0.1.nip.io", "alice.10.0.0.1.nip.io", "10.0.0.1.nip.io", true},
		{"empty request host", "", "alice.example.com", "", false},
		{"empty workspace host", "lab.example.com", "", "", false},
		{"workspace host with leading dot", "lab.example.com", ".example.com", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			domain, ok := openChamberCookieDomain(tt.reqHost, tt.wsHost)

			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.wantDomain, domain)
		})
	}
}

func TestHostWithoutPort(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in, want string
	}{
		{"example.com", "example.com"},
		{"Example.COM:8080", "example.com"},
		{"  example.com  ", "example.com"},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, hostWithoutPort(tt.in))
		})
	}
}
