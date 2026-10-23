package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"easylab/internal/providers/workspace"
)

// minWorkspaceIdleTimeoutMinutes is the shortest idle timeout a lab can set. The
// IDE's own activity heartbeat has a one-minute resolution and idle workspaces
// are only looked at once per cleanup interval, so anything shorter would pause
// students on what is little more than a slow network round-trip.
const minWorkspaceIdleTimeoutMinutes = 10

// normalizeIdleTimeout maps a submitted idle timeout onto a value the cleanup
// loop can act on: nothing or a negative number disables it, and a value under
// the minimum is raised to it rather than dropped — the admin asked for idle
// workspaces to be paused, and pausing them later is the safe reading of that.
func normalizeIdleTimeout(minutes int) int {
	if minutes <= 0 {
		return 0
	}
	if minutes < minWorkspaceIdleTimeoutMinutes {
		return minWorkspaceIdleTimeoutMinutes
	}
	return minutes
}

// parseIdleTimeout reads the idle timeout from a lifecycle form. Blank and 0
// both mean "never pause"; unlike normalizeIdleTimeout it rejects a value under
// the minimum, because this form can tell the admin why.
func parseIdleTimeout(r *http.Request) (int, error) {
	raw := strings.TrimSpace(r.FormValue("workspace_idle_timeout_minutes"))
	if raw == "" {
		return 0, nil
	}
	minutes, err := strconv.Atoi(raw)
	if err != nil || minutes < 0 {
		return 0, fmt.Errorf("Idle timeout must be a non-negative number")
	}
	if minutes > 0 && minutes < minWorkspaceIdleTimeoutMinutes {
		return 0, fmt.Errorf("Idle timeout must be 0 (disabled) or at least %d minutes", minWorkspaceIdleTimeoutMinutes)
	}
	return minutes, nil
}

// idleOutcome counts what one lab's idle pass did with each of its workspaces,
// so a single summary line can account for every workspace left running. A line
// per workspace would be a line a minute per student, so this is logged only
// when the pass actually found something idle (see worthLogging).
type idleOutcome struct {
	notReady    atomic.Int32 // building, starting, or already paused
	inGrace     atomic.Int32 // created or resumed less than one timeout ago
	ephemeral   atomic.Int32 // no persistent volume: pausing would lose the files
	inUse       atomic.Int32 // last activity more recent than the timeout
	unreadable  atomic.Int32 // activity could not be read, so treated as in use
	unsupported atomic.Int32 // the workspace's IDE reports no activity at all
	firstPass   atomic.Int32 // idle, waiting for a second consecutive reading
	paused      atomic.Int32
	failed      atomic.Int32
}

// worthLogging reports whether this pass found anything that would explain a
// workspace not being paused. A lab whose students are all working says nothing.
//
// unsupported is deliberately not in the sum: an IDE that reports no activity
// reports none on every pass forever, so counting it here would turn a permanent
// property of the lab into a log line every couple of minutes. Admins learn it
// from the lab's own settings instead (see docs/admin-lab-management.md).
func (o *idleOutcome) worthLogging() bool {
	return o.firstPass.Load()+o.paused.Load()+o.failed.Load()+o.unreadable.Load() > 0
}

func (o *idleOutcome) String() string {
	return fmt.Sprintf("paused %d, awaiting a second reading %d, stop failed %d, activity unreadable %d, activity not reported by the IDE %d, in use %d, within grace period %d, not running %d, no volume to keep %d",
		o.paused.Load(), o.firstPass.Load(), o.failed.Load(), o.unreadable.Load(), o.unsupported.Load(),
		o.inUse.Load(), o.inGrace.Load(), o.notReady.Load(), o.ephemeral.Load())
}

