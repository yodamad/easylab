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
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newNotifyTestHandler returns a handler with one lab whose workspace history
// names the given owners, which is what makes them students of it.
func newNotifyTestHandler(t *testing.T, owners ...string) (*Handler, string) {
	t.Helper()

	jm := NewJobManager("")
	labID := jm.CreateJob(&LabConfig{StackName: "workshop"})
	job, ok := jm.GetJob(labID)
	require.True(t, ok)

	job.mu.Lock()
	job.Status = JobStatusCompleted
	for i, owner := range owners {
		job.WorkspaceEvents = append(job.WorkspaceEvents, WorkspaceEvent{
			At:            time.Now(),
			Action:        WorkspaceEventCreated,
			WorkspaceID:   fmt.Sprintf("ws-%d", i),
			WorkspaceName: fmt.Sprintf("ws-%d", i),
			Owner:         owner,
		})
	}
	job.mu.Unlock()

	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	return h, labID
}

func testNotification(message, recipient string, ttl time.Duration) StudentNotification {
	now := time.Now()
	return StudentNotification{
		// Space-free: an ID is spliced into a request path by the cancel tests.
		ID:        "n-" + strings.ReplaceAll(message, " ", "-"),
		At:        now,
		ExpiresAt: now.Add(ttl),
		Message:   message,
		Level:     NotificationLevelInfo,
		Recipient: recipient,
	}
}

