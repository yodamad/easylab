package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"
)

// Branding lets an admin put a workshop's own name, logo, tagline and colour on
// the student portal. It is instance-wide: one branding applies to this
// instance's own student area and to every in-lab portal, which receives it in
// the state the admin syncs (see PortalState.Branding).
//
// EasyLab attribution is deliberately not configurable. A branded portal still
// says what runs it, in the footer where the copyright already was.
const (
	// brandingLogoMaxBytes caps an uploaded logo. The logo travels to in-lab
	// portals inside a Kubernetes Secret, which holds 1MB in total and already
	// carries this lab's templates, notifications and help requests. 128KB raw is
	// ~171KB once base64-encoded, which leaves that room intact.
	brandingLogoMaxBytes = 128 << 10
	// brandingNameMaxRunes and brandingTaglineMaxRunes keep the brand lockup from
	// breaking its layout. Counted in runes, not bytes: a name in a non-Latin
	// script must not be cut shorter than a Latin one.
	brandingNameMaxRunes    = 40
	brandingTaglineMaxRunes = 80

	// brandingDefaultName and brandingDefaultTagline are what the student pages
	// show when nothing is branded — the values that were hardcoded in the
	// templates before branding existed, so an unbranded instance is unchanged.
	brandingDefaultName    = "EasyLab"
	brandingDefaultTagline = "Cloud Lab Infrastructure Platform"
	// brandingDefaultLogo is the logo shipped with the build.
	brandingDefaultLogo = "/static/logo.png"
	// brandingLogoURLPath is where an uploaded logo is served from. It is public:
	// the student login page shows the logo before anyone has signed in.
	brandingLogoURLPath = "/branding/logo"

	brandingLogoTypePNG = "image/png"
	brandingLogoTypeSVG = "image/svg+xml"
)

// BrandingSettings is what an admin saved. An empty field means "use the
// default", so the zero value is stock EasyLab.
type BrandingSettings struct {
	// Name replaces the "EasyLab" wordmark on student pages and the browser title.
	Name string `json:"name,omitempty"`
	// Tagline is the line under the name on the student login page.
	Tagline string `json:"tagline,omitempty"`
	// Accent is "#rrggbb", re-tinting what --primary-color drives.
	Accent string `json:"accent,omitempty"`
	// LogoData is the uploaded image itself, base64-encoded by encoding/json, and
	// LogoType its media type. Kept inline rather than as a file beside the JSON
	// because this value has to travel to in-lab portals, which share no
	// filesystem with the admin (see PortalState).
	LogoData []byte `json:"logo_data,omitempty"`
	LogoType string `json:"logo_type,omitempty"`
}

// IsZero reports whether nothing is branded, in which case every page renders
// exactly as it did before branding existed.
func (b BrandingSettings) IsZero() bool {
	return b.Name == "" && b.Tagline == "" && b.Accent == "" && len(b.LogoData) == 0
}

// BrandingView is the resolved form the templates render, with every default
// already applied. Templates reach it through the "branding" function, which is
// why it carries no error and never needs a nil check.
type BrandingView struct {
	// Name is always set — the admin's, else EasyLab's own — because the <title>
	// and the logo's alt text always need a word. Tagline may be empty: once an
	// instance is branded, EasyLab's own tagline would be someone else's slogan
	// under their logo, so it is dropped rather than defaulted.
	Name    string
	Tagline string
	// ShowWordmark says the lockup should print Name beside the logo. It is false
	// when an admin uploaded a logo but named nothing: most real logos already
	// contain the wordmark, and printing the name again doubles it up.
	ShowWordmark bool
	// LogoAlt is the logo image's alt text. It is Name, except for an uploaded
	// logo under no name at all: we do not know what that mark is called, and
	// labelling someone else's logo "EasyLab" tells a screen reader something
	// untrue. Empty alt then leaves the page to be announced by its heading.
	LogoAlt string
	// IsCustom says an admin has branded this instance. The footer uses it to
	// credit them while still naming EasyLab.
	IsCustom bool
	// LogoURL is what the brand lockups point at — the uploaded logo, carrying a
	// content hash so a new upload invalidates the browser's copy, or the
	// build's own logo.
	LogoURL       string
	HasCustomLogo bool
	// Accent is the colour --primary-color is overridden with, "" when none is
	// set. AccentHover is the darker companion --primary-hover needs; both are
	// emitted together or not at all.
	Accent      string
	AccentHover string
	// AccentAdjusted says the accent an admin chose was too light to carry white
	// text and was darkened. The admin page tells them rather than silently
	// rendering something else than they picked.
	AccentAdjusted bool

	// logo* back ServeBrandingLogo. Unexported: a template has no business
	// rendering the bytes, and this value is copied on every read.
	logoData []byte
	logoType string
	logoETag string
}

