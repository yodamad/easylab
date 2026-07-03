package server

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/coder/v2/codersdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// helper to build a minimal workspace with one agent
func workspaceWithAgent(buildStatus string, agentStatus codersdk.WorkspaceAgentStatus, lifecycle codersdk.WorkspaceAgentLifecycle, appHealth ...codersdk.WorkspaceAppHealth) codersdk.Workspace {
	agent := codersdk.WorkspaceAgent{
		Name:           "main",
		Status:         agentStatus,
		LifecycleState: lifecycle,
	}
	for _, h := range appHealth {
		agent.Apps = append(agent.Apps, codersdk.WorkspaceApp{Health: h})
	}
	return codersdk.Workspace{
		LatestBuild: codersdk.WorkspaceBuild{
			Status: codersdk.WorkspaceStatus(buildStatus),
			Resources: []codersdk.WorkspaceResource{
				{Agents: []codersdk.WorkspaceAgent{agent}},
			},
		},
	}
}

func workspaceWithBuildStatus(status string) codersdk.Workspace {
	return codersdk.Workspace{
		LatestBuild: codersdk.WorkspaceBuild{
			Status: codersdk.WorkspaceStatus(status),
		},
	}
}

// --- workspaceReadinessStatus tests ---

func TestWorkspaceReadinessStatus_NotRunning(t *testing.T) {
	tests := []struct {
		status string
	}{
		{"pending"},
		{"starting"},
		{"stopping"},
		{"stopped"},
		{"failed"},
		{"canceling"},
		{"canceled"},
		{"deleting"},
		{"deleted"},
	}
	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			t.Parallel()
			ws := workspaceWithBuildStatus(tt.status)
			got := workspaceReadinessStatus(ws)
			assert.Equal(t, tt.status, got)
		})
	}
}

func TestWorkspaceReadinessStatus_RunningNoAgents(t *testing.T) {
	ws := codersdk.Workspace{
		LatestBuild: codersdk.WorkspaceBuild{
			Status: "running",
		},
	}
	got := workspaceReadinessStatus(ws)
	assert.Equal(t, "agents_starting", got)
}

func TestWorkspaceReadinessStatus_RunningAgentReady(t *testing.T) {
	ws := workspaceWithAgent("running",
		codersdk.WorkspaceAgentConnected,
		codersdk.WorkspaceAgentLifecycleReady,
	)
	got := workspaceReadinessStatus(ws)
	assert.Equal(t, "running", got)
}

func TestWorkspaceReadinessStatus_AgentTimeout(t *testing.T) {
	ws := workspaceWithAgent("running",
		codersdk.WorkspaceAgentTimeout,
		codersdk.WorkspaceAgentLifecycleReady,
	)
	got := workspaceReadinessStatus(ws)
	assert.Equal(t, "agents_failed", got)
}

func TestWorkspaceReadinessStatus_LifecycleStartTimeout(t *testing.T) {
	ws := workspaceWithAgent("running",
		codersdk.WorkspaceAgentConnected,
		codersdk.WorkspaceAgentLifecycleStartTimeout,
	)
	got := workspaceReadinessStatus(ws)
	assert.Equal(t, "agents_failed", got)
}

func TestWorkspaceReadinessStatus_LifecycleStartError(t *testing.T) {
	ws := workspaceWithAgent("running",
		codersdk.WorkspaceAgentConnected,
		codersdk.WorkspaceAgentLifecycleStartError,
	)
	got := workspaceReadinessStatus(ws)
	assert.Equal(t, "agents_failed", got)
}

func TestWorkspaceReadinessStatus_LifecycleStarting(t *testing.T) {
	ws := workspaceWithAgent("running",
		codersdk.WorkspaceAgentConnected,
		codersdk.WorkspaceAgentLifecycleStarting,
	)
	got := workspaceReadinessStatus(ws)
	assert.Equal(t, "agents_starting", got)
}

func TestWorkspaceReadinessStatus_AppInitializing(t *testing.T) {
	ws := workspaceWithAgent("running",
		codersdk.WorkspaceAgentConnected,
		codersdk.WorkspaceAgentLifecycleReady,
		codersdk.WorkspaceAppHealthInitializing,
	)
	got := workspaceReadinessStatus(ws)
	assert.Equal(t, "agents_starting", got)
}

