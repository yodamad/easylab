package server

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sort"
	"sync"
	"time"
)

// studentWorkspacesTimeout bounds each lab's cluster lookup when listing a
// student's workspaces. It runs while the My Workspaces page loads, so one
// unreachable cluster must not hang the page for the other labs.
const studentWorkspacesTimeout = 5 * time.Second

// studentWorkspace is one entry of the My Workspaces list. It deliberately has
// no token: the student gets into the IDE through OpenWorkspace, which checks
// ownership again and posts the token on their behalf.
type studentWorkspace struct {
	LabID         string `json:"lab_id"`
	LabName       string `json:"lab_name"`
	WorkspaceName string `json:"workspace_name"`
	WorkspaceURL  string `json:"workspace_url"`
	Email         string `json:"email"`
	Template      string `json:"template,omitempty"`
	CreatedAt     string `json:"created_at"`
	// DeletionAt is when the workspace is automatically deleted, "" when nothing
	// is scheduled.
	DeletionAt string `json:"deletion_at"`
	Ready      bool   `json:"ready"`
	// TeacherAccessedAt is when an admin last opened the workspace, "" if never.
	TeacherAccessedAt string `json:"teacher_accessed_at"`

	createdAt time.Time
}

// ListStudentWorkspaces handles GET /api/student/workspaces: it returns the
// authenticated student's workspaces across every lab, read from the labs'
// clusters. Because the list follows the student's login identity rather than
// anything stored in the browser, it is the same on every device they sign in on.
//
// A lab whose cluster cannot be reached is skipped and reported through
// "incomplete", so the page can say the list may be missing workspaces instead
// of failing altogether.
func (h *Handler) ListStudentWorkspaces(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	email := studentEmailFromContext(r)
	// The owner is always the authenticated student — never trusted from the client.
	owner := usernameFromEmail(email)
	if owner == "" {
		writeJSONError(w, http.StatusUnauthorized, "Session email not found, please log in again")
		return
	}

	type lab struct {
		id              string
		name            string
		kubeconfig      string
		namespace       string
		lifetimeHours   int
		labDeletionDate *time.Time
	}

	h.jobManager.mu.RLock()
	var labs []lab
	for _, job := range h.jobManager.jobs {
		job.mu.RLock()
		l := lab{
			id:         job.ID,
			name:       job.ID,
			kubeconfig: extractStringFromConfigValue(job.Kubeconfig),
			namespace:  job.workspaceNamespace(),
		}
		status := job.Status
		if job.Config != nil {
			if job.Config.StackName != "" {
				l.name = job.Config.StackName
			}
			l.lifetimeHours = job.Config.WorkspaceLifetimeHours
			l.labDeletionDate = job.Config.LabDeletionDate
		}
		job.mu.RUnlock()
		// A lab that is not completed or has no kubeconfig has no cluster to ask.
		if status != JobStatusCompleted || l.kubeconfig == "" {
			continue
		}
		labs = append(labs, l)
	}
	h.jobManager.mu.RUnlock()

	// The cluster calls run after the locks are released, one goroutine per lab
	// so a slow cluster only costs its own timeout.
	var (
		mu         sync.Mutex
		wg         sync.WaitGroup
		workspaces = []studentWorkspace{}
		incomplete bool
	)
	for _, l := range labs {
		wg.Add(1)
		go func() {
			defer wg.Done()

			backend, err := h.workspaceBackendFor(l.id, l.kubeconfig, l.namespace)
			if err != nil {
				log.Printf("ListStudentWorkspaces: failed to build backend for lab %s: %v", l.id, err)
				mu.Lock()
				incomplete = true
				mu.Unlock()
				return
			}

			ctx, cancel := context.WithTimeout(r.Context(), studentWorkspacesTimeout)
			defer cancel()
			found, err := backend.ListWorkspaces(ctx, l.id)
			if err != nil {
				log.Printf("ListStudentWorkspaces: failed to list workspaces for lab %s: %v", l.id, err)
				mu.Lock()
				incomplete = true
				mu.Unlock()
				return
			}

			for _, ws := range found {
				// Authorization: a student only ever sees their own workspaces.
				if ws.Owner != owner {
					continue
				}
				entry := studentWorkspace{
					LabID:         l.id,
					LabName:       l.name,
					WorkspaceName: ws.Name,
					WorkspaceURL:  ws.URL,
					Email:         email,
					Template:      ws.Template,
					CreatedAt:     ws.CreatedAt.Format(time.RFC3339),
					Ready:         ws.Ready,
					createdAt:     ws.CreatedAt,
				}
				if deletionAt := workspaceDeletionTime(ws.CreatedAt, l.lifetimeHours, l.labDeletionDate); deletionAt != nil {
					entry.DeletionAt = deletionAt.Format(time.RFC3339)
				}
				if access, ok := h.jobManager.GetWorkspaceAccess(l.id, ws.Name); ok && access.Owner == owner {
					entry.TeacherAccessedAt = access.At.Format(time.RFC3339)
				}
				mu.Lock()
				workspaces = append(workspaces, entry)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	// Newest first, with the name as a tie-breaker so the order is stable.
	sort.Slice(workspaces, func(i, j int) bool {
		if !workspaces[i].createdAt.Equal(workspaces[j].createdAt) {
			return workspaces[i].createdAt.After(workspaces[j].createdAt)
		}
		return workspaces[i].WorkspaceName < workspaces[j].WorkspaceName
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"workspaces": workspaces,
		"incomplete": incomplete,
	})
}
