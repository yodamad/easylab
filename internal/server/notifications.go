package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Notifications are the admin's way of telling a lab's students something during
// a workshop ("break in 10 minutes", "move on to exercise 3"). They are
// deliberately short-lived: an admin picks how long the message stays up and it
// then disappears on its own, so there is no dismissal state to keep per student
// and no history to prune by hand.
//
// A message reaches a student over two channels at once, because students spend
// a workshop in their IDE tab rather than on the portal:
//
//   - the portal pages poll StudentNotifications and raise a desktop
//     notification from whatever tab is open (web/static/student-notifications.js);
//   - the IDE polls WorkspaceNotifications from inside the student's container
//     and shows the message in code-server itself (see notifyExtensionStep in
//     internal/providers/workspace/kube).
//
// Both read the same entries off the lab's Job, which is also what carries them
// to a lab's own in-lab portal through PortalState — the same route
// WorkspaceAccesses takes.

// Notification levels. Anything else is rejected at the handler.
const (
	NotificationLevelInfo    = "info"
	NotificationLevelWarning = "warning"
)

// maxLabNotifications caps how many unexpired messages one lab holds. Expired
// entries are pruned on every write, so this only bites an admin sending far
// more than a workshop needs — past it the oldest go, rather than letting the
// lab's job file grow without bound.
const maxLabNotifications = 50

// maxNotificationMessageLength is how much of a message is kept. Long enough for
// an announcement, short enough to sit in a desktop notification.
const maxNotificationMessageLength = 500

// notificationTTLChoices are the lifetimes the admin form offers, in minutes.
// The form is a select, so this is also the allow-list the handler validates
// against rather than accepting an arbitrary duration.
var notificationTTLChoices = []int{5, 15, 30, 60}

// StudentNotification is a message an admin sent to a lab's students. It is
// never dismissed: it stops being served at ExpiresAt and is pruned from the job
// on the next write.
type StudentNotification struct {
	ID        string    `json:"id"`
	At        time.Time `json:"at"`
	ExpiresAt time.Time `json:"expires_at"`
	Message   string    `json:"message"`
	Level     string    `json:"level"`
	// Recipient is the one student the message is for, in the same form as
	// WorkspaceAccess.Owner (login email when known, else the sanitized
	// username). Empty addresses every student of the lab.
	Recipient string `json:"recipient,omitempty"`
	// Sender is the admin who sent it, shown only in the admin's own list.
	Sender string `json:"sender,omitempty"`
}

// expired reports whether the notification has outlived its TTL at now.
func (n StudentNotification) expired(now time.Time) bool {
	return !n.ExpiresAt.After(now)
}

// notificationBelongsTo reports whether n is addressed to the student signed in
// as email. It mirrors workspaceAccessBelongsTo: a recipient may have been
// recorded as the login email or as the sanitized username, so both are
// accepted.
func notificationBelongsTo(n StudentNotification, email string) bool {
	if n.Recipient == "" {
		// Addressed to the whole lab.
		return true
	}
	if email == "" {
		return false
	}
	if normalizeEmail(n.Recipient) == normalizeEmail(email) {
		return true
	}
	return n.Recipient == usernameFromEmail(email)
}

// validNotificationLevel reports whether level is one this server serves.
func validNotificationLevel(level string) bool {
	return level == NotificationLevelInfo || level == NotificationLevelWarning
}

// validNotificationTTL reports whether minutes is one of the offered lifetimes.
func validNotificationTTL(minutes int) bool {
	for _, choice := range notificationTTLChoices {
		if minutes == choice {
			return true
		}
	}
	return false
}

// newNotificationID returns an identifier for one message. The clients dedupe on
// it — the desktop notification uses it as its tag and the IDE keeps the ones it
// has shown — so it only has to be unique within a lab.
func newNotificationID() (string, error) {
	token, err := GenerateWorkspaceToken()
	if err != nil {
		return "", fmt.Errorf("failed to generate notification ID: %w", err)
	}
	return token, nil
}

