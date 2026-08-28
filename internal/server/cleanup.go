package server

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"easylab/internal/providers/workspace"
)

// cleanupInterval returns the workspace cleanup interval, configurable via CLEANUP_INTERVAL_MINUTES env var.
// Defaults to 5 minutes.
func cleanupInterval() time.Duration {
	if v := os.Getenv("CLEANUP_INTERVAL_MINUTES"); v != "" {
		if mins, err := strconv.Atoi(v); err == nil && mins > 0 {
			return time.Duration(mins) * time.Minute
		}
	}
	return 5 * time.Minute
}

// deletionRetryMaxRetries returns the maximum number of deletion retry attempts,
// configurable via CLEANUP_DELETE_MAX_RETRIES env var. Defaults to 3.
func deletionRetryMaxRetries() int {
	if v := os.Getenv("CLEANUP_DELETE_MAX_RETRIES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 3
}

// deletionRetryInterval returns the minimum duration between deletion retry attempts,
// configurable via CLEANUP_DELETE_RETRY_INTERVAL_HOURS env var. Defaults to 2 hours.
func deletionRetryInterval() time.Duration {
	if v := os.Getenv("CLEANUP_DELETE_RETRY_INTERVAL_HOURS"); v != "" {
		if h, err := strconv.Atoi(v); err == nil && h > 0 {
			return time.Duration(h) * time.Hour
		}
	}
	return 2 * time.Hour
}

// clusterReachabilityTimeout bounds how long we probe the cluster API before skipping cleanup for a job.
const clusterReachabilityTimeout = 5 * time.Second

// clusterOperationTimeout bounds how long a single list/delete call against a
// lab's cluster is allowed to take during cleanup. Unlike clusterReachabilityTimeout
// (a quick up/down probe), listing or deleting workspaces does real work, so this
// is longer — but still bounded, so a cluster that degrades between the
// reachability probe and this call can no longer stall the whole cleanup tick.
const clusterOperationTimeout = 30 * time.Second

// cleanupDeleteConcurrency returns the maximum number of workspace deletions
// run at once per job during a cleanup pass, configurable via
// CLEANUP_DELETE_CONCURRENCY env var. Defaults to 5. Deliberately a separate
// budget from workspaceCreateSem: this runs on a background goroutine, not an
// interactive request, so it must not make interactive workspace-request
// latency depend on unrelated background cleanup activity.
func cleanupDeleteConcurrency() int {
	if v := os.Getenv("CLEANUP_DELETE_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 5
}

// StartWorkspaceCleanup starts a background goroutine that periodically deletes
// workspaces that have exceeded their configured lifetime and destroys labs past
// their scheduled deletion date.
func (h *Handler) StartWorkspaceCleanup(ctx context.Context) {
	interval := cleanupInterval()
	log.Printf("[cleanup] cleanup batch interval: %v (set CLEANUP_INTERVAL_MINUTES to override)", interval)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.cleanupExpiredWorkspaces()
			h.cleanupExpiredLabs()
		}
	}
}

