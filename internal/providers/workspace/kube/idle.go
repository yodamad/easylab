package kube

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"easylab/internal/providers/workspace"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Backend pauses and resumes workspaces, see workspace.IdleManager.
var _ workspace.IdleManager = (*Backend)(nil)

const (
	// servicePort is the port every workspace Service listens on (see createService).
	servicePort = "80"
	// healthzPath is code-server's unauthenticated health endpoint.
	healthzPath = "healthz"
	// healthzAlive is the status code-server reports while its heartbeat is fresh.
	healthzAlive = "alive"
	// healthzMaxBytes caps how much of a /healthz answer is read. The real payload
	// is under a hundred bytes.
	healthzMaxBytes = 4 << 10
)

// healthzResponse is code-server's /healthz payload. Its heartbeat is refreshed
// by every request except /healthz itself, and re-armed once a minute for as long
// as the server holds an open connection — so an IDE tab left open counts as
// activity even when the student is not typing, and asking does not.
type healthzResponse struct {
	Status        string `json:"status"`
	LastHeartbeat int64  `json:"lastHeartbeat"` // epoch milliseconds; 0 = never
}

// WorkspaceLastActivity asks the workspace's code-server when it was last in
// use. It goes through the API server's Service proxy rather than the workspace's
// public URL: that works with nothing but the lab's kubeconfig, whether or not
// this process can reach the lab's ingress.
func (b *Backend) WorkspaceLastActivity(ctx context.Context, name string) (time.Time, error) {
	probe := b.healthz
	if probe == nil {
		probe = b.proxyHealthz
	}
	body, err := probe(ctx, name)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to probe activity of workspace %s: %w", name, err)
	}
	return parseHealthz(body, time.Now())
}

// proxyHealthz fetches a workspace's /healthz through the API server.
func (b *Backend) proxyHealthz(ctx context.Context, name string) ([]byte, error) {
	stream, err := b.client.CoreV1().Services(b.namespace).ProxyGet("http", name, servicePort, healthzPath, nil).Stream(ctx)
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	return readHealthz(stream)
}

// readHealthz reads a /healthz body, refusing one larger than healthzMaxBytes.
// The answer comes from inside the student's own container, where they have a
// shell and can put anything they like on that port: reading it unbounded would
// let one workspace exhaust this process's memory.
func readHealthz(r io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, healthzMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("failed to read healthz response: %w", err)
	}
	if len(body) > healthzMaxBytes {
		return nil, fmt.Errorf("healthz response exceeds %d bytes", healthzMaxBytes)
	}
	return body, nil
}

// parseHealthz turns a /healthz payload into a last-activity time. A body that
// is not the payload described above is an error rather than "never used": the
// image may not be code-server at all, and reading that as idle would pause a
// workspace on no evidence.
func parseHealthz(body []byte, now time.Time) (time.Time, error) {
	var resp healthzResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return time.Time{}, fmt.Errorf("failed to parse healthz response: %w", err)
	}
	if resp.Status == "" {
		return time.Time{}, fmt.Errorf("healthz response has no status")
	}
	// "alive" is code-server's own verdict on its own clock: trusting it over the
	// timestamp keeps a clock offset between the pod and this process from ever
	// making an in-use workspace look idle.
	if resp.Status == healthzAlive {
		return now, nil
	}
	if resp.LastHeartbeat <= 0 {
		return time.Time{}, nil
	}
	return time.UnixMilli(resp.LastHeartbeat), nil
}

// StopWorkspace scales the workspace to zero. Everything else — volume, Service,
// Ingress, token — is left in place for StartWorkspace.
func (b *Backend) StopWorkspace(ctx context.Context, name string) error {
	dep, err := b.getDeployment(ctx, name)
	if err != nil {
		return err
	}
	if !hasPersistentVolume(dep) {
		return fmt.Errorf("workspace %s has no persistent volume: stopping it would lose its files", name)
	}
	return b.setReplicas(ctx, dep, 0)
}

// StartWorkspace scales a stopped workspace back to one replica.
func (b *Backend) StartWorkspace(ctx context.Context, name string) error {
	dep, err := b.getDeployment(ctx, name)
	if err != nil {
		return err
	}
	return b.setReplicas(ctx, dep, 1)
}

func (b *Backend) getDeployment(ctx context.Context, name string) (*appsv1.Deployment, error) {
	dep, err := b.client.AppsV1().Deployments(b.namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("workspace not found: %s", name)
		}
		return nil, fmt.Errorf("failed to get workspace %s: %w", name, err)
	}
	return dep, nil
}

// setReplicas updates the Deployment itself rather than its scale subresource:
// "update" on deployments is what the in-lab student portal's Role already grants
// (see portalRules), so a student can resume from there with no extra permission.
func (b *Backend) setReplicas(ctx context.Context, dep *appsv1.Deployment, replicas int32) error {
	if dep.Spec.Replicas != nil && *dep.Spec.Replicas == replicas {
		return nil
	}
	dep.Spec.Replicas = int32Ptr(replicas)
	if _, err := b.client.AppsV1().Deployments(b.namespace).Update(ctx, dep, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("failed to scale workspace %s to %d: %w", dep.Name, replicas, err)
	}
	return nil
}

// hasPersistentVolume reports whether the workspace keeps its files on a PVC, as
// opposed to the EmptyDir an ephemeral workspace gets (see workspaceDisk).
func hasPersistentVolume(dep *appsv1.Deployment) bool {
	for _, v := range dep.Spec.Template.Spec.Volumes {
		if v.PersistentVolumeClaim != nil && v.PersistentVolumeClaim.ClaimName == dep.Name {
			return true
		}
	}
	return false
}

// isStopped reports whether the Deployment was scaled to zero by StopWorkspace.
func isStopped(dep *appsv1.Deployment) bool {
	return dep.Spec.Replicas != nil && *dep.Spec.Replicas == 0
}
