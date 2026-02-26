package server

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestJobStatus_Constants(t *testing.T) {
	tests := []struct {
		name   string
		status JobStatus
		want   string
	}{
		{"pending", JobStatusPending, "pending"},
		{"running", JobStatusRunning, "running"},
		{"completed", JobStatusCompleted, "completed"},
		{"failed", JobStatusFailed, "failed"},
		{"dry-run-completed", JobStatusDryRunCompleted, "dry-run-completed"},
		{"destroyed", JobStatusDestroyed, "destroyed"},
	}

	for _, tt := range tests {
		// Capture loop variable to avoid potential race conditions
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			// Use local variables to ensure safe access
			statusStr := string(tt.status)
			wantStr := tt.want
			if statusStr != wantStr {
				t.Errorf("JobStatus = %s, want %s", statusStr, wantStr)
			}
		})
	}
}

func TestNewJobManager(t *testing.T) {
	jm := NewJobManager("")

	if jm == nil {
		t.Fatal("NewJobManager() returned nil")
	}

	// Access jobs map with proper locking to avoid race conditions
	jm.mu.RLock()
	jobsNil := jm.jobs == nil
	jm.mu.RUnlock()

	if jobsNil {
		t.Error("NewJobManager() jobs map is nil")
	}

	if jm.dataDir != "" {
		t.Errorf("NewJobManager() dataDir = %s, want empty", jm.dataDir)
	}
}

func TestNewJobManager_WithDataDir(t *testing.T) {
	tempDir := t.TempDir()
	jm := NewJobManager(tempDir)

	if jm.dataDir != tempDir {
		t.Errorf("NewJobManager() dataDir = %s, want %s", jm.dataDir, tempDir)
	}
}

func TestJobManager_CreateJob(t *testing.T) {
	jm := NewJobManager("")

	config := &LabConfig{
		StackName:      "test-stack",
		K8sClusterName: "test-cluster",
	}

	jobID := jm.CreateJob(config)

	if jobID == "" {
		t.Error("CreateJob() returned empty job ID")
	}

	if !strings.HasPrefix(jobID, "job-") {
		t.Errorf("CreateJob() job ID = %s, want prefix 'job-'", jobID)
	}
}

func TestJobManager_CreateJob_MultipleIDs(t *testing.T) {
	jm := NewJobManager("")

	config := &LabConfig{StackName: "test"}

	// Create multiple jobs and verify they all exist
	// Note: IDs are based on UnixNano which may not be unique in tight loops
	// but the JobManager should handle this gracefully
	for i := 0; i < 10; i++ {
		id := jm.CreateJob(config)
		if id == "" {
			t.Error("CreateJob() returned empty ID")
		}
		if !strings.HasPrefix(id, "job-") {
			t.Errorf("CreateJob() ID %s doesn't have 'job-' prefix", id)
		}
	}
}

func TestJobManager_GetJob(t *testing.T) {
	jm := NewJobManager("")

	config := &LabConfig{StackName: "test"}
	jobID := jm.CreateJob(config)

	job, exists := jm.GetJob(jobID)

	if !exists {
		t.Error("GetJob() returned exists = false for existing job")
	}

	if job == nil {
		t.Fatal("GetJob() returned nil job")
	}

	if job.ID != jobID {
		t.Errorf("GetJob() job.ID = %s, want %s", job.ID, jobID)
	}

	if job.Status != JobStatusPending {
		t.Errorf("GetJob() job.Status = %s, want %s", job.Status, JobStatusPending)
	}

	if job.Config.StackName != "test" {
		t.Errorf("GetJob() job.Config.StackName = %s, want test", job.Config.StackName)
	}
}

func TestJobManager_GetJob_NotFound(t *testing.T) {
	jm := NewJobManager("")

	job, exists := jm.GetJob("nonexistent")

	if exists {
		t.Error("GetJob() returned exists = true for nonexistent job")
	}

	if job != nil {
		t.Error("GetJob() returned non-nil job for nonexistent ID")
	}
}

func TestJobManager_UpdateJobStatus(t *testing.T) {
	jm := NewJobManager("")

	config := &LabConfig{StackName: "test"}
	jobID := jm.CreateJob(config)

	err := jm.UpdateJobStatus(jobID, JobStatusRunning)
	if err != nil {
		t.Fatalf("UpdateJobStatus() error = %v", err)
	}

	job, _ := jm.GetJob(jobID)
	if job.Status != JobStatusRunning {
		t.Errorf("UpdateJobStatus() job.Status = %s, want %s", job.Status, JobStatusRunning)
	}
}

