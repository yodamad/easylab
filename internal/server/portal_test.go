package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"easylab/coder"
	"easylab/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPortalImage(t *testing.T) {
	tests := []struct {
		name     string
		env      string
		version  string
		expected string
	}{
		{name: "override wins", env: "registry.example.com/easylab:custom", version: "1.2.3", expected: "registry.example.com/easylab:custom"},
		{name: "release image of this version", version: "1.2.3", expected: "docker.io/yodamad/easylab:v1.2.3"},
		{name: "version already prefixed", version: "v1.2.3", expected: "docker.io/yodamad/easylab:v1.2.3"},
		{name: "development build has no image", version: "dev", expected: ""},
		{name: "unknown version has no image", version: "", expected: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(EnvPortalImage, tt.env)
			previous := Version
			Version = tt.version
			t.Cleanup(func() { Version = previous })

			assert.Equal(t, tt.expected, portalImage())
		})
	}
}

// The portal may read exactly the credentials the templates name.
func TestTemplateSecretNames(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		templates []WorkspaceTemplate
		expected  []string
	}{
		{name: "no templates", expected: nil},
		{name: "no credentials", templates: []WorkspaceTemplate{{Name: "go"}}, expected: nil},
		{
			name: "git, registry and config repo credentials, de-duplicated",
			templates: []WorkspaceTemplate{
				{Name: "go", GitAuthSecret: "git-token"},
				{Name: "dev", GitAuthSecret: " git-token ", Devcontainer: &DevcontainerConfig{RegistryAuthSecret: "registry-creds", ConfigAuthSecret: "config-token"}},
			},
			expected: []string{"git-token", "registry-creds", "config-token"},
		},
		{
			// The kubelet resolves these; the portal never reads them.
			name:      "image pull secrets are not included",
			templates: []WorkspaceTemplate{{Name: "go", ImagePullSecrets: []string{"pull-secret"}}},
			expected:  nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, templateSecretNames(tt.templates))
		})
	}
}

func TestReconcilePortal_GrantsOnlyTemplateSecrets(t *testing.T) {
	t.Setenv(EnvPortalImage, testPortalImage)
	h, id, fb := newPortalLab(t, &LabConfig{StackName: "workshop", Domain: "lab.example.com", WorkspaceTemplates: []WorkspaceTemplate{
		{Name: "go", GitAuthSecret: "git-token"},
	}})

	h.reconcilePortal(id)

	specs := fb.ensuredSpecs()
	require.Len(t, specs, 1)
	assert.Equal(t, []string{"git-token"}, specs[0].SecretNames)
}

func TestReconcilePortal_Deploys(t *testing.T) {
	tests := []struct {
		name         string
		config       *LabConfig
		wantWildcard string
		wantIssuer   string
		wantCache    int
	}{
		{
			name:       "domain without DNS provider gets a per-host certificate",
			config:     &LabConfig{StackName: "workshop", Domain: "lab.example.com"},
			wantIssuer: utils.DefaultClusterIssuerName,
		},
		{
			name:         "DNS provider serves the portal from the wildcard certificate",
			config:       &LabConfig{StackName: "workshop", Domain: "lab.example.com", DNSProvider: "ovh", ClusterIssuerName: "custom-issuer"},
			wantWildcard: coder.WildcardTLSSecretName,
			wantIssuer:   "custom-issuer",
		},
		{
			name: "in-cluster build cache is provisioned on the portal's behalf",
			config: &LabConfig{StackName: "workshop", Domain: "lab.example.com", WorkspaceTemplates: []WorkspaceTemplate{
				{Name: "dev", Devcontainer: &DevcontainerConfig{Enabled: true, UseInClusterCache: true}},
			}},
			wantIssuer: utils.DefaultClusterIssuerName,
			wantCache:  1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(EnvPortalImage, testPortalImage)
			h, id, fb := newPortalLab(t, tt.config)
			h.SetPortalAuth("https://admin.example.com/", func() StudentAuthSnapshot {
				return StudentAuthSnapshot{StudentPasswordHash: "bcrypt-hash", GitHub: true}
			})

			h.reconcilePortal(id)

			specs := fb.ensuredSpecs()
			require.Len(t, specs, 1)
			spec := specs[0]
			assert.Equal(t, id, spec.LabID)
			assert.Equal(t, testPortalImage, spec.Image)
			assert.Equal(t, "lab.example.com", spec.Domain)
			assert.Equal(t, tt.wantWildcard, spec.WildcardTLSSecret)
			assert.Equal(t, tt.wantIssuer, spec.ClusterIssuer)
			assert.Equal(t, tt.wantCache, fb.buildCacheCalls)
			assert.Empty(t, h.portalError(id))

			// The state it was deployed with is the lab's, and can broker sign-ins:
			// a broker secret was generated and the public URL carried over.
			state, err := decodePortalState(spec.Config)
			require.NoError(t, err)
			assert.Equal(t, id, state.LabID)
			assert.Equal(t, "https://admin.example.com", state.Auth.AdminURL)
			assert.True(t, state.Auth.GitHub)
			assert.Equal(t, "bcrypt-hash", state.Auth.StudentPasswordHash)
			require.NotEmpty(t, state.Auth.BrokerSecret)

			job, _ := h.jobManager.GetJob(id)
			job.mu.RLock()
			assert.Equal(t, state.Auth.BrokerSecret, job.PortalBrokerSecret)
			job.mu.RUnlock()

			// A second reconcile keeps the same secret: rotating it would sign every
			// student in the middle of a sign-in out of luck.
			h.reconcilePortal(id)
			again, err := decodePortalState(fb.ensuredSpecs()[1].Config)
			require.NoError(t, err)
			assert.Equal(t, state.Auth.BrokerSecret, again.Auth.BrokerSecret)
		})
	}
}