// Logo returns the uploaded image, its media type and its ETag. The bytes are
// empty when no logo is uploaded.
func (v BrandingView) Logo() (data []byte, mediaType, etag string) {
	return v.logoData, v.logoType, v.logoETag
}

// brandingProvider holds the branding every template renders. It is package
// level for the same reason Version is: html/template binds its function map at
// parse time, and both template sets that parse web/base.html — the Handler's
// and the AuthHandler's — share one map (see templateFuncMap). Threading
// branding through request data instead would mean touching every student
// handler and the login handlers in auth.go.
//
// An atomic pointer rather than a mutex because this is read on every rendered
// page and written only on save or on a portal's ten-second state reload.
var brandingProvider atomic.Pointer[BrandingView]

// currentBranding is the "branding" template function. A nil provider — every
// test, and any process that never wired a store — yields stock EasyLab.
func currentBranding() BrandingView {
	if v := brandingProvider.Load(); v != nil {
		return *v
	}
	return BrandingSettings{}.View()
}

// SetBrandingProvider installs the branding the templates render. Both run modes
// call it: the admin process from its store, at startup and after each save, and
// an in-lab portal from the state the admin synced (see portal_runtime.go).
func SetBrandingProvider(view BrandingView) {
	brandingProvider.Store(&view)
}

// View resolves the settings into what the templates render, applying every
// default and the contrast guard.
func (b BrandingSettings) View() BrandingView {
	view := BrandingView{
		Name:         brandingDefaultName,
		LogoURL:      assetURL(brandingDefaultLogo),
		IsCustom:     !b.IsZero(),
		ShowWordmark: true,
	}
	// EasyLab's own name and tagline are the defaults only while nothing is
	// branded. On a branded instance they would put our words under someone
	// else's logo, so an empty field means "say nothing" instead.
	if !view.IsCustom {
		view.Tagline = brandingDefaultTagline
	}
	if b.Name != "" {
		view.Name = b.Name
	}
	if b.Tagline != "" {
		view.Tagline = b.Tagline
	}
	if len(b.LogoData) > 0 && b.LogoType != "" {
		sum := sha256.Sum256(b.LogoData)
		fingerprint := hex.EncodeToString(sum[:])[:10]
		view.HasCustomLogo = true
		// An uploaded logo with no name given: the image is the lockup. Name still
		// carries EasyLab's default, because the <title> needs a word, but the page
		// must neither print it next to someone else's logo nor use it to label it.
		view.ShowWordmark = b.Name != ""
		view.LogoURL = fmt.Sprintf("%s?v=%s", brandingLogoURLPath, fingerprint)
		view.logoData = b.LogoData
		view.logoType = b.LogoType
		view.logoETag = `"` + fingerprint + `"`
	}
	if view.ShowWordmark {
		view.LogoAlt = view.Name
	}
	if b.Accent != "" {
		accent, adjusted := accentForWhiteText(b.Accent)
		view.Accent = accent
		view.AccentHover = darkenHex(accent, 0.14)
		view.AccentAdjusted = adjusted
	}
	return view
}

// BrandingStore persists BrandingSettings to disk. Nothing here is a secret — a
// logo and a colour are what students are shown — so unlike the sign-in provider
// stores it neither encrypts nor restricts the file's mode.
type BrandingStore struct {
	settings BrandingSettings
	dataDir  string
	mu       sync.RWMutex
}

