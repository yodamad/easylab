package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// portalStateKey is the key of the portal config (see workspace.PortalSpec.Config)
// holding the JSON-encoded PortalState.
const portalStateKey = "state.json"

// portalKubeconfigSentinel stands in for a kubeconfig on the job an in-lab portal
// serves from. The student handlers treat an empty kubeconfig as "this lab has no
// cluster"; the portal's cluster is the one it runs in, reached through its
// service account rather than a kubeconfig, so there is nothing real to put there.
const portalKubeconfigSentinel = "in-cluster"

// PortalState is everything an in-lab student portal knows about its lab. The
// admin writes it into the lab's cluster on every change (see syncPortalState);
// the portal serves students from it alone, with no connection back to the admin.
//
// It is deliberately a separate, explicit type rather than a LabConfig: nothing
// reaches a lab's cluster unless it is listed here, so a credential later added
// to LabConfig cannot leak into a student-facing process by default.
type PortalState struct {
	LabID       string    `json:"lab_id"`
	StackName   string    `json:"stack_name,omitempty"`
	Description string    `json:"description,omitempty"`
	CreatedAt   time.Time `json:"created_at"`

	WorkspaceNamespace     string              `json:"workspace_namespace,omitempty"`
	WorkspaceTemplates     []WorkspaceTemplate `json:"workspace_templates,omitempty"`
	WorkspaceLifetimeHours int                 `json:"workspace_lifetime_hours,omitempty"`
	LabDeletionDate        *time.Time          `json:"lab_deletion_date,omitempty"`

	BakedImages map[string]BakedImage `json:"baked_images,omitempty"`
	Disabled    bool                  `json:"disabled,omitempty"`
	// LabCloseDate is synced so the portal closes its own lab on time. It serves
	// students with no connection back to the admin, and the admin only
	// materializes a scheduled close every few minutes — without the date itself,
	// a portal would keep handing out workspaces until the next sync reached it.
	LabCloseDate      *time.Time      `json:"lab_close_date,omitempty"`
	DisabledTemplates map[string]bool `json:"disabled_templates,omitempty"`

	// Domain, DNSProvider and ClusterIssuerName decide how a workspace is exposed
	// (see RequestWorkspace). DNSProvider is only ever read as "is there one".
	Domain            string `json:"domain,omitempty"`
	DNSProvider       string `json:"dns_provider,omitempty"`
	ClusterIssuerName string `json:"cluster_issuer_name,omitempty"`

	// WorkspaceAccesses tells a student when a teacher last opened their workspace.
	WorkspaceAccesses map[string]WorkspaceAccess `json:"workspace_accesses,omitempty"`

	// Notifications are the admin's messages to this lab's students that have not
	// expired yet (see notifications.go). They are the admin's own words, not a
	// secret, but they do reach the lab's cluster in this state — which is why
	// admins are told not to send credentials this way.
	Notifications []StudentNotification `json:"notifications,omitempty"`
	// NotifySecret lets the portal verify the tokens the lab's workspaces carry
	// when they read their notifications from inside the IDE. Signing key only: it
	// grants nothing but reading one owner's messages in this one lab.
	NotifySecret string `json:"notify_secret,omitempty"`
	// PortalURL is the address the admin deployed this portal under. A portal is
	// not told its own address at startup, and the workspaces it creates need one
	// to read their notifications from — this is it.
	PortalURL string `json:"portal_url,omitempty"`

	Auth PortalAuthState `json:"auth"`
}

// PortalAuthState is how students sign in to an in-lab portal.
type PortalAuthState struct {
	// StudentPasswordHash is the admin's bcrypt hash of the shared student
	// password, "" when password login is not configured.
	StudentPasswordHash string `json:"student_password_hash,omitempty"`
	// ClassicLoginDisabled hides the password form in favor of the providers below.
	ClassicLoginDisabled bool `json:"classic_login_disabled,omitempty"`
	// AzureAD, GitHub and GitLab list the sign-in providers the portal offers.
	// They are brokered by the admin instance at AdminURL (see broker.go), which
	// signs each result with BrokerSecret. All false when AdminURL is empty.
	AzureAD      bool   `json:"azure_ad,omitempty"`
	GitHub       bool   `json:"github,omitempty"`
	GitLab       bool   `json:"gitlab,omitempty"`
	AdminURL     string `json:"admin_url,omitempty"`
	BrokerSecret string `json:"broker_secret,omitempty"`
}

// StudentAuthSnapshot is the part of the admin's live sign-in settings a portal
// mirrors. AuthHandler.StudentAuthSnapshot produces it.
type StudentAuthSnapshot struct {
	StudentPasswordHash  string
	ClassicLoginDisabled bool
	AzureAD              bool
	GitHub               bool
	GitLab               bool
}

