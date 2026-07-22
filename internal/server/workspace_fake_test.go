package server

import (
	"context"
	"fmt"

	"easylab/internal/providers/workspace"
)

// fakeBackend is an in-memory workspace.Backend used in tests. It records calls
// so assertions can verify cleanup / handler behaviour without a real cluster.
type fakeBackend struct {
	reachable  bool
	workspaces []workspace.Workspace
	getWS      *workspace.Workspace
	getErr     error
	ensureErr  error
	listErr    error
	deleteErr  error

	// routingDomain/routingScheme stand in for the backend's fallback resolution
	// when the lab has no domain of its own.
	routingDomain string
	routingScheme string

	DeleteCalls []string
	Ensured     []workspace.Spec
}

func (f *fakeBackend) EnsureWorkspace(_ context.Context, spec workspace.Spec) (workspace.Workspace, error) {
	f.Ensured = append(f.Ensured, spec)
	if f.ensureErr != nil {
		return workspace.Workspace{}, f.ensureErr
	}
	if f.getWS != nil {
		return *f.getWS, nil
	}
	return workspace.Workspace{ID: "ws-" + spec.Owner, Name: "ws-" + spec.Owner, Owner: spec.Owner, Token: spec.Token}, nil
}

func (f *fakeBackend) GetWorkspace(_ context.Context, _ string) (workspace.Workspace, error) {
	if f.getErr != nil {
		return workspace.Workspace{}, f.getErr
	}
	if f.getWS != nil {
		return *f.getWS, nil
	}
	return workspace.Workspace{}, fmt.Errorf("not found")
}

func (f *fakeBackend) ListWorkspaces(_ context.Context, _ string) ([]workspace.Workspace, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.workspaces, nil
}

func (f *fakeBackend) DeleteWorkspace(_ context.Context, _ , id string) error {
	f.DeleteCalls = append(f.DeleteCalls, id)
	return f.deleteErr
}

func (f *fakeBackend) Reachable(_ context.Context) bool { return f.reachable }

func (f *fakeBackend) Routing(_ context.Context, labDomain string) (string, string) {
	if labDomain != "" {
		return labDomain, "https"
	}
	return f.routingDomain, f.routingScheme
}

// useFakeBackend wires a handler to return the given fake backend for every lab.
func useFakeBackend(h *Handler, fb *fakeBackend) {
	h.newWorkspaceBackend = func(_, _ string) (workspace.Backend, error) { return fb, nil }
}