// stopIdleWorkspaces pauses the workspaces of every lab with an idle timeout
// that nobody has used for longer than it.
//
// Pausing someone mid-work is the failure this is built to avoid, so every doubt
// resolves to leaving the workspace alone: activity that cannot be read counts as
// activity, a workspace that is not fully up is never a candidate, and one idle
// reading is not enough — a workspace is only paused when it was already found
// idle on the previous pass too, on a fresh reading taken right before the stop.
//
// That second reading is why this runs on idleCheckInterval rather than the
// cleanup tick: the wait it adds beyond the admin's configured timeout is one
// interval, so it has to be short.
func (h *Handler) stopIdleWorkspaces() {
	// Strikes only carry from one pass to the very next: a lab this pass skips
	// (unreachable cluster, listing error) starts over rather than being paused on
	// an old reading.
	h.idleStrikesMu.Lock()
	previous := h.idleStrikes
	h.idleStrikes = map[string]bool{}
	h.idleStrikesMu.Unlock()

	for _, job := range h.jobManager.GetAllJobs() {
		job.mu.RLock()
		status := job.Status
		kubeconfig := extractStringFromConfigValue(job.Kubeconfig)
		namespace := job.workspaceNamespace()
		timeoutMinutes := 0
		if job.Config != nil {
			timeoutMinutes = job.Config.WorkspaceIdleTimeoutMinutes
		}
		job.mu.RUnlock()

		// A lab with no idle timeout, or one that is not up, is not something the
		// admin is waiting on — stay quiet about those. Everything past this point
		// is a lab that asked for pausing, so a reason is logged whenever one of
		// its workspaces is left running.
		if timeoutMinutes <= 0 || status != JobStatusCompleted {
			continue
		}
		if kubeconfig == "" {
			log.Printf("[idle] skipping job %s: lab has no kubeconfig", job.ID)
			continue
		}

		backend, err := h.workspaceBackendFor(job.ID, kubeconfig, namespace)
		if err != nil {
			log.Printf("[idle] skipping job %s: failed to build backend: %v", job.ID, err)
			continue
		}
		idler, ok := backend.(workspace.IdleManager)
		if !ok {
			log.Printf("[idle] skipping job %s: %T cannot pause workspaces", job.ID, backend)
			continue
		}

		reachCtx, cancel := context.WithTimeout(context.Background(), clusterReachabilityTimeout)
		reachable := backend.Reachable(reachCtx)
		cancel()
		if !reachable {
			log.Printf("[idle] skipping job %s: cluster API unreachable", job.ID)
			continue
		}

		listCtx, listCancel := context.WithTimeout(context.Background(), clusterOperationTimeout)
		workspaces, err := backend.ListWorkspaces(listCtx, job.ID)
		listCancel()
		if err != nil {
			log.Printf("[idle] failed to list workspaces for job %s: %v", job.ID, err)
			continue
		}

		timeout := time.Duration(timeoutMinutes) * time.Minute
		var outcome idleOutcome
		var wg sync.WaitGroup
		sem := make(chan struct{}, cleanupDeleteConcurrency())
		for _, ws := range workspaces {
			// Not ready means building, starting or already paused.
			if !ws.Ready {
				outcome.notReady.Add(1)
				continue
			}
			// A recent UpdatedAt means just created or just resumed: the student
			// has not had a full timeout to come back to it yet.
			if time.Since(ws.UpdatedAt) <= timeout {
				outcome.inGrace.Add(1)
				continue
			}
			// StopWorkspace would refuse this one anyway — its files live in an
			// EmptyDir and would not survive the pause. Skipping it here rather
			// than attempting the stop keeps a failure the admin cannot act on out
			// of the log on every single pass.
			if ws.Ephemeral {
				outcome.ephemeral.Add(1)
				continue
			}

			wg.Add(1)
			sem <- struct{}{}
			go func(ws workspace.Workspace) {
				defer wg.Done()
				defer func() { <-sem }()

				idle, err := workspaceIsIdle(idler, ws, timeout)
				switch {
				// An IDE with no activity signal can never be shown to be idle, so
				// it is never a candidate. No strike is recorded, so the
				// two-reading gate below cannot fire for it however long the lab
				// runs — the workspace is bounded by its lifetime and by manual
				// pause instead.
				case errors.Is(err, workspace.ErrActivityUnsupported):
					outcome.unsupported.Add(1)
					return
				case err != nil:
					outcome.unreadable.Add(1)
					return
				case !idle:
					outcome.inUse.Add(1)
					return
				}
				key := job.ID + "/" + ws.ID
				if !previous[key] {
					outcome.firstPass.Add(1)
					h.recordIdleStrike(key)
					return
				}

				log.Printf("[idle] pausing workspace %s in job %s: idle for more than %d min", ws.Name, job.ID, timeoutMinutes)
				stopCtx, stopCancel := context.WithTimeout(context.Background(), clusterOperationTimeout)
				stopErr := idler.StopWorkspace(stopCtx, ws.ID)
				stopCancel()
				if stopErr != nil {
					log.Printf("[idle] failed to pause workspace %s: %v", ws.ID, stopErr)
					outcome.failed.Add(1)
					// Keep the strike so the next pass retries the stop, rather
					// than starting the two-reading count over from scratch.
					h.recordIdleStrike(key)
					return
				}
				outcome.paused.Add(1)
				h.recordAudit("system", "system", "workspace.stop", job.ID, fmt.Sprintf("%s: auto-pause, idle for more than %d min", ws.Name, timeoutMinutes))
			}(ws)
		}
		wg.Wait()
		if outcome.worthLogging() {
			log.Printf("[idle] job %s (%d min timeout), %d workspace(s): %s", job.ID, timeoutMinutes, len(workspaces), &outcome)
		}
	}
}

