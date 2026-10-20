package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"easylab/internal/providers/workspace"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const idleTestTimeoutMinutes = 30

// idleLab creates a completed lab with a kubeconfig and the given idle timeout.
func idleLab(jm *JobManager, timeoutMinutes int) string {
	id := completedLabWithKubeconfig(jm, 0)
	job, _ := jm.GetJob(id)
	job.mu.Lock()
	job.Config.WorkspaceIdleTimeoutMinutes = timeoutMinutes
	job.mu.Unlock()
	return id
}

// runningWorkspace is a workspace that has been up for upFor.
func runningWorkspace(id string, upFor time.Duration) workspace.Workspace {
	return workspace.Workspace{
		ID: id, Name: id, Owner: "alice",
		Ready: true, Phase: workspace.PhaseRunning,
		CreatedAt: time.Now().Add(-upFor), UpdatedAt: time.Now().Add(-upFor),
	}
}

// ephemeralWorkspace is a running workspace whose files are not on a volume of
// its own, so pausing it would lose them.
func ephemeralWorkspace(id string, upFor time.Duration) workspace.Workspace {
	ws := runningWorkspace(id, upFor)
	ws.Ephemeral = true
	return ws
}

func usedAgo(fb *fakeBackend, id string, ago time.Duration) {
	fb.activity = map[string]time.Time{id: time.Now().Add(-ago)}
}

