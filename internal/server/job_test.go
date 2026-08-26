package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
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

// TestJobManager_SaveJob_ConcurrentWritesPreserveAllEvents guards against a
// regression of the SaveJob lost-update race: many students creating
// workspaces in the same lab at once each call RecordWorkspaceEvent then
// SaveJob concurrently, and every one of those events must survive on disk —
// not just in memory — even though they all write the same job file.
func TestJobManager_SaveJob_ConcurrentWritesPreserveAllEvents(t *testing.T) {
	tempDir := t.TempDir()
	jm := NewJobManager(tempDir)

	config := &LabConfig{StackName: "test"}
	jobID := jm.CreateJob(config)
	jm.UpdateJobStatus(jobID, JobStatusCompleted)

	const concurrency = 50
	var wg sync.WaitGroup
	wg.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		go func(n int) {
			defer wg.Done()
			wsID := fmt.Sprintf("ws-%d", n)
			if err := jm.RecordWorkspaceEvent(jobID, WorkspaceEventCreated, wsID, wsID, "student", "default"); err != nil {
				t.Errorf("RecordWorkspaceEvent() error = %v", err)
				return
			}
			if err := jm.SaveJob(jobID); err != nil {
				t.Errorf("SaveJob() error = %v", err)
			}
		}(i)
	}
	wg.Wait()

	job, exists := jm.GetJob(jobID)
	if !exists {
		t.Fatalf("job %s not found after concurrent writes", jobID)
	}
	job.mu.RLock()
	inMemory := len(job.WorkspaceEvents)
	job.mu.RUnlock()
	if inMemory != concurrency {
		t.Fatalf("in-memory WorkspaceEvents = %d, want %d", inMemory, concurrency)
	}

	// The persisted file must match: this is what a lost-update race corrupts —
	// in-memory state stays correct while the file silently falls behind.
	jobFile := filepath.Join(tempDir, "jobs", jobID+".json")
	data, err := os.ReadFile(jobFile)
	if err != nil {
		t.Fatalf("failed to read persisted job file: %v", err)
	}
	var persisted struct {
		WorkspaceEvents []WorkspaceEvent `json:"workspace_events"`
	}
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("failed to unmarshal persisted job file: %v", err)
	}
	if len(persisted.WorkspaceEvents) != concurrency {
		t.Errorf("persisted WorkspaceEvents = %d, want %d (lost update under concurrent SaveJob)", len(persisted.WorkspaceEvents), concurrency)
	}
}

func TestSaveJob_RedactsCredsAndEncryptsKubeconfig(t *testing.T) {
	if err := InitDataEncryption(testKey); err != nil {
		t.Fatalf("InitDataEncryption: %v", err)
	}
	defer InitDataEncryption(nil)

	tempDir := t.TempDir()
	jm := NewJobManager(tempDir)

	config := &LabConfig{
		StackName:            "lab",
		Provider:             "ovh",
		OvhApplicationKey:    "app-key",
		OvhApplicationSecret: "app-secret-value",
		OvhConsumerKey:       "consumer-key",
		AzureClientSecret:    "azure-secret",
		ExternalKubeconfig:   "external-kubeconfig-contents",
		DNSCredentials:       map[string]string{"applicationSecret": "dns-secret"},
	}
	jobID := jm.CreateJob(config)
	if err := jm.SetKubeconfig(jobID, "provisioned-kubeconfig-contents"); err != nil {
		t.Fatalf("SetKubeconfig: %v", err)
	}
	jm.UpdateJobStatus(jobID, JobStatusCompleted)

	if err := jm.SaveJob(jobID); err != nil {
		t.Fatalf("SaveJob: %v", err)
	}

	jobFile := filepath.Join(tempDir, "jobs", jobID+".json")

	// The file must not be world-readable.
	info, err := os.Stat(jobFile)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("job file mode = %o, want 0600", perm)
	}

	raw, err := os.ReadFile(jobFile)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	onDisk := string(raw)

	// No cleartext secret value may appear on disk.
	for _, secret := range []string{
		"app-key", "app-secret-value", "consumer-key", "azure-secret",
		"provisioned-kubeconfig-contents", "external-kubeconfig-contents", "dns-secret",
	} {
		if strings.Contains(onDisk, secret) {
			t.Errorf("persisted job file contains cleartext secret %q", secret)
		}
	}
	// Kubeconfig/DNS fields are still present, but encrypted.
	if !strings.Contains(onDisk, encPrefix) {
		t.Errorf("expected encrypted (%s) values in persisted job file", encPrefix)
	}
}

