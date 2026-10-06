package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"easylab/internal/providers/workspace"
)

const (
	// portalReloadInterval is how often an in-lab portal re-reads its state, and so
	// the longest an admin's change (a closed template, a new one) takes to reach
	// students.
	portalReloadInterval = 10 * time.Second
	// portalFlushInterval is how often queued reports are written to the outbox
	// when nothing asked for an immediate flush.
	portalFlushInterval = 2 * time.Second
	// portalClusterTimeout bounds one read or write against the portal's cluster.
	portalClusterTimeout = 10 * time.Second
	// portalMaxPending caps the reports held in memory while the outbox cannot be
	// written. Past it the oldest are dropped: an unreachable API server must not
	// grow the portal until it is killed.
	portalMaxPending = 5000
)

// pendingPortalRecord is a report waiting to be written to the outbox. done is
// non-nil when its producer is waiting to learn whether the write succeeded.
type pendingPortalRecord struct {
	id   string
	data string
	done chan error
}

// PortalRuntime is what turns the server into the student portal of one lab,
// running inside that lab's cluster (ModeStudent). It keeps a read-only mirror of
// the lab — synced by the admin, see portal.go — in the JobManager the student
// handlers already read from, and reports what happens here (workspaces created
// and deleted, feedback, audit entries) through an outbox the admin drains.
type PortalRuntime struct {
	labID      string
	backend    workspace.Backend
	runtime    workspace.PortalRuntime
	jobManager *JobManager
	auth       *AuthHandler

	mu      sync.Mutex
	pending []pendingPortalRecord
	// kick wakes the flush loop early; buffered so a producer never blocks on it.
	kick chan struct{}
}

// NewPortalRuntime builds the runtime for labID on top of a backend reaching the
// portal's own cluster. auth may be nil (tests that do not exercise sign-in).
func NewPortalRuntime(labID string, backend workspace.Backend, jobManager *JobManager, auth *AuthHandler) (*PortalRuntime, error) {
	if labID == "" {
		return nil, fmt.Errorf("portal lab ID is required")
	}
	rt, ok := backend.(workspace.PortalRuntime)
	if !ok {
		return nil, fmt.Errorf("workspace backend cannot host a student portal")
	}
	p := &PortalRuntime{
		labID:      labID,
		backend:    backend,
		runtime:    rt,
		jobManager: jobManager,
		auth:       auth,
		kick:       make(chan struct{}, 1),
	}
	jobManager.workspaceEventSink = p.reportWorkspaceEvent
	return p, nil
}

// UsePortalRuntime makes the handler serve students as an in-lab portal: every
// lab resolves to the portal's own cluster, and what the student handlers record
// is reported to the admin instead of stored here.
func (h *Handler) UsePortalRuntime(p *PortalRuntime) {
	h.portal = p
	h.newWorkspaceBackend = func(_, _ string) (workspace.Backend, error) { return p.backend, nil }
}

// Load reads the lab state the admin last synced and applies it.
func (p *PortalRuntime) Load(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, portalClusterTimeout)
	defer cancel()

	config, err := p.runtime.ReadPortalConfig(ctx, p.labID)
	if err != nil {
		return fmt.Errorf("failed to read portal state: %w", err)
	}
	state, err := decodePortalState(config)
	if err != nil {
		return err
	}
	if state.LabID != p.labID {
		return fmt.Errorf("portal state is for lab %s, not %s", state.LabID, p.labID)
	}
	p.jobManager.setPortalJob(state)
	if p.auth != nil {
		p.auth.ConfigurePortalAuth(state.LabID, state.Auth)
	}
	return nil
}

// Run keeps the state current and the outbox flushed until ctx is done, then
// writes out whatever is still queued.
func (p *PortalRuntime) Run(ctx context.Context) {
	reload := time.NewTicker(portalReloadInterval)
	defer reload.Stop()
	flush := time.NewTicker(portalFlushInterval)
	defer flush.Stop()

	// A state that cannot be read yet (the admin is still deploying the portal) is
	// retried on the ticker; students see no lab until it arrives.
	loaded := false
	load := func() {
		if err := p.Load(ctx); err != nil {
			log.Printf("[portal] %v", err)
			return
		}
		if !loaded {
			loaded = true
			log.Printf("[portal] serving lab %s", p.labID)
		}
	}
	load()

	for {
		select {
		case <-ctx.Done():
			p.Flush(context.Background())
			return
		case <-reload.C:
			load()
		case <-flush.C:
			p.Flush(ctx)
		case <-p.kick:
			p.Flush(ctx)
		}
	}
}

