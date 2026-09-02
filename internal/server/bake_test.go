package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"easylab/internal/providers/workspace"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeBakeProvider is a minimal workspace.BakeProvider for testing awaitBake's
// polling/retry/failure behavior without a real cluster.
type fakeBakeProvider struct {
	jobStatus       workspace.BakeState
	remoteUserErrs  int32 // number of leading BakeRemoteUser calls that return an error
	remoteUserCalls int32
	remoteUser      string
}

func (f *fakeBakeProvider) EnsureBakeJob(context.Context, workspace.BakeRequest) error { return nil }
func (f *fakeBakeProvider) BakeJobStatus(context.Context, string, string) (workspace.BakeState, error) {
	return f.jobStatus, nil
}
func (f *fakeBakeProvider) BakeRemoteUser(context.Context, string, bool, string) (string, error) {
	n := atomic.AddInt32(&f.remoteUserCalls, 1)
	if n <= f.remoteUserErrs {
		return "", errors.New("tls: failed to verify certificate")
	}
	return f.remoteUser, nil
}

// bakeLab creates a completed lab with a kubeconfig set (BakeTemplate rejects a lab
// with none before it ever reaches template-level gating) and the given templates.
func bakeLab(t *testing.T, jm *JobManager, templates ...WorkspaceTemplate) string {
	t.Helper()
	id := completedLab(t, jm, templates...)
	job, ok := jm.GetJob(id)
	require.True(t, ok)
	job.mu.Lock()
	job.Kubeconfig = "fake-kubeconfig"
	job.mu.Unlock()
	return id
}

// postBake drives BakeTemplate for a given lab/template.
func postBake(h *Handler, labID, templateName string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/labs/"+labID+"/templates/"+templateName+"/bake", nil)
	rec := httptest.NewRecorder()
	h.BakeTemplate(rec, req)
	return rec
}

func TestBakeTemplate_RejectsNonDevcontainerTemplate(t *testing.T) {
	h, jm := newUploadTestHandler(t)
	id := bakeLab(t, jm, WorkspaceTemplate{Name: "plain", Image: "golang:1.22"})

	rec := postBake(h, id, "plain")
	assert.Contains(t, rec.Body.String(), "devcontainer", "body: %s", rec.Body.String())
}

func TestBakeTemplate_RejectsDisabledDevcontainerBlock(t *testing.T) {
	h, jm := newUploadTestHandler(t)
	id := bakeLab(t, jm, WorkspaceTemplate{Name: "inert", Devcontainer: &DevcontainerConfig{Enabled: false}})

	rec := postBake(h, id, "inert")
	assert.Contains(t, rec.Body.String(), "devcontainer")
}

func TestBakeTemplate_RejectsMissingCacheRegistry(t *testing.T) {
	h, jm := newUploadTestHandler(t)
	id := bakeLab(t, jm, WorkspaceTemplate{
		Name:         "go-workshop",
		GitRepo:      "https://gitlab.com/org/workshop.git",
		Devcontainer: &DevcontainerConfig{Enabled: true},
	})

	rec := postBake(h, id, "go-workshop")
	assert.Contains(t, rec.Body.String(), "cache registry")
}

func TestBakeTemplate_RejectsInClusterWithoutDomain(t *testing.T) {
	h, jm := newUploadTestHandler(t)
	id := jm.CreateJob(&LabConfig{
		StackName: "test",
		// Domain deliberately empty: an in-cluster bake needs one to expose the
		// registry over trusted HTTPS.
		WorkspaceTemplates: []WorkspaceTemplate{{
			Name:         "go-workshop",
			GitRepo:      "https://gitlab.com/org/workshop.git",
			Devcontainer: &DevcontainerConfig{Enabled: true, UseInClusterCache: true},
		}},
	})
	jm.UpdateJobStatus(id, JobStatusCompleted)
	job, _ := jm.GetJob(id)
	job.mu.Lock()
	job.Kubeconfig = "fake-kubeconfig"
	job.mu.Unlock()

	rec := postBake(h, id, "go-workshop")
	assert.Contains(t, rec.Body.String(), "domain")
}

