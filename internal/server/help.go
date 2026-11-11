package server

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"easylab/internal/providers/workspace"
)

// Help requests are the student's way of telling the organiser they are stuck,
// and the mirror image of notifications.go: where a notification travels from
// the admin to a lab's students, a help request travels from one student back to
// the admin, who answers it by opening that student's workspace.
//
// They are a queue rather than a log. A workshop has several people stuck at
// once, so the admin works through a list; an answered request is removed, and
// the audit log keeps the history. Two rules keep that list readable:
//
//   - one open request per student per lab. Asking again refreshes what the
//     student said rather than adding a row, so an impatient student cannot bury
//     the others;
//   - a request prunes itself after helpRequestTTL, on every write and on every
//     read, exactly as a notification expires. A workshop that ends with requests
//     still open must not leave them in the lab's job file forever.
//
// A student reaches this from two places, because they spend a workshop in their
// IDE rather than on the portal:
//
//   - the portal header's "Ask for help" button, which posts to StudentHelp and
//     then polls it to follow the request's state (web/static/student-help.js);
//   - the IDE's own "EasyLab: Ask for help" command, which posts to WorkspaceHelp
//     from inside the student's container (see notifyExtensionStep in
//     internal/providers/workspace/kube).
//
// Both store the request on the lab's Job, which is also what carries it to a
// lab's own in-lab portal through PortalState — the same route Notifications and
// WorkspaceAccesses take. In the other direction, a request raised *on* such a
// portal reaches the admin through the outbox (see reportHelpRequest).

// Help request states. A request is only ever waiting for someone or being dealt
// with: resolving it removes it, so there is no third state to serve.
const (
	HelpStatusWaiting  = "waiting"
	HelpStatusHandling = "handling"
)

// Where a request came from, shown to the admin so they know whether the student
// was on the portal or already in their IDE.
const (
	HelpSourcePortal = "portal"
	HelpSourceIDE    = "ide"
)

const (
	// maxHelpMessageLength is how much of a student's note is kept. Long enough
	// for "my pod will not start", short enough to read at a glance in a queue.
	maxHelpMessageLength = 300
	// maxLabHelpRequests caps how many open requests one lab holds. The
	// one-per-student rule already bounds this in practice; past the cap the
	// oldest go, rather than letting the lab's job file grow without bound.
	maxLabHelpRequests = 100
	// helpRequestTTL is how long an unanswered request stays up. A workshop
	// afternoon, so nothing a student raised is dropped while it still matters,
	// and nothing survives the workshop that raised it.
	helpRequestTTL = 4 * time.Hour
	// helpDrainInterval is the shortest gap between two collections of one lab's
	// portal outbox on behalf of the admin's help-request poll. It bounds the
	// cluster traffic that poll can cause (see drainPortalsForHelp).
	helpDrainInterval = 15 * time.Second
)

// HelpRequest is one student's open call for help in a lab.
type HelpRequest struct {
	ID string `json:"id"`
	// At is when the student last asked — refreshed when they ask again, so the
	// admin's queue orders by who is waiting on their most recent word.
	At time.Time `json:"at"`
	// Student is who asked, in the same form as WorkspaceAccess.Owner: the login
	// email when it is known, else the sanitized username. It is what the
	// student-facing lookups compare against.
	Student string `json:"student"`
	// StudentEmail is the login email as typed, for the admin's display only. It
	// is empty for a request raised from inside an IDE, which authenticates with
	// a workspace token rather than a session.
	StudentEmail string `json:"student_email,omitempty"`
	Message      string `json:"message,omitempty"`
	// WorkspaceName is the student's workspace in this lab, resolved server-side
	// when the request is raised so the admin's row can offer to open it. Empty
	// when the student has none or the lab's cluster could not be asked — which
	// must not stop them asking for help.
	WorkspaceName  string     `json:"workspace_name,omitempty"`
	Status         string     `json:"status"`
	AcknowledgedAt *time.Time `json:"acknowledged_at,omitempty"`
	// AcknowledgedBy is the admin who took it on, shown only in the admin's own
	// list — a student is told someone is coming, not who.
	AcknowledgedBy string `json:"acknowledged_by,omitempty"`
	Source         string `json:"source,omitempty"`
	// Cancelled marks an outbox record as a withdrawal rather than a request (see
	// portalRecordHelpRequest). It is never stored on a lab's job.
	Cancelled bool `json:"cancelled,omitempty"`
}

// expired reports whether the request has outlived helpRequestTTL at now.
func (hr HelpRequest) expired(now time.Time) bool {
	return !hr.At.Add(helpRequestTTL).After(now)
}

// helpRequestBelongsTo reports whether hr was raised by the student signed in as
// email. It mirrors notificationBelongsTo: the student may have been recorded as
// the login email or as the sanitized username, so both are accepted.
func helpRequestBelongsTo(hr HelpRequest, email string) bool {
	if hr.Student == "" || email == "" {
		return false
	}
	if normalizeEmail(hr.Student) == normalizeEmail(email) {
		return true
	}
	return hr.Student == usernameFromEmail(email)
}

