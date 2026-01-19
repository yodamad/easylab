package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// =============================================================================
// Chaos Testing Helpers
// =============================================================================

// randomString generates a random string of given length
func randomString(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}

// randomBytes generates random bytes of given length
func randomBytes(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

// withTimeout runs a function with a timeout
func withTimeout(t *testing.T, timeout time.Duration, fn func()) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()

	select {
	case <-done:
		// Success
	case <-time.After(timeout):
		t.Fatalf("Test timed out after %v", timeout)
	}
}

// =============================================================================
// Concurrent Access Chaos Tests
// =============================================================================

func TestChaos_JobManager_ConcurrentCreateAndRead(t *testing.T) {
	jm := NewJobManager("")
	config := &LabConfig{StackName: "chaos-test"}

	var wg sync.WaitGroup
	var createdJobs sync.Map
	errCount := int32(0)

	// Launch many concurrent operations
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			
			// Randomly create or read
			if idx%2 == 0 {
				id := jm.CreateJob(config)
				createdJobs.Store(id, true)
			} else {
				// Try to read a job that may or may not exist
				jm.GetAllJobs()
			}
		}(i)
	}

	wg.Wait()

	if errCount > 0 {
		t.Errorf("Got %d errors during concurrent operations", errCount)
	}

	// Verify we can still get all jobs
	jobs := jm.GetAllJobs()
	if len(jobs) == 0 {
		t.Error("No jobs were created")
	}
}

func TestChaos_JobManager_ConcurrentStatusUpdates(t *testing.T) {
	jm := NewJobManager("")
	config := &LabConfig{StackName: "chaos-status"}
	jobID := jm.CreateJob(config)

	var wg sync.WaitGroup
	statuses := []JobStatus{
		JobStatusPending,
		JobStatusRunning,
		JobStatusCompleted,
		JobStatusFailed,
	}

	// Rapidly update status from multiple goroutines
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			status := statuses[idx%len(statuses)]
			jm.UpdateJobStatus(jobID, status)
		}(i)
	}

	wg.Wait()

	// Job should still be retrievable
	job, exists := jm.GetJob(jobID)
	if !exists {
		t.Error("Job disappeared after concurrent status updates")
	}
	if job == nil {
		t.Error("Job is nil after concurrent status updates")
	}
}

func TestChaos_JobManager_ConcurrentOutputAppend(t *testing.T) {
	jm := NewJobManager("")
	config := &LabConfig{StackName: "chaos-output"}
	jobID := jm.CreateJob(config)

	var wg sync.WaitGroup
	lineCount := 100

	// Rapidly append output from multiple goroutines
	for i := 0; i < lineCount; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			jm.AppendOutput(jobID, fmt.Sprintf("Line %d", idx))
		}(i)
	}

	wg.Wait()

	job, _ := jm.GetJob(jobID)
	if len(job.Output) == 0 {
		t.Error("No output was appended")
	}
}

func TestChaos_CredentialsManager_ConcurrentSetAndGet(t *testing.T) {
	cm := &CredentialsManager{}

	var wg sync.WaitGroup
	errCount := int32(0)

	// Concurrent sets and gets
	for i := 0; i < 100; i++ {
		wg.Add(2)

		// Setter
		go func(idx int) {
			defer wg.Done()
			creds := &OVHCredentials{
				ApplicationKey:    fmt.Sprintf("key-%d", idx),
				ApplicationSecret: fmt.Sprintf("secret-%d", idx),
				ConsumerKey:       fmt.Sprintf("consumer-%d", idx),
				ServiceName:       fmt.Sprintf("service-%d", idx),
				Endpoint:          "ovh-eu",
			}
			if err := cm.SetCredentials(creds); err != nil {
				atomic.AddInt32(&errCount, 1)
			}
		}(i)

		// Getter
		go func() {
			defer wg.Done()
			cm.GetCredentials("ovh")
			cm.HasCredentials("ovh")
		}()
	}

	wg.Wait()

	// All sets should succeed since all fields are valid
	if errCount > 0 {
		t.Errorf("Got %d errors during concurrent credential operations", errCount)
	}
}

