package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandler_GetCoderVersions_WrongMethod(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("POST", "/api/coder/versions", nil)
	w := httptest.NewRecorder()
	h.GetCoderVersions(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GetCoderVersions wrong method = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestWriteVersionsError(t *testing.T) {
	w := httptest.NewRecorder()
	writeVersionsError(w)
	body := w.Body.String()
	if !strings.Contains(body, "custom") {
		t.Error("writeVersionsError should contain 'custom' option")
	}
}