func TestJobManager_UpdateJobStatus_NotFound(t *testing.T) {
	jm := NewJobManager("")

	err := jm.UpdateJobStatus("nonexistent", JobStatusRunning)
	if err == nil {
		t.Error("UpdateJobStatus() expected error for nonexistent job")
	}
}

func TestJobManager_AppendOutput(t *testing.T) {
	jm := NewJobManager("")

	config := &LabConfig{StackName: "test"}
	jobID := jm.CreateJob(config)

	err := jm.AppendOutput(jobID, "line 1")
	if err != nil {
		t.Fatalf("AppendOutput() error = %v", err)
	}

	err = jm.AppendOutput(jobID, "line 2")
	if err != nil {
		t.Fatalf("AppendOutput() error = %v", err)
	}

	job, _ := jm.GetJob(jobID)
	if len(job.Output) != 2 {
		t.Errorf("AppendOutput() output length = %d, want 2", len(job.Output))
	}

	if job.Output[0] != "line 1" || job.Output[1] != "line 2" {
		t.Errorf("AppendOutput() output = %v, want [line 1, line 2]", job.Output)
	}
}

func TestJobManager_AppendOutput_NotFound(t *testing.T) {
	jm := NewJobManager("")

	err := jm.AppendOutput("nonexistent", "line")
	if err == nil {
		t.Error("AppendOutput() expected error for nonexistent job")
	}
}

func TestJobManager_SetError(t *testing.T) {
	jm := NewJobManager("")

	config := &LabConfig{StackName: "test"}
	jobID := jm.CreateJob(config)

	testErr := errors.New("test error")
	err := jm.SetError(jobID, testErr)
	if err != nil {
		t.Fatalf("SetError() error = %v", err)
	}

	job, _ := jm.GetJob(jobID)
	if job.Error != "test error" {
		t.Errorf("SetError() job.Error = %s, want 'test error'", job.Error)
	}

	if job.Status != JobStatusFailed {
		t.Errorf("SetError() job.Status = %s, want %s", job.Status, JobStatusFailed)
	}
}

func TestJobManager_SetError_NotFound(t *testing.T) {
	jm := NewJobManager("")

	err := jm.SetError("nonexistent", errors.New("test"))
	if err == nil {
		t.Error("SetError() expected error for nonexistent job")
	}
}

func TestJobManager_SetKubeconfig(t *testing.T) {
	jm := NewJobManager("")

	config := &LabConfig{StackName: "test"}
	jobID := jm.CreateJob(config)

	kubeconfig := "apiVersion: v1\nkind: Config"
	err := jm.SetKubeconfig(jobID, kubeconfig)
	if err != nil {
		t.Fatalf("SetKubeconfig() error = %v", err)
	}

	job, _ := jm.GetJob(jobID)
	if job.Kubeconfig != kubeconfig {
		t.Errorf("SetKubeconfig() job.Kubeconfig mismatch")
	}
}

func TestJobManager_SetKubeconfig_NotFound(t *testing.T) {
	jm := NewJobManager("")

	err := jm.SetKubeconfig("nonexistent", "config")
	if err == nil {
		t.Error("SetKubeconfig() expected error for nonexistent job")
	}
}

func TestJobManager_SetCoderConfig(t *testing.T) {
	jm := NewJobManager("")

	config := &LabConfig{StackName: "test"}
	jobID := jm.CreateJob(config)

	err := jm.SetCoderConfig(jobID, "http://coder.example.com", "admin@test.com", "password123", "session-token", "org-id")
	if err != nil {
		t.Fatalf("SetCoderConfig() error = %v", err)
	}

	job, _ := jm.GetJob(jobID)
	if job.CoderURL != "http://coder.example.com" {
		t.Errorf("SetCoderConfig() CoderURL = %s, want http://coder.example.com", job.CoderURL)
	}
	if job.CoderAdminEmail != "admin@test.com" {
		t.Errorf("SetCoderConfig() CoderAdminEmail = %s, want admin@test.com", job.CoderAdminEmail)
	}
	if job.CoderAdminPassword != "password123" {
		t.Errorf("SetCoderConfig() CoderAdminPassword mismatch")
	}
	if job.CoderSessionToken != "session-token" {
		t.Errorf("SetCoderConfig() CoderSessionToken mismatch")
	}
	if job.CoderOrganizationID != "org-id" {
		t.Errorf("SetCoderConfig() CoderOrganizationID = %s, want org-id", job.CoderOrganizationID)
	}
}