func TestChaos_AuthHandler_ConcurrentSessions(t *testing.T) {
	ah := &AuthHandler{
		sessions:        make(map[string]*Session),
		studentSessions: make(map[string]*Session),
	}

	var wg sync.WaitGroup
	var tokens sync.Map

	// Create many sessions concurrently
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			token := ah.createSession()
			tokens.Store(token, true)
		}()
	}

	wg.Wait()

	// Validate all sessions concurrently
	tokens.Range(func(key, _ interface{}) bool {
		wg.Add(1)
		go func(token string) {
			defer wg.Done()
			ah.validateSession(token)
		}(key.(string))
		return true
	})

	wg.Wait()

	// Delete all sessions concurrently
	tokens.Range(func(key, _ interface{}) bool {
		wg.Add(1)
		go func(token string) {
			defer wg.Done()
			ah.deleteSession(token)
		}(key.(string))
		return true
	})

	wg.Wait()
}

// =============================================================================
// Input Chaos Tests - Malformed/Edge Case Inputs
// =============================================================================

func TestChaos_JobManager_NilConfig(t *testing.T) {
	jm := NewJobManager("")

	// Create job with nil config should not panic
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("Panic on nil config: %v", r)
		}
	}()

	jobID := jm.CreateJob(nil)
	job, exists := jm.GetJob(jobID)
	if !exists {
		t.Error("Job with nil config not created")
	}
	if job.Config != nil {
		t.Error("Expected nil config")
	}
}

func TestChaos_JobManager_EmptyStrings(t *testing.T) {
	jm := NewJobManager("")
	config := &LabConfig{
		StackName:            "",
		OvhApplicationKey:    "",
		OvhApplicationSecret: "",
		K8sClusterName:       "",
	}

	jobID := jm.CreateJob(config)
	job, exists := jm.GetJob(jobID)
	if !exists {
		t.Error("Job with empty config not created")
	}
	if job.Config.StackName != "" {
		t.Error("Expected empty StackName")
	}
}

func TestChaos_JobManager_VeryLongStrings(t *testing.T) {
	jm := NewJobManager("")
	longString := randomString(10000)
	config := &LabConfig{
		StackName:      longString,
		K8sClusterName: longString,
	}

	jobID := jm.CreateJob(config)
	job, exists := jm.GetJob(jobID)
	if !exists {
		t.Error("Job with long strings not created")
	}
	if job.Config.StackName != longString {
		t.Error("Long string was modified")
	}
}

func TestChaos_JobManager_SpecialCharacters(t *testing.T) {
	jm := NewJobManager("")
	specialStrings := []string{
		"<script>alert('xss')</script>",
		"'; DROP TABLE jobs;--",
		"../../../etc/passwd",
		"\x00\x01\x02\x03",
		"日本語テスト",
		"emoji 🚀🎉",
		"newline\n\r\ttab",
		strings.Repeat("a", 1000),
	}

	for _, s := range specialStrings {
		config := &LabConfig{StackName: s}
		jobID := jm.CreateJob(config)
		job, exists := jm.GetJob(jobID)
		if !exists {
			t.Errorf("Job with special string %q not created", s[:min(len(s), 20)])
		}
		if job.Config.StackName != s {
			t.Errorf("Special string %q was modified", s[:min(len(s), 20)])
		}
	}
}

func TestChaos_CredentialsManager_MalformedInputs(t *testing.T) {
	cm := &CredentialsManager{}

	malformedCreds := []*OVHCredentials{
		nil,
		{ApplicationKey: "", ApplicationSecret: "s", ConsumerKey: "c", ServiceName: "s", Endpoint: "e"},
		{ApplicationKey: "k", ApplicationSecret: "", ConsumerKey: "c", ServiceName: "s", Endpoint: "e"},
		{ApplicationKey: "k", ApplicationSecret: "s", ConsumerKey: "", ServiceName: "s", Endpoint: "e"},
		{ApplicationKey: "k", ApplicationSecret: "s", ConsumerKey: "c", ServiceName: "", Endpoint: "e"},
		{ApplicationKey: "k", ApplicationSecret: "s", ConsumerKey: "c", ServiceName: "s", Endpoint: ""},
	}

	for i, creds := range malformedCreds {
		if creds == nil {
			// nil should cause panic or be handled - skip for now
			continue
		}
		err := cm.SetCredentials(creds)
		if err == nil {
			t.Errorf("Case %d: Expected error for malformed credentials", i)
		}
	}
}