func TestWorkspaceReadinessStatus_AppUnhealthy(t *testing.T) {
	ws := workspaceWithAgent("running",
		codersdk.WorkspaceAgentConnected,
		codersdk.WorkspaceAgentLifecycleReady,
		codersdk.WorkspaceAppHealthUnhealthy,
	)
	got := workspaceReadinessStatus(ws)
	assert.Equal(t, "agents_failed", got)
}

func TestWorkspaceReadinessStatus_AppHealthy(t *testing.T) {
	ws := workspaceWithAgent("running",
		codersdk.WorkspaceAgentConnected,
		codersdk.WorkspaceAgentLifecycleReady,
		codersdk.WorkspaceAppHealthHealthy,
	)
	got := workspaceReadinessStatus(ws)
	assert.Equal(t, "running", got)
}

// --- workspaceFirstAgentName tests ---

func TestWorkspaceFirstAgentName_NoAgents(t *testing.T) {
	ws := codersdk.Workspace{
		LatestBuild: codersdk.WorkspaceBuild{},
	}
	got := workspaceFirstAgentName(ws)
	assert.Equal(t, "main", got)
}

func TestWorkspaceFirstAgentName_WithAgent(t *testing.T) {
	ws := workspaceWithAgent("running",
		codersdk.WorkspaceAgentConnected,
		codersdk.WorkspaceAgentLifecycleReady,
	)
	got := workspaceFirstAgentName(ws)
	assert.Equal(t, "main", got)
}

func TestWorkspaceFirstAgentName_EmptyAgentName(t *testing.T) {
	ws := codersdk.Workspace{
		LatestBuild: codersdk.WorkspaceBuild{
			Resources: []codersdk.WorkspaceResource{
				{Agents: []codersdk.WorkspaceAgent{{Name: ""}}},
			},
		},
	}
	got := workspaceFirstAgentName(ws)
	assert.Equal(t, "main", got)
}

func TestWorkspaceFirstAgentName_CustomName(t *testing.T) {
	ws := codersdk.Workspace{
		LatestBuild: codersdk.WorkspaceBuild{
			Resources: []codersdk.WorkspaceResource{
				{Agents: []codersdk.WorkspaceAgent{{Name: "custom-agent"}}},
			},
		},
	}
	got := workspaceFirstAgentName(ws)
	assert.Equal(t, "custom-agent", got)
}

// --- createStudentAppToken tests ---