func TestBakeTemplate_AcceptsExternalCacheRepoWithoutDomain(t *testing.T) {
	h, jm := newUploadTestHandler(t)
	id := bakeLab(t, jm, WorkspaceTemplate{
		Name:         "go-workshop",
		GitRepo:      "https://gitlab.com/org/workshop.git",
		Devcontainer: &DevcontainerConfig{Enabled: true, CacheRepo: "registry.example.com/easylab/cache"},
	})

	// No domain configured on this lab (StackName-only LabConfig from bakeLab), and
	// no BakeProvider-implementing backend wired — so this must fail past the
	// gating logic (at the backend/EnsureBakeJob step, whatever the exact error),
	// not be rejected by the gating checks this test targets.
	rec := postBake(h, id, "go-workshop")
	assert.NotContains(t, rec.Body.String(), "cache registry")
	assert.NotContains(t, rec.Body.String(), "domain")
}

func TestBakeTemplate_RejectsUnknownLab(t *testing.T) {
	h, _ := newUploadTestHandler(t)
	rec := postBake(h, "does-not-exist", "go-workshop")
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestBakeTemplate_RejectsUnknownTemplate(t *testing.T) {
	h, jm := newUploadTestHandler(t)
	id := bakeLab(t, jm, WorkspaceTemplate{Name: "go-workshop", Devcontainer: &DevcontainerConfig{Enabled: true, CacheRepo: "registry.example.com/cache"}})

	rec := postBake(h, id, "does-not-exist")
	assert.Contains(t, rec.Body.String(), "devcontainer")
}

// TestRenderBakeStatus_BuildingPollerReplacesWholeContainer guards against a
// regression where the self-poller only swapped itself (hx-swap="outerHTML" on
// its own div): the response bundles a fresh badge alongside the poller, so an
// outerHTML self-swap leaves the previous badge behind as a stale sibling,
// duplicating it on every 10s cycle instead of replacing it. The poller must
// target the stable outer container (#bake-status-<name>) and replace its whole
// innerHTML instead, so each cycle fully redraws rather than accumulates.
func TestRenderBakeStatus_BuildingPollerReplacesWholeContainer(t *testing.T) {
	h, jm := newUploadTestHandler(t)
	id := bakeLab(t, jm, WorkspaceTemplate{Name: "go-workshop", Devcontainer: &DevcontainerConfig{Enabled: true, CacheRepo: "registry.example.com/cache"}})

	h.bakeStatusesMu.Lock()
	h.bakeStatuses[id+"/go-workshop"] = &bakeStatus{State: "building"}
	h.bakeStatusesMu.Unlock()

	got := h.renderBakeStatus(id, "go-workshop")

	assert.NotContains(t, got, `hx-swap="outerHTML"`, "the poller must not swap only itself, or the badge outside it is never replaced")
	assert.Contains(t, got, `hx-target="#bake-status-go-workshop"`)
	assert.Contains(t, got, `hx-swap="innerHTML"`)
	// Exactly one badge in the fragment EasyLab hands back on each poll — the bug
	// was never about this single response, it was about what accumulates in the
	// DOM across polls, but a duplicate badge in one response would be just as wrong.
	assert.Equal(t, 1, strings.Count(got, "status-badge"))
}

// TestAwaitBake_PersistentPullFailureMarksBakeFailed guards against the bug where a
// baked image that a kubelet can never actually pull (e.g. a stalled cert-manager
// Certificate, so the Ingress serves Traefik's default self-signed cert) was still
// recorded as a successful bake — awaitBake used to log a BakeRemoteUser error and
// proceed anyway. A persistent failure must be reported as a failed bake, not
// written to LabConfig.BakedImages.
func TestAwaitBake_PersistentPullFailureMarksBakeFailed(t *testing.T) {
	t.Setenv("BAKE_REGISTRY_READY_ATTEMPTS", "2")
	t.Setenv("BAKE_REGISTRY_READY_INTERVAL_SECONDS", "1")

	h, jm := newUploadTestHandler(t)
	id := bakeLab(t, jm, WorkspaceTemplate{Name: "go-workshop", Devcontainer: &DevcontainerConfig{Enabled: true, CacheRepo: "registry.example.com/cache"}})

	fp := &fakeBakeProvider{jobStatus: workspace.BakeStateComplete, remoteUserErrs: 999}
	h.awaitBake(id, "go-workshop", fp, "registry.example.com/cache/baked-go-workshop:latest", false, "")

	job, _ := jm.GetJob(id)
	job.mu.RLock()
	_, baked := job.Config.BakedImages["go-workshop"]
	job.mu.RUnlock()
	assert.False(t, baked, "a template that never became pullable must not be recorded as baked")

	h.bakeStatusesMu.RLock()
	st := h.bakeStatuses[id+"/go-workshop"]
	h.bakeStatusesMu.RUnlock()
	require.NotNil(t, st)
	assert.Equal(t, "failed", st.State)
	assert.Contains(t, st.Error, "never became pullable")
	assert.Equal(t, int32(2), fp.remoteUserCalls, "must retry up to the configured attempt count, not fail on the first try")
}

// TestAwaitBake_FailedRebuildInvalidatesStaleBakedImage guards a second bug found
// alongside the one above: RequestWorkspace reads LabConfig.BakedImages directly and
// has no visibility into bakeStatuses (in-memory, admin-UI-only) — so a bake
// recorded as successful before the pull-verification check existed (or from any
// earlier attempt) kept being handed to students forever, even after a Rebuild
// confirmed the current image is not pullable. A confirmed pull-verification
// failure must clear that stale record, not just report "failed" in the UI.
func TestAwaitBake_FailedRebuildInvalidatesStaleBakedImage(t *testing.T) {
	t.Setenv("BAKE_REGISTRY_READY_ATTEMPTS", "1")
	t.Setenv("BAKE_REGISTRY_READY_INTERVAL_SECONDS", "1")

	h, jm := newUploadTestHandler(t)
	id := bakeLab(t, jm, WorkspaceTemplate{Name: "go-workshop", Devcontainer: &DevcontainerConfig{Enabled: true, CacheRepo: "registry.example.com/cache"}})

	// Simulate a stale record from before pull verification existed: recorded as
	// baked, but never actually confirmed pullable.
	job, _ := jm.GetJob(id)
	job.mu.Lock()
	job.Config.BakedImages = map[string]BakedImage{
		"go-workshop": {Image: "registry.example.com/cache/baked-go-workshop:latest", At: time.Now()},
	}
	job.mu.Unlock()

	fp := &fakeBakeProvider{jobStatus: workspace.BakeStateComplete, remoteUserErrs: 999}
	h.awaitBake(id, "go-workshop", fp, "registry.example.com/cache/baked-go-workshop:latest", false, "")

	job, _ = jm.GetJob(id)
	job.mu.RLock()
	_, stillBaked := job.Config.BakedImages["go-workshop"]
	job.mu.RUnlock()
	assert.False(t, stillBaked, "a confirmed-broken pull path must clear the stale record, or RequestWorkspace keeps handing it to students")
}

// TestAwaitBake_TransientPullFailureRecovers guards the other side: a certificate
// that simply hasn't finished propagating yet must not fail the bake outright — a
// later successful check must still record it as baked.
func TestAwaitBake_TransientPullFailureRecovers(t *testing.T) {
	t.Setenv("BAKE_REGISTRY_READY_ATTEMPTS", "5")
	t.Setenv("BAKE_REGISTRY_READY_INTERVAL_SECONDS", "1")

	h, jm := newUploadTestHandler(t)
	id := bakeLab(t, jm, WorkspaceTemplate{Name: "go-workshop", Devcontainer: &DevcontainerConfig{Enabled: true, CacheRepo: "registry.example.com/cache"}})

	fp := &fakeBakeProvider{jobStatus: workspace.BakeStateComplete, remoteUserErrs: 2, remoteUser: "vscode"}
	h.awaitBake(id, "go-workshop", fp, "registry.example.com/cache/baked-go-workshop:latest", false, "")

	job, _ := jm.GetJob(id)
	job.mu.RLock()
	baked, ok := job.Config.BakedImages["go-workshop"]
	job.mu.RUnlock()
	require.True(t, ok, "must recover once the pull path becomes reachable")
	assert.Equal(t, "vscode", baked.RemoteUser)

	h.bakeStatusesMu.RLock()
	_, stillTracked := h.bakeStatuses[id+"/go-workshop"]
	h.bakeStatusesMu.RUnlock()
	assert.False(t, stillTracked, "a successful bake must clear the in-flight status")
}

func TestSanitizeRepoSegment(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"already safe", "go-workshop", "go-workshop"},
		{"uppercase and spaces", "Go Workshop", "go-workshop"},
		{"leading/trailing punctuation trimmed", "--go--", "go"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, sanitizeRepoSegment(tt.in))
		})
	}
}
