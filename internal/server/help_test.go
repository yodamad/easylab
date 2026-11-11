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

// testHelpRequest builds an open request raised at a given age.
func testHelpRequest(id, student string, age time.Duration) HelpRequest {
	return HelpRequest{
		ID:      id,
		At:      time.Now().Add(-age),
		Student: student,
		Message: "stuck on " + id,
		Status:  HelpStatusWaiting,
		Source:  HelpSourcePortal,
	}
}

// storedHelpRequests reads a lab's raw queue, without the read-side filtering.
func storedHelpRequests(t *testing.T, jm *JobManager, labID string) []HelpRequest {
	t.Helper()
	job, ok := jm.GetJob(labID)
	require.True(t, ok)
	job.mu.RLock()
	defer job.mu.RUnlock()
	return append([]HelpRequest(nil), job.HelpRequests...)
}

// waitForAudit returns the audit log once it holds at least n entries. Entries
// are written off the request's path, so a test has to wait for them.
func waitForAudit(t *testing.T, as *AuditStore, n int) []AuditEntry {
	t.Helper()
	var entries []AuditEntry
	require.Eventually(t, func() bool {
		var err error
		entries, err = as.Recent(0)
		return err == nil && len(entries) >= n
	}, 2*time.Second, 10*time.Millisecond)
	return entries
}

func formRequest(method, target string, form url.Values) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

func TestHelpRequestExpired(t *testing.T) {
	t.Parallel()

	now := time.Now()
	tests := []struct {
		name string
		at   time.Time
		want bool
	}{
		{name: "just raised", at: now, want: false},
		{name: "an hour old", at: now.Add(-time.Hour), want: false},
		{name: "a second short of the lifetime", at: now.Add(-helpRequestTTL + time.Second), want: false},
		{name: "exactly the lifetime", at: now.Add(-helpRequestTTL), want: true},
		{name: "past the lifetime", at: now.Add(-helpRequestTTL - time.Minute), want: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, HelpRequest{At: tt.at}.expired(now))
		})
	}
}

func TestHelpRequestBelongsTo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		student string
		email   string
		want    bool
	}{
		{name: "same email", student: "alice@example.com", email: "alice@example.com", want: true},
		{name: "email case is ignored", student: "Alice@Example.com", email: "alice@example.com", want: true},
		{name: "recorded as a username", student: "alice", email: "alice@example.com", want: true},
		{name: "username from a dotted email", student: "alice-smith", email: "Alice.Smith@example.com", want: true},
		{name: "another student", student: "bob@example.com", email: "alice@example.com", want: false},
		{name: "another student's username", student: "bob", email: "alice@example.com", want: false},
		{name: "request with no student", student: "", email: "alice@example.com", want: false},
		{name: "reader with no identity", student: "alice@example.com", email: "", want: false},
		{name: "neither", student: "", email: "", want: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, helpRequestBelongsTo(HelpRequest{Student: tt.student}, tt.email))
		})
	}
}

// Characters, not bytes: slicing bytes would cut an accented character in half.
func TestTruncateHelpMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     string
		wantRunes int
		unchanged bool
	}{
		{name: "empty", input: "", wantRunes: 0, unchanged: true},
		{name: "short", input: "my pod will not start", wantRunes: 21, unchanged: true},
		{name: "short with accents", input: "Ça bloque à l'étape 3 ☕", wantRunes: 23, unchanged: true},
		{name: "exactly the limit", input: strings.Repeat("x", maxHelpMessageLength), wantRunes: maxHelpMessageLength, unchanged: true},
		{name: "one over, ascii", input: strings.Repeat("x", maxHelpMessageLength+1), wantRunes: maxHelpMessageLength},
		{name: "far over, ascii", input: strings.Repeat("x", maxHelpMessageLength+500), wantRunes: maxHelpMessageLength},
		{name: "exactly the limit in multi-byte runes", input: strings.Repeat("é", maxHelpMessageLength), wantRunes: maxHelpMessageLength, unchanged: true},
		{name: "over the limit in multi-byte runes", input: strings.Repeat("é", maxHelpMessageLength+20), wantRunes: maxHelpMessageLength},
		{name: "over the limit in 4-byte runes", input: strings.Repeat("🙋", maxHelpMessageLength+5), wantRunes: maxHelpMessageLength},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := truncateHelpMessage(tt.input)
			assert.Equal(t, tt.wantRunes, utf8.RuneCountInString(got))
			assert.True(t, utf8.ValidString(got), "no character may be cut in half")
			if tt.unchanged {
				assert.Equal(t, tt.input, got)
			}
		})
	}
}

func TestNewHelpRequestID(t *testing.T) {
	t.Parallel()

	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		id, err := newHelpRequestID()
		require.NoError(t, err)
		require.NotEmpty(t, id)
		assert.False(t, seen[id], "ids must not repeat")
		seen[id] = true
	}
}

func TestHumanWaited(t *testing.T) {
	t.Parallel()

	now := time.Now()
	tests := []struct {
		name   string
		waited time.Duration
		want   string
	}{
		{name: "no time at all", waited: 0, want: "just now"},
		{name: "seconds", waited: 30 * time.Second, want: "just now"},
		{name: "a second short of a minute", waited: 59 * time.Second, want: "just now"},
		{name: "a clock slightly ahead", waited: -5 * time.Second, want: "just now"},
		{name: "exactly one minute", waited: time.Minute, want: "1 minute"},
		{name: "one minute and change", waited: 90 * time.Second, want: "1 minute"},
		{name: "two minutes", waited: 2 * time.Minute, want: "2 minutes"},
		{name: "many minutes", waited: 42 * time.Minute, want: "42 minutes"},
		{name: "the last minute before an hour", waited: 59*time.Minute + 59*time.Second, want: "59 minutes"},
		{name: "exactly one hour", waited: time.Hour, want: "1 hour"},
		{name: "one hour and a minute", waited: time.Hour + time.Minute, want: "1 hour 1 min"},
		{name: "one hour and a half", waited: 90 * time.Minute, want: "1 hour 30 min"},
		{name: "exactly two hours", waited: 2 * time.Hour, want: "2 hours"},
		{name: "hours and minutes", waited: 3*time.Hour + 15*time.Minute, want: "3 hours 15 min"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, humanWaited(now.Add(-tt.waited), now))
		})
	}
}

func TestPruneHelpRequests(t *testing.T) {
	t.Parallel()

	now := time.Now()
	entries := []HelpRequest{
		testHelpRequest("old", "a", helpRequestTTL+time.Minute),
		testHelpRequest("first", "b", time.Hour),
		{ID: "edge", Student: "c", At: now.Add(-helpRequestTTL)},
		testHelpRequest("second", "d", time.Minute),
	}

	got := pruneHelpRequests(entries, now)
	require.Len(t, got, 2)
	assert.Equal(t, "first", got[0].ID, "the order is kept")
	assert.Equal(t, "second", got[1].ID)

	t.Run("nothing to keep is empty, not nil", func(t *testing.T) {
		t.Parallel()
		got := pruneHelpRequests([]HelpRequest{testHelpRequest("old", "a", 2*helpRequestTTL)}, now)
		assert.NotNil(t, got)
		assert.Empty(t, got)
	})

	t.Run("nil input", func(t *testing.T) {
		t.Parallel()
		assert.Empty(t, pruneHelpRequests(nil, now))
	})
}