func TestReconcilePortal_Skips(t *testing.T) {
	tests := []struct {
		name  string
		setup func(h *Handler, job *Job)
	}{
		{name: "lab did not ask for a portal", setup: func(_ *Handler, job *Job) { job.Config.StudentPortal = false }},
		{name: "lab is not deployed", setup: func(_ *Handler, job *Job) { job.Status = JobStatusRunning }},
		{name: "lab has no cluster", setup: func(_ *Handler, job *Job) { job.Kubeconfig = "" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(EnvPortalImage, testPortalImage)
			h, id, fb := newPortalLab(t, nil)
			job, _ := h.jobManager.GetJob(id)
			job.mu.Lock()
			tt.setup(h, job)
			job.mu.Unlock()

			h.reconcilePortal(id)
			h.syncPortalState(id)

			assert.Empty(t, fb.ensuredSpecs())
			assert.Zero(t, fb.syncCalls)
		})
	}

	t.Run("unknown lab", func(t *testing.T) {
		h, _, fb := newPortalLab(t, nil)
		h.reconcilePortal("job-missing")
		h.syncPortalState("job-missing")
		h.removePortal("job-missing")
		h.drainPortalOutbox(context.Background(), "job-missing")
		assert.Empty(t, fb.ensuredSpecs())
	})

	// A backend that cannot host a portal is skipped rather than failed.
	t.Run("backend without portal support", func(t *testing.T) {
		t.Setenv(EnvPortalImage, testPortalImage)
		h, id, _ := newPortalLab(t, nil)
		useFakeBackend(h, &fakeBackend{reachable: true})
		h.evictWorkspaceBackend(id)

		h.reconcilePortal(id)
		assert.Empty(t, h.portalError(id))
		assert.False(t, h.portalDisplayFor(context.Background(), mustJob(t, h, id)).Supported)
	})
}

func mustJob(t *testing.T, h *Handler, id string) *Job {
	t.Helper()
	job, exists := h.jobManager.GetJob(id)
	require.True(t, exists)
	return job
}

