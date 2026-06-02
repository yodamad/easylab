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

// --- FeedbackStore unit tests ---

func TestNewFeedbackStore_Success(t *testing.T) {
	dir := t.TempDir()
	fs, err := NewFeedbackStore(dir)
	require.NoError(t, err)
	assert.Equal(t, dir, fs.dataDir)
}

func TestNewFeedbackStore_CreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "new", "nested", "dir")
	_, err := NewFeedbackStore(dir)
	require.NoError(t, err)
	_, statErr := os.Stat(dir)
	assert.NoError(t, statErr)
}

func TestFeedbackStore_GetByLab_Empty(t *testing.T) {
	fs, err := NewFeedbackStore(t.TempDir())
	require.NoError(t, err)

	entries, err := fs.GetByLab("nonexistent-lab")
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestFeedbackStore_AddAndGetByLab(t *testing.T) {
	fs, err := NewFeedbackStore(t.TempDir())
	require.NoError(t, err)

	f := Feedback{
		ID:          "1",
		LabID:       "lab-abc",
		Email:       "student@example.com",
		Rating:      4,
		Difficulty:  "just-right",
		Recommend:   "yes",
		Comment:     "Great lab!",
		SubmittedAt: time.Now().UTC(),
	}
	require.NoError(t, fs.Add(f))

	entries, err := fs.GetByLab("lab-abc")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, f.Email, entries[0].Email)
	assert.Equal(t, f.Rating, entries[0].Rating)
	assert.Equal(t, f.Difficulty, entries[0].Difficulty)
}

func TestFeedbackStore_AddMultiple(t *testing.T) {
	fs, err := NewFeedbackStore(t.TempDir())
	require.NoError(t, err)

	for i := 1; i <= 3; i++ {
		require.NoError(t, fs.Add(Feedback{
			ID:         string(rune('0' + i)),
			LabID:      "lab-x",
			Rating:     i,
			Difficulty: "just-right",
		}))
	}

	entries, err := fs.GetByLab("lab-x")
	require.NoError(t, err)
	assert.Len(t, entries, 3)
}

func TestFeedbackStore_AddIsolatedByLab(t *testing.T) {
	fs, err := NewFeedbackStore(t.TempDir())
	require.NoError(t, err)

	require.NoError(t, fs.Add(Feedback{ID: "1", LabID: "lab-a", Rating: 5, Difficulty: "just-right"}))
	require.NoError(t, fs.Add(Feedback{ID: "2", LabID: "lab-b", Rating: 3, Difficulty: "challenging"}))

	a, err := fs.GetByLab("lab-a")
	require.NoError(t, err)
	assert.Len(t, a, 1)

	b, err := fs.GetByLab("lab-b")
	require.NoError(t, err)
	assert.Len(t, b, 1)
}

func TestFeedbackStore_ReadUnsafe_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	fs, err := NewFeedbackStore(dir)
	require.NoError(t, err)

	labID := "lab-bad"
	// Write invalid JSON to the file
	err = os.WriteFile(filepath.Join(dir, labID+".json"), []byte("not valid json"), 0644)
	require.NoError(t, err)

	_, err = fs.GetByLab(labID)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse")
}

func TestFeedbackStore_JSONRoundtrip(t *testing.T) {
	fs, err := NewFeedbackStore(t.TempDir())
	require.NoError(t, err)

	original := Feedback{
		ID:          "42",
		LabID:       "lab-rt",
		Email:       "user@test.com",
		Rating:      5,
		Difficulty:  "too-hard",
		Recommend:   "maybe",
		Comment:     "Needed more hints",
		SubmittedAt: time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC),
	}
	require.NoError(t, fs.Add(original))

	entries, err := fs.GetByLab("lab-rt")
	require.NoError(t, err)
	require.Len(t, entries, 1)

	data, _ := json.Marshal(entries[0])
	var restored Feedback
	require.NoError(t, json.Unmarshal(data, &restored))
	assert.Equal(t, original.Rating, restored.Rating)
	assert.Equal(t, original.Comment, restored.Comment)
	assert.Equal(t, original.SubmittedAt.Unix(), restored.SubmittedAt.Unix())
}

// --- writeToast tests ---

func TestWriteToast_Success(t *testing.T) {
	w := httptest.NewRecorder()
	writeToast(w, true, "All good!")

	body := w.Body.String()
	assert.Contains(t, body, "toast-success")
	assert.Contains(t, body, "All good!")
	assert.Contains(t, body, "✅")
	assert.Equal(t, "text/html", w.Header().Get("Content-Type"))
}

func TestWriteToast_Error(t *testing.T) {
	w := httptest.NewRecorder()
	writeToast(w, false, "Something went wrong")

	body := w.Body.String()
	assert.Contains(t, body, "toast-error")
	assert.Contains(t, body, "Something went wrong")
	assert.Contains(t, body, "❌")
}