func TestNotificationBelongsTo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		recipient string
		email     string
		want      bool
	}{
		{name: "lab-wide reaches everyone", recipient: "", email: "alice@example.com", want: true},
		{name: "lab-wide reaches a signed-out reader too", recipient: "", email: "", want: true},
		{name: "same email", recipient: "alice@example.com", email: "alice@example.com", want: true},
		{name: "email case is ignored", recipient: "Alice@Example.com", email: "alice@example.com", want: true},
		{name: "recipient recorded as a username", recipient: "alice", email: "alice@example.com", want: true},
		{name: "another student", recipient: "bob@example.com", email: "alice@example.com", want: false},
		{name: "addressed message with no reader", recipient: "alice@example.com", email: "", want: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := notificationBelongsTo(StudentNotification{Recipient: tt.recipient}, tt.email)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestValidNotificationLevelAndTTL(t *testing.T) {
	t.Parallel()

	assert.True(t, validNotificationLevel(NotificationLevelInfo))
	assert.True(t, validNotificationLevel(NotificationLevelWarning))
	assert.False(t, validNotificationLevel("critical"))
	assert.False(t, validNotificationLevel(""))

	assert.True(t, validNotificationTTL(15))
	assert.False(t, validNotificationTTL(0))
	assert.False(t, validNotificationTTL(7))
	// A long TTL is not on offer: a message outliving the workshop is a bug, not
	// a feature.
	assert.False(t, validNotificationTTL(1440))
}

// An expired message is served to nobody, and is dropped from the job the next
// time one is written — that is the whole of the auto-expiry.
func TestStudentNotificationExpiry(t *testing.T) {
	t.Parallel()

	jm := NewJobManager("")
	labID := jm.CreateJob(&LabConfig{StackName: "workshop"})

	live := testNotification("still up", "", 10*time.Minute)
	stale := testNotification("long gone", "", -time.Minute)
	require.NoError(t, jm.AddStudentNotification(labID, stale))
	require.NoError(t, jm.AddStudentNotification(labID, live))

	active := jm.ActiveStudentNotifications(labID, "alice@example.com")
	require.Len(t, active, 1)
	assert.Equal(t, "still up", active[0].Message)

	// The expired one was pruned by the second write, not merely filtered on read.
	job, ok := jm.GetJob(labID)
	require.True(t, ok)
	job.mu.RLock()
	stored := len(job.Notifications)
	job.mu.RUnlock()
	assert.Equal(t, 1, stored)

	assert.Empty(t, jm.LabNotifications("job-does-not-exist"))
}

func TestStudentNotificationRecipients(t *testing.T) {
	t.Parallel()

	jm := NewJobManager("")
	labID := jm.CreateJob(&LabConfig{StackName: "workshop"})

	require.NoError(t, jm.AddStudentNotification(labID, testNotification("everyone", "", time.Hour)))
	require.NoError(t, jm.AddStudentNotification(labID, testNotification("for alice", "alice@example.com", time.Hour)))

	alice := jm.ActiveStudentNotifications(labID, "alice@example.com")
	require.Len(t, alice, 2)

	bob := jm.ActiveStudentNotifications(labID, "bob@example.com")
	require.Len(t, bob, 1)
	assert.Equal(t, "everyone", bob[0].Message)

	// The admin's own view keeps both, including who the addressed one is for.
	assert.Len(t, jm.LabNotifications(labID), 2)
}

func TestStudentNotificationsAreCapped(t *testing.T) {
	t.Parallel()

	jm := NewJobManager("")
	labID := jm.CreateJob(&LabConfig{StackName: "workshop"})

	for i := 0; i < maxLabNotifications+10; i++ {
		n := testNotification(fmt.Sprintf("message %d", i), "", time.Hour)
		n.ID = fmt.Sprintf("n-%d", i)
		require.NoError(t, jm.AddStudentNotification(labID, n))
	}

	job, ok := jm.GetJob(labID)
	require.True(t, ok)
	job.mu.RLock()
	stored := append([]StudentNotification(nil), job.Notifications...)
	job.mu.RUnlock()

	require.Len(t, stored, maxLabNotifications)
	// The oldest went, not the newest.
	assert.Equal(t, "message 10", stored[0].Message)
}

func TestDeleteStudentNotification(t *testing.T) {
	t.Parallel()

	jm := NewJobManager("")
	labID := jm.CreateJob(&LabConfig{StackName: "workshop"})
	n := testNotification("taken down", "", time.Hour)
	require.NoError(t, jm.AddStudentNotification(labID, n))

	removed, err := jm.DeleteStudentNotification(labID, n.ID)
	require.NoError(t, err)
	assert.True(t, removed)
	assert.Empty(t, jm.LabNotifications(labID))

	removed, err = jm.DeleteStudentNotification(labID, n.ID)
	require.NoError(t, err)
	assert.False(t, removed, "a message already gone is not removed twice")

	_, err = jm.DeleteStudentNotification("job-missing", n.ID)
	require.Error(t, err)
}

func TestStudentInLabAndLabStudents(t *testing.T) {
	t.Parallel()

	h, labID := newNotifyTestHandler(t, "alice@example.com", "bob", "alice@example.com")
	job, ok := h.jobManager.GetJob(labID)
	require.True(t, ok)

	assert.True(t, studentInLab(job, "alice@example.com"))
	assert.True(t, studentInLab(job, "ALICE@example.com"), "email case is ignored")
	assert.True(t, studentInLab(job, "bob@example.com"), "a username-only record still matches")
	assert.False(t, studentInLab(job, "carol@example.com"))
	assert.False(t, studentInLab(job, ""))

	// De-duplicated and sorted. This is the whole history, which is why the
	// recipient picker uses notifyRecipients instead — see below.
	assert.Equal(t, []string{"alice@example.com", "bob"}, labStudents(job))
}

func TestNotifyRecipients(t *testing.T) {
	t.Parallel()

	workspace := func(owner string) WorkspaceDisplay {
		return WorkspaceDisplay{Name: "ws-" + owner, Owner: owner}
	}

	tests := []struct {
		name string
		// history is who the lab remembers ever having a workspace.
		history []string
		// live is the lab's current workspace list; nil means it could not be had.
		live     *WorkspacesViewModel
		expected []string
	}{
		{
			name:    "only the students with a workspace right now",
			history: []string{"alice@example.com", "bob@example.com", "carol@example.com"},
			live: &WorkspacesViewModel{Workspaces: []WorkspaceDisplay{
				workspace("carol@example.com"), workspace("alice@example.com"),
			}},
			// Sorted, and bob is gone: his workspace was cleaned up.
			expected: []string{"alice@example.com", "carol@example.com"},
		},
		{
			name:     "nobody left is an empty picker, not the whole history",
			history:  []string{"alice@example.com", "bob@example.com"},
			live:     &WorkspacesViewModel{},
			expected: nil,
		},
		{
			name:    "duplicates collapse",
			history: []string{"alice@example.com"},
			live: &WorkspacesViewModel{Workspaces: []WorkspaceDisplay{
				workspace("alice@example.com"), workspace("alice@example.com"),
			}},
			expected: []string{"alice@example.com"},
		},
		{
			name:    "an unattributed workspace is not a recipient",
			history: []string{"alice@example.com"},
			live: &WorkspacesViewModel{Workspaces: []WorkspaceDisplay{
				workspace("alice@example.com"), {Name: "ws-orphan", Owner: "  "},
			}},
			expected: []string{"alice@example.com"},
		},
		{
			name:    "no live list falls back to the history",
			history: []string{"bob@example.com", "alice@example.com"},
			live:    nil,
			// Better a name too many than a picker the admin cannot use at all.
			expected: []string{"alice@example.com", "bob@example.com"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h, labID := newNotifyTestHandler(t, tt.history...)
			job, ok := h.jobManager.GetJob(labID)
			require.True(t, ok)

			assert.Equal(t, tt.expected, notifyRecipients(job, tt.live))
		})
	}
}

func TestNotifyTokenVerification(t *testing.T) {
	t.Parallel()

	const (
		secret = "s3cret-signing-key"
		labID  = "job-1111"
		owner  = "alice@example.com"
	)
	token := notifyToken(secret, labID, owner)

	tests := []struct {
		name   string
		secret string
		labID  string
		token  string
		want   string
		ok     bool
	}{
		{name: "the token it was minted for", secret: secret, labID: labID, token: token, want: owner, ok: true},
		{name: "another lab's token is refused", secret: secret, labID: "job-2222", token: token},
		{name: "another key is refused", secret: "other-key", labID: labID, token: token},
		{name: "a tampered owner is refused", secret: secret, labID: labID, token: "bob@example.com." + strings.SplitN(token, ".", 2)[1]},
		{name: "a tampered signature is refused", secret: secret, labID: labID, token: owner + ".deadbeef"},
		{name: "no signature at all", secret: secret, labID: labID, token: owner},
		{name: "empty token", secret: secret, labID: labID, token: ""},
		{name: "no key configured yet", secret: "", labID: labID, token: token},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := verifyNotifyToken(tt.secret, tt.labID, tt.token)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

// The token is deterministic, so a workspace whose pod restarts keeps reading
// its notifications without being handed a new one.
func TestNotifyTokenIsStable(t *testing.T) {
	t.Parallel()

	first := notifyToken("key", "job-1", "alice@example.com")
	second := notifyToken("key", "job-1", "alice@example.com")
	assert.Equal(t, first, second)
	assert.NotEqual(t, first, notifyToken("key", "job-1", "bob@example.com"))
}

func TestEnsureNotifySecret(t *testing.T) {
	t.Parallel()

	job := &Job{ID: "job-1"}

	secret, generated, err := ensureNotifySecret(job)
	require.NoError(t, err)
	assert.True(t, generated)
	assert.NotEmpty(t, secret)

	again, generated, err := ensureNotifySecret(job)
	require.NoError(t, err)
	assert.False(t, generated, "an existing key is kept")
	assert.Equal(t, secret, again)
}

func TestSendLabNotification(t *testing.T) {
	t.Parallel()

	h, labID := newNotifyTestHandler(t, "alice@example.com")

	form := url.Values{
		"message":     {"Break for 10 minutes."},
		"level":       {NotificationLevelWarning},
		"recipient":   {""},
		"ttl_minutes": {"15"},
	}
	req := httptest.NewRequest(http.MethodPost, "/api/labs/"+labID+"/notifications", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	h.SendLabNotification(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "Break for 10 minutes.")
	assert.Contains(t, rec.Body.String(), "All students")

	stored := h.jobManager.LabNotifications(labID)
	require.Len(t, stored, 1)
	assert.Equal(t, NotificationLevelWarning, stored[0].Level)
	assert.Empty(t, stored[0].Recipient)
	assert.WithinDuration(t, time.Now().Add(15*time.Minute), stored[0].ExpiresAt, time.Minute)
}

func TestSendLabNotificationRejectsBadInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		form url.Values
	}{
		{name: "no message", form: url.Values{"level": {"info"}, "ttl_minutes": {"15"}}},
		{name: "blank message", form: url.Values{"message": {"   "}, "ttl_minutes": {"15"}}},
		{name: "unknown level", form: url.Values{"message": {"hi"}, "level": {"critical"}, "ttl_minutes": {"15"}}},
		{name: "no lifetime", form: url.Values{"message": {"hi"}, "level": {"info"}}},
		{name: "a lifetime not on offer", form: url.Values{"message": {"hi"}, "level": {"info"}, "ttl_minutes": {"999"}}},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h, labID := newNotifyTestHandler(t, "alice@example.com")
			req := httptest.NewRequest(http.MethodPost, "/api/labs/"+labID+"/notifications", strings.NewReader(tt.form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rec := httptest.NewRecorder()

			h.SendLabNotification(rec, req)

			assert.Contains(t, rec.Body.String(), "toast-error")
			assert.Empty(t, h.jobManager.LabNotifications(labID), "nothing is stored for a rejected send")
		})
	}
}

func TestSendLabNotificationGuards(t *testing.T) {
	t.Parallel()

	h, labID := newNotifyTestHandler(t, "alice@example.com")

	t.Run("method", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.SendLabNotification(rec, httptest.NewRequest(http.MethodGet, "/api/labs/"+labID+"/notifications", nil))
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})

	t.Run("unknown lab", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.SendLabNotification(rec, httptest.NewRequest(http.MethodPost, "/api/labs/job-missing/notifications", nil))
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})
}

func TestSendLabNotificationTruncatesLongMessages(t *testing.T) {
	t.Parallel()

	h, labID := newNotifyTestHandler(t, "alice@example.com")
	form := url.Values{
		"message":     {strings.Repeat("x", maxNotificationMessageLength+250)},
		"level":       {NotificationLevelInfo},
		"ttl_minutes": {"5"},
	}
	req := httptest.NewRequest(http.MethodPost, "/api/labs/"+labID+"/notifications", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	h.SendLabNotification(httptest.NewRecorder(), req)

	stored := h.jobManager.LabNotifications(labID)
	require.Len(t, stored, 1)
	assert.Len(t, stored[0].Message, maxNotificationMessageLength)
}

// Characters, not bytes: slicing bytes would cut an accented character in half.
func TestTruncateNotificationMessage(t *testing.T) {
	t.Parallel()

	short := "Pause café ☕"
	assert.Equal(t, short, truncateNotificationMessage(short))

	long := strings.Repeat("é", maxNotificationMessageLength+20)
	got := truncateNotificationMessage(long)
	assert.Equal(t, maxNotificationMessageLength, len([]rune(got)))
	assert.True(t, utf8.ValidString(got))
}

func TestCancelLabNotification(t *testing.T) {
	t.Parallel()

	h, labID := newNotifyTestHandler(t, "alice@example.com")
	n := testNotification("going away", "", time.Hour)
	require.NoError(t, h.jobManager.AddStudentNotification(labID, n))

	path := "/api/labs/" + labID + "/notifications/" + n.ID + "/cancel"
	rec := httptest.NewRecorder()
	h.CancelLabNotification(rec, httptest.NewRequest(http.MethodPost, path, nil))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, h.jobManager.LabNotifications(labID))
	// The panel the form targets comes back, now empty.
	assert.Contains(t, rec.Body.String(), "lab-notify-empty")
}

func TestStudentNotificationsEndpoint(t *testing.T) {
	t.Parallel()

	h, labID := newNotifyTestHandler(t, "alice@example.com")
	require.NoError(t, h.jobManager.AddStudentNotification(labID, testNotification("everyone", "", time.Hour)))
	require.NoError(t, h.jobManager.AddStudentNotification(labID, testNotification("for alice", "alice@example.com", time.Hour)))

	read := func(email string) []notificationResponse {
		req := httptest.NewRequest(http.MethodGet, "/api/student/notifications", nil)
		if email != "" {
			req = req.WithContext(context.WithValue(req.Context(), studentEmailContextKey, email))
		}
		rec := httptest.NewRecorder()
		h.StudentNotifications(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)

		var payload struct {
			Notifications []notificationResponse `json:"notifications"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
		return payload.Notifications
	}

	alice := read("alice@example.com")
	require.Len(t, alice, 2)
	assert.Equal(t, "workshop", alice[0].LabName)

	// A student of another lab is not one of this lab's students, so a lab-wide
	// message does not reach them.
	assert.Empty(t, read("carol@example.com"))
	// Nor does anything reach a request with no identity on it.
	assert.Empty(t, read(""))
}

func TestStudentNotificationsRejectsNonGet(t *testing.T) {
	t.Parallel()

	h, _ := newNotifyTestHandler(t, "alice@example.com")
	rec := httptest.NewRecorder()
	h.StudentNotifications(rec, httptest.NewRequest(http.MethodPost, "/api/student/notifications", nil))
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

// The messages never leak who else they went to, or which admin sent them.
func TestStudentNotificationResponseHidesAdminFields(t *testing.T) {
	t.Parallel()

	n := testNotification("hello", "alice@example.com", time.Hour)
	n.Sender = "teacher@example.com"
	encoded, err := json.Marshal(newNotificationResponse(n, "workshop"))
	require.NoError(t, err)

	assert.NotContains(t, string(encoded), "teacher@example.com")
	assert.NotContains(t, string(encoded), "alice@example.com")
	assert.Contains(t, string(encoded), "hello")
}

func TestWorkspaceNotificationsEndpoint(t *testing.T) {
	t.Parallel()

	h, labID := newNotifyTestHandler(t, "alice@example.com")
	job, ok := h.jobManager.GetJob(labID)
	require.True(t, ok)
	secret, _, err := ensureNotifySecret(job)
	require.NoError(t, err)

	require.NoError(t, h.jobManager.AddStudentNotification(labID, testNotification("everyone", "", time.Hour)))
	require.NoError(t, h.jobManager.AddStudentNotification(labID, testNotification("for bob", "bob@example.com", time.Hour)))

	get := func(lab, token string) *httptest.ResponseRecorder {
		target := fmt.Sprintf("/api/workspace/notifications?lab_id=%s&token=%s",
			url.QueryEscape(lab), url.QueryEscape(token))
		rec := httptest.NewRecorder()
		h.WorkspaceNotifications(rec, httptest.NewRequest(http.MethodGet, target, nil))
		return rec
	}

	t.Run("its owner's messages", func(t *testing.T) {
		rec := get(labID, notifyToken(secret, labID, "alice@example.com"))
		require.Equal(t, http.StatusOK, rec.Code)

		var payload struct {
			Notifications []notificationResponse `json:"notifications"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
		require.Len(t, payload.Notifications, 1, "bob's message is not alice's to read")
		assert.Equal(t, "everyone", payload.Notifications[0].Message)
	})

	t.Run("a bad token", func(t *testing.T) {
		assert.Equal(t, http.StatusForbidden, get(labID, "alice@example.com.deadbeef").Code)
	})

	t.Run("an unknown lab answers like a bad token", func(t *testing.T) {
		rec := get("job-missing", notifyToken(secret, labID, "alice@example.com"))
		assert.Equal(t, http.StatusForbidden, rec.Code)
	})

	t.Run("missing parameters", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.WorkspaceNotifications(rec, httptest.NewRequest(http.MethodGet, "/api/workspace/notifications", nil))
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("method", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.WorkspaceNotifications(rec, httptest.NewRequest(http.MethodPost, "/api/workspace/notifications", nil))
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})
}

// With no portal and no public address there is nowhere in the lab's cluster the
// IDE could reach, so the feature is left off rather than pointing it at a URL
// only the admin's browser can resolve.
func TestWorkspaceNotifyAccess(t *testing.T) {
	t.Parallel()

	h, labID := newNotifyTestHandler(t, "alice@example.com")

	notifyURL, token := h.workspaceNotifyAccess(labID, "alice@example.com")
	assert.Empty(t, notifyURL)
	assert.Empty(t, token)

	h.publicURL = "https://easylab.example.com"
	notifyURL, token = h.workspaceNotifyAccess(labID, "Alice@Example.com")
	require.NotEmpty(t, notifyURL)
	assert.True(t, strings.HasPrefix(notifyURL, "https://easylab.example.com/api/workspace/notifications?lab_id="))

	job, ok := h.jobManager.GetJob(labID)
	require.True(t, ok)
	job.mu.RLock()
	secret := job.NotifySecret
	job.mu.RUnlock()
	require.NotEmpty(t, secret, "the signing key is generated on first use")

	// The owner is normalized, so the token matches a recipient recorded the way
	// workspaceOwnerKey records it.
	owner, valid := verifyNotifyToken(secret, labID, token)
	assert.True(t, valid)
	assert.Equal(t, "alice@example.com", owner)

	// A lab's own portal is preferred: it is in the same cluster as the workspace.
	h.setPortalURL(labID, "https://lab.example.com")
	notifyURL, _ = h.workspaceNotifyAccess(labID, "alice@example.com")
	assert.True(t, strings.HasPrefix(notifyURL, "https://lab.example.com/api/workspace/notifications?lab_id="))

	notifyURL, _ = h.workspaceNotifyAccess("job-missing", "alice@example.com")
	assert.Empty(t, notifyURL)
}

func TestNotificationPathParsing(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "job-1", notificationLabID("/api/labs/job-1/notifications"))
	assert.Equal(t, "job-1", notificationLabID("/api/labs/job-1/notifications/abc/cancel"))
	assert.Empty(t, notificationLabID("/api/labs/job-1/secrets"))
	assert.Empty(t, notificationLabID("/api/labs"))

	labID, notificationID := cancelNotificationPathParts("/api/labs/job-1/notifications/abc/cancel")
	assert.Equal(t, "job-1", labID)
	assert.Equal(t, "abc", notificationID)

	labID, notificationID = cancelNotificationPathParts("/api/labs/job-1/notifications")
	assert.Empty(t, labID)
	assert.Empty(t, notificationID)
}

