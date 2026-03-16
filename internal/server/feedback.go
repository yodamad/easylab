package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Feedback represents a single student feedback entry
type Feedback struct {
	ID          string    `json:"id"`
	LabID       string    `json:"lab_id"`
	Email       string    `json:"email"`
	Rating      int       `json:"rating"`     // 1-5
	Difficulty  string    `json:"difficulty"` // "too-easy", "a-bit-easy", "just-right", "challenging", "too-hard"
	Recommend   string    `json:"recommend"`  // "yes", "maybe", "no"
	Comment     string    `json:"comment"`
	SubmittedAt time.Time `json:"submitted_at"`
}

// FeedbackStore manages feedback persistence on disk
type FeedbackStore struct {
	dataDir string
	mu      sync.RWMutex
}

// NewFeedbackStore creates a new FeedbackStore that persists data in dataDir
func NewFeedbackStore(dataDir string) (*FeedbackStore, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create feedback data directory: %w", err)
	}
	return &FeedbackStore{dataDir: dataDir}, nil
}

func (fs *FeedbackStore) filePath(labID string) string {
	return filepath.Join(fs.dataDir, labID+".json")
}

// Add appends a feedback entry to the lab's JSON file
func (fs *FeedbackStore) Add(f Feedback) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	entries, err := fs.readUnsafe(f.LabID)
	if err != nil {
		return err
	}
	entries = append(entries, f)
	return fs.writeUnsafe(f.LabID, entries)
}

// GetByLab returns all feedback entries for a given lab
func (fs *FeedbackStore) GetByLab(labID string) ([]Feedback, error) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()
	return fs.readUnsafe(labID)
}

func (fs *FeedbackStore) readUnsafe(labID string) ([]Feedback, error) {
	path := fs.filePath(labID)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return []Feedback{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read feedback file: %w", err)
	}
	var entries []Feedback
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("failed to parse feedback file: %w", err)
	}
	return entries, nil
}

func (fs *FeedbackStore) writeUnsafe(labID string, entries []Feedback) error {
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal feedback: %w", err)
	}
	if err := os.WriteFile(fs.filePath(labID), data, 0644); err != nil {
		return fmt.Errorf("failed to write feedback file: %w", err)
	}
	return nil
}

// ServeFeedback serves the student feedback form page
func (h *Handler) ServeFeedback(w http.ResponseWriter, r *http.Request) {
	data := map[string]interface{}{
		"Success": r.URL.Query().Get("success") == "1",
		"Error":   r.URL.Query().Get("error"),
		"Email":   studentEmailFromContext(r),
	}
	h.serveTemplate(w, "student-feedback.html", data)
}

// writeToast writes a toast notification HTML fragment to the response
func writeToast(w http.ResponseWriter, success bool, message string) {
	w.Header().Set("Content-Type", "text/html")
	class := "toast toast-success"
	icon := "✅"
	if !success {
		class = "toast toast-error"
		icon = "❌"
	}
	fmt.Fprintf(w, `<div class="%s"><span class="toast-icon">%s</span><span>%s</span></div>`, class, icon, message)
}

// isHTMXRequest returns true when the request was made by HTMX (has HX-Request header)
func isHTMXRequest(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true"
}