// --- isHTMXRequest tests ---

func TestIsHTMXRequest(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   bool
	}{
		{"htmx request", "true", true},
		{"non-htmx", "", false},
		{"wrong value", "1", false},
		{"false value", "false", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequest("GET", "/", nil)
			if tt.header != "" {
				r.Header.Set("HX-Request", tt.header)
			}
			assert.Equal(t, tt.want, isHTMXRequest(r))
		})
	}
}

// --- redirectFeedback tests ---

func TestRedirectFeedback_Success(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/student/feedback", nil)
	redirectFeedback(w, r, "")

	assert.Equal(t, http.StatusSeeOther, w.Code)
	assert.Equal(t, "/student/feedback?success=1", w.Header().Get("Location"))
}

func TestRedirectFeedback_Error(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/student/feedback", nil)
	redirectFeedback(w, r, "something+went+wrong")

	assert.Equal(t, http.StatusSeeOther, w.Code)
	assert.Equal(t, "/student/feedback?error=something+went+wrong", w.Header().Get("Location"))
}

// --- SubmitFeedback HTTP handler tests ---

func newHandlerWithFeedbackStore(t *testing.T) *Handler {
	t.Helper()
	fs, err := NewFeedbackStore(t.TempDir())
	require.NoError(t, err)
	return NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, fs)
}

