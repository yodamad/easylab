package server

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

// JobStatus represents the current status of a Pulumi job
type JobStatus string

const (
	JobStatusPending         JobStatus = "pending"
	JobStatusRunning         JobStatus = "running"
	JobStatusCompleted       JobStatus = "completed"
	JobStatusFailed          JobStatus = "failed"
	JobStatusDryRunCompleted JobStatus = "dry-run-completed"
	JobStatusDestroyed       JobStatus = "destroyed"
)

// CleanupEvent records a single automatic workspace cleanup run.
type CleanupEvent struct {
	At    time.Time `json:"at"`
	Count int       `json:"count"`
}

// WorkspaceDeletionRetry tracks failed auto-deletion attempts for a single workspace.
type WorkspaceDeletionRetry struct {
	WorkspaceID   string    `json:"workspace_id"`
	WorkspaceName string    `json:"workspace_name"`
	Attempts      int       `json:"attempts"`
	LastAttempt   time.Time `json:"last_attempt"`
	GiveUp        bool      `json:"give_up"`
}

// WorkspaceSnapshot records the total workspace count observed at a point in time.
type WorkspaceSnapshot struct {
	At    time.Time `json:"at"`
	Count int       `json:"count"`
}

// Job represents a Pulumi execution job
type Job struct {
	ID         string     `json:"id"`
	Status     JobStatus  `json:"status"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	Output     []string   `json:"output"`
	Error      string     `json:"error,omitempty"`
	Config     *LabConfig `json:"config,omitempty"`
	Kubeconfig string     `json:"kubeconfig,omitempty"`
	// CleanupEvents/WorkspaceSnapshots/DeletionRetries feed the stats dashboard
	// and the background workspace cleanup loop.
	CleanupEvents      []CleanupEvent                     `json:"cleanup_events,omitempty"`
	WorkspaceSnapshots []WorkspaceSnapshot                `json:"workspace_snapshots,omitempty"`
	DeletionRetries    map[string]*WorkspaceDeletionRetry `json:"deletion_retries,omitempty"`
	mu                 sync.RWMutex                       `json:"-"`
}

// WorkspaceTemplate defines a selectable workspace flavor for a lab: the IDE
// base, container image, an optional git repo cloned on first start, resource
// sizing, and environment provisioning (startup script, dotfiles, extensions,
// sidecars, and ConfigMap/Secret mounts) applied to the student's pod.
type WorkspaceTemplate struct {
	Name      string            `json:"name"`
	Image     string            `json:"image,omitempty"`
	GitRepo   string            `json:"git_repo,omitempty"`
	GitBranch string            `json:"git_branch,omitempty"`
	GitFolder string            `json:"git_folder,omitempty"`
	CPU       string            `json:"cpu,omitempty"`
	Memory    string            `json:"memory,omitempty"`
	DiskSize  string            `json:"disk_size,omitempty"`
	Env       map[string]string `json:"env,omitempty"`

	// IDE selects the workspace IDE base: "openvscode" (default) or "code-server".
	IDE string `json:"ide,omitempty"`
	// StartupScript runs (best-effort) in the workspace container before the IDE
	// starts — install tools, configure the shell, run a bootstrap.
	StartupScript string `json:"startup_script,omitempty"`
	// DotfilesRepo is cloned into ~/.dotfiles and its install script (if any) run.
	DotfilesRepo string `json:"dotfiles_repo,omitempty"`
	// Extensions are VS Code extension IDs (or .vsix URLs) installed on start.
	Extensions []string `json:"extensions,omitempty"`
	// Sidecars are additional containers in the workspace pod (e.g. a database),
	// reachable from the IDE at localhost:<port>.
	Sidecars []WorkspaceSidecar `json:"sidecars,omitempty"`
	// Mounts mount existing ConfigMaps/Secrets (in the workspace namespace) into
	// the workspace container.
	Mounts []WorkspaceMount `json:"mounts,omitempty"`
	// ImagePullSecrets name existing kubernetes.io/dockerconfigjson Secrets in the
	// workspace namespace that the kubelet uses to pull this template's images —
	// the workspace image, the sidecars and the init containers alike. References
	// rather than inline credentials, so no secret material is stored in the lab
	// config or leaves through the templates export.
	//
	// The devcontainer build pulls its base and fallback images from inside the pod
	// rather than through the kubelet, so those are covered by
	// devcontainer.registry_auth_secret, not by this.
	ImagePullSecrets []string `json:"image_pull_secrets,omitempty"`
	// GitAuthSecret names an existing kubernetes.io/basic-auth Secret (username +
	// password keys) in the workspace namespace, used to clone a private GitRepo
	// over HTTPS. The credentials are injected by reference, so the token reaches
	// git without ever appearing in the Deployment.
	//
	// It is read by the clone step only, never by the IDE container: the student
	// has a shell there, and the workshop author's token must not be in it.
	GitAuthSecret string `json:"git_auth_secret,omitempty"`
	// Devcontainer builds the workspace image from the workshop repo's
	// devcontainer.json instead of using Image. Requires GitRepo; conflicts with
	// Image.
	Devcontainer *DevcontainerConfig `json:"devcontainer,omitempty"`
}

// DevcontainerConfig builds a workspace from the repo's devcontainer.json
// (image or Dockerfile, plus features) rather than a fixed image. The build runs
// inside the student's pod on first start, so the layer cache is what keeps that
// start bearable — hence CacheRepo being required rather than optional.
//
// Note the division of labour: envbuilder reads image/build/features/containerEnv
// and the lifecycle commands straight from the repo, so none of those appear
// here. The surrounding WorkspaceTemplate covers what envbuilder ignores
// (Extensions, CPU/Memory/DiskSize, GitFolder).
type DevcontainerConfig struct {
	Enabled bool `json:"enabled,omitempty"`
	// Dir is the folder containing devcontainer.json, relative to the repo root
	// (default ".devcontainer").
	Dir string `json:"dir,omitempty"`
	// CacheRepo is the container registry image layers are cached in. Required:
	// without it every student's pod rebuilds the devcontainer from scratch.
	CacheRepo string `json:"cache_repo,omitempty"`
	// RegistryAuthSecret names an existing kubernetes.io/dockerconfigjson Secret
	// in the workspace namespace, used to authenticate every registry operation the
	// build makes from inside the pod: pulling the devcontainer's base image and
	// FallbackImage, and pulling and pushing CacheRepo. It is a reference rather
	// than inline credentials so no secret material is stored in the lab config or
	// leaves through the templates export.
	//
	// Distinct from the template's ImagePullSecrets, which cover the images the
	// kubelet pulls (the workspace image, sidecars, init containers). The two are
	// not interchangeable: the build pulls with its own credentials, not the
	// kubelet's.
	RegistryAuthSecret string `json:"registry_auth_secret,omitempty"`
	// FallbackImage is used when the devcontainer declares neither an image nor a
	// Dockerfile (features-only). A failed build is not fallen back to: it fails
	// the workspace rather than handing the student an environment missing its tools.
	FallbackImage string `json:"fallback_image,omitempty"`
	// Insecure bypasses TLS verification when cloning and pulling from registries.
	Insecure bool `json:"insecure,omitempty"`
}

// WorkspaceSidecar is an additional container co-located in the workspace pod.
type WorkspaceSidecar struct {
	Name  string            `json:"name"`
	Image string            `json:"image"`
	Ports []int             `json:"ports,omitempty"`
	Env   map[string]string `json:"env,omitempty"`
	// Privileged runs the sidecar as a privileged container (required by
	// docker-in-docker). Use with care — a privileged container can escalate to
	// the node.
	Privileged bool `json:"privileged,omitempty"`
	// Capabilities are Linux capabilities added to the sidecar (e.g. "SYS_ADMIN"),
	// for less-than-privileged setups such as rootless docker-in-docker.
	Capabilities []string `json:"capabilities,omitempty"`
}

// WorkspaceMount mounts a ConfigMap or Secret into the workspace container.
type WorkspaceMount struct {
	Type string `json:"type"` // "configmap" | "secret"
	Name string `json:"name"` // ConfigMap/Secret name (must exist in the namespace)
	Path string `json:"path"` // mount path in the workspace container
}

// LabConfig holds all configuration values for a lab
type LabConfig struct {
	// Pulumi Stack Name
	StackName string `json:"stack_name"`

	// Cloud Provider
	Provider string `json:"provider"` // "ovh", "aws", "azure", etc.

	// Bring Your Own Kubernetes cluster
	UseExistingCluster bool   `json:"use_existing_cluster"`
	ExternalKubeconfig string `json:"external_kubeconfig,omitempty"`

	// OVH Environment Variables (kept for backward compatibility)
	OvhApplicationKey    string `json:"ovh_application_key"`
	OvhApplicationSecret string `json:"ovh_application_secret"`
	OvhConsumerKey       string `json:"ovh_consumer_key"`
	OvhServiceName       string `json:"ovh_service_name"`

	// Network Configuration
	NetworkGatewayName        string `json:"network_gateway_name"`
	NetworkGatewayModel       string `json:"network_gateway_model"`
	NetworkPrivateNetworkName string `json:"network_private_network_name"`
	NetworkRegion             string `json:"network_region"`
	NetworkMask               string `json:"network_mask"`
	NetworkStartIP            string `json:"network_start_ip"`
	NetworkEndIP              string `json:"network_end_ip"`
	NetworkID                 string `json:"network_id,omitempty"`

	// Kubernetes Configuration
	K8sClusterName string `json:"k8s_cluster_name"`

	// Node Pool Configuration
	NodePoolName             string `json:"nodepool_name"`
	NodePoolFlavor           string `json:"nodepool_flavor"`
	NodePoolDesiredNodeCount int    `json:"nodepool_desired_node_count"`
	NodePoolMinNodeCount     int    `json:"nodepool_min_node_count"`
	NodePoolMaxNodeCount     int    `json:"nodepool_max_node_count"`

	// Workspace Configuration
	// WorkspaceNamespace is the Kubernetes namespace student workspaces are created in.
	WorkspaceNamespace     string              `json:"workspace_namespace,omitempty"`
	WorkspaceTemplates     []WorkspaceTemplate `json:"workspace_templates,omitempty"`
	WorkspaceLifetimeHours int                 `json:"workspace_lifetime_hours,omitempty"`
	LabDeletionDate        *time.Time          `json:"lab_deletion_date,omitempty"`

	// OVH Endpoint
	OvhEndpoint string `json:"ovh_endpoint"`

	// Azure credentials (for AKS provisioning)
	AzureClientID       string `json:"azure_client_id,omitempty"`
	AzureClientSecret   string `json:"azure_client_secret,omitempty"`
	AzureTenantID       string `json:"azure_tenant_id,omitempty"`
	AzureSubscriptionID string `json:"azure_subscription_id,omitempty"`
	AzureLocation       string `json:"azure_location,omitempty"`

	// HTTPS / ingress configuration. Domain is the lab's public domain, wired into
	// cert-manager + ingress-nginx and used to build per-student workspace URLs
	// ("{workspace}.{Domain}"). WildcardDomain drives the wildcard DNS A-record so
	// per-student subdomains resolve.
	Domain         string `json:"domain,omitempty"`
	AcmeEmail      string `json:"acme_email,omitempty"`
	WildcardDomain string `json:"wildcard_domain,omitempty"`

	// Controls whether nginx-ingress / cert-manager are installed as part of the lab.
	// nil means "install" (default, preserves backward compat for persisted jobs).
	// &false means "skip" (cluster already has it installed).
	InstallNginxIngress     *bool  `json:"install_nginx_ingress,omitempty"`
	NginxIngressNamespace   string `json:"nginx_ingress_namespace,omitempty"`
	NginxIngressServiceName string `json:"nginx_ingress_service_name,omitempty"`
	InstallCertManager      *bool  `json:"install_cert_manager,omitempty"`
	CertManagerNamespace    string `json:"cert_manager_namespace,omitempty"`

	// DNS provider for automated A-record creation and DNS-01 cert issuance
	DNSProvider    string            `json:"dns_provider,omitempty"`
	DNSZone        string            `json:"dns_zone,omitempty"`
	DNSCredentials map[string]string `json:"dns_credentials,omitempty"`
}

// GetWorkspaceTemplates returns the lab's workspace templates. When none are
// configured it returns a single default OpenVSCode template so every lab is
// usable out of the box.
func (c *LabConfig) GetWorkspaceTemplates() []WorkspaceTemplate {
	if len(c.WorkspaceTemplates) > 0 {
		return c.WorkspaceTemplates
	}
	return []WorkspaceTemplate{{Name: "default"}}
}

// JobManager manages Pulumi execution jobs
type JobManager struct {
	jobs    map[string]*Job
	dataDir string
	mu      sync.RWMutex
}

// NewJobManager creates a new job manager with optional data directory for persistence
func NewJobManager(dataDir string) *JobManager {
	jm := &JobManager{
		jobs:    make(map[string]*Job),
		dataDir: dataDir,
	}

	// Job loading is now done asynchronously after server starts
	// See cmd/server/main.go for the async LoadJobs() call

	return jm
}

// CreateJob creates a new job and returns its ID
func (jm *JobManager) CreateJob(config *LabConfig) string {
	if jm == nil {
		return ""
	}
	jm.mu.Lock()
	defer jm.mu.Unlock()

	now := time.Now()
	jobID := "job-" + uuid.New().String()
	job := &Job{
		ID:              jobID,
		Status:          JobStatusPending,
		CreatedAt:       now,
		UpdatedAt:       now,
		Output:          []string{},
		Config:          config,
		DeletionRetries: make(map[string]*WorkspaceDeletionRetry),
	}

	jm.jobs[jobID] = job
	return jobID
}

// GetJob retrieves a job by ID
func (jm *JobManager) GetJob(id string) (*Job, bool) {
	if jm == nil {
		return nil, false
	}
	jm.mu.RLock()
	defer jm.mu.RUnlock()

	job, exists := jm.jobs[id]
	return job, exists
}

// UpdateJobStatus updates the status of a job
func (jm *JobManager) UpdateJobStatus(id string, status JobStatus) error {
	if jm == nil {
		return fmt.Errorf("job manager is nil")
	}
	jm.mu.RLock()
	job, exists := jm.jobs[id]
	jm.mu.RUnlock()

	if !exists {
		return fmt.Errorf("job %s not found", id)
	}

	job.mu.Lock()
	defer job.mu.Unlock()

	job.Status = status
	job.UpdatedAt = time.Now()
	return nil
}

// AppendOutput appends output to a job
func (jm *JobManager) AppendOutput(id string, line string) error {
	jm.mu.RLock()
	job, exists := jm.jobs[id]
	jm.mu.RUnlock()

	if !exists {
		return fmt.Errorf("job %s not found", id)
	}

	job.mu.Lock()
	defer job.mu.Unlock()

	job.Output = append(job.Output, line)
	job.UpdatedAt = time.Now()
	return nil
}

// SetError sets an error message on a job
func (jm *JobManager) SetError(id string, err error) error {
	jm.mu.RLock()
	job, exists := jm.jobs[id]
	jm.mu.RUnlock()

	if !exists {
		return fmt.Errorf("job %s not found", id)
	}

	job.mu.Lock()
	defer job.mu.Unlock()

	job.Error = err.Error()
	job.Status = JobStatusFailed
	job.UpdatedAt = time.Now()
	return nil
}

// SetKubeconfig sets the kubeconfig for a job
func (jm *JobManager) SetKubeconfig(id string, kubeconfig string) error {
	jm.mu.RLock()
	job, exists := jm.jobs[id]
	jm.mu.RUnlock()

	if !exists {
		return fmt.Errorf("job %s not found", id)
	}

	job.mu.Lock()
	defer job.mu.Unlock()

	job.Kubeconfig = kubeconfig
	job.UpdatedAt = time.Now()
	return nil
}

// defaultWorkspaceNamespace is the namespace student workspaces live in when a
// lab does not specify one. It matches kube.DefaultNamespace.
const defaultWorkspaceNamespace = "workshops"

// workspaceNamespace returns the effective Kubernetes namespace student
// workspaces live in for this job, falling back to the default when unset.
// Must be called with the job at least read-locked (or on a detached copy).
func (j *Job) workspaceNamespace() string {
	if j.Config != nil && j.Config.WorkspaceNamespace != "" {
		return j.Config.WorkspaceNamespace
	}
	return defaultWorkspaceNamespace
}

// RecordCleanupEvent appends an auto-cleanup event to a job.
func (jm *JobManager) RecordCleanupEvent(id string, count int) error {
	jm.mu.RLock()
	job, exists := jm.jobs[id]
	jm.mu.RUnlock()
	if !exists {
		return fmt.Errorf("job %s not found", id)
	}
	job.mu.Lock()
	job.CleanupEvents = append(job.CleanupEvents, CleanupEvent{At: time.Now(), Count: count})
	job.UpdatedAt = time.Now()
	job.mu.Unlock()
	return nil
}

// RecordWorkspaceSnapshot appends the observed workspace count for a job.
func (jm *JobManager) RecordWorkspaceSnapshot(id string, count int) error {
	jm.mu.RLock()
	job, exists := jm.jobs[id]
	jm.mu.RUnlock()
	if !exists {
		return fmt.Errorf("job %s not found", id)
	}
	job.mu.Lock()
	job.WorkspaceSnapshots = append(job.WorkspaceSnapshots, WorkspaceSnapshot{At: time.Now(), Count: count})
	job.mu.Unlock()
	return nil
}

// ResetJobForRetry resets a failed job to pending status for retry
func (jm *JobManager) ResetJobForRetry(id string) error {
	jm.mu.RLock()
	job, exists := jm.jobs[id]
	jm.mu.RUnlock()

	if !exists {
		return fmt.Errorf("job %s not found", id)
	}

	job.mu.Lock()
	defer job.mu.Unlock()

	// Only allow retry for failed jobs
	if job.Status != JobStatusFailed {
		return fmt.Errorf("job %s is not in failed status (current status: %s)", id, job.Status)
	}

	// Reset job state
	job.Status = JobStatusPending
	job.Error = ""
	job.Output = []string{} // Clear previous output
	job.UpdatedAt = time.Now()

	return nil
}

// SaveJob persists a completed job to disk
func (jm *JobManager) SaveJob(id string) error {
	if jm.dataDir == "" {
		return nil // Persistence disabled
	}

	jm.mu.RLock()
	job, exists := jm.jobs[id]
	jm.mu.RUnlock()

	if !exists {
		return fmt.Errorf("job %s not found", id)
	}

	job.mu.RLock()
	status := job.Status
	job.mu.RUnlock()

	// Persist completed, destroyed, and failed jobs
	if status != JobStatusCompleted && status != JobStatusDestroyed && status != JobStatusFailed {
		return nil
	}

	// Create jobs directory if it doesn't exist
	jobsDir := filepath.Join(jm.dataDir, "jobs")
	if err := os.MkdirAll(jobsDir, 0755); err != nil {
		return fmt.Errorf("failed to create jobs directory: %w", err)
	}

	// Marshal job to JSON
	job.mu.RLock()
	jobData, err := json.MarshalIndent(job, "", "  ")
	job.mu.RUnlock()
	if err != nil {
		return fmt.Errorf("failed to marshal job: %w", err)
	}

	// Write atomically: write to temp file, then rename
	jobFile := filepath.Join(jobsDir, fmt.Sprintf("%s.json", id))
	tmpFile := jobFile + ".tmp"

	if err := os.WriteFile(tmpFile, jobData, 0644); err != nil {
		return fmt.Errorf("failed to write job file: %w", err)
	}

	if err := os.Rename(tmpFile, jobFile); err != nil {
		os.Remove(tmpFile) // Clean up temp file on error
		return fmt.Errorf("failed to rename temp file: %w", err)
	}

	return nil
}

// LoadJobs loads all persisted completed jobs from disk
func (jm *JobManager) LoadJobs() error {
	if jm.dataDir == "" {
		return nil // Persistence disabled
	}

	jobsDir := filepath.Join(jm.dataDir, "jobs")

	// Check if jobs directory exists
	if _, err := os.Stat(jobsDir); os.IsNotExist(err) {
		return nil // No jobs directory, nothing to load
	}

	// Read all JSON files in jobs directory
	entries, err := os.ReadDir(jobsDir)
	if err != nil {
		return fmt.Errorf("failed to read jobs directory: %w", err)
	}

	loadedCount := 0
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}

		jobFile := filepath.Join(jobsDir, entry.Name())
		data, err := os.ReadFile(jobFile)
		if err != nil {
			log.Printf("Warning: failed to read job file %s: %v", jobFile, err)
			continue
		}

		var job Job
		if err := json.Unmarshal(data, &job); err != nil {
			log.Printf("Warning: failed to unmarshal job file %s: %v", jobFile, err)
			continue
		}

		// Load completed, destroyed, and failed jobs
		if job.Status != JobStatusCompleted && job.Status != JobStatusDestroyed && job.Status != JobStatusFailed {
			continue
		}

		// Initialise maps that may be absent in jobs persisted before this field was added.
		if job.DeletionRetries == nil {
			job.DeletionRetries = make(map[string]*WorkspaceDeletionRetry)
		}

		// Add to jobs map
		jm.mu.Lock()
		jm.jobs[job.ID] = &job
		jm.mu.Unlock()
		loadedCount++
	}

	if loadedCount > 0 {
		log.Printf("Loaded %d persisted job(s) from %s", loadedCount, jobsDir)
	}

	return nil
}

// GetAllJobs returns all jobs sorted by creation time (newest first)
func (jm *JobManager) GetAllJobs() []*Job {
	if jm == nil {
		return nil
	}
	jm.mu.RLock()
	defer jm.mu.RUnlock()

	jobs := make([]*Job, 0, len(jm.jobs))
	for _, job := range jm.jobs {
		jobs = append(jobs, job)
	}

	// Sort by CreatedAt descending (newest first)
	sort.Slice(jobs, func(i, j int) bool {
		return jobs[i].CreatedAt.Before(jobs[j].CreatedAt)
	})
	// Reverse to get newest first
	for i, j := 0, len(jobs)-1; i < j; i, j = i+1, j-1 {
		jobs[i], jobs[j] = jobs[j], jobs[i]
	}

	return jobs
}

// RecordDeletionFailure increments the failed deletion attempt count for a workspace.
// When attempts reach maxRetries, GiveUp is set to true and no further attempts will be made.
func (jm *JobManager) RecordDeletionFailure(jobID, wsID, wsName string, maxRetries int) error {
	jm.mu.RLock()
	job, exists := jm.jobs[jobID]
	jm.mu.RUnlock()
	if !exists {
		return fmt.Errorf("job %s not found", jobID)
	}
	job.mu.Lock()
	defer job.mu.Unlock()
	if job.DeletionRetries == nil {
		job.DeletionRetries = make(map[string]*WorkspaceDeletionRetry)
	}
	r := job.DeletionRetries[wsID]
	if r == nil {
		r = &WorkspaceDeletionRetry{WorkspaceID: wsID, WorkspaceName: wsName}
		job.DeletionRetries[wsID] = r
	}
	r.Attempts++
	r.LastAttempt = time.Now()
	if r.Attempts >= maxRetries {
		r.GiveUp = true
	}
	job.UpdatedAt = time.Now()
	return nil
}

// ClearDeletionRetry removes the retry record for a workspace (called on successful deletion).
func (jm *JobManager) ClearDeletionRetry(jobID, wsID string) error {
	jm.mu.RLock()
	job, exists := jm.jobs[jobID]
	jm.mu.RUnlock()
	if !exists {
		return fmt.Errorf("job %s not found", jobID)
	}
	job.mu.Lock()
	defer job.mu.Unlock()
	delete(job.DeletionRetries, wsID)
	return nil
}

// RemoveJob removes a job from the manager and optionally deletes its persisted file
func (jm *JobManager) RemoveJob(id string) error {
	jm.mu.Lock()
	defer jm.mu.Unlock()

	_, exists := jm.jobs[id]
	if !exists {
		return fmt.Errorf("job %s not found", id)
	}

	// Remove from memory
	delete(jm.jobs, id)

	// Remove persisted file if it exists
	if jm.dataDir != "" {
		jobsDir := filepath.Join(jm.dataDir, "jobs")
		jobFile := filepath.Join(jobsDir, fmt.Sprintf("%s.json", id))
		if err := os.Remove(jobFile); err != nil && !os.IsNotExist(err) {
			log.Printf("Warning: failed to remove job file %s: %v", jobFile, err)
			// Don't fail the operation if file removal fails
		}
	}

	return nil
}