// portalStateFromJob builds the state synced to a lab's portal. publicURL is this
// admin instance's own address as students reach it; without one the portal
// cannot be sent back here to sign in, so it falls back to password login alone.
//
// Password sign-in uses the lab's *own* password, not this instance's: that is
// what keeps a password leaked from one lab from opening any other. auth supplies
// only the brokered providers, which are this instance's to offer.
//
// The job must not be locked by the caller.
func portalStateFromJob(job *Job, auth StudentAuthSnapshot, publicURL string) PortalState {
	job.mu.RLock()
	defer job.mu.RUnlock()

	// The hash alone — never job.Config.StudentPortalPassword. This state is
	// written to a Secret in the namespace the lab's student workspaces run in.
	labPasswordHash := ""
	if job.Config != nil {
		labPasswordHash = job.Config.StudentPortalPasswordHash
	}

	state := PortalState{
		LabID:     job.ID,
		CreatedAt: job.CreatedAt,
		Auth: PortalAuthState{
			StudentPasswordHash: labPasswordHash,
		},
	}
	if publicURL != "" && job.PortalBrokerSecret != "" {
		state.Auth.AdminURL = publicURL
		state.Auth.BrokerSecret = job.PortalBrokerSecret
		state.Auth.AzureAD = auth.AzureAD
		state.Auth.GitHub = auth.GitHub
		state.Auth.GitLab = auth.GitLab
		// Only meaningful — and only safe — while a provider is actually on offer.
		state.Auth.ClassicLoginDisabled = auth.ClassicLoginDisabled
	}
	if len(job.WorkspaceAccesses) > 0 {
		state.WorkspaceAccesses = make(map[string]WorkspaceAccess, len(job.WorkspaceAccesses))
		for id, access := range job.WorkspaceAccesses {
			state.WorkspaceAccesses[id] = access
		}
	}
	// Only what is still showing: an expired message has nothing to tell the
	// portal's students, and the state is written to a Secret, so it stays small.
	state.Notifications = pruneNotifications(job.Notifications, time.Now())
	if len(state.Notifications) == 0 {
		state.Notifications = nil
	}
	state.NotifySecret = job.NotifySecret
	if cfg := job.Config; cfg != nil {
		state.StackName = cfg.StackName
		state.Description = cfg.Description
		state.WorkspaceNamespace = cfg.WorkspaceNamespace
		state.WorkspaceTemplates = cfg.WorkspaceTemplates
		state.WorkspaceLifetimeHours = cfg.WorkspaceLifetimeHours
		state.LabDeletionDate = cfg.LabDeletionDate
		state.BakedImages = cfg.BakedImages
		state.Disabled = cfg.Disabled
		state.LabCloseDate = cfg.LabCloseDate
		state.DisabledTemplates = cfg.DisabledTemplates
		state.Domain = cfg.Domain
		state.DNSProvider = cfg.DNSProvider
		state.ClusterIssuerName = cfg.ClusterIssuerName
	}
	return state
}

// labConfig is the LabConfig the student handlers read, rebuilt on the portal side.
func (s PortalState) labConfig() *LabConfig {
	return &LabConfig{
		StackName:              s.StackName,
		Description:            s.Description,
		WorkspaceNamespace:     s.WorkspaceNamespace,
		WorkspaceTemplates:     s.WorkspaceTemplates,
		WorkspaceLifetimeHours: s.WorkspaceLifetimeHours,
		LabDeletionDate:        s.LabDeletionDate,
		BakedImages:            s.BakedImages,
		Disabled:               s.Disabled,
		LabCloseDate:           s.LabCloseDate,
		DisabledTemplates:      s.DisabledTemplates,
		Domain:                 s.Domain,
		DNSProvider:            s.DNSProvider,
		ClusterIssuerName:      s.ClusterIssuerName,
		StudentPortal:          true,
	}
}

// encodePortalState renders the state as the portal config the backend stores.
func encodePortalState(state PortalState) (map[string][]byte, error) {
	data, err := json.Marshal(state)
	if err != nil {
		return nil, fmt.Errorf("failed to encode portal state: %w", err)
	}
	return map[string][]byte{portalStateKey: data}, nil
}

// decodePortalState reads the state back out of a portal config.
func decodePortalState(config map[string][]byte) (PortalState, error) {
	raw, ok := config[portalStateKey]
	if !ok {
		return PortalState{}, fmt.Errorf("portal config has no %s", portalStateKey)
	}
	var state PortalState
	if err := json.Unmarshal(raw, &state); err != nil {
		return PortalState{}, fmt.Errorf("failed to decode portal state: %w", err)
	}
	if state.LabID == "" {
		return PortalState{}, fmt.Errorf("portal state names no lab")
	}
	return state, nil
}

// Outbox record kinds: what an in-lab portal reports back to the admin.
const (
	portalRecordFeedback       = "feedback"
	portalRecordWorkspaceEvent = "workspace_event"
	portalRecordAudit          = "audit"
)

// portalRecord is one entry of a portal's outbox. Exactly one payload is set,
// matching Kind.
type portalRecord struct {
	Kind           string          `json:"kind"`
	Feedback       *Feedback       `json:"feedback,omitempty"`
	WorkspaceEvent *WorkspaceEvent `json:"workspace_event,omitempty"`
	Audit          *AuditEntry     `json:"audit,omitempty"`
}

// newPortalRecordID returns an outbox key that sorts by creation time — the admin
// applies records in key order — with a random suffix so two records created in
// the same nanosecond still get distinct keys. It stays within the characters a
// ConfigMap key allows.
func newPortalRecordID(at time.Time) (string, error) {
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return "", fmt.Errorf("failed to generate record ID: %w", err)
	}
	return fmt.Sprintf("%020d-%s", at.UnixNano(), hex.EncodeToString(suffix)), nil
}
