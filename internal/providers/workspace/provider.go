// Package workspace defines the backend abstraction for provisioning student
// IDE workspaces on a Kubernetes cluster. It replaces the former Coder-based
// workspace layer: each student gets a self-managed set of Kubernetes resources
// (Deployment + Service + Ingress + PVC) running a code-server image.
//
// Backends are registered by name (see registry.go) and instantiated per-lab
// from the lab's kubeconfig, so handlers never hardcode backend selection.
package workspace

import (
	"context"
	"time"
)

// Readiness phases. These mirror the strings the student UI already renders in
// buildWorkspaceStatusHTML (internal/server/handler.go), so the polling contract
// is unchanged.
const (
	PhaseProvisioning   = "provisioning"
	PhaseAgentsStarting = "agents_starting"
	PhaseRunning        = "running"
	PhaseFailed         = "agents_failed"
)

// Workspace is a single student IDE environment running on the cluster.
type Workspace struct {
	// ID uniquely identifies the workspace within its lab. It is also the
	// Kubernetes resource name, so it is safe to use for deletion.
	ID string `json:"id"`
	// Name is the human-facing workspace name (currently equal to ID).
	Name string `json:"name"`
	// Owner is the sanitized student username the workspace belongs to.
	Owner string `json:"owner"`
	// URL is the base workspace URL (shown to the student; no auth token).
	URL string `json:"url"`
	// OpenURL is the redirect target that lands the student in the IDE. It is the
	// base URL: the student authenticates with Token on the code-server login page.
	OpenURL string `json:"open_url"`
	// Token is the IDE access secret (the code-server password). It is a
	// per-student secret and is never serialized to admin APIs.
	Token string `json:"-"`
	// IDE is the IDE base this workspace runs. Always "code-server".
	IDE string `json:"ide"`
	// Template is the workspace template this environment was created from. It is
	// empty for workspaces created before template attribution was added.
	Template string `json:"template,omitempty"`
	// CreatedAt is the workspace creation time (used for lifetime cleanup).
	CreatedAt time.Time `json:"created_at"`
	// UpdatedAt is the last time the workspace's Deployment changed.
	UpdatedAt time.Time `json:"updated_at"`
	// Ready reports whether the workspace is fully up and serving.
	Ready bool `json:"ready"`
	// Phase is one of the Phase* constants above.
	Phase string `json:"phase"`
}

// IDE base identifiers.
const (
	// IDEOpenVSCode is a legacy value kept for backward compatibility only.
	// OpenVSCode Server support was removed; labs and saved YAML carrying this
	// value are normalized to IDECodeServer rather than rejected.
	IDEOpenVSCode  = "openvscode"
	IDECodeServer  = "code-server"
	DefaultIDEKind = IDECodeServer
)

// Sidecar is an additional container co-located in the workspace pod.
type Sidecar struct {
	Name         string
	Image        string
	Ports        []int
	Env          map[string]string
	Privileged   bool     // run privileged (required by docker-in-docker)
	Capabilities []string // added Linux capabilities (e.g. "SYS_ADMIN")
}

// Mount mounts an existing ConfigMap or Secret into the workspace container.
type Mount struct {
	Type string // "configmap" | "secret"
	Name string
	Path string
}

// DevcontainerSpec builds the workspace image from the repo's devcontainer.json
// rather than running Spec.Image directly. The build happens inside the
// student's own pod on first start, which is what lets a workshop's Dockerfile
// and features work without EasyLab having to understand them.
//
// The repo to build comes from Spec.GitRepo/GitBranch — a devcontainer workspace
// is always repo-backed.
type DevcontainerSpec struct {
	// Dir is the folder containing devcontainer.json, relative to the repo root
	// (empty means the builder's own default).
	Dir string
	// CacheRepo is the registry image layers are cached in. Without it every
	// workspace rebuilds the devcontainer from scratch, so callers are expected to
	// require it.
	CacheRepo string
	// RegistryAuthSecret names a kubernetes.io/dockerconfigjson Secret in the
	// workspace namespace granting access to CacheRepo.
	RegistryAuthSecret string
	// FallbackImage is used when the devcontainer names neither an image nor a
	// Dockerfile. A failed build is not fallen back to — it fails the workspace.
	FallbackImage string
	// Insecure bypasses TLS verification when cloning and pulling from registries.
	Insecure bool
}

