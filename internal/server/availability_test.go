package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"easylab/internal/providers/workspace"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// closableLab creates a completed lab with a kubeconfig and the given templates,
// i.e. one students can request workspaces on.
func closableLab(t *testing.T, jm *JobManager, templates ...WorkspaceTemplate) string {
	t.Helper()
	id := completedLabWithKubeconfig(jm, 0)
	job, ok := jm.GetJob(id)
	require.True(t, ok)
	job.mu.Lock()
	job.Config.WorkspaceTemplates = templates
	job.mu.Unlock()
	return id
}

// postAvailability drives one of the availability handlers with a urlencoded body.
func postAvailability(handler http.HandlerFunc, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

// asStudent attaches an authenticated student to a request.
func asStudent(req *http.Request, email string) *http.Request {
	return req.WithContext(context.WithValue(req.Context(), studentEmailContextKey, email))
}

// labAvailability returns a lab's closed state as currently stored.
func labAvailability(t *testing.T, jm *JobManager, id string) (bool, map[string]bool) {
	t.Helper()
	job, ok := jm.GetJob(id)
	require.True(t, ok)
	job.mu.RLock()
	defer job.mu.RUnlock()
	return job.Config.Disabled, job.Config.DisabledTemplates
}

func TestWithTemplateDisabled(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    map[string]bool
		template string
		value    bool
		expected map[string]bool
	}{
		{name: "close on a nil map", input: nil, template: "go", value: true, expected: map[string]bool{"go": true}},
		{name: "close a second template", input: map[string]bool{"go": true}, template: "python", value: true, expected: map[string]bool{"go": true, "python": true}},
		{name: "close twice", input: map[string]bool{"go": true}, template: "go", value: true, expected: map[string]bool{"go": true}},
		{name: "reopen one of two", input: map[string]bool{"go": true, "python": true}, template: "go", value: false, expected: map[string]bool{"python": true}},
		{name: "reopen the last one leaves nil", input: map[string]bool{"go": true}, template: "go", value: false, expected: nil},
		{name: "reopen one that was never closed", input: nil, template: "go", value: false, expected: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			before := make(map[string]bool, len(tt.input))
			for k, v := range tt.input {
				before[k] = v
			}

			assert.Equal(t, tt.expected, withTemplateDisabled(tt.input, tt.template, tt.value))
			if tt.input != nil {
				assert.Equal(t, before, tt.input, "the input map is never mutated")
			}
		})
	}
}

func TestLabConfig_IsTemplateDisabled(t *testing.T) {
	t.Parallel()

	var nilConfig *LabConfig
	assert.False(t, nilConfig.IsTemplateDisabled("go"))
	assert.False(t, (&LabConfig{}).IsTemplateDisabled("go"))

	config := &LabConfig{DisabledTemplates: map[string]bool{"go": true}}
	assert.True(t, config.IsTemplateDisabled("go"))
	assert.False(t, config.IsTemplateDisabled("python"))
}

func TestSetTemplateAvailability(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		method   string
		preset   map[string]bool
		labID    string // overrides the created lab's ID when set
		template string
		body     string
		wantCode int
		expected map[string]bool
	}{
		{name: "close", method: http.MethodPost, template: "go", body: "disabled=true", wantCode: http.StatusOK, expected: map[string]bool{"go": true}},
		{name: "close twice", method: http.MethodPost, preset: map[string]bool{"go": true}, template: "go", body: "disabled=true", wantCode: http.StatusOK, expected: map[string]bool{"go": true}},
		{name: "reopen", method: http.MethodPost, preset: map[string]bool{"go": true, "python": true}, template: "go", body: "disabled=false", wantCode: http.StatusOK, expected: map[string]bool{"python": true}},
		{name: "unknown template", method: http.MethodPost, template: "rust", body: "disabled=true", wantCode: http.StatusNotFound},
		{name: "unknown lab", method: http.MethodPost, labID: "no-such-lab", template: "go", body: "disabled=true", wantCode: http.StatusNotFound},
		{name: "not a boolean", method: http.MethodPost, template: "go", body: "disabled=maybe", wantCode: http.StatusBadRequest},
		{name: "missing value", method: http.MethodPost, template: "go", body: "", wantCode: http.StatusBadRequest},
		{name: "wrong method", method: http.MethodGet, template: "go", body: "disabled=true", wantCode: http.StatusMethodNotAllowed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h, jm := newUploadTestHandler(t)
			labID := completedLab(t, jm, WorkspaceTemplate{Name: "go"}, WorkspaceTemplate{Name: "python"})
			h.updateJobConfig(labID, func(c *LabConfig) { c.DisabledTemplates = tt.preset })
			target := labID
			if tt.labID != "" {
				target = tt.labID
			}

			rec := postAvailability(h.SetTemplateAvailability, tt.method, "/api/labs/"+target+"/templates/"+tt.template+"/availability", tt.body)
			require.Equal(t, tt.wantCode, rec.Code, rec.Body.String())

			_, disabled := labAvailability(t, jm, labID)
			if tt.wantCode != http.StatusOK {
				assert.Equal(t, tt.preset, disabled, "a rejected request changes nothing")
				return
			}
			assert.Equal(t, tt.expected, disabled)
			assert.Equal(t, []string{"go", "python"}, labTemplateNames(t, jm, labID), "closing a template never removes it")
		})
	}
}