func TestUpsertHelpRequest(t *testing.T) {
	t.Parallel()

	now := time.Now()
	acked := now.Add(-time.Minute)

	t.Run("a new student gets a waiting entry", func(t *testing.T) {
		t.Parallel()
		hr := testHelpRequest("r1", "alice@example.com", 0)
		// Whatever the caller claims about state, a new entry starts waiting.
		hr.Status = HelpStatusHandling
		hr.AcknowledgedBy = "admin"
		hr.Cancelled = true

		got, stored, changed := upsertHelpRequest(nil, hr, now)
		require.Len(t, got, 1)
		assert.True(t, changed)
		assert.Equal(t, HelpStatusWaiting, stored.Status)
		assert.Empty(t, stored.AcknowledgedBy)
		assert.Nil(t, stored.AcknowledgedAt)
		assert.False(t, stored.Cancelled)
	})

	t.Run("asking again refreshes, keeping ID status and acknowledgement", func(t *testing.T) {
		t.Parallel()
		existing := testHelpRequest("r1", "alice@example.com", 10*time.Minute)
		existing.Status = HelpStatusHandling
		existing.AcknowledgedAt = &acked
		existing.AcknowledgedBy = "teacher@example.com"

		again := testHelpRequest("fresh-id", "alice@example.com", 0)
		again.Message = "still stuck"

		got, stored, changed := upsertHelpRequest([]HelpRequest{existing}, again, now)
		require.Len(t, got, 1, "one open request per student")
		assert.True(t, changed)
		assert.Equal(t, "r1", stored.ID)
		assert.Equal(t, "still stuck", stored.Message)
		assert.True(t, stored.At.Equal(again.At))
		assert.Equal(t, HelpStatusHandling, stored.Status)
		require.NotNil(t, stored.AcknowledgedAt)
		assert.True(t, stored.AcknowledgedAt.Equal(acked))
		assert.Equal(t, "teacher@example.com", stored.AcknowledgedBy)
	})

	t.Run("the ID is matched before the student", func(t *testing.T) {
		t.Parallel()
		entries := []HelpRequest{
			testHelpRequest("r1", "alice@example.com", 20*time.Minute),
			testHelpRequest("r2", "bob@example.com", 10*time.Minute),
		}
		incoming := testHelpRequest("r2", "alice@example.com", 0)
		incoming.Message = "by id"

		got, stored, _ := upsertHelpRequest(entries, incoming, now)
		require.Len(t, got, 2)
		assert.Equal(t, "r2", stored.ID)
		assert.Equal(t, "bob@example.com", stored.Student)
		assert.Equal(t, "by id", got[1].Message)
		assert.Equal(t, "stuck on r1", got[0].Message)
	})

	t.Run("another student is a second entry", func(t *testing.T) {
		t.Parallel()
		entries := []HelpRequest{testHelpRequest("r1", "alice@example.com", time.Minute)}
		got, _, changed := upsertHelpRequest(entries, testHelpRequest("r2", "bob@example.com", 0), now)
		assert.Len(t, got, 2)
		assert.True(t, changed)
	})

	t.Run("an identical record changes nothing", func(t *testing.T) {
		t.Parallel()
		hr := testHelpRequest("r1", "alice@example.com", time.Minute)
		first, _, _ := upsertHelpRequest(nil, hr, now)
		got, _, changed := upsertHelpRequest(first, hr, now)
		assert.Len(t, got, 1)
		assert.False(t, changed)
	})

	t.Run("a moved timestamp or message is a change", func(t *testing.T) {
		t.Parallel()
		hr := testHelpRequest("r1", "alice@example.com", time.Minute)
		first, _, _ := upsertHelpRequest(nil, hr, now)

		later := hr
		later.At = hr.At.Add(time.Second)
		_, _, changed := upsertHelpRequest(first, later, now)
		assert.True(t, changed, "timestamp")

		reworded := hr
		reworded.Message = "a different note"
		_, _, changed = upsertHelpRequest(first, reworded, now)
		assert.True(t, changed, "message")
	})

	t.Run("a workspace once found is never cleared", func(t *testing.T) {
		t.Parallel()
		hr := testHelpRequest("r1", "alice@example.com", time.Minute)
		hr.WorkspaceName = "ws-alice"
		hr.StudentEmail = "alice@example.com"
		first, _, _ := upsertHelpRequest(nil, hr, now)

		blind := hr
		blind.WorkspaceName = ""
		blind.StudentEmail = ""
		got, stored, changed := upsertHelpRequest(first, blind, now)
		require.Len(t, got, 1)
		assert.False(t, changed)
		assert.Equal(t, "ws-alice", stored.WorkspaceName)
		assert.Equal(t, "alice@example.com", stored.StudentEmail)
	})

	t.Run("a workspace found later is filled in", func(t *testing.T) {
		t.Parallel()
		hr := testHelpRequest("r1", "alice@example.com", time.Minute)
		first, _, _ := upsertHelpRequest(nil, hr, now)

		found := hr
		found.WorkspaceName = "ws-alice"
		_, stored, changed := upsertHelpRequest(first, found, now)
		assert.True(t, changed)
		assert.Equal(t, "ws-alice", stored.WorkspaceName)
	})

	t.Run("expired entries are dropped on write", func(t *testing.T) {
		t.Parallel()
		entries := []HelpRequest{testHelpRequest("old", "carol@example.com", 2*helpRequestTTL)}
		got, _, _ := upsertHelpRequest(entries, testHelpRequest("r1", "alice@example.com", 0), now)
		require.Len(t, got, 1)
		assert.Equal(t, "r1", got[0].ID)
	})

	t.Run("an expired request of the same student does not absorb a new one", func(t *testing.T) {
		t.Parallel()
		old := testHelpRequest("old", "alice@example.com", 2*helpRequestTTL)
		old.Status = HelpStatusHandling
		old.AcknowledgedBy = "teacher@example.com"

		got, stored, _ := upsertHelpRequest([]HelpRequest{old}, testHelpRequest("new", "alice@example.com", 0), now)
		require.Len(t, got, 1)
		assert.Equal(t, "new", stored.ID)
		assert.Equal(t, HelpStatusWaiting, stored.Status)
		assert.Empty(t, stored.AcknowledgedBy)
	})

	t.Run("past the cap the oldest go", func(t *testing.T) {
		t.Parallel()
		var entries []HelpRequest
		for i := 0; i < maxLabHelpRequests+5; i++ {
			// Oldest first, as the list is stored.
			hr := testHelpRequest(fmt.Sprintf("r%d", i), fmt.Sprintf("student-%d", i), time.Hour-time.Duration(i)*time.Second)
			entries, _, _ = upsertHelpRequest(entries, hr, now)
		}
		require.Len(t, entries, maxLabHelpRequests)
		assert.Equal(t, "r5", entries[0].ID, "the five oldest were dropped")
		assert.Equal(t, fmt.Sprintf("r%d", maxLabHelpRequests+4), entries[len(entries)-1].ID)
	})
}

func TestRaiseHelpRequest(t *testing.T) {
	t.Parallel()

	jm := NewJobManager("")
	labID := jm.CreateJob(&LabConfig{StackName: "workshop"})

	first := testHelpRequest("r1", "alice@example.com", time.Minute)
	stored, err := jm.RaiseHelpRequest(labID, first)
	require.NoError(t, err)
	assert.Equal(t, "r1", stored.ID)
	assert.Equal(t, HelpStatusWaiting, stored.Status)

	again := testHelpRequest("r2", "alice@example.com", 0)
	again.Message = "second attempt"
	stored, err = jm.RaiseHelpRequest(labID, again)
	require.NoError(t, err)
	assert.Equal(t, "r1", stored.ID, "the open request is refreshed, not duplicated")
	assert.Equal(t, "second attempt", stored.Message)
	assert.Len(t, storedHelpRequests(t, jm, labID), 1)

	_, err = jm.RaiseHelpRequest("job-missing", first)
	require.Error(t, err)
}

// The outbox is delivered at least once: importing a record already applied
// changes nothing, so the lab is not saved and the audit is not repeated.
func TestImportHelpRequest(t *testing.T) {
	t.Parallel()

	jm := NewJobManager("")
	labID := jm.CreateJob(&LabConfig{StackName: "workshop"})
	hr := testHelpRequest("r1", "alice@example.com", time.Minute)

	changed, err := jm.ImportHelpRequest(labID, hr)
	require.NoError(t, err)
	assert.True(t, changed)

	changed, err = jm.ImportHelpRequest(labID, hr)
	require.NoError(t, err)
	assert.False(t, changed, "the same record is not applied twice")

	moved := hr
	moved.At = hr.At.Add(30 * time.Second)
	changed, err = jm.ImportHelpRequest(labID, moved)
	require.NoError(t, err)
	assert.True(t, changed, "a later ask moves the timestamp")

	reworded := moved
	reworded.Message = "something else"
	changed, err = jm.ImportHelpRequest(labID, reworded)
	require.NoError(t, err)
	assert.True(t, changed, "a reworded ask changes the message")

	require.Len(t, storedHelpRequests(t, jm, labID), 1)

	_, err = jm.ImportHelpRequest("job-missing", hr)
	require.Error(t, err)
}

