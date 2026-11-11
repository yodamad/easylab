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

// The lab's own portal closes on time without the admin instance, so the close
// date must survive the sync to the cluster and the rebuild on the portal side.
func TestPortalState_LabCloseDate(t *testing.T) {
	t.Parallel()

	closeAt := time.Date(2099, 5, 6, 7, 8, 0, 0, time.UTC)
	past := time.Now().Add(-time.Hour)

	tests := []struct {
		name       string
		closeDate  *time.Time
		wantClosed bool
	}{
		{name: "no date", closeDate: nil},
		{name: "future date", closeDate: &closeAt},
		{name: "passed date closes the portal's lab", closeDate: &past, wantClosed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			jm := NewJobManager("")
			id := jm.CreateJob(&LabConfig{StackName: "workshop", LabCloseDate: tt.closeDate})
			job, _ := jm.GetJob(id)

			state := portalStateFromJob(job, StudentAuthSnapshot{}, "")
			if tt.closeDate == nil {
				assert.Nil(t, state.LabCloseDate)
			} else {
				require.NotNil(t, state.LabCloseDate)
				assert.True(t, tt.closeDate.Equal(*state.LabCloseDate))
			}

			encoded, err := encodePortalState(state)
			require.NoError(t, err)
			decoded, err := decodePortalState(encoded)
			require.NoError(t, err)

			rebuilt := decoded.labConfig()
			if tt.closeDate == nil {
				assert.Nil(t, rebuilt.LabCloseDate)
			} else {
				require.NotNil(t, rebuilt.LabCloseDate)
				assert.True(t, tt.closeDate.Equal(*rebuilt.LabCloseDate))
			}
			assert.False(t, rebuilt.Disabled, "the flag itself is not set by a schedule")
			assert.Equal(t, tt.wantClosed, rebuilt.IsClosed())
		})
	}
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

// An admin that has no key to sync (an older build, or a lab not yet minted)
// must not wipe the one the portal verifies workspace tokens with.
func TestSetPortalJob_NotifySecret(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		existing string
		incoming string
		expected string
	}{
		{"empty incoming keeps the live key", "live-key", "", "live-key"},
		{"non-empty incoming replaces it", "live-key", "rotated-key", "rotated-key"},
		{"same key stays", "live-key", "live-key", "live-key"},
		{"empty over empty stays empty", "", "", ""},
		{"first real key is adopted", "", "first-key", "first-key"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			jm := NewJobManager("")
			jm.setPortalJob(PortalState{LabID: "lab-1", NotifySecret: tt.existing})
			jm.setPortalJob(PortalState{LabID: "lab-1", NotifySecret: tt.incoming})

			job, ok := jm.GetJob("lab-1")
			require.True(t, ok)
			job.mu.RLock()
			defer job.mu.RUnlock()
			assert.Equal(t, tt.expected, job.NotifySecret)
		})
	}
}

// A job the portal has never seen takes the state's value as-is.
func TestSetPortalJob_CreateTakesStateSecret(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		incoming string
	}{
		{"with a key", "signing-key"},
		{"without a key", ""},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			jm := NewJobManager("")
			jm.setPortalJob(PortalState{LabID: "lab-new", NotifySecret: tt.incoming})

			job, ok := jm.GetJob("lab-new")
			require.True(t, ok)
			job.mu.RLock()
			defer job.mu.RUnlock()
			assert.Equal(t, tt.incoming, job.NotifySecret)
		})
	}
}

// The admin mints the lab's key before the first state reaches the portal, so
// the portal never has to invent one of its own.
func TestPortalConfigFor_MintsNotifySecret(t *testing.T) {
	t.Parallel()

	jm := NewJobManager("")
	id := jm.CreateJob(&LabConfig{StackName: "workshop"})
	job, ok := jm.GetJob(id)
	require.True(t, ok)
	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

	config, err := h.portalConfigFor(job)
	require.NoError(t, err)

	job.mu.RLock()
	minted := job.NotifySecret
	job.mu.RUnlock()
	require.NotEmpty(t, minted, "the key must exist after the first sync")

	state, err := decodePortalState(config)
	require.NoError(t, err)
	assert.Equal(t, minted, state.NotifySecret)

	// Stable: a second sync carries the same key rather than a fresh one.
	config, err = h.portalConfigFor(job)
	require.NoError(t, err)
	state, err = decodePortalState(config)
	require.NoError(t, err)
	assert.Equal(t, minted, state.NotifySecret)
}