// AddStudentNotification stores a message for a lab's students, dropping any
// that have already expired on the way.
func (jm *JobManager) AddStudentNotification(id string, n StudentNotification) error {
	jm.mu.RLock()
	job, exists := jm.jobs[id]
	jm.mu.RUnlock()
	if !exists {
		return fmt.Errorf("job %s not found", id)
	}

	job.mu.Lock()
	job.Notifications = append(pruneNotifications(job.Notifications, n.At), n)
	// Oldest first, so dropping the head drops the oldest.
	if len(job.Notifications) > maxLabNotifications {
		job.Notifications = job.Notifications[len(job.Notifications)-maxLabNotifications:]
	}
	job.UpdatedAt = time.Now()
	job.mu.Unlock()
	return nil
}

// DeleteStudentNotification removes one message early — the admin's Cancel — and
// prunes whatever else has expired meanwhile. It reports whether the message was
// still there.
func (jm *JobManager) DeleteStudentNotification(id, notificationID string) (bool, error) {
	jm.mu.RLock()
	job, exists := jm.jobs[id]
	jm.mu.RUnlock()
	if !exists {
		return false, fmt.Errorf("job %s not found", id)
	}

	now := time.Now()
	job.mu.Lock()
	defer job.mu.Unlock()

	kept := make([]StudentNotification, 0, len(job.Notifications))
	found := false
	for _, n := range job.Notifications {
		if n.ID == notificationID {
			found = true
			continue
		}
		if n.expired(now) {
			continue
		}
		kept = append(kept, n)
	}
	job.Notifications = kept
	job.UpdatedAt = now
	return found, nil
}

// LabNotifications returns every unexpired message of a lab, newest first. It is
// the admin's view: it includes the per-student ones, which a student endpoint
// must never do.
func (jm *JobManager) LabNotifications(id string) []StudentNotification {
	jm.mu.RLock()
	job, exists := jm.jobs[id]
	jm.mu.RUnlock()
	if !exists {
		return nil
	}

	now := time.Now()
	job.mu.RLock()
	active := pruneNotifications(job.Notifications, now)
	job.mu.RUnlock()

	out := make([]StudentNotification, len(active))
	copy(out, active)
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out
}

// ActiveStudentNotifications returns the unexpired messages of a lab that are
// addressed to the student signed in as email, newest first.
func (jm *JobManager) ActiveStudentNotifications(id, email string) []StudentNotification {
	jm.mu.RLock()
	job, exists := jm.jobs[id]
	jm.mu.RUnlock()
	if !exists {
		return nil
	}

	now := time.Now()
	job.mu.RLock()
	var out []StudentNotification
	for _, n := range job.Notifications {
		if n.expired(now) || !notificationBelongsTo(n, email) {
			continue
		}
		out = append(out, n)
	}
	job.mu.RUnlock()

	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out
}

// pruneNotifications returns the entries still alive at now, keeping their
// order. Callers hold the job's lock.
func pruneNotifications(entries []StudentNotification, now time.Time) []StudentNotification {
	kept := make([]StudentNotification, 0, len(entries))
	for _, n := range entries {
		if n.expired(now) {
			continue
		}
		kept = append(kept, n)
	}
	return kept
}

// studentInLab reports whether the student signed in as email is one of the
// lab's students, so a lab-wide message does not reach another lab's students.
//
// There is no student registry in EasyLab: a student's only identity is the
// email on their session. The lab's own workspace history is the cheap, accurate
// answer — it is already in memory, where asking the cluster would mean a call
// per lab on every poll. The consequence is that a student who has signed in but
// never requested a workspace in the lab is not yet one of its students.
func studentInLab(job *Job, email string) bool {
	if email == "" {
		return false
	}
	job.mu.RLock()
	defer job.mu.RUnlock()
	for _, event := range job.WorkspaceEvents {
		if event.Owner == "" {
			continue
		}
		if normalizeEmail(event.Owner) == normalizeEmail(email) || event.Owner == usernameFromEmail(email) {
			return true
		}
	}
	return false
}

// ensureNotifySecret returns the key that signs a lab's workspace notification
// tokens, generating one the first time a workspace needs it. It reports whether
// it had to, so the caller can persist the job.
func ensureNotifySecret(job *Job) (secret string, generated bool, err error) {
	job.mu.Lock()
	defer job.mu.Unlock()
	if job.NotifySecret != "" {
		return job.NotifySecret, false, nil
	}
	created, err := GenerateWorkspaceToken()
	if err != nil {
		return "", false, fmt.Errorf("failed to generate notification secret: %w", err)
	}
	job.NotifySecret = created
	return created, true, nil
}