func TestStopIdleWorkspaces(t *testing.T) {
	t.Parallel()

	idle := func(fb *fakeBackend) { usedAgo(fb, "ws-1", time.Hour) }
	active := func(fb *fakeBackend) { usedAgo(fb, "ws-1", 5*time.Minute) }
	neverUsed := func(fb *fakeBackend) { fb.activity = nil }
	probeFails := func(fb *fakeBackend) { fb.activityErr = errors.New("proxy unavailable") }
	noActivitySignal := func(fb *fakeBackend) { fb.activityErr = workspace.ErrActivityUnsupported }
	unreachable := func(fb *fakeBackend) { fb.reachable = false }
	reachable := func(fb *fakeBackend) { fb.reachable = true }
	both := func(steps ...func(*fakeBackend)) func(*fakeBackend) {
		return func(fb *fakeBackend) {
			for _, s := range steps {
				s(fb)
			}
		}
	}

	tests := []struct {
		name      string
		timeout   int
		workspace workspace.Workspace
		// passes configures the backend before each successive idle pass.
		passes    []func(*fakeBackend)
		wantStops []string
	}{
		{
			name:      "idle on a single pass is not enough",
			timeout:   idleTestTimeoutMinutes,
			workspace: runningWorkspace("ws-1", 2*time.Hour),
			passes:    []func(*fakeBackend){idle},
		},
		{
			name:      "idle on two consecutive passes is paused",
			timeout:   idleTestTimeoutMinutes,
			workspace: runningWorkspace("ws-1", 2*time.Hour),
			passes:    []func(*fakeBackend){idle, idle},
			wantStops: []string{"ws-1"},
		},
		{
			name:      "paused only once however long it stays idle",
			timeout:   idleTestTimeoutMinutes,
			workspace: runningWorkspace("ws-1", 2*time.Hour),
			passes:    []func(*fakeBackend){idle, idle, idle},
			wantStops: []string{"ws-1"},
		},
		{
			name:      "activity between two idle readings clears the strike",
			timeout:   idleTestTimeoutMinutes,
			workspace: runningWorkspace("ws-1", 2*time.Hour),
			passes:    []func(*fakeBackend){idle, active, idle},
		},
		{
			name:      "student coming back before the second pass is not paused",
			timeout:   idleTestTimeoutMinutes,
			workspace: runningWorkspace("ws-1", 2*time.Hour),
			passes:    []func(*fakeBackend){idle, active},
		},
		{
			name:      "active workspace is never paused",
			timeout:   idleTestTimeoutMinutes,
			workspace: runningWorkspace("ws-1", 2*time.Hour),
			passes:    []func(*fakeBackend){active, active, active},
		},
		{
			name:      "never opened and up for longer than the timeout is paused",
			timeout:   idleTestTimeoutMinutes,
			workspace: runningWorkspace("ws-1", 2*time.Hour),
			passes:    []func(*fakeBackend){neverUsed, neverUsed},
			wantStops: []string{"ws-1"},
		},
		{
			name:      "just created or resumed gets a full timeout of grace",
			timeout:   idleTestTimeoutMinutes,
			workspace: runningWorkspace("ws-1", 5*time.Minute),
			passes:    []func(*fakeBackend){neverUsed, neverUsed},
		},
		{
			name:    "workspace that is not ready is left alone",
			timeout: idleTestTimeoutMinutes,
			workspace: workspace.Workspace{
				ID: "ws-1", Name: "ws-1", Phase: workspace.PhaseAgentsStarting,
				UpdatedAt: time.Now().Add(-2 * time.Hour),
			},
			passes: []func(*fakeBackend){idle, idle},
		},
		{
			name:      "unreadable activity is not idleness",
			timeout:   idleTestTimeoutMinutes,
			workspace: runningWorkspace("ws-1", 2*time.Hour),
			passes:    []func(*fakeBackend){probeFails, probeFails, probeFails},
		},
		{
			name:      "no idle timeout disables pausing",
			timeout:   0,
			workspace: runningWorkspace("ws-1", 2*time.Hour),
			passes:    []func(*fakeBackend){idle, idle},
		},
		{
			name:      "unreachable cluster is skipped",
			timeout:   idleTestTimeoutMinutes,
			workspace: runningWorkspace("ws-1", 2*time.Hour),
			passes:    []func(*fakeBackend){both(idle, unreachable), both(idle, unreachable)},
		},
		{
			name:      "a skipped pass does not count as an idle reading",
			timeout:   idleTestTimeoutMinutes,
			workspace: runningWorkspace("ws-1", 2*time.Hour),
			passes:    []func(*fakeBackend){idle, unreachable, both(idle, reachable)},
		},
		{
			// Its files live in an EmptyDir, so a pause would lose them. Never
			// offered a stop at all, rather than attempted and refused on
			// every pass.
			name:      "a workspace with no volume to keep is never paused",
			timeout:   idleTestTimeoutMinutes,
			workspace: ephemeralWorkspace("ws-1", 2*time.Hour),
			passes:    []func(*fakeBackend){idle, idle, idle},
		},
		{
			// An OpenChamber workspace: its IDE publishes no activity signal, so
			// it can never be shown to be idle. No strike is ever recorded, so
			// the two-reading gate cannot fire however many passes run.
			name:      "an IDE that reports no activity at all is never paused",
			timeout:   idleTestTimeoutMinutes,
			workspace: runningWorkspace("ws-1", 2*time.Hour),
			passes:    []func(*fakeBackend){noActivitySignal, noActivitySignal, noActivitySignal},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			jm := NewJobManager("")
			idleLab(jm, tt.timeout)
			h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
			fb := &fakeBackend{reachable: true, workspaces: []workspace.Workspace{tt.workspace}}
			useFakeBackend(h, fb)

			for _, configure := range tt.passes {
				configure(fb)
				h.stopIdleWorkspaces()
			}

			assert.Equal(t, tt.wantStops, fb.StopCalls)
		})
	}
}

func TestStopIdleWorkspaces_FailedStopRetriesOnTheNextPass(t *testing.T) {
	t.Parallel()

	jm := NewJobManager("")
	idleLab(jm, idleTestTimeoutMinutes)
	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	fb := &fakeBackend{
		reachable:  true,
		workspaces: []workspace.Workspace{runningWorkspace("ws-1", 2*time.Hour)},
		activity:   map[string]time.Time{"ws-1": time.Now().Add(-time.Hour)},
		stopErr:    errors.New("the API server said no"),
	}
	useFakeBackend(h, fb)

	// Two passes to earn the stop, then a third: a stop that failed must not
	// send the workspace back to the start of the two-reading count.
	h.stopIdleWorkspaces()
	h.stopIdleWorkspaces()
	h.stopIdleWorkspaces()

	assert.Equal(t, []string{"ws-1", "ws-1"}, fb.StopCalls)

	// And once the cluster accepts it, the workspace is paused and left alone.
	fb.stopErr = nil
	h.stopIdleWorkspaces()
	h.stopIdleWorkspaces()
	assert.Equal(t, []string{"ws-1", "ws-1", "ws-1"}, fb.StopCalls)
}