// A lab with no configured template still offers the implicit "default" one,
// and it can be closed like any other.
func TestSetTemplateAvailability_ImplicitDefaultTemplate(t *testing.T) {
	t.Parallel()
	h, jm := newUploadTestHandler(t)
	labID := completedLab(t, jm)

	rec := postAvailability(h.SetTemplateAvailability, http.MethodPost, "/api/labs/"+labID+"/templates/default/availability", "disabled=true")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	_, disabled := labAvailability(t, jm, labID)
	assert.Equal(t, map[string]bool{"default": true}, disabled)
}

func TestSetLabAvailability(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		method   string
		preset   bool
		path     string // "%s" is replaced by the lab ID
		body     string
		wantCode int
		expected bool
	}{
		{name: "close", method: http.MethodPost, path: "/api/labs/%s/availability", body: "disabled=true", wantCode: http.StatusOK, expected: true},
		{name: "close twice", method: http.MethodPost, preset: true, path: "/api/labs/%s/availability", body: "disabled=true", wantCode: http.StatusOK, expected: true},
		{name: "reopen", method: http.MethodPost, preset: true, path: "/api/labs/%s/availability", body: "disabled=false", wantCode: http.StatusOK, expected: false},
		{name: "legacy jobs prefix", method: http.MethodPost, path: "/api/jobs/%s/availability", body: "disabled=true", wantCode: http.StatusOK, expected: true},
		{name: "unknown lab", method: http.MethodPost, path: "/api/labs/no-such-lab/availability", body: "disabled=true", wantCode: http.StatusNotFound},
		{name: "not a boolean", method: http.MethodPost, path: "/api/labs/%s/availability", body: "disabled=maybe", wantCode: http.StatusBadRequest},
		{name: "missing value", method: http.MethodPost, path: "/api/labs/%s/availability", body: "", wantCode: http.StatusBadRequest},
		{name: "wrong method", method: http.MethodGet, path: "/api/labs/%s/availability", body: "disabled=true", wantCode: http.StatusMethodNotAllowed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h, jm := newUploadTestHandler(t)
			labID := completedLab(t, jm, WorkspaceTemplate{Name: "go"})
			h.updateJobConfig(labID, func(c *LabConfig) { c.Disabled = tt.preset })

			rec := postAvailability(h.SetLabAvailability, tt.method, strings.Replace(tt.path, "%s", labID, 1), tt.body)
			require.Equal(t, tt.wantCode, rec.Code, rec.Body.String())

			disabled, _ := labAvailability(t, jm, labID)
			if tt.wantCode != http.StatusOK {
				assert.Equal(t, tt.preset, disabled, "a rejected request changes nothing")
				return
			}
			assert.Equal(t, tt.expected, disabled)

			var body map[string]interface{}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			assert.Equal(t, true, body["success"])
			assert.Equal(t, tt.expected, body["disabled"])
		})
	}
}