// cleanupExpiredWorkspaces iterates all jobs and deletes workspaces older than
// their configured WorkspaceLifetimeHours.
func (h *Handler) cleanupExpiredWorkspaces() {
	jobs := h.jobManager.GetAllJobs()
	for _, job := range jobs {
		if job.Status != JobStatusCompleted {
			continue
		}
		if job.Kubeconfig == "" || job.Config == nil || job.Config.WorkspaceLifetimeHours <= 0 {
			continue
		}

		backend, err := h.workspaceBackendFor(job.ID, extractStringFromConfigValue(job.Kubeconfig), job.workspaceNamespace())
		if err != nil {
			log.Printf("[cleanup] skipping job %s: failed to build backend: %v", job.ID, err)
			continue
		}

		reachCtx, cancel := context.WithTimeout(context.Background(), clusterReachabilityTimeout)
		reachable := backend.Reachable(reachCtx)
		cancel()
		if !reachable {
			log.Printf("[cleanup] skipping job %s: cluster API unreachable", job.ID)
			continue
		}

		lifetime := time.Duration(job.Config.WorkspaceLifetimeHours) * time.Hour
		listCtx, listCancel := context.WithTimeout(context.Background(), clusterOperationTimeout)
		workspaces, err := backend.ListWorkspaces(listCtx, job.ID)
		listCancel()
		if err != nil {
			log.Printf("[cleanup] failed to list workspaces for job %s: %v", job.ID, err)
			continue
		}
		// Record current workspace count before any deletions.
		if err := h.jobManager.RecordWorkspaceSnapshot(job.ID, len(workspaces)); err != nil {
			log.Printf("[cleanup] failed to record workspace snapshot for job %s: %v", job.ID, err)
		}

		maxRetries := deletionRetryMaxRetries()
		retryInterval := deletionRetryInterval()

		// Bound and parallelize per-workspace deletes within this job: sequential
		// deletes made a job with many simultaneously-expiring workspaces stall
		// every other job behind it in the outer loop for the rest of this tick.
		var deleted atomic.Int32
		var wg sync.WaitGroup
		deleteSem := make(chan struct{}, cleanupDeleteConcurrency())
		for _, ws := range workspaces {
			if time.Since(ws.CreatedAt) <= lifetime {
				continue
			}
			wsID := ws.ID

			job.mu.RLock()
			retry := job.DeletionRetries[wsID]
			job.mu.RUnlock()

			if retry != nil && retry.GiveUp {
				log.Printf("[cleanup] skipping workspace %s (%s) in job %s: max retries (%d) exhausted", ws.Name, wsID, job.ID, maxRetries)
				continue
			}
			if retry != nil && time.Since(retry.LastAttempt) < retryInterval {
				remaining := (retryInterval - time.Since(retry.LastAttempt)).Round(time.Minute)
				log.Printf("[cleanup] skipping workspace %s (%s) in job %s: next retry in %v (attempt %d/%d)", ws.Name, wsID, job.ID, remaining, retry.Attempts, maxRetries)
				continue
			}

			wg.Add(1)
			deleteSem <- struct{}{}
			go func(ws workspace.Workspace, wsID string) {
				defer wg.Done()
				defer func() { <-deleteSem }()

				log.Printf("[cleanup] deleting workspace %s (%s) in job %s: exceeded %dh lifetime", ws.Name, wsID, job.ID, job.Config.WorkspaceLifetimeHours)
				delCtx, delCancel := context.WithTimeout(context.Background(), clusterOperationTimeout)
				delErr := backend.DeleteWorkspace(delCtx, job.ID, wsID)
				delCancel()
				if delErr != nil {
					log.Printf("[cleanup] failed to delete workspace %s: %v", wsID, delErr)
					if ferr := h.jobManager.RecordDeletionFailure(job.ID, wsID, ws.Name, maxRetries); ferr != nil {
						log.Printf("[cleanup] failed to record deletion failure for workspace %s: %v", wsID, ferr)
					}
				} else {
					deleted.Add(1)
					if cerr := h.jobManager.ClearDeletionRetry(job.ID, wsID); cerr != nil {
						log.Printf("[cleanup] failed to clear deletion retry for workspace %s: %v", wsID, cerr)
					}
					if rerr := h.jobManager.RecordWorkspaceEvent(job.ID, WorkspaceEventDeleted, wsID, ws.Name, ownerDisplayName(ws), ws.Template); rerr != nil {
						log.Printf("[cleanup] failed to record deletion event for workspace %s: %v", wsID, rerr)
					}
					h.recordAudit("system", "system", "workspace.delete", job.ID, fmt.Sprintf("%s: auto-cleanup, exceeded %dh lifetime", ws.Name, job.Config.WorkspaceLifetimeHours))
				}
			}(ws, wsID)
		}
		wg.Wait()
		if deletedCount := int(deleted.Load()); deletedCount > 0 {
			if err := h.jobManager.RecordCleanupEvent(job.ID, deletedCount); err != nil {
				log.Printf("[cleanup] failed to record cleanup event for job %s: %v", job.ID, err)
			} else if err := h.jobManager.SaveJob(job.ID); err != nil {
				log.Printf("[cleanup] failed to persist cleanup event for job %s: %v", job.ID, err)
			}
		}
	}
}

// cleanupExpiredLabs iterates all completed jobs and destroys those that have passed
// their configured LabDeletionDate.
func (h *Handler) cleanupExpiredLabs() {
	jobs := h.jobManager.GetAllJobs()
	for _, job := range jobs {
		if job.Status != JobStatusCompleted {
			continue
		}
		if job.Config == nil || job.Config.LabDeletionDate == nil {
			continue
		}
		if !time.Now().After(*job.Config.LabDeletionDate) {
			continue
		}
		jobID := job.ID
		log.Printf("[cleanup] scheduling automatic lab deletion for job %s (deletion date: %s)", jobID, job.Config.LabDeletionDate.Format("2006-01-02"))
		// Provider credentials are not persisted with the job — repopulate them
		// from the in-memory credential store before this non-interactive destroy.
		// If they are unavailable (e.g. entered via the UI and lost on restart),
		// skip this tick rather than run a credential-less destroy that would
		// orphan cloud resources; the status stays Completed so it retries later.
		if err := h.rehydrateProviderCredentials(job.Config); err != nil {
			log.Printf("[cleanup] cannot auto-delete job %s: %v; will retry next cycle", jobID, err)
			continue
		}
		// Mark as running immediately to prevent duplicate destroy on the next tick.
		if err := h.jobManager.UpdateJobStatus(jobID, JobStatusRunning); err != nil {
			log.Printf("[cleanup] failed to mark job %s as running before deletion: %v", jobID, err)
			continue
		}
		h.recordAudit("system", "system", "lab.destroy", jobID, fmt.Sprintf("%s: auto-cleanup, past deletion date", job.Config.StackName))
		go func(id string) {
			h.pulumiExecSem <- struct{}{}
			defer func() { <-h.pulumiExecSem }()
			if err := h.pulumiExec.Destroy(id); err != nil {
				log.Printf("[cleanup] automatic lab deletion failed for job %s: %v", id, err)
			}
		}(jobID)
	}
}
