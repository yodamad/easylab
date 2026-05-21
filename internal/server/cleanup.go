package server

import (
	"context"
	"easylab/coder"
	"log"
	"net/http"
	"time"

	"github.com/google/uuid"
)

const workspaceCleanupInterval = 5 * time.Minute

// coderReachabilityTimeout is the maximum time to wait when probing Coder before skipping cleanup for a job.
const coderReachabilityTimeout = 5 * time.Second

// isCoderReachable does a fast HEAD probe to the Coder health endpoint.
// Returns false (and skips cleanup) when the server is down or the IP is gone.
func isCoderReachable(coderURL string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), coderReachabilityTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, coderURL+"/healthz", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return true
}

// StartWorkspaceCleanup starts a background goroutine that periodically deletes
// workspaces that have exceeded their configured lifetime.
func (h *Handler) StartWorkspaceCleanup(ctx context.Context) {
	ticker := time.NewTicker(workspaceCleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.cleanupExpiredWorkspaces()
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
		if job.CoderURL == "" || job.Config == nil || job.Config.WorkspaceLifetimeHours <= 0 {
			continue
		}
		if !isCoderReachable(job.CoderURL) {
			log.Printf("[cleanup] skipping job %s: Coder server unreachable (%s)", job.ID, job.CoderURL)
			continue
		}
		lifetime := time.Duration(job.Config.WorkspaceLifetimeHours) * time.Hour
		coderConfig := coder.CoderClientConfig{
			ServerURL:      job.CoderURL,
			SessionToken:   job.CoderSessionToken,
			OrganizationID: job.CoderOrganizationID,
		}
		workspaces, _, err := coder.ListWorkspacesWithRetry(coderConfig, job.CoderAdminEmail, job.CoderAdminPassword, uuid.Nil, "")
		if err != nil {
			log.Printf("[cleanup] failed to list workspaces for job %s: %v", job.ID, err)
			continue
		}
		for _, ws := range workspaces {
			if time.Since(ws.CreatedAt) > lifetime {
				log.Printf("[cleanup] deleting workspace %s (%s) in job %s: exceeded %dh lifetime", ws.Name, ws.ID, job.ID, job.Config.WorkspaceLifetimeHours)
				if _, err := coder.DeleteWorkspaceWithRetry(coderConfig, job.CoderAdminEmail, job.CoderAdminPassword, ws.ID); err != nil {
					log.Printf("[cleanup] failed to delete workspace %s: %v", ws.ID, err)
				}
			}
		}
	}
}
