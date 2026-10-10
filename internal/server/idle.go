package server

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
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

// stopIdleWorkspaces pauses the workspaces of every lab with an idle timeout
// that nobody has used for longer than it.
//
// Pausing someone mid-work is the failure this is built to avoid, so every doubt
// resolves to leaving the workspace alone: activity that cannot be read counts as
// activity, a workspace that is not fully up is never a candidate, and one idle
// reading is not enough — a workspace is only paused when it was already found
// idle on the previous pass too, on a fresh reading taken right before the stop.
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

		if status != JobStatusCompleted || kubeconfig == "" || timeoutMinutes <= 0 {
			continue
		}

		backend, err := h.workspaceBackendFor(job.ID, kubeconfig, namespace)
		if err != nil {
			log.Printf("[idle] skipping job %s: failed to build backend: %v", job.ID, err)
			continue
		}
		idler, ok := backend.(workspace.IdleManager)
		if !ok {
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
		var wg sync.WaitGroup
		sem := make(chan struct{}, cleanupDeleteConcurrency())
		for _, ws := range workspaces {
			// Not ready means building, starting or already paused. A recent
			// UpdatedAt means just created or just resumed: the student has not
			// had a full timeout to come back to it yet.
			if !ws.Ready || time.Since(ws.UpdatedAt) <= timeout {
				continue
			}

			wg.Add(1)
			sem <- struct{}{}
			go func(ws workspace.Workspace) {
				defer wg.Done()
				defer func() { <-sem }()

				if !workspaceIsIdle(idler, ws, timeout) {
					return
				}
				key := job.ID + "/" + ws.ID
				if !previous[key] {
					h.idleStrikesMu.Lock()
					h.idleStrikes[key] = true
					h.idleStrikesMu.Unlock()
					return
				}

				log.Printf("[idle] pausing workspace %s in job %s: idle for more than %d min", ws.Name, job.ID, timeoutMinutes)
				stopCtx, stopCancel := context.WithTimeout(context.Background(), clusterOperationTimeout)
				stopErr := idler.StopWorkspace(stopCtx, ws.ID)
				stopCancel()
				if stopErr != nil {
					log.Printf("[idle] failed to pause workspace %s: %v", ws.ID, stopErr)
					return
				}
				h.recordAudit("system", "system", "workspace.stop", job.ID, fmt.Sprintf("%s: auto-pause, idle for more than %d min", ws.Name, timeoutMinutes))
			}(ws)
		}
		wg.Wait()
	}
}

// workspaceIsIdle reports whether a running workspace has gone unused for longer
// than timeout. Activity that cannot be read is not idleness.
func workspaceIsIdle(idler workspace.IdleManager, ws workspace.Workspace, timeout time.Duration) bool {
	ctx, cancel := context.WithTimeout(context.Background(), clusterOperationTimeout)
	defer cancel()
	last, err := idler.WorkspaceLastActivity(ctx, ws.ID)
	if err != nil {
		log.Printf("[idle] leaving workspace %s alone: %v", ws.ID, err)
		return false
	}
	// Never used since it started: count from when it came up instead.
	if last.Before(ws.UpdatedAt) {
		last = ws.UpdatedAt
	}
	return time.Since(last) > timeout
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
	// The owner is always the authenticated student — never trusted from the client.
	email := studentEmailFromContext(r)
	owner := usernameFromEmail(email)

	if labID == "" || workspaceName == "" || owner == "" {
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
	if err != nil || ws.Owner != owner {
		log.Printf("ResumeWorkspace: lookup/authz failed workspace=%s owner=%s in lab %s: %v", workspaceName, owner, labID, err)
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
	fmt.Fprint(w, buildWorkspaceStatusHTML(labID, ws.Name, "resuming", ""))
}
