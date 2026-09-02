package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- AuditStore ---

func TestNewAuditStore_Success(t *testing.T) {
	dir := t.TempDir()
	as, err := NewAuditStore(dir)
	require.NoError(t, err)
	assert.Equal(t, dir, as.dataDir)
}

func TestNewAuditStore_CreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "new", "nested", "dir")
	_, err := NewAuditStore(dir)
	require.NoError(t, err)
	_, statErr := os.Stat(dir)
	assert.NoError(t, statErr)
}

func TestAuditStore_Recent_Empty(t *testing.T) {
	as, err := NewAuditStore(t.TempDir())
	require.NoError(t, err)

	entries, err := as.Recent(10)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestAuditStore_RecordAndRecent_RoundTrip(t *testing.T) {
	as, err := NewAuditStore(t.TempDir())
	require.NoError(t, err)

	base := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	require.NoError(t, as.Record(AuditEntry{At: base, Actor: "admin", Role: "admin", Action: "lab.create", LabID: "job-1", Detail: "stack-a"}))
	require.NoError(t, as.Record(AuditEntry{At: base.Add(time.Minute), Actor: "student@example.com", Role: "student", Action: "workspace.create", LabID: "job-1", Detail: "ws-1"}))
	require.NoError(t, as.Record(AuditEntry{At: base.Add(2 * time.Minute), Actor: "system", Role: "system", Action: "lab.destroy", LabID: "job-1", Detail: "auto-cleanup"}))

	entries, err := as.Recent(0)
	require.NoError(t, err)
	require.Len(t, entries, 3)
	// Newest first.
	assert.Equal(t, "lab.destroy", entries[0].Action)
	assert.Equal(t, "workspace.create", entries[1].Action)
	assert.Equal(t, "lab.create", entries[2].Action)
	assert.Equal(t, "student@example.com", entries[1].Actor)
}

func TestAuditStore_Recent_LimitCaps(t *testing.T) {
	as, err := NewAuditStore(t.TempDir())
	require.NoError(t, err)

	for i := 0; i < 5; i++ {
		require.NoError(t, as.Record(AuditEntry{At: time.Now(), Actor: "admin", Role: "admin", Action: "lab.create"}))
	}

	entries, err := as.Recent(2)
	require.NoError(t, err)
	assert.Len(t, entries, 2)
}

func TestAuditStore_RecentForLab_FiltersByLab(t *testing.T) {
	as, err := NewAuditStore(t.TempDir())
	require.NoError(t, err)

	base := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	require.NoError(t, as.Record(AuditEntry{At: base, Actor: "admin", Role: "admin", Action: "lab.create", LabID: "job-1"}))
	require.NoError(t, as.Record(AuditEntry{At: base.Add(time.Minute), Actor: "admin", Role: "admin", Action: "lab.create", LabID: "job-2"}))
	require.NoError(t, as.Record(AuditEntry{At: base.Add(2 * time.Minute), Actor: "admin", Role: "admin", Action: "lab.destroy", LabID: "job-1"}))

	entries, err := as.RecentForLab("job-1", 0)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	// Newest first, only job-1's entries.
	assert.Equal(t, "lab.destroy", entries[0].Action)
	assert.Equal(t, "lab.create", entries[1].Action)
	for _, e := range entries {
		assert.Equal(t, "job-1", e.LabID)
	}
}

func TestAuditStore_RecentForLab_LimitCaps(t *testing.T) {
	as, err := NewAuditStore(t.TempDir())
	require.NoError(t, err)

	for i := 0; i < 5; i++ {
		require.NoError(t, as.Record(AuditEntry{At: time.Now(), Actor: "admin", Role: "admin", Action: "lab.create", LabID: "job-1"}))
	}

	entries, err := as.RecentForLab("job-1", 2)
	require.NoError(t, err)
	assert.Len(t, entries, 2)
}

func TestAuditStore_RecentForLab_NoMatches(t *testing.T) {
	as, err := NewAuditStore(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, as.Record(AuditEntry{At: time.Now(), Actor: "admin", Role: "admin", Action: "lab.create", LabID: "job-1"}))

	entries, err := as.RecentForLab("job-unknown", 0)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestAuditStore_Recent_SkipsMalformedLines(t *testing.T) {
	dir := t.TempDir()
	as, err := NewAuditStore(dir)
	require.NoError(t, err)

	require.NoError(t, as.Record(AuditEntry{At: time.Now(), Actor: "admin", Role: "admin", Action: "lab.create"}))

	// Simulate a partial/corrupt write (e.g. a crash mid-write).
	f, err := os.OpenFile(filepath.Join(dir, "audit.jsonl"), os.O_APPEND|os.O_WRONLY, 0600)
	require.NoError(t, err)
	_, err = f.WriteString("not valid json\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	require.NoError(t, as.Record(AuditEntry{At: time.Now(), Actor: "admin", Role: "admin", Action: "lab.destroy"}))

	entries, err := as.Recent(0)
	require.NoError(t, err)
	require.Len(t, entries, 2, "the malformed line should be skipped, not fail the whole read")
}

// --- adminActor ---

func TestAdminActor(t *testing.T) {
	base := httptest.NewRequest("GET", "/", nil)
	withEmail := base.WithContext(context.WithValue(base.Context(), adminEmailContextKey, "admin@example.com"))
	assert.Equal(t, "admin@example.com", adminActor(withEmail))

	noEmail := httptest.NewRequest("GET", "/", nil)
	assert.Equal(t, "admin", adminActor(noEmail))

	emptyEmail := base.WithContext(context.WithValue(base.Context(), adminEmailContextKey, ""))
	assert.Equal(t, "admin", adminActor(emptyEmail), "an empty (classic-login) session email must fall back to the generic label")
}

// --- recordAudit + representative handlers ---

func TestHandler_RecordAudit_NoopWithoutStore(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	// Must not panic when no audit store is configured.
	h.recordAudit("admin", "admin", "lab.create", "job-1", "")
}

func TestHandler_SetCredentials_RecordsAudit(t *testing.T) {
	as, err := NewAuditStore(t.TempDir())
	require.NoError(t, err)
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	h.SetAuditStore(as)

	form := "provider=ovh&ovh_application_key=k&ovh_application_secret=s&ovh_consumer_key=c&ovh_service_name=svc&ovh_endpoint=ovh-eu"
	req := httptest.NewRequest("POST", "/api/credentials", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.SetCredentials(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	waitForAuditEntry(t, as, "credentials.set")
}

func TestHandler_DeleteLab_RecordsAudit(t *testing.T) {
	as, err := NewAuditStore(t.TempDir())
	require.NoError(t, err)
	jm := NewJobManager("")
	jobID := jm.CreateJob(&LabConfig{StackName: "test"})
	require.NoError(t, jm.UpdateJobStatus(jobID, JobStatusFailed))

	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	h.SetAuditStore(as)

	req := httptest.NewRequest("POST", "/api/labs/"+jobID+"/delete", nil)
	w := httptest.NewRecorder()
	h.DeleteLab(w, req)
	require.Equal(t, http.StatusSeeOther, w.Code)

	entries := waitForAuditEntry(t, as, "lab.delete")
	assert.Equal(t, jobID, entries[0].LabID)
	assert.Equal(t, "admin", entries[0].Actor)
}

func TestHandler_RequestWorkspace_RecordsAuditAsStudent(t *testing.T) {
	as, err := NewAuditStore(t.TempDir())
	require.NoError(t, err)
	jm := NewJobManager("")
	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	h.SetAuditStore(as)
	useFakeBackend(h, &fakeBackend{reachable: true})

	labID := completedLabWithKubeconfig(jm, 0)
	job, _ := jm.GetJob(labID)
	job.mu.Lock()
	job.Config.WorkspaceTemplates = []WorkspaceTemplate{{Name: "default"}}
	job.mu.Unlock()

	form := url.Values{"lab_id": {labID}}
	req := postForm(t, "/api/workspace/request", form)
	req = req.WithContext(context.WithValue(req.Context(), studentEmailContextKey, "alice@example.com"))

	rec := httptest.NewRecorder()
	h.RequestWorkspace(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	entries := waitForAuditEntry(t, as, "workspace.create")
	assert.Equal(t, "alice@example.com", entries[0].Actor)
	assert.Equal(t, "student", entries[0].Role)
	assert.Equal(t, labID, entries[0].LabID)
}

// waitForAuditEntry polls the audit store briefly since recordAudit writes
// asynchronously (off the response's critical path), returning the matching
// entries newest-first once found.
func waitForAuditEntry(t *testing.T, as *AuditStore, action string) []AuditEntry {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		entries, err := as.Recent(0)
		require.NoError(t, err)
		var matched []AuditEntry
		for _, e := range entries {
			if e.Action == action {
				matched = append(matched, e)
			}
		}
		if len(matched) > 0 {
			return matched
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no audit entry with action %q recorded within the deadline", action)
	return nil
}
