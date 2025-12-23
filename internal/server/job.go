package server

import (
	"fmt"
	"sync"
	"time"
)

// JobStatus represents the current status of a Pulumi job
type JobStatus string

const (
	JobStatusPending   JobStatus = "pending"
	JobStatusRunning   JobStatus = "running"
	JobStatusCompleted JobStatus = "completed"
	JobStatusFailed    JobStatus = "failed"
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
	mu         sync.RWMutex
}

// LabConfig holds all configuration values for a lab
type LabConfig struct {
	// Pulumi Stack Name
	StackName string `json:"stack_name"`

	// OVH Environment Variables
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

	// OVH Endpoint
	OvhEndpoint string `json:"ovh_endpoint"`
}

// JobManager manages Pulumi execution jobs
type JobManager struct {
	jobs map[string]*Job
	mu   sync.RWMutex
}

// NewJobManager creates a new job manager
func NewJobManager() *JobManager {
	return &JobManager{
		jobs: make(map[string]*Job),
	}
}

// CreateJob creates a new job and returns its ID
func (jm *JobManager) CreateJob(config *LabConfig) string {
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
	jm.mu.RLock()
	defer jm.mu.RUnlock()

	job, exists := jm.jobs[id]
	return job, exists
}

// UpdateJobStatus updates the status of a job
func (jm *JobManager) UpdateJobStatus(id string, status JobStatus) error {
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
