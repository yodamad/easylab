package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"easylab/internal/providers/workspace"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// postRemoveTemplate drives RemoveTemplateFromLab for a template on a lab.
func postRemoveTemplate(h *Handler, labID, name string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/labs/"+labID+"/templates/"+name+"/remove", nil)
	rec := httptest.NewRecorder()
	h.RemoveTemplateFromLab(rec, req)
	return rec
}

// removeTemplateResponse mirrors RemoveTemplateFromLab's JSON body.
type removeTemplateResponse struct {
	Status        string `json:"status"`
	Template      string `json:"template"`
	Deleted       int    `json:"deleted"`
	Failed        int    `json:"failed"`
	CleanupFailed bool   `json:"cleanup_failed"`
}

func decodeRemoveTemplate(t *testing.T, rec *httptest.ResponseRecorder) removeTemplateResponse {
	t.Helper()
	var resp removeTemplateResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	return resp
}

func TestRemoveTemplateFromLab_RemovesTemplateAndItsWorkspaces(t *testing.T) {
	h, jm := newUploadTestHandler(t)
	fb := &fakeBackend{
		reachable: true,
		workspaces: []workspace.Workspace{
			{ID: "ws-alice", Name: "ws-alice", Owner: "alice", OwnerEmail: "alice@example.com", Template: "go"},
			{ID: "ws-bob", Name: "ws-bob", Owner: "bob", Template: "go"},
			{ID: "ws-carol", Name: "ws-carol", Owner: "carol", Template: "python"},
			{ID: "ws-legacy", Name: "ws-legacy", Owner: "dave"}, // unattributed
		},
	}
	useFakeBackend(h, fb)
	labID := completedLab(t, jm, WorkspaceTemplate{Name: "go"}, WorkspaceTemplate{Name: "python"})
	h.updateJobConfig(labID, func(c *LabConfig) {
		c.BakedImages = map[string]BakedImage{
			"go":     {Image: "registry/go:latest", At: time.Now()},
			"python": {Image: "registry/python:latest", At: time.Now()},
		}
	})

	rec := postRemoveTemplate(h, labID, "go")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	resp := decodeRemoveTemplate(t, rec)
	assert.Equal(t, "go", resp.Template)
	assert.Equal(t, 2, resp.Deleted)
	assert.Zero(t, resp.Failed)
	assert.False(t, resp.CleanupFailed)

	assert.Equal(t, []string{"python"}, labTemplateNames(t, jm, labID))

	fb.callsMu.Lock()
	deleted := append([]string(nil), fb.DeleteCalls...)
	fb.callsMu.Unlock()
	sort.Strings(deleted)
	assert.Equal(t, []string{"ws-alice", "ws-bob"}, deleted, "only the removed template's workspaces are deleted")

	job, _ := jm.GetJob(labID)
	job.mu.RLock()
	defer job.mu.RUnlock()
	assert.NotContains(t, job.Config.BakedImages, "go", "the removed template's baked image is forgotten")
	assert.Contains(t, job.Config.BakedImages, "python")

	require.Len(t, job.WorkspaceEvents, 2)
	owners := []string{}
	for _, e := range job.WorkspaceEvents {
		assert.Equal(t, WorkspaceEventDeleted, e.Action)
		assert.Equal(t, "go", e.Template)
		owners = append(owners, e.Owner)
	}
	sort.Strings(owners)
	assert.Equal(t, []string{"alice@example.com", "bob"}, owners)
}

func TestRemoveTemplateFromLab_NoWorkspaces(t *testing.T) {
	h, jm := newUploadTestHandler(t)
	fb := &fakeBackend{reachable: true}
	useFakeBackend(h, fb)
	labID := completedLab(t, jm, WorkspaceTemplate{Name: "go"}, WorkspaceTemplate{Name: "python"})

	rec := postRemoveTemplate(h, labID, "python")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Zero(t, decodeRemoveTemplate(t, rec).Deleted)
	assert.Equal(t, []string{"go"}, labTemplateNames(t, jm, labID))
	assert.Empty(t, fb.DeleteCalls)
}

func TestRemoveTemplateFromLab_Rejections(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		method    string
		templates []WorkspaceTemplate
		completed bool
		labID     string // overrides the created lab's ID when set
		remove    string
		wantCode  int
	}{
		{
			name:      "last template",
			method:    http.MethodPost,
			templates: []WorkspaceTemplate{{Name: "go"}},
			completed: true,
			remove:    "go",
			wantCode:  http.StatusConflict,
		},
		{
			name:      "unknown template",
			method:    http.MethodPost,
			templates: []WorkspaceTemplate{{Name: "go"}, {Name: "python"}},
			completed: true,
			remove:    "rust",
			wantCode:  http.StatusNotFound,
		},
		{
			name:      "unknown lab",
			method:    http.MethodPost,
			templates: []WorkspaceTemplate{{Name: "go"}, {Name: "python"}},
			completed: true,
			labID:     "no-such-lab",
			remove:    "go",
			wantCode:  http.StatusNotFound,
		},
		{
			name:      "lab not completed",
			method:    http.MethodPost,
			templates: []WorkspaceTemplate{{Name: "go"}, {Name: "python"}},
			completed: false,
			remove:    "go",
			wantCode:  http.StatusConflict,
		},
		{
			name:      "wrong method",
			method:    http.MethodGet,
			templates: []WorkspaceTemplate{{Name: "go"}, {Name: "python"}},
			completed: true,
			remove:    "go",
			wantCode:  http.StatusMethodNotAllowed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h, jm := newUploadTestHandler(t)
			fb := &fakeBackend{reachable: true, workspaces: []workspace.Workspace{{ID: "ws-a", Template: "go"}}}
			useFakeBackend(h, fb)

			labID := jm.CreateJob(&LabConfig{StackName: "test", WorkspaceTemplates: tt.templates})
			if tt.completed {
				jm.UpdateJobStatus(labID, JobStatusCompleted)
			}
			target := labID
			if tt.labID != "" {
				target = tt.labID
			}

			req := httptest.NewRequest(tt.method, "/api/labs/"+target+"/templates/"+tt.remove+"/remove", nil)
			rec := httptest.NewRecorder()
			h.RemoveTemplateFromLab(rec, req)

			assert.Equal(t, tt.wantCode, rec.Code)
			assert.Len(t, labTemplateNames(t, jm, labID), len(tt.templates), "a rejected removal leaves the templates alone")
			assert.Empty(t, fb.DeleteCalls, "a rejected removal deletes no workspace")
		})
	}
}

