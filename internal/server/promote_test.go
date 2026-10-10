package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"easylab/internal/providers/workspace"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// postPromote drives PromoteTemplate for a template of the lab sourceID.
func postPromote(h *Handler, sourceID, name string, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/labs/"+sourceID+"/templates/"+name+"/promote", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.PromoteTemplate(rec, req)
	return rec
}

func TestPromoteTemplate_CopiesTheDefinitionOnly(t *testing.T) {
	h, jm := newUploadTestHandler(t)
	source := completedLab(t, jm, WorkspaceTemplate{
		Name:       "go",
		Image:      "golang:1.26",
		Env:        map[string]string{"FOO": "bar"},
		Extensions: []string{"golang.go"},
		Sidecars:   []WorkspaceSidecar{{Name: "db", Image: "postgres:16"}},
	})
	h.updateJobConfig(source, func(c *LabConfig) {
		c.BakedImages = map[string]BakedImage{"go": {Image: "registry/go@sha256:abc", At: time.Now()}}
		c.DisabledTemplates = map[string]bool{"go": true}
	})
	target := completedLab(t, jm, WorkspaceTemplate{Name: "python"})

	rec := postPromote(h, source, "go", url.Values{"target_lab_id": {target}})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := rec.Body.String()
	assert.Contains(t, body, "success-message")
	assert.Contains(t, body, `href="/labs/`+target+`#workspaces"`)
	assert.NotContains(t, body, "warning-message", "a template naming no credential has nothing to warn about")

	assert.Equal(t, []string{"python", "go"}, labTemplateNames(t, jm, target))
	assert.Equal(t, []string{"go"}, labTemplateNames(t, jm, source), "the source lab keeps its template")

	targetJob, _ := jm.GetJob(target)
	targetJob.mu.Lock()
	got := targetJob.Config.WorkspaceTemplates[1]
	assert.Equal(t, "golang:1.26", got.Image)
	assert.Equal(t, []string{"golang.go"}, got.Extensions)
	assert.Equal(t, "postgres:16", got.Sidecars[0].Image)
	assert.NotContains(t, targetJob.Config.BakedImages, "go", "a baked image is per lab")
	assert.False(t, targetJob.Config.IsTemplateDisabled("go"), "the copy arrives open to students")
	// Changing the copy must not reach the lab it came from.
	got.Env["FOO"] = "changed"
	got.Extensions[0] = "changed"
	targetJob.mu.Unlock()

	sourceJob, _ := jm.GetJob(source)
	sourceJob.mu.RLock()
	defer sourceJob.mu.RUnlock()
	assert.Equal(t, "bar", sourceJob.Config.WorkspaceTemplates[0].Env["FOO"])
	assert.Equal(t, []string{"golang.go"}, sourceJob.Config.WorkspaceTemplates[0].Extensions)
}

func TestPromoteTemplate_Rename(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		targetName string
		expected   []string // templates on the target afterwards
		wantOK     bool
		wantBody   string
	}{
		{name: "same name clashes", targetName: "", expected: []string{"go"}, wantBody: "already has a template named"},
		{name: "explicit same name clashes", targetName: "go", expected: []string{"go"}, wantBody: "already has a template named"},
		{name: "renamed", targetName: "go-v2", expected: []string{"go", "go-v2"}, wantOK: true, wantBody: "as <strong>go-v2</strong>"},
		{name: "name is trimmed", targetName: "  go-v2  ", expected: []string{"go", "go-v2"}, wantOK: true, wantBody: "as <strong>go-v2</strong>"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h, jm := newUploadTestHandler(t)
			source := completedLab(t, jm, WorkspaceTemplate{Name: "go", Image: "golang:1.26"})
			target := completedLab(t, jm, WorkspaceTemplate{Name: "go", Image: "golang:1.25"})

			rec := postPromote(h, source, "go", url.Values{"target_lab_id": {target}, "target_name": {tt.targetName}})
			require.Equal(t, http.StatusOK, rec.Code)
			body := rec.Body.String()

			assert.Equal(t, tt.wantOK, strings.Contains(body, "success-message"), body)
			assert.Equal(t, !tt.wantOK, strings.Contains(body, "error-message"), body)
			assert.Contains(t, body, tt.wantBody)
			assert.Equal(t, tt.expected, labTemplateNames(t, jm, target))

			targetJob, _ := jm.GetJob(target)
			targetJob.mu.RLock()
			defer targetJob.mu.RUnlock()
			assert.Equal(t, "golang:1.25", targetJob.Config.WorkspaceTemplates[0].Image, "the target's own template is never overwritten")
		})
	}
}