func TestImportHelpRequest_UnchangedLeavesJobUntouched(t *testing.T) {
	t.Parallel()

	jm := NewJobManager("")
	labID := jm.CreateJob(&LabConfig{StackName: "workshop"})
	hr := testHelpRequest("r1", "alice@example.com", time.Minute)
	_, err := jm.ImportHelpRequest(labID, hr)
	require.NoError(t, err)

	job, ok := jm.GetJob(labID)
	require.True(t, ok)
	job.mu.RLock()
	before := job.UpdatedAt
	job.mu.RUnlock()

	changed, err := jm.ImportHelpRequest(labID, hr)
	require.NoError(t, err)
	require.False(t, changed)

	job.mu.RLock()
	defer job.mu.RUnlock()
	assert.True(t, before.Equal(job.UpdatedAt), "an idempotent import must not look like a change")
}

func TestMergeLocalHelpRequests(t *testing.T) {
	t.Parallel()

	t.Run("adds what the synced state does not carry", func(t *testing.T) {
		t.Parallel()
		jm := NewJobManager("")
		labID := jm.CreateJob(&LabConfig{StackName: "workshop"})
		_, err := jm.RaiseHelpRequest(labID, testHelpRequest("synced", "bob@example.com", time.Minute))
		require.NoError(t, err)

		jm.MergeLocalHelpRequests(labID, []HelpRequest{testHelpRequest("local", "alice@example.com", 0)})

		stored := storedHelpRequests(t, jm, labID)
		require.Len(t, stored, 2)
		assert.Equal(t, "synced", stored[0].ID)
		assert.Equal(t, "local", stored[1].ID)
	})

	t.Run("the synced copy wins over a local one with the same ID", func(t *testing.T) {
		t.Parallel()
		jm := NewJobManager("")
		labID := jm.CreateJob(&LabConfig{StackName: "workshop"})
		synced := testHelpRequest("r1", "alice@example.com", time.Minute)
		synced.Status = HelpStatusHandling
		synced.AcknowledgedBy = "teacher@example.com"
		job, _ := jm.GetJob(labID)
		job.mu.Lock()
		job.HelpRequests = []HelpRequest{synced}
		job.mu.Unlock()

		local := testHelpRequest("r1", "alice@example.com", 0)
		local.Message = "local wording"
		jm.MergeLocalHelpRequests(labID, []HelpRequest{local})

		stored := storedHelpRequests(t, jm, labID)
		require.Len(t, stored, 1)
		assert.Equal(t, HelpStatusHandling, stored[0].Status)
		assert.Equal(t, "stuck on r1", stored[0].Message)
	})

	t.Run("an expired local request is not added", func(t *testing.T) {
		t.Parallel()
		jm := NewJobManager("")
		labID := jm.CreateJob(&LabConfig{StackName: "workshop"})
		jm.MergeLocalHelpRequests(labID, []HelpRequest{testHelpRequest("old", "alice@example.com", 2*helpRequestTTL)})
		assert.Empty(t, storedHelpRequests(t, jm, labID))
	})

	t.Run("nothing to merge and an unknown lab are no-ops", func(t *testing.T) {
		t.Parallel()
		jm := NewJobManager("")
		labID := jm.CreateJob(&LabConfig{StackName: "workshop"})
		jm.MergeLocalHelpRequests(labID, nil)
		jm.MergeLocalHelpRequests("job-missing", []HelpRequest{testHelpRequest("r1", "a", 0)})
		assert.Empty(t, storedHelpRequests(t, jm, labID))
	})
}

func TestCancelHelpRequest(t *testing.T) {
	t.Parallel()

	setup := func(t *testing.T) (*JobManager, string) {
		t.Helper()
		jm := NewJobManager("")
		labID := jm.CreateJob(&LabConfig{StackName: "workshop"})
		_, err := jm.RaiseHelpRequest(labID, testHelpRequest("r1", "alice@example.com", time.Minute))
		require.NoError(t, err)
		_, err = jm.RaiseHelpRequest(labID, testHelpRequest("r2", "bob@example.com", time.Minute))
		require.NoError(t, err)
		return jm, labID
	}

	t.Run("withdraws only that student's request", func(t *testing.T) {
		t.Parallel()
		jm, labID := setup(t)

		removed, found, err := jm.CancelHelpRequest(labID, "alice@example.com")
		require.NoError(t, err)
		assert.True(t, found)
		assert.Equal(t, "r1", removed.ID)

		stored := storedHelpRequests(t, jm, labID)
		require.Len(t, stored, 1)
		assert.Equal(t, "r2", stored[0].ID)
	})

	t.Run("nothing open is not an error", func(t *testing.T) {
		t.Parallel()
		jm, labID := setup(t)
		_, found, err := jm.CancelHelpRequest(labID, "carol@example.com")
		require.NoError(t, err)
		assert.False(t, found)
		assert.Len(t, storedHelpRequests(t, jm, labID), 2)
	})

	t.Run("drops expired entries on the way", func(t *testing.T) {
		t.Parallel()
		jm, labID := setup(t)
		job, _ := jm.GetJob(labID)
		job.mu.Lock()
		job.HelpRequests = append(job.HelpRequests, testHelpRequest("old", "dave@example.com", 2*helpRequestTTL))
		job.mu.Unlock()

		_, _, err := jm.CancelHelpRequest(labID, "alice@example.com")
		require.NoError(t, err)
		assert.Len(t, storedHelpRequests(t, jm, labID), 1)
	})

	t.Run("unknown lab", func(t *testing.T) {
		t.Parallel()
		jm, _ := setup(t)
		_, _, err := jm.CancelHelpRequest("job-missing", "alice@example.com")
		require.Error(t, err)
	})
}

func TestResolveHelpRequest(t *testing.T) {
	t.Parallel()

	jm := NewJobManager("")
	labID := jm.CreateJob(&LabConfig{StackName: "workshop"})
	_, err := jm.RaiseHelpRequest(labID, testHelpRequest("r1", "alice@example.com", time.Minute))
	require.NoError(t, err)
	_, err = jm.RaiseHelpRequest(labID, testHelpRequest("r2", "bob@example.com", time.Minute))
	require.NoError(t, err)

	removed, found, err := jm.ResolveHelpRequest(labID, "r1")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "alice@example.com", removed.Student)

	// Resolving removes the entry: there is no "resolved" state left to serve.
	stored := storedHelpRequests(t, jm, labID)
	require.Len(t, stored, 1)
	assert.Equal(t, "r2", stored[0].ID)
	_, open := jm.StudentHelpRequest(labID, "alice@example.com")
	assert.False(t, open)

	_, found, err = jm.ResolveHelpRequest(labID, "r1")
	require.NoError(t, err)
	assert.False(t, found, "already gone")

	_, _, err = jm.ResolveHelpRequest("job-missing", "r1")
	require.Error(t, err)
}