func TestStopIdleWorkspaces_OnlyTheIdleOnes(t *testing.T) {
	t.Parallel()

	jm := NewJobManager("")
	idleLab(jm, idleTestTimeoutMinutes)
	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	fb := &fakeBackend{
		reachable: true,
		workspaces: []workspace.Workspace{
			runningWorkspace("ws-idle", 3*time.Hour),
			runningWorkspace("ws-active", 3*time.Hour),
		},
		activity: map[string]time.Time{
			"ws-idle":   time.Now().Add(-2 * time.Hour),
			"ws-active": time.Now().Add(-time.Minute),
		},
	}
	useFakeBackend(h, fb)

	h.stopIdleWorkspaces()
	h.stopIdleWorkspaces()

	assert.Equal(t, []string{"ws-idle"}, fb.StopCalls)
}

func TestNormalizeIdleTimeout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    int
		expected int
	}{
		{name: "zero disables", input: 0, expected: 0},
		{name: "negative disables", input: -5, expected: 0},
		{name: "below the minimum is raised to it", input: 3, expected: minWorkspaceIdleTimeoutMinutes},
		{name: "minimum is kept", input: minWorkspaceIdleTimeoutMinutes, expected: minWorkspaceIdleTimeoutMinutes},
		{name: "above the minimum is kept", input: 90, expected: 90},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, normalizeIdleTimeout(tt.input))
		})
	}
}

func TestCreateLabConfigFromForm_IdleTimeout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected int
	}{
		{name: "unset", input: "", expected: 0},
		{name: "valid", input: "45", expected: 45},
		{name: "too short is raised to the minimum", input: "2", expected: minWorkspaceIdleTimeoutMinutes},
		{name: "not a number", input: "soon", expected: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
			req := httptest.NewRequest(http.MethodPost, "/api/labs", nil)
			req.Form = url.Values{"stack_name": {"lab"}, "workspace_idle_timeout_minutes": {tt.input}}
			req.PostForm = req.Form

			cfg := h.createLabConfigFromForm(req, nil)

			require.NotNil(t, cfg)
			assert.Equal(t, tt.expected, cfg.WorkspaceIdleTimeoutMinutes)
		})
	}
}

func TestUpdateLabLifecycle_IdleTimeout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		input      string
		wantStatus int
		expected   int
	}{
		{name: "blank disables", input: "", wantStatus: http.StatusOK, expected: 0},
		{name: "zero disables", input: "0", wantStatus: http.StatusOK, expected: 0},
		{name: "valid", input: "60", wantStatus: http.StatusOK, expected: 60},
		{name: "minimum", input: "10", wantStatus: http.StatusOK, expected: 10},
		{name: "below the minimum is rejected", input: "9", wantStatus: http.StatusBadRequest, expected: 25},
		{name: "negative is rejected", input: "-1", wantStatus: http.StatusBadRequest, expected: 25},
		{name: "not a number is rejected", input: "soon", wantStatus: http.StatusBadRequest, expected: 25},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			jm := NewJobManager("")
			// 25 is what a rejected update must leave untouched.
			id := idleLab(jm, 25)
			h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

			rec := postLifecycle(h, id, url.Values{"workspace_idle_timeout_minutes": {tt.input}}.Encode())

			assert.Equal(t, tt.wantStatus, rec.Code)
			job, _ := jm.GetJob(id)
			job.mu.RLock()
			defer job.mu.RUnlock()
			assert.Equal(t, tt.expected, job.Config.WorkspaceIdleTimeoutMinutes)
		})
	}
}

func resumeRequest(method, labID, workspaceName, email string) *http.Request {
	target := "/api/student/workspace/resume?lab_id=" + url.QueryEscape(labID) + "&workspace_name=" + url.QueryEscape(workspaceName)
	req := httptest.NewRequest(method, target, nil)
	if email != "" {
		req = asStudent(req, email)
	}
	return req
}

