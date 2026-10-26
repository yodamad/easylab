package server

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The state synced into a lab's cluster is read by a student-facing process: it
// must carry what students are served from, and none of the lab's credentials.
func TestPortalStateFromJob_CarriesNoCredentials(t *testing.T) {
	t.Parallel()
	deletion := time.Date(2030, 1, 2, 3, 4, 0, 0, time.UTC)
	jm := NewJobManager("")
	id := jm.CreateJob(&LabConfig{
		StackName:              "workshop",
		Description:            "Kubernetes 101",
		WorkspaceNamespace:     "labs",
		WorkspaceTemplates:     []WorkspaceTemplate{{Name: "go"}, {Name: "python"}},
		WorkspaceLifetimeHours: 8,
		LabDeletionDate:        &deletion,
		BakedImages:            map[string]BakedImage{"go": {Image: "registry/go@sha256:abc"}},
		Disabled:               true,
		DisabledTemplates:      map[string]bool{"python": true},
		Domain:                 "lab.example.com",
		DNSProvider:            "ovh",
		ClusterIssuerName:      "letsencrypt",
		StudentPortal:          true,

		OvhApplicationSecret: "ovh-secret-value",
		AzureClientSecret:    "azure-secret-value",
		ExternalKubeconfig:   "external-kubeconfig-value",
		DNSCredentials:       map[string]string{"token": "dns-secret-value"},

		// The portal verifies sign-in against the hash, so the password itself has
		// no business in a Secret living in the students' own namespace.
		StudentPortalPassword:     "lab-portal-password-value",
		StudentPortalPasswordHash: "lab-portal-bcrypt-hash",
	})
	job, _ := jm.GetJob(id)
	job.mu.Lock()
	job.Kubeconfig = "cluster-admin-kubeconfig-value"
	job.PortalBrokerSecret = "broker-secret-value"
	job.WorkspaceAccesses = map[string]WorkspaceAccess{"ws-alice": {At: deletion, Owner: "alice"}}
	job.mu.Unlock()

	auth := StudentAuthSnapshot{StudentPasswordHash: "bcrypt-hash", GitHub: true, ClassicLoginDisabled: true}
	state := portalStateFromJob(job, auth, "https://admin.example.com")

	assert.Equal(t, id, state.LabID)
	assert.Equal(t, "workshop", state.StackName)
	assert.Equal(t, "labs", state.WorkspaceNamespace)
	assert.Len(t, state.WorkspaceTemplates, 2)
	assert.Equal(t, 8, state.WorkspaceLifetimeHours)
	assert.Equal(t, &deletion, state.LabDeletionDate)
	assert.True(t, state.Disabled)
	assert.True(t, state.DisabledTemplates["python"])
	assert.Equal(t, "registry/go@sha256:abc", state.BakedImages["go"].Image)
	assert.Equal(t, "lab.example.com", state.Domain)
	assert.Equal(t, "ovh", state.DNSProvider)
	assert.Equal(t, "letsencrypt", state.ClusterIssuerName)
	assert.Equal(t, "alice", state.WorkspaceAccesses["ws-alice"].Owner)

	config, err := encodePortalState(state)
	require.NoError(t, err)
	encoded := string(config[portalStateKey])
	for _, secret := range []string{
		"ovh-secret-value", "azure-secret-value", "external-kubeconfig-value",
		"dns-secret-value", "cluster-admin-kubeconfig-value", "lab-portal-password-value",
	} {
		assert.NotContains(t, encoded, secret)
	}
	// The broker secret is the one thing shared on purpose: the portal verifies
	// sign-in assertions with it. The password hash likewise — that is what the
	// portal compares a student's sign-in against.
	assert.Contains(t, encoded, "broker-secret-value")
	assert.Equal(t, "lab-portal-bcrypt-hash", state.Auth.StudentPasswordHash)

	// And the config the portal rebuilds from it is the student-facing subset.
	decoded, err := decodePortalState(config)
	require.NoError(t, err)
	rebuilt := decoded.labConfig()
	assert.Equal(t, []WorkspaceTemplate{{Name: "go"}, {Name: "python"}}, rebuilt.WorkspaceTemplates)
	assert.True(t, rebuilt.StudentPortal)
	assert.Empty(t, rebuilt.DNSCredentials)
	assert.Empty(t, rebuilt.ExternalKubeconfig)
}