// truncateHelpMessage caps a note at maxHelpMessageLength characters. Counted in
// runes, not bytes: slicing bytes would cut an accented character in half and
// leave invalid UTF-8 in what the admin is shown.
func truncateHelpMessage(message string) string {
	runes := []rune(message)
	if len(runes) <= maxHelpMessageLength {
		return message
	}
	return string(runes[:maxHelpMessageLength])
}

// newHelpRequestID returns an identifier for one request. The admin's actions
// address a request by it, and the clients dedupe on it, so it only has to be
// unique within a lab.
func newHelpRequestID() (string, error) {
	token, err := GenerateWorkspaceToken()
	if err != nil {
		return "", fmt.Errorf("failed to generate help request ID: %w", err)
	}
	return token, nil
}

// pruneHelpRequests returns the entries still alive at now, keeping their order.
// Callers hold the job's lock.
func pruneHelpRequests(entries []HelpRequest, now time.Time) []HelpRequest {
	kept := make([]HelpRequest, 0, len(entries))
	for _, hr := range entries {
		if hr.expired(now) {
			continue
		}
		kept = append(kept, hr)
	}
	return kept
}

// upsertHelpRequest folds hr into entries under the two rules that keep the
// admin's queue readable: a request already known by its ID is refreshed, and a
// student may hold only one open request in a lab. Either way the stored entry
// keeps its own ID and status — the admin may already be on their way — and only
// what the student just told us is replaced.
//
// It returns the new slice, the stored entry, and whether anything changed (a
// record the admin has already applied changes nothing, which is what makes
// importing one from a portal outbox idempotent).
func upsertHelpRequest(entries []HelpRequest, hr HelpRequest, now time.Time) ([]HelpRequest, HelpRequest, bool) {
	kept := pruneHelpRequests(entries, now)

	refresh := func(i int) ([]HelpRequest, HelpRequest, bool) {
		changed := kept[i].Message != hr.Message || !kept[i].At.Equal(hr.At)
		kept[i].Message = hr.Message
		kept[i].At = hr.At
		// Only ever filled in, never cleared: a later ask whose workspace lookup
		// failed must not lose the workspace an earlier one found.
		if hr.WorkspaceName != "" && kept[i].WorkspaceName != hr.WorkspaceName {
			kept[i].WorkspaceName = hr.WorkspaceName
			changed = true
		}
		if hr.StudentEmail != "" && kept[i].StudentEmail != hr.StudentEmail {
			kept[i].StudentEmail = hr.StudentEmail
			changed = true
		}
		if hr.Source != "" && kept[i].Source != hr.Source {
			kept[i].Source = hr.Source
			changed = true
		}
		return kept, kept[i], changed
	}

	for i := range kept {
		if kept[i].ID == hr.ID {
			return refresh(i)
		}
	}
	for i := range kept {
		if kept[i].Student == hr.Student {
			return refresh(i)
		}
	}

	hr.Status = HelpStatusWaiting
	hr.AcknowledgedAt = nil
	hr.AcknowledgedBy = ""
	hr.Cancelled = false
	kept = append(kept, hr)
	// Oldest first, so dropping the head drops the oldest.
	if len(kept) > maxLabHelpRequests {
		kept = kept[len(kept)-maxLabHelpRequests:]
	}
	return kept, hr, true
}

// RaiseHelpRequest stores a student's call for help on a lab, refreshing the one
// they already had open rather than adding a second. It returns the request as
// stored, which is what the student is then shown.
func (jm *JobManager) RaiseHelpRequest(id string, hr HelpRequest) (HelpRequest, error) {
	job, exists := jm.GetJob(id)
	if !exists {
		return HelpRequest{}, fmt.Errorf("job %s not found", id)
	}

	job.mu.Lock()
	defer job.mu.Unlock()
	entries, stored, _ := upsertHelpRequest(job.HelpRequests, hr, hr.At)
	job.HelpRequests = entries
	job.UpdatedAt = time.Now()
	return stored, nil
}

// ImportHelpRequest applies a request a lab's own portal reported. Delivery is
// at-least-once, so it has to be idempotent: a record already applied reports
// no change and leaves the lab's job untouched (and so unsaved).
func (jm *JobManager) ImportHelpRequest(id string, hr HelpRequest) (bool, error) {
	job, exists := jm.GetJob(id)
	if !exists {
		return false, fmt.Errorf("job %s not found", id)
	}

	now := time.Now()
	job.mu.Lock()
	defer job.mu.Unlock()
	entries, _, changed := upsertHelpRequest(job.HelpRequests, hr, now)
	job.HelpRequests = entries
	if changed {
		job.UpdatedAt = now
	}
	return changed, nil
}

// MergeLocalHelpRequests adds requests this process raised that the lab's state
// does not carry yet. It is how an in-lab portal keeps showing a student the
// request they just made: the portal's job is a read-only mirror the admin
// overwrites every few seconds, and the request only comes back once the admin
// has collected it (see PortalRuntime.reapplyLocalHelp).
//
// Entries already present by ID are left exactly as they are — the synced copy
// is the authoritative one.
func (jm *JobManager) MergeLocalHelpRequests(id string, local []HelpRequest) {
	if len(local) == 0 {
		return
	}
	job, exists := jm.GetJob(id)
	if !exists {
		return
	}

	now := time.Now()
	job.mu.Lock()
	defer job.mu.Unlock()

	kept := pruneHelpRequests(job.HelpRequests, now)
	known := make(map[string]bool, len(kept))
	for _, hr := range kept {
		known[hr.ID] = true
	}
	added := false
	for _, hr := range local {
		if known[hr.ID] || hr.expired(now) {
			continue
		}
		known[hr.ID] = true
		kept = append(kept, hr)
		added = true
	}
	job.HelpRequests = kept
	if added {
		job.UpdatedAt = now
	}
}

