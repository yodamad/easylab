package server

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"easylab/internal/clusterscan"

	"github.com/google/uuid"
)

// A cluster profile is a named, saved kubeconfig an admin picks in the lab wizard
// instead of uploading or pasting it again for every lab on the same cluster.
// Profiles live in the credential vault (credential_vault.go) and follow its
// rules: kept across restarts only under a key that is not on disk, in memory
// otherwise. A kubeconfig is usually a cluster-admin credential, so it is never
// written anywhere else and never sent back to the browser.

const (
	maxClusterProfileNameLength        = 64
	maxClusterProfileDescriptionLength = 200
)

var (
	errClusterProfileNotFound  = errors.New("cluster profile not found")
	errClusterProfileNameTaken = errors.New("a cluster profile with this name already exists")
	// errKubeconfigUnreadable marks a kubeconfig that could not be read out of the
	// request, as opposed to one that could not be found or decrypted.
	errKubeconfigUnreadable = errors.New("kubeconfig could not be read")
)

// ClusterProfile is a saved kubeconfig for an existing Kubernetes cluster.
type ClusterProfile struct {
	ID   string
	Name string
	// Description is free text to tell clusters apart. It is stored in the clear,
	// so it must not be used for anything secret.
	Description string
	UpdatedAt   time.Time
	// Kubeconfig is the cluster's kubeconfig. Empty in listings.
	Kubeconfig string
}

// ListClusterProfiles returns the saved profiles, by name, without their
// kubeconfigs. It works while the vault is locked.
func (v *CredentialVault) ListClusterProfiles() []ClusterProfile {
	v.mu.RLock()
	defer v.mu.RUnlock()
	profiles := make([]ClusterProfile, 0, len(v.clusters))
	for _, c := range v.clusters {
		profiles = append(profiles, ClusterProfile{ID: c.ID, Name: c.Name, Description: c.Description, UpdatedAt: c.UpdatedAt})
	}
	sort.Slice(profiles, func(i, j int) bool {
		return strings.ToLower(profiles[i].Name) < strings.ToLower(profiles[j].Name)
	})
	return profiles
}

// GetClusterProfile returns one profile with its kubeconfig.
func (v *CredentialVault) GetClusterProfile(id string) (ClusterProfile, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if err := v.usableLocked(); err != nil {
		return ClusterProfile{}, err
	}
	for _, c := range v.clusters {
		if c.ID != id {
			continue
		}
		plain, err := openWithAEAD(v.aead, c.Kubeconfig, vaultClusterKubeconfigAAD(c.ID))
		if err != nil {
			return ClusterProfile{}, fmt.Errorf("failed to read cluster profile %q: %w", c.Name, err)
		}
		return ClusterProfile{ID: c.ID, Name: c.Name, Description: c.Description, UpdatedAt: c.UpdatedAt, Kubeconfig: plain}, nil
	}
	return ClusterProfile{}, errClusterProfileNotFound
}

// SaveClusterProfile creates p when its ID is empty and replaces the profile with
// that ID otherwise. It returns the profile as saved, without its kubeconfig.
func (v *CredentialVault) SaveClusterProfile(p ClusterProfile) (ClusterProfile, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if err := v.usableLocked(); err != nil {
		return ClusterProfile{}, err
	}

	index := -1
	for i, existing := range v.clusters {
		if existing.ID == p.ID && p.ID != "" {
			index = i
			continue
		}
		if strings.EqualFold(existing.Name, p.Name) {
			return ClusterProfile{}, errClusterProfileNameTaken
		}
	}
	if p.ID == "" {
		p.ID = uuid.NewString()
	} else if index < 0 {
		return ClusterProfile{}, errClusterProfileNotFound
	}

	kubeconfig, err := sealWithAEAD(v.aead, p.Kubeconfig, vaultClusterKubeconfigAAD(p.ID))
	if err != nil {
		return ClusterProfile{}, err
	}
	sealed := vaultClusterProfile{ID: p.ID, Name: p.Name, Description: p.Description, UpdatedAt: v.now(), Kubeconfig: kubeconfig}

	previous := v.clusters
	next := append([]vaultClusterProfile(nil), v.clusters...)
	if index >= 0 {
		next[index] = sealed
	} else {
		next = append(next, sealed)
	}
	v.clusters = next
	if err := v.saveLocked(); err != nil {
		v.clusters = previous
		return ClusterProfile{}, err
	}
	return ClusterProfile{ID: sealed.ID, Name: sealed.Name, Description: sealed.Description, UpdatedAt: sealed.UpdatedAt}, nil
}