func TestPromoteTemplate_Rejections(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		method          string
		template        string
		sourceID        string // overrides the source lab's ID when set
		targetID        string // "self" targets the source lab; "" sends none
		targetCompleted bool
		wantCode        int
		wantBody        string
	}{
		{name: "wrong method", method: http.MethodGet, template: "go", targetCompleted: true, wantCode: http.StatusMethodNotAllowed},
		{name: "unknown source lab", method: http.MethodPost, template: "go", sourceID: "no-such-lab", targetCompleted: true, wantCode: http.StatusNotFound},
		{name: "unknown template", method: http.MethodPost, template: "rust", targetCompleted: true, wantCode: http.StatusNotFound},
		{name: "no target", method: http.MethodPost, template: "go", targetID: "", targetCompleted: true, wantCode: http.StatusOK, wantBody: "Choose the lab"},
		{name: "target is the source", method: http.MethodPost, template: "go", targetID: "self", targetCompleted: true, wantCode: http.StatusOK, wantBody: "Choose another lab"},
		{name: "unknown target", method: http.MethodPost, template: "go", targetID: "no-such-lab", targetCompleted: true, wantCode: http.StatusOK, wantBody: "no longer exists"},
		{name: "target not ready", method: http.MethodPost, template: "go", targetCompleted: false, wantCode: http.StatusOK, wantBody: "not ready yet"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h, jm := newUploadTestHandler(t)
			source := completedLab(t, jm, WorkspaceTemplate{Name: "go"})
			target := jm.CreateJob(&LabConfig{StackName: "target", WorkspaceTemplates: []WorkspaceTemplate{{Name: "python"}}})
			if tt.targetCompleted {
				jm.UpdateJobStatus(target, JobStatusCompleted)
			}

			form := url.Values{}
			switch {
			case tt.targetID == "self":
				form.Set("target_lab_id", source)
			case tt.targetID != "":
				form.Set("target_lab_id", tt.targetID)
			case tt.name != "no target":
				form.Set("target_lab_id", target)
			}
			from := source
			if tt.sourceID != "" {
				from = tt.sourceID
			}

			req := httptest.NewRequest(tt.method, "/api/labs/"+from+"/templates/"+tt.template+"/promote", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rec := httptest.NewRecorder()
			h.PromoteTemplate(rec, req)

			assert.Equal(t, tt.wantCode, rec.Code)
			assert.NotContains(t, rec.Body.String(), "success-message")
			if tt.wantBody != "" {
				assert.Contains(t, rec.Body.String(), "error-message")
				assert.Contains(t, rec.Body.String(), tt.wantBody)
			}
			assert.Equal(t, []string{"python"}, labTemplateNames(t, jm, target), "a rejected promotion leaves the target alone")
			assert.Equal(t, []string{"go"}, labTemplateNames(t, jm, source))
		})
	}
}

// The source lab is only read, so a template can be promoted out of a lab that
// is no longer up.
func TestPromoteTemplate_SourceNeedNotBeCompleted(t *testing.T) {
	h, jm := newUploadTestHandler(t)
	source := jm.CreateJob(&LabConfig{StackName: "old", WorkspaceTemplates: []WorkspaceTemplate{{Name: "go"}}})
	jm.UpdateJobStatus(source, JobStatusDestroyed)
	target := completedLab(t, jm, WorkspaceTemplate{Name: "python"})

	rec := postPromote(h, source, "go", url.Values{"target_lab_id": {target}})
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "success-message")
	assert.Equal(t, []string{"python", "go"}, labTemplateNames(t, jm, target))
}

// A promoted devcontainer template is baked on the target when asked to — and
// only there: the bake is the target lab's, under the name it has on that lab.
func TestPromoteTemplate_Bake(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		template WorkspaceTemplate
		bake     string
		expected []string
	}{
		{name: "asked for", template: bakeableTemplate("go"), bake: "true", expected: []string{"go-v2"}},
		{name: "not asked for", template: bakeableTemplate("go"), bake: "", expected: nil},
		{name: "a plain template has nothing to bake", template: WorkspaceTemplate{Name: "go"}, bake: "true", expected: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h, jm := newUploadTestHandler(t)
			cb := useCapturingBakeBackend(h)
			source := bakeLab(t, jm, tt.template)
			target := bakeLab(t, jm, WorkspaceTemplate{Name: "existing"})

			form := url.Values{"target_lab_id": {target}, "target_name": {"go-v2"}}
			if tt.bake != "" {
				form.Set("bake", tt.bake)
			}
			rec := postPromote(h, source, "go", form)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(t, len(tt.expected) > 0, strings.Contains(rec.Body.String(), "Baking its image"))

			// The bake itself starts off the response's path.
			if len(tt.expected) > 0 {
				assert.Contains(t, h.renderBakeStatus(target, "go-v2"), "bake-status", "the target's card must open on a running bake")
				require.Eventually(t, func() bool { return len(cb.bakedTemplates()) == len(tt.expected) }, 2*time.Second, 10*time.Millisecond)
			} else {
				time.Sleep(50 * time.Millisecond)
			}
			assert.ElementsMatch(t, tt.expected, cb.bakedTemplates())

			cb.mu.Lock()
			defer cb.mu.Unlock()
			for _, req := range cb.requests {
				assert.Equal(t, target, req.LabID)
			}
		})
	}
}