func TestChaos_HashPassword_EdgeCases(t *testing.T) {
	edgeCases := []string{
		"",
		" ",
		"\t\n\r",
		strings.Repeat("a", 100000),
		string(randomBytes(1000)),
	}

	for _, input := range edgeCases {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Panic on input length %d: %v", len(input), r)
			}
		}()

		hash := hashPassword(input)
		if len(hash) != 64 {
			t.Errorf("Hash length %d for input length %d", len(hash), len(input))
		}
	}
}

func TestChaos_GenerateSecurePassword_Stress(t *testing.T) {
	// Generate many passwords rapidly
	var wg sync.WaitGroup
	errCount := int32(0)

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			password, err := GenerateSecurePassword()
			if err != nil {
				atomic.AddInt32(&errCount, 1)
				return
			}
			if len(password) < 16 {
				atomic.AddInt32(&errCount, 1)
			}
		}()
	}

	wg.Wait()

	if errCount > 0 {
		t.Errorf("Got %d errors during concurrent password generation", errCount)
	}
}

// =============================================================================
// HTTP Handler Chaos Tests
// =============================================================================

func TestChaos_Handler_MalformedRequests(t *testing.T) {
	jm := NewJobManager("")
	h := NewHandler(jm, &PulumiExecutor{}, &CredentialsManager{})

	tests := []struct {
		name        string
		method      string
		path        string
		body        string
		contentType string
	}{
		{"empty body POST", "POST", "/api/labs", "", "application/x-www-form-urlencoded"},
		{"invalid content-type", "POST", "/api/labs", "data", "invalid/type"},
		{"very long path", "GET", "/" + strings.Repeat("a", 10000), "", ""},
		{"path traversal attempt", "GET", "/../../../etc/passwd", "", ""},
		// Note: null bytes in URL are rejected by Go's net/http - this is expected
		// {"null bytes in path", "GET", "/api/\x00jobs", "", ""},
		{"unicode in path", "GET", "/api/jobs/日本語", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("Panic on %s: %v", tt.name, r)
				}
			}()

			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			if tt.contentType != "" {
				req.Header.Set("Content-Type", tt.contentType)
			}
			w := httptest.NewRecorder()

			// Route to appropriate handler based on path
			if strings.HasPrefix(tt.path, "/api/jobs") {
				h.GetJobStatus(w, req)
			} else if strings.HasPrefix(tt.path, "/api/labs") {
				h.CreateLab(w, req)
			} else {
				h.ServeUI(w, req)
			}

			// Should not panic, response code doesn't matter
		})
	}
}

func TestChaos_Handler_ConcurrentRequests(t *testing.T) {
	jm := NewJobManager("")
	h := NewHandler(jm, &PulumiExecutor{}, &CredentialsManager{})

	var wg sync.WaitGroup
	requestCount := 100

	// Create a job to query
	config := &LabConfig{StackName: "chaos"}
	jobID := jm.CreateJob(config)

	for i := 0; i < requestCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest("GET", "/api/jobs/"+jobID+"/status", nil)
			w := httptest.NewRecorder()
			h.GetJobStatus(w, req)
		}()
	}

	wg.Wait()
}