// CancelHelpRequest drops the open request of one student — their own "never
// mind". It reports whether there was one, and which it was, so the caller can
// audit it.
func (jm *JobManager) CancelHelpRequest(id, student string) (HelpRequest, bool, error) {
	job, exists := jm.GetJob(id)
	if !exists {
		return HelpRequest{}, false, fmt.Errorf("job %s not found", id)
	}

	now := time.Now()
	job.mu.Lock()
	defer job.mu.Unlock()

	kept := make([]HelpRequest, 0, len(job.HelpRequests))
	var removed HelpRequest
	found := false
	for _, hr := range job.HelpRequests {
		if !found && hr.Student == student {
			removed, found = hr, true
			continue
		}
		if hr.expired(now) {
			continue
		}
		kept = append(kept, hr)
	}
	job.HelpRequests = kept
	job.UpdatedAt = now
	return removed, found, nil
}

// ResolveHelpRequest removes one request — the admin's "dealt with". It reports
// whether it was still there, and which it was, for the audit entry.
func (jm *JobManager) ResolveHelpRequest(id, requestID string) (HelpRequest, bool, error) {
	job, exists := jm.GetJob(id)
	if !exists {
		return HelpRequest{}, false, fmt.Errorf("job %s not found", id)
	}

	now := time.Now()
	job.mu.Lock()
	defer job.mu.Unlock()

	kept := make([]HelpRequest, 0, len(job.HelpRequests))
	var removed HelpRequest
	found := false
	for _, hr := range job.HelpRequests {
		if hr.ID == requestID {
			removed, found = hr, true
			continue
		}
		if hr.expired(now) {
			continue
		}
		kept = append(kept, hr)
	}
	job.HelpRequests = kept
	job.UpdatedAt = now
	return removed, found, nil
}

// AckHelpRequest marks a request as being dealt with, so the student is told
// someone is coming and another admin can see it is taken. Already-acknowledged
// requests are left alone: the first admin to take it keeps it.
func (jm *JobManager) AckHelpRequest(id, requestID, by string) (HelpRequest, bool, error) {
	job, exists := jm.GetJob(id)
	if !exists {
		return HelpRequest{}, false, fmt.Errorf("job %s not found", id)
	}

	now := time.Now()
	job.mu.Lock()
	defer job.mu.Unlock()

	kept := pruneHelpRequests(job.HelpRequests, now)
	job.HelpRequests = kept
	for i := range kept {
		if kept[i].ID != requestID {
			continue
		}
		if kept[i].Status != HelpStatusHandling {
			kept[i].Status = HelpStatusHandling
			kept[i].AcknowledgedAt = &now
			kept[i].AcknowledgedBy = by
			job.UpdatedAt = now
		}
		return kept[i], true, nil
	}
	return HelpRequest{}, false, nil
}

// AckHelpRequestForWorkspace marks the open request on one workspace as being
// dealt with. It is what makes opening a student's workspace enough — the admin
// does not have to say so twice — and a no-op when that workspace has no request
// open, which is the usual case elsewhere in the admin UI.
func (jm *JobManager) AckHelpRequestForWorkspace(id, workspaceName, by string) (HelpRequest, bool) {
	job, exists := jm.GetJob(id)
	if !exists || workspaceName == "" {
		return HelpRequest{}, false
	}

	now := time.Now()
	job.mu.Lock()
	defer job.mu.Unlock()

	kept := pruneHelpRequests(job.HelpRequests, now)
	job.HelpRequests = kept
	for i := range kept {
		if kept[i].WorkspaceName != workspaceName || kept[i].Status == HelpStatusHandling {
			continue
		}
		kept[i].Status = HelpStatusHandling
		kept[i].AcknowledgedAt = &now
		kept[i].AcknowledgedBy = by
		job.UpdatedAt = now
		return kept[i], true
	}
	return HelpRequest{}, false
}

// LabHelpRequests returns a lab's open requests, the longest-waiting first — the
// order an admin works through them in. It is the admin's view: it carries who
// asked and which admin took it, which a student endpoint must never do.
func (jm *JobManager) LabHelpRequests(id string) []HelpRequest {
	job, exists := jm.GetJob(id)
	if !exists {
		return nil
	}

	now := time.Now()
	job.mu.RLock()
	active := pruneHelpRequests(job.HelpRequests, now)
	job.mu.RUnlock()

	sort.Slice(active, func(i, j int) bool { return active[i].At.Before(active[j].At) })
	return active
}