func TestResumeWorkspace(t *testing.T) {
	t.Parallel()

	stopped := workspace.Workspace{ID: "ws-alice", Name: "ws-alice", Owner: "alice", Phase: workspace.PhaseStopped}
	running := workspace.Workspace{ID: "ws-alice", Name: "ws-alice", Owner: "alice", Phase: workspace.PhaseRunning, Ready: true}

	tests := []struct {
		name       string
		method     string
		email      string
		workspace  *workspace.Workspace
		startErr   error
		unknownLab bool
		wantStatus int
		wantStarts []string
	}{
		{name: "owner resumes a paused workspace", method: http.MethodPost, email: "alice@example.com", workspace: &stopped, wantStatus: http.StatusOK, wantStarts: []string{"ws-alice"}},
		{name: "running workspace is left as is", method: http.MethodPost, email: "alice@example.com", workspace: &running, wantStatus: http.StatusOK},
		{name: "another student cannot resume it", method: http.MethodPost, email: "mallory@example.com", workspace: &stopped, wantStatus: http.StatusServiceUnavailable},
		{name: "unknown workspace", method: http.MethodPost, email: "alice@example.com", wantStatus: http.StatusServiceUnavailable},
		{name: "unknown lab", method: http.MethodPost, email: "alice@example.com", workspace: &stopped, unknownLab: true, wantStatus: http.StatusNotFound},
		{name: "not logged in", method: http.MethodPost, workspace: &stopped, wantStatus: http.StatusBadRequest},
		{name: "GET is refused", method: http.MethodGet, email: "alice@example.com", workspace: &stopped, wantStatus: http.StatusMethodNotAllowed},
		{name: "cluster refuses the resume", method: http.MethodPost, email: "alice@example.com", workspace: &stopped, startErr: errors.New("forbidden: internal detail"), wantStatus: http.StatusServiceUnavailable, wantStarts: []string{"ws-alice"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			jm := NewJobManager("")
			labID := idleLab(jm, idleTestTimeoutMinutes)
			if tt.unknownLab {
				labID = "no-such-lab"
			}
			h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
			fb := &fakeBackend{reachable: true, getWS: tt.workspace, startErr: tt.startErr}
			useFakeBackend(h, fb)

			rec := httptest.NewRecorder()
			h.ResumeWorkspace(rec, resumeRequest(tt.method, labID, "ws-alice", tt.email))

			assert.Equal(t, tt.wantStatus, rec.Code)
			assert.Equal(t, tt.wantStarts, fb.StartCalls)
			assert.NotContains(t, rec.Body.String(), "internal detail", "cluster errors must not reach the student")
			if tt.wantStatus == http.StatusOK {
				assert.Contains(t, rec.Body.String(), "data-poll-url", "the page must go back to polling")
			}
		})
	}
}

func TestBuildWorkspaceStatusHTML_Stopped(t *testing.T) {
	t.Parallel()

	html := buildWorkspaceStatusHTML("lab 1", "ws&1", workspace.PhaseStopped, "", "")

	assert.Contains(t, html, "workspace-ready-status--paused")
	assert.Contains(t, html, "btn-workspace-resume")
	assert.Contains(t, html, `data-resume-url="/api/student/workspace/resume?lab_id=lab+1&amp;workspace_name=ws%261"`)
	assert.NotContains(t, html, "data-poll-url", "a paused workspace must not be polled")
}

func TestWorkspaceStatus_PausedWorkspace(t *testing.T) {
	t.Parallel()

	jm := NewJobManager("")
	labID := idleLab(jm, idleTestTimeoutMinutes)
	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	useFakeBackend(h, &fakeBackend{reachable: true, getWS: &workspace.Workspace{ID: "ws-alice", Name: "ws-alice", Owner: "alice", Phase: workspace.PhaseStopped}})

	req := httptest.NewRequest(http.MethodGet, "/api/student/workspace/status?lab_id="+labID+"&workspace_name=ws-alice", nil)
	rec := httptest.NewRecorder()
	h.WorkspaceStatus(rec, asStudent(req, "alice@example.com"))

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "data-resume-url")
}