func TestAckHelpRequest(t *testing.T) {
	t.Parallel()

	jm := NewJobManager("")
	labID := jm.CreateJob(&LabConfig{StackName: "workshop"})
	_, err := jm.RaiseHelpRequest(labID, testHelpRequest("r1", "alice@example.com", time.Minute))
	require.NoError(t, err)

	hr, found, err := jm.AckHelpRequest(labID, "r1", "first@example.com")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, HelpStatusHandling, hr.Status)
	assert.Equal(t, "first@example.com", hr.AcknowledgedBy)
	require.NotNil(t, hr.AcknowledgedAt)
	firstAt := *hr.AcknowledgedAt

	// The first admin to take it keeps it.
	hr, found, err = jm.AckHelpRequest(labID, "r1", "second@example.com")
	require.NoError(t, err)
	require.True(t, found, "it is still there, just not re-acknowledged")
	assert.Equal(t, "first@example.com", hr.AcknowledgedBy)
	require.NotNil(t, hr.AcknowledgedAt)
	assert.True(t, firstAt.Equal(*hr.AcknowledgedAt))

	stored := storedHelpRequests(t, jm, labID)
	require.Len(t, stored, 1)
	assert.Equal(t, "first@example.com", stored[0].AcknowledgedBy)

	_, found, err = jm.AckHelpRequest(labID, "nope", "first@example.com")
	require.NoError(t, err)
	assert.False(t, found)

	_, _, err = jm.AckHelpRequest("job-missing", "r1", "first@example.com")
	require.Error(t, err)
}

func TestAckHelpRequest_ExpiredIsGone(t *testing.T) {
	t.Parallel()

	jm := NewJobManager("")
	labID := jm.CreateJob(&LabConfig{StackName: "workshop"})
	job, _ := jm.GetJob(labID)
	job.mu.Lock()
	job.HelpRequests = []HelpRequest{testHelpRequest("old", "alice@example.com", 2*helpRequestTTL)}
	job.mu.Unlock()

	_, found, err := jm.AckHelpRequest(labID, "old", "admin")
	require.NoError(t, err)
	assert.False(t, found)
	assert.Empty(t, storedHelpRequests(t, jm, labID))
}

func TestAckHelpRequestForWorkspace(t *testing.T) {
	t.Parallel()

	setup := func(t *testing.T) (*JobManager, string) {
		t.Helper()
		jm := NewJobManager("")
		labID := jm.CreateJob(&LabConfig{StackName: "workshop"})
		with := testHelpRequest("r1", "alice@example.com", time.Minute)
		with.WorkspaceName = "ws-alice"
		without := testHelpRequest("r2", "bob@example.com", time.Minute)
		_, err := jm.RaiseHelpRequest(labID, with)
		require.NoError(t, err)
		_, err = jm.RaiseHelpRequest(labID, without)
		require.NoError(t, err)
		return jm, labID
	}

	t.Run("opening a workspace marks its request as being dealt with", func(t *testing.T) {
		t.Parallel()
		jm, labID := setup(t)

		hr, ok := jm.AckHelpRequestForWorkspace(labID, "ws-alice", "teacher@example.com")
		require.True(t, ok)
		assert.Equal(t, "r1", hr.ID)
		assert.Equal(t, HelpStatusHandling, hr.Status)
		assert.Equal(t, "teacher@example.com", hr.AcknowledgedBy)

		stored := storedHelpRequests(t, jm, labID)
		assert.Equal(t, HelpStatusHandling, stored[0].Status)
		assert.Equal(t, HelpStatusWaiting, stored[1].Status, "other students are untouched")
	})

	t.Run("an already handled request is not taken over", func(t *testing.T) {
		t.Parallel()
		jm, labID := setup(t)
		_, ok := jm.AckHelpRequestForWorkspace(labID, "ws-alice", "first@example.com")
		require.True(t, ok)

		_, ok = jm.AckHelpRequestForWorkspace(labID, "ws-alice", "second@example.com")
		assert.False(t, ok)
		assert.Equal(t, "first@example.com", storedHelpRequests(t, jm, labID)[0].AcknowledgedBy)
	})

	tests := []struct {
		name      string
		lab       string
		workspace string
	}{
		{name: "a workspace with no request", lab: "", workspace: "ws-carol"},
		{name: "no workspace name", lab: "", workspace: ""},
		{name: "an unknown lab", lab: "job-missing", workspace: "ws-alice"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name+" is a no-op", func(t *testing.T) {
			t.Parallel()
			jm, labID := setup(t)
			if tt.lab != "" {
				labID = tt.lab
			}
			_, ok := jm.AckHelpRequestForWorkspace(labID, tt.workspace, "teacher@example.com")
			assert.False(t, ok)
		})
	}
}

func TestLabHelpRequestsAndStudentHelpRequest(t *testing.T) {
	t.Parallel()

	jm := NewJobManager("")
	labID := jm.CreateJob(&LabConfig{StackName: "workshop"})
	job, _ := jm.GetJob(labID)
	// Stored out of order, with one that has outlived its lifetime.
	job.mu.Lock()
	job.HelpRequests = []HelpRequest{
		testHelpRequest("recent", "carol@example.com", time.Minute),
		testHelpRequest("longest", "alice@example.com", time.Hour),
		testHelpRequest("old", "dave@example.com", 2*helpRequestTTL),
		testHelpRequest("middle", "bob", 10*time.Minute),
	}
	job.mu.Unlock()

	t.Run("longest wait first, expired ones filtered on read", func(t *testing.T) {
		t.Parallel()
		got := jm.LabHelpRequests(labID)
		require.Len(t, got, 3)
		assert.Equal(t, []string{"longest", "middle", "recent"}, []string{got[0].ID, got[1].ID, got[2].ID})
	})

	t.Run("unknown lab", func(t *testing.T) {
		t.Parallel()
		assert.Empty(t, jm.LabHelpRequests("job-missing"))
	})

	tests := []struct {
		name   string
		email  string
		wantID string
	}{
		{name: "by email", email: "alice@example.com", wantID: "longest"},
		{name: "by username when recorded that way", email: "bob@example.com", wantID: "middle"},
		{name: "an expired request is not served", email: "dave@example.com"},
		{name: "a student with none", email: "erin@example.com"},
		{name: "no identity", email: ""},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := jm.StudentHelpRequest(labID, tt.email)
			assert.Equal(t, tt.wantID != "", ok)
			assert.Equal(t, tt.wantID, got.ID)
		})
	}

	t.Run("StudentHelpRequest on an unknown lab", func(t *testing.T) {
		t.Parallel()
		_, ok := jm.StudentHelpRequest("job-missing", "alice@example.com")
		assert.False(t, ok)
	})
}

// ---------------------------------------------------------------------------
// The student's endpoints
// ---------------------------------------------------------------------------

func raiseHelpAs(h *Handler, email string, form url.Values) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.StudentHelp(rec, asStudent(formRequest(http.MethodPost, "/api/student/help", form), email))
	return rec
}