// A failed deployment is kept for the admin, and cleared by the next success.
func TestReconcilePortal_RecordsFailure(t *testing.T) {
	t.Run("no image for a development build", func(t *testing.T) {
		t.Setenv(EnvPortalImage, "")
		previous := Version
		Version = "dev"
		t.Cleanup(func() { Version = previous })

		h, id, fb := newPortalLab(t, nil)
		h.reconcilePortal(id)

		assert.Empty(t, fb.ensuredSpecs())
		assert.Contains(t, h.portalError(id), EnvPortalImage)
	})

	t.Run("cluster refuses", func(t *testing.T) {
		t.Setenv(EnvPortalImage, testPortalImage)
		h, id, fb := newPortalLab(t, nil)
		fb.portalEnsureErr = fmt.Errorf("portal host portal.lab.example.com is already used by lab job-other")

		h.reconcilePortal(id)
		assert.Contains(t, h.portalError(id), "already used")
		display := h.portalDisplayFor(context.Background(), mustJob(t, h, id))
		assert.True(t, display.Enabled)
		assert.False(t, display.Deployed)
		assert.Contains(t, display.Error, "already used")

		fb.portalMu.Lock()
		fb.portalEnsureErr = nil
		fb.portalMu.Unlock()
		h.reconcilePortal(id)
		assert.Empty(t, h.portalError(id))
		display = h.portalDisplayFor(context.Background(), mustJob(t, h, id))
		assert.True(t, display.Deployed)
		assert.True(t, display.Ready)
		assert.Equal(t, "https://portal.lab.example.com", display.URL)
	})
}

// Every change an admin makes to the lab reaches the portal's state.
func TestSyncPortalState(t *testing.T) {
	t.Setenv(EnvPortalImage, testPortalImage)
	h, id, fb := newPortalLab(t, &LabConfig{StackName: "workshop", Domain: "lab.example.com", WorkspaceTemplates: []WorkspaceTemplate{{Name: "go"}}})
	h.reconcilePortal(id)

	job := mustJob(t, h, id)
	job.mu.Lock()
	job.Config.Disabled = true
	job.mu.Unlock()
	require.NoError(t, h.jobManager.RecordWorkspaceAccess(id, "ws-alice", "alice"))

	h.syncPortalState(id)

	config, err := fb.ReadPortalConfig(context.Background(), id)
	require.NoError(t, err)
	state, err := decodePortalState(config)
	require.NoError(t, err)
	assert.True(t, state.Disabled)
	assert.Equal(t, "alice", state.WorkspaceAccesses["ws-alice"].Owner)
	// A sync never redeploys.
	assert.Len(t, fb.ensuredSpecs(), 1)
}

// updateJobConfig is the choke point every config change goes through; it must
// bring the portal along without the caller asking.
func TestUpdateJobConfig_ReconcilesPortal(t *testing.T) {
	t.Setenv(EnvPortalImage, testPortalImage)
	h, id, fb := newPortalLab(t, nil)

	h.updateJobConfig(id, func(config *LabConfig) { config.Disabled = true })

	require.Eventually(t, func() bool { return len(fb.ensuredSpecs()) == 1 }, 2*time.Second, 10*time.Millisecond)
	state, err := decodePortalState(fb.ensuredSpecs()[0].Config)
	require.NoError(t, err)
	assert.True(t, state.Disabled)
}

func putOutbox(t *testing.T, fb *fakePortalBackend, id string, record portalRecord) {
	t.Helper()
	raw, err := json.Marshal(record)
	require.NoError(t, err)
	fb.portalMu.Lock()
	fb.outbox[id] = string(raw)
	fb.portalMu.Unlock()
}