func TestSubmitFeedback_WrongMethod(t *testing.T) {
	h := newHandlerWithFeedbackStore(t)
	req := httptest.NewRequest("GET", "/api/student/feedback", nil)
	w := httptest.NewRecorder()

	h.SubmitFeedback(w, req)
	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

func TestSubmitFeedback_MissingLabID(t *testing.T) {
	tests := []struct {
		name string
		htmx bool
	}{
		{"htmx", true},
		{"non-htmx", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHandlerWithFeedbackStore(t)
			form := url.Values{}
			form.Set("rating", "3")
			form.Set("difficulty", "just-right")
			req := httptest.NewRequest("POST", "/api/student/feedback", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if tt.htmx {
				req.Header.Set("HX-Request", "true")
			}
			w := httptest.NewRecorder()

			h.SubmitFeedback(w, req)

			if tt.htmx {
				assert.Contains(t, w.Body.String(), "toast-error")
			} else {
				assert.Equal(t, http.StatusSeeOther, w.Code)
			}
		})
	}
}

func TestSubmitFeedback_InvalidRating(t *testing.T) {
	tests := []struct {
		name   string
		rating string
	}{
		{"zero", "0"},
		{"too high", "6"},
		{"not a number", "abc"},
		{"empty", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHandlerWithFeedbackStore(t)
			form := url.Values{}
			form.Set("lab_id", "lab-1")
			form.Set("rating", tt.rating)
			form.Set("difficulty", "just-right")
			req := httptest.NewRequest("POST", "/api/student/feedback", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("HX-Request", "true")
			w := httptest.NewRecorder()

			h.SubmitFeedback(w, req)
			assert.Contains(t, w.Body.String(), "toast-error")
		})
	}
}

func TestSubmitFeedback_InvalidDifficulty(t *testing.T) {
	h := newHandlerWithFeedbackStore(t)
	form := url.Values{}
	form.Set("lab_id", "lab-1")
	form.Set("rating", "3")
	form.Set("difficulty", "invalid-value")
	req := httptest.NewRequest("POST", "/api/student/feedback", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	w := httptest.NewRecorder()

	h.SubmitFeedback(w, req)
	assert.Contains(t, w.Body.String(), "toast-error")
}

func TestSubmitFeedback_Success_HTMX(t *testing.T) {
	h := newHandlerWithFeedbackStore(t)
	form := url.Values{}
	form.Set("lab_id", "lab-1")
	form.Set("rating", "5")
	form.Set("difficulty", "just-right")
	form.Set("recommend", "yes")
	form.Set("comment", "Excellent lab!")
	req := httptest.NewRequest("POST", "/api/student/feedback", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	w := httptest.NewRecorder()

	h.SubmitFeedback(w, req)
	assert.Contains(t, w.Body.String(), "toast-success")
	assert.Contains(t, w.Body.String(), "Thank you")
}

func TestSubmitFeedback_Success_NonHTMX(t *testing.T) {
	h := newHandlerWithFeedbackStore(t)
	form := url.Values{}
	form.Set("lab_id", "lab-1")
	form.Set("rating", "4")
	form.Set("difficulty", "challenging")
	form.Set("recommend", "maybe")
	req := httptest.NewRequest("POST", "/api/student/feedback", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	h.SubmitFeedback(w, req)
	assert.Equal(t, http.StatusSeeOther, w.Code)
	assert.Contains(t, w.Header().Get("Location"), "success=1")
}

func TestSubmitFeedback_AllDifficultyValues(t *testing.T) {
	difficulties := []string{"too-easy", "a-bit-easy", "just-right", "challenging", "too-hard"}

	for _, d := range difficulties {
		t.Run(d, func(t *testing.T) {
			t.Parallel()
			h := newHandlerWithFeedbackStore(t)
			form := url.Values{}
			form.Set("lab_id", "lab-diff")
			form.Set("rating", "3")
			form.Set("difficulty", d)
			req := httptest.NewRequest("POST", "/api/student/feedback", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("HX-Request", "true")
			w := httptest.NewRecorder()

			h.SubmitFeedback(w, req)
			assert.Contains(t, w.Body.String(), "toast-success")
		})
	}
}

func TestSubmitFeedback_CommentTruncated(t *testing.T) {
	h := newHandlerWithFeedbackStore(t)
	form := url.Values{}
	form.Set("lab_id", "lab-1")
	form.Set("rating", "3")
	form.Set("difficulty", "just-right")
	form.Set("comment", strings.Repeat("x", 3000))
	req := httptest.NewRequest("POST", "/api/student/feedback", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	w := httptest.NewRecorder()

	h.SubmitFeedback(w, req)
	assert.Contains(t, w.Body.String(), "toast-success")

	// Verify comment was truncated in the store
	entries, err := h.feedbackStore.GetByLab("lab-1")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Len(t, entries[0].Comment, 2000)
}

// --- ServeAdminLabFeedback tests ---

func TestServeAdminLabFeedback_NoLabID(t *testing.T) {
	h := newHandlerWithFeedbackStore(t)
	req := httptest.NewRequest("GET", "/admin/feedback", nil)
	w := httptest.NewRecorder()

	h.ServeAdminLabFeedback(w, req)
	// Will return 500 since templates aren't loaded in tests — but the function body runs
	// (HasLabID=false path is exercised)
}

func TestServeAdminLabFeedback_WithLabID_NoFeedback(t *testing.T) {
	jm := NewJobManager("")
	jobID := jm.CreateJob(&LabConfig{StackName: "my-lab"})

	fs, err := NewFeedbackStore(t.TempDir())
	require.NoError(t, err)
	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, fs)

	req := httptest.NewRequest("GET", "/admin/feedback?lab_id="+jobID, nil)
	w := httptest.NewRecorder()

	h.ServeAdminLabFeedback(w, req)
	// Exercises the GetByLab path with an empty result
}

func TestServeAdminLabFeedback_WithLabID_HasFeedback(t *testing.T) {
	jm := NewJobManager("")
	jobID := jm.CreateJob(&LabConfig{StackName: "my-lab"})

	fs, err := NewFeedbackStore(t.TempDir())
	require.NoError(t, err)

	require.NoError(t, fs.Add(Feedback{
		ID:          "1",
		LabID:       jobID,
		Email:       "a@b.com",
		Rating:      5,
		Difficulty:  "just-right",
		Recommend:   "yes",
		Comment:     "Great!",
		SubmittedAt: time.Now(),
	}))
	require.NoError(t, fs.Add(Feedback{
		ID:          "2",
		LabID:       jobID,
		Email:       "c@d.com",
		Rating:      3,
		Difficulty:  "challenging",
		Recommend:   "maybe",
		Comment:     "",
		SubmittedAt: time.Now(),
	}))

	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, fs)

	req := httptest.NewRequest("GET", "/admin/feedback?lab_id="+jobID, nil)
	w := httptest.NewRecorder()

	h.ServeAdminLabFeedback(w, req)
	// Exercises average calculation + star building paths
}

func TestServeAdminLabFeedback_UnknownLabID(t *testing.T) {
	fs, err := NewFeedbackStore(t.TempDir())
	require.NoError(t, err)
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, fs)

	req := httptest.NewRequest("GET", "/admin/feedback?lab_id=unknown-lab", nil)
	w := httptest.NewRecorder()

	h.ServeAdminLabFeedback(w, req)
	// lab not in jobManager → LabName falls back to labID value
}

// --- ServeFeedback ---

func TestServeFeedback(t *testing.T) {
	fs, err := NewFeedbackStore(t.TempDir())
	require.NoError(t, err)
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, fs)

	req := httptest.NewRequest("GET", "/student/feedback?success=1", nil)
	w := httptest.NewRecorder()
	h.ServeFeedback(w, req)
	// Template will fail (no web/ dir) but the handler body runs
}

func TestServeFeedback_WithError(t *testing.T) {
	fs, err := NewFeedbackStore(t.TempDir())
	require.NoError(t, err)
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, fs)

	req := httptest.NewRequest("GET", "/student/feedback?error=something+went+wrong", nil)
	w := httptest.NewRecorder()
	h.ServeFeedback(w, req)
}