// NewBrandingStore creates a store and loads persisted branding from disk.
func NewBrandingStore(dataDir string) *BrandingStore {
	s := &BrandingStore{dataDir: dataDir}
	if err := s.load(); err != nil {
		log.Printf("[BRANDING] Warning: failed to load branding: %v", err)
	}
	return s
}

func (s *BrandingStore) path() string {
	return filepath.Join(s.dataDir, "branding.json")
}

// load reads the branding from disk. A missing file is not an error.
func (s *BrandingStore) load() error {
	if s.dataDir == "" {
		return nil
	}
	data, err := os.ReadFile(s.path())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read branding: %w", err)
	}
	var settings BrandingSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		return fmt.Errorf("failed to parse branding: %w", err)
	}

	s.mu.Lock()
	s.settings = settings
	s.mu.Unlock()
	return nil
}

// Get returns the saved branding.
func (s *BrandingStore) Get() BrandingSettings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.settings.clone()
}

// clone copies the settings so a caller cannot mutate the stored logo through
// the slice it was handed.
func (b BrandingSettings) clone() BrandingSettings {
	out := b
	if len(b.LogoData) > 0 {
		out.LogoData = make([]byte, len(b.LogoData))
		copy(out.LogoData, b.LogoData)
	}
	return out
}

// Set updates the branding and persists it.
//
// The write is temp-and-rename, unlike the smaller settings stores: this file
// carries a logo, so a torn write would leave unparseable JSON behind and every
// page would fall back to stock EasyLab until an admin saved again.
func (s *BrandingStore) Set(settings BrandingSettings) error {
	if s.dataDir != "" {
		data, err := json.MarshalIndent(settings, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal branding: %w", err)
		}
		if err := os.MkdirAll(s.dataDir, 0755); err != nil {
			return fmt.Errorf("failed to create data dir: %w", err)
		}
		path := s.path()
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, data, 0644); err != nil {
			return fmt.Errorf("failed to write branding: %w", err)
		}
		if err := os.Rename(tmp, path); err != nil {
			os.Remove(tmp)
			return fmt.Errorf("failed to replace branding: %w", err)
		}
	}
	s.mu.Lock()
	s.settings = settings.clone()
	s.mu.Unlock()
	return nil
}

// SetBrandingStore wires the store behind the Branding admin page and installs
// what it holds as the branding every template renders.
func (h *Handler) SetBrandingStore(store *BrandingStore) {
	h.brandingStore = store
	if store != nil {
		SetBrandingProvider(store.Get().View())
	}
}

// savedBranding returns what the admin saved, the zero value when there is no
// store (an in-lab portal, or a test).
func (h *Handler) savedBranding() BrandingSettings {
	if h.brandingStore == nil {
		return BrandingSettings{}
	}
	return h.brandingStore.Get()
}

// --- validation ---

// brandingAccentRegex is a six-digit hex colour. Three-digit shorthand is
// deliberately not accepted: the value is emitted into a stylesheet, and one
// shape to validate is one shape to reason about.
var brandingAccentRegex = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// pngMagic is the 8-byte PNG signature.
var pngMagic = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

// normalizeBrandName validates the name an admin typed. Empty means "use
// EasyLab's own", which is how a branding is cleared.
func normalizeBrandName(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if utf8.RuneCountInString(raw) > brandingNameMaxRunes {
		return "", fmt.Errorf("must be at most %d characters", brandingNameMaxRunes)
	}
	if strings.ContainsAny(raw, "\r\n") {
		return "", fmt.Errorf("must be a single line")
	}
	return raw, nil
}

// normalizeTagline validates the tagline an admin typed.
func normalizeTagline(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if utf8.RuneCountInString(raw) > brandingTaglineMaxRunes {
		return "", fmt.Errorf("must be at most %d characters", brandingTaglineMaxRunes)
	}
	if strings.ContainsAny(raw, "\r\n") {
		return "", fmt.Errorf("must be a single line")
	}
	return raw, nil
}

// normalizeAccent validates the accent colour an admin picked and returns it
// lowercased. It ends up inside a stylesheet, so nothing but #rrggbb passes.
func normalizeAccent(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if !brandingAccentRegex.MatchString(raw) {
		return "", fmt.Errorf("must be a colour like #2563eb")
	}
	return strings.ToLower(raw), nil
}