// DeleteClusterProfile removes a saved profile.
func (v *CredentialVault) DeleteClusterProfile(id string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if err := v.usableLocked(); err != nil {
		return err
	}
	previous := v.clusters
	next := make([]vaultClusterProfile, 0, len(v.clusters))
	for _, c := range v.clusters {
		if c.ID != id {
			next = append(next, c)
		}
	}
	if len(next) == len(previous) {
		return errClusterProfileNotFound
	}
	v.clusters = next
	if err := v.saveLocked(); err != nil {
		v.clusters = previous
		return err
	}
	return nil
}

// clusterProfileRow is one saved profile as the clusters page lists it.
type clusterProfileRow struct {
	ID          string
	Name        string
	Description string
	UpdatedAt   string
}

// clusterProfileErrorMessage turns a profile error into text that is safe to show.
func clusterProfileErrorMessage(err error) string {
	switch {
	case errors.Is(err, errClusterProfileNotFound):
		return "That cluster profile no longer exists."
	case errors.Is(err, errClusterProfileNameTaken):
		return "Another cluster profile already uses this name."
	case errors.Is(err, clusterscan.ErrInvalidKubeconfig):
		return "That does not look like a valid kubeconfig."
	case errors.Is(err, errKubeconfigUnreadable):
		return "Failed to read the kubeconfig, please check the file and try again."
	default:
		return vaultErrorMessage(err)
	}
}

// ServeClusterProfiles serves the cluster profiles admin page.
func (h *Handler) ServeClusterProfiles(w http.ResponseWriter, r *http.Request) {
	if h.credentialVault == nil {
		http.Error(w, "Cluster profiles not available", http.StatusServiceUnavailable)
		return
	}

	var rows []clusterProfileRow
	for _, p := range h.credentialVault.ListClusterProfiles() {
		rows = append(rows, clusterProfileRow{
			ID:          p.ID,
			Name:        p.Name,
			Description: p.Description,
			UpdatedAt:   p.UpdatedAt.Format("2006-01-02 15:04"),
		})
	}

	storage := h.credentialStorageView()
	data := map[string]interface{}{
		"Storage":  storage,
		"Profiles": rows,
		// Profiles cannot be read or written while their key is unavailable.
		"CanEdit": storage.State != VaultLocked && storage.State != VaultUnreadable,
		"Edit":    ClusterProfile{},
	}
	if id := r.URL.Query().Get("edit"); id != "" {
		// Taken from the listing, which carries no kubeconfig: the form only
		// acknowledges that one is saved, so there is nothing here to decrypt.
		for _, p := range h.credentialVault.ListClusterProfiles() {
			if p.ID == id {
				data["Edit"] = p
				data["HasKubeconfig"] = true
				break
			}
		}
	}
	h.serveTemplate(w, "cluster-profiles.html", data)
}

