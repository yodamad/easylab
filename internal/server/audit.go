package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// AuditEntry is one recorded action in the audit log.
type AuditEntry struct {
	At     time.Time `json:"at"`
	Actor  string    `json:"actor"`  // email, "admin" (identity unknown), or "system"
	Role   string    `json:"role"`   // "admin" | "student" | "system"
	Action string    `json:"action"` // e.g. "lab.create", "workspace.delete"
	LabID  string    `json:"lab_id,omitempty"`
	Detail string    `json:"detail,omitempty"` // short human-readable context, never secrets
}

// AuditStore is an append-only log of admin/student/system actions,
// persisted as newline-delimited JSON in a single file. Unlike
// FeedbackStore/JobManager's read-modify-rewrite-whole-file pattern, this
// appends one line per record — O(1) per write instead of O(existing
// history), avoiding the exact write-amplification issue found and fixed for
// job.go's SaveJob. Audit volume is inherently low (admin/student actions,
// not per-request), so a single running file and a single mutex are enough —
// no need for FeedbackStore's per-key locking or job.go's atomic tmp+rename
// dance (a torn write here just means the last line is dropped on read).
type AuditStore struct {
	dataDir string
	mu      sync.Mutex
}

// NewAuditStore creates a new AuditStore that persists data in dataDir.
func NewAuditStore(dataDir string) (*AuditStore, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create audit data directory: %w", err)
	}
	return &AuditStore{dataDir: dataDir}, nil
}

func (as *AuditStore) filePath() string {
	return filepath.Join(as.dataDir, "audit.jsonl")
}

// Record appends one entry to the audit log.
func (as *AuditStore) Record(e AuditEntry) error {
	as.mu.Lock()
	defer as.mu.Unlock()

	data, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("failed to marshal audit entry: %w", err)
	}
	f, err := os.OpenFile(as.filePath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("failed to open audit log: %w", err)
	}
	defer f.Close()
	data = append(data, '\n')
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("failed to write audit entry: %w", err)
	}
	return nil
}

// Recent returns up to limit audit entries, newest first. limit <= 0 means
// no cap. Malformed lines (e.g. a partial write from a crash) are skipped
// with a log warning rather than failing the whole read, matching
// JobManager.LoadJobs's tolerance for corrupt persisted data.
func (as *AuditStore) Recent(limit int) ([]AuditEntry, error) {
	as.mu.Lock()
	defer as.mu.Unlock()

	data, err := os.ReadFile(as.filePath())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read audit log: %w", err)
	}

	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	entries := make([]AuditEntry, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			continue
		}
		var e AuditEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			log.Printf("[audit] skipping malformed entry: %v", err)
			continue
		}
		entries = append(entries, e)
	}

	// Reverse in place: newest first.
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}
	if limit > 0 && len(entries) > limit {
		entries = entries[:limit]
	}
	return entries, nil
}

// adminActor returns a display string for the audit log's actor field: the
// admin's email when known (Azure AD admin login), or a generic "admin"
// label when the deployment uses classic shared-password login and no
// per-admin identity is available.
func adminActor(r *http.Request) string {
	if email := adminEmailFromContext(r); email != "" {
		return email
	}
	return "admin"
}

// ServeAuditLog serves the admin audit log page, showing the most recent
// actions across labs, workspaces, and credentials.
func (h *Handler) ServeAuditLog(w http.ResponseWriter, r *http.Request) {
	type AuditDisplay struct {
		At     string
		Actor  string
		Role   string
		Action string
		LabID  string
		Detail string
	}

	var entries []AuditDisplay
	if h.auditStore != nil {
		recent, err := h.auditStore.Recent(200)
		if err != nil {
			log.Printf("Failed to load audit log: %v", err)
			http.Error(w, "Failed to load audit log", http.StatusInternalServerError)
			return
		}
		for _, e := range recent {
			entries = append(entries, AuditDisplay{
				At:     e.At.Format("2006-01-02 15:04:05"),
				Actor:  e.Actor,
				Role:   e.Role,
				Action: e.Action,
				LabID:  e.LabID,
				Detail: e.Detail,
			})
		}
	}

	data := map[string]interface{}{
		"Entries": entries,
		"Count":   len(entries),
	}
	h.serveTemplate(w, "admin-audit-log.html", data)
}