// normalizeLogo validates an uploaded logo and returns it with the media type it
// will be served as.
//
// The type is decided by the bytes, never by the upload's filename or the
// client's Content-Type: this image is served back from our own origin, so what
// it actually is has to be established here.
func normalizeLogo(data []byte) ([]byte, string, error) {
	if len(data) == 0 {
		return nil, "", nil
	}
	if len(data) > brandingLogoMaxBytes {
		return nil, "", fmt.Errorf("must be at most %d KB", brandingLogoMaxBytes>>10)
	}
	switch {
	case len(data) >= len(pngMagic) && string(data[:len(pngMagic)]) == string(pngMagic):
		return data, brandingLogoTypePNG, nil
	case looksLikeSVG(data):
		return data, brandingLogoTypeSVG, nil
	}
	return nil, "", fmt.Errorf("must be a PNG or an SVG")
}

// looksLikeSVG reports whether data parses as XML whose root element is <svg>.
// Parsing rather than sniffing for the string, so a file that merely mentions
// "svg" is not served as one.
//
// Text before the root element is rejected too: with a lenient decoder, prose
// such as `this is an <svg> file` would otherwise reach the root check and be
// served under an image media type.
func looksLikeSVG(data []byte) bool {
	decoder := xml.NewDecoder(strings.NewReader(string(data)))
	// An SVG in the wild may carry an undeclared HTML entity (&nbsp; in a <text>);
	// that is not a reason to reject the image.
	decoder.Strict = false
	decoder.Entity = xml.HTMLEntity
	for {
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		switch t := token.(type) {
		case xml.StartElement:
			return strings.EqualFold(t.Name.Local, "svg")
		case xml.CharData:
			// A declaration or a comment may precede the root; words may not.
			if len(strings.TrimSpace(string(t))) > 0 {
				return false
			}
		}
	}
}

// --- colour maths ---

// accentForWhiteText returns the accent to actually use, darkening the one an
// admin picked until white text on it clears WCAG's 3:1 large-text ratio, and
// whether it had to. The accent drives surfaces that carry white text — the
// student header's gradient, primary buttons — so a pale brand colour would
// otherwise render an unreadable page.
func accentForWhiteText(value string) (string, bool) {
	r, g, b, ok := parseHexColor(value)
	if !ok {
		return value, false
	}
	adjusted := false
	// Each step removes 10% of the remaining brightness; 24 of them take any
	// colour to near-black, so this terminates well before the bound.
	for i := 0; i < 24; i++ {
		if contrastWithWhite(r, g, b) >= 3.0 {
			break
		}
		r, g, b = scaleChannel(r, 0.9), scaleChannel(g, 0.9), scaleChannel(b, 0.9)
		adjusted = true
	}
	return formatHexColor(r, g, b), adjusted
}

// darkenHex returns the colour darkened by the given fraction, for the
// --primary-hover companion of an accent.
func darkenHex(value string, fraction float64) string {
	r, g, b, ok := parseHexColor(value)
	if !ok {
		return value
	}
	scale := 1 - fraction
	return formatHexColor(scaleChannel(r, scale), scaleChannel(g, scale), scaleChannel(b, scale))
}

func scaleChannel(v uint8, scale float64) uint8 {
	return uint8(math.Round(float64(v) * scale))
}

// contrastWithWhite is the WCAG 2.1 contrast ratio between a colour and white.
func contrastWithWhite(r, g, b uint8) float64 {
	return 1.05 / (relativeLuminance(r, g, b) + 0.05)
}

// relativeLuminance is WCAG 2.1's relative luminance of an sRGB colour.
func relativeLuminance(r, g, b uint8) float64 {
	return 0.2126*linearizeChannel(r) + 0.7152*linearizeChannel(g) + 0.0722*linearizeChannel(b)
}

func linearizeChannel(v uint8) float64 {
	c := float64(v) / 255
	if c <= 0.03928 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}

func parseHexColor(value string) (r, g, b uint8, ok bool) {
	if !brandingAccentRegex.MatchString(value) {
		return 0, 0, 0, false
	}
	decoded, err := hex.DecodeString(value[1:])
	if err != nil {
		return 0, 0, 0, false
	}
	return decoded[0], decoded[1], decoded[2], true
}

