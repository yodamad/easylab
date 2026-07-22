package main

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResolveLabRoute(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		path   string
		method string
		format string
		want   labRoute
	}{
		// Secrets. The delete case is the one that matters: "/secrets/delete" also
		// ends with "/delete", so an ordering slip sends it to DeleteLab instead.
		{
			name:   "delete a secret is not a lab deletion",
			path:   "/api/labs/job-1/secrets/delete",
			method: http.MethodPost,
			want:   routeDeleteLabSecret,
		},
		{
			name:   "save a secret",
			path:   "/api/labs/job-1/secrets",
			method: http.MethodPost,
			want:   routeSaveLabSecret,
		},
		{
			name:   "list secrets",
			path:   "/api/labs/job-1/secrets",
			method: http.MethodGet,
			want:   routeServeLabSecrets,
		},
		{
			// The /api/jobs/ prefix is kept for backward compatibility and must route
			// identically.
			name:   "secrets under the jobs prefix",
			path:   "/api/jobs/job-1/secrets/delete",
			method: http.MethodPost,
			want:   routeDeleteLabSecret,
		},

		// Lab deletion must still reach DeleteLab.
		{
			name:   "delete a lab",
			path:   "/api/labs/job-1/delete",
			method: http.MethodPost,
			want:   routeDeleteLab,
		},

		// Workspaces.
		{
			name:   "list workspaces",
			path:   "/api/labs/job-1/workspaces",
			method: http.MethodGet,
			want:   routeListWorkspaces,
		},
		{
			name:   "delete a workspace is not a lab deletion",
			path:   "/api/labs/job-1/workspaces/ws-1/delete",
			method: http.MethodPost,
			want:   routeDeleteWorkspace,
		},

		// The rest.
		{name: "retry", path: "/api/labs/job-1/retry", method: http.MethodPost, want: routeRetryJob},
		{
			name:   "upload a template",
			path:   "/api/labs/job-1/templates/upload",
			method: http.MethodPost,
			want:   routeUploadTemplate,
		},
		{
			name:   "coder credentials",
			path:   "/api/labs/job-1/coder-credentials",
			method: http.MethodGet,
			want:   routeCoderCredentials,
		},
		{name: "kubeconfig", path: "/api/labs/job-1/kubeconfig", method: http.MethodGet, want: routeKubeconfig},

		// Fallback.
		{name: "job status", path: "/api/labs/job-1", method: http.MethodGet, want: routeJobStatus},
		{
			name:   "job status as json",
			path:   "/api/labs/job-1",
			method: http.MethodGet,
			format: "json",
			want:   routeJobStatusJSON,
		},
		{
			// A lab ID is a UUID, but nothing stops one containing a word a route
			// matches on. Suffix matching keeps that from mattering.
			name:   "a lab id ending in a route word still resolves to status",
			path:   "/api/labs/my-secrets",
			method: http.MethodGet,
			want:   routeJobStatus,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, resolveLabRoute(tt.path, tt.method, tt.format),
				"%s %s", tt.method, tt.path)
		})
	}
}

// The secrets routes were, at one point, declared after the generic "/delete"
// case and were consequently unreachable. Nothing else catches that: the handler
// is correct, its own tests pass, and only the routing is wrong.
func TestResolveLabRoute_SecretsAreNotShadowedByDelete(t *testing.T) {
	t.Parallel()

	got := resolveLabRoute("/api/labs/job-1/secrets/delete", http.MethodPost, "")
	assert.NotEqual(t, routeDeleteLab, got,
		"deleting a credential must not resolve to the lab-deletion route")
	assert.Equal(t, routeDeleteLabSecret, got)
}

// GET on a write-only path must not fall through to the job-status handler and
// answer 200 with a lab's status.
func TestResolveLabRoute_MethodIsPartOfTheMatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		path   string
		method string
		want   labRoute
	}{
		{
			name:   "GET on secrets/delete falls through rather than deleting",
			path:   "/api/labs/job-1/secrets/delete",
			method: http.MethodGet,
			want:   routeJobStatus,
		},
		{
			name:   "GET on delete falls through rather than deleting",
			path:   "/api/labs/job-1/delete",
			method: http.MethodGet,
			want:   routeJobStatus,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, resolveLabRoute(tt.path, tt.method, ""))
		})
	}
}