// TestCreateStudentAppToken_RefreshesOnExpiredToken proves the student "Open
// workspace" path self-heals: when the stored admin token is expired, it
// re-logins with admin credentials and returns the refreshed token so the caller
// can persist it, instead of leaking a raw Coder 401 to the student.
func TestCreateStudentAppToken_RefreshesOnExpiredToken(t *testing.T) {
	const staleToken = "stale-token"
	const refreshedToken = "refreshed-token"
	const wantKey = "student-api-key"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		token := r.Header.Get(codersdk.SessionTokenHeader)
		switch {
		case r.URL.Path == "/api/v2/users/login" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(codersdk.LoginWithPasswordResponse{SessionToken: refreshedToken})
		case strings.HasSuffix(r.URL.Path, "/keys/tokens") && r.Method == http.MethodPost:
			// Long-lived admin-token minting — force fallback to the session token.
			w.WriteHeader(http.StatusNotFound)
		case strings.HasSuffix(r.URL.Path, "/status") && r.Method == http.MethodPut:
			// Reactivation is best-effort; failing it proves it is non-fatal.
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(codersdk.Response{Message: "unauthorized"})
		case strings.HasSuffix(r.URL.Path, "/keys") && r.Method == http.MethodPost:
			// Only the refreshed token is accepted — the stale token 401s.
			if token != refreshedToken {
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(codersdk.Response{Message: "unauthorized"})
				return
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(codersdk.GenerateAPIKeyResponse{Key: wantKey})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	key, freshToken, err := createStudentAppToken(srv.URL, staleToken, "admin@example.com", "pass", "student")
	require.NoError(t, err)
	assert.Equal(t, wantKey, key)
	assert.Equal(t, refreshedToken, freshToken)
}

// --- GetCoderCredentials path tests ---

func TestHandler_GetCoderCredentials_InvalidPath(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("GET", "/api/invalid/path", nil)
	w := httptest.NewRecorder()
	h.GetCoderCredentials(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandler_GetCoderCredentials_WrongSection(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("GET", "/api/other/job-id/coder-credentials", nil)
	w := httptest.NewRecorder()
	h.GetCoderCredentials(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandler_GetCoderCredentials_JobNotFound(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("GET", "/api/jobs/nonexistent/coder-credentials", nil)
	w := httptest.NewRecorder()
	h.GetCoderCredentials(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestHandler_GetCoderCredentials_NotCompleted(t *testing.T) {
	jm := NewJobManager("")
	id := jm.CreateJob(&LabConfig{StackName: "test"})
	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

	req := httptest.NewRequest("GET", "/api/jobs/"+id+"/coder-credentials", nil)
	w := httptest.NewRecorder()
	h.GetCoderCredentials(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// --- ListLabTemplates tests ---

func TestHandler_ListLabTemplates_WrongMethod(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("POST", "/api/jobs/x/templates", nil)
	w := httptest.NewRecorder()
	h.ListLabTemplates(w, req)
	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

func TestHandler_ListLabTemplates_MissingLabID(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("GET", "/api/labs/templates", nil)
	w := httptest.NewRecorder()
	h.ListLabTemplates(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandler_ListLabTemplates_LabNotFound(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("GET", "/api/labs/templates?lab_id=nonexistent", nil)
	w := httptest.NewRecorder()
	h.ListLabTemplates(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestHandler_ListLabTemplates_NotCompleted(t *testing.T) {
	jm := NewJobManager("")
	id := jm.CreateJob(&LabConfig{StackName: "test"})
	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("GET", "/api/labs/templates?lab_id="+id, nil)
	w := httptest.NewRecorder()
	h.ListLabTemplates(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// --- UploadTemplateToLab tests ---

func TestHandler_UploadTemplateToLab_WrongMethod(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("GET", "/api/labs/x/templates/upload", nil)
	w := httptest.NewRecorder()
	h.UploadTemplateToLab(w, req)
	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

func TestHandler_UploadTemplateToLab_InvalidPath(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("POST", "/api/invalid/x/templates/upload", nil)
	w := httptest.NewRecorder()
	h.UploadTemplateToLab(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandler_UploadTemplateToLab_LabNotFound(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("POST", "/api/labs/nonexistent/templates/upload", nil)
	w := httptest.NewRecorder()
	h.UploadTemplateToLab(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestHandler_UploadTemplateToLab_NotCompleted(t *testing.T) {
	jm := NewJobManager("")
	id := jm.CreateJob(&LabConfig{StackName: "test"})
	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("POST", "/api/labs/"+id+"/templates/upload", nil)
	w := httptest.NewRecorder()
	h.UploadTemplateToLab(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandler_UploadTemplateToLab_InvalidFileType(t *testing.T) {
	jm := NewJobManager("")
	id := jm.CreateJob(&LabConfig{StackName: "test"})
	job, _ := jm.GetJob(id)
	job.mu.Lock()
	job.Status = JobStatusCompleted
	job.CoderURL = "http://coder.example"
	job.CoderSessionToken = "token"
	job.CoderOrganizationID = "00000000-0000-0000-0000-000000000000"
	job.CoderAdminEmail = "admin@example.com"
	job.CoderAdminPassword = "pass"
	job.mu.Unlock()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("template_name", "mytemplate")
	fw, _ := mw.CreateFormFile("template_file", "bad.json")
	fw.Write([]byte(`{}`))
	mw.Close()

	req := httptest.NewRequest("POST", "/api/labs/"+id+"/templates/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	h.UploadTemplateToLab(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), ".zip or .tf")
}

// --- ServeLabWorkspaces error paths ---

func TestHandler_ServeLabWorkspaces_InvalidPath(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("GET", "/labs/x/notworkspaces", nil)
	w := httptest.NewRecorder()
	h.ServeLabWorkspaces(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandler_ServeLabWorkspaces_LabNotFound(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("GET", "/labs/nonexistent/workspaces", nil)
	w := httptest.NewRecorder()
	h.ServeLabWorkspaces(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestHandler_ServeLabWorkspaces_NotCompleted(t *testing.T) {
	jm := NewJobManager("")
	id := jm.CreateJob(&LabConfig{StackName: "test"})
	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("GET", "/labs/"+id+"/workspaces", nil)
	w := httptest.NewRecorder()
	h.ServeLabWorkspaces(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// --- WorkspaceStatus error paths ---

func TestHandler_WorkspaceStatus_WrongMethod(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("POST", "/api/student/workspace/status", nil)
	w := httptest.NewRecorder()
	h.WorkspaceStatus(w, req)
	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

func TestHandler_WorkspaceStatus_MissingParams(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("GET", "/api/student/workspace/status?lab_id=x", nil)
	w := httptest.NewRecorder()
	h.WorkspaceStatus(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandler_WorkspaceStatus_LabNotFound(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("GET", "/api/student/workspace/status?lab_id=x&workspace_name=ws&owner_id=owner", nil)
	w := httptest.NewRecorder()
	h.WorkspaceStatus(w, req)
	// Returns 200 with "unknown" status HTML
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "workspace")
}

// --- buildWorkspaceStatusHTML tests ---

func TestBuildWorkspaceStatusHTML_Running_WithURL(t *testing.T) {
	html := buildWorkspaceStatusHTML("lab1", "ws", "owner", "running", "https://coder.example.com/ws")
	assert.Contains(t, html, "workspace-ready-status--ready")
	assert.Contains(t, html, "https://coder.example.com/ws")
	assert.Contains(t, html, "Open in code-server")
}

func TestBuildWorkspaceStatusHTML_Running_NoURL(t *testing.T) {
	html := buildWorkspaceStatusHTML("lab1", "ws", "owner", "running", "")
	assert.Contains(t, html, "workspace-ready-status--ready")
	assert.NotContains(t, html, "Open in code-server")
}

func TestBuildWorkspaceStatusHTML_Failed(t *testing.T) {
	for _, status := range []string{"failed", "agents_failed"} {
		t.Run(status, func(t *testing.T) {
			html := buildWorkspaceStatusHTML("lab1", "ws", "owner", status, "")
			assert.Contains(t, html, "workspace-ready-status--error")
		})
	}
}

func TestBuildWorkspaceStatusHTML_Canceled(t *testing.T) {
	for _, status := range []string{"canceled", "canceling"} {
		t.Run(status, func(t *testing.T) {
			html := buildWorkspaceStatusHTML("lab1", "ws", "owner", status, "")
			assert.Contains(t, html, "workspace-ready-status--error")
		})
	}
}

func TestBuildWorkspaceStatusHTML_Checking(t *testing.T) {
	html := buildWorkspaceStatusHTML("lab1", "ws", "owner", "checking", "")
	assert.Contains(t, html, "workspace-ready-status--starting")
	assert.Contains(t, html, "Checking workspace status")
}

func TestBuildWorkspaceStatusHTML_AgentsStarting(t *testing.T) {
	html := buildWorkspaceStatusHTML("lab1", "ws", "owner", "agents_starting", "")
	assert.Contains(t, html, "workspace-ready-status--starting")
	assert.Contains(t, html, "agent to be ready")
}

func TestBuildWorkspaceStatusHTML_Other(t *testing.T) {
	html := buildWorkspaceStatusHTML("lab1", "ws", "owner", "pending", "")
	assert.Contains(t, html, "workspace-ready-status--starting")
	assert.Contains(t, html, "pending")
}

func TestBuildWorkspaceStatusHTML_PollURLEncoding(t *testing.T) {
	html := buildWorkspaceStatusHTML("lab/1", "ws name", "owner&id", "starting", "")
	// lab_id, workspace_name, owner_id should be URL-encoded
	assert.True(t, strings.Contains(html, "lab%2F1") || strings.Contains(html, "lab_id=lab%2F1"))
}