func TestHumanTimeLeft(t *testing.T) {
	t.Parallel()

	now := time.Now()
	tests := []struct {
		name string
		in   time.Duration
		want string
	}{
		{name: "already gone", in: -time.Minute, want: "no time"},
		{name: "seconds", in: 20 * time.Second, want: "less than a minute"},
		{name: "one minute", in: 80 * time.Second, want: "1 minute"},
		{name: "several minutes", in: 14*time.Minute + 30*time.Second, want: "15 minutes"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, humanTimeLeft(now.Add(tt.in), now))
		})
	}
}

// On an in-lab portal every signed-in student is a student of the lab it serves,
// so the roster check is skipped rather than run against a history the portal
// does not mirror.
func TestStudentNotificationsOnPortal(t *testing.T) {
	t.Parallel()

	h, labID := newNotifyTestHandler(t)
	require.NoError(t, h.jobManager.AddStudentNotification(labID, testNotification("everyone", "", time.Hour)))

	req := httptest.NewRequest(http.MethodGet, "/api/student/notifications", nil)
	req = req.WithContext(context.WithValue(req.Context(), studentEmailContextKey, "nobody@example.com"))

	// Without a portal the lab has no students, so nothing is served.
	rec := httptest.NewRecorder()
	h.StudentNotifications(rec, req)
	assert.JSONEq(t, `{"notifications":[]}`, rec.Body.String())

	h.portal = &PortalRuntime{labID: labID}
	rec = httptest.NewRecorder()
	h.StudentNotifications(rec, req)
	assert.Contains(t, rec.Body.String(), "everyone")
}