// Providers are brokered by the admin instance: without a public URL to send
// students to (or a secret to verify the answer with) the portal must not offer
// them — and must not hide the password form either, or nobody could sign in.
func TestPortalStateFromJob_Auth(t *testing.T) {
	t.Parallel()
	auth := StudentAuthSnapshot{StudentPasswordHash: "bcrypt-hash", AzureAD: true, GitHub: true, GitLab: true, ClassicLoginDisabled: true}

	tests := []struct {
		name         string
		publicURL    string
		brokerSecret string
		wantBrokered bool
	}{
		{name: "public URL and secret", publicURL: "https://admin.example.com", brokerSecret: "s3cret", wantBrokered: true},
		{name: "no public URL", brokerSecret: "s3cret"},
		{name: "no broker secret yet", publicURL: "https://admin.example.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			jm := NewJobManager("")
			id := jm.CreateJob(&LabConfig{StackName: "workshop", StudentPortalPasswordHash: "lab-bcrypt-hash"})
			job, _ := jm.GetJob(id)
			job.mu.Lock()
			job.PortalBrokerSecret = tt.brokerSecret
			job.mu.Unlock()

			got := portalStateFromJob(job, auth, tt.publicURL).Auth

			// Password login is local to the portal either way, and it is the lab's
			// own password — never the one this instance's student area uses.
			assert.Equal(t, "lab-bcrypt-hash", got.StudentPasswordHash)
			assert.Equal(t, tt.wantBrokered, got.AzureAD)
			assert.Equal(t, tt.wantBrokered, got.GitHub)
			assert.Equal(t, tt.wantBrokered, got.GitLab)
			assert.Equal(t, tt.wantBrokered, got.ClassicLoginDisabled)
			if tt.wantBrokered {
				assert.Equal(t, tt.publicURL, got.AdminURL)
				assert.Equal(t, tt.brokerSecret, got.BrokerSecret)
			} else {
				assert.Empty(t, got.AdminURL)
				assert.Empty(t, got.BrokerSecret)
			}
		})
	}
}

func TestDecodePortalState_Rejects(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		config map[string][]byte
	}{
		{name: "no state key", config: map[string][]byte{"other": []byte("{}")}},
		{name: "not JSON", config: map[string][]byte{portalStateKey: []byte("nope")}},
		{name: "no lab", config: map[string][]byte{portalStateKey: []byte(`{"stack_name":"x"}`)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := decodePortalState(tt.config)
			require.Error(t, err)
		})
	}
}

// The admin applies outbox records in key order, so keys must sort by time.
func TestNewPortalRecordID_SortsByTime(t *testing.T) {
	t.Parallel()
	base := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	var ids []string
	for _, offset := range []time.Duration{3 * time.Second, time.Nanosecond, 2 * time.Hour, 0} {
		id, err := newPortalRecordID(base.Add(offset))
		require.NoError(t, err)
		assert.Regexp(t, `^[-._a-zA-Z0-9]+$`, id, "must be a valid ConfigMap key")
		ids = append(ids, id)
	}
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	assert.Equal(t, []string{ids[3], ids[1], ids[0], ids[2]}, sorted)

	// Same instant, still distinct.
	a, err := newPortalRecordID(base)
	require.NoError(t, err)
	b, err := newPortalRecordID(base)
	require.NoError(t, err)
	assert.NotEqual(t, a, b)
}

