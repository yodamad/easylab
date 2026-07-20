package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

func TestPulumiExecutor_outputValueToString(t *testing.T) {
	pe := &PulumiExecutor{}

	tests := []struct {
		name     string
		input    interface{}
		expected string
	}{
		{
			name:     "plain string",
			input:    "http://example.com",
			expected: "http://example.com",
		},
		{
			name:     "nil value",
			input:    nil,
			expected: "",
		},
		{
			name: "ConfigValue map format",
			input: map[string]interface{}{
				"Value":  "http://example.com",
				"Secret": false,
			},
			expected: "http://example.com",
		},
		{
			name: "ConfigValue map with secret true",
			input: map[string]interface{}{
				"Value":  "secret-token",
				"Secret": true,
			},
			expected: "secret-token",
		},
		{
			name:     "JSON-encoded string",
			input:    json.RawMessage(`"http://example.com"`),
			expected: "http://example.com",
		},
		{
			name:     "number",
			input:    123,
			expected: "123",
		},
		{
			name: "map without Value key",
			input: map[string]interface{}{
				"other": "value",
			},
			expected: `{"other":"value"}`,
		},
		{
			name:     "pulumi.StringOutput (should return empty)",
			input:    pulumi.String("test").ToStringOutput(),
			expected: "",
		},
		{
			name: "ConfigValue JSON marshaled",
			input: func() interface{} {
				jsonStr := `{"Value":"http://example.com","Secret":false}`
				var result interface{}
				json.Unmarshal([]byte(jsonStr), &result)
				return result
			}(),
			expected: "http://example.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := pe.outputValueToString(tt.input)
			if result != tt.expected {
				t.Errorf("outputValueToString() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func Test_extractStringFromConfigValue(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "plain string",
			input:    "http://example.com",
			expected: "http://example.com",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "ConfigValue JSON string",
			input:    `{"Value":"http://example.com","Secret":false}`,
			expected: "http://example.com",
		},
		{
			name:     "ConfigValue JSON with secret true",
			input:    `{"Value":"secret-token","Secret":true}`,
			expected: "secret-token",
		},
		{
			name:     "invalid JSON (not ConfigValue)",
			input:    `{"other":"value"}`,
			expected: `{"other":"value"}`,
		},
		{
			name:     "not JSON at all",
			input:    "just a regular string",
			expected: "just a regular string",
		},
		{
			name:     "malformed JSON",
			input:    `{"Value":"test"`,
			expected: `{"Value":"test"`,
		},
		{
			name:     "ConfigValue with non-string Value",
			input:    `{"Value":123,"Secret":false}`,
			expected: `{"Value":123,"Secret":false}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := extractStringFromConfigValue(tt.input)
			if result != tt.expected {
				t.Errorf("extractStringFromConfigValue() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestGetEnvOrDefault_Server(t *testing.T) {
	tests := []struct {
		name         string
		key          string
		envValue     string
		defaultValue string
		want         string
	}{
		{"env set", "TEST_SERVER_PRESENT", "set-value", "default", "set-value"},
		{"env not set", "TEST_SERVER_ABSENT", "", "fallback", "fallback"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(tt.key, tt.envValue)
			got := getEnvOrDefault(tt.key, tt.defaultValue)
			if got != tt.want {
				t.Errorf("getEnvOrDefault(%q, %q) = %q, want %q", tt.key, tt.defaultValue, got, tt.want)
			}
		})
	}
}

func TestGetAppBaseDir_WorkDir(t *testing.T) {
	t.Setenv("WORK_DIR", "/app/jobs")
	t.Setenv("PULUMI_HOME", "")
	t.Setenv("GOMODCACHE", "")
	t.Setenv("GOPATH", "")
	t.Setenv("DATA_DIR", "")
	got := getAppBaseDir()
	if got != "/app" {
		t.Errorf("getAppBaseDir() = %q, want /app", got)
	}
}

func TestGetAppBaseDir_Fallback(t *testing.T) {
	t.Setenv("WORK_DIR", "")
	t.Setenv("PULUMI_HOME", "")
	t.Setenv("GOMODCACHE", "")
	t.Setenv("GOPATH", "")
	t.Setenv("DATA_DIR", "")
	got := getAppBaseDir()
	if got != "/app" {
		t.Errorf("getAppBaseDir() fallback = %q, want /app", got)
	}
}

func TestGetAppBaseDir_PulumiHome(t *testing.T) {
	t.Setenv("WORK_DIR", "")
	t.Setenv("PULUMI_HOME", "/app/.pulumi")
	t.Setenv("GOMODCACHE", "")
	t.Setenv("GOPATH", "")
	t.Setenv("DATA_DIR", "")
	got := getAppBaseDir()
	if got != "/app" {
		t.Errorf("getAppBaseDir() PULUMI_HOME = %q, want /app", got)
	}
}

func TestGetAppBaseDir_GOMODCACHE(t *testing.T) {
	t.Setenv("WORK_DIR", "")
	t.Setenv("PULUMI_HOME", "")
	t.Setenv("GOMODCACHE", "/app/.go/pkg/mod")
	t.Setenv("GOPATH", "")
	t.Setenv("DATA_DIR", "")
	got := getAppBaseDir()
	if got != "/app" {
		t.Errorf("getAppBaseDir() GOMODCACHE = %q, want /app", got)
	}
}

func TestGetAppBaseDir_GOPATH(t *testing.T) {
	t.Setenv("WORK_DIR", "")
	t.Setenv("PULUMI_HOME", "")
	t.Setenv("GOMODCACHE", "")
	t.Setenv("GOPATH", "/app/.go")
	t.Setenv("DATA_DIR", "")
	got := getAppBaseDir()
	if got != "/app" {
		t.Errorf("getAppBaseDir() GOPATH = %q, want /app", got)
	}
}

func TestGetAppBaseDir_DataDir(t *testing.T) {
	t.Setenv("WORK_DIR", "")
	t.Setenv("PULUMI_HOME", "")
	t.Setenv("GOMODCACHE", "")
	t.Setenv("GOPATH", "")
	t.Setenv("DATA_DIR", "/app/data")
	got := getAppBaseDir()
	if got != "/app" {
		t.Errorf("getAppBaseDir() DATA_DIR = %q, want /app", got)
	}
}

func TestGetAppBaseDir_RootWorkDir_Fallthrough(t *testing.T) {
	// WORK_DIR at root level should fall through to PULUMI_HOME
	t.Setenv("WORK_DIR", "/jobs")       // filepath.Dir("/jobs") = "/"
	t.Setenv("PULUMI_HOME", "/app/.pulumi")
	t.Setenv("GOMODCACHE", "")
	t.Setenv("GOPATH", "")
	t.Setenv("DATA_DIR", "")
	got := getAppBaseDir()
	if got != "/app" {
		t.Errorf("getAppBaseDir() root WORK_DIR = %q, want /app", got)
	}
}

func TestNewPulumiExecutor_And_GetWorkDir(t *testing.T) {
	jm := NewJobManager("")
	pe := NewPulumiExecutor(jm, "/tmp/test-workdir")
	if pe == nil {
		t.Fatal("NewPulumiExecutor() returned nil")
	}
	if pe.GetWorkDir() != "/tmp/test-workdir" {
		t.Errorf("GetWorkDir() = %q, want /tmp/test-workdir", pe.GetWorkDir())
	}
}

func TestJobOutputWriter_Write_And_Flush(t *testing.T) {
	jm := NewJobManager("")
	id := jm.CreateJob(&LabConfig{StackName: "test"})
	w := &jobOutputWriter{jobID: id, jobManager: jm}

	n, err := w.Write([]byte("line one\nline two\n"))
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if n != 18 {
		t.Errorf("Write() n = %d, want 18", n)
	}

	// Flush remaining buffer (returns void)
	w.Flush()
}

func TestJobOutputWriter_Write_NoNewline(t *testing.T) {
	jm := NewJobManager("")
	id := jm.CreateJob(&LabConfig{StackName: "test"})
	w := &jobOutputWriter{jobID: id, jobManager: jm}

	// Write without newline - buffered until Flush
	_, err := w.Write([]byte("partial line"))
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	w.Flush()
}

func TestIsJobDirectoryReady_MissingDir(t *testing.T) {
	pe := &PulumiExecutor{workDir: t.TempDir()}
	if pe.isJobDirectoryReady("nonexistent-job") {
		t.Error("isJobDirectoryReady() should return false for missing dir")
	}
}

func TestIsJobDirectoryReady_MissingFiles(t *testing.T) {
	dir := t.TempDir()
	pe := &PulumiExecutor{workDir: dir}

	jobID := "test-job"
	jobDir := filepath.Join(dir, jobID)
	if err := os.MkdirAll(jobDir, 0755); err != nil {
		t.Fatal(err)
	}

	if pe.isJobDirectoryReady(jobID) {
		t.Error("isJobDirectoryReady() should return false when required files are missing")
	}
}

func TestGetLocalBackendEnvVars_NoWorkDir(t *testing.T) {
	vars := getLocalBackendEnvVars()
	if vars["PULUMI_BACKEND_URL"] != "file://" {
		t.Errorf("PULUMI_BACKEND_URL = %q, want file://", vars["PULUMI_BACKEND_URL"])
	}
	if vars["PULUMI_CONFIG_PASSPHRASE"] == "" {
		t.Error("PULUMI_CONFIG_PASSPHRASE should not be empty")
	}
	if vars["GOWORK"] != "off" {
		t.Errorf("GOWORK = %q, want off", vars["GOWORK"])
	}
}

func TestGetLocalBackendEnvVars_WithWorkDir(t *testing.T) {
	dir := t.TempDir()
	vars := getLocalBackendEnvVars(dir)
	if vars["GOMODCACHE"] != dir+"/.gomodcache" {
		t.Errorf("GOMODCACHE = %q, want %s/.gomodcache", vars["GOMODCACHE"], dir)
	}
}

func TestGetPulumiEnvVars_OVHProvider(t *testing.T) {
	cfg := &LabConfig{
		Provider:             "ovh",
		OvhApplicationKey:    "app-key",
		OvhApplicationSecret: "app-secret",
		OvhConsumerKey:       "consumer-key",
		OvhServiceName:       "service",
	}
	vars := getPulumiEnvVars(cfg)
	if vars["OVH_APPLICATION_KEY"] != "app-key" {
		t.Errorf("OVH_APPLICATION_KEY = %q, want app-key", vars["OVH_APPLICATION_KEY"])
	}
}

func TestGetPulumiEnvVars_AzureProvider(t *testing.T) {
	cfg := &LabConfig{
		Provider:            "azure",
		AzureClientID:       "client-id",
		AzureClientSecret:   "secret",
		AzureTenantID:       "tenant",
		AzureSubscriptionID: "sub-id",
	}
	vars := getPulumiEnvVars(cfg)
	if vars["AZURE_CLIENT_ID"] != "client-id" {
		t.Errorf("AZURE_CLIENT_ID = %q, want client-id", vars["AZURE_CLIENT_ID"])
	}
}

func TestGetPulumiEnvVars_NilConfig(t *testing.T) {
	// Should not panic with nil config
	vars := getPulumiEnvVars(nil)
	if vars["PULUMI_BACKEND_URL"] != "file://" {
		t.Errorf("PULUMI_BACKEND_URL = %q, want file://", vars["PULUMI_BACKEND_URL"])
	}
}

func TestGetPulumiEnvVars_DefaultOVH(t *testing.T) {
	// Empty provider defaults to OVH
	cfg := &LabConfig{OvhApplicationKey: "key"}
	vars := getPulumiEnvVars(cfg)
	if vars["OVH_APPLICATION_KEY"] != "key" {
		t.Errorf("OVH_APPLICATION_KEY = %q, want key", vars["OVH_APPLICATION_KEY"])
	}
}

func TestGetConfigCommands_BYOK(t *testing.T) {
	pe := &PulumiExecutor{}
	cfg := &LabConfig{
		UseExistingCluster: true,
		WorkspaceNamespace: "workshops",
	}
	cmds := pe.getConfigCommands(cfg)
	if len(cmds) == 0 {
		t.Error("getConfigCommands() returned empty for BYOK mode")
	}
	// Find k8s:useExistingCluster
	found := false
	for _, c := range cmds {
		if c.key == "k8s:useExistingCluster" && c.value == "true" {
			found = true
		}
	}
	if !found {
		t.Error("getConfigCommands() BYOK should contain k8s:useExistingCluster=true")
	}
}

func TestGetConfigCommands_OVH(t *testing.T) {
	pe := &PulumiExecutor{}
	cfg := &LabConfig{
		Provider:         "ovh",
		StackName:        "my-stack",
		NetworkGatewayName: "gw",
	}
	cmds := pe.getConfigCommands(cfg)
	if len(cmds) == 0 {
		t.Error("getConfigCommands() returned empty for OVH mode")
	}
}

func TestGetConfigCommands_Azure(t *testing.T) {
	pe := &PulumiExecutor{}
	cfg := &LabConfig{
		Provider:        "azure",
		StackName:       "my-stack",
		AzureLocation:   "eastus",
		K8sClusterName:  "aks",
		NodePoolName:    "pool",
	}
	cmds := pe.getConfigCommands(cfg)
	if len(cmds) == 0 {
		t.Error("getConfigCommands() returned empty for Azure mode")
	}
}

func TestGetConfigCommands_WithDomain(t *testing.T) {
	pe := &PulumiExecutor{}
	cfg := &LabConfig{
		Domain:    "coder.example.com",
		AcmeEmail: "acme@example.com",
	}
	cmds := pe.getConfigCommands(cfg)
	found := false
	for _, c := range cmds {
		if c.key == "coder:domain" {
			found = true
		}
	}
	if !found {
		t.Error("getConfigCommands() with domain should contain coder:domain")
	}
}

func TestIsJobDirectoryReady_AllFiles(t *testing.T) {
	dir := t.TempDir()
	pe := &PulumiExecutor{workDir: dir}

	jobID := "test-job"
	jobDir := filepath.Join(dir, jobID)
	if err := os.MkdirAll(jobDir, 0755); err != nil {
		t.Fatal(err)
	}

	for _, f := range []string{"Pulumi.yaml", "main.go", "go.mod"} {
		if err := os.WriteFile(filepath.Join(jobDir, f), []byte("# test"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	if !pe.isJobDirectoryReady(jobID) {
		t.Error("isJobDirectoryReady() should return true when all required files exist")
	}
}

func TestCheckLocalKubeconfigFile_Missing(t *testing.T) {
	jm := NewJobManager("")
	id := jm.CreateJob(&LabConfig{StackName: "test"})
	pe := &PulumiExecutor{workDir: t.TempDir(), jobManager: jm}

	// Should not panic; no files → no kubeconfig set
	pe.checkLocalKubeconfigFile(id)

	job, _ := jm.GetJob(id)
	job.mu.RLock()
	kc := job.Kubeconfig
	job.mu.RUnlock()
	if kc != "" {
		t.Errorf("checkLocalKubeconfigFile() set kubeconfig from non-existent files: %q", kc)
	}
}

func TestCheckLocalKubeconfigFile_ValidKubeconfig(t *testing.T) {
	jm := NewJobManager("")
	id := jm.CreateJob(&LabConfig{StackName: "test"})
	dir := t.TempDir()
	pe := &PulumiExecutor{workDir: dir, jobManager: jm}

	jobDir := filepath.Join(dir, id)
	if err := os.MkdirAll(jobDir, 0755); err != nil {
		t.Fatal(err)
	}
	kubeconfig := "apiVersion: v1\nkind: Config"
	if err := os.WriteFile(filepath.Join(jobDir, "kubeconfig.yaml"), []byte(kubeconfig), 0644); err != nil {
		t.Fatal(err)
	}

	pe.checkLocalKubeconfigFile(id)

	job, _ := jm.GetJob(id)
	job.mu.RLock()
	kc := job.Kubeconfig
	job.mu.RUnlock()
	if kc != kubeconfig {
		t.Errorf("checkLocalKubeconfigFile() set kubeconfig = %q, want %q", kc, kubeconfig)
	}
}