// Credentials never travel with a template: the answer names the ones the target
// lab's cluster still needs.
func TestPromoteTemplate_CredentialWarning(t *testing.T) {
	private := WorkspaceTemplate{
		Name:             "private",
		GitRepo:          "https://gitlab.com/org/private.git",
		GitAuthSecret:    "gitcred",
		ImagePullSecrets: []string{"regcred"},
	}

	tests := []struct {
		name        string
		backend     workspace.Backend
		wantWarning bool
		wantNames   []string
		wantAbsent  []string
	}{
		{
			name:        "target has none of them",
			backend:     &fakeSecretBackend{},
			wantWarning: true,
			wantNames:   []string{"The target lab is missing", "gitcred", "regcred"},
		},
		{
			name:        "target has one",
			backend:     &fakeSecretBackend{secrets: []workspace.AuthSecret{{Name: "gitcred", Type: workspace.AuthSecretGit}}},
			wantWarning: true,
			wantNames:   []string{"regcred"},
			wantAbsent:  []string{"gitcred"},
		},
		{
			name: "target has them all",
			backend: &fakeSecretBackend{secrets: []workspace.AuthSecret{
				{Name: "gitcred", Type: workspace.AuthSecretGit},
				{Name: "regcred", Type: workspace.AuthSecretRegistry},
			}},
			wantWarning: false,
		},
		{
			name:        "the target's cluster cannot be asked",
			backend:     &fakeSecretBackend{listErrSecrets: assert.AnError},
			wantWarning: true,
			wantNames:   []string{"This template references", "gitcred", "regcred"},
			wantAbsent:  []string{assert.AnError.Error()},
		},
		{
			name:        "the backend cannot manage credentials",
			backend:     &fakeBackend{reachable: true},
			wantWarning: true,
			wantNames:   []string{"This template references", "gitcred", "regcred"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, jm := newUploadTestHandler(t)
			h.newWorkspaceBackend = func(_, _ string) (workspace.Backend, error) { return tt.backend, nil }
			source := completedLab(t, jm, private)
			target := completedLabWithKubeconfig(jm, 0)

			rec := postPromote(h, source, "private", url.Values{"target_lab_id": {target}})
			require.Equal(t, http.StatusOK, rec.Code)
			body := rec.Body.String()
			require.Contains(t, body, "success-message", "a missing credential never blocks the promotion")

			assert.Equal(t, tt.wantWarning, strings.Contains(body, "warning-message"), body)
			for _, s := range tt.wantNames {
				assert.Contains(t, body, s)
			}
			for _, s := range tt.wantAbsent {
				assert.NotContains(t, body, s)
			}
		})
	}
}

func TestPromoteTemplate_EscapesNames(t *testing.T) {
	h, jm := newUploadTestHandler(t)
	source := completedLab(t, jm, WorkspaceTemplate{Name: "go", ImagePullSecrets: []string{"<b>cred</b>"}})
	target := jm.CreateJob(&LabConfig{StackName: "<script>alert(1)</script>"})
	jm.UpdateJobStatus(target, JobStatusCompleted)

	rec := postPromote(h, source, "go", url.Values{"target_lab_id": {target}, "target_name": {"<i>x</i>"}})
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, "success-message")
	assert.NotContains(t, body, "<script>")
	assert.NotContains(t, body, "<i>x</i>")
	assert.NotContains(t, body, "<b>cred</b>")
}

