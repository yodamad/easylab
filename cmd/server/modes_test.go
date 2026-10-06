package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"easylab/internal/server"

	"github.com/stretchr/testify/assert"
)

func newModeMux(mode server.Mode) *http.ServeMux {
	handler := server.NewHandler(server.NewJobManager(""), nil, nil, nil, nil, nil)
	handler.SetMode(mode)
	return buildMux(mode, handler, server.NewStudentAuthHandler())
}

// routed reports whether the mux has a route of its own for path. Anything it
// does not falls through to the "/" catch-all, which answers 404.
func routed(mux *http.ServeMux, path string) bool {
	_, pattern := mux.Handler(httptest.NewRequest(http.MethodGet, path, nil))
	return pattern != "/"
}

// Each mode serves its own areas and nothing of the other: the point of the
// split is that an in-lab portal has no admin surface at all, and an admin-only
// instance no student one.
func TestBuildMux_RoutesPerMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		path    string
		all     bool
		admin   bool
		student bool
	}{
		// Everywhere.
		{name: "health", path: "/health", all: true, admin: true, student: true},
		{name: "static assets", path: "/static/style.css", all: true, admin: true, student: true},

		// Student portal.
		{name: "student login", path: "/student/login", all: true, student: true},
		{name: "student logout", path: "/student/logout", all: true, student: true},
		{name: "student dashboard", path: "/student/dashboard", all: true, student: true},
		{name: "student workspaces page", path: "/student/workspaces", all: true, student: true},
		{name: "student feedback page", path: "/student/feedback", all: true, student: true},
		{name: "student labs API", path: "/api/student/labs", all: true, student: true},
		{name: "student templates API", path: "/api/student/labs/templates", all: true, student: true},
		{name: "student workspace request", path: "/api/student/workspace/request", all: true, student: true},
		{name: "student workspace open", path: "/api/student/workspace/open", all: true, student: true},
		{name: "student workspace delete", path: "/api/student/workspace/delete", all: true, student: true},
		{name: "student feedback API", path: "/api/student/feedback", all: true, student: true},

		// Provider sign-in starts on the same path everywhere: run here on the
		// central instance, handed to it on a portal.
		{name: "GitHub login", path: "/student/auth/github/login", all: true, admin: true, student: true},
		{name: "GitLab login", path: "/student/auth/gitlab/login", all: true, admin: true, student: true},
		{name: "Azure AD login", path: "/student/auth/azure/login", all: true, admin: true, student: true},
		// The provider callbacks are the registered ones: central only.
		{name: "GitHub callback", path: "/student/auth/github/callback", all: true, admin: true},
		{name: "GitLab callback", path: "/student/auth/gitlab/callback", all: true, admin: true},
		{name: "Azure AD callback", path: "/student/auth/azure/callback", all: true, admin: true},
		// And the broker callback is the portal's.
		{name: "broker callback", path: "/student/auth/broker/callback", student: true},

		// Admin.
		{name: "admin login", path: "/login", all: true, admin: true},
		{name: "admin logout", path: "/logout", all: true, admin: true},
		{name: "admin Azure AD login", path: "/admin/auth/azure/login", all: true, admin: true},
		{name: "admin UI", path: "/admin", all: true, admin: true},
		{name: "admin feedback", path: "/admin/feedback", all: true, admin: true},
		{name: "audit log", path: "/admin/audit-log", all: true, admin: true},
		{name: "stats", path: "/admin/stats", all: true, admin: true},
		{name: "labs list", path: "/labs", all: true, admin: true},
		{name: "lab detail", path: "/labs/job-1", all: true, admin: true},
		{name: "legacy jobs list", path: "/jobs", all: true, admin: true},
		{name: "credentials", path: "/credentials", all: true, admin: true},
		{name: "credentials API", path: "/api/credentials", all: true, admin: true},
		{name: "OVH credentials (legacy)", path: "/api/ovh-credentials", all: true, admin: true},
		{name: "create lab", path: "/api/labs", all: true, admin: true},
		{name: "lab API", path: "/api/labs/job-1/kubeconfig", all: true, admin: true},
		{name: "legacy jobs API", path: "/api/jobs/job-1", all: true, admin: true},
		{name: "destroy stack", path: "/api/stacks/destroy", all: true, admin: true},
		{name: "student portal password", path: "/api/student-portal-password", all: true, admin: true},
		{name: "GitHub login settings", path: "/api/github-auth-config", all: true, admin: true},
	}

	muxes := map[server.Mode]*http.ServeMux{
		server.ModeAll:     newModeMux(server.ModeAll),
		server.ModeAdmin:   newModeMux(server.ModeAdmin),
		server.ModeStudent: newModeMux(server.ModeStudent),
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.all, routed(muxes[server.ModeAll], tt.path), "mode all")
			assert.Equal(t, tt.admin, routed(muxes[server.ModeAdmin], tt.path), "mode admin")
			assert.Equal(t, tt.student, routed(muxes[server.ModeStudent], tt.path), "mode student")
		})
	}
}

// What a mode does not route is not merely unlinked: it answers 404.
func TestBuildMux_OtherAreaIsNotFound(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		mode server.Mode
		path string
	}{
		{name: "admin-only instance has no student dashboard", mode: server.ModeAdmin, path: "/student/dashboard"},
		{name: "admin-only instance has no student login", mode: server.ModeAdmin, path: "/student/login"},
		{name: "admin-only instance has no student API", mode: server.ModeAdmin, path: "/api/student/labs"},
		{name: "portal has no admin UI", mode: server.ModeStudent, path: "/admin"},
		{name: "portal has no admin login", mode: server.ModeStudent, path: "/login"},
		{name: "portal has no lab API", mode: server.ModeStudent, path: "/api/labs/job-1/kubeconfig"},
		{name: "portal has no credentials API", mode: server.ModeStudent, path: "/api/credentials"},
		{name: "portal runs no provider callback", mode: server.ModeStudent, path: "/student/auth/github/callback"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			newModeMux(tt.mode).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))
			assert.Equal(t, http.StatusNotFound, rec.Code)
		})
	}
}

// A portal's homepage is the portal; health answers in every mode.
func TestBuildMux_CommonRoutes(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	newModeMux(server.ModeStudent).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	assert.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Equal(t, "/student/dashboard", rec.Header().Get("Location"))

	for _, mode := range []server.Mode{server.ModeAll, server.ModeAdmin, server.ModeStudent} {
		rec := httptest.NewRecorder()
		newModeMux(mode).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
		assert.Equal(t, http.StatusOK, rec.Code, "mode %s", mode)
		assert.Equal(t, "OK", rec.Body.String(), "mode %s", mode)
	}
}

// The student portal's protected pages send an unauthenticated visitor to its
// login page — in the modes that have one.
func TestBuildMux_StudentPagesRequireSignIn(t *testing.T) {
	t.Parallel()
	for _, mode := range []server.Mode{server.ModeAll, server.ModeStudent} {
		handler := server.NewHandler(server.NewJobManager(""), nil, nil, nil, nil, nil)
		handler.SetMode(mode)
		auth := server.NewStudentAuthHandler()
		auth.ConfigurePortalAuth("job-lab", server.PortalAuthState{StudentPasswordHash: "hash"})

		rec := httptest.NewRecorder()
		buildMux(mode, handler, auth).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/student/dashboard", nil))
		assert.Equal(t, http.StatusSeeOther, rec.Code, "mode %s", mode)
		assert.Equal(t, "/student/login", rec.Header().Get("Location"), "mode %s", mode)
	}
}