// The address handed to a workspace is polled from inside the lab's cluster, so
// anything that resolves to the pod itself or to the admin's own machine is
// worse than no address at all.
func TestReachableFromCluster(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{"public https host", "https://demo1.labdevrel.ovh", true},
		{"public host with port and path", "https://easylab.example.com:8443/base", true},
		{"private cluster IP", "http://10.0.0.12:8081", true},
		{"in-cluster service name", "http://easylab-portal.lab.svc:8081", true},
		{"bare hostname without scheme is not a host", "easylab.example.com", false},
		{"localhost", "http://localhost:8081", false},
		{"localhost mixed case", "http://LocalHost:8081", false},
		{"subdomain of localhost", "http://easylab.localhost:8081", false},
		{"ipv4 loopback", "http://127.0.0.1:8081", false},
		{"ipv4 loopback elsewhere in 127/8", "http://127.1.2.3", false},
		{"ipv6 loopback", "http://[::1]:8081", false},
		{"ipv4 unspecified", "http://0.0.0.0:8081", false},
		{"ipv6 unspecified", "http://[::]:8081", false},
		{"ipv4 link-local", "http://169.254.10.1:8081", false},
		{"ipv6 link-local", "http://[fe80::1]:8081", false},
		{"surrounding whitespace is trimmed", "  https://demo1.labdevrel.ovh  ", true},
		{"empty", "", false},
		{"blank", "   ", false},
		{"malformed", "http://[::1", false},
		{"scheme without host", "http://", false},
		{"port only", "http://:8081", false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, reachableFromCluster(tt.input))
		})
	}
}

