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

func localHelpCount(p *PortalRuntime) int {
	p.localHelpMu.Lock()
	defer p.localHelpMu.Unlock()
	return len(p.localHelp)
}

// Raising a request on a portal reports it to the admin and remembers it, so the
// student keeps seeing it until the admin has been seen to hold it.
func TestPortalRuntime_ReportHelpRequest(t *testing.T) {
	t.Parallel()

	fb := newFakePortalBackend()
	_, p := newTestPortal(t, fb, portalTestState())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	hr := HelpRequest{ID: "r1", At: time.Now(), Student: "alice@example.com", Message: "stuck", Status: HelpStatusWaiting}
	require.NoError(t, p.reportHelpRequest(ctx, hr))

	records := decodedOutbox(t, fb)
	require.Len(t, records[portalRecordHelpRequest], 1)
	assert.Equal(t, "r1", records[portalRecordHelpRequest][0].HelpRequest.ID)
	assert.Equal(t, 1, localHelpCount(p))

	// A withdrawal is the same record, flagged, and forgets the local copy.
	hr.Cancelled = true
	require.NoError(t, p.reportHelpRequest(ctx, hr))
	records = decodedOutbox(t, fb)
	require.Len(t, records[portalRecordHelpRequest], 2)
	assert.Equal(t, 0, localHelpCount(p))
}

// An outbox that cannot be written must not leave the student believing someone
// has been told.
func TestPortalRuntime_ReportHelpRequest_OutboxUnreachable(t *testing.T) {
	t.Parallel()

	fb := newFakePortalBackend()
	fb.appendErr = fmt.Errorf("connection refused to 10.0.0.1")
	_, p := newTestPortal(t, fb, portalTestState())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	err := p.reportHelpRequest(ctx, HelpRequest{ID: "r1", At: time.Now(), Student: "alice@example.com"})
	require.Error(t, err)
	assert.Equal(t, 0, localHelpCount(p), "a request that never left is not remembered as raised")
}

func TestPortalRuntime_ReportHelpRequest_ContextDone(t *testing.T) {
	t.Parallel()

	_, p := newTestPortal(t, newFakePortalBackend(), portalTestState())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Nothing is flushing the outbox, and the caller has given up.
	err := p.reportHelpRequest(ctx, HelpRequest{ID: "r1", At: time.Now(), Student: "alice@example.com"})
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 0, localHelpCount(p))
}

