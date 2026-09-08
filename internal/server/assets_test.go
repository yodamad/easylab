package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// staticFixture builds a throwaway web/static tree and chdirs into its root, so the
// relative paths assetFingerprint and ServeStatic both use resolve against it.
func staticFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, staticDir), 0o755))
	for name, content := range files {
		require.NoError(t, os.WriteFile(filepath.Join(root, staticDir, name), []byte(content), 0o644))
	}
	t.Chdir(root)

	// The memo is package-level and shared across tests; clear it so a fixture file
	// is never mistaken for a same-named one from another test.
	assetFingerprintsMu.Lock()
	assetFingerprints = map[string]assetEntry{}
	assetFingerprintsMu.Unlock()

	return root
}

func TestAssetFingerprint(t *testing.T) {
	tests := []struct {
		name     string
		files    map[string]string
		asset    string
		wantHash bool
	}{
		{
			name:     "hashes an existing file",
			files:    map[string]string{"style.css": "body{}"},
			asset:    "style.css",
			wantHash: true,
		},
		{
			name:  "missing file yields no fingerprint",
			files: map[string]string{},
			asset: "gone.css",
		},
		{
			name:  "directory yields no fingerprint",
			files: map[string]string{},
			asset: ".",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			staticFixture(t, tt.files)

			got := assetFingerprint(tt.asset)
			if !tt.wantHash {
				assert.Empty(t, got)
				return
			}
			assert.Len(t, got, 10)
			assert.Regexp(t, regexp.MustCompile(`^[0-9a-f]{10}$`), got)
			assert.Equal(t, got, assetFingerprint(tt.asset), "fingerprint must be stable across calls")
		})
	}
}

func TestAssetFingerprint_ChangesWithContent(t *testing.T) {
	root := staticFixture(t, map[string]string{"app.js": "console.log(1)"})
	path := filepath.Join(root, staticDir, "app.js")

	before := assetFingerprint("app.js")
	require.NotEmpty(t, before)

	// Rewrite with different content and a different mtime, which is what a redeploy
	// (or a `make dev` edit) looks like — the memo must not keep serving the old hash.
	require.NoError(t, os.WriteFile(path, []byte("console.log(2)"), 0o644))
	require.NoError(t, os.Chtimes(path, time.Now().Add(time.Hour), time.Now().Add(time.Hour)))

	assert.NotEqual(t, before, assetFingerprint("app.js"))
}

func TestAssetFingerprint_SameContentSameHash(t *testing.T) {
	staticFixture(t, map[string]string{"a.css": "same", "b.css": "same"})

	assert.Equal(t, assetFingerprint("a.css"), assetFingerprint("b.css"),
		"identical bytes must hash alike so an unchanged asset keeps its cached URL across deploys")
}

func TestAssetURL(t *testing.T) {
	staticFixture(t, map[string]string{"style.css": "body{}"})

	t.Run("fingerprints a known asset", func(t *testing.T) {
		got := assetURL("/static/style.css")
		assert.Regexp(t, regexp.MustCompile(`^/static/style\.css\?v=[0-9a-f]{10}$`), got)
	})

	t.Run("falls back to the build version when unreadable", func(t *testing.T) {
		assert.Equal(t, "/static/gone.js?v="+Version, assetURL("/static/gone.js"))
	})
}

func TestServeStatic_CacheHeaders(t *testing.T) {
	staticFixture(t, map[string]string{"style.css": "body{color:red}"})
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

	fingerprint := assetFingerprint("style.css")
	require.NotEmpty(t, fingerprint)
	wantETag := `"` + fingerprint + `"`

	tests := []struct {
		name             string
		url              string
		wantCacheControl string
	}{
		{
			name:             "fingerprinted URL is immutable",
			url:              "/static/style.css?v=" + fingerprint,
			wantCacheControl: "public, max-age=31536000, immutable",
		},
		{
			name:             "bare URL must revalidate",
			url:              "/static/style.css",
			wantCacheControl: "no-cache",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.ServeStatic(w, httptest.NewRequest(http.MethodGet, tt.url, nil))

			require.Equal(t, http.StatusOK, w.Code)
			assert.Equal(t, tt.wantCacheControl, w.Header().Get("Cache-Control"))
			assert.Equal(t, wantETag, w.Header().Get("ETag"))
			assert.Equal(t, "text/css", w.Header().Get("Content-Type"))
		})
	}
}

func TestServeStatic_ConditionalGET(t *testing.T) {
	staticFixture(t, map[string]string{"app.js": "console.log(1)"})
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

	etag := `"` + assetFingerprint("app.js") + `"`

	t.Run("matching ETag returns 304", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/static/app.js", nil)
		req.Header.Set("If-None-Match", etag)
		w := httptest.NewRecorder()

		h.ServeStatic(w, req)

		assert.Equal(t, http.StatusNotModified, w.Code)
		assert.Empty(t, w.Body.String())
	})

	t.Run("stale ETag returns the body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/static/app.js", nil)
		req.Header.Set("If-None-Match", `"0000000000"`)
		w := httptest.NewRecorder()

		h.ServeStatic(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "console.log(1)", w.Body.String())
	})
}