func TestPromoteTemplate_RecordsAudit(t *testing.T) {
	tests := []struct {
		name       string
		targetName string
		wantDetail func(source string) string
	}{
		{name: "same name", targetName: "", wantDetail: func(source string) string { return "go from lab " + source }},
		{name: "renamed", targetName: "go-v2", wantDetail: func(source string) string { return "go-v2 (was go) from lab " + source }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, jm := newUploadTestHandler(t)
			as, err := NewAuditStore(t.TempDir())
			require.NoError(t, err)
			h.SetAuditStore(as)
			source := completedLab(t, jm, WorkspaceTemplate{Name: "go"})
			target := completedLab(t, jm, WorkspaceTemplate{Name: "python"})

			rec := postPromote(h, source, "go", url.Values{"target_lab_id": {target}, "target_name": {tt.targetName}})
			require.Equal(t, http.StatusOK, rec.Code)

			entries := waitForAuditEntry(t, as, "lab.template_promote")
			require.Len(t, entries, 1)
			assert.Equal(t, target, entries[0].LabID, "the promotion is recorded on the lab that changed")
			assert.Equal(t, tt.wantDetail(source), entries[0].Detail)
		})
	}
}

func TestCloneWorkspaceTemplate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		template WorkspaceTemplate
	}{
		{name: "name only", template: WorkspaceTemplate{Name: "default"}},
		{
			name: "every reference field",
			template: WorkspaceTemplate{
				Name:             "full",
				Env:              map[string]string{"A": "b"},
				Extensions:       []string{"golang.go"},
				VSCodeSettings:   map[string]any{"files.exclude": map[string]any{"**/node_modules": true}},
				Sidecars:         []WorkspaceSidecar{{Name: "db", Image: "postgres:16", Env: map[string]string{"P": "q"}}},
				Mounts:           []WorkspaceMount{{Type: "secret", Name: "s", Path: "/etc/s"}},
				ImagePullSecrets: []string{"regcred"},
				Devcontainer:     &DevcontainerConfig{Enabled: true, CacheRepo: "registry.example.com/cache"},
				NodeSelector:     map[string]string{"pool": "workspaces"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			clone, err := cloneWorkspaceTemplate(tt.template)
			require.NoError(t, err)
			assert.Equal(t, tt.template, clone)
			if tt.template.Devcontainer != nil {
				assert.NotSame(t, tt.template.Devcontainer, clone.Devcontainer)
			}
		})
	}
}

// The Promote control renders on a lab's template cards only when there is
// another lab that is up to promote to.
func TestLabDetail_PromoteTemplateControl(t *testing.T) {
	t.Chdir("../..") // getTemplate resolves web/ relative to the working directory

	tests := []struct {
		name        string
		others      func(jm *JobManager) []string // returns the IDs expected as targets
		wantPromote bool
	}{
		{
			name:        "no other lab",
			others:      func(*JobManager) []string { return nil },
			wantPromote: false,
		},
		{
			name: "another lab that is not up",
			others: func(jm *JobManager) []string {
				jm.CreateJob(&LabConfig{StackName: "pending"})
				return nil
			},
			wantPromote: false,
		},
		{
			name: "another lab that is up",
			others: func(jm *JobManager) []string {
				jm.CreateJob(&LabConfig{StackName: "pending"})
				id := jm.CreateJob(&LabConfig{StackName: "prod-workshop"})
				jm.UpdateJobStatus(id, JobStatusCompleted)
				return []string{id}
			},
			wantPromote: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jm := NewJobManager("")
			h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
			useFakeBackend(h, &fakeBackend{reachable: true})
			labID := completedLabWithKubeconfig(jm, 0)
			h.updateJobConfig(labID, func(c *LabConfig) {
				c.WorkspaceTemplates = []WorkspaceTemplate{{Name: "plain"}, bakeableTemplate("go")}
			})
			targets := tt.others(jm)

			rec := httptest.NewRecorder()
			h.ServeLabDetail(rec, httptest.NewRequest(http.MethodGet, "/labs/"+labID, nil))
			require.Equal(t, http.StatusOK, rec.Code)
			body := rec.Body.String()
			require.Contains(t, body, "template-status-card", "the templates panel must render")

			assert.Equal(t, tt.wantPromote, strings.Contains(body, "template-promote-panel"))
			assert.Equal(t, tt.wantPromote, strings.Contains(body, `hx-post="/api/labs/`+labID+`/templates/go/promote"`))
			for _, id := range targets {
				assert.Contains(t, body, `<option value="`+id+`">prod-workshop</option>`)
			}
			if tt.wantPromote {
				assert.NotContains(t, body, `<option value="`+labID+`">`, "a lab is not its own promotion target")
				assert.Equal(t, 1, strings.Count(body, `name="bake" value="true"`), "only the devcontainer template offers a bake")
			}
		})
	}
}