// notifyToken is the credential a student's IDE sends to read its own
// notifications. The IDE runs in the student's own container with no session
// cookie, so it cannot use RequireStudentAuth; it carries this instead.
//
// The token is an HMAC over the lab and the workspace's owner, which makes it
// verifiable with no lookup, no stored state and no call to the lab's cluster,
// and deterministic — the same workspace gets the same token back after its pod
// restarts. It authorizes exactly one thing: reading the messages addressed to
// that one owner in that one lab.
func notifyToken(secret, labID, owner string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(labID))
	mac.Write([]byte{0})
	mac.Write([]byte(owner))
	return owner + "." + hex.EncodeToString(mac.Sum(nil))
}

// verifyNotifyToken returns the workspace owner a token is good for, or false.
func verifyNotifyToken(secret, labID, token string) (owner string, ok bool) {
	if secret == "" || token == "" {
		return "", false
	}
	// The owner is a sanitized username or an email, neither of which contains a
	// dot-separated signature, so split on the last dot.
	cut := strings.LastIndex(token, ".")
	if cut <= 0 || cut == len(token)-1 {
		return "", false
	}
	owner = token[:cut]
	expected := notifyToken(secret, labID, owner)
	if subtle.ConstantTimeCompare([]byte(token), []byte(expected)) != 1 {
		return "", false
	}
	return owner, true
}

// workspaceNotifyPath is the endpoint a student's IDE reads its notifications
// from, relative to whichever EasyLab it can reach.
const workspaceNotifyPath = "/api/workspace/notifications"

// workspaceNotifyAccess returns where a new workspace's IDE should poll for its
// owner's notifications, and the token it must carry. Both are empty when the
// workspace has nowhere to reach: the student then gets their messages on the
// portal pages only.
//
// The address is the lab's own in-lab portal when it has one — that is in the
// same cluster as the workspace and keeps working while this instance is down —
// and otherwise this instance's public address. Without either there is nothing
// a pod in the lab's cluster could reliably resolve, so the feature is left off
// rather than pointing the IDE at a URL only the admin's browser can reach.
func (h *Handler) workspaceNotifyAccess(labID, owner string) (notifyURL, token string) {
	job, exists := h.jobManager.GetJob(labID)
	if !exists {
		return "", ""
	}

	base := ""
	h.portalErrorsMu.RLock()
	portalURL := h.portalURLs[labID]
	h.portalErrorsMu.RUnlock()
	if portalURL != "" {
		base = portalURL
	} else if public := h.effectivePublicURL(); public != "" {
		base = public
	}
	if base == "" {
		return "", ""
	}

	secret, generated, err := ensureNotifySecret(job)
	if err != nil {
		log.Printf("[notify] failed to prepare the notification secret for lab %s: %v", labID, err)
		return "", ""
	}
	if generated {
		if err := h.jobManager.SaveJob(labID); err != nil {
			// Logged, not fatal: this workspace's token works now, and a lost
			// secret only means the next one is minted against a fresh key.
			log.Printf("[notify] failed to persist the notification secret for lab %s: %v", labID, err)
		}
		// The lab's own portal verifies these tokens, so it needs the key too.
		go h.syncPortalState(labID)
	}

	owner = normalizeRecipient(owner)
	notifyURL = fmt.Sprintf("%s%s?lab_id=%s", strings.TrimRight(base, "/"), workspaceNotifyPath, url.QueryEscape(labID))
	return notifyURL, notifyToken(secret, labID, owner)
}

// notificationResponse is one message as the clients consume it. It deliberately
// leaves out Recipient and Sender: a student is told what the message says, not
// who else it went to or which admin wrote it.
type notificationResponse struct {
	ID        string `json:"id"`
	Message   string `json:"message"`
	Level     string `json:"level"`
	At        string `json:"at"`
	ExpiresAt string `json:"expires_at"`
	LabName   string `json:"lab_name,omitempty"`
}

