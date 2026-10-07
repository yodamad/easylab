package server

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"

	"easylab/internal/providers/workspace"

	"github.com/stretchr/testify/require"
)

const testPortalImage = "docker.io/yodamad/easylab:v9.9.9"

// fakePortalBackend is a fakeBackend that can also host a student portal: it
// keeps the portal's config and outbox in memory, standing in for both sides of
// the cluster resources (workspace.PortalDeployer for the admin,
// workspace.PortalRuntime for the portal itself).
type fakePortalBackend struct {
	fakeBackend

	portalMu sync.Mutex
	// config is nil until a portal is deployed.
	config map[string][]byte
	outbox map[string]string
	url    string

	ensured         []workspace.PortalSpec
	syncCalls       int
	removeCalls     int
	buildCacheCalls int

	portalEnsureErr error
	appendErr       error
	statusErr       error
}

func newFakePortalBackend() *fakePortalBackend {
	return &fakePortalBackend{
		fakeBackend: fakeBackend{reachable: true},
		outbox:      map[string]string{},
		url:         "https://lab.example.com",
	}
}

func (f *fakePortalBackend) EnsurePortal(_ context.Context, spec workspace.PortalSpec) (string, error) {
	f.portalMu.Lock()
	defer f.portalMu.Unlock()
	f.ensured = append(f.ensured, spec)
	if f.portalEnsureErr != nil {
		return "", f.portalEnsureErr
	}
	f.config = spec.Config
	return f.url, nil
}

func (f *fakePortalBackend) SyncPortalConfig(_ context.Context, _ string, config map[string][]byte) error {
	f.portalMu.Lock()
	defer f.portalMu.Unlock()
	f.syncCalls++
	if f.config != nil {
		f.config = config
	}
	return nil
}

func (f *fakePortalBackend) DrainPortalOutbox(_ context.Context, _ string, apply func(id, record string) error) error {
	f.portalMu.Lock()
	ids := make([]string, 0, len(f.outbox))
	for id := range f.outbox {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	records := make(map[string]string, len(f.outbox))
	for id, record := range f.outbox {
		records[id] = record
	}
	f.portalMu.Unlock()

	for _, id := range ids {
		if err := apply(id, records[id]); err != nil {
			continue
		}
		f.portalMu.Lock()
		delete(f.outbox, id)
		f.portalMu.Unlock()
	}
	return nil
}

func (f *fakePortalBackend) RemovePortal(_ context.Context, _ string) error {
	f.portalMu.Lock()
	defer f.portalMu.Unlock()
	f.removeCalls++
	f.config = nil
	return nil
}

func (f *fakePortalBackend) PortalStatus(_ context.Context, _ string) (workspace.PortalStatus, error) {
	f.portalMu.Lock()
	defer f.portalMu.Unlock()
	if f.statusErr != nil {
		return workspace.PortalStatus{}, f.statusErr
	}
	if f.config == nil {
		return workspace.PortalStatus{}, nil
	}
	return workspace.PortalStatus{Deployed: true, Ready: true, URL: f.url, Image: testPortalImage}, nil
}

func (f *fakePortalBackend) ReadPortalConfig(_ context.Context, _ string) (map[string][]byte, error) {
	f.portalMu.Lock()
	defer f.portalMu.Unlock()
	if f.config == nil {
		return nil, fmt.Errorf("portal config not found")
	}
	return f.config, nil
}

func (f *fakePortalBackend) AppendPortalOutbox(_ context.Context, _ string, records map[string]string) error {
	f.portalMu.Lock()
	defer f.portalMu.Unlock()
	if f.appendErr != nil {
		return f.appendErr
	}
	for id, record := range records {
		f.outbox[id] = record
	}
	return nil
}

// The three methods below make it a workspace.RegistryCacheProvider, so tests can
// see whether the admin provisioned the build cache on the portal's behalf.
func (f *fakePortalBackend) EnsureBuildCache(context.Context) (string, error) {
	f.portalMu.Lock()
	defer f.portalMu.Unlock()
	f.buildCacheCalls++
	return "registry.local/cache", nil
}

func (f *fakePortalBackend) EnsureRegistryIngress(context.Context, string, string, string) (string, error) {
	return "", nil
}

func (f *fakePortalBackend) BakedImageRepo(string, string, string) (string, string) {
	return "", ""
}

func (f *fakePortalBackend) outboxRecords() map[string]string {
	f.portalMu.Lock()
	defer f.portalMu.Unlock()
	out := make(map[string]string, len(f.outbox))
	for id, record := range f.outbox {
		out[id] = record
	}
	return out
}

func (f *fakePortalBackend) ensuredSpecs() []workspace.PortalSpec {
	f.portalMu.Lock()
	defer f.portalMu.Unlock()
	return append([]workspace.PortalSpec(nil), f.ensured...)
}

// newPortalLab returns an admin-side handler with one deployed (completed) lab
// that asked for a student portal, wired to a fake cluster.
func newPortalLab(t *testing.T, config *LabConfig) (*Handler, string, *fakePortalBackend) {
	t.Helper()
	if config == nil {
		config = &LabConfig{StackName: "workshop", Domain: "lab.example.com"}
	}
	config.StudentPortal = true

	jm := NewJobManager("")
	id := jm.CreateJob(config)
	require.NoError(t, jm.UpdateJobStatus(id, JobStatusCompleted))
	job, _ := jm.GetJob(id)
	job.mu.Lock()
	job.Kubeconfig = "fake-kubeconfig"
	job.mu.Unlock()

	fb := newFakePortalBackend()
	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	h.newWorkspaceBackend = func(_, _ string) (workspace.Backend, error) { return fb, nil }
	return h, id, fb
}

// newTestPortal returns a student-mode handler serving labID from fb, with the
// given state already synced into the fake cluster and loaded.
func newTestPortal(t *testing.T, fb *fakePortalBackend, state PortalState) (*Handler, *PortalRuntime) {
	t.Helper()
	config, err := encodePortalState(state)
	require.NoError(t, err)
	fb.portalMu.Lock()
	fb.config = config
	fb.portalMu.Unlock()

	jm := NewJobManager("")
	h := NewHandler(jm, nil, nil, nil, nil, nil)
	h.SetMode(ModeStudent)
	p, err := NewPortalRuntime(state.LabID, fb, jm, nil)
	require.NoError(t, err)
	h.UsePortalRuntime(p)
	require.NoError(t, p.Load(context.Background()))
	return h, p
}