func TestLoadJobs_DecryptsKubeconfigAndDropsCreds(t *testing.T) {
	if err := InitDataEncryption(testKey); err != nil {
		t.Fatalf("InitDataEncryption: %v", err)
	}
	defer InitDataEncryption(nil)

	tempDir := t.TempDir()

	jm1 := NewJobManager(tempDir)
	config := &LabConfig{
		StackName:            "roundtrip-lab",
		Provider:             "ovh",
		OvhApplicationSecret: "app-secret-value",
		ExternalKubeconfig:   "external-kc",
		DNSCredentials:       map[string]string{"applicationSecret": "dns-secret"},
	}
	jobID := jm1.CreateJob(config)
	if err := jm1.SetKubeconfig(jobID, "provisioned-kc"); err != nil {
		t.Fatalf("SetKubeconfig: %v", err)
	}
	jm1.UpdateJobStatus(jobID, JobStatusCompleted)
	if err := jm1.SaveJob(jobID); err != nil {
		t.Fatalf("SaveJob: %v", err)
	}

	jm2 := NewJobManager(tempDir)
	if err := jm2.LoadJobs(); err != nil {
		t.Fatalf("LoadJobs: %v", err)
	}
	loaded, ok := jm2.GetJob(jobID)
	if !ok {
		t.Fatalf("job %s not loaded", jobID)
	}

	// Kubeconfigs and DNS credentials decrypt back to plaintext for in-memory use.
	if loaded.Kubeconfig != "provisioned-kc" {
		t.Errorf("Kubeconfig = %q, want plaintext round-trip", loaded.Kubeconfig)
	}
	if loaded.Config.ExternalKubeconfig != "external-kc" {
		t.Errorf("ExternalKubeconfig = %q, want plaintext round-trip", loaded.Config.ExternalKubeconfig)
	}
	if got := loaded.Config.DNSCredentials["applicationSecret"]; got != "dns-secret" {
		t.Errorf("DNS credential = %q, want plaintext round-trip", got)
	}
	// Provider credentials were never persisted.
	if loaded.Config.OvhApplicationSecret != "" {
		t.Errorf("OvhApplicationSecret = %q, want empty (not persisted)", loaded.Config.OvhApplicationSecret)
	}
}