func TestRemoveTemplateFromLab_WorkspaceDeleteFails(t *testing.T) {
	h, jm := newUploadTestHandler(t)
	useFakeBackend(h, &fakeBackend{
		reachable:  true,
		workspaces: []workspace.Workspace{{ID: "ws-alice", Name: "ws-alice", Template: "go"}},
		deleteErr:  assert.AnError,
	})
	labID := completedLab(t, jm, WorkspaceTemplate{Name: "go"}, WorkspaceTemplate{Name: "python"})

	rec := postRemoveTemplate(h, labID, "go")
	require.Equal(t, http.StatusOK, rec.Code)

	resp := decodeRemoveTemplate(t, rec)
	assert.Zero(t, resp.Deleted)
	assert.Equal(t, 1, resp.Failed)
	assert.NotContains(t, rec.Body.String(), assert.AnError.Error(), "backend errors stay server-side")
	assert.Equal(t, []string{"python"}, labTemplateNames(t, jm, labID), "the template is removed even when its workspaces aren't")
}

func TestRemoveTemplateFromLab_ClusterUnreachable(t *testing.T) {
	h, jm := newUploadTestHandler(t)
	useFakeBackend(h, &fakeBackend{listErr: assert.AnError})
	labID := completedLab(t, jm, WorkspaceTemplate{Name: "go"}, WorkspaceTemplate{Name: "python"})

	rec := postRemoveTemplate(h, labID, "go")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, decodeRemoveTemplate(t, rec).CleanupFailed)
	assert.Equal(t, []string{"python"}, labTemplateNames(t, jm, labID))
}

// The Remove control renders on every card of a multi-template lab, lists who
// loses a workspace, and is absent when the lab has a single template.
func TestLabDetail_RemoveTemplateControl(t *testing.T) {
	t.Chdir("../..") // getTemplate resolves web/ relative to the working directory

	tests := []struct {
		name         string
		templates    []WorkspaceTemplate
		wantRemove   bool
		wantContains []string
	}{
		{
			name:       "single template has no remove control",
			templates:  []WorkspaceTemplate{{Name: "go"}},
			wantRemove: false,
		},
		{
			name:       "several templates can each be removed",
			templates:  []WorkspaceTemplate{{Name: "go"}, {Name: "python"}},
			wantRemove: true,
			wantContains: []string{
				`data-template="go"`,
				`data-template="python"`,
				"<li>alice@example.com</li>",
				"Remove template and 1 workspace<",
				"No workspaces use <strong>python</strong>",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jm := NewJobManager("")
			h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
			useFakeBackend(h, &fakeBackend{
				reachable:  true,
				workspaces: []workspace.Workspace{{ID: "ws-alice", Name: "ws-alice", Owner: "alice", OwnerEmail: "alice@example.com", Template: "go"}},
			})
			labID := completedLabWithKubeconfig(jm, 0)
			h.updateJobConfig(labID, func(c *LabConfig) { c.WorkspaceTemplates = tt.templates })

			rec := httptest.NewRecorder()
			h.ServeLabDetail(rec, httptest.NewRequest(http.MethodGet, "/labs/"+labID, nil))
			require.Equal(t, http.StatusOK, rec.Code)
			body := rec.Body.String()
			require.Contains(t, body, "template-status-card", "the templates panel must render")

			assert.Equal(t, tt.wantRemove, strings.Contains(body, "template-remove-confirm"))
			for _, s := range tt.wantContains {
				assert.Contains(t, body, s)
			}
		})
	}
}

func TestRemoveTemplateFromLab_RecordsAudit(t *testing.T) {
	h, jm := newUploadTestHandler(t)
	as, err := NewAuditStore(t.TempDir())
	require.NoError(t, err)
	h.SetAuditStore(as)
	useFakeBackend(h, &fakeBackend{
		reachable:  true,
		workspaces: []workspace.Workspace{{ID: "ws-alice", Name: "ws-alice", Template: "go"}},
	})
	labID := completedLab(t, jm, WorkspaceTemplate{Name: "go"}, WorkspaceTemplate{Name: "python"})

	rec := postRemoveTemplate(h, labID, "go")
	require.Equal(t, http.StatusOK, rec.Code)

	removed := waitForAuditEntry(t, as, "lab.template_remove")
	require.Len(t, removed, 1)
	assert.Equal(t, labID, removed[0].LabID)
	assert.Equal(t, "go", removed[0].Detail)

	deleted := waitForAuditEntry(t, as, "workspace.delete")
	require.Len(t, deleted, 1)
	assert.Equal(t, "template removal: ws-alice", deleted[0].Detail)
}
