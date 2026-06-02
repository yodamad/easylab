package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandler_GetOVHRegions_WrongMethod(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("POST", "/api/ovh/regions", nil)
	w := httptest.NewRecorder()
	h.GetOVHRegions(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GetOVHRegions wrong method = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandler_GetOVHRegions_NoCredentials(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("GET", "/api/ovh/regions", nil)
	w := httptest.NewRecorder()
	h.GetOVHRegions(w, req)
	if !strings.Contains(w.Body.String(), "Failed to load") {
		t.Error("GetOVHRegions no-creds should return error option")
	}
}

func TestHandler_GetOVHRegions_WithCachedRegions(t *testing.T) {
	cm := NewCredentialsManager()
	opts := NewOVHOptionsManager("", cm)
	opts.mu.Lock()
	opts.cachedRegions = map[string]bool{"GRA7": true, "DE1": true}
	opts.mu.Unlock()

	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, cm, opts, nil, nil)
	req := httptest.NewRequest("GET", "/api/ovh/regions", nil)
	w := httptest.NewRecorder()
	h.GetOVHRegions(w, req)
	body := w.Body.String()
	if !strings.Contains(body, "GRA7") && !strings.Contains(body, "DE1") {
		t.Error("GetOVHRegions cached should return cached regions")
	}
}

func TestHandler_GetOVHFlavors_WrongMethod(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("POST", "/api/ovh/flavors", nil)
	w := httptest.NewRecorder()
	h.GetOVHFlavors(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GetOVHFlavors wrong method = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandler_GetOVHFlavors_NoCredentials(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("GET", "/api/ovh/flavors?region=GRA7", nil)
	w := httptest.NewRecorder()
	h.GetOVHFlavors(w, req)
	if !strings.Contains(w.Body.String(), "Failed to load") {
		t.Error("GetOVHFlavors no-creds should return error option")
	}
}

func TestEscapeHTML(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"no special chars", "hello world", "hello world"},
		{"ampersand", "a & b", "a &amp; b"},
		{"less than", "<script>", "&lt;script&gt;"},
		{"greater than", "a > b", "a &gt; b"},
		{"double quote", `say "hi"`, "say &#34;hi&#34;"},
		{"single quote", "it's fine", "it&#39;s fine"},
		{"empty", "", ""},
		{"mixed", `<b>"hello" & 'world'</b>`, "&lt;b&gt;&#34;hello&#34; &amp; &#39;world&#39;&lt;/b&gt;"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := escapeHTML(tt.input); got != tt.want {
				t.Errorf("escapeHTML(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
