package server

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// staticURLPrefix is the URL path all fingerprinted assets are served under, and
// staticDir is where ServeStatic reads them from. Both must stay in sync with the
// route registered in cmd/server/main.go and with ServeStatic itself.
const (
	staticURLPrefix = "/static/"
	staticDir       = "web/static"
)

// assetEntry is one memoised fingerprint plus the file identity it was computed
// from. modTime/size act as a cheap invalidation key so `make dev` picks up an
// edited CSS/JS file without a restart, while steady-state requests never re-hash.
type assetEntry struct {
	fingerprint string
	modTimeUnix int64
	size        int64
}

var (
	assetFingerprintsMu sync.RWMutex
	assetFingerprints   = map[string]assetEntry{}
)

// assetFingerprint returns a short content hash for a file under web/static, or an
// empty string when it cannot be read. Callers treat the empty string as "fall back
// to the build version" rather than as an error: a missing fingerprint must never
// stop a page from rendering.
func assetFingerprint(name string) string {
	path := filepath.Join(staticDir, filepath.Clean(name))

	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	modTime, size := info.ModTime().UnixNano(), info.Size()

	assetFingerprintsMu.RLock()
	cached, ok := assetFingerprints[name]
	assetFingerprintsMu.RUnlock()
	if ok && cached.modTimeUnix == modTime && cached.size == size {
		return cached.fingerprint
	}

	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()

	sum := sha256.New()
	if _, err := io.Copy(sum, file); err != nil {
		return ""
	}
	// 10 hex chars (40 bits) is far more than enough to distinguish successive
	// builds of the same file, and keeps the rendered URLs readable.
	fingerprint := hex.EncodeToString(sum.Sum(nil))[:10]

	assetFingerprintsMu.Lock()
	assetFingerprints[name] = assetEntry{fingerprint: fingerprint, modTimeUnix: modTime, size: size}
	assetFingerprintsMu.Unlock()

	return fingerprint
}

// assetURL renders a cache-busting URL for a static asset, e.g.
// "/static/style.css" -> "/static/style.css?v=1f4c9ab203". It is exposed to
// templates as the "asset" function.
//
// The query string is what makes a deploy reach users: pages are served no-store,
// so a browser always fetches fresh HTML, and the new HTML points at a URL that is
// not in its cache — no hard refresh needed. ServeStatic then serves anything
// carrying a "v" query immutably, since the URL changes whenever the bytes do.
func assetURL(urlPath string) string {
	name := strings.TrimPrefix(urlPath, staticURLPrefix)
	if fingerprint := assetFingerprint(name); fingerprint != "" {
		return fmt.Sprintf("%s?v=%s", urlPath, fingerprint)
	}
	// Unreadable or outside web/static: still vary by build so a redeploy
	// invalidates, even though we cannot fingerprint the content.
	return fmt.Sprintf("%s?v=%s", urlPath, Version)
}
