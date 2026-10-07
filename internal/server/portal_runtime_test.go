package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"easylab/internal/providers/workspace"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func portalTestState() PortalState {
	return PortalState{
		LabID:              "job-lab",
		StackName:          "workshop",
		WorkspaceNamespace: "workshops",
		WorkspaceTemplates: []WorkspaceTemplate{{Name: "go"}, {Name: "python"}},
		Domain:             "lab.example.com",
	}
}

// decodedOutbox returns the fake cluster's outbox records, grouped by kind.
func decodedOutbox(t *testing.T, fb *fakePortalBackend) map[string][]portalRecord {
	t.Helper()
	out := map[string][]portalRecord{}
	for _, raw := range fb.outboxRecords() {
		var record portalRecord
		require.NoError(t, json.Unmarshal([]byte(raw), &record))
		out[record.Kind] = append(out[record.Kind], record)
	}
	return out
}

func TestNewPortalRuntime_Requirements(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		labID   string
		backend workspace.Backend
	}{
		{name: "no lab ID", labID: "", backend: newFakePortalBackend()},
		{name: "backend cannot host a portal", labID: "job-lab", backend: &fakeBackend{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewPortalRuntime(tt.labID, tt.backend, NewJobManager(""), nil)
			require.Error(t, err)
		})
	}
}

// Loading the synced state is what makes the lab exist for the student handlers.
func TestPortalRuntime_Load(t *testing.T) {
	t.Parallel()
	fb := newFakePortalBackend()
	h, p := newTestPortal(t, fb, portalTestState())

	job, exists := h.jobManager.GetJob("job-lab")
	require.True(t, exists)
	job.mu.RLock()
	assert.Equal(t, JobStatusCompleted, job.Status)
	assert.Equal(t, portalKubeconfigSentinel, job.Kubeconfig)
	assert.Equal(t, "workshops", job.workspaceNamespace())
	assert.Len(t, job.Config.GetWorkspaceTemplates(), 2)
	job.mu.RUnlock()

	// A later sync updates the same job in place: handlers hold on to it.
	next := portalTestState()
	next.Disabled = true
	next.DisabledTemplates = map[string]bool{"python": true}
	next.WorkspaceAccesses = map[string]WorkspaceAccess{"ws-alice": {At: time.Now(), Owner: "alice"}}
	config, err := encodePortalState(next)
	require.NoError(t, err)
	require.NoError(t, fb.SyncPortalConfig(context.Background(), "job-lab", config))
	require.NoError(t, p.Load(context.Background()))

	again, _ := h.jobManager.GetJob("job-lab")
	assert.Same(t, job, again)
	job.mu.RLock()
	assert.True(t, job.Config.Disabled)
	assert.True(t, job.Config.IsTemplateDisabled("python"))
	job.mu.RUnlock()
	access, ok := h.jobManager.GetWorkspaceAccess("job-lab", "ws-alice")
	require.True(t, ok)
	assert.Equal(t, "alice", access.Owner)
}

func TestPortalRuntime_Load_Errors(t *testing.T) {
	t.Parallel()

	t.Run("no state synced yet", func(t *testing.T) {
		t.Parallel()
		jm := NewJobManager("")
		p, err := NewPortalRuntime("job-lab", newFakePortalBackend(), jm, nil)
		require.NoError(t, err)
		require.Error(t, p.Load(context.Background()))
		assert.Empty(t, jm.GetAllJobs())
	})

	// A portal must never start serving another lab because of a mixed-up Secret.
	t.Run("state of another lab", func(t *testing.T) {
		t.Parallel()
		fb := newFakePortalBackend()
		other := portalTestState()
		other.LabID = "job-other"
		config, err := encodePortalState(other)
		require.NoError(t, err)
		fb.config = config

		jm := NewJobManager("")
		p, err := NewPortalRuntime("job-lab", fb, jm, nil)
		require.NoError(t, err)
		err = p.Load(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "job-other")
		assert.Empty(t, jm.GetAllJobs())
	})
}