// SaveClusterProfile creates a cluster profile, or updates the one named by
// profile_id.
func (h *Handler) SaveClusterProfile(w http.ResponseWriter, r *http.Request) {
	if !h.vaultRequest(w, r) {
		return
	}

	id := getFormValue(r, "profile_id")
	name := strings.TrimSpace(getFormValue(r, "profile_name"))
	description := strings.TrimSpace(getFormValue(r, "profile_description"))

	if name == "" || len(name) > maxClusterProfileNameLength {
		h.renderHTMLError(w, "Profile Not Saved", fmt.Sprintf("Give the profile a name of up to %d characters.", maxClusterProfileNameLength))
		return
	}
	if len(description) > maxClusterProfileDescriptionLength {
		h.renderHTMLError(w, "Profile Not Saved", fmt.Sprintf("Keep the description under %d characters.", maxClusterProfileDescriptionLength))
		return
	}

	kubeconfig, err := h.readKubeconfigFromForm(r)
	if err != nil {
		log.Printf("SaveClusterProfile: failed to read kubeconfig: %v", err)
		h.renderHTMLError(w, "Profile Not Saved", "Failed to read the kubeconfig, please check the file and try again.")
		return
	}
	kubeconfig = strings.TrimSpace(kubeconfig)

	// The kubeconfig is never sent back to the form, so blank means "keep".
	if kubeconfig == "" && id != "" {
		current, err := h.credentialVault.GetClusterProfile(id)
		if err != nil {
			log.Printf("SaveClusterProfile: failed to load profile: %v", err)
			h.renderHTMLError(w, "Profile Not Saved", clusterProfileErrorMessage(err))
			return
		}
		kubeconfig = current.Kubeconfig
	}
	if kubeconfig == "" {
		h.renderHTMLError(w, "Profile Not Saved", "Provide a kubeconfig file or paste its content.")
		return
	}
	// Parse it before storing it: this builds clients without talking to the
	// cluster, so a typo is caught here rather than minutes into a lab.
	if _, err := clusterscan.New(kubeconfig); err != nil {
		log.Printf("SaveClusterProfile: kubeconfig rejected: %v", err)
		h.renderHTMLError(w, "Profile Not Saved", clusterProfileErrorMessage(err))
		return
	}

	saved, err := h.credentialVault.SaveClusterProfile(ClusterProfile{ID: id, Name: name, Description: description, Kubeconfig: kubeconfig})
	if err != nil {
		log.Printf("SaveClusterProfile: failed to save profile: %v", err)
		h.renderHTMLError(w, "Profile Not Saved", clusterProfileErrorMessage(err))
		return
	}

	action := "cluster_profile.create"
	if id != "" {
		action = "cluster_profile.update"
	}
	h.recordAudit(adminActor(r), "admin", action, "", saved.Name)

	if isHTMXRequest(r) {
		w.Header().Set("HX-Redirect", "/admin/clusters")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<div class="success-message"><p>Cluster profile saved</p></div>`)
		return
	}
	http.Redirect(w, r, "/admin/clusters", http.StatusSeeOther)
}

// DeleteClusterProfile deletes the cluster profile named by profile_id.
func (h *Handler) DeleteClusterProfile(w http.ResponseWriter, r *http.Request) {
	if !h.vaultRequest(w, r) {
		return
	}
	id := getFormValue(r, "profile_id")
	name := id
	for _, p := range h.credentialVault.ListClusterProfiles() {
		if p.ID == id {
			name = p.Name
		}
	}
	if err := h.credentialVault.DeleteClusterProfile(id); err != nil {
		log.Printf("DeleteClusterProfile: %v", err)
		h.renderHTMLError(w, "Profile Not Deleted", clusterProfileErrorMessage(err))
		return
	}
	h.recordAudit(adminActor(r), "admin", "cluster_profile.delete", "", name)

	if isHTMXRequest(r) {
		w.Header().Set("HX-Redirect", "/admin/clusters")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<div class="success-message"><p>Cluster profile deleted</p></div>`)
		return
	}
	http.Redirect(w, r, "/admin/clusters", http.StatusSeeOther)
}

// wizardClusterProfile is one saved profile as the lab wizard's dropdown offers it.
type wizardClusterProfile struct {
	ID          string
	Name        string
	Description string
}

// wizardClusterProfiles returns the profiles the wizard can offer, and whether
// some exist but are locked away.
func (h *Handler) wizardClusterProfiles() (profiles []wizardClusterProfile, locked bool) {
	if h.credentialVault == nil {
		return nil, false
	}
	saved := h.credentialVault.ListClusterProfiles()
	if state := h.credentialVault.Status().State; state == VaultLocked || state == VaultUnreadable {
		return nil, len(saved) > 0
	}
	for _, p := range saved {
		profiles = append(profiles, wizardClusterProfile{ID: p.ID, Name: p.Name, Description: p.Description})
	}
	return profiles, false
}

// kubeconfigFromFormOrProfile returns the kubeconfig for a BYO-cluster request:
// an uploaded or pasted one when present, otherwise the saved profile picked in
// the wizard (form field cluster_profile). The second return is the profile's
// name, for LabConfig.ClusterProfile. Both are empty when neither was provided,
// which the caller reports as a missing kubeconfig.
//
// The kubeconfig is copied out of the profile here, so a lab created from one
// keeps working whatever later happens to the profile or the vault.
func (h *Handler) kubeconfigFromFormOrProfile(r *http.Request) (kubeconfig, profileName string, err error) {
	submitted, err := h.readKubeconfigFromForm(r)
	if err != nil {
		return "", "", fmt.Errorf("%w: %w", errKubeconfigUnreadable, err)
	}
	if strings.TrimSpace(submitted) != "" {
		return submitted, "", nil
	}

	id := getFormValue(r, "cluster_profile")
	if id == "" {
		return "", "", nil
	}
	if h.credentialVault == nil {
		return "", "", errClusterProfileNotFound
	}
	profile, err := h.credentialVault.GetClusterProfile(id)
	if err != nil {
		return "", "", err
	}
	return profile.Kubeconfig, profile.Name, nil
}