// What the portal reports ends up where the same action on the central instance
// would have put it — under the drained lab, whatever the record claims.
func TestDrainPortalOutbox(t *testing.T) {
	h, id, fb := newPortalLab(t, nil)
	fs, err := NewFeedbackStore(t.TempDir())
	require.NoError(t, err)
	h.feedbackStore = fs
	as, err := NewAuditStore(t.TempDir())
	require.NoError(t, err)
	h.SetAuditStore(as)

	createdAt := time.Date(2030, 1, 1, 10, 0, 0, 0, time.UTC)
	require.NoError(t, h.jobManager.RecordWorkspaceAccess(id, "ws-alice", "alice"))

	putOutbox(t, fb, "001", portalRecord{Kind: portalRecordWorkspaceEvent, WorkspaceEvent: &WorkspaceEvent{
		At: createdAt, Action: WorkspaceEventCreated, WorkspaceID: "ws-alice", WorkspaceName: "ws-alice", Owner: "alice@example.com", Template: "go",
	}})
	putOutbox(t, fb, "002", portalRecord{Kind: portalRecordFeedback, Feedback: &Feedback{
		ID: "fb-1", LabID: "../../etc/other-lab", Email: "alice@example.com", Rating: 5, Difficulty: "just-right",
	}})
	putOutbox(t, fb, "003", portalRecord{Kind: portalRecordAudit, Audit: &AuditEntry{
		At: createdAt, Actor: "alice@example.com", Role: "admin", Action: "workspace.create", LabID: "job-other", Detail: "ws-alice",
	}})
	putOutbox(t, fb, "004", portalRecord{Kind: portalRecordWorkspaceEvent, WorkspaceEvent: &WorkspaceEvent{
		At: createdAt.Add(time.Hour), Action: WorkspaceEventDeleted, WorkspaceID: "ws-alice", WorkspaceName: "ws-alice", Owner: "alice@example.com",
	}})
	// Records that can never be applied are dropped, not retried forever.
	fb.portalMu.Lock()
	fb.outbox["005"] = "not json"
	fb.outbox["006"] = `{"kind":"mystery"}`
	fb.outbox["007"] = `{"kind":"feedback"}`
	fb.portalMu.Unlock()

	h.drainPortalOutbox(context.Background(), id)
	assert.Empty(t, fb.outboxRecords())

	job := mustJob(t, h, id)
	job.mu.RLock()
	require.Len(t, job.WorkspaceEvents, 2)
	assert.True(t, job.WorkspaceEvents[0].At.Equal(createdAt), "the event keeps the time it happened at")
	assert.Equal(t, "alice@example.com", job.WorkspaceEvents[0].Owner)
	assert.Equal(t, WorkspaceEventDeleted, job.WorkspaceEvents[1].Action)
	// A deletion clears the teacher-access record, as it does on the central instance.
	assert.NotContains(t, job.WorkspaceAccesses, "ws-alice")
	job.mu.RUnlock()

	feedback, err := fs.GetByLab(id)
	require.NoError(t, err)
	require.Len(t, feedback, 1)
	assert.Equal(t, id, feedback[0].LabID, "the record's own lab ID is never trusted")
	assert.Equal(t, 5, feedback[0].Rating)

	entries, err := as.Recent(0)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, id, entries[0].LabID)
	assert.Equal(t, "student", entries[0].Role, "a portal cannot file admin actions")
	assert.True(t, entries[0].At.Equal(createdAt))
}

// The outbox is delivered at least once: a record seen twice is applied once.
func TestDrainPortalOutbox_IsIdempotent(t *testing.T) {
	h, id, fb := newPortalLab(t, nil)
	fs, err := NewFeedbackStore(t.TempDir())
	require.NoError(t, err)
	h.feedbackStore = fs

	at := time.Date(2030, 1, 1, 10, 0, 0, 0, time.UTC)
	event := portalRecord{Kind: portalRecordWorkspaceEvent, WorkspaceEvent: &WorkspaceEvent{At: at, Action: WorkspaceEventCreated, WorkspaceID: "ws-alice"}}
	feedback := portalRecord{Kind: portalRecordFeedback, Feedback: &Feedback{ID: "fb-1", Rating: 4}}

	for range 2 {
		putOutbox(t, fb, "001", event)
		putOutbox(t, fb, "002", feedback)
		h.drainPortalOutbox(context.Background(), id)
	}

	job := mustJob(t, h, id)
	job.mu.RLock()
	assert.Len(t, job.WorkspaceEvents, 1)
	job.mu.RUnlock()
	stored, err := fs.GetByLab(id)
	require.NoError(t, err)
	assert.Len(t, stored, 1)
}

func TestRemovePortal_CollectsBeforeDeleting(t *testing.T) {
	t.Setenv(EnvPortalImage, testPortalImage)
	h, id, fb := newPortalLab(t, nil)
	h.reconcilePortal(id)
	putOutbox(t, fb, "001", portalRecord{Kind: portalRecordWorkspaceEvent, WorkspaceEvent: &WorkspaceEvent{
		At: time.Now(), Action: WorkspaceEventCreated, WorkspaceID: "ws-alice",
	}})

	h.removePortal(id)

	assert.Equal(t, 1, fb.removeCalls)
	job := mustJob(t, h, id)
	job.mu.RLock()
	assert.Len(t, job.WorkspaceEvents, 1, "what the portal still held is not lost with it")
	job.mu.RUnlock()
	// And a removed portal can no longer be handed a sign-in.
	_, _, ok := h.BrokerPortal(id)
	assert.False(t, ok)
}