// StudentHelpRequest returns the open request of the student signed in as email
// in one lab, and whether they have one.
func (jm *JobManager) StudentHelpRequest(id, email string) (HelpRequest, bool) {
	job, exists := jm.GetJob(id)
	if !exists {
		return HelpRequest{}, false
	}

	now := time.Now()
	job.mu.RLock()
	defer job.mu.RUnlock()
	for _, hr := range job.HelpRequests {
		if hr.expired(now) || !helpRequestBelongsTo(hr, email) {
			continue
		}
		return hr, true
	}
	return HelpRequest{}, false
}

// ---------------------------------------------------------------------------
// The student's side
// ---------------------------------------------------------------------------

// studentHelpResponse is one of the authenticated student's own open requests.
// It deliberately leaves out which admin took it and what every other student
// asked: a student is told that someone is coming, not who, and nothing at all
// about anybody else.
type studentHelpResponse struct {
	LabID   string `json:"lab_id"`
	LabName string `json:"lab_name,omitempty"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
	At      string `json:"at"`
}

func newStudentHelpResponse(labID, labName string, hr HelpRequest) studentHelpResponse {
	return studentHelpResponse{
		LabID:   labID,
		LabName: labName,
		Status:  hr.Status,
		Message: hr.Message,
		At:      hr.At.Format(time.RFC3339),
	}
}

// writeStudentHelp sends the student's own requests as the JSON the portal pages
// expect.
func writeStudentHelp(w http.ResponseWriter, entries []studentHelpResponse) {
	if entries == nil {
		entries = []studentHelpResponse{}
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]interface{}{"requests": entries}); err != nil {
		log.Printf("[help] failed to encode help requests: %v", err)
	}
}

// StudentHelp handles /api/student/help: GET lists the authenticated student's
// own open requests across the labs they are in, and POST raises one (or
// refreshes the one they already had).
func (h *Handler) StudentHelp(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.serveStudentHelpState(w, r)
	case http.MethodPost:
		h.raiseStudentHelp(w, r)
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// serveStudentHelpState answers the poll behind the portal's "Ask for help"
// button, so it follows the request through to an admin picking it up.
func (h *Handler) serveStudentHelpState(w http.ResponseWriter, r *http.Request) {
	// The identity is always the authenticated student — never trusted from the client.
	email := studentEmailFromContext(r)
	if email == "" {
		writeStudentHelp(w, nil)
		return
	}

	var entries []studentHelpResponse
	for _, job := range h.jobManager.GetAllJobs() {
		hr, ok := h.jobManager.StudentHelpRequest(job.ID, email)
		if !ok {
			continue
		}
		entries = append(entries, newStudentHelpResponse(job.ID, labDisplayName(job), hr))
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].At > entries[j].At })
	writeStudentHelp(w, entries)
}

// raiseStudentHelp stores a request from the portal. The lab only has to exist
// and be ready — deliberately not the studentInLab check the notification
// endpoints use, which reads the lab's workspace history and so would turn away
// exactly the student whose workspace never came up. A bogus request costs an
// admin one click, and the one-per-student rule bounds it.
func (h *Handler) raiseStudentHelp(w http.ResponseWriter, r *http.Request) {
	// The identity is always the authenticated student — never trusted from the client.
	email := studentEmailFromContext(r)
	if email == "" {
		writeJSONError(w, http.StatusUnauthorized, "Session email not found, please log in again")
		return
	}

	if err := h.parseForm(w, r, 0); err != nil {
		log.Printf("[help] failed to parse the help request form: %v", err)
		writeJSONError(w, http.StatusBadRequest, "Could not read the submitted form.")
		return
	}

	labID := strings.TrimSpace(getFormValue(r, "lab_id"))
	if labID == "" {
		writeJSONError(w, http.StatusBadRequest, "Pick the lab you need help with.")
		return
	}
	job, exists := h.jobManager.GetJob(labID)
	if !exists {
		writeJSONError(w, http.StatusNotFound, "Lab not found")
		return
	}

	hr := HelpRequest{
		At:           time.Now(),
		Student:      normalizeRecipient(email),
		StudentEmail: email,
		Message:      truncateHelpMessage(strings.TrimSpace(getFormValue(r, "message"))),
		Status:       HelpStatusWaiting,
		Source:       HelpSourcePortal,
	}
	hr.WorkspaceName = h.studentWorkspaceIn(r.Context(), labID, func(ws workspace.Workspace) bool {
		return workspaceOwnedBy(ws, email)
	})

	stored, err := h.storeHelpRequest(r.Context(), labID, hr)
	if err != nil {
		log.Printf("[help] failed to store a help request for lab %s: %v", labID, err)
		writeJSONError(w, http.StatusInternalServerError, "Could not send your request. Try again.")
		return
	}

	// The note is never audited: it is the student's own words, and the audit
	// log's rule is that Detail carries context, not content.
	h.recordAudit(email, "student", "lab.help_request", labID, hr.Source)
	log.Printf("[help] %s asked for help in lab %s", hr.Student, labID)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"request": newStudentHelpResponse(labID, labDisplayName(job), stored),
	})
}

// CancelStudentHelp handles POST /api/student/help/cancel: the student's own
// "never mind".
func (h *Handler) CancelStudentHelp(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	// The identity is always the authenticated student — never trusted from the client.
	email := studentEmailFromContext(r)
	if email == "" {
		writeJSONError(w, http.StatusUnauthorized, "Session email not found, please log in again")
		return
	}

	if err := h.parseForm(w, r, 0); err != nil {
		log.Printf("[help] failed to parse the help cancel form: %v", err)
		writeJSONError(w, http.StatusBadRequest, "Could not read the submitted form.")
		return
	}

	labID := strings.TrimSpace(getFormValue(r, "lab_id"))
	if labID == "" {
		writeJSONError(w, http.StatusBadRequest, "lab_id is required")
		return
	}
	// Authorization: a student may only withdraw their own request, which is why
	// the lookup is by their session identity and not by a request ID they send.
	existing, ok := h.jobManager.StudentHelpRequest(labID, email)
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "cancelled": false})
		return
	}

	if err := h.dropHelpRequest(r.Context(), labID, existing); err != nil {
		log.Printf("[help] failed to cancel the help request of %s in lab %s: %v", existing.Student, labID, err)
		writeJSONError(w, http.StatusInternalServerError, "Could not cancel your request. Try again.")
		return
	}

	h.recordAudit(email, "student", "lab.help_cancel", labID, "")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "cancelled": true})
}

// WorkspaceHelp handles POST /api/workspace/help?lab_id=&token=: a student asking
// for help from inside their IDE.
//
// Like WorkspaceNotifications it is not behind RequireStudentAuth: the IDE runs
// in the student's own container with no session cookie, and carries the
// workspace's notify token instead (see notifyToken). The token names the lab and
// the owner; the only thing read from the request itself is the note.
func (h *Handler) WorkspaceHelp(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	labID := r.URL.Query().Get("lab_id")
	token := r.URL.Query().Get("token")
	if labID == "" || token == "" {
		writeJSONError(w, http.StatusBadRequest, "lab and token are required")
		return
	}

	job, exists := h.jobManager.GetJob(labID)
	if !exists {
		// Same answer as a bad token: an unauthenticated caller learns nothing
		// about which labs exist.
		log.Printf("[help] help requested from a workspace of unknown lab %s", labID)
		writeJSONError(w, http.StatusForbidden, "Not authorized")
		return
	}

	job.mu.RLock()
	secret := job.NotifySecret
	job.mu.RUnlock()

	owner, ok := verifyNotifyToken(secret, labID, token)
	if !ok {
		log.Printf("[help] rejected a workspace help token for lab %s", labID)
		writeJSONError(w, http.StatusForbidden, "Not authorized")
		return
	}

	if err := h.parseForm(w, r, 0); err != nil {
		log.Printf("[help] failed to parse the workspace help form for lab %s: %v", labID, err)
		writeJSONError(w, http.StatusBadRequest, "Could not read the request.")
		return
	}

	hr := HelpRequest{
		At:      time.Now(),
		Student: normalizeRecipient(owner),
		Message: truncateHelpMessage(strings.TrimSpace(getFormValue(r, "message"))),
		Status:  HelpStatusWaiting,
		Source:  HelpSourceIDE,
	}
	hr.WorkspaceName = h.studentWorkspaceIn(r.Context(), labID, func(ws workspace.Workspace) bool {
		return workspaceOwnerKey(ws) == hr.Student || (ws.Owner != "" && ws.Owner == owner)
	})

	if _, err := h.storeHelpRequest(r.Context(), labID, hr); err != nil {
		log.Printf("[help] failed to store a help request from a workspace of lab %s: %v", labID, err)
		writeJSONError(w, http.StatusInternalServerError, "Could not send the request.")
		return
	}

	h.recordAudit(hr.Student, "student", "lab.help_request", labID, hr.Source)
	log.Printf("[help] %s asked for help from their IDE in lab %s", hr.Student, labID)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
}

// storeHelpRequest records a raised request: on the lab's job, or — on an in-lab
// portal, whose job is a read-only mirror of the admin's — in the outbox the
// admin collects it from, plus a local copy so the student sees it at once. It
// mirrors saveFeedback's fork.
func (h *Handler) storeHelpRequest(ctx context.Context, labID string, hr HelpRequest) (HelpRequest, error) {
	// Asking again keeps the request the admin may already have picked up, rather
	// than replacing it with a fresh one they have never seen.
	if existing, ok := h.jobManager.StudentHelpRequest(labID, hr.Student); ok {
		hr.ID = existing.ID
		hr.Status = existing.Status
		hr.AcknowledgedAt = existing.AcknowledgedAt
		hr.AcknowledgedBy = existing.AcknowledgedBy
	} else {
		id, err := newHelpRequestID()
		if err != nil {
			return HelpRequest{}, err
		}
		hr.ID = id
	}

	if h.portal != nil {
		if err := h.portal.reportHelpRequest(ctx, hr); err != nil {
			return HelpRequest{}, err
		}
		h.jobManager.MergeLocalHelpRequests(labID, []HelpRequest{hr})
		return hr, nil
	}

	stored, err := h.jobManager.RaiseHelpRequest(labID, hr)
	if err != nil {
		return HelpRequest{}, err
	}
	if err := h.jobManager.SaveJob(labID); err != nil {
		// Logged, not fatal: the request is live now, and losing it to a restart is
		// no worse than the student having to ask again.
		log.Printf("[help] failed to persist the help request for lab %s: %v", labID, err)
	}
	// Pushed to the lab's own portal — where its students may be — the way a
	// notification and an admin opening a workspace are.
	go h.syncPortalState(labID)
	return stored, nil
}

// dropHelpRequest removes a request, on whichever side this process is.
func (h *Handler) dropHelpRequest(ctx context.Context, labID string, hr HelpRequest) error {
	if h.portal != nil {
		hr.Cancelled = true
		if err := h.portal.reportHelpRequest(ctx, hr); err != nil {
			return err
		}
		if _, _, err := h.jobManager.CancelHelpRequest(labID, hr.Student); err != nil {
			return err
		}
		return nil
	}

	if _, _, err := h.jobManager.ResolveHelpRequest(labID, hr.ID); err != nil {
		return err
	}
	if err := h.jobManager.SaveJob(labID); err != nil {
		log.Printf("[help] failed to persist the help request removal for lab %s: %v", labID, err)
	}
	go h.syncPortalState(labID)
	return nil
}

// studentWorkspaceIn returns the name of the workspace in labID that match
// accepts, or "" when there is none, the lab has no cluster to ask, or asking it
// failed. The name is a convenience for the admin's queue, never an
// authorization input, so every failure is simply an empty answer — a student
// whose workspace is broken is exactly who needs to be able to ask for help.
//
// The client never names a workspace, which is why this resolves it here.
func (h *Handler) studentWorkspaceIn(ctx context.Context, labID string, match func(workspace.Workspace) bool) string {
	job, exists := h.jobManager.GetJob(labID)
	if !exists {
		return ""
	}

	job.mu.RLock()
	kubeconfig := extractStringFromConfigValue(job.Kubeconfig)
	namespace := job.workspaceNamespace()
	job.mu.RUnlock()
	if kubeconfig == "" {
		return ""
	}

	backend, err := h.workspaceBackendFor(labID, kubeconfig, namespace)
	if err != nil {
		log.Printf("[help] failed to build the backend for lab %s: %v", labID, err)
		return ""
	}

	ctx, cancel := context.WithTimeout(ctx, studentWorkspacesTimeout)
	defer cancel()
	found, err := backend.ListWorkspaces(ctx, labID)
	if err != nil {
		log.Printf("[help] failed to list the workspaces of lab %s: %v", labID, err)
		return ""
	}
	for _, ws := range found {
		if match(ws) {
			return ws.Name
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// The admin's side
// ---------------------------------------------------------------------------

// helpRequestDisplay is one open request as the admin's page and its poll see
// it. CanOpen only says the request names a workspace, not that the workspace is
// still up: checking that would mean a cluster call per row on a poll that runs
// on every admin page. An opened workspace that has gone away answers "workspace
// not available", which is the same thing a stale row on the lab's own
// Workspaces tab does.
type helpRequestDisplay struct {
	ID            string `json:"id"`
	LabID         string `json:"lab_id"`
	LabName       string `json:"lab_name"`
	Student       string `json:"student"`
	Message       string `json:"message,omitempty"`
	WorkspaceName string `json:"workspace_name,omitempty"`
	Status        string `json:"status"`
	Source        string `json:"source,omitempty"`
	At            string `json:"at"`
	Waiting       string `json:"waiting"`
	TakenBy       string `json:"taken_by,omitempty"`
	CanOpen       bool   `json:"can_open"`
}

// labDisplayName is the lab's name as an admin or a student knows it, falling
// back to its ID. The job must not be locked by the caller.
func labDisplayName(job *Job) string {
	job.mu.RLock()
	defer job.mu.RUnlock()
	if job.Config != nil && job.Config.StackName != "" {
		return job.Config.StackName
	}
	return job.ID
}

// openHelpRequests gathers every lab's open requests, the longest-waiting first
// — one queue across the whole instance, because an admin running three labs at
// once should not have to look in three places.
func (h *Handler) openHelpRequests() []helpRequestDisplay {
	now := time.Now()
	entries := []helpRequestDisplay{}
	for _, job := range h.jobManager.GetAllJobs() {
		labName := labDisplayName(job)
		for _, hr := range h.jobManager.LabHelpRequests(job.ID) {
			student := hr.StudentEmail
			if student == "" {
				student = hr.Student
			}
			entries = append(entries, helpRequestDisplay{
				ID:            hr.ID,
				LabID:         job.ID,
				LabName:       labName,
				Student:       student,
				Message:       hr.Message,
				WorkspaceName: hr.WorkspaceName,
				Status:        hr.Status,
				Source:        hr.Source,
				At:            hr.At.Format(time.RFC3339),
				Waiting:       humanWaited(hr.At, now),
				TakenBy:       hr.AcknowledgedBy,
				CanOpen:       hr.WorkspaceName != "",
			})
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].At < entries[j].At })
	return entries
}

// humanWaited renders how long a student has been waiting, for the admin's
// queue. Minutes, because that is the unit a workshop is run in.
func humanWaited(since, now time.Time) string {
	waited := now.Sub(since)
	if waited < time.Minute {
		return "just now"
	}
	minutes := int(waited / time.Minute)
	if minutes < 60 {
		if minutes == 1 {
			return "1 minute"
		}
		return fmt.Sprintf("%d minutes", minutes)
	}
	hours := minutes / 60
	minutes -= hours * 60
	label := fmt.Sprintf("%d hours", hours)
	if hours == 1 {
		label = "1 hour"
	}
	if minutes == 0 {
		return label
	}
	return fmt.Sprintf("%s %d min", label, minutes)
}

// ServeHelpRequests serves the admin's Help requests page: the queue of students
// waiting, across every lab.
//
// The page is the shell only — the queue itself arrives from
// ServeHelpRequestsPanel, which the panel asks for on load and every few seconds
// after, so there is nothing to collect or count here.
func (h *Handler) ServeHelpRequests(w http.ResponseWriter, r *http.Request) {
	h.serveTemplate(w, "admin-help-requests.html", nil)
}

// ListHelpRequests handles GET /api/admin/help-requests: the open requests as
// JSON, for the sidebar badge and the desktop notification that every admin page
// polls (web/static/admin-help-requests.js).
func (h *Handler) ListHelpRequests(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	h.drainPortalsForHelp(r.Context())
	entries := h.openHelpRequests()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]interface{}{
		"count":    len(entries),
		"requests": entries,
	}); err != nil {
		log.Printf("[help] failed to encode the help request list: %v", err)
	}
}

// ServeHelpRequestsPanel handles GET /api/admin/help-requests/panel: the queue as
// the page's HTMX panel, which is also what the actions below answer with.
func (h *Handler) ServeHelpRequestsPanel(w http.ResponseWriter, r *http.Request) {
	h.drainPortalsForHelp(r.Context())
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprint(w, h.renderHelpRequestsPanel())
}

// AckHelpRequest handles POST /api/admin/help-requests/ack: "on my way", for the
// organiser who walks over to the student rather than opening their IDE.
func (h *Handler) AckHelpRequest(w http.ResponseWriter, r *http.Request) {
	labID, requestID, ok := h.helpActionTarget(w, r)
	if !ok {
		return
	}

	hr, found, err := h.jobManager.AckHelpRequest(labID, requestID, adminActor(r))
	if err != nil {
		log.Printf("[help] failed to acknowledge request %s of lab %s: %v", requestID, labID, err)
		writeToast(w, false, "Could not pick that request up. Try again.")
		return
	}
	if found {
		h.persistHelpChange(labID)
		h.recordAudit(adminActor(r), "admin", "lab.help_ack", labID, hr.Student)
		log.Printf("[help] %s picked up the request of %s in lab %s", adminActor(r), hr.Student, labID)
	}

	h.ServeHelpRequestsPanel(w, r)
}

// ResolveHelpRequest handles POST /api/admin/help-requests/resolve: the request
// is dealt with and leaves the queue. The audit log keeps the history, so there
// is no resolved state to serve and nothing for anyone to tidy up.
func (h *Handler) ResolveHelpRequest(w http.ResponseWriter, r *http.Request) {
	labID, requestID, ok := h.helpActionTarget(w, r)
	if !ok {
		return
	}

	hr, found, err := h.jobManager.ResolveHelpRequest(labID, requestID)
	if err != nil {
		log.Printf("[help] failed to resolve request %s of lab %s: %v", requestID, labID, err)
		writeToast(w, false, "Could not close that request. Try again.")
		return
	}
	if found {
		h.persistHelpChange(labID)
		h.recordAudit(adminActor(r), "admin", "lab.help_resolve", labID, hr.Student)
		log.Printf("[help] %s resolved the request of %s in lab %s", adminActor(r), hr.Student, labID)
	}

	h.ServeHelpRequestsPanel(w, r)
}

// helpActionTarget reads and validates the lab and request an admin action
// addresses, answering the client itself when either is missing.
func (h *Handler) helpActionTarget(w http.ResponseWriter, r *http.Request) (labID, requestID string, ok bool) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return "", "", false
	}
	if err := h.parseForm(w, r, 0); err != nil {
		log.Printf("[help] failed to parse a help request action form: %v", err)
		writeToast(w, false, "Could not read the submitted form.")
		return "", "", false
	}
	labID = strings.TrimSpace(getFormValue(r, "lab_id"))
	requestID = strings.TrimSpace(getFormValue(r, "request_id"))
	if labID == "" || requestID == "" {
		http.Error(w, "lab and request are required", http.StatusBadRequest)
		return "", "", false
	}
	if _, exists := h.jobManager.GetJob(labID); !exists {
		http.Error(w, "Lab not found", http.StatusNotFound)
		return "", "", false
	}
	return labID, requestID, true
}

// persistHelpChange saves a lab whose queue an admin just changed and tells its
// own portal, so the student's button follows within seconds.
func (h *Handler) persistHelpChange(labID string) {
	if err := h.jobManager.SaveJob(labID); err != nil {
		log.Printf("[help] failed to persist the help request change for lab %s: %v", labID, err)
	}
	go h.syncPortalState(labID)
}

// renderHelpRequestsPanel builds the admin's queue: one card per waiting
// student, with the ways to answer them. Like renderNotificationsPanel and the
// credentials panel it is a fragment assembled here rather than a template,
// which is how the admin's other HTMX panels answer.
func (h *Handler) renderHelpRequestsPanel() string {
	entries := h.openHelpRequests()
	if len(entries) == 0 {
		return `<p class="help-request-empty">Nobody is waiting. A student asking for help from their portal or their IDE turns up here, and in the count beside <strong>Help requests</strong> in the sidebar.</p>`
	}

	var b strings.Builder
	for _, e := range entries {
		class := "help-request-item"
		if e.Status == HelpStatusHandling {
			class += " help-request-item--handling"
		}
		fmt.Fprintf(&b, `<div class="%s">`, class)

		b.WriteString(`<div class="help-request-head">`)
		fmt.Fprintf(&b, `<span class="help-request-student">%s</span>`, template.HTMLEscapeString(e.Student))
		fmt.Fprintf(&b, `<a class="help-request-lab" href="/labs/%s">%s</a>`,
			template.HTMLEscapeString(e.LabID), template.HTMLEscapeString(e.LabName))
		fmt.Fprintf(&b, `<span class="help-request-wait">waiting %s</span>`, template.HTMLEscapeString(e.Waiting))
		if e.Source == HelpSourceIDE {
			b.WriteString(`<span class="help-request-source">from their IDE</span>`)
		}
		if e.Status == HelpStatusHandling {
			taken := "Being dealt with"
			if e.TakenBy != "" {
				taken = "Picked up by " + e.TakenBy
			}
			fmt.Fprintf(&b, `<span class="help-request-taken">%s</span>`, template.HTMLEscapeString(taken))
		}
		b.WriteString(`</div>`)

		if e.Message != "" {
			fmt.Fprintf(&b, `<div class="help-request-message">%s</div>`, template.HTMLEscapeString(e.Message))
		}

		b.WriteString(`<div class="help-request-actions">`)
		if e.CanOpen {
			// The existing admin open: it signs the teacher into the student's IDE
			// and, on the way past, marks this request as being dealt with.
			fmt.Fprintf(&b, `<form method="POST" action="/api/labs/%s/workspaces/%s/open" target="_blank" class="help-request-open-form">`,
				template.HTMLEscapeString(e.LabID), template.HTMLEscapeString(e.WorkspaceName))
			b.WriteString(`<button type="submit" class="btn btn-primary btn-sm">Open workspace</button>`)
			b.WriteString(`</form>`)
		} else {
			b.WriteString(`<span class="help-request-no-workspace">No workspace of theirs was found in this lab.</span>`)
		}
		if e.Status != HelpStatusHandling {
			b.WriteString(h.renderHelpAction(e, "ack", "On my way", "btn btn-secondary btn-sm"))
		}
		b.WriteString(h.renderHelpAction(e, "resolve", "Resolve", "btn btn-secondary btn-sm"))
		b.WriteString(`</div>`)

		b.WriteString(`</div>`)
	}
	return b.String()
}

// renderHelpAction builds one of the queue's HTMX buttons. Both post the lab and
// the request as form fields rather than in the path, which keeps them out of the
// /api/labs/ subtree and its ordered route table.
func (h *Handler) renderHelpAction(e helpRequestDisplay, action, label, class string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<form hx-post="/api/admin/help-requests/%s" hx-target="#help-requests-panel" hx-swap="innerHTML" class="help-request-action-form">`,
		template.HTMLEscapeString(action))
	fmt.Fprintf(&b, `<input type="hidden" name="lab_id" value="%s">`, template.HTMLEscapeString(e.LabID))
	fmt.Fprintf(&b, `<input type="hidden" name="request_id" value="%s">`, template.HTMLEscapeString(e.ID))
	fmt.Fprintf(&b, `<button type="submit" class="%s">%s</button>`,
		template.HTMLEscapeString(class), template.HTMLEscapeString(label))
	b.WriteString(`</form>`)
	return b.String()
}

