package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkspaceNotifications_ResourceLink(t *testing.T) {
	t.Parallel()

	const a, b = "https://slides.example.com/a", "https://slides.example.com/b"
	tests := []struct {
		name      string
		templates []WorkspaceTemplate
		closed    map[string]bool
		query     string
		expected  string
	}{
		{"template param match", []WorkspaceTemplate{{Name: "go", ResourceLink: a}, {Name: "java", ResourceLink: b}}, nil, "&template=java", b},
		{"no param, single link", []WorkspaceTemplate{{Name: "go", ResourceLink: a}, {Name: "java"}}, nil, "", a},
		{"no param, same link twice", []WorkspaceTemplate{{Name: "go", ResourceLink: a}, {Name: "java", ResourceLink: a}}, nil, "", a},
		{"no param, two different links", []WorkspaceTemplate{{Name: "go", ResourceLink: a}, {Name: "java", ResourceLink: b}}, nil, "", ""},
		{"unknown template falls back to the single link", []WorkspaceTemplate{{Name: "go", ResourceLink: a}}, nil, "&template=nope", a},
		{"unknown template, two links", []WorkspaceTemplate{{Name: "go", ResourceLink: a}, {Name: "java", ResourceLink: b}}, nil, "&template=nope", ""},
		{"known template without link yields none", []WorkspaceTemplate{{Name: "go"}, {Name: "java", ResourceLink: b}}, nil, "&template=go", ""},
		{"invalid stored link is filtered", []WorkspaceTemplate{{Name: "go", ResourceLink: "javascript:alert(1)"}}, nil, "&template=go", ""},
		{"invalid stored link filtered without param", []WorkspaceTemplate{{Name: "go", ResourceLink: `http://a"x.com`}}, nil, "", ""},
		{"no templates", nil, nil, "", ""},
		{"closed template ignored by fallback", []WorkspaceTemplate{{Name: "go", ResourceLink: a}, {Name: "java", ResourceLink: b}}, map[string]bool{"java": true}, "", a},
		{"only closed template has a link, no fallback", []WorkspaceTemplate{{Name: "go"}, {Name: "java", ResourceLink: b}}, map[string]bool{"java": true}, "&template=nope", ""},
		{"closed template still matches by name", []WorkspaceTemplate{{Name: "go", ResourceLink: a}, {Name: "java", ResourceLink: b}}, map[string]bool{"java": true}, "&template=java", b},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h, labID := newNotifyTestHandler(t, "alice@example.com")
			job, ok := h.jobManager.GetJob(labID)
			require.True(t, ok)
			secret, _, err := ensureNotifySecret(job)
			require.NoError(t, err)
			job.mu.Lock()
			job.Config.WorkspaceTemplates = tt.templates
			job.Config.DisabledTemplates = tt.closed
			job.mu.Unlock()

			target := fmt.Sprintf("/api/workspace/notifications?lab_id=%s&token=%s%s",
				url.QueryEscape(labID), url.QueryEscape(notifyToken(secret, labID, "alice@example.com")), tt.query)
			rec := httptest.NewRecorder()
			h.WorkspaceNotifications(rec, httptest.NewRequest(http.MethodGet, target, nil))
			require.Equal(t, http.StatusOK, rec.Code)

			var raw map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw))
			assert.Contains(t, raw, "notifications")
			var link string
			if v, present := raw["resource_link"]; present {
				require.NoError(t, json.Unmarshal(v, &link))
			} else {
				assert.Empty(t, tt.expected, "resource_link is omitted only when there is none")
			}
			assert.Equal(t, tt.expected, link)
		})
	}
}