func TestChaos_Handler_RapidFormSubmissions(t *testing.T) {
	cm := &CredentialsManager{}
	jm := NewJobManager("")
	h := NewHandler(jm, &PulumiExecutor{}, cm)

	var wg sync.WaitGroup

	// Simulate rapid form submissions
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			form := url.Values{}
			form.Set("ovh_application_key", fmt.Sprintf("key-%d", idx))
			form.Set("ovh_application_secret", fmt.Sprintf("secret-%d", idx))
			form.Set("ovh_consumer_key", fmt.Sprintf("consumer-%d", idx))
			form.Set("ovh_service_name", fmt.Sprintf("service-%d", idx))
			form.Set("ovh_endpoint", "ovh-eu")

			req := httptest.NewRequest("POST", "/api/credentials", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			w := httptest.NewRecorder()

			h.SetOVHCredentials(w, req)
		}(i)
	}

	wg.Wait()
}

func TestChaos_Handler_MixedConcurrentOperations(t *testing.T) {
	jm := NewJobManager("")
	cm := &CredentialsManager{}
	h := NewHandler(jm, &PulumiExecutor{}, cm)

	// Set up credentials
	cm.SetCredentials(&OVHCredentials{
		ApplicationKey:    "key",
		ApplicationSecret: "secret",
		ConsumerKey:       "consumer",
		ServiceName:       "service",
		Endpoint:          "endpoint",
	})

	var wg sync.WaitGroup

	// Mix of different operations
	operations := []func(){
		func() {
			req := httptest.NewRequest("GET", "/api/ovh-credentials", nil)
			w := httptest.NewRecorder()
			h.GetOVHCredentials(w, req)
		},
		func() {
			config := &LabConfig{StackName: "chaos"}
			jm.CreateJob(config)
		},
		func() {
			jm.GetAllJobs()
		},
		func() {
			req := httptest.NewRequest("GET", "/", nil)
			w := httptest.NewRecorder()
			h.ServeUI(w, req)
		},
	}

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			op := operations[idx%len(operations)]
			op()
		}(i)
	}

	wg.Wait()
}

// =============================================================================
// State Chaos Tests
// =============================================================================

func TestChaos_JobManager_JobNotFoundOperations(t *testing.T) {
	jm := NewJobManager("")

	nonExistentIDs := []string{
		"nonexistent",
		"",
		"job-0",
		randomString(100),
		"../../../etc/passwd",
	}

	for _, id := range nonExistentIDs {
		// All operations should return appropriate errors, not panic
		_, exists := jm.GetJob(id)
		if exists {
			t.Errorf("Job %q should not exist", id)
		}

		err := jm.UpdateJobStatus(id, JobStatusRunning)
		if err == nil {
			t.Errorf("UpdateJobStatus should fail for %q", id)
		}

		err = jm.AppendOutput(id, "test")
		if err == nil {
			t.Errorf("AppendOutput should fail for %q", id)
		}

		err = jm.SetError(id, errors.New("test"))
		if err == nil {
			t.Errorf("SetError should fail for %q", id)
		}

		err = jm.SetKubeconfig(id, "test")
		if err == nil {
			t.Errorf("SetKubeconfig should fail for %q", id)
		}
	}
}

func TestChaos_JobManager_PersistenceWithCorruptedFile(t *testing.T) {
	tempDir := t.TempDir()
	jobsDir := filepath.Join(tempDir, "jobs")
	os.MkdirAll(jobsDir, 0755)

	// Create corrupted JSON files
	corruptedFiles := []struct {
		name    string
		content string
	}{
		{"corrupt1.json", "{ invalid json"},
		{"corrupt2.json", ""},
		{"corrupt3.json", "null"},
		{"corrupt4.json", "[]"},
		{"corrupt5.json", string(randomBytes(1000))},
	}

	for _, f := range corruptedFiles {
		os.WriteFile(filepath.Join(jobsDir, f.name), []byte(f.content), 0644)
	}

	// Create one valid completed job
	validJob := &Job{
		ID:        "job-valid",
		Status:    JobStatusCompleted,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Config:    &LabConfig{StackName: "valid"},
	}
	validData, _ := json.Marshal(validJob)
	os.WriteFile(filepath.Join(jobsDir, "job-valid.json"), validData, 0644)

	// Load jobs - should not panic and should load valid job
	jm := NewJobManager(tempDir)
	err := jm.LoadJobs()
	if err != nil {
		t.Errorf("LoadJobs failed: %v", err)
	}

	// Valid job should be loaded
	job, exists := jm.GetJob("job-valid")
	if !exists {
		t.Error("Valid job was not loaded")
	}
	if job.Config.StackName != "valid" {
		t.Error("Job config was corrupted during load")
	}
}

