package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseMode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    string
		expected Mode
		wantErr  bool
	}{
		{name: "empty defaults to all", input: "", expected: ModeAll},
		{name: "all", input: "all", expected: ModeAll},
		{name: "admin", input: "admin", expected: ModeAdmin},
		{name: "student", input: "student", expected: ModeStudent},
		{name: "case and spaces are ignored", input: "  Admin ", expected: ModeAdmin},
		{name: "unknown", input: "teacher", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseMode(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestMode_Serves(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		mode        Mode
		wantAdmin   bool
		wantStudent bool
	}{
		{name: "all", mode: ModeAll, wantAdmin: true, wantStudent: true},
		{name: "admin", mode: ModeAdmin, wantAdmin: true},
		{name: "student", mode: ModeStudent, wantStudent: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.wantAdmin, tt.mode.ServesAdmin())
			assert.Equal(t, tt.wantStudent, tt.mode.ServesStudent())
		})
	}
}

// The homepage asks "student or admin?" only where both answers lead somewhere.
func TestServeUI_ByMode(t *testing.T) {
	t.Chdir("../..")

	tests := []struct {
		name            string
		mode            Mode
		wantStatus      int
		wantLocation    string
		wantStudentCard bool
	}{
		{name: "unset behaves as all", mode: "", wantStatus: http.StatusOK, wantStudentCard: true},
		{name: "all", mode: ModeAll, wantStatus: http.StatusOK, wantStudentCard: true},
		{name: "admin hides the student space", mode: ModeAdmin, wantStatus: http.StatusOK},
		{name: "student goes straight to the portal", mode: ModeStudent, wantStatus: http.StatusSeeOther, wantLocation: "/student/dashboard"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewHandler(NewJobManager(""), nil, nil, nil, nil, nil)
			h.SetMode(tt.mode)

			w := httptest.NewRecorder()
			h.ServeUI(w, httptest.NewRequest(http.MethodGet, "/", nil))

			require.Equal(t, tt.wantStatus, w.Code)
			assert.Equal(t, tt.wantLocation, w.Header().Get("Location"))
			if tt.wantStatus != http.StatusOK {
				return
			}
			body := w.Body.String()
			assert.Contains(t, body, "Admin Space")
			if tt.wantStudentCard {
				assert.Contains(t, body, "Student Space")
			} else {
				assert.NotContains(t, body, "Student Space")
				assert.NotContains(t, body, "/student/login")
			}
		})
	}
}

func TestServeUI_UnknownPathIsNotFound(t *testing.T) {
	t.Parallel()
	for _, mode := range []Mode{ModeAll, ModeAdmin, ModeStudent} {
		h := NewHandler(NewJobManager(""), nil, nil, nil, nil, nil)
		h.SetMode(mode)
		w := httptest.NewRecorder()
		h.ServeUI(w, httptest.NewRequest(http.MethodGet, "/student/dashboard", nil))
		assert.Equal(t, http.StatusNotFound, w.Code, "mode %s", mode)
	}
}