func studentHelpState(t *testing.T, h *Handler, email string) []studentHelpResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/student/help", nil)
	if email != "" {
		req = asStudent(req, email)
	}
	rec := httptest.NewRecorder()
	h.StudentHelp(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var payload struct {
		Requests []studentHelpResponse `json:"requests"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	return payload.Requests
}

func TestStudentHelp_Raise(t *testing.T) {
	t.Parallel()

	// No workspace history at all: the student whose workspace never came up must
	// still be able to ask — which is why the lab gate is not studentInLab.
	h, labID := newNotifyTestHandler(t)
	as, err := NewAuditStore(t.TempDir())
	require.NoError(t, err)
	h.SetAuditStore(as)

	rec := raiseHelpAs(h, "Alice@Example.com", url.Values{"lab_id": {labID}, "message": {"  my pod will not start  "}})
	require.Equal(t, http.StatusOK, rec.Code)

	var body struct {
		Success bool                `json:"success"`
		Request studentHelpResponse `json:"request"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.True(t, body.Success)
	assert.Equal(t, labID, body.Request.LabID)
	assert.Equal(t, "workshop", body.Request.LabName)
	assert.Equal(t, HelpStatusWaiting, body.Request.Status)
	assert.Equal(t, "my pod will not start", body.Request.Message, "the note is trimmed")

	stored := storedHelpRequests(t, h.jobManager, labID)
	require.Len(t, stored, 1)
	assert.Equal(t, "alice@example.com", stored[0].Student, "the identity is normalized")
	assert.Equal(t, "Alice@Example.com", stored[0].StudentEmail, "the admin sees the email as typed")
	assert.Equal(t, HelpSourcePortal, stored[0].Source)
	assert.NotEmpty(t, stored[0].ID)
	assert.Empty(t, stored[0].WorkspaceName, "a lab with no cluster has no workspace to name")

	// The audit entry says that it happened, never what was said.
	entries := waitForAudit(t, as, 1)
	require.Len(t, entries, 1)
	assert.Equal(t, "lab.help_request", entries[0].Action)
	assert.Equal(t, "student", entries[0].Role)
	assert.Equal(t, labID, entries[0].LabID)
	assert.NotContains(t, entries[0].Detail, "pod")
}

func TestStudentHelp_IdentityComesFromTheSession(t *testing.T) {
	t.Parallel()

	h, labID := newNotifyTestHandler(t)

	// A client claiming to be somebody else, in every field it could try.
	rec := raiseHelpAs(h, "alice@example.com", url.Values{
		"lab_id":          {labID},
		"message":         {"help"},
		"student":         {"bob@example.com"},
		"email":           {"bob@example.com"},
		"student_email":   {"bob@example.com"},
		"status":          {HelpStatusHandling},
		"acknowledged_by": {"teacher@example.com"},
		"id":              {"chosen-by-the-client"},
	})
	require.Equal(t, http.StatusOK, rec.Code)

	stored := storedHelpRequests(t, h.jobManager, labID)
	require.Len(t, stored, 1)
	assert.Equal(t, "alice@example.com", stored[0].Student)
	assert.Equal(t, "alice@example.com", stored[0].StudentEmail)
	assert.Equal(t, HelpStatusWaiting, stored[0].Status)
	assert.Empty(t, stored[0].AcknowledgedBy)
	assert.NotEqual(t, "chosen-by-the-client", stored[0].ID)

	assert.Empty(t, studentHelpState(t, h, "bob@example.com"))
}

func TestStudentHelp_Rejects(t *testing.T) {
	t.Parallel()

	h, labID := newNotifyTestHandler(t)

	tests := []struct {
		name       string
		method     string
		email      string
		form       url.Values
		wantStatus int
	}{
		{name: "no session identity", method: http.MethodPost, email: "", form: url.Values{"lab_id": {labID}}, wantStatus: http.StatusUnauthorized},
		{name: "no lab", method: http.MethodPost, email: "alice@example.com", form: url.Values{"message": {"help"}}, wantStatus: http.StatusBadRequest},
		{name: "blank lab", method: http.MethodPost, email: "alice@example.com", form: url.Values{"lab_id": {"   "}}, wantStatus: http.StatusBadRequest},
		{name: "unknown lab", method: http.MethodPost, email: "alice@example.com", form: url.Values{"lab_id": {"job-missing"}}, wantStatus: http.StatusNotFound},
		{name: "unsupported method", method: http.MethodDelete, email: "alice@example.com", form: nil, wantStatus: http.StatusMethodNotAllowed},
		{name: "PUT is no better", method: http.MethodPut, email: "alice@example.com", form: nil, wantStatus: http.StatusMethodNotAllowed},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := formRequest(tt.method, "/api/student/help", tt.form)
			if tt.email != "" {
				req = asStudent(req, tt.email)
			}
			rec := httptest.NewRecorder()
			h.StudentHelp(rec, req)
			assert.Equal(t, tt.wantStatus, rec.Code)
		})
	}

	assert.Empty(t, storedHelpRequests(t, h.jobManager, labID), "nothing is stored for a rejected request")
}

func TestStudentHelp_TruncatesTheNote(t *testing.T) {
	t.Parallel()

	h, labID := newNotifyTestHandler(t)
	rec := raiseHelpAs(h, "alice@example.com", url.Values{
		"lab_id":  {labID},
		"message": {strings.Repeat("é", maxHelpMessageLength+40)},
	})
	require.Equal(t, http.StatusOK, rec.Code)

	stored := storedHelpRequests(t, h.jobManager, labID)
	require.Len(t, stored, 1)
	assert.Equal(t, maxHelpMessageLength, utf8.RuneCountInString(stored[0].Message))
	assert.True(t, utf8.ValidString(stored[0].Message))
}

// One open request per student per lab: asking again refreshes the note and the
// time but not the request the admin may already have picked up.
func TestStudentHelp_AskingAgainKeepsTheRequest(t *testing.T) {
	t.Parallel()

	h, labID := newNotifyTestHandler(t)
	require.Equal(t, http.StatusOK, raiseHelpAs(h, "alice@example.com", url.Values{"lab_id": {labID}, "message": {"first"}}).Code)

	first := storedHelpRequests(t, h.jobManager, labID)
	require.Len(t, first, 1)
	_, found, err := h.jobManager.AckHelpRequest(labID, first[0].ID, "teacher@example.com")
	require.NoError(t, err)
	require.True(t, found)

	time.Sleep(2 * time.Millisecond)
	require.Equal(t, http.StatusOK, raiseHelpAs(h, "alice@example.com", url.Values{"lab_id": {labID}, "message": {"second"}}).Code)

	again := storedHelpRequests(t, h.jobManager, labID)
	require.Len(t, again, 1, "no second row")
	assert.Equal(t, first[0].ID, again[0].ID)
	assert.Equal(t, "second", again[0].Message)
	assert.True(t, again[0].At.After(first[0].At))
	assert.Equal(t, HelpStatusHandling, again[0].Status, "the admin is still on their way")
	assert.Equal(t, "teacher@example.com", again[0].AcknowledgedBy)
	assert.NotNil(t, again[0].AcknowledgedAt)

	// Another student in the same lab is a row of their own.
	require.Equal(t, http.StatusOK, raiseHelpAs(h, "bob@example.com", url.Values{"lab_id": {labID}}).Code)
	assert.Len(t, storedHelpRequests(t, h.jobManager, labID), 2)
}

func TestStudentHelp_StateIsPerStudent(t *testing.T) {
	t.Parallel()

	h, labID := newNotifyTestHandler(t)
	require.Equal(t, http.StatusOK, raiseHelpAs(h, "alice@example.com", url.Values{"lab_id": {labID}, "message": {"alice's secret note"}}).Code)
	_, _, err := h.jobManager.AckHelpRequest(labID, storedHelpRequests(t, h.jobManager, labID)[0].ID, "teacher@example.com")
	require.NoError(t, err)

	alice := studentHelpState(t, h, "alice@example.com")
	require.Len(t, alice, 1)
	assert.Equal(t, labID, alice[0].LabID)
	assert.Equal(t, HelpStatusHandling, alice[0].Status)
	assert.Equal(t, "alice's secret note", alice[0].Message)

	// Bob sees nothing of Alice's, and an unauthenticated read sees nothing at all.
	assert.Empty(t, studentHelpState(t, h, "bob@example.com"))
	assert.Empty(t, studentHelpState(t, h, ""))

	// The student is told someone is coming, not who.
	rec := httptest.NewRecorder()
	h.StudentHelp(rec, asStudent(httptest.NewRequest(http.MethodGet, "/api/student/help", nil), "alice@example.com"))
	assert.NotContains(t, rec.Body.String(), "teacher@example.com")
	assert.NotContains(t, rec.Body.String(), "acknowledged")
}

func TestStudentHelp_StateIsAnEmptyListNotNull(t *testing.T) {
	t.Parallel()

	h, _ := newNotifyTestHandler(t)
	rec := httptest.NewRecorder()
	h.StudentHelp(rec, asStudent(httptest.NewRequest(http.MethodGet, "/api/student/help", nil), "alice@example.com"))
	assert.JSONEq(t, `{"requests":[]}`, rec.Body.String())
}

