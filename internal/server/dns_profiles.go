package server

import (
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	dnsregistry "easylab/internal/providers/dns"

	"github.com/google/uuid"
)

// A DNS profile is a named, saved set of DNS provider credentials an admin picks
// in the lab wizard instead of typing them again. Profiles live in the
// credential vault (credential_vault.go) and follow its rules: kept across
// restarts only under a key that is not on disk, in memory otherwise.

const maxDNSProfileNameLength = 64

var (
	errDNSProfileNotFound  = errors.New("DNS profile not found")
	errDNSProfileNameTaken = errors.New("a DNS profile with this name already exists")
)

// DNSProfile is a saved set of DNS provider credentials.
type DNSProfile struct {
	ID       string
	Name     string
	Provider string
	// Zone, when set, is offered as the wizard's DNS zone for this profile.
	Zone      string
	UpdatedAt time.Time
	// Credentials maps dns.CredentialField names to values. Nil in listings.
	Credentials map[string]string
}

// ListDNSProfiles returns the saved profiles, by name, without their
// credentials. It works while the vault is locked.
func (v *CredentialVault) ListDNSProfiles() []DNSProfile {
	v.mu.RLock()
	defer v.mu.RUnlock()
	profiles := make([]DNSProfile, 0, len(v.profiles))
	for _, p := range v.profiles {
		profiles = append(profiles, DNSProfile{ID: p.ID, Name: p.Name, Provider: p.Provider, Zone: p.Zone, UpdatedAt: p.UpdatedAt})
	}
	sort.Slice(profiles, func(i, j int) bool {
		return strings.ToLower(profiles[i].Name) < strings.ToLower(profiles[j].Name)
	})
	return profiles
}

// GetDNSProfile returns one profile with its credentials.
func (v *CredentialVault) GetDNSProfile(id string) (DNSProfile, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if err := v.usableLocked(); err != nil {
		return DNSProfile{}, err
	}
	for _, p := range v.profiles {
		if p.ID != id {
			continue
		}
		out := DNSProfile{ID: p.ID, Name: p.Name, Provider: p.Provider, Zone: p.Zone, UpdatedAt: p.UpdatedAt,
			Credentials: make(map[string]string, len(p.Credentials))}
		for field, sealed := range p.Credentials {
			plain, err := openWithAEAD(v.aead, sealed, vaultDNSFieldAAD(p.ID, field))
			if err != nil {
				return DNSProfile{}, fmt.Errorf("failed to read DNS profile %q: %w", p.Name, err)
			}
			out.Credentials[field] = plain
		}
		return out, nil
	}
	return DNSProfile{}, errDNSProfileNotFound
}

// SaveDNSProfile creates p when its ID is empty and replaces the profile with
// that ID otherwise. It returns the profile as saved, without credentials.
func (v *CredentialVault) SaveDNSProfile(p DNSProfile) (DNSProfile, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if err := v.usableLocked(); err != nil {
		return DNSProfile{}, err
	}

	index := -1
	for i, existing := range v.profiles {
		if existing.ID == p.ID && p.ID != "" {
			index = i
			continue
		}
		if strings.EqualFold(existing.Name, p.Name) {
			return DNSProfile{}, errDNSProfileNameTaken
		}
	}
	if p.ID == "" {
		p.ID = uuid.NewString()
	} else if index < 0 {
		return DNSProfile{}, errDNSProfileNotFound
	}

	sealed := vaultDNSProfile{ID: p.ID, Name: p.Name, Provider: p.Provider, Zone: p.Zone, UpdatedAt: v.now(),
		Credentials: make(map[string]string, len(p.Credentials))}
	for field, value := range p.Credentials {
		if value == "" {
			continue
		}
		s, err := sealWithAEAD(v.aead, value, vaultDNSFieldAAD(p.ID, field))
		if err != nil {
			return DNSProfile{}, err
		}
		sealed.Credentials[field] = s
	}

	previous := v.profiles
	next := append([]vaultDNSProfile(nil), v.profiles...)
	if index >= 0 {
		next[index] = sealed
	} else {
		next = append(next, sealed)
	}
	v.profiles = next
	if err := v.saveLocked(); err != nil {
		v.profiles = previous
		return DNSProfile{}, err
	}
	return DNSProfile{ID: sealed.ID, Name: sealed.Name, Provider: sealed.Provider, Zone: sealed.Zone, UpdatedAt: sealed.UpdatedAt}, nil
}