// The portal's lab is a mirror the admin overwrites every few seconds, so a
// request raised here has to survive a sync that does not carry it yet — and only
// until the admin has been seen to hold it, or been given up on.
func TestPortalRuntime_ReapplyLocalHelp(t *testing.T) {
	t.Parallel()

	now := time.Now()
	raised := HelpRequest{ID: "r1", At: now.Add(-time.Minute), Student: "alice@example.com", Message: "stuck", Status: HelpStatusWaiting}

	t.Run("survives a sync that does not carry it", func(t *testing.T) {
		t.Parallel()
		h, p := newTestPortal(t, newFakePortalBackend(), portalTestState())
		p.localHelp[raised.ID] = raised

		// A sync overwrites the queue, then the runtime puts the request back.
		state := portalTestState()
		h.jobManager.setPortalJob(state)
		require.Empty(t, storedHelpRequests(t, h.jobManager, "job-lab"))
		p.reapplyLocalHelp(state)

		stored := storedHelpRequests(t, h.jobManager, "job-lab")
		require.Len(t, stored, 1)
		assert.Equal(t, "r1", stored[0].ID)
		assert.Equal(t, 1, localHelpCount(p), "still waiting to be seen in a synced state")
	})

	t.Run("is dropped once seen in a synced state", func(t *testing.T) {
		t.Parallel()
		_, p := newTestPortal(t, newFakePortalBackend(), portalTestState())
		p.localHelp[raised.ID] = raised

		synced := raised
		synced.Status = HelpStatusHandling
		synced.AcknowledgedBy = "teacher@example.com"
		state := portalTestState()
		state.HelpRequests = []HelpRequest{synced}
		p.jobManager.setPortalJob(state)
		p.reapplyLocalHelp(state)

		assert.Equal(t, 0, localHelpCount(p))
		stored := storedHelpRequests(t, p.jobManager, "job-lab")
		require.Len(t, stored, 1)
		assert.Equal(t, HelpStatusHandling, stored[0].Status, "the synced copy is the authoritative one")
	})

	t.Run("an admin resolving it clears it for good", func(t *testing.T) {
		t.Parallel()
		_, p := newTestPortal(t, newFakePortalBackend(), portalTestState())
		p.localHelp[raised.ID] = raised

		// First sync: the admin holds it.
		withIt := portalTestState()
		withIt.HelpRequests = []HelpRequest{raised}
		p.jobManager.setPortalJob(withIt)
		p.reapplyLocalHelp(withIt)
		require.Len(t, storedHelpRequests(t, p.jobManager, "job-lab"), 1)

		// Second sync: the admin resolved it, so it is simply not there — and must
		// not be resurrected from the local copy.
		resolved := portalTestState()
		p.jobManager.setPortalJob(resolved)
		p.reapplyLocalHelp(resolved)
		assert.Empty(t, storedHelpRequests(t, p.jobManager, "job-lab"))
		assert.Equal(t, 0, localHelpCount(p))
	})

	t.Run("is given up on past the grace period", func(t *testing.T) {
		t.Parallel()
		_, p := newTestPortal(t, newFakePortalBackend(), portalTestState())
		neglected := raised
		neglected.At = now.Add(-localHelpGrace - time.Second)
		p.localHelp[neglected.ID] = neglected

		state := portalTestState()
		p.jobManager.setPortalJob(state)
		p.reapplyLocalHelp(state)

		assert.Empty(t, storedHelpRequests(t, p.jobManager, "job-lab"))
		assert.Equal(t, 0, localHelpCount(p))
	})

	t.Run("one just inside the grace period is kept", func(t *testing.T) {
		t.Parallel()
		_, p := newTestPortal(t, newFakePortalBackend(), portalTestState())
		fresh := raised
		fresh.At = now.Add(-localHelpGrace + time.Minute)
		p.localHelp[fresh.ID] = fresh

		state := portalTestState()
		p.jobManager.setPortalJob(state)
		p.reapplyLocalHelp(state)

		assert.Len(t, storedHelpRequests(t, p.jobManager, "job-lab"), 1)
		assert.Equal(t, 1, localHelpCount(p))
	})

	t.Run("nothing local is a no-op", func(t *testing.T) {
		t.Parallel()
		_, p := newTestPortal(t, newFakePortalBackend(), portalTestState())
		state := portalTestState()
		state.HelpRequests = []HelpRequest{raised}
		p.jobManager.setPortalJob(state)
		p.reapplyLocalHelp(state)
		assert.Len(t, storedHelpRequests(t, p.jobManager, "job-lab"), 1)
	})

	t.Run("a mix: held ones go, the uncollected one stays", func(t *testing.T) {
		t.Parallel()
		_, p := newTestPortal(t, newFakePortalBackend(), portalTestState())
		other := HelpRequest{ID: "r2", At: now.Add(-30 * time.Second), Student: "bob@example.com", Status: HelpStatusWaiting}
		p.localHelp[raised.ID] = raised
		p.localHelp[other.ID] = other

		state := portalTestState()
		state.HelpRequests = []HelpRequest{raised}
		p.jobManager.setPortalJob(state)
		p.reapplyLocalHelp(state)

		stored := storedHelpRequests(t, p.jobManager, "job-lab")
		require.Len(t, stored, 2)
		assert.Equal(t, 1, localHelpCount(p))
		_, stillLocal := p.localHelp["r2"]
		assert.True(t, stillLocal)
	})
}

// Load puts the request back after installing the admin's state, which would
// otherwise wipe it from the student's own screen.
func TestPortalRuntime_Load_ReappliesLocalHelp(t *testing.T) {
	t.Parallel()

	fb := newFakePortalBackend()
	h, p := newTestPortal(t, fb, portalTestState())
	hr := HelpRequest{ID: "r1", At: time.Now(), Student: "alice@example.com", Message: "stuck", Status: HelpStatusWaiting}
	p.localHelp[hr.ID] = hr

	require.NoError(t, p.Load(context.Background()))

	mine, ok := h.jobManager.StudentHelpRequest("job-lab", "alice@example.com")
	require.True(t, ok)
	assert.Equal(t, "stuck", mine.Message)
}

func TestSetPortalJob_HelpRequests(t *testing.T) {
	t.Parallel()

	req := func(id string) []HelpRequest {
		return []HelpRequest{{ID: id, At: time.Now(), Student: "alice@example.com"}}
	}

	jm := NewJobManager("")
	jm.setPortalJob(PortalState{LabID: "lab-1", HelpRequests: req("created")})
	job, ok := jm.GetJob("lab-1")
	require.True(t, ok)
	assert.Equal(t, "created", storedHelpRequests(t, jm, "lab-1")[0].ID, "the create branch takes the state's queue")

	jm.setPortalJob(PortalState{LabID: "lab-1", HelpRequests: req("updated")})
	again, _ := jm.GetJob("lab-1")
	assert.Same(t, job, again)
	assert.Equal(t, "updated", storedHelpRequests(t, jm, "lab-1")[0].ID, "the in-place branch replaces it")

	jm.setPortalJob(PortalState{LabID: "lab-1"})
	assert.Empty(t, storedHelpRequests(t, jm, "lab-1"))
}