// redirectFeedback redirects non-HTMX form submissions back to the feedback page
func redirectFeedback(w http.ResponseWriter, r *http.Request, errMsg string) {
	target := "/student/feedback"
	if errMsg != "" {
		target += "?error=" + errMsg
	} else {
		target += "?success=1"
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// SubmitFeedback handles POST /api/student/feedback
func (h *Handler) SubmitFeedback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	htmx := isHTMXRequest(r)

	if err := r.ParseForm(); err != nil {
		if htmx {
			writeToast(w, false, "Invalid request. Please try again.")
		} else {
			redirectFeedback(w, r, "Invalid+request")
		}
		return
	}

	labID := getFormValue(r, "lab_id")
	if labID == "" {
		if htmx {
			writeToast(w, false, "Please select a lab.")
		} else {
			redirectFeedback(w, r, "Please+select+a+lab")
		}
		return
	}

	rating := atoiForm(getFormValue(r, "rating"))
	if rating < 1 || rating > 5 {
		if htmx {
			writeToast(w, false, "Please select a rating between 1 and 5.")
		} else {
			redirectFeedback(w, r, "Please+select+a+rating")
		}
		return
	}

	difficulty := getFormValue(r, "difficulty")
	validDifficulty := map[string]bool{"too-easy": true, "a-bit-easy": true, "just-right": true, "challenging": true, "too-hard": true}
	if !validDifficulty[difficulty] {
		if htmx {
			writeToast(w, false, "Please select a difficulty level.")
		} else {
			redirectFeedback(w, r, "Please+select+a+difficulty+level")
		}
		return
	}

	recommend := getFormValue(r, "recommend") // optional: "yes", "maybe", "no"

	comment := getFormValue(r, "comment")
	if len(comment) > 2000 {
		comment = comment[:2000]
	}

	f := Feedback{
		ID:          fmt.Sprintf("%d", time.Now().UnixNano()),
		LabID:       labID,
		Email:       studentEmailFromContext(r),
		Rating:      rating,
		Difficulty:  difficulty,
		Recommend:   recommend,
		Comment:     comment,
		SubmittedAt: time.Now().UTC(),
	}

	if err := h.feedbackStore.Add(f); err != nil {
		log.Printf("Failed to save feedback for lab %s: %v", labID, err)
		if htmx {
			writeToast(w, false, "Failed to save feedback. Please try again.")
		} else {
			redirectFeedback(w, r, "Failed+to+save+feedback")
		}
		return
	}

	log.Printf("Feedback submitted for lab %s (rating=%d)", labID, rating)
	if htmx {
		writeToast(w, true, "Thank you! Your feedback has been saved.")
	} else {
		redirectFeedback(w, r, "")
	}
}

// ServeAdminLabFeedback serves the admin feedback view for a specific lab
// Route: GET /admin/feedback?lab_id=...
func (h *Handler) ServeAdminLabFeedback(w http.ResponseWriter, r *http.Request) {
	labID := r.URL.Query().Get("lab_id")

	type LabOption struct {
		ID       string
		Name     string
		Selected bool
	}

	type FeedbackDisplay struct {
		ID          string
		Email       string
		Rating      int
		Stars       string
		Difficulty  string
		Comment     string
		SubmittedAt string
	}

	type LabFeedbackData struct {
		LabID     string
		LabName   string
		Labs      []LabOption
		Feedbacks []FeedbackDisplay
		Count     int
		AvgRating string
		HasLabID  bool
	}

	// Build lab list from jobManager (admin-side, no auth issue)
	allJobs := h.jobManager.GetAllJobs()
	var labs []LabOption
	for _, job := range allJobs {
		job.mu.RLock()
		id := job.ID
		name := id
		if job.Config != nil && job.Config.StackName != "" {
			name = job.Config.StackName
		}
		job.mu.RUnlock()
		labs = append(labs, LabOption{ID: id, Name: name, Selected: id == labID})
	}

	data := LabFeedbackData{
		LabID:    labID,
		HasLabID: labID != "",
		Labs:     labs,
	}

	if labID != "" {
		// Resolve lab display name
		for _, l := range labs {
			if l.ID == labID {
				data.LabName = l.Name
				break
			}
		}
		if data.LabName == "" {
			data.LabName = labID
		}

		entries, err := h.feedbackStore.GetByLab(labID)
		if err != nil {
			log.Printf("Failed to load feedback for lab %s: %v", labID, err)
			http.Error(w, "Failed to load feedback", http.StatusInternalServerError)
			return
		}

		difficultyLabel := map[string]string{
			"too-easy":   "😴 Too Easy",
			"a-bit-easy": "🙂 A Bit Easy",
			"just-right": "👍 Just Right",
			"challenging": "🤔 Challenging",
			"too-hard":   "🔥 Too Hard",
		}

		var totalRating int
		for _, e := range entries {
			stars := ""
			for i := 0; i < 5; i++ {
				if i < e.Rating {
					stars += "★"
				} else {
					stars += "☆"
				}
			}
			data.Feedbacks = append(data.Feedbacks, FeedbackDisplay{
				ID:          e.ID,
				Email:       e.Email,
				Rating:      e.Rating,
				Stars:       stars,
				Difficulty:  difficultyLabel[e.Difficulty],
				Comment:     e.Comment,
				SubmittedAt: e.SubmittedAt.Format("2006-01-02 15:04"),
			})
			totalRating += e.Rating
		}

		data.Count = len(entries)
		if data.Count > 0 {
			avg := float64(totalRating) / float64(data.Count)
			data.AvgRating = fmt.Sprintf("%.1f", avg)
		} else {
			data.AvgRating = "—"
		}
	}

	h.serveTemplate(w, "admin-feedback.html", data)
}