func formatHexColor(r, g, b uint8) string {
	return fmt.Sprintf("#%02x%02x%02x", r, g, b)
}

// --- handlers ---

// brandingPreset is one colour offered as a swatch on the Branding page, so the
// common case is a click rather than a hex code.
type brandingPreset struct {
	Name  string
	Value string
}

// brandingPresets are the accents offered on the Branding page. The first is
// EasyLab's own, so "back to default" is reachable the same way as any other.
var brandingPresets = []brandingPreset{
	{Name: "EasyLab blue", Value: "#2563eb"},
	{Name: "Ink", Value: "#1f2933"},
	{Name: "Forest", Value: "#157f5f"},
	{Name: "Plum", Value: "#7c3aed"},
	{Name: "Rust", Value: "#b4451f"},
	{Name: "Teal", Value: "#0f766e"},
}

// brandingView is the data rendered by the Branding admin page.
type brandingView struct {
	// Branding is what students see right now, defaults applied.
	Branding BrandingView
	// Name, Tagline and Accent are the raw saved values ("" when defaulted), so
	// an empty field shows its placeholder rather than the default as if typed.
	Name    string
	Tagline string
	Accent  string
	// DefaultName and DefaultTagline are what applies when a field is empty.
	DefaultName    string
	DefaultTagline string
	// Presets are the offered accent swatches.
	Presets []brandingPreset
	// LogoMaxKB is the upload cap, stated on the page rather than only on failure.
	LogoMaxKB int
	// PortalLabs counts the labs with a portal, which a save re-syncs.
	PortalLabs int
}

// ServeBranding serves the Branding admin page.
func (h *Handler) ServeBranding(w http.ResponseWriter, r *http.Request) {
	if h.brandingStore == nil {
		http.Error(w, "Branding is not available", http.StatusServiceUnavailable)
		return
	}
	saved := h.brandingStore.Get()

	portalLabs := 0
	for _, job := range h.jobManager.GetAllJobs() {
		if portalTargetFor(job).enabled {
			portalLabs++
		}
	}

	h.serveTemplate(w, "branding.html", brandingView{
		Branding:       saved.View(),
		Name:           saved.Name,
		Tagline:        saved.Tagline,
		Accent:         saved.Accent,
		DefaultName:    brandingDefaultName,
		DefaultTagline: brandingDefaultTagline,
		Presets:        brandingPresets,
		LogoMaxKB:      brandingLogoMaxBytes >> 10,
		PortalLabs:     portalLabs,
	})
}