// A loopback address is skipped at both levels, falling through to the next
// candidate and finally to nothing.
func TestWorkspaceNotifyAccess_SkipsUnreachableAddresses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		portalURL string
		publicURL string
		wantBase  string
	}{
		{"loopback public URL only", "", "http://localhost:8081", ""},
		{"loopback portal and public", "http://127.0.0.1:8081", "http://localhost:8081", ""},
		{"loopback portal falls back to a good public URL", "http://localhost:9000", "https://easylab.example.com", "https://easylab.example.com"},
		{"good portal wins over a loopback public URL", "https://lab.example.com", "http://localhost:8081", "https://lab.example.com"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h, labID := newNotifyTestHandler(t, "alice@example.com")
			h.publicURL = tt.publicURL
			if tt.portalURL != "" {
				h.setPortalURL(labID, tt.portalURL)
			}

			notifyURL, token := h.workspaceNotifyAccess(labID, "alice@example.com")
			if tt.wantBase == "" {
				assert.Empty(t, notifyURL)
				assert.Empty(t, token)
				return
			}
			assert.True(t, strings.HasPrefix(notifyURL, tt.wantBase+"/api/workspace/notifications?lab_id="), notifyURL)
			assert.NotEmpty(t, token)
		})
	}
}

func TestNotifyBaseFor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		portalURL string
		publicURL string
		want      string
	}{
		{"reachable portal URL wins over public URL", "https://lab.example.com", "https://easylab.example.com", "https://lab.example.com"},
		{"loopback portal URL falls through to public URL", "http://localhost:9000", "https://easylab.example.com", "https://easylab.example.com"},
		{"empty portal URL falls through to public URL", "", "https://easylab.example.com", "https://easylab.example.com"},
		{"both loopback", "http://127.0.0.1:9000", "http://localhost:8081", ""},
		{"empty portal URL and loopback public URL", "", "http://localhost:8081", ""},
		{"neither set", "", "", ""},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h, labID := newNotifyTestHandler(t, "alice@example.com")
			h.publicURL = tt.publicURL
			if tt.portalURL != "" {
				h.setPortalURL(labID, tt.portalURL)
			}

			assert.Equal(t, tt.want, h.notifyBaseFor(labID))

			job, ok := h.jobManager.GetJob(labID)
			require.True(t, ok)
			job.mu.RLock()
			secret := job.NotifySecret
			job.mu.RUnlock()
			assert.Empty(t, secret, "looking up the address must not mint the signing key")
		})
	}
}

func TestNotifyBaseFor_UnknownLab(t *testing.T) {
	t.Parallel()
	h, _ := newNotifyTestHandler(t)
	h.publicURL = ""
	assert.Empty(t, h.notifyBaseFor("no-such-lab"))
}