// DeleteDNSProfile removes a saved profile.
func (v *CredentialVault) DeleteDNSProfile(id string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if err := v.usableLocked(); err != nil {
		return err
	}
	previous := v.profiles
	next := make([]vaultDNSProfile, 0, len(v.profiles))
	for _, p := range v.profiles {
		if p.ID != id {
			next = append(next, p)
		}
	}
	if len(next) == len(previous) {
		return errDNSProfileNotFound
	}
	v.profiles = next
	if err := v.saveLocked(); err != nil {
		v.profiles = previous
		return err
	}
	return nil
}

// dnsProviderOption is one entry of a DNS provider dropdown.
type dnsProviderOption struct {
	Name  string
	Label string
}

// dnsProviderLabel is the display name of a registered DNS provider.
func dnsProviderLabel(name string) string {
	switch name {
	case "ovh":
		return "OVH DNS"
	case "azure":
		return "Azure DNS"
	default:
		return name
	}
}

// dnsProviderOptions lists the registered DNS providers, by name.
func dnsProviderOptions() []dnsProviderOption {
	names := dnsregistry.List()
	sort.Strings(names)
	options := make([]dnsProviderOption, 0, len(names))
	for _, name := range names {
		options = append(options, dnsProviderOption{Name: name, Label: dnsProviderLabel(name)})
	}
	return options
}

// dnsProfileFieldsHTML renders the credential inputs of a DNS provider, from the
// fields the provider itself declares. existing holds the values of the profile
// being edited: non-secret ones are shown, secret ones are only acknowledged.
func dnsProfileFieldsHTML(provider string, existing map[string]string) template.HTML {
	dnsP, err := dnsregistry.Get(provider)
	if err != nil || dnsP == nil {
		return ""
	}
	var b strings.Builder
	for _, f := range dnsP.GetCredentialFields() {
		id := "profile_cred_" + f.Name
		fmt.Fprintf(&b, `<div class="form-group"><label for="%s">%s</label>`, escapeHTML(id), escapeHTML(f.Label))
		if f.IsSecret {
			placeholder := ""
			if existing[f.Name] != "" {
				placeholder = "(saved — enter new value to change)"
			}
			fmt.Fprintf(&b, `<input type="password" id="%s" name="dns_cred_%s" placeholder="%s" autocomplete="new-password">`,
				escapeHTML(id), escapeHTML(f.Name), escapeHTML(placeholder))
		} else {
			fmt.Fprintf(&b, `<input type="text" id="%s" name="dns_cred_%s" value="%s" placeholder="%s" autocomplete="off">`,
				escapeHTML(id), escapeHTML(f.Name), escapeHTML(existing[f.Name]), escapeHTML(f.Placeholder))
		}
		b.WriteString(`</div>`)
	}
	return template.HTML(b.String())
}

// dnsProfileRow is one saved profile as the DNS page lists it.
type dnsProfileRow struct {
	ID            string
	Name          string
	ProviderLabel string
	Zone          string
	UpdatedAt     string
}