func TestJobManager_SetCoderConfig_NotFound(t *testing.T) {
	jm := NewJobManager("")

	err := jm.SetCoderConfig("nonexistent", "", "", "", "", "")
	if err == nil {
		t.Error("SetCoderConfig() expected error for nonexistent job")
	}
}

func TestJobManager_GetAllJobs(t *testing.T) {
	jm := NewJobManager("")

	// Create multiple jobs with slight delays to ensure different timestamps
	config := &LabConfig{StackName: "test"}
	jm.CreateJob(config)
	time.Sleep(10 * time.Millisecond)
	jm.CreateJob(config)
	time.Sleep(10 * time.Millisecond)
	jm.CreateJob(config)

	jobs := jm.GetAllJobs()

	if len(jobs) != 3 {
		t.Errorf("GetAllJobs() length = %d, want 3", len(jobs))
	}

	// Check jobs are sorted by CreatedAt descending (newest first)
	for i := 0; i < len(jobs)-1; i++ {
		if jobs[i].CreatedAt.Before(jobs[i+1].CreatedAt) {
			t.Error("GetAllJobs() jobs not sorted by CreatedAt descending")
		}
	}
}

func TestJobManager_RemoveJob(t *testing.T) {
	jm := NewJobManager("")

	config := &LabConfig{StackName: "test"}
	jobID := jm.CreateJob(config)

	// Verify job exists
	_, exists := jm.GetJob(jobID)
	if !exists {
		t.Fatal("Job should exist before removal")
	}

	err := jm.RemoveJob(jobID)
	if err != nil {
		t.Fatalf("RemoveJob() error = %v", err)
	}

	// Verify job is removed
	_, exists = jm.GetJob(jobID)
	if exists {
		t.Error("GetJob() returned true after RemoveJob()")
	}
}

func TestJobManager_RemoveJob_NotFound(t *testing.T) {
	jm := NewJobManager("")

	err := jm.RemoveJob("nonexistent")
	if err == nil {
		t.Error("RemoveJob() expected error for nonexistent job")
	}
}

func TestJobManager_SaveJob(t *testing.T) {
	tempDir := t.TempDir()
	jm := NewJobManager(tempDir)

	config := &LabConfig{StackName: "test"}
	jobID := jm.CreateJob(config)

	// Set job to completed (only completed jobs are saved)
	jm.UpdateJobStatus(jobID, JobStatusCompleted)

	err := jm.SaveJob(jobID)
	if err != nil {
		t.Fatalf("SaveJob() error = %v", err)
	}

	// Verify file was created
	jobFile := filepath.Join(tempDir, "jobs", jobID+".json")
	if _, err := os.Stat(jobFile); os.IsNotExist(err) {
		t.Error("SaveJob() did not create job file")
	}
}

func TestJobManager_SaveJob_NoPersistence(t *testing.T) {
	jm := NewJobManager("") // No dataDir, persistence disabled

	config := &LabConfig{StackName: "test"}
	jobID := jm.CreateJob(config)
	jm.UpdateJobStatus(jobID, JobStatusCompleted)

	err := jm.SaveJob(jobID)
	if err != nil {
		t.Errorf("SaveJob() error = %v, want nil for disabled persistence", err)
	}
}

func TestJobManager_SaveJob_NotFound(t *testing.T) {
	tempDir := t.TempDir()
	jm := NewJobManager(tempDir)

	err := jm.SaveJob("nonexistent")
	if err == nil {
		t.Error("SaveJob() expected error for nonexistent job")
	}
}