func newNotificationResponse(n StudentNotification, labName string) notificationResponse {
	return notificationResponse{
		ID:        n.ID,
		Message:   n.Message,
		Level:     n.Level,
		At:        n.At.Format(time.RFC3339),
		ExpiresAt: n.ExpiresAt.Format(time.RFC3339),
		LabName:   labName,
	}
}

// writeNotifications sends a notification list as the JSON both clients expect.
func writeNotifications(w http.ResponseWriter, entries []notificationResponse) {
	if entries == nil {
		entries = []notificationResponse{}
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]interface{}{"notifications": entries}); err != nil {
		log.Printf("[notify] failed to encode notifications: %v", err)
	}
}

// StudentNotifications handles GET /api/student/notifications: the unexpired
// messages addressed to the authenticated student across the labs they are a
// student of, newest first. The portal pages poll it and raise a desktop
// notification for each one they have not shown yet.
func (h *Handler) StudentNotifications(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	// The identity is always the authenticated student — never trusted from the client.
	email := studentEmailFromContext(r)
	if email == "" {
		writeNotifications(w, nil)
		return
	}

	// Collected with their send time so the whole list can be ordered before it is
	// formatted — newest first across every lab, not per lab.
	type labNotification struct {
		notification StudentNotification
		labName      string
	}
	var found []labNotification

	for _, job := range h.jobManager.GetAllJobs() {
		// An in-lab portal serves one lab, and everyone signed in to it is a
		// student of that lab; elsewhere the lab's history says who is.
		if h.portal == nil && !studentInLab(job, email) {
			continue
		}
		job.mu.RLock()
		labName := job.ID
		if job.Config != nil && job.Config.StackName != "" {
			labName = job.Config.StackName
		}
		job.mu.RUnlock()

		for _, n := range h.jobManager.ActiveStudentNotifications(job.ID, email) {
			found = append(found, labNotification{notification: n, labName: labName})
		}
	}

	sort.Slice(found, func(i, j int) bool {
		return found[i].notification.At.After(found[j].notification.At)
	})

	entries := make([]notificationResponse, 0, len(found))
	for _, f := range found {
		entries = append(entries, newNotificationResponse(f.notification, f.labName))
	}
	writeNotifications(w, entries)
}

// WorkspaceNotifications handles GET /api/workspace/notifications?lab_id=&token=:
// the messages for one workspace's owner, read from inside the student's IDE.
//
// It is the one student-facing endpoint not behind RequireStudentAuth: the IDE
// has no session cookie, so it authenticates with the workspace's notify token
// (see notifyToken). The token names the lab and the owner; nothing else is read
// from the request.
func (h *Handler) WorkspaceNotifications(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
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
		log.Printf("[notify] workspace notifications requested for unknown lab %s", labID)
		writeJSONError(w, http.StatusForbidden, "Not authorized")
		return
	}

	job.mu.RLock()
	secret := job.NotifySecret
	job.mu.RUnlock()

	owner, ok := verifyNotifyToken(secret, labID, token)
	if !ok {
		log.Printf("[notify] rejected workspace notification token for lab %s", labID)
		writeJSONError(w, http.StatusForbidden, "Not authorized")
		return
	}

	var entries []notificationResponse
	for _, n := range h.jobManager.ActiveStudentNotifications(labID, owner) {
		entries = append(entries, newNotificationResponse(n, ""))
	}
	writeNotifications(w, entries)
}