func TestSetAvailability_RecordsAudit(t *testing.T) {
	tests := []struct {
		name       string
		template   string // "" targets the lab
		body       string
		wantAction string
		wantDetail string
	}{
		{name: "close a lab", body: "disabled=true", wantAction: "lab.disable"},
		{name: "reopen a lab", body: "disabled=false", wantAction: "lab.enable"},
		{name: "close a template", template: "go", body: "disabled=true", wantAction: "lab.template_disable", wantDetail: "go"},
		{name: "reopen a template", template: "go", body: "disabled=false", wantAction: "lab.template_enable", wantDetail: "go"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, jm := newUploadTestHandler(t)
			as, err := NewAuditStore(t.TempDir())
			require.NoError(t, err)
			h.SetAuditStore(as)
			labID := completedLab(t, jm, WorkspaceTemplate{Name: "go"})

			var rec *httptest.ResponseRecorder
			if tt.template == "" {
				rec = postAvailability(h.SetLabAvailability, http.MethodPost, "/api/labs/"+labID+"/availability", tt.body)
			} else {
				rec = postAvailability(h.SetTemplateAvailability, http.MethodPost, "/api/labs/"+labID+"/templates/"+tt.template+"/availability", tt.body)
			}
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			entries := waitForAuditEntry(t, as, tt.wantAction)
			require.Len(t, entries, 1)
			assert.Equal(t, "admin", entries[0].Role)
			assert.Equal(t, labID, entries[0].LabID)
			assert.Equal(t, tt.wantDetail, entries[0].Detail)
		})
	}
}