func TestChaos_JobManager_RapidSaveAndLoad(t *testing.T) {
	tempDir := t.TempDir()
	jm := NewJobManager(tempDir)

	var wg sync.WaitGroup

	// Create, complete, and save jobs rapidly
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			config := &LabConfig{StackName: fmt.Sprintf("chaos-%d", idx)}
			jobID := jm.CreateJob(config)
			jm.UpdateJobStatus(jobID, JobStatusCompleted)
			jm.SaveJob(jobID)
		}(i)
	}

	wg.Wait()

	// Load in a new manager
	jm2 := NewJobManager(tempDir)
	err := jm2.LoadJobs()
	if err != nil {
		t.Errorf("LoadJobs after rapid saves failed: %v", err)
	}
}

// =============================================================================
// Recovery Chaos Tests
// =============================================================================

func TestChaos_Recovery_AfterCredentialsCleared(t *testing.T) {
	cm := &CredentialsManager{}

	// Set credentials
	cm.SetCredentials(&OVHCredentials{
		ApplicationKey:    "key",
		ApplicationSecret: "secret",
		ConsumerKey:       "consumer",
		ServiceName:       "service",
		Endpoint:          "endpoint",
	})

	// Clear and try to use
	cm.ClearCredentials("ovh")

	_, err := cm.GetCredentials("ovh")
	if err == nil {
		t.Error("Expected error after clearing credentials")
	}

	// Should be able to set new credentials
	err = cm.SetCredentials(&OVHCredentials{
		ApplicationKey:    "new-key",
		ApplicationSecret: "new-secret",
		ConsumerKey:       "new-consumer",
		ServiceName:       "new-service",
		Endpoint:          "new-endpoint",
	})
	if err != nil {
		t.Errorf("Failed to set new credentials: %v", err)
	}
}

func TestChaos_Recovery_AfterJobRemoval(t *testing.T) {
	jm := NewJobManager("")

	// Create and remove job
	config := &LabConfig{StackName: "to-remove"}
	jobID := jm.CreateJob(config)
	jm.RemoveJob(jobID)

	// Operations on removed job should fail gracefully
	_, exists := jm.GetJob(jobID)
	if exists {
		t.Error("Removed job still exists")
	}

	// Should be able to create new jobs
	newJobID := jm.CreateJob(config)
	_, exists = jm.GetJob(newJobID)
	if !exists {
		t.Error("New job not created after removal")
	}
}

func TestChaos_Recovery_SessionAfterExpiry(t *testing.T) {
	ah := &AuthHandler{
		sessions: make(map[string]*Session),
	}

	// Create expired session manually
	token := "expired-token"
	ah.sessions[token] = &Session{
		Token:     token,
		ExpiresAt: time.Now().Add(-1 * time.Hour),
	}

	// Validate should fail
	if ah.validateSession(token) {
		t.Error("Expired session validated")
	}

	// Create new session should work
	newToken := ah.createSession()
	if !ah.validateSession(newToken) {
		t.Error("New session not valid")
	}
}

// =============================================================================
// Timeout Chaos Tests
// =============================================================================

func TestChaos_Operations_WithTimeout(t *testing.T) {
	jm := NewJobManager("")
	config := &LabConfig{StackName: "timeout-test"}

	// Create jobs with timeout
	withTimeout(t, 5*time.Second, func() {
		for i := 0; i < 100; i++ {
			jm.CreateJob(config)
		}
	})

	// Get all jobs with timeout
	withTimeout(t, 5*time.Second, func() {
		for i := 0; i < 100; i++ {
			jm.GetAllJobs()
		}
	})
}