func TestCancelStudentHelp(t *testing.T) {
	t.Parallel()

	cancelAs := func(h *Handler, email string, form url.Values) *httptest.ResponseRecorder {
		req := formRequest(http.MethodPost, "/api/student/help/cancel", form)
		if email != "" {
			req = asStudent(req, email)
		}
		rec := httptest.NewRecorder()
		h.CancelStudentHelp(rec, req)
		return rec
	}

	t.Run("a student withdraws their own request", func(t *testing.T) {
		t.Parallel()
		h, labID := newNotifyTestHandler(t)
		as, err := NewAuditStore(t.TempDir())
		require.NoError(t, err)
		h.SetAuditStore(as)
		raiseHelpAs(h, "alice@example.com", url.Values{"lab_id": {labID}, "message": {"help"}})

		rec := cancelAs(h, "alice@example.com", url.Values{"lab_id": {labID}})
		require.Equal(t, http.StatusOK, rec.Code)
		assert.JSONEq(t, `{"success":true,"cancelled":true}`, rec.Body.String())
		assert.Empty(t, storedHelpRequests(t, h.jobManager, labID))

		entries := waitForAudit(t, as, 2)
		require.Len(t, entries, 2)
		actions := []string{entries[0].Action, entries[1].Action}
		assert.ElementsMatch(t, []string{"lab.help_request", "lab.help_cancel"}, actions)
		for _, e := range entries {
			assert.Equal(t, "student", e.Role)
		}
		assert.Equal(t, "student", entries[0].Role)
	})

	// The lookup is by session identity, so no ID, student or email the client
	// sends can reach another student's request.
	t.Run("a student cannot withdraw another's request", func(t *testing.T) {
		t.Parallel()
		h, labID := newNotifyTestHandler(t)
		raiseHelpAs(h, "alice@example.com", url.Values{"lab_id": {labID}, "message": {"help"}})
		aliceID := storedHelpRequests(t, h.jobManager, labID)[0].ID

		rec := cancelAs(h, "bob@example.com", url.Values{
			"lab_id":     {labID},
			"request_id": {aliceID},
			"id":         {aliceID},
			"student":    {"alice@example.com"},
			"email":      {"alice@example.com"},
		})
		require.Equal(t, http.StatusOK, rec.Code)
		assert.JSONEq(t, `{"success":true,"cancelled":false}`, rec.Body.String())

		stored := storedHelpRequests(t, h.jobManager, labID)
		require.Len(t, stored, 1)
		assert.Equal(t, aliceID, stored[0].ID)
	})

	t.Run("withdrawing with nothing open is a quiet success", func(t *testing.T) {
		t.Parallel()
		h, labID := newNotifyTestHandler(t)
		rec := cancelAs(h, "alice@example.com", url.Values{"lab_id": {labID}})
		require.Equal(t, http.StatusOK, rec.Code)
		assert.JSONEq(t, `{"success":true,"cancelled":false}`, rec.Body.String())
	})

	t.Run("an unknown lab has nothing to withdraw", func(t *testing.T) {
		t.Parallel()
		h, _ := newNotifyTestHandler(t)
		rec := cancelAs(h, "alice@example.com", url.Values{"lab_id": {"job-missing"}})
		require.Equal(t, http.StatusOK, rec.Code)
		assert.JSONEq(t, `{"success":true,"cancelled":false}`, rec.Body.String())
	})

	rejects := []struct {
		name       string
		method     string
		email      string
		form       url.Values
		wantStatus int
	}{
		{name: "no session identity", method: http.MethodPost, email: "", form: url.Values{"lab_id": {"x"}}, wantStatus: http.StatusUnauthorized},
		{name: "no lab", method: http.MethodPost, email: "alice@example.com", form: url.Values{}, wantStatus: http.StatusBadRequest},
		{name: "GET", method: http.MethodGet, email: "alice@example.com", form: nil, wantStatus: http.StatusMethodNotAllowed},
	}
	for _, tt := range rejects {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h, labID := newNotifyTestHandler(t)
			raiseHelpAs(h, "alice@example.com", url.Values{"lab_id": {labID}})

			req := formRequest(tt.method, "/api/student/help/cancel", tt.form)
			if tt.email != "" {
				req = asStudent(req, tt.email)
			}
			rec := httptest.NewRecorder()
			h.CancelStudentHelp(rec, req)
			assert.Equal(t, tt.wantStatus, rec.Code)
			assert.Len(t, storedHelpRequests(t, h.jobManager, labID), 1, "a rejected withdrawal removes nothing")
		})
	}
}

// ---------------------------------------------------------------------------
// From inside the IDE
// ---------------------------------------------------------------------------

func TestWorkspaceHelp(t *testing.T) {
	t.Parallel()

	h, labID := newNotifyTestHandler(t, "alice@example.com")
	job, ok := h.jobManager.GetJob(labID)
	require.True(t, ok)
	secret, _, err := ensureNotifySecret(job)
	require.NoError(t, err)
	as, err := NewAuditStore(t.TempDir())
	require.NoError(t, err)
	h.SetAuditStore(as)

	post := func(method, lab, token string, form url.Values) *httptest.ResponseRecorder {
		target := fmt.Sprintf("/api/workspace/help?lab_id=%s&token=%s", url.QueryEscape(lab), url.QueryEscape(token))
		rec := httptest.NewRecorder()
		h.WorkspaceHelp(rec, formRequest(method, target, form))
		return rec
	}

	t.Run("a bad token and an unknown lab answer alike", func(t *testing.T) {
		bad := post(http.MethodPost, labID, "alice@example.com.deadbeef", nil)
		unknown := post(http.MethodPost, "job-missing", notifyToken(secret, labID, "alice@example.com"), nil)

		assert.Equal(t, http.StatusForbidden, bad.Code)
		assert.Equal(t, http.StatusForbidden, unknown.Code)
		assert.Equal(t, bad.Body.String(), unknown.Body.String(),
			"an unauthenticated caller must learn nothing about which labs exist")
		assert.Contains(t, unknown.Body.String(), "Not authorized")
		assert.Empty(t, storedHelpRequests(t, h.jobManager, labID))
	})

	t.Run("another lab's token is refused", func(t *testing.T) {
		rec := post(http.MethodPost, labID, notifyToken(secret, "job-other", "alice@example.com"), nil)
		assert.Equal(t, http.StatusForbidden, rec.Code)
	})

	t.Run("missing parameters", func(t *testing.T) {
		tests := []struct{ name, lab, token string }{
			{"no lab", "", "tok"},
			{"no token", labID, ""},
			{"neither", "", ""},
		}
		for _, tt := range tests {
			rec := post(http.MethodPost, tt.lab, tt.token, nil)
			assert.Equal(t, http.StatusBadRequest, rec.Code, tt.name)
		}
	})

	t.Run("method", func(t *testing.T) {
		rec := post(http.MethodGet, labID, notifyToken(secret, labID, "alice@example.com"), nil)
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})

	t.Run("a valid token raises the request of the workspace's owner", func(t *testing.T) {
		rec := post(http.MethodPost, labID, notifyToken(secret, labID, "alice@example.com"),
			url.Values{"message": {" cannot reach the app "}, "student": {"bob@example.com"}})
		require.Equal(t, http.StatusOK, rec.Code)
		assert.JSONEq(t, `{"success":true}`, rec.Body.String())

		stored := storedHelpRequests(t, h.jobManager, labID)
		require.Len(t, stored, 1)
		assert.Equal(t, "alice@example.com", stored[0].Student, "who asked comes from the token, not the body")
		assert.Equal(t, "cannot reach the app", stored[0].Message)
		assert.Equal(t, HelpSourceIDE, stored[0].Source)
		assert.Empty(t, stored[0].StudentEmail, "an IDE has no session, so no email as typed")

		// The student's own portal view of it follows from the same record.
		mine, ok := h.jobManager.StudentHelpRequest(labID, "alice@example.com")
		require.True(t, ok)
		assert.Equal(t, stored[0].ID, mine.ID)

		entries := waitForAudit(t, as, 1)
		require.NotEmpty(t, entries)
		last := entries[len(entries)-1]
		assert.Equal(t, "lab.help_request", last.Action)
		assert.Equal(t, "student", last.Role)
		assert.NotContains(t, last.Detail, "reach")
	})

	t.Run("asking from the IDE after the portal refreshes the same request", func(t *testing.T) {
		before := storedHelpRequests(t, h.jobManager, labID)
		require.Len(t, before, 1)

		rec := post(http.MethodPost, labID, notifyToken(secret, labID, "alice@example.com"), url.Values{"message": {"again"}})
		require.Equal(t, http.StatusOK, rec.Code)

		after := storedHelpRequests(t, h.jobManager, labID)
		require.Len(t, after, 1)
		assert.Equal(t, before[0].ID, after[0].ID)
		assert.Equal(t, "again", after[0].Message)
	})

	t.Run("the note is truncated", func(t *testing.T) {
		rec := post(http.MethodPost, labID, notifyToken(secret, labID, "bob@example.com"),
			url.Values{"message": {strings.Repeat("é", maxHelpMessageLength+10)}})
		require.Equal(t, http.StatusOK, rec.Code)

		got, ok := h.jobManager.StudentHelpRequest(labID, "bob@example.com")
		require.True(t, ok)
		assert.Equal(t, maxHelpMessageLength, utf8.RuneCountInString(got.Message))
	})
}