// recordIdleStrike remembers that a workspace read as idle on this pass, so the
// next one can pause it.
func (h *Handler) recordIdleStrike(key string) {
	h.idleStrikesMu.Lock()
	h.idleStrikes[key] = true
	h.idleStrikesMu.Unlock()
}

// workspaceIsIdle reports whether a running workspace has gone unused for longer
// than timeout. Activity that cannot be read is not idleness: the error is
// returned so the caller leaves the workspace alone and can say why.
func workspaceIsIdle(idler workspace.IdleManager, ws workspace.Workspace, timeout time.Duration) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), clusterOperationTimeout)
	defer cancel()
	last, err := idler.WorkspaceLastActivity(ctx, ws.ID)
	if err != nil {
		// An IDE that reports no activity is not a failed reading: it would log
		// here for every workspace on every pass, saying nothing new each time.
		// The caller counts it instead.
		if !errors.Is(err, workspace.ErrActivityUnsupported) {
			log.Printf("[idle] leaving workspace %s alone: %v", ws.ID, err)
		}
		return false, err
	}
	// Never used since it started: count from when it came up instead.
	if last.Before(ws.UpdatedAt) {
		last = ws.UpdatedAt
	}
	return time.Since(last) > timeout, nil
}

// resumeIfStopped starts ws again when it was paused for inactivity, and
// reports whether it did.
func resumeIfStopped(ctx context.Context, backend workspace.Backend, ws workspace.Workspace) (bool, error) {
	if ws.Phase != workspace.PhaseStopped {
		return false, nil
	}
	idler, ok := backend.(workspace.IdleManager)
	if !ok {
		return false, fmt.Errorf("backend cannot resume workspace %s", ws.Name)
	}
	if err := idler.StartWorkspace(ctx, ws.ID); err != nil {
		return false, err
	}
	return true, nil
}

// ResumeWorkspace handles POST /api/student/workspace/resume: it starts a
// workspace that was paused for inactivity and answers with the same status
// partial WorkspaceStatus serves, so the page goes straight back to polling.
func (h *Handler) ResumeWorkspace(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	labID := r.FormValue("lab_id")
	workspaceName := r.FormValue("workspace_name")
	// The identity is always the authenticated student — never trusted from the client.
	email := studentEmailFromContext(r)

	if labID == "" || workspaceName == "" || email == "" {
		http.Error(w, "lab_id and workspace_name are required and you must be logged in", http.StatusBadRequest)
		return
	}

	job, exists := h.jobManager.GetJob(labID)
	if !exists {
		http.Error(w, "lab not found", http.StatusNotFound)
		return
	}

	job.mu.RLock()
	kubeconfig := extractStringFromConfigValue(job.Kubeconfig)
	namespace := job.workspaceNamespace()
	job.mu.RUnlock()

	if kubeconfig == "" {
		http.Error(w, "lab is not ready", http.StatusServiceUnavailable)
		return
	}

	backend, err := h.workspaceBackendFor(labID, kubeconfig, namespace)
	if err != nil {
		log.Printf("ResumeWorkspace: failed to build backend for lab %s: %v", labID, err)
		http.Error(w, "lab is not ready", http.StatusServiceUnavailable)
		return
	}

	ws, err := backend.GetWorkspace(r.Context(), workspaceName)
	// Authorization: a student may only resume their own workspace.
	if err != nil || !workspaceOwnedBy(ws, email) {
		log.Printf("ResumeWorkspace: lookup/authz failed workspace=%s owner=%s in lab %s: %v", workspaceName, email, labID, err)
		http.Error(w, "workspace not available", http.StatusServiceUnavailable)
		return
	}

	resumed, err := resumeIfStopped(r.Context(), backend, ws)
	if err != nil {
		log.Printf("ResumeWorkspace: failed to resume workspace %s in lab %s: %v", ws.Name, labID, err)
		http.Error(w, "workspace could not be resumed", http.StatusServiceUnavailable)
		return
	}
	if resumed {
		h.recordAudit(email, "student", "workspace.resume", labID, ws.Name)
	}

	w.Header().Set("Content-Type", "text/html")
	fmt.Fprint(w, buildWorkspaceStatusHTML(labID, ws.Name, "resuming", "", ws.IDE))
}