func TestBrokerPortal(t *testing.T) {
	t.Setenv(EnvPortalImage, testPortalImage)

	t.Run("deployed portal", func(t *testing.T) {
		h, id, _ := newPortalLab(t, nil)
		h.reconcilePortal(id)

		portalURL, secret, ok := h.BrokerPortal(id)
		require.True(t, ok)
		assert.Equal(t, "https://portal.lab.example.com", portalURL)
		assert.NotEmpty(t, secret)
	})

	// After a restart the URL is not cached yet: it is read back from the cluster.
	t.Run("URL read from the cluster", func(t *testing.T) {
		h, id, _ := newPortalLab(t, nil)
		h.reconcilePortal(id)
		h.setPortalURL(id, "")

		portalURL, _, ok := h.BrokerPortal(id)
		require.True(t, ok)
		assert.Equal(t, "https://portal.lab.example.com", portalURL)
	})

	refused := []struct {
		name  string
		setup func(h *Handler, id string, fb *fakePortalBackend) string
	}{
		{name: "unknown lab", setup: func(*Handler, string, *fakePortalBackend) string { return "job-missing" }},
		{name: "never deployed", setup: func(_ *Handler, id string, _ *fakePortalBackend) string { return id }},
		{name: "portal switched off", setup: func(h *Handler, id string, _ *fakePortalBackend) string {
			h.reconcilePortal(id)
			job, _ := h.jobManager.GetJob(id)
			job.mu.Lock()
			job.Config.StudentPortal = false
			job.mu.Unlock()
			return id
		}},
		{name: "portal has no public address", setup: func(h *Handler, id string, fb *fakePortalBackend) string {
			fb.url = ""
			h.reconcilePortal(id)
			return id
		}},
	}
	for _, tt := range refused {
		t.Run(tt.name, func(t *testing.T) {
			h, id, fb := newPortalLab(t, nil)
			_, _, ok := h.BrokerPortal(tt.setup(h, id, fb))
			assert.False(t, ok)
		})
	}
}

func TestSetLabPortal(t *testing.T) {
	tests := []struct {
		name        string
		method      string
		path        string // "%s" is replaced by the lab created for the test
		body        string
		setup       func(job *Job, fb *fakePortalBackend)
		wantStatus  int
		wantEnabled bool
		wantEnsured int
		wantRemoved int
	}{
		{
			name: "enable deploys", method: http.MethodPost, path: "/api/labs/%s/portal", body: "enabled=true",
			setup:      func(job *Job, _ *fakePortalBackend) { job.Config.StudentPortal = false },
			wantStatus: http.StatusOK, wantEnabled: true, wantEnsured: 1,
		},
		{
			name: "enable on an enabled lab redeploys", method: http.MethodPost, path: "/api/jobs/%s/portal", body: "enabled=true",
			wantStatus: http.StatusOK, wantEnabled: true, wantEnsured: 1,
		},
		{
			name: "disable removes", method: http.MethodPost, path: "/api/labs/%s/portal", body: "enabled=false",
			wantStatus: http.StatusOK, wantRemoved: 1,
		},
		{
			name: "deployment failure is reported without its cause", method: http.MethodPost, path: "/api/labs/%s/portal", body: "enabled=true",
			setup: func(_ *Job, fb *fakePortalBackend) {
				fb.portalEnsureErr = fmt.Errorf("forbidden: secrets in namespace kube-system")
			},
			wantStatus: http.StatusBadGateway, wantEnabled: true, wantEnsured: 1,
		},
		{
			name: "lab not deployed yet", method: http.MethodPost, path: "/api/labs/%s/portal", body: "enabled=true",
			setup: func(job *Job, _ *fakePortalBackend) {
				job.Config.StudentPortal = false
				job.Status = JobStatusRunning
			},
			wantStatus: http.StatusConflict,
		},
		{name: "missing flag", method: http.MethodPost, path: "/api/labs/%s/portal", body: "", wantStatus: http.StatusBadRequest, wantEnabled: true},
		{name: "invalid flag", method: http.MethodPost, path: "/api/labs/%s/portal", body: "enabled=maybe", wantStatus: http.StatusBadRequest, wantEnabled: true},
		{name: "wrong method", method: http.MethodGet, path: "/api/labs/%s/portal", wantStatus: http.StatusMethodNotAllowed, wantEnabled: true},
		{name: "unknown lab", method: http.MethodPost, path: "/api/labs/job-missing/portal", body: "enabled=true", wantStatus: http.StatusNotFound, wantEnabled: true},
		{name: "malformed path", method: http.MethodPost, path: "/api/labs/%s/extra/portal", body: "enabled=true", wantStatus: http.StatusBadRequest, wantEnabled: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(EnvPortalImage, testPortalImage)
			h, id, fb := newPortalLab(t, nil)
			job := mustJob(t, h, id)
			if tt.setup != nil {
				job.mu.Lock()
				tt.setup(job, fb)
				job.mu.Unlock()
			}

			path := tt.path
			if strings.Contains(path, "%s") {
				path = fmt.Sprintf(path, id)
			}
			req := httptest.NewRequest(tt.method, path, strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			w := httptest.NewRecorder()
			h.SetLabPortal(w, req)

			require.Equal(t, tt.wantStatus, w.Code)
			assert.NotContains(t, w.Body.String(), "kube-system", "internal error details stay server-side")
			assert.Len(t, fb.ensuredSpecs(), tt.wantEnsured)
			assert.Equal(t, tt.wantRemoved, fb.removeCalls)

			job.mu.RLock()
			enabled := job.Config.StudentPortal
			job.mu.RUnlock()
			assert.Equal(t, tt.wantEnabled, enabled)

			if tt.wantStatus == http.StatusOK {
				var body map[string]interface{}
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
				assert.Equal(t, true, body["success"])
				assert.Equal(t, tt.wantEnabled, body["enabled"])
			}
		})
	}
}

