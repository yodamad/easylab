package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"
)

// OpenChamber signs its own browser session: a HS256 JWT carried in the
// oc_ui_session cookie, signed with the OPENCODE_JWT_SECRET its process was
// started with, and validated by verifying that signature and nothing else —
// there is no server-side session store to enrol in. EasyLab is the one that
// sets that secret per workspace, so it can mint the very same session and plant
// it in the student's browser, which is what spares them OpenChamber's password
// prompt. The password is still set on the workspace: it stays the way in for
// the fallback path and for a student who opens the workspace URL directly.
const (
	// openChamberSessionCookie is the cookie OpenChamber reads its session from.
	// The name is fixed upstream (per-port variants exist, but a workspace is
	// reached through the ingress on the default port, so the bare name applies).
	openChamberSessionCookie = "oc_ui_session"

	// openChamberSessionTTL matches OpenChamber's own default session lifetime
	// (OPENCHAMBER_UI_SESSION_TTL_HOURS). A fresh session is planted on every open,
	// so this only has to outlast a single sitting.
	openChamberSessionTTL = 12 * time.Hour
)

// openChamberSessionToken mints the session token OpenChamber's own issueSession
// would have produced for this workspace: HS256 over {"type":"ui-session"} with
// the issued-at and expiry claims it sets, signed with the workspace's session
// secret. The header is deliberately just {"alg":"HS256"} — the same minimal
// header upstream signs with, so verification cannot differ over it.
//
// The secret signs as the UTF-8 bytes of the environment value, not a decoding
// of it, which is how OpenChamber loads it into its own signing key.
func openChamberSessionToken(secret string, now time.Time, ttl time.Duration) (string, error) {
	if strings.TrimSpace(secret) == "" {
		return "", fmt.Errorf("failed to mint OpenChamber session: no signing secret recorded")
	}

	header, err := json.Marshal(map[string]string{"alg": "HS256"})
	if err != nil {
		return "", fmt.Errorf("failed to encode OpenChamber session header: %w", err)
	}
	claims, err := json.Marshal(struct {
		Type      string `json:"type"`
		IssuedAt  int64  `json:"iat"`
		ExpiresAt int64  `json:"exp"`
	}{Type: "ui-session", IssuedAt: now.Unix(), ExpiresAt: now.Add(ttl).Unix()})
	if err != nil {
		return "", fmt.Errorf("failed to encode OpenChamber session claims: %w", err)
	}

	signing := base64url(header) + "." + base64url(claims)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signing))
	return signing + "." + base64url(mac.Sum(nil)), nil
}

// base64url encodes a JWT segment: base64url, unpadded.
func base64url(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

// openChamberCookieDomain returns the domain to scope a planted session cookie
// to, and whether planting one is possible at all.
//
// A workspace is served from "{name}.{labDomain}" and EasyLab from a host of its
// own, so the cookie has to be scoped to the lab domain for the workspace to be
// sent it. A browser only accepts a Domain that the responding host itself sits
// under: the lab's in-cluster student portal (lab.example.com) can set one for
// example.com, but an admin UI served from an unrelated host cannot, and gets
// false so the caller falls back to handing the password over.
//
// Two consequences of scoping to the lab domain rather than the workspace host,
// which is not something this function can choose:
//
// The cookie name is fixed upstream, so one lab domain holds one such cookie:
// opening a second workspace on the same domain replaces the first's session.
// Each workspace signs with its own secret, so a replaced cookie only fails to
// verify at the other workspace — never signs anyone into it — and opening that
// workspace again plants its session afresh.
//
// More importantly, the browser sends a domain-scoped cookie to every host under
// the lab domain, which includes the other students' workspaces. A workspace's
// own occupant can read what arrives at it, so a student who follows a link to a
// classmate's workspace hands that classmate their live OpenChamber session.
// code-server's flow does not have this exposure: its password is posted to the
// workspace's own origin and the cookie it sets is host-only. Closing it means
// serving the sign-in from the workspace's origin — an EasyLab path added to the
// workspace Ingress — so the cookie can be host-only here too.
func openChamberCookieDomain(requestHost, workspaceHost string) (string, bool) {
	reqHost := hostWithoutPort(requestHost)
	wsHost := hostWithoutPort(workspaceHost)
	if reqHost == "" || wsHost == "" {
		return "", false
	}

	// The lab domain is the workspace host minus its own leading label.
	dot := strings.Index(wsHost, ".")
	if dot <= 0 {
		return "", false
	}
	labDomain := wsHost[dot+1:]
	// A single-label domain would be an intranet name or a typo; scoping a cookie
	// to one is not something to attempt silently.
	if !strings.Contains(labDomain, ".") {
		return "", false
	}

	if reqHost != labDomain && !strings.HasSuffix(reqHost, "."+labDomain) {
		return "", false
	}
	return labDomain, true
}

// hostWithoutPort normalizes a Host header or URL host for comparison.
func hostWithoutPort(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}