// ---------------------------------------------------------------------------
// The admin's endpoints
// ---------------------------------------------------------------------------

func newHelpAdminHandler(t *testing.T) (*Handler, string, string) {
	t.Helper()
	h, labA := newNotifyTestHandler(t)
	labB := h.jobManager.CreateJob(&LabConfig{StackName: "devoxx"})
	return h, labA, labB
}

func TestListHelpRequests(t *testing.T) {
	t.Parallel()

	h, labA, labB := newHelpAdminHandler(t)

	// Across every lab, longest wait first.
	waiting := testHelpRequest("newest", "carol@example.com", time.Minute)
	waiting.StudentEmail = "Carol@Example.com"
	oldest := testHelpRequest("oldest", "alice", 90*time.Minute)
	oldest.WorkspaceName = "ws-alice"
	oldest.Source = HelpSourceIDE
	middle := testHelpRequest("middle", "bob@example.com", 10*time.Minute)
	for lab, hr := range map[string]HelpRequest{labA: waiting, labB: oldest} {
		_, err := h.jobManager.RaiseHelpRequest(lab, hr)
		require.NoError(t, err)
	}
	_, err := h.jobManager.RaiseHelpRequest(labA, middle)
	require.NoError(t, err)
	_, _, err = h.jobManager.AckHelpRequest(labA, "middle", "teacher@example.com")
	require.NoError(t, err)
	_, err = h.jobManager.RaiseHelpRequest(labB, testHelpRequest("expired", "dave@example.com", 2*helpRequestTTL))
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	h.ListHelpRequests(rec, httptest.NewRequest(http.MethodGet, "/api/admin/help-requests", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Type"), "application/json")

	var payload struct {
		Count    int                  `json:"count"`
		Requests []helpRequestDisplay `json:"requests"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Equal(t, 3, payload.Count, "the expired one is not counted")
	require.Len(t, payload.Requests, 3)

	assert.Equal(t, []string{"oldest", "middle", "newest"},
		[]string{payload.Requests[0].ID, payload.Requests[1].ID, payload.Requests[2].ID})

	first := payload.Requests[0]
	assert.Equal(t, labB, first.LabID)
	assert.Equal(t, "devoxx", first.LabName)
	assert.Equal(t, "alice", first.Student, "falls back to the username when there is no email")
	assert.Equal(t, "ws-alice", first.WorkspaceName)
	assert.True(t, first.CanOpen)
	assert.Equal(t, HelpSourceIDE, first.Source)
	assert.Equal(t, "1 hour 30 min", first.Waiting)

	second := payload.Requests[1]
	assert.Equal(t, HelpStatusHandling, second.Status)
	assert.Equal(t, "teacher@example.com", second.TakenBy)
	assert.False(t, second.CanOpen, "no workspace was found for them")

	third := payload.Requests[2]
	assert.Equal(t, "Carol@Example.com", third.Student, "the email as typed is preferred")
	assert.Equal(t, "1 minute", third.Waiting)
}

func TestListHelpRequests_EmptyAndMethod(t *testing.T) {
	t.Parallel()

	h, _, _ := newHelpAdminHandler(t)

	rec := httptest.NewRecorder()
	h.ListHelpRequests(rec, httptest.NewRequest(http.MethodGet, "/api/admin/help-requests", nil))
	assert.JSONEq(t, `{"count":0,"requests":[]}`, rec.Body.String())

	rec = httptest.NewRecorder()
	h.ListHelpRequests(rec, httptest.NewRequest(http.MethodPost, "/api/admin/help-requests", nil))
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

func TestServeHelpRequestsPanel(t *testing.T) {
	t.Parallel()

	panel := func(h *Handler) string {
		rec := httptest.NewRecorder()
		h.ServeHelpRequestsPanel(rec, httptest.NewRequest(http.MethodGet, "/api/admin/help-requests/panel", nil))
		require.Equal(t, http.StatusOK, rec.Code)
		return rec.Body.String()
	}

	t.Run("nobody waiting", func(t *testing.T) {
		t.Parallel()
		h, _, _ := newHelpAdminHandler(t)
		assert.Contains(t, panel(h), "Nobody is waiting")
	})

	t.Run("a waiting student with a workspace", func(t *testing.T) {
		t.Parallel()
		h, labA, _ := newHelpAdminHandler(t)
		hr := testHelpRequest("r1", "alice@example.com", 5*time.Minute)
		hr.WorkspaceName = "ws-alice"
		_, err := h.jobManager.RaiseHelpRequest(labA, hr)
		require.NoError(t, err)

		body := panel(h)
		assert.Contains(t, body, "alice@example.com")
		assert.Contains(t, body, "waiting 5 minutes")
		assert.Contains(t, body, "Open workspace")
		assert.Contains(t, body, "/api/labs/"+labA+"/workspaces/ws-alice/open")
		assert.Contains(t, body, "On my way")
		assert.Contains(t, body, "Resolve")
		assert.NotContains(t, body, "from their IDE")
		assert.NotContains(t, body, "Being dealt with")
	})

	t.Run("a request taken on loses its On my way button", func(t *testing.T) {
		t.Parallel()
		h, labA, _ := newHelpAdminHandler(t)
		hr := testHelpRequest("r1", "alice@example.com", time.Minute)
		hr.Source = HelpSourceIDE
		_, err := h.jobManager.RaiseHelpRequest(labA, hr)
		require.NoError(t, err)
		_, _, err = h.jobManager.AckHelpRequest(labA, "r1", "teacher@example.com")
		require.NoError(t, err)

		body := panel(h)
		assert.Contains(t, body, "Picked up by teacher@example.com")
		assert.Contains(t, body, "from their IDE")
		assert.NotContains(t, body, "On my way")
		assert.Contains(t, body, "Resolve")
		assert.Contains(t, body, "No workspace of theirs was found")
		assert.NotContains(t, body, "Open workspace")
	})

	t.Run("a student's words are escaped", func(t *testing.T) {
		t.Parallel()
		h, labA, _ := newHelpAdminHandler(t)
		hr := testHelpRequest("r1", "alice@example.com", time.Minute)
		hr.Message = `<script>alert("x")</script>`
		hr.StudentEmail = `<b>alice</b>@example.com`
		_, err := h.jobManager.RaiseHelpRequest(labA, hr)
		require.NoError(t, err)

		body := panel(h)
		assert.NotContains(t, body, "<script>")
		assert.NotContains(t, body, "<b>alice")
		assert.Contains(t, body, "&lt;script&gt;")
	})
}

func TestAdminHelpActions(t *testing.T) {
	t.Parallel()

	setup := func(t *testing.T) (*Handler, string, *AuditStore) {
		t.Helper()
		h, labID := newNotifyTestHandler(t)
		as, err := NewAuditStore(t.TempDir())
		require.NoError(t, err)
		h.SetAuditStore(as)
		_, err = h.jobManager.RaiseHelpRequest(labID, testHelpRequest("r1", "alice@example.com", time.Minute))
		require.NoError(t, err)
		return h, labID, as
	}

	t.Run("on my way marks it and audits it", func(t *testing.T) {
		t.Parallel()
		h, labID, as := setup(t)

		rec := httptest.NewRecorder()
		h.AckHelpRequest(rec, formRequest(http.MethodPost, "/api/admin/help-requests/ack",
			url.Values{"lab_id": {labID}, "request_id": {"r1"}}))
		require.Equal(t, http.StatusOK, rec.Code)
		assert.NotContains(t, rec.Body.String(), "On my way", "the panel that comes back no longer offers it")
		assert.Contains(t, rec.Body.String(), "Picked up by admin")

		stored := storedHelpRequests(t, h.jobManager, labID)
		require.Len(t, stored, 1)
		assert.Equal(t, HelpStatusHandling, stored[0].Status)
		assert.Equal(t, "admin", stored[0].AcknowledgedBy)

		entries := waitForAudit(t, as, 1)
		require.Len(t, entries, 1)
		assert.Equal(t, "lab.help_ack", entries[0].Action)
		assert.Equal(t, "admin", entries[0].Role)
		assert.Equal(t, labID, entries[0].LabID)
	})

	t.Run("acknowledging twice keeps the first acknowledgement", func(t *testing.T) {
		t.Parallel()
		h, labID, _ := setup(t)
		form := url.Values{"lab_id": {labID}, "request_id": {"r1"}}

		rec := httptest.NewRecorder()
		h.AckHelpRequest(rec, formRequest(http.MethodPost, "/api/admin/help-requests/ack", form))
		require.Equal(t, http.StatusOK, rec.Code)
		first := storedHelpRequests(t, h.jobManager, labID)[0]
		require.NotNil(t, first.AcknowledgedAt)

		rec = httptest.NewRecorder()
		h.AckHelpRequest(rec, formRequest(http.MethodPost, "/api/admin/help-requests/ack", form))
		require.Equal(t, http.StatusOK, rec.Code)
		second := storedHelpRequests(t, h.jobManager, labID)[0]
		require.NotNil(t, second.AcknowledgedAt)
		assert.True(t, first.AcknowledgedAt.Equal(*second.AcknowledgedAt))
	})

	t.Run("resolve removes it and audits it", func(t *testing.T) {
		t.Parallel()
		h, labID, as := setup(t)

		rec := httptest.NewRecorder()
		h.ResolveHelpRequest(rec, formRequest(http.MethodPost, "/api/admin/help-requests/resolve",
			url.Values{"lab_id": {labID}, "request_id": {"r1"}}))
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), "Nobody is waiting")
		assert.Empty(t, storedHelpRequests(t, h.jobManager, labID))

		entries := waitForAudit(t, as, 1)
		require.Len(t, entries, 1)
		assert.Equal(t, "lab.help_resolve", entries[0].Action)
		assert.Equal(t, "admin", entries[0].Role)
	})

	t.Run("resolving what is already gone is quiet", func(t *testing.T) {
		t.Parallel()
		h, labID, as := setup(t)
		rec := httptest.NewRecorder()
		h.ResolveHelpRequest(rec, formRequest(http.MethodPost, "/api/admin/help-requests/resolve",
			url.Values{"lab_id": {labID}, "request_id": {"already-gone"}}))
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Len(t, storedHelpRequests(t, h.jobManager, labID), 1)
		// The write is asynchronous, so give a (wrongly) recorded entry time to land.
		time.Sleep(100 * time.Millisecond)
		entries, err := as.Recent(0)
		require.NoError(t, err)
		assert.Empty(t, entries)
	})

	rejects := []struct {
		name       string
		method     string
		form       url.Values
		wantStatus int
	}{
		{name: "GET", method: http.MethodGet, form: nil, wantStatus: http.StatusMethodNotAllowed},
		{name: "no request", method: http.MethodPost, form: url.Values{"lab_id": {"x"}}, wantStatus: http.StatusBadRequest},
		{name: "no lab", method: http.MethodPost, form: url.Values{"request_id": {"r1"}}, wantStatus: http.StatusBadRequest},
		{name: "unknown lab", method: http.MethodPost, form: url.Values{"lab_id": {"job-missing"}, "request_id": {"r1"}}, wantStatus: http.StatusNotFound},
	}
	for _, action := range []struct {
		name string
		call func(h *Handler, w http.ResponseWriter, r *http.Request)
	}{
		{"ack", func(h *Handler, w http.ResponseWriter, r *http.Request) { h.AckHelpRequest(w, r) }},
		{"resolve", func(h *Handler, w http.ResponseWriter, r *http.Request) { h.ResolveHelpRequest(w, r) }},
	} {
		action := action
		for _, tt := range rejects {
			tt := tt
			t.Run(action.name+": "+tt.name, func(t *testing.T) {
				t.Parallel()
				h, labID, _ := setup(t)
				rec := httptest.NewRecorder()
				action.call(h, rec, formRequest(tt.method, "/api/admin/help-requests/"+action.name, tt.form))
				assert.Equal(t, tt.wantStatus, rec.Code)
				assert.Len(t, storedHelpRequests(t, h.jobManager, labID), 1, "a rejected action changes nothing")
				assert.Equal(t, HelpStatusWaiting, storedHelpRequests(t, h.jobManager, labID)[0].Status)
			})
		}
	}
}

// ---------------------------------------------------------------------------
// Collecting from the labs' portals
// ---------------------------------------------------------------------------

// The admin's poll collects each portal's outbox, but at most once per lab per
// helpDrainInterval, so a dozen open tabs cannot hammer a lab's cluster.
func TestDrainPortalsForHelp_IsRateLimited(t *testing.T) {
	t.Setenv(EnvPortalImage, testPortalImage)
	h, id, fb := newPortalLab(t, nil)
	h.reconcilePortal(id)

	raise := func(requestID, student string) {
		putOutbox(t, fb, requestID, portalRecord{Kind: portalRecordHelpRequest, HelpRequest: &HelpRequest{
			ID: requestID, At: time.Now(), Student: student, Message: "help",
		}})
	}

	raise("001", "alice@example.com")
	h.drainPortalsForHelp(context.Background())
	require.Len(t, storedHelpRequests(t, h.jobManager, id), 1, "the first poll collects")

	raise("002", "bob@example.com")
	h.drainPortalsForHelp(context.Background())
	assert.Len(t, storedHelpRequests(t, h.jobManager, id), 1, "a second poll inside the interval does not")
	assert.Len(t, fb.outboxRecords(), 1)

	// Once the interval has passed the lab is due again.
	h.helpDrainMu.Lock()
	h.helpDrainAt[id] = time.Now().Add(-helpDrainInterval - time.Second)
	h.helpDrainMu.Unlock()
	h.drainPortalsForHelp(context.Background())
	assert.Len(t, storedHelpRequests(t, h.jobManager, id), 2)
	assert.Empty(t, fb.outboxRecords())
}

func TestDrainPortalsForHelp_SkipsLabsWithoutAPortalAndPortals(t *testing.T) {
	t.Setenv(EnvPortalImage, testPortalImage)

	t.Run("a lab with no portal is not collected", func(t *testing.T) {
		h, id, fb := newPortalLab(t, nil)
		job := mustJob(t, h, id)
		job.mu.Lock()
		job.Config.StudentPortal = false
		job.mu.Unlock()
		putOutbox(t, fb, "001", portalRecord{Kind: portalRecordHelpRequest, HelpRequest: &HelpRequest{ID: "r1", At: time.Now(), Student: "alice"}})

		h.drainPortalsForHelp(context.Background())
		assert.Empty(t, storedHelpRequests(t, h.jobManager, id))
		assert.Len(t, fb.outboxRecords(), 1)
	})

	t.Run("a portal does not collect from itself", func(t *testing.T) {
		h, id, fb := newPortalLab(t, nil)
		h.portal = &PortalRuntime{labID: id}
		putOutbox(t, fb, "001", portalRecord{Kind: portalRecordHelpRequest, HelpRequest: &HelpRequest{ID: "r1", At: time.Now(), Student: "alice"}})

		h.drainPortalsForHelp(context.Background())
		assert.Empty(t, storedHelpRequests(t, h.jobManager, id))
	})
}