// SendLabNotification handles POST /api/labs/{id}/notifications: an admin sends
// a message to every student of a lab, or to one of them. It answers with the
// lab's notification panel, which is what the form targets.
func (h *Handler) SendLabNotification(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	labID := notificationLabID(r.URL.Path)
	if labID == "" {
		http.Error(w, "lab is required", http.StatusBadRequest)
		return
	}

	if _, exists := h.jobManager.GetJob(labID); !exists {
		http.Error(w, "Lab not found", http.StatusNotFound)
		return
	}

	if err := h.parseForm(w, r, 0); err != nil {
		log.Printf("Failed to parse form while sending a notification for lab %s: %v", labID, err)
		writeToast(w, false, "Could not read the submitted form.")
		return
	}

	message := strings.TrimSpace(getFormValue(r, "message"))
	level := strings.TrimSpace(getFormValue(r, "level"))
	recipient := strings.TrimSpace(getFormValue(r, "recipient"))
	ttlMinutes := atoiForm(getFormValue(r, "ttl_minutes"))

	if message == "" {
		writeToast(w, false, "Type a message to send.")
		return
	}
	message = truncateNotificationMessage(message)
	if level == "" {
		level = NotificationLevelInfo
	}
	if !validNotificationLevel(level) {
		writeToast(w, false, "Pick a valid message level.")
		return
	}
	if !validNotificationTTL(ttlMinutes) {
		writeToast(w, false, "Pick how long the message should stay up.")
		return
	}

	id, err := newNotificationID()
	if err != nil {
		log.Printf("Failed to generate a notification ID for lab %s: %v", labID, err)
		writeToast(w, false, "Could not send the message. Try again.")
		return
	}

	now := time.Now()
	notification := StudentNotification{
		ID:        id,
		At:        now,
		ExpiresAt: now.Add(time.Duration(ttlMinutes) * time.Minute),
		Message:   message,
		Level:     level,
		Recipient: normalizeRecipient(recipient),
		Sender:    adminActor(r),
	}

	if err := h.jobManager.AddStudentNotification(labID, notification); err != nil {
		log.Printf("Failed to store a notification for lab %s: %v", labID, err)
		writeToast(w, false, "Could not send the message. Try again.")
		return
	}

	// Persisted so a restart does not drop a message that is still up, and pushed
	// to the lab's own portal — where its students may be — the way an admin
	// opening a workspace is (see OpenWorkspaceAsAdmin).
	if err := h.jobManager.SaveJob(labID); err != nil {
		log.Printf("[notify] failed to persist notification for lab %s: %v", labID, err)
	}
	go h.syncPortalState(labID)

	audience := "all students"
	if notification.Recipient != "" {
		audience = notification.Recipient
	}
	// The message body is never audited: it is the admin's words, and the audit
	// log's own rule is that Detail carries context, not content.
	h.recordAudit(adminActor(r), "admin", "lab.notify", labID,
		fmt.Sprintf("%s, %s, %d min", audience, level, ttlMinutes))
	log.Printf("Sent a %s notification to %s in lab %s for %d minutes", level, audience, labID, ttlMinutes)

	h.ServeLabNotifications(w, r)
}

// CancelLabNotification handles POST /api/labs/{id}/notifications/{nid}/cancel:
// it takes a message down before its TTL runs out.
func (h *Handler) CancelLabNotification(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	labID, notificationID := cancelNotificationPathParts(r.URL.Path)
	if labID == "" || notificationID == "" {
		http.Error(w, "lab and notification are required", http.StatusBadRequest)
		return
	}

	if _, exists := h.jobManager.GetJob(labID); !exists {
		http.Error(w, "Lab not found", http.StatusNotFound)
		return
	}

	removed, err := h.jobManager.DeleteStudentNotification(labID, notificationID)
	if err != nil {
		log.Printf("Failed to cancel notification %s for lab %s: %v", notificationID, labID, err)
		writeToast(w, false, "Could not take the message down. Try again.")
		return
	}
	if removed {
		if err := h.jobManager.SaveJob(labID); err != nil {
			log.Printf("[notify] failed to persist notification cancel for lab %s: %v", labID, err)
		}
		go h.syncPortalState(labID)
		h.recordAudit(adminActor(r), "admin", "lab.notify_cancel", labID, "")
		log.Printf("Cancelled notification %s in lab %s", notificationID, labID)
	}

	h.ServeLabNotifications(w, r)
}