// The student API answers from the mirrored lab, without leaking the sentinel
// that stands in for a kubeconfig.
func TestPortal_StudentHandlersServeTheMirroredLab(t *testing.T) {
	t.Parallel()
	fb := newFakePortalBackend()
	h, _ := newTestPortal(t, fb, portalTestState())

	w := httptest.NewRecorder()
	h.ListLabs(w, asStudent(httptest.NewRequest(http.MethodGet, "/api/student/labs", nil), "alice@example.com"))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"id":"job-lab"`)
	assert.NotContains(t, w.Body.String(), portalKubeconfigSentinel)

	w = httptest.NewRecorder()
	h.ListLabTemplates(w, asStudent(httptest.NewRequest(http.MethodGet, "/api/student/labs/templates?lab_id=job-lab", nil), "alice@example.com"))
	require.Equal(t, http.StatusOK, w.Code)
	var templates []map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &templates))
	require.Len(t, templates, 2)
	assert.Equal(t, "go", templates[0]["name"])
}

// A workspace created in the portal is created on the portal's own cluster and
// reported to the admin: its history and audit log live there, not here.
func TestPortal_RequestWorkspaceReportsToAdmin(t *testing.T) {
	t.Parallel()
	fb := newFakePortalBackend()
	h, p := newTestPortal(t, fb, portalTestState())

	form := url.Values{"lab_id": {"job-lab"}, "template_id": {"python"}}
	req := httptest.NewRequest(http.MethodPost, "/api/student/workspace/request", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.RequestWorkspace(w, asStudent(req, "alice@example.com"))

	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "success-message")
	require.Len(t, fb.Ensured, 1)
	assert.Equal(t, "python", fb.Ensured[0].Template)
	assert.Equal(t, "alice", fb.Ensured[0].Owner)
	assert.Equal(t, "lab.example.com", fb.Ensured[0].Domain)

	// Nothing is kept in the mirror, which the next sync would overwrite anyway.
	job, _ := h.jobManager.GetJob("job-lab")
	job.mu.RLock()
	assert.Empty(t, job.WorkspaceEvents)
	job.mu.RUnlock()

	p.Flush(context.Background())
	records := decodedOutbox(t, fb)
	require.Len(t, records[portalRecordWorkspaceEvent], 1)
	event := records[portalRecordWorkspaceEvent][0].WorkspaceEvent
	assert.Equal(t, WorkspaceEventCreated, event.Action)
	assert.Equal(t, "alice@example.com", event.Owner)
	assert.Equal(t, "python", event.Template)

	require.Len(t, records[portalRecordAudit], 1)
	audit := records[portalRecordAudit][0].Audit
	assert.Equal(t, "workspace.create", audit.Action)
	assert.Equal(t, "alice@example.com", audit.Actor)
}

// Feedback is only acknowledged once it is in the outbox.
func TestPortal_SubmitFeedback(t *testing.T) {
	t.Parallel()

	submit := func(h *Handler) *httptest.ResponseRecorder {
		form := url.Values{"lab_id": {"job-lab"}, "rating": {"4"}, "difficulty": {"just-right"}, "comment": {"Great"}}
		req := httptest.NewRequest(http.MethodPost, "/api/student/feedback", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("HX-Request", "true")
		w := httptest.NewRecorder()
		h.SubmitFeedback(w, asStudent(req, "alice@example.com"))
		return w
	}

	t.Run("saved", func(t *testing.T) {
		t.Parallel()
		fb := newFakePortalBackend()
		h, p := newTestPortal(t, fb, portalTestState())
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go p.Run(ctx)

		w := submit(h)
		assert.Contains(t, w.Body.String(), "toast-success")

		records := decodedOutbox(t, fb)
		require.Len(t, records[portalRecordFeedback], 1)
		f := records[portalRecordFeedback][0].Feedback
		assert.Equal(t, 4, f.Rating)
		assert.Equal(t, "alice@example.com", f.Email)
		assert.Equal(t, "Great", f.Comment)
	})

	t.Run("outbox unreachable", func(t *testing.T) {
		t.Parallel()
		fb := newFakePortalBackend()
		fb.appendErr = fmt.Errorf("connection refused to 10.0.0.1")
		h, p := newTestPortal(t, fb, portalTestState())
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go p.Run(ctx)

		w := submit(h)
		assert.Contains(t, w.Body.String(), "toast-error")
		assert.NotContains(t, w.Body.String(), "10.0.0.1")
		assert.Empty(t, fb.outboxRecords())

		// The student was told it failed; it must not be delivered behind their back.
		fb.portalMu.Lock()
		fb.appendErr = nil
		fb.portalMu.Unlock()
		p.Flush(context.Background())
		assert.Empty(t, fb.outboxRecords())
	})
}

// Reports nobody waits on survive an outbox that cannot be written for a while.
func TestPortalRuntime_FlushRetries(t *testing.T) {
	t.Parallel()
	fb := newFakePortalBackend()
	_, p := newTestPortal(t, fb, portalTestState())

	fb.portalMu.Lock()
	fb.appendErr = fmt.Errorf("api server unavailable")
	fb.portalMu.Unlock()

	p.reportAudit(AuditEntry{Action: "workspace.create", Actor: "alice@example.com"})
	p.reportWorkspaceEvent("job-lab", WorkspaceEvent{Action: WorkspaceEventCreated, WorkspaceID: "ws-alice"})
	p.Flush(context.Background())
	assert.Empty(t, fb.outboxRecords())

	fb.portalMu.Lock()
	fb.appendErr = nil
	fb.portalMu.Unlock()
	p.reportAudit(AuditEntry{Action: "workspace.delete", Actor: "alice@example.com"})
	p.Flush(context.Background())

	records := decodedOutbox(t, fb)
	assert.Len(t, records[portalRecordAudit], 2)
	assert.Len(t, records[portalRecordWorkspaceEvent], 1)

	// Nothing is written twice once it has been delivered.
	before := len(fb.outboxRecords())
	p.Flush(context.Background())
	assert.Len(t, fb.outboxRecords(), before)
}

func TestPortalRuntime_FlushDropsOldestPastTheCap(t *testing.T) {
	t.Parallel()
	fb := newFakePortalBackend()
	fb.appendErr = fmt.Errorf("api server unavailable")
	_, p := newTestPortal(t, fb, portalTestState())

	for i := 0; i < portalMaxPending+10; i++ {
		p.reportAudit(AuditEntry{Action: "workspace.create", Detail: fmt.Sprintf("ws-%d", i)})
	}
	p.Flush(context.Background())

	p.mu.Lock()
	defer p.mu.Unlock()
	require.Len(t, p.pending, portalMaxPending)
	assert.Contains(t, p.pending[0].data, "ws-10", "the oldest records are the ones dropped")
}

// Run flushes what is still queued when the portal shuts down.
func TestPortalRuntime_RunFlushesOnShutdown(t *testing.T) {
	t.Parallel()
	fb := newFakePortalBackend()
	_, p := newTestPortal(t, fb, portalTestState())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.Run(ctx)
	}()

	p.reportAudit(AuditEntry{Action: "workspace.create"})
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}
	assert.Len(t, fb.outboxRecords(), 1)
}
