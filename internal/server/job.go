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
	// Coder configuration stored after deployment
	CoderURL            string       `json:"coder_url,omitempty"`
	CoderAdminEmail     string       `json:"coder_admin_email,omitempty"`
	CoderAdminPassword  string       `json:"coder_admin_password,omitempty"`
	CoderSessionToken   string       `json:"coder_session_token,omitempty"`
	CoderOrganizationID string       `json:"coder_organization_id,omitempty"`
	mu                  sync.RWMutex `json:"-"`
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

	// Coder Configuration
	CoderAdminEmail    string `json:"coder_admin_email"`
	CoderAdminPassword string `json:"coder_admin_password"`
	CoderVersion       string `json:"coder_version"`
	CoderDbUser        string `json:"coder_db_user"`
	CoderDbPassword    string `json:"coder_db_password"`
	CoderDbName        string `json:"coder_db_name"`
	CoderTemplateName  string `json:"coder_template_name"`
	TemplateFilePath   string `json:"template_file_path,omitempty"` // Path to uploaded template file (zip or tf)

	// Template Source Configuration
	TemplateSource    string `json:"template_source,omitempty"`     // "upload" or "git"
	TemplateGitRepo   string `json:"template_git_repo,omitempty"`   // Git repository URL (mandatory for git source)
	TemplateGitFolder string `json:"template_git_folder,omitempty"` // Folder path in repository (optional, empty = root)
	TemplateGitBranch string `json:"template_git_branch,omitempty"` // Git branch (optional, default "main")

	// OVH Endpoint
	OvhEndpoint string `json:"ovh_endpoint"`
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

	jobID := fmt.Sprintf("job-%d", time.Now().UnixNano())
	job := &Job{
		ID:        jobID,
		Status:    JobStatusPending,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Output:    []string{},
		Config:    config,
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

// SetCoderConfig sets the Coder configuration for a job
func (jm *JobManager) SetCoderConfig(id string, coderURL, coderAdminEmail, coderAdminPassword, coderSessionToken, coderOrganizationID string) error {
	jm.mu.RLock()
	job, exists := jm.jobs[id]
	jm.mu.RUnlock()

	if !exists {
		return fmt.Errorf("job %s not found", id)
	}

	job.mu.Lock()
	defer job.mu.Unlock()

	job.CoderURL = coderURL
	job.CoderAdminEmail = coderAdminEmail
	job.CoderAdminPassword = coderAdminPassword
	job.CoderSessionToken = coderSessionToken
	job.CoderOrganizationID = coderOrganizationID
	job.UpdatedAt = time.Now()
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

		// Initialize mutex for loaded job (mutex is not serialized)
		// The mutex will be zero-initialized which is correct for sync.RWMutex

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