// The handlers save off the response's critical path; the change must still
// land on disk so a closed lab stays closed across a restart.
func TestSetAvailability_PersistsAsynchronously(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	jm := NewJobManager(dataDir)
	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	labID := completedLab(t, jm, WorkspaceTemplate{Name: "go"}, WorkspaceTemplate{Name: "python"})

	rec := postAvailability(h.SetLabAvailability, http.MethodPost, "/api/labs/"+labID+"/availability", "disabled=true")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = postAvailability(h.SetTemplateAvailability, http.MethodPost, "/api/labs/"+labID+"/templates/go/availability", "disabled=true")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	jobFile := filepath.Join(dataDir, "jobs", labID+".json")
	deadline := time.Now().Add(2 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(jobFile)
		if err == nil {
			var persisted struct {
				Config struct {
					Disabled          bool            `json:"disabled"`
					DisabledTemplates map[string]bool `json:"disabled_templates"`
				} `json:"config"`
			}
			if json.Unmarshal(data, &persisted) == nil && persisted.Config.Disabled && persisted.Config.DisabledTemplates["go"] {
				return // both async saves landed
			}
		} else {
			lastErr = err
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job file never reflected the availability change within the deadline (last read error: %v)", lastErr)
}

func TestRemoveTemplateFromLab_ForgetsClosedState(t *testing.T) {
	h, jm := newUploadTestHandler(t)
	useFakeBackend(h, &fakeBackend{reachable: true})
	labID := completedLab(t, jm, WorkspaceTemplate{Name: "go"}, WorkspaceTemplate{Name: "python"})
	h.updateJobConfig(labID, func(c *LabConfig) {
		c.DisabledTemplates = map[string]bool{"go": true, "python": true}
	})

	rec := postRemoveTemplate(h, labID, "go")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	_, disabled := labAvailability(t, jm, labID)
	assert.Equal(t, map[string]bool{"python": true}, disabled, "a template re-added under the same name must not come back closed")
}

func TestListLabTemplates_ClosedTemplates(t *testing.T) {
	t.Parallel()

	alicePython := workspace.Workspace{ID: "ws-alice-python", Owner: "alice", Template: "python"}
	bobPython := workspace.Workspace{ID: "ws-bob-python", Owner: "bob", Template: "python"}
	aliceLegacy := workspace.Workspace{ID: "ws-alice", Owner: "alice"} // created before template attribution

	type option struct {
		Name   string `json:"name"`
		Closed bool   `json:"closed"`
	}

	tests := []struct {
		name              string
		labDisabled       bool
		disabledTemplates map[string]bool
		backend           *fakeBackend
		expected          []option
		wantListCalls     int
	}{
		{
			name:     "nothing closed asks the cluster nothing",
			backend:  &fakeBackend{workspaces: []workspace.Workspace{alicePython}},
			expected: []option{{Name: "go"}, {Name: "python"}},
		},
		{
			name:              "closed template is hidden from a student without a workspace",
			disabledTemplates: map[string]bool{"python": true},
			backend:           &fakeBackend{workspaces: []workspace.Workspace{bobPython}},
			expected:          []option{{Name: "go"}},
			wantListCalls:     1,
		},
		{
			name:              "closed template stays for its workspace owner",
			disabledTemplates: map[string]bool{"python": true},
			backend:           &fakeBackend{workspaces: []workspace.Workspace{alicePython, bobPython}},
			expected:          []option{{Name: "go"}, {Name: "python", Closed: true}},
			wantListCalls:     1,
		},
		{
			name:              "an unattributed workspace does not unlock a closed template",
			disabledTemplates: map[string]bool{"python": true},
			backend:           &fakeBackend{workspaces: []workspace.Workspace{aliceLegacy}},
			expected:          []option{{Name: "go"}},
			wantListCalls:     1,
		},
		{
			name:              "every template closed",
			disabledTemplates: map[string]bool{"go": true, "python": true},
			backend:           &fakeBackend{},
			expected:          []option{},
			wantListCalls:     1,
		},
		{
			name:          "closed lab only offers the templates the student has a workspace on",
			labDisabled:   true,
			backend:       &fakeBackend{workspaces: []workspace.Workspace{alicePython}},
			expected:      []option{{Name: "python", Closed: true}},
			wantListCalls: 1,
		},
		{
			name:              "unreachable cluster hides closed templates",
			disabledTemplates: map[string]bool{"python": true},
			backend:           &fakeBackend{listErr: assert.AnError},
			expected:          []option{{Name: "go"}},
			wantListCalls:     1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h, jm := newUploadTestHandler(t)
			useFakeBackend(h, tt.backend)
			labID := closableLab(t, jm, WorkspaceTemplate{Name: "go"}, WorkspaceTemplate{Name: "python"})
			h.updateJobConfig(labID, func(c *LabConfig) {
				c.Disabled = tt.labDisabled
				c.DisabledTemplates = tt.disabledTemplates
			})

			req := asStudent(httptest.NewRequest(http.MethodGet, "/api/student/labs/templates?lab_id="+labID, nil), "alice@example.com")
			rec := httptest.NewRecorder()
			h.ListLabTemplates(rec, req)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			var got []option
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
			assert.Equal(t, tt.expected, got)
			assert.NotContains(t, rec.Body.String(), assert.AnError.Error(), "backend errors stay server-side")
			assert.Equal(t, tt.wantListCalls, tt.backend.ListCalls)
		})
	}
}

func TestListLabs_ClosedLab(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		disabled      bool
		backend       *fakeBackend
		wantListed    bool
		wantListCalls int
	}{
		{
			name:       "open lab is listed without asking the cluster",
			backend:    &fakeBackend{},
			wantListed: true,
		},
		{
			name:          "closed lab is hidden from a student without a workspace",
			disabled:      true,
			backend:       &fakeBackend{workspaces: []workspace.Workspace{{ID: "ws-bob", Owner: "bob", Template: "go"}}},
			wantListCalls: 1,
		},
		{
			name:          "closed lab stays for a student with a workspace",
			disabled:      true,
			backend:       &fakeBackend{workspaces: []workspace.Workspace{{ID: "ws-alice", Owner: "alice", Template: "go"}}},
			wantListed:    true,
			wantListCalls: 1,
		},
		{
			name:          "unreachable cluster hides a closed lab",
			disabled:      true,
			backend:       &fakeBackend{listErr: assert.AnError},
			wantListCalls: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h, jm := newUploadTestHandler(t)
			useFakeBackend(h, tt.backend)
			labID := closableLab(t, jm, WorkspaceTemplate{Name: "go"})
			h.updateJobConfig(labID, func(c *LabConfig) { c.Disabled = tt.disabled })

			req := asStudent(httptest.NewRequest(http.MethodGet, "/api/student/labs", nil), "alice@example.com")
			rec := httptest.NewRecorder()
			h.ListLabs(rec, req)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			var labs []struct {
				ID     string `json:"id"`
				Config struct {
					Disabled bool `json:"disabled"`
				} `json:"config"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &labs))
			if !tt.wantListed {
				assert.Empty(t, labs)
			} else {
				require.Len(t, labs, 1)
				assert.Equal(t, labID, labs[0].ID)
				assert.Equal(t, tt.disabled, labs[0].Config.Disabled, "the picker reads this to mark the lab as closed")
			}
			assert.NotContains(t, rec.Body.String(), "fake-kubeconfig")
			assert.Equal(t, tt.wantListCalls, tt.backend.ListCalls)
		})
	}
}

func TestRequestWorkspace_Closed(t *testing.T) {
	t.Parallel()

	alicePython := workspace.Workspace{ID: "ws-alice-python", Name: "ws-alice-python", Owner: "alice", Template: "python"}

	tests := []struct {
		name              string
		labDisabled       bool
		disabledTemplates map[string]bool
		templateID        string
		backend           *fakeBackend
		wantTemplate      string // template handed to EnsureWorkspace; "" means it must not be called
		wantBody          string
	}{
		{
			name:              "new student is refused a closed template",
			disabledTemplates: map[string]bool{"python": true},
			templateID:        "python",
			backend:           &fakeBackend{},
			wantBody:          "This template is closed to new students.",
		},
		{
			name:              "owner gets their workspace back on a closed template",
			disabledTemplates: map[string]bool{"python": true},
			templateID:        "python",
			backend:           &fakeBackend{workspaces: []workspace.Workspace{alicePython}, getWS: &alicePython},
			wantTemplate:      "python",
		},
		{
			name:              "an open template is unaffected by a closed neighbour",
			disabledTemplates: map[string]bool{"python": true},
			templateID:        "go",
			backend:           &fakeBackend{},
			wantTemplate:      "go",
		},
		{
			name:              "no template named falls back to the first open one",
			disabledTemplates: map[string]bool{"go": true},
			backend:           &fakeBackend{},
			wantTemplate:      "python",
		},
		{
			name:         "new student is refused a closed lab",
			labDisabled:  true,
			templateID:   "go",
			backend:      &fakeBackend{},
			wantBody:     "This lab is closed to new students.",
			wantTemplate: "",
		},
		{
			name:         "owner gets their workspace back on a closed lab",
			labDisabled:  true,
			templateID:   "python",
			backend:      &fakeBackend{workspaces: []workspace.Workspace{alicePython}, getWS: &alicePython},
			wantTemplate: "python",
		},
		{
			name:        "owning one template of a closed lab does not open the others",
			labDisabled: true,
			templateID:  "go",
			backend:     &fakeBackend{workspaces: []workspace.Workspace{alicePython}},
			wantBody:    "This lab is closed to new students.",
		},
		{
			name:              "unreachable cluster refuses rather than creating",
			disabledTemplates: map[string]bool{"python": true},
			templateID:        "python",
			backend:           &fakeBackend{listErr: assert.AnError},
			wantBody:          "Unable to reach the lab cluster.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h, jm := newUploadTestHandler(t)
			useFakeBackend(h, tt.backend)
			labID := closableLab(t, jm, WorkspaceTemplate{Name: "go"}, WorkspaceTemplate{Name: "python"})
			h.updateJobConfig(labID, func(c *LabConfig) {
				c.Disabled = tt.labDisabled
				c.DisabledTemplates = tt.disabledTemplates
			})

			form := url.Values{"lab_id": {labID}}
			if tt.templateID != "" {
				form.Set("template_id", tt.templateID)
			}
			req := asStudent(postForm(t, "/api/student/workspace/request", form), "alice@example.com")
			rec := httptest.NewRecorder()
			h.RequestWorkspace(rec, req)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			if tt.wantBody != "" {
				assert.Contains(t, rec.Body.String(), tt.wantBody)
			}
			assert.NotContains(t, rec.Body.String(), assert.AnError.Error(), "backend errors stay server-side")

			if tt.wantTemplate == "" {
				assert.Empty(t, tt.backend.Ensured, "a refused request must not reach the cluster")
				return
			}
			require.Len(t, tt.backend.Ensured, 1)
			assert.Equal(t, tt.wantTemplate, tt.backend.Ensured[0].Template)
		})
	}
}

// Closing a lab and its template takes nothing away from a student who already
// has a workspace there: they can still open it and still delete it.
func TestClosedLab_ExistingWorkspaceStaysUsable(t *testing.T) {
	mine := workspace.Workspace{
		ID: "ws-student", Name: "ws-student", Owner: "student", Template: "go",
		OpenURL: "https://ws.example.com/", Token: "secret-token-123",
	}
	h, labID := openWorkspaceHandler(t, mine)
	fb := &fakeBackend{getWS: &mine, workspaces: []workspace.Workspace{mine}}
	useFakeBackend(h, fb)
	h.updateJobConfig(labID, func(c *LabConfig) {
		c.Disabled = true
		c.DisabledTemplates = map[string]bool{"go": true}
	})

	t.Run("open", func(t *testing.T) {
		req := asStudent(httptest.NewRequest(http.MethodGet, "/api/student/workspace/open?lab_id="+labID+"&workspace_name=ws-student", nil), "student@example.com")
		rec := httptest.NewRecorder()
		h.OpenWorkspace(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Contains(t, rec.Body.String(), `action="https://ws.example.com/login"`)
	})

	t.Run("delete", func(t *testing.T) {
		form := url.Values{"lab_id": {labID}, "workspace_name": {"ws-student"}}
		req := asStudent(postForm(t, "/api/student/workspace/delete", form), "student@example.com")
		rec := httptest.NewRecorder()
		h.DeleteStudentWorkspace(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Equal(t, []string{"ws-student"}, fb.DeleteCalls)
	})
}

func TestLabDetail_AvailabilityControls(t *testing.T) {
	t.Chdir("../..") // getTemplate resolves web/ relative to the working directory

	tests := []struct {
		name              string
		labDisabled       bool
		disabledTemplates map[string]bool
		wantContains      []string
		wantAbsent        []string
	}{
		{
			name: "everything open",
			wantContains: []string{
				"Open to students",
				"Close lab to new students",
				`data-url="/api/labs/LAB/templates/go/availability" data-disabled="true"`,
				`data-url="/api/labs/LAB/templates/python/availability" data-disabled="true"`,
				"2 configured<",
			},
			wantAbsent: []string{"lab-closed-badge", "template-closed-badge", "is-closed"},
		},
		{
			name:              "one template closed",
			disabledTemplates: map[string]bool{"go": true},
			wantContains: []string{
				"2 configured, 1 closed<",
				`class="template-status-card is-closed"`,
				"template-closed-badge",
				"1 existing workspace keeps running.",
				`data-url="/api/labs/LAB/templates/go/availability" data-disabled="false"`,
				`data-url="/api/labs/LAB/templates/python/availability" data-disabled="true"`,
				"template-remove-confirm", // a closed template can still be removed
			},
			wantAbsent: []string{"lab-closed-badge", "Reopen lab"},
		},
		{
			name:        "lab closed",
			labDisabled: true,
			wantContains: []string{
				"lab-closed-badge",
				`class="lab-access-strip is-closed"`,
				"Reopen lab",
				`data-url="/api/labs/LAB/availability" data-disabled="false"`,
			},
			wantAbsent: []string{"Close lab to new students", "template-closed-badge"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, jm := newUploadTestHandler(t)
			useFakeBackend(h, &fakeBackend{
				reachable:  true,
				workspaces: []workspace.Workspace{{ID: "ws-alice", Name: "ws-alice", Owner: "alice", Template: "go"}},
			})
			labID := closableLab(t, jm, WorkspaceTemplate{Name: "go"}, WorkspaceTemplate{Name: "python"})
			h.updateJobConfig(labID, func(c *LabConfig) {
				c.Disabled = tt.labDisabled
				c.DisabledTemplates = tt.disabledTemplates
			})

			rec := httptest.NewRecorder()
			h.ServeLabDetail(rec, httptest.NewRequest(http.MethodGet, "/labs/"+labID, nil))
			require.Equal(t, http.StatusOK, rec.Code)
			body := rec.Body.String()
			require.Contains(t, body, "template-status-card", "the templates panel must render")

			for _, s := range tt.wantContains {
				assert.Contains(t, body, strings.ReplaceAll(s, "LAB", labID))
			}
			for _, s := range tt.wantAbsent {
				assert.NotContains(t, body, s)
			}
		})
	}
}

func TestLabsList_ClosedBadge(t *testing.T) {
	t.Chdir("../..") // getTemplate resolves web/ relative to the working directory

	tests := []struct {
		name      string
		disabled  bool
		wantBadge bool
	}{
		{name: "open lab has no badge", disabled: false, wantBadge: false},
		{name: "closed lab is badged", disabled: true, wantBadge: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, jm := newUploadTestHandler(t)
			labID := completedLab(t, jm, WorkspaceTemplate{Name: "go"})
			h.updateJobConfig(labID, func(c *LabConfig) { c.Disabled = tt.disabled })

			rec := httptest.NewRecorder()
			h.ServeLabsList(rec, httptest.NewRequest(http.MethodGet, "/labs", nil))
			require.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, tt.wantBadge, strings.Contains(rec.Body.String(), "lab-closed-badge"))
		})
	}
}