// ServeLabNotifications handles GET /api/labs/{id}/notifications: the panel
// listing what is currently up for a lab, with the students it can be addressed
// to. It is also what the send and cancel handlers answer with.
func (h *Handler) ServeLabNotifications(w http.ResponseWriter, r *http.Request) {
	labID := notificationLabID(r.URL.Path)
	if labID == "" {
		http.Error(w, "lab is required", http.StatusBadRequest)
		return
	}

	if _, exists := h.jobManager.GetJob(labID); !exists {
		http.Error(w, "Lab not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "text/html")
	fmt.Fprint(w, h.renderNotificationsPanel(labID))
}

// renderNotificationsPanel builds the list of messages still showing for a lab,
// each with a button to take it down early. Like buildWorkspaceStatusHTML and
// the credentials panel it is a fragment assembled here rather than a template,
// which is how the lab detail page's other HTMX panels answer.
func (h *Handler) renderNotificationsPanel(labID string) string {
	active := h.jobManager.LabNotifications(labID)
	if len(active) == 0 {
		return `<p class="lab-notify-empty">No message is showing. Anything you send disappears on its own when its time is up.</p>`
	}

	now := time.Now()
	var b strings.Builder
	for _, n := range active {
		audience := "All students"
		if n.Recipient != "" {
			audience = n.Recipient
		}
		fmt.Fprintf(&b, `<div class="lab-notify-item lab-notify-item--%s">`, template.HTMLEscapeString(n.Level))
		fmt.Fprintf(&b, `<div class="lab-notify-item-message">%s</div>`, template.HTMLEscapeString(n.Message))
		fmt.Fprintf(&b, `<div class="lab-notify-item-meta"><span>%s</span><span>%s left</span></div>`,
			template.HTMLEscapeString(audience), template.HTMLEscapeString(humanTimeLeft(n.ExpiresAt, now)))
		fmt.Fprintf(&b, `<form class="lab-notify-item-action" hx-post="/api/labs/%s/notifications/%s/cancel" hx-target="#notifications-panel" hx-swap="innerHTML">`,
			template.HTMLEscapeString(labID), template.HTMLEscapeString(n.ID))
		b.WriteString(`<button type="submit" class="btn btn-secondary btn-sm">Take down</button>`)
		b.WriteString(`</form>`)
		b.WriteString(`</div>`)
	}
	return b.String()
}

// labStudents lists the students who have had a workspace in the lab, so the
// admin can address one of them. Same source as studentInLab, sorted and
// de-duplicated for the picker.
func labStudents(job *Job) []string {
	job.mu.RLock()
	defer job.mu.RUnlock()

	seen := map[string]bool{}
	var students []string
	for _, event := range job.WorkspaceEvents {
		owner := strings.TrimSpace(event.Owner)
		if owner == "" || seen[owner] {
			continue
		}
		seen[owner] = true
		students = append(students, owner)
	}
	sort.Strings(students)
	return students
}

// truncateNotificationMessage caps a message at maxNotificationMessageLength
// characters. Counted in runes, not bytes: slicing bytes would cut an accented
// character in half and leave invalid UTF-8 in the message students are served.
func truncateNotificationMessage(message string) string {
	runes := []rune(message)
	if len(runes) <= maxNotificationMessageLength {
		return message
	}
	return string(runes[:maxNotificationMessageLength])
}

// normalizeRecipient puts a recipient in the form the student endpoints compare
// against, matching workspaceOwnerKey: an email is lowercased, a sanitized
// username is left alone.
func normalizeRecipient(recipient string) string {
	if recipient == "" {
		return ""
	}
	if strings.Contains(recipient, "@") {
		return normalizeEmail(recipient)
	}
	return recipient
}

// humanTimeLeft renders how long a message has left, for the admin's list.
func humanTimeLeft(expiresAt, now time.Time) string {
	remaining := expiresAt.Sub(now)
	if remaining <= 0 {
		return "no time"
	}
	if remaining < time.Minute {
		return "less than a minute"
	}
	minutes := int(remaining.Round(time.Minute) / time.Minute)
	if minutes == 1 {
		return "1 minute"
	}
	return fmt.Sprintf("%d minutes", minutes)
}

// notificationLabID pulls the lab out of /api/labs/{id}/notifications.
func notificationLabID(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 4 || parts[0] != "api" || parts[1] != "labs" || parts[3] != "notifications" {
		return ""
	}
	return parts[2]
}

// cancelNotificationPathParts pulls the lab and the message out of
// /api/labs/{id}/notifications/{nid}/cancel.
func cancelNotificationPathParts(path string) (labID, notificationID string) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 6 || parts[0] != "api" || parts[1] != "labs" || parts[3] != "notifications" || parts[5] != "cancel" {
		return "", ""
	}
	return parts[2], parts[4]
}