func TestChaos_Password_GenerationTimeout(t *testing.T) {
	withTimeout(t, 10*time.Second, func() {
		for i := 0; i < 1000; i++ {
			_, err := GenerateSecurePassword()
			if err != nil {
				t.Fatalf("Password generation failed: %v", err)
			}
		}
	})
}

// =============================================================================
// Context Cancellation Tests
// =============================================================================

func TestChaos_Handler_ContextCancellation(t *testing.T) {
	jm := NewJobManager("")
	h := NewHandler(jm, &PulumiExecutor{}, &CredentialsManager{})

	// Create a request with cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	req := httptest.NewRequest("GET", "/", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	// Should not panic
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("Panic on cancelled context: %v", r)
		}
	}()

	h.ServeUI(w, req)
}

// =============================================================================
// Resource Exhaustion Simulation
// =============================================================================

func TestChaos_JobManager_ManyJobs(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping resource exhaustion test in short mode")
	}

	jm := NewJobManager("")
	config := &LabConfig{StackName: "exhaustion"}

	// Create many jobs
	jobCount := 1000
	for i := 0; i < jobCount; i++ {
		jm.CreateJob(config)
	}

	jobs := jm.GetAllJobs()
	if len(jobs) == 0 {
		t.Error("No jobs created")
	}

	// All operations should still work
	newJobID := jm.CreateJob(config)
	_, exists := jm.GetJob(newJobID)
	if !exists {
		t.Error("Cannot create new job after many jobs")
	}
}

func TestChaos_JobManager_LargeOutput(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping large output test in short mode")
	}

	jm := NewJobManager("")
	config := &LabConfig{StackName: "large-output"}
	jobID := jm.CreateJob(config)

	// Append many large output lines
	for i := 0; i < 1000; i++ {
		jm.AppendOutput(jobID, randomString(1000))
	}

	job, _ := jm.GetJob(jobID)
	if len(job.Output) != 1000 {
		t.Errorf("Expected 1000 output lines, got %d", len(job.Output))
	}
}

// =============================================================================
// Error Injection Tests
// =============================================================================

func TestChaos_Handler_DirectoryTraversalAttempts(t *testing.T) {
	jm := NewJobManager("")
	h := NewHandler(jm, &PulumiExecutor{}, &CredentialsManager{})

	traversalPaths := []string{
		"/static/../../../etc/passwd",
		"/static/..\\..\\..\\etc\\passwd",
		"/static/%2e%2e%2f%2e%2e%2f",
		"/static/....//....//",
	}

	for _, path := range traversalPaths {
		req := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()

		h.ServeStatic(w, req)

		if w.Code == http.StatusOK {
			t.Errorf("Directory traversal allowed: %s", path)
		}
	}
}

func TestChaos_Handler_HTTPMethodMismatch(t *testing.T) {
	jm := NewJobManager("")
	h := NewHandler(jm, &PulumiExecutor{}, &CredentialsManager{})

	// Try wrong methods on various endpoints
	methodTests := []struct {
		handler func(http.ResponseWriter, *http.Request)
		path    string
		methods []string
	}{
		{h.CreateLab, "/api/labs", []string{"GET", "PUT", "DELETE", "PATCH"}},
		{h.DryRunLab, "/api/labs/dry-run", []string{"GET", "PUT", "DELETE"}},
		{h.LaunchLab, "/api/labs/launch", []string{"GET", "PUT", "DELETE"}},
		{h.SetOVHCredentials, "/api/credentials", []string{"GET", "PUT", "DELETE"}},
		{h.DestroyStack, "/api/stack/destroy", []string{"GET", "PUT", "DELETE"}},
	}

	for _, mt := range methodTests {
		for _, method := range mt.methods {
			req := httptest.NewRequest(method, mt.path, nil)
			w := httptest.NewRecorder()

			mt.handler(w, req)

			// Should return 405 Method Not Allowed or similar
			if w.Code == http.StatusOK {
				t.Errorf("Unexpected 200 for %s %s", method, mt.path)
			}
		}
	}
}

// Helper function
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

