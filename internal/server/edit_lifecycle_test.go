package server

import (
	"encoding/json"
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

// postLifecycle drives UpdateLabLifecycle with a urlencoded body.
func postLifecycle(h *Handler, id, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/labs/"+id+"/lifecycle", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.UpdateLabLifecycle(rec, req)
	return rec
}

func TestUpdateLabLifecycle_WorkspaceHours(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		form       url.Values
		wantHours  int
		wantStatus int
	}{
		{
			name:       "plain hours",
			form:       url.Values{"workspace_lifetime_hours": {"6"}, "workspace_lifetime_unit": {"hours"}},
			wantHours:  6,
			wantStatus: http.StatusOK,
		},
		{
			name:       "days converted to hours",
			form:       url.Values{"workspace_lifetime_hours": {"2"}, "workspace_lifetime_unit": {"days"}},
			wantHours:  48,
			wantStatus: http.StatusOK,
		},
		{
			name:       "blank disables cleanup",
			form:       url.Values{"workspace_lifetime_hours": {""}},
			wantHours:  0,
			wantStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h, jm := newUploadTestHandler(t)
			id := completedLab(t, jm)

			rec := postLifecycle(h, id, tt.form.Encode())
			require.Equal(t, tt.wantStatus, rec.Code, "body: %s", rec.Body.String())

			job, _ := jm.GetJob(id)
			job.mu.RLock()
			defer job.mu.RUnlock()
			assert.Equal(t, tt.wantHours, job.Config.WorkspaceLifetimeHours)
		})
	}
}

// TestUpdateLabLifecycle_PersistsAsynchronously is a regression test for
// making the request-path SaveJob call async: the handler must still respond
// successfully, and the change must eventually land on disk even though the
// HTTP response doesn't wait for the write.
func TestUpdateLabLifecycle_PersistsAsynchronously(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	jm := NewJobManager(dataDir)
	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	id := completedLab(t, jm)

	rec := postLifecycle(h, id, url.Values{"workspace_lifetime_hours": {"6"}, "workspace_lifetime_unit": {"hours"}}.Encode())
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	jobFile := filepath.Join(dataDir, "jobs", id+".json")
	deadline := time.Now().Add(2 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(jobFile)
		if err == nil {
			var persisted struct {
				Config struct {
					WorkspaceLifetimeHours int `json:"workspace_lifetime_hours"`
				} `json:"config"`
			}
			require.NoError(t, json.Unmarshal(data, &persisted))
			if persisted.Config.WorkspaceLifetimeHours == 6 {
				return // async save landed with the expected content
			}
		} else {
			lastErr = err
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job file never reflected the async save within the deadline (last read error: %v)", lastErr)
}

func TestUpdateLabLifecycle_WorkspaceHoursInvalid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		form url.Values
	}{
		{name: "not a number", form: url.Values{"workspace_lifetime_hours": {"abc"}}},
		{name: "negative", form: url.Values{"workspace_lifetime_hours": {"-1"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h, jm := newUploadTestHandler(t)
			id := completedLab(t, jm)

			rec := postLifecycle(h, id, tt.form.Encode())
			assert.Equal(t, http.StatusBadRequest, rec.Code)
		})
	}
}

func TestUpdateLabLifecycle_DeletionDate(t *testing.T) {
	t.Parallel()

	future := time.Now().Add(48 * time.Hour)
	futureDateStr := future.Format("2006-01-02")

	h, jm := newUploadTestHandler(t)
	id := completedLab(t, jm)

	// Set a future deletion date and time.
	rec := postLifecycle(h, id, url.Values{
		"lab_deletion_date": {futureDateStr},
		"lab_deletion_time": {"09:30"},
	}.Encode())
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	job, _ := jm.GetJob(id)
	job.mu.RLock()
	require.NotNil(t, job.Config.LabDeletionDate)
	assert.Equal(t, futureDateStr, job.Config.LabDeletionDate.Format("2006-01-02"))
	assert.Equal(t, "09:30", job.Config.LabDeletionDate.Format("15:04"))
	job.mu.RUnlock()

	// Clearing the date (blank field) disables auto-deletion.
	rec = postLifecycle(h, id, url.Values{"lab_deletion_date": {""}}.Encode())
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	job.mu.RLock()
	assert.Nil(t, job.Config.LabDeletionDate)
	job.mu.RUnlock()
}

func TestUpdateLabLifecycle_DeletionDateInPast(t *testing.T) {
	t.Parallel()

	h, jm := newUploadTestHandler(t)
	id := completedLab(t, jm)

	rec := postLifecycle(h, id, url.Values{"lab_deletion_date": {"2000-01-01"}}.Encode())
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "future")

	// The lab's config is left untouched on a rejected update.
	job, _ := jm.GetJob(id)
	job.mu.RLock()
	assert.Nil(t, job.Config.LabDeletionDate)
	job.mu.RUnlock()
}

func TestUpdateLabLifecycle_NotCompleted(t *testing.T) {
	t.Parallel()

	h, jm := newUploadTestHandler(t)
	id := jm.CreateJob(&LabConfig{StackName: "test"}) // left in default (non-completed) status

	rec := postLifecycle(h, id, url.Values{"workspace_lifetime_hours": {"6"}}.Encode())
	require.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "not ready")
}

func TestUpdateLabLifecycle_UnknownLab(t *testing.T) {
	t.Parallel()

	h, _ := newUploadTestHandler(t)

	rec := postLifecycle(h, "does-not-exist", url.Values{"workspace_lifetime_hours": {"6"}}.Encode())
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestUpdateLabLifecycle_MethodNotAllowed(t *testing.T) {
	t.Parallel()

	h, jm := newUploadTestHandler(t)
	id := completedLab(t, jm)

	req := httptest.NewRequest(http.MethodGet, "/api/labs/"+id+"/lifecycle", nil)
	rec := httptest.NewRecorder()
	h.UpdateLabLifecycle(rec, req)
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}