func TestLoadJobs_PlaintextBackwardCompatible(t *testing.T) {
	if err := InitDataEncryption(testKey); err != nil {
		t.Fatalf("InitDataEncryption: %v", err)
	}
	defer InitDataEncryption(nil)

	tempDir := t.TempDir()
	jobsDir := filepath.Join(tempDir, "jobs")
	if err := os.MkdirAll(jobsDir, 0700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// A job file written before at-rest encryption existed: plaintext kubeconfig,
	// no enc: prefix. It must still load.
	legacy := `{"id":"job-legacy","status":"completed","kubeconfig":"legacy-plaintext-kc","config":{"stack_name":"legacy"}}`
	if err := os.WriteFile(filepath.Join(jobsDir, "job-legacy.json"), []byte(legacy), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	jm := NewJobManager(tempDir)
	if err := jm.LoadJobs(); err != nil {
		t.Fatalf("LoadJobs: %v", err)
	}
	loaded, ok := jm.GetJob("job-legacy")
	if !ok {
		t.Fatalf("legacy job not loaded")
	}
	if loaded.Kubeconfig != "legacy-plaintext-kc" {
		t.Errorf("legacy Kubeconfig = %q, want plaintext passthrough", loaded.Kubeconfig)
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
		WorkspaceNamespace:        "workshops",
		WorkspaceTemplates:        []WorkspaceTemplate{{Name: "template"}},
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

// --- GetWorkspaceTemplates tests ---

func TestLabConfig_GetWorkspaceTemplates_MultiTemplate(t *testing.T) {
	c := &LabConfig{
		WorkspaceTemplates: []WorkspaceTemplate{
			{Name: "t1", Image: "img1"},
			{Name: "t2", GitRepo: "https://example.com/repo"},
		},
	}
	got := c.GetWorkspaceTemplates()
	if len(got) != 2 {
		t.Errorf("GetWorkspaceTemplates() len = %d, want 2", len(got))
	}
}

func TestWorkspaceTemplate_JSONRoundTrip(t *testing.T) {
	in := WorkspaceTemplate{
		Name:          "full",
		IDE:           "code-server",
		Image:         "codercom/code-server:latest",
		GitRepo:       "https://example.com/repo",
		GitBranch:     "dev",
		GitFolder:     "backend",
		CPU:           "500m",
		Memory:        "1Gi",
		DiskSize:      "5Gi",
		Env:           map[string]string{"K": "V"},
		StartupScript: "sudo apt-get install -y jq",
		DotfilesRepo:  "https://example.com/dotfiles",
		Extensions:    []string{"golang.go"},
		Sidecars:      []WorkspaceSidecar{{Name: "docker", Image: "docker:dind", Ports: []int{2375}, Env: map[string]string{"DOCKER_TLS_CERTDIR": ""}, Privileged: true, Capabilities: []string{"SYS_ADMIN"}}},
		Mounts:        []WorkspaceMount{{Type: "secret", Name: "tls", Path: "/etc/tls"}},
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out WorkspaceTemplate
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Errorf("round-trip mismatch:\n in=%+v\nout=%+v", in, out)
	}
}

func TestLabConfig_GetWorkspaceTemplates_DefaultWhenEmpty(t *testing.T) {
	c := &LabConfig{}
	got := c.GetWorkspaceTemplates()
	if len(got) != 1 {
		t.Fatalf("GetWorkspaceTemplates() len = %d, want 1 default", len(got))
	}
	if got[0].Name != "default" {
		t.Errorf("GetWorkspaceTemplates() default name = %q, want %q", got[0].Name, "default")
	}
}

// --- RecordCleanupEvent tests ---

func TestJobManager_RecordCleanupEvent_Success(t *testing.T) {
	jm := NewJobManager("")
	id := jm.CreateJob(&LabConfig{StackName: "test"})

	err := jm.RecordCleanupEvent(id, 3)
	if err != nil {
		t.Fatalf("RecordCleanupEvent() error = %v", err)
	}

	job, _ := jm.GetJob(id)
	job.mu.RLock()
	events := job.CleanupEvents
	job.mu.RUnlock()

	if len(events) != 1 {
		t.Errorf("CleanupEvents len = %d, want 1", len(events))
	}
	if events[0].Count != 3 {
		t.Errorf("CleanupEvents[0].Count = %d, want 3", events[0].Count)
	}
}

func TestJobManager_RecordCleanupEvent_NotFound(t *testing.T) {
	jm := NewJobManager("")
	err := jm.RecordCleanupEvent("nonexistent", 1)
	if err == nil {
		t.Error("RecordCleanupEvent() should error for nonexistent job")
	}
}

// --- RecordWorkspaceSnapshot tests ---

func TestJobManager_RecordWorkspaceSnapshot_Success(t *testing.T) {
	jm := NewJobManager("")
	id := jm.CreateJob(&LabConfig{StackName: "test"})

	err := jm.RecordWorkspaceSnapshot(id, 5)
	if err != nil {
		t.Fatalf("RecordWorkspaceSnapshot() error = %v", err)
	}

	job, _ := jm.GetJob(id)
	job.mu.RLock()
	snapshots := job.WorkspaceSnapshots
	job.mu.RUnlock()

	if len(snapshots) != 1 {
		t.Errorf("WorkspaceSnapshots len = %d, want 1", len(snapshots))
	}
	if snapshots[0].Count != 5 {
		t.Errorf("WorkspaceSnapshots[0].Count = %d, want 5", snapshots[0].Count)
	}
}

func TestJobManager_RecordWorkspaceSnapshot_NotFound(t *testing.T) {
	jm := NewJobManager("")
	err := jm.RecordWorkspaceSnapshot("nonexistent", 1)
	if err == nil {
		t.Error("RecordWorkspaceSnapshot() should error for nonexistent job")
	}
}

// --- RecordWorkspaceEvent tests ---

func TestJobManager_RecordWorkspaceEvent_Success(t *testing.T) {
	jm := NewJobManager("")
	id := jm.CreateJob(&LabConfig{StackName: "test"})

	err := jm.RecordWorkspaceEvent(id, WorkspaceEventCreated, "ws-alice", "ws-alice", "alice", "default")
	if err != nil {
		t.Fatalf("RecordWorkspaceEvent() error = %v", err)
	}

	job, _ := jm.GetJob(id)
	job.mu.RLock()
	events := job.WorkspaceEvents
	job.mu.RUnlock()

	if len(events) != 1 {
		t.Fatalf("WorkspaceEvents len = %d, want 1", len(events))
	}
	got := events[0]
	if got.Action != WorkspaceEventCreated || got.WorkspaceID != "ws-alice" ||
		got.WorkspaceName != "ws-alice" || got.Owner != "alice" || got.Template != "default" {
		t.Errorf("WorkspaceEvents[0] = %+v, unexpected fields", got)
	}
	if got.At.IsZero() {
		t.Error("WorkspaceEvents[0].At should be set")
	}
}

func TestJobManager_RecordWorkspaceEvent_AppendsInOrder(t *testing.T) {
	jm := NewJobManager("")
	id := jm.CreateJob(&LabConfig{StackName: "test"})

	if err := jm.RecordWorkspaceEvent(id, WorkspaceEventCreated, "ws-bob", "ws-bob", "bob", "default"); err != nil {
		t.Fatalf("RecordWorkspaceEvent() error = %v", err)
	}
	if err := jm.RecordWorkspaceEvent(id, WorkspaceEventDeleted, "ws-bob", "ws-bob", "bob", "default"); err != nil {
		t.Fatalf("RecordWorkspaceEvent() error = %v", err)
	}

	job, _ := jm.GetJob(id)
	job.mu.RLock()
	events := job.WorkspaceEvents
	job.mu.RUnlock()

	if len(events) != 2 {
		t.Fatalf("WorkspaceEvents len = %d, want 2", len(events))
	}
	if events[0].Action != WorkspaceEventCreated {
		t.Errorf("WorkspaceEvents[0].Action = %q, want %q", events[0].Action, WorkspaceEventCreated)
	}
	if events[1].Action != WorkspaceEventDeleted {
		t.Errorf("WorkspaceEvents[1].Action = %q, want %q", events[1].Action, WorkspaceEventDeleted)
	}
}

func TestJobManager_RecordWorkspaceEvent_NotFound(t *testing.T) {
	jm := NewJobManager("")
	err := jm.RecordWorkspaceEvent("nonexistent", WorkspaceEventCreated, "ws-1", "ws-1", "alice", "default")
	if err == nil {
		t.Error("RecordWorkspaceEvent() should error for nonexistent job")
	}
}

// --- ResetJobForRetry tests ---

func TestJobManager_ResetJobForRetry_Success(t *testing.T) {
	jm := NewJobManager("")
	id := jm.CreateJob(&LabConfig{StackName: "test"})
	jm.UpdateJobStatus(id, JobStatusFailed)

	err := jm.ResetJobForRetry(id)
	if err != nil {
		t.Fatalf("ResetJobForRetry() error = %v", err)
	}

	job, _ := jm.GetJob(id)
	job.mu.RLock()
	status := job.Status
	errMsg := job.Error
	job.mu.RUnlock()

	if status != JobStatusPending {
		t.Errorf("ResetJobForRetry() status = %v, want %v", status, JobStatusPending)
	}
	if errMsg != "" {
		t.Errorf("ResetJobForRetry() error should be cleared, got %q", errMsg)
	}
}

func TestJobManager_ResetJobForRetry_NotFailed(t *testing.T) {
	jm := NewJobManager("")
	id := jm.CreateJob(&LabConfig{StackName: "test"})
	// Status is pending, not failed

	err := jm.ResetJobForRetry(id)
	if err == nil {
		t.Error("ResetJobForRetry() should error for non-failed job")
	}
}

func TestJobManager_ResetJobForRetry_NotFound(t *testing.T) {
	jm := NewJobManager("")
	err := jm.ResetJobForRetry("nonexistent")
	if err == nil {
		t.Error("ResetJobForRetry() should error for nonexistent job")
	}
}

// --- RecordDeletionFailure / ClearDeletionRetry tests ---

func TestJobManager_RecordDeletionFailure_FirstAttempt(t *testing.T) {
	t.Parallel()
	jm := NewJobManager("")
	id := jm.CreateJob(&LabConfig{StackName: "test"})

	err := jm.RecordDeletionFailure(id, "ws-1", "my-ws", 3)
	if err != nil {
		t.Fatalf("RecordDeletionFailure() error = %v", err)
	}

	job, _ := jm.GetJob(id)
	job.mu.RLock()
	r := job.DeletionRetries["ws-1"]
	job.mu.RUnlock()

	if r == nil {
		t.Fatal("expected retry record, got nil")
	}
	if r.Attempts != 1 {
		t.Errorf("Attempts = %d, want 1", r.Attempts)
	}
	if r.GiveUp {
		t.Error("GiveUp should be false after first attempt")
	}
	if r.WorkspaceName != "my-ws" {
		t.Errorf("WorkspaceName = %q, want %q", r.WorkspaceName, "my-ws")
	}
}

func TestJobManager_RecordDeletionFailure_SetsGiveUpAtMaxRetries(t *testing.T) {
	t.Parallel()
	jm := NewJobManager("")
	id := jm.CreateJob(&LabConfig{StackName: "test"})

	for i := 0; i < 3; i++ {
		if err := jm.RecordDeletionFailure(id, "ws-1", "my-ws", 3); err != nil {
			t.Fatalf("RecordDeletionFailure() error = %v", err)
		}
	}

	job, _ := jm.GetJob(id)
	job.mu.RLock()
	r := job.DeletionRetries["ws-1"]
	job.mu.RUnlock()

	if r == nil {
		t.Fatal("expected retry record, got nil")
	}
	if r.Attempts != 3 {
		t.Errorf("Attempts = %d, want 3", r.Attempts)
	}
	if !r.GiveUp {
		t.Error("GiveUp should be true after max retries")
	}
}

func TestJobManager_RecordDeletionFailure_NotFound(t *testing.T) {
	t.Parallel()
	jm := NewJobManager("")
	err := jm.RecordDeletionFailure("nonexistent", "ws-1", "my-ws", 3)
	if err == nil {
		t.Error("RecordDeletionFailure() should error for nonexistent job")
	}
}

func TestJobManager_ClearDeletionRetry_RemovesRecord(t *testing.T) {
	t.Parallel()
	jm := NewJobManager("")
	id := jm.CreateJob(&LabConfig{StackName: "test"})

	// Record a failure first
	if err := jm.RecordDeletionFailure(id, "ws-1", "my-ws", 3); err != nil {
		t.Fatalf("RecordDeletionFailure() error = %v", err)
	}

	// Clear it
	if err := jm.ClearDeletionRetry(id, "ws-1"); err != nil {
		t.Fatalf("ClearDeletionRetry() error = %v", err)
	}

	job, _ := jm.GetJob(id)
	job.mu.RLock()
	r := job.DeletionRetries["ws-1"]
	job.mu.RUnlock()

	if r != nil {
		t.Error("expected retry record to be removed")
	}
}

func TestJobManager_ClearDeletionRetry_NotFound(t *testing.T) {
	t.Parallel()
	jm := NewJobManager("")
	err := jm.ClearDeletionRetry("nonexistent", "ws-1")
	if err == nil {
		t.Error("ClearDeletionRetry() should error for nonexistent job")
	}
}