// SaveBranding handles saving the Branding admin page. In-lab portals are
// brought in line in the background: branding reaches them as a state sync, so
// no portal is restarted and no student is signed out.
func (h *Handler) SaveBranding(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.brandingStore == nil {
		http.Error(w, "Branding is not available", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	// A logo makes this a multipart form; parseForm handles both encodings.
	if err := h.parseForm(w, r, maxUploadSizeSmall); err != nil {
		fmt.Fprint(w, `<div class="error-message"><p>Failed to read the form. The logo may be too large.</p></div>`)
		return
	}

	name, err := normalizeBrandName(r.FormValue("branding_name"))
	if err != nil {
		log.Printf("SaveBranding: invalid name: %v", err)
		fmt.Fprintf(w, `<div class="error-message"><p>The name %s.</p></div>`, escapeHTML(err.Error()))
		return
	}
	tagline, err := normalizeTagline(r.FormValue("branding_tagline"))
	if err != nil {
		log.Printf("SaveBranding: invalid tagline: %v", err)
		fmt.Fprintf(w, `<div class="error-message"><p>The tagline %s.</p></div>`, escapeHTML(err.Error()))
		return
	}
	accent, err := normalizeAccent(r.FormValue("branding_accent"))
	if err != nil {
		log.Printf("SaveBranding: invalid accent: %v", err)
		fmt.Fprint(w, `<div class="error-message"><p>The accent colour must be a hex colour like <code>#2563eb</code>.</p></div>`)
		return
	}

	// An empty file input leaves the saved logo alone; the explicit checkbox is
	// what removes it. Saving the page for a new tagline must not drop the logo.
	saved := h.brandingStore.Get()
	logoData, logoType := saved.LogoData, saved.LogoType
	logoDetail := "unchanged"
	if r.FormValue("branding_logo_remove") == "on" {
		logoData, logoType, logoDetail = nil, "", "removed"
	} else if uploaded, uploadErr := brandingLogoFromForm(r); uploadErr != nil {
		log.Printf("SaveBranding: invalid logo: %v", uploadErr)
		fmt.Fprintf(w, `<div class="error-message"><p>The logo %s.</p></div>`, escapeHTML(uploadErr.Error()))
		return
	} else if len(uploaded) > 0 {
		data, mediaType, normErr := normalizeLogo(uploaded)
		if normErr != nil {
			log.Printf("SaveBranding: rejected logo: %v", normErr)
			fmt.Fprintf(w, `<div class="error-message"><p>The logo %s.</p></div>`, escapeHTML(normErr.Error()))
			return
		}
		logoData, logoType, logoDetail = data, mediaType, "updated"
	}

	settings := BrandingSettings{
		Name:     name,
		Tagline:  tagline,
		Accent:   accent,
		LogoData: logoData,
		LogoType: logoType,
	}
	if err := h.brandingStore.Set(settings); err != nil {
		log.Printf("SaveBranding: failed to persist: %v", err)
		fmt.Fprint(w, `<div class="error-message"><p>Failed to save the branding. Check the server logs for details.</p></div>`)
		return
	}

	// In force on this instance's own pages immediately — they are rendered by
	// this process — and on in-lab portals at their next state sync.
	SetBrandingProvider(settings.View())
	go h.syncAllPortals()

	detail := fmt.Sprintf("name: %s, tagline: %s, accent: %s, logo: %s",
		orDefault(name), orDefault(tagline), orDefault(accent), logoDetail)
	h.recordAudit(adminActor(r), "admin", "branding.update", "", detail)

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", "/admin/branding")
		fmt.Fprint(w, `<div class="success-message"><p>Branding saved</p></div>`)
	} else {
		http.Redirect(w, r, "/admin/branding", http.StatusSeeOther)
	}
}

// brandingLogoFromForm reads the uploaded logo, if any. A missing file is not an
// error: the form is saved for the other fields too.
func brandingLogoFromForm(r *http.Request) ([]byte, error) {
	file, header, err := r.FormFile("branding_logo")
	if err != nil {
		return nil, nil
	}
	defer file.Close()
	if header.Size > brandingLogoMaxBytes {
		return nil, fmt.Errorf("must be at most %d KB", brandingLogoMaxBytes>>10)
	}
	// Read one byte past the cap, so an oversize upload is refused on what was
	// actually sent rather than on Size, which is the client's own claim.
	data, err := io.ReadAll(io.LimitReader(file, brandingLogoMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("could not be read")
	}
	if len(data) > brandingLogoMaxBytes {
		return nil, fmt.Errorf("must be at most %d KB", brandingLogoMaxBytes>>10)
	}
	return data, nil
}

// ServeBrandingLogo serves the uploaded logo. It is registered without auth, in
// every mode: the student login page shows the logo before anyone has signed in,
// and an in-lab portal serves the copy it was synced.
func (h *Handler) ServeBrandingLogo(w http.ResponseWriter, r *http.Request) {
	data, mediaType, etag := currentBranding().Logo()
	if len(data) == 0 {
		http.NotFound(w, r)
		return
	}

	// An SVG opened directly is a document, not an image, and this one came from
	// an upload. Served under a policy that allows it nothing — no script, no
	// fetch, no subresource — so it cannot act on this origin even if navigated
	// to, and with nosniff so the declared type is the one the browser honours.
	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src data:; sandbox")
	w.Header().Set("ETag", etag)
	if r.URL.Query().Get("v") != "" {
		// The URL carries a hash of the bytes, so it changes whenever they do.
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	if match := r.Header.Get("If-None-Match"); match != "" && strings.Contains(match, etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Write(data)
}