// The wizard's checkbox is what sets the flag at creation.
func TestCreateLabConfigFromForm_StudentPortal(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		value    string
		expected bool
	}{
		{name: "checked", value: "true", expected: true},
		{name: "unchecked", value: "", expected: false},
		{name: "anything else", value: "on", expected: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			form := url.Values{"stack_name": {"workshop"}}
			if tt.value != "" {
				form.Set("student_portal", tt.value)
			}
			req := httptest.NewRequest(http.MethodPost, "/api/labs", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			require.NoError(t, req.ParseForm())

			h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
			config := h.createLabConfigFromForm(req, nil)
			assert.Equal(t, tt.expected, config.StudentPortal)
		})
	}
}

// A new lab gets its own portal without the admin doing anything — except on a
// build that has no image to run one from, where the option is off and says why.
func TestServeAdminUI_StudentPortalDefault(t *testing.T) {
	t.Chdir("../..")

	tests := []struct {
		name        string
		image       string
		version     string
		wantChecked bool
	}{
		{name: "release build: on by default", version: "1.2.3", wantChecked: true},
		{name: "development build with an image override: on by default", image: testPortalImage, version: "dev", wantChecked: true},
		{name: "development build: unavailable", version: "dev", wantChecked: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(EnvPortalImage, tt.image)
			previous := Version
			Version = tt.version
			t.Cleanup(func() { Version = previous })

			h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
			w := httptest.NewRecorder()
			h.ServeAdminUI(w, httptest.NewRequest(http.MethodGet, "/admin", nil))

			require.Equal(t, http.StatusOK, w.Code)
			body := w.Body.String()
			if tt.wantChecked {
				assert.Contains(t, body, `name="student_portal" value="true" checked>`)
				assert.NotContains(t, body, "Not available on this build")
			} else {
				assert.Contains(t, body, `name="student_portal" value="true" disabled>`)
				assert.Contains(t, body, "Not available on this build")
			}
		})
	}
}