// ServeDNSProfiles serves the DNS profiles admin page.
func (h *Handler) ServeDNSProfiles(w http.ResponseWriter, r *http.Request) {
	if h.credentialVault == nil {
		http.Error(w, "DNS profiles not available", http.StatusServiceUnavailable)
		return
	}

	var rows []dnsProfileRow
	for _, p := range h.credentialVault.ListDNSProfiles() {
		rows = append(rows, dnsProfileRow{
			ID:            p.ID,
			Name:          p.Name,
			ProviderLabel: dnsProviderLabel(p.Provider),
			Zone:          p.Zone,
			UpdatedAt:     p.UpdatedAt.Format("2006-01-02 15:04"),
		})
	}

	storage := h.credentialStorageView()
	data := map[string]interface{}{
		"Storage":   storage,
		"Profiles":  rows,
		"Providers": dnsProviderOptions(),
		// Profiles cannot be read or written while their key is unavailable.
		"CanEdit":    storage.State != VaultLocked && storage.State != VaultUnreadable,
		"Edit":       DNSProfile{},
		"FieldsHTML": template.HTML(""),
	}
	if id := r.URL.Query().Get("edit"); id != "" {
		if profile, err := h.credentialVault.GetDNSProfile(id); err == nil {
			data["Edit"] = profile
			data["FieldsHTML"] = dnsProfileFieldsHTML(profile.Provider, profile.Credentials)
		}
	}
	h.serveTemplate(w, "dns-profiles.html", data)
}

// DNSProfileFields returns the credential inputs for the provider picked in the
// profile form.
func (h *Handler) DNSProfileFields(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// The provider dropdown sends its own name/value pair.
	fmt.Fprint(w, dnsProfileFieldsHTML(r.URL.Query().Get("profile_provider"), nil))
}

// dnsProfileErrorMessage turns a profile error into text that is safe to show.
func dnsProfileErrorMessage(err error) string {
	switch {
	case errors.Is(err, errDNSProfileNotFound):
		return "That DNS profile no longer exists."
	case errors.Is(err, errDNSProfileNameTaken):
		return "Another DNS profile already uses this name."
	default:
		return vaultErrorMessage(err)
	}
}

// SaveDNSProfile creates a DNS profile, or updates the one named by profile_id.
func (h *Handler) SaveDNSProfile(w http.ResponseWriter, r *http.Request) {
	if !h.vaultRequest(w, r) {
		return
	}

	id := getFormValue(r, "profile_id")
	name := strings.TrimSpace(getFormValue(r, "profile_name"))
	provider := getFormValue(r, "profile_provider")
	zone := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(getFormValue(r, "profile_zone"))), ".")

	if name == "" || len(name) > maxDNSProfileNameLength {
		h.renderHTMLError(w, "Profile Not Saved", fmt.Sprintf("Give the profile a name of up to %d characters.", maxDNSProfileNameLength))
		return
	}
	dnsP, err := dnsregistry.Get(provider)
	if err != nil || dnsP == nil {
		h.renderHTMLError(w, "Profile Not Saved", "Choose a DNS provider.")
		return
	}

	var existing map[string]string
	if id != "" {
		current, err := h.credentialVault.GetDNSProfile(id)
		if err != nil {
			log.Printf("SaveDNSProfile: failed to load profile: %v", err)
			h.renderHTMLError(w, "Profile Not Saved", dnsProfileErrorMessage(err))
			return
		}
		// A different provider has different fields: nothing carries over.
		if current.Provider == provider {
			existing = current.Credentials
		}
	}

	credentials := make(map[string]string)
	for _, f := range dnsP.GetCredentialFields() {
		value := strings.TrimSpace(getFormValue(r, "dns_cred_"+f.Name))
		if f.IsSecret {
			// Secrets are never sent back to the form, so blank means "keep".
			if value == "" {
				value = existing[f.Name]
			}
			if value == "" {
				h.renderHTMLError(w, "Profile Not Saved", fmt.Sprintf("%s is required.", f.Label))
				return
			}
		}
		credentials[f.Name] = value
	}

	saved, err := h.credentialVault.SaveDNSProfile(DNSProfile{ID: id, Name: name, Provider: provider, Zone: zone, Credentials: credentials})
	if err != nil {
		log.Printf("SaveDNSProfile: failed to save profile: %v", err)
		h.renderHTMLError(w, "Profile Not Saved", dnsProfileErrorMessage(err))
		return
	}

	action := "dns_profile.create"
	if id != "" {
		action = "dns_profile.update"
	}
	h.recordAudit(adminActor(r), "admin", action, "", fmt.Sprintf("%s (%s)", saved.Name, saved.Provider))

	if isHTMXRequest(r) {
		w.Header().Set("HX-Redirect", "/admin/dns")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<div class="success-message"><p>DNS profile saved</p></div>`)
		return
	}
	http.Redirect(w, r, "/admin/dns", http.StatusSeeOther)
}

// DeleteDNSProfile deletes the DNS profile named by profile_id.
func (h *Handler) DeleteDNSProfile(w http.ResponseWriter, r *http.Request) {
	if !h.vaultRequest(w, r) {
		return
	}
	id := getFormValue(r, "profile_id")
	name := id
	for _, p := range h.credentialVault.ListDNSProfiles() {
		if p.ID == id {
			name = p.Name
		}
	}
	if err := h.credentialVault.DeleteDNSProfile(id); err != nil {
		log.Printf("DeleteDNSProfile: %v", err)
		h.renderHTMLError(w, "Profile Not Deleted", dnsProfileErrorMessage(err))
		return
	}
	h.recordAudit(adminActor(r), "admin", "dns_profile.delete", "", name)

	if isHTMXRequest(r) {
		w.Header().Set("HX-Redirect", "/admin/dns")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<div class="success-message"><p>DNS profile deleted</p></div>`)
		return
	}
	http.Redirect(w, r, "/admin/dns", http.StatusSeeOther)
}