// setPortalJob installs state as the lab the portal serves, replacing what an
// earlier sync installed. An existing job is updated in place: handlers hold on
// to the *Job across a request.
func (jm *JobManager) setPortalJob(state PortalState) {
	config := state.labConfig()

	jm.mu.Lock()
	defer jm.mu.Unlock()

	job, exists := jm.jobs[state.LabID]
	if !exists {
		jm.jobs[state.LabID] = &Job{
			ID:                state.LabID,
			Status:            JobStatusCompleted,
			CreatedAt:         state.CreatedAt,
			UpdatedAt:         time.Now(),
			Output:            []string{},
			Config:            config,
			Kubeconfig:        portalKubeconfigSentinel,
			WorkspaceAccesses: state.WorkspaceAccesses,
			DeletionRetries:   make(map[string]*WorkspaceDeletionRetry),
		}
		return
	}
	job.mu.Lock()
	job.Config = config
	job.WorkspaceAccesses = state.WorkspaceAccesses
	job.UpdatedAt = time.Now()
	job.mu.Unlock()
}

// enqueue queues a report for the next flush. With wait it asks for an immediate
// flush and returns a channel carrying that write's result.
func (p *PortalRuntime) enqueue(record portalRecord, wait bool) (<-chan error, error) {
	data, err := json.Marshal(record)
	if err != nil {
		return nil, fmt.Errorf("failed to encode portal record: %w", err)
	}
	id, err := newPortalRecordID(time.Now())
	if err != nil {
		return nil, err
	}
	entry := pendingPortalRecord{id: id, data: string(data)}
	if wait {
		entry.done = make(chan error, 1)
	}

	p.mu.Lock()
	p.pending = append(p.pending, entry)
	p.mu.Unlock()

	if wait {
		select {
		case p.kick <- struct{}{}:
		default:
		}
	}
	return entry.done, nil
}

// Flush writes every queued report to the outbox in one update. Reports nobody
// is waiting on are kept for the next flush when the write fails; a waiting
// producer gets the error instead and decides for itself.
func (p *PortalRuntime) Flush(ctx context.Context) {
	p.mu.Lock()
	batch := p.pending
	p.pending = nil
	p.mu.Unlock()
	if len(batch) == 0 {
		return
	}

	records := make(map[string]string, len(batch))
	for _, entry := range batch {
		records[entry.id] = entry.data
	}
	ctx, cancel := context.WithTimeout(ctx, portalClusterTimeout)
	err := p.runtime.AppendPortalOutbox(ctx, p.labID, records)
	cancel()

	var retry []pendingPortalRecord
	for _, entry := range batch {
		switch {
		case entry.done != nil:
			entry.done <- err
		case err != nil:
			retry = append(retry, entry)
		}
	}
	if err == nil {
		return
	}
	log.Printf("[portal] failed to report %d record(s) to the admin: %v", len(batch), err)

	p.mu.Lock()
	p.pending = append(retry, p.pending...)
	if dropped := len(p.pending) - portalMaxPending; dropped > 0 {
		log.Printf("[portal] dropping the %d oldest unreported record(s)", dropped)
		p.pending = p.pending[dropped:]
	}
	p.mu.Unlock()
}

// reportWorkspaceEvent is the JobManager's workspaceEventSink on a portal.
func (p *PortalRuntime) reportWorkspaceEvent(_ string, event WorkspaceEvent) {
	if _, err := p.enqueue(portalRecord{Kind: portalRecordWorkspaceEvent, WorkspaceEvent: &event}, false); err != nil {
		log.Printf("[portal] failed to queue workspace event for %s: %v", event.WorkspaceName, err)
	}
}

// reportAudit queues an audit entry for the admin's audit log.
func (p *PortalRuntime) reportAudit(entry AuditEntry) {
	if _, err := p.enqueue(portalRecord{Kind: portalRecordAudit, Audit: &entry}, false); err != nil {
		log.Printf("[portal] failed to queue audit entry %s: %v", entry.Action, err)
	}
}

// reportFeedback hands a student's feedback to the admin, and only returns once
// it is safely in the outbox: the student is told it was saved.
func (p *PortalRuntime) reportFeedback(ctx context.Context, f Feedback) error {
	done, err := p.enqueue(portalRecord{Kind: portalRecordFeedback, Feedback: &f}, true)
	if err != nil {
		return err
	}
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