// The broker secret is a credential: encrypted on disk, absent from API
// responses, and back in the clear after a restart.
func TestJob_PortalBrokerSecretAtRest(t *testing.T) {
	require.NoError(t, InitDataEncryption([]byte(strings.Repeat("k", 32))))
	t.Cleanup(func() { InitDataEncryption(nil) })

	dataDir := t.TempDir()
	jm := NewJobManager(dataDir)
	id := jm.CreateJob(&LabConfig{StackName: "workshop", StudentPortal: true})
	require.NoError(t, jm.UpdateJobStatus(id, JobStatusCompleted))
	job, _ := jm.GetJob(id)
	job.mu.Lock()
	job.PortalBrokerSecret = "broker-secret-value"
	job.mu.Unlock()

	job.mu.RLock()
	exposed, err := job.sanitizedCopy(false)
	job.mu.RUnlock()
	require.NoError(t, err)
	assert.Empty(t, exposed.PortalBrokerSecret)

	require.NoError(t, jm.SaveJob(id))
	raw, err := os.ReadFile(filepath.Join(dataDir, "jobs", id+".json"))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "broker-secret-value")
	assert.Contains(t, string(raw), "portal_broker_secret")

	reloaded := NewJobManager(dataDir)
	require.NoError(t, reloaded.LoadJobs())
	loaded, exists := reloaded.GetJob(id)
	require.True(t, exists)
	assert.Equal(t, "broker-secret-value", loaded.PortalBrokerSecret)
	assert.True(t, loaded.Config.StudentPortal)
}

func TestImportWorkspaceEvent(t *testing.T) {
	t.Parallel()
	jm := NewJobManager("")
	id := jm.CreateJob(&LabConfig{StackName: "workshop"})
	at := time.Date(2030, 1, 1, 10, 0, 0, 0, time.UTC)
	event := WorkspaceEvent{At: at, Action: WorkspaceEventCreated, WorkspaceID: "ws-alice"}

	added, err := jm.ImportWorkspaceEvent(id, event)
	require.NoError(t, err)
	assert.True(t, added)

	added, err = jm.ImportWorkspaceEvent(id, event)
	require.NoError(t, err)
	assert.False(t, added, "the same event is not added twice")

	// Same workspace, later: a different event.
	added, err = jm.ImportWorkspaceEvent(id, WorkspaceEvent{At: at.Add(time.Minute), Action: WorkspaceEventDeleted, WorkspaceID: "ws-alice"})
	require.NoError(t, err)
	assert.True(t, added)

	_, err = jm.ImportWorkspaceEvent("job-missing", event)
	require.Error(t, err)
}

func TestFeedbackStore_AddIfAbsent(t *testing.T) {
	t.Parallel()
	fs, err := NewFeedbackStore(t.TempDir())
	require.NoError(t, err)

	added, err := fs.AddIfAbsent(Feedback{ID: "fb-1", LabID: "lab-1", Rating: 5})
	require.NoError(t, err)
	assert.True(t, added)
	added, err = fs.AddIfAbsent(Feedback{ID: "fb-1", LabID: "lab-1", Rating: 1})
	require.NoError(t, err)
	assert.False(t, added)

	entries, err := fs.GetByLab("lab-1")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, 5, entries[0].Rating)
}

// The lab detail page shows the portal, and renders for labs with and without one.
func TestServeLabDetail_StudentPortal(t *testing.T) {
	t.Chdir("../..")
	t.Setenv(EnvPortalImage, testPortalImage)

	tests := []struct {
		name         string
		enabled      bool
		publicURL    string
		contains     []string
		doesNotMatch []string
	}{
		{
			name: "deployed portal", enabled: true, publicURL: "https://admin.example.com",
			contains:     []string{"Student portal &middot; running", "https://portal.lab.example.com/student/login", "Redeploy", "Remove portal"},
			doesNotMatch: []string{"Password sign-in only", "Deploy student portal"},
		},
		{
			name: "deployed portal without a public URL", enabled: true,
			contains: []string{"Student portal &middot; running", "Password sign-in only"},
		},
		{
			name:         "no portal",
			contains:     []string{"No dedicated student portal", "Deploy student portal", "sign in on this instance"},
			doesNotMatch: []string{"Remove portal"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, id, _ := newPortalLab(t, nil)
			h.SetPortalAuth(tt.publicURL, nil)
			if tt.enabled {
				h.reconcilePortal(id)
			} else {
				job := mustJob(t, h, id)
				job.mu.Lock()
				job.Config.StudentPortal = false
				job.mu.Unlock()
			}

			w := httptest.NewRecorder()
			h.ServeLabDetail(w, httptest.NewRequest(http.MethodGet, "/labs/"+id, nil))

			require.Equal(t, http.StatusOK, w.Code)
			body := w.Body.String()
			for _, want := range tt.contains {
				assert.Contains(t, body, want)
			}
			for _, unwanted := range tt.doesNotMatch {
				assert.NotContains(t, body, unwanted)
			}
		})
	}
}
