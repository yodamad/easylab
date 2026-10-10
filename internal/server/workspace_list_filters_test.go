package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"easylab/internal/providers/workspace"
)

// The Workspaces tab filters and pages its lists in the browser, from data-*
// attributes on each row and controls the server renders alongside them.
func TestLabDetail_WorkspaceListFilters(t *testing.T) {
	t.Chdir("../..") // getTemplate resolves web/ relative to the working directory

	tests := []struct {
		name       string
		workspaces []workspace.Workspace
		history    bool
		want       []string
		notWant    []string
	}{
		{
			name: "rows carry what the filters match on",
			workspaces: []workspace.Workspace{
				{ID: "ws-alice", Name: "ws-alice", Owner: "alice", OwnerEmail: "alice@example.com", Phase: workspace.PhaseRunning, Template: "go"},
				{ID: "ws-bob", Name: "ws-bob", Owner: "bob", Phase: workspace.PhaseProvisioning},
			},
			want: []string{
				`data-workspace-id="ws-alice" data-search="ws-alice alice@example.com" data-status="running" data-template="go"`,
				`data-workspace-id="ws-bob" data-search="ws-bob bob" data-status="provisioning" data-template=""`,
				`data-ws-filter="search"`,
				`data-ws-filter="status"`,
				`data-ws-page-size`,
				`data-ws-page="next"`,
			},
		},
		{
			name: "status filter offers each present status once, sorted",
			workspaces: []workspace.Workspace{
				{ID: "ws-a", Name: "ws-a", Owner: "a", Phase: workspace.PhaseRunning},
				{ID: "ws-b", Name: "ws-b", Owner: "b", Phase: workspace.PhaseProvisioning},
				{ID: "ws-c", Name: "ws-c", Owner: "c", Phase: workspace.PhaseRunning},
			},
			want:    []string{`<option value="provisioning">provisioning</option><option value="running">running</option>`},
			notWant: []string{`<option value="agents_failed">`},
		},
		{
			name:    "history rows are searchable and filterable by action",
			history: true,
			want: []string{
				`data-at="`,
				`data-search="ws-gone carol@example.com"`,
				`data-ws-filter="action"`,
			},
		},
		{
			name:    "an empty lab renders no filters",
			notWant: []string{`data-ws-filter=`, `data-ws-page-size`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jm := NewJobManager("")
			h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
			useFakeBackend(h, &fakeBackend{reachable: true, workspaces: tt.workspaces})
			labID := completedLabWithKubeconfig(jm, 0)
			if tt.history {
				require.NoError(t, jm.RecordWorkspaceEvent(labID, WorkspaceEventDeleted, "ws-gone", "ws-gone", "carol@example.com", "go"))
			}

			rec := httptest.NewRecorder()
			h.ServeLabDetail(rec, httptest.NewRequest(http.MethodGet, "/labs/"+labID, nil))
			require.Equal(t, http.StatusOK, rec.Code)
			body := rec.Body.String()

			for _, s := range tt.want {
				assert.Contains(t, body, s)
			}
			for _, s := range tt.notWant {
				assert.NotContains(t, body, s)
			}
		})
	}
}