// Help requests ride the same state as notifications: only what is still open is
// synced, and nothing at all when the queue is empty.
func TestPortalStateFromJob_HelpRequests(t *testing.T) {
	t.Parallel()

	now := time.Now()
	live := HelpRequest{ID: "live", At: now.Add(-time.Minute), Student: "alice@example.com", Message: "stuck", Status: HelpStatusWaiting, Source: HelpSourcePortal}
	stale := HelpRequest{ID: "stale", At: now.Add(-2 * helpRequestTTL), Student: "bob@example.com", Status: HelpStatusWaiting}

	tests := []struct {
		name     string
		requests []HelpRequest
		wantIDs  []string
	}{
		{name: "none", requests: nil, wantIDs: nil},
		{name: "an empty queue", requests: []HelpRequest{}, wantIDs: nil},
		{name: "only expired ones leaves the field nil", requests: []HelpRequest{stale}, wantIDs: nil},
		{name: "a live one", requests: []HelpRequest{live}, wantIDs: []string{"live"}},
		{name: "expired entries are pruned, order kept", requests: []HelpRequest{stale, live}, wantIDs: []string{"live"}},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			jm := NewJobManager("")
			id := jm.CreateJob(&LabConfig{StackName: "workshop"})
			job, ok := jm.GetJob(id)
			require.True(t, ok)
			job.mu.Lock()
			job.HelpRequests = tt.requests
			job.mu.Unlock()

			state := portalStateFromJob(job, StudentAuthSnapshot{}, "")
			if tt.wantIDs == nil {
				assert.Nil(t, state.HelpRequests)
				raw, err := json.Marshal(state)
				require.NoError(t, err)
				assert.NotContains(t, string(raw), "help_requests")
				return
			}
			var got []string
			for _, hr := range state.HelpRequests {
				got = append(got, hr.ID)
			}
			assert.Equal(t, tt.wantIDs, got)
		})
	}
}

func TestPortalState_HelpRequestsRoundTrip(t *testing.T) {
	t.Parallel()

	acked := time.Date(2030, 1, 1, 10, 5, 0, 0, time.UTC)
	state := PortalState{
		LabID: "lab-1",
		HelpRequests: []HelpRequest{{
			ID:             "r1",
			At:             time.Date(2030, 1, 1, 10, 0, 0, 0, time.UTC),
			Student:        "alice@example.com",
			StudentEmail:   "Alice@Example.com",
			Message:        "pod will not start",
			WorkspaceName:  "ws-alice",
			Status:         HelpStatusHandling,
			AcknowledgedAt: &acked,
			AcknowledgedBy: "teacher@example.com",
			Source:         HelpSourceIDE,
		}},
	}

	config, err := encodePortalState(state)
	require.NoError(t, err)
	decoded, err := decodePortalState(config)
	require.NoError(t, err)

	require.Len(t, decoded.HelpRequests, 1)
	got := decoded.HelpRequests[0]
	want := state.HelpRequests[0]
	assert.Equal(t, want.ID, got.ID)
	assert.True(t, want.At.Equal(got.At))
	assert.Equal(t, want.Student, got.Student)
	assert.Equal(t, want.StudentEmail, got.StudentEmail)
	assert.Equal(t, want.Message, got.Message)
	assert.Equal(t, want.WorkspaceName, got.WorkspaceName)
	assert.Equal(t, want.Status, got.Status)
	require.NotNil(t, got.AcknowledgedAt)
	assert.True(t, acked.Equal(*got.AcknowledgedAt))
	assert.Equal(t, want.AcknowledgedBy, got.AcknowledgedBy)
	assert.Equal(t, want.Source, got.Source)

	// And the portal's mirror of the lab serves the request from there.
	portalJobs := NewJobManager("")
	portalJobs.setPortalJob(PortalState{LabID: "lab-1", HelpRequests: []HelpRequest{{
		ID: "r1", At: time.Now(), Student: "alice@example.com", Status: HelpStatusHandling,
	}}})
	mine, ok := portalJobs.StudentHelpRequest("lab-1", "alice@example.com")
	require.True(t, ok)
	assert.Equal(t, HelpStatusHandling, mine.Status)

	// A later sync replaces the queue in place — which is how the admin resolving
	// a request reaches the student.
	portalJobs.setPortalJob(PortalState{LabID: "lab-1"})
	_, ok = portalJobs.StudentHelpRequest("lab-1", "alice@example.com")
	assert.False(t, ok)
}

func TestPortalRecord_HelpRequestRoundTrip(t *testing.T) {
	t.Parallel()

	at := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	record := portalRecord{Kind: portalRecordHelpRequest, HelpRequest: &HelpRequest{
		ID: "r1", At: at, Student: "alice@example.com", Cancelled: true,
	}}
	raw, err := json.Marshal(record)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "feedback")
	assert.NotContains(t, string(raw), "audit")

	var decoded portalRecord
	require.NoError(t, json.Unmarshal(raw, &decoded))
	assert.Equal(t, portalRecordHelpRequest, decoded.Kind)
	require.NotNil(t, decoded.HelpRequest)
	assert.True(t, decoded.HelpRequest.Cancelled, "a withdrawal is the same record with Cancelled set")
	assert.True(t, decoded.HelpRequest.At.Equal(at))
}

// A request is a student's own words about a lab: it is persisted with the lab,
// and never part of what a job API response carries.
func TestJob_HelpRequestsAreNotExposed(t *testing.T) {
	t.Parallel()

	job := &Job{ID: "job-1", HelpRequests: []HelpRequest{{ID: "r1", Student: "alice@example.com", Message: "stuck", At: time.Now()}}}

	exposed, err := job.sanitizedCopy(false)
	require.NoError(t, err)
	assert.Empty(t, exposed.HelpRequests)

	persisted, err := job.sanitizedCopy(true)
	require.NoError(t, err)
	require.Len(t, persisted.HelpRequests, 1)
	assert.Equal(t, "stuck", persisted.HelpRequests[0].Message)
}