// drainPortalsForHelp collects what the labs' own portals have queued, so a
// request raised on an in-lab portal reaches the admin's badge in seconds rather
// than waiting for the five-minute reconcile tick.
//
// It rides the admin's own poll, so it only ever runs while somebody is actually
// looking, and it is rate-limited per lab: a dozen admin tabs polling together
// still cost one collection per lab per helpDrainInterval. Each lab is collected
// in its own goroutine under a single short timeout, so one unreachable cluster
// delays the answer by that timeout rather than by the sum of them.
func (h *Handler) drainPortalsForHelp(ctx context.Context) {
	// A portal has no portals of its own, and the lab it serves is a mirror it
	// must not try to collect from.
	if h.portal != nil || h.helpDrainAt == nil {
		return
	}

	now := time.Now()
	var due []string
	h.helpDrainMu.Lock()
	for _, job := range h.jobManager.GetAllJobs() {
		target := portalTargetFor(job)
		if !target.enabled || !target.completed {
			continue
		}
		if last, ok := h.helpDrainAt[job.ID]; ok && now.Sub(last) < helpDrainInterval {
			continue
		}
		h.helpDrainAt[job.ID] = now
		due = append(due, job.ID)
	}
	h.helpDrainMu.Unlock()
	if len(due) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, portalDrainTimeout)
	defer cancel()

	var wg sync.WaitGroup
	for _, labID := range due {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.drainPortalOutbox(ctx, labID)
		}()
	}
	wg.Wait()
}