// wizardDNSProfile is one saved profile as the lab wizard's dropdown offers it.
type wizardDNSProfile struct {
	ID            string
	Name          string
	Provider      string
	ProviderLabel string
	Zone          string
}

// wizardDNSProfiles returns the profiles the wizard can offer, and whether some
// exist but are locked away.
func (h *Handler) wizardDNSProfiles() (profiles []wizardDNSProfile, locked bool) {
	if h.credentialVault == nil {
		return nil, false
	}
	saved := h.credentialVault.ListDNSProfiles()
	if state := h.credentialVault.Status().State; state == VaultLocked || state == VaultUnreadable {
		return nil, len(saved) > 0
	}
	for _, p := range saved {
		profiles = append(profiles, wizardDNSProfile{ID: p.ID, Name: p.Name, Provider: p.Provider, ProviderLabel: dnsProviderLabel(p.Provider), Zone: p.Zone})
	}
	return profiles, false
}

// applyDNSProfile fills a lab config's DNS provider and credentials from the
// saved profile picked in the wizard (form field dns_profile). It does nothing
// when none was picked. The credentials are copied into the config, so the lab
// keeps working whatever later happens to the profile or the vault.
func (h *Handler) applyDNSProfile(r *http.Request, config *LabConfig) error {
	id := getFormValue(r, "dns_profile")
	if id == "" {
		return nil
	}
	if h.credentialVault == nil {
		return errDNSProfileNotFound
	}
	profile, err := h.credentialVault.GetDNSProfile(id)
	if err != nil {
		return err
	}
	if config.DNSProvider != "" && config.DNSProvider != profile.Provider {
		return fmt.Errorf("DNS profile %q is for %s, not %s", profile.Name, dnsProviderLabel(profile.Provider), dnsProviderLabel(config.DNSProvider))
	}
	config.DNSProvider = profile.Provider
	config.DNSProfile = profile.Name
	config.DNSCredentials = profile.Credentials
	if strings.TrimSpace(config.DNSZone) == "" {
		config.DNSZone = profile.Zone
	}
	return nil
}