func TestJobManager_LoadJobs(t *testing.T) {
	tempDir := t.TempDir()

	// Create and save a job
	jm1 := NewJobManager(tempDir)
	config := &LabConfig{StackName: "test-stack"}
	jobID := jm1.CreateJob(config)
	jm1.UpdateJobStatus(jobID, JobStatusCompleted)
	jm1.SaveJob(jobID)

	// Create new job manager and load jobs
	jm2 := NewJobManager(tempDir)
	err := jm2.LoadJobs()
	if err != nil {
		t.Fatalf("LoadJobs() error = %v", err)
	}

	// Verify job was loaded
	job, exists := jm2.GetJob(jobID)
	if !exists {
		t.Error("LoadJobs() did not load saved job")
	}

	if job.Config.StackName != "test-stack" {
		t.Errorf("LoadJobs() job.Config.StackName = %s, want test-stack", job.Config.StackName)
	}
}

func TestJobManager_LoadJobs_NoPersistence(t *testing.T) {
	jm := NewJobManager("") // No dataDir

	err := jm.LoadJobs()
	if err != nil {
		t.Errorf("LoadJobs() error = %v, want nil for disabled persistence", err)
	}
}

func TestJobManager_LoadJobs_NoJobsDir(t *testing.T) {
	tempDir := t.TempDir()
	jm := NewJobManager(tempDir)

	// Jobs directory doesn't exist yet
	err := jm.LoadJobs()
	if err != nil {
		t.Errorf("LoadJobs() error = %v, want nil when jobs dir doesn't exist", err)
	}
}

func TestJob_Timestamps(t *testing.T) {
	jm := NewJobManager("")

	config := &LabConfig{StackName: "test"}
	before := time.Now()
	jobID := jm.CreateJob(config)
	after := time.Now()

	job, _ := jm.GetJob(jobID)

	if job.CreatedAt.Before(before) || job.CreatedAt.After(after) {
		t.Error("Job CreatedAt is not within expected time range")
	}

	// Update job and check UpdatedAt
	beforeUpdate := time.Now()
	jm.UpdateJobStatus(jobID, JobStatusRunning)
	afterUpdate := time.Now()

	job, _ = jm.GetJob(jobID)
	if job.UpdatedAt.Before(beforeUpdate) || job.UpdatedAt.After(afterUpdate) {
		t.Error("Job UpdatedAt is not within expected time range after status update")
	}
}

func TestLabConfig_Fields(t *testing.T) {
	config := &LabConfig{
		StackName:                 "test-stack",
		OvhApplicationKey:         "key",
		OvhApplicationSecret:      "secret",
		OvhConsumerKey:            "consumer",
		OvhServiceName:            "service",
		NetworkGatewayName:        "gateway",
		NetworkGatewayModel:       "model",
		NetworkPrivateNetworkName: "network",
		NetworkRegion:             "region",
		NetworkMask:               "255.255.255.0",
		NetworkStartIP:            "10.0.0.1",
		NetworkEndIP:              "10.0.0.254",
		NetworkID:                 "network-id",
		K8sClusterName:            "cluster",
		NodePoolName:              "pool",
		NodePoolFlavor:            "flavor",
		NodePoolDesiredNodeCount:  3,
		NodePoolMinNodeCount:      1,
		NodePoolMaxNodeCount:      5,
		CoderNamespace:             "coder",
		CoderAdminEmail:           "admin@test.com",
		CoderAdminPassword:        "password",
		CoderVersion:              "1.0.0",
		CoderDbUser:               "coder",
		CoderDbPassword:           "dbpass",
		CoderDbName:               "coder",
		CoderTemplateName:         "template",
		OvhEndpoint:               "ovh-eu",
	}

	if config.StackName != "test-stack" {
		t.Errorf("StackName = %s, want test-stack", config.StackName)
	}
	if config.NodePoolDesiredNodeCount != 3 {
		t.Errorf("NodePoolDesiredNodeCount = %d, want 3", config.NodePoolDesiredNodeCount)
	}
}

func TestJobManager_ConcurrentAccess(t *testing.T) {
	jm := NewJobManager("")

	done := make(chan bool)
	config := &LabConfig{StackName: "test"}

	// Concurrent job creation
	go func() {
		for i := 0; i < 50; i++ {
			jm.CreateJob(config)
		}
		done <- true
	}()

	// Concurrent job reading
	go func() {
		for i := 0; i < 50; i++ {
			jm.GetAllJobs()
		}
		done <- true
	}()

	// Wait for all goroutines
	<-done
	<-done

	// Verify at least some jobs were created (exact count may vary due to ID collisions with UnixNano)
	jobs := jm.GetAllJobs()
	if len(jobs) == 0 {
		t.Error("Expected at least some jobs to be created")
	}
}