// Spec describes the workspace to create for a student.
type Spec struct {
	LabID    string // owning lab/job ID, stored as a label for selection
	Owner    string // sanitized student username
	Template string // template name — part of the workspace identity (one workspace per template)

	IDE       string            // IDE base; only "code-server" is supported (empty = default)
	Image     string            // container image override (empty = IDE default)
	GitRepo   string            // optional git repo cloned into the workspace on first start
	GitBranch string            // optional branch to clone (default branch when empty)
	GitFolder string            // optional subfolder the IDE opens (repo root when empty)
	CPU       string            // optional CPU request/limit (e.g. "500m")
	Memory    string            // optional memory request/limit (e.g. "1Gi")
	DiskSize  string            // PVC size (e.g. "5Gi"); empty means no persistent volume
	Env       map[string]string // extra environment variables for the IDE container

	StartupScript string   // best-effort setup run before the IDE starts
	DotfilesRepo  string   // dotfiles repo cloned + install script run
	Extensions    []string // VS Code extensions installed on start
	Sidecars      []Sidecar
	Mounts        []Mount

	// ImagePullSecrets name dockerconfigjson Secrets in the workspace namespace the
	// kubelet pulls this workspace's images with (workspace image, sidecars, init
	// containers). The devcontainer build pulls from inside the pod instead, so its
	// images are covered by DevcontainerSpec.RegistryAuthSecret rather than this.
	ImagePullSecrets []string
	// GitAuthSecret names a kubernetes.io/basic-auth Secret in the workspace
	// namespace used to clone a private GitRepo. It is injected by reference into
	// the clone step only — never into the IDE container, where the student has a
	// shell.
	GitAuthSecret string

	// Devcontainer, when set, builds the workspace image from GitRepo's
	// devcontainer.json instead of using Image.
	Devcontainer *DevcontainerSpec

	Domain            string // base domain; the workspace host is "{ID}.{Domain}"
	WildcardTLSSecret string // pre-provisioned wildcard TLS secret; when empty a per-host cert is requested
	ClusterIssuer     string // cert-manager ClusterIssuer used for per-host certs
	Token             string // IDE access secret (connection token / password)
}

// Backend manages the lifecycle of student workspaces on a single cluster.
// Implementations are scoped to one kubeconfig + namespace.
type Backend interface {
	// EnsureWorkspace creates the workspace if it does not exist and returns it.
	// It is idempotent: calling it for an existing workspace returns the current
	// state without recreating resources.
	EnsureWorkspace(ctx context.Context, spec Spec) (Workspace, error)
	// GetWorkspace returns the workspace with the given resource name. Callers
	// must verify Workspace.Owner against the requesting student.
	GetWorkspace(ctx context.Context, name string) (Workspace, error)
	// ListWorkspaces returns all workspaces belonging to a lab.
	ListWorkspaces(ctx context.Context, labID string) ([]Workspace, error)
	// DeleteWorkspace removes all Kubernetes resources for the workspace with the given ID.
	DeleteWorkspace(ctx context.Context, labID, id string) error
	// Reachable reports whether the cluster API is currently reachable.
	Reachable(ctx context.Context) bool
	// Routing reports the base domain and URL scheme workspaces are exposed under
	// for a lab whose configured domain is labDomain (empty when the lab has none,
	// in which case the backend resolves its fallback). An empty returned domain
	// means workspaces are reachable only from inside the cluster.
	Routing(ctx context.Context, labDomain string) (domain, scheme string)
}

// Secret types reported by ListAuthSecrets.
const (
	AuthSecretRegistry = "registry"
	AuthSecretGit      = "git"
)

// AuthSecret describes a credential Secret in the workspace namespace as reported
// to the admin: enough to know which one it is and what a template must name to
// reference it. It never carries secret material — no password, and no
// dockerconfigjson "auth" blob, which is only base64 and would hand over the token.
type AuthSecret struct {
	Name     string   `json:"name"`
	Type     string   `json:"type"`              // AuthSecretRegistry | AuthSecretGit
	Servers  []string `json:"servers,omitempty"` // registry hosts (registry secrets only)
	Username string   `json:"username,omitempty"`
}

// SecretManager materializes the credential Secrets that templates reference by
// name, so an admin can add a registry or git token without kubectl access to the
// lab's cluster.
//
// It is optional and deliberately separate from Backend: a backend that cannot
// manage secrets simply does not implement it, and the admin creates the Secrets
// out of band instead. Callers type-assert for it.
//
// The Secret lives in the cluster rather than in EasyLab, which is what keeps the
// token out of the lab config and makes it survive a server restart. Callers hold
// the token only for the lifetime of the request that writes it.
type SecretManager interface {
	// EnsureRegistrySecret writes a kubernetes.io/dockerconfigjson Secret granting
	// access to server. It overwrites an existing Secret of the same name, so it
	// doubles as token rotation.
	EnsureRegistrySecret(ctx context.Context, name, server, username, token string) error
	// EnsureGitAuthSecret writes a kubernetes.io/basic-auth Secret. It overwrites an
	// existing Secret of the same name, so it doubles as token rotation.
	EnsureGitAuthSecret(ctx context.Context, name, username, token string) error
	// ListAuthSecrets reports the credential Secrets a template can reference,
	// whoever created them — including ones made out of band with kubectl.
	ListAuthSecrets(ctx context.Context) ([]AuthSecret, error)
	// DeleteAuthSecret removes a credential Secret. Deleting one that is not there
	// is not an error.
	DeleteAuthSecret(ctx context.Context, name string) error
}