func TestPortalRecord_RoundTrip(t *testing.T) {
	t.Parallel()
	at := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	record := portalRecord{Kind: portalRecordWorkspaceEvent, WorkspaceEvent: &WorkspaceEvent{At: at, Action: WorkspaceEventCreated, WorkspaceID: "ws-1"}}
	raw, err := json.Marshal(record)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "feedback")
	assert.NotContains(t, string(raw), "audit")

	var decoded portalRecord
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.NotNil(t, decoded.WorkspaceEvent)
	assert.True(t, decoded.WorkspaceEvent.At.Equal(at))
}

// A lab's own portal serves its students, so the admin's messages have to reach
// it — and only the ones still showing: an expired message has nothing to tell
// anyone, and the state is written to a Secret.
func TestPortalStateFromJob_Notifications(t *testing.T) {
	t.Parallel()

	jm := NewJobManager("")
	id := jm.CreateJob(&LabConfig{StackName: "workshop"})
	job, ok := jm.GetJob(id)
	require.True(t, ok)

	now := time.Now()
	job.mu.Lock()
	job.NotifySecret = "signing-key"
	job.Notifications = []StudentNotification{
		{ID: "live", At: now, ExpiresAt: now.Add(time.Hour), Message: "break in 10", Level: NotificationLevelInfo},
		{ID: "stale", At: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour), Message: "long gone", Level: NotificationLevelInfo},
	}
	job.mu.Unlock()

	state := portalStateFromJob(job, StudentAuthSnapshot{}, "https://easylab.example.com")

	require.Len(t, state.Notifications, 1)
	assert.Equal(t, "live", state.Notifications[0].ID)
	// The portal verifies the tokens its lab's workspaces carry, so it needs the key.
	assert.Equal(t, "signing-key", state.NotifySecret)

	// It survives the trip through the Secret intact, and the portal's mirror of
	// the lab serves it to students from there.
	config, err := encodePortalState(state)
	require.NoError(t, err)
	decoded, err := decodePortalState(config)
	require.NoError(t, err)
	require.Len(t, decoded.Notifications, 1)
	assert.Equal(t, "break in 10", decoded.Notifications[0].Message)
	assert.Equal(t, "signing-key", decoded.NotifySecret)

	portalJobs := NewJobManager("")
	portalJobs.setPortalJob(decoded)
	mirrored, ok := portalJobs.GetJob(id)
	require.True(t, ok)
	assert.Equal(t, "signing-key", mirrored.NotifySecret)
	assert.Len(t, portalJobs.ActiveStudentNotifications(id, "alice@example.com"), 1)
}

// Nothing to say means nothing in the state, rather than an empty list taking up
// room in every sync.
func TestPortalStateFromJob_NoNotifications(t *testing.T) {
	t.Parallel()

	jm := NewJobManager("")
	id := jm.CreateJob(&LabConfig{StackName: "workshop"})
	job, ok := jm.GetJob(id)
	require.True(t, ok)

	state := portalStateFromJob(job, StudentAuthSnapshot{}, "")
	assert.Nil(t, state.Notifications)

	encoded, err := json.Marshal(state)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "notifications")
}

// A portal is not told its own address at startup: it learns it from the state
// the admin syncs, and that is where the workspaces it creates read their
// notifications.
func TestPortalRuntime_LearnsItsOwnURL(t *testing.T) {
	t.Parallel()

	jm := NewJobManager("")
	id := jm.CreateJob(&LabConfig{StackName: "workshop"})
	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

	p := &PortalRuntime{labID: id, jobManager: NewJobManager("")}
	h.UsePortalRuntime(p)
	require.NotNil(t, p.onState)

	p.onState(PortalState{LabID: id, PortalURL: "https://lab.example.com"})

	h.portalErrorsMu.RLock()
	learned := h.portalURLs[id]
	h.portalErrorsMu.RUnlock()
	assert.Equal(t, "https://lab.example.com", learned)

	// Which is then what a workspace created on this portal is told to poll.
	notifyURL, token := h.workspaceNotifyAccess(id, "alice@example.com")
	assert.True(t, strings.HasPrefix(notifyURL, "https://lab.example.com/api/workspace/notifications?lab_id="))
	assert.NotEmpty(t, token)
}
