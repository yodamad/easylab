package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	internalPulumi "easylab/internal/pulumi"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/optdestroy"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/optpreview"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/optup"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// getEnvOrDefault returns the environment variable value or a default if not set
func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// PulumiExecutor handles Pulumi command execution
type PulumiExecutor struct {
	jobManager *JobManager
	workDir    string
}

// jobOutputWriter is a custom io.Writer that forwards output to jobManager
type jobOutputWriter struct {
	jobID      string
	jobManager *JobManager
	buffer     []byte
}

func (w *jobOutputWriter) Write(p []byte) (n int, err error) {
	w.buffer = append(w.buffer, p...)

	// Process complete lines
	for {
		idx := -1
		for i, b := range w.buffer {
			if b == '\n' {
				idx = i
				break
			}
		}

		if idx == -1 {
			break // No complete line yet
		}

		line := strings.TrimRight(string(w.buffer[:idx]), "\r\n")
		if line != "" {
			w.jobManager.AppendOutput(w.jobID, line)
		}

		w.buffer = w.buffer[idx+1:]
	}

	return len(p), nil
}

func (w *jobOutputWriter) Flush() {
	if len(w.buffer) > 0 {
		line := strings.TrimRight(string(w.buffer), "\r\n")
		if line != "" {
			w.jobManager.AppendOutput(w.jobID, line)
		}
		w.buffer = nil
	}
}

// getAppBaseDir returns the base application directory derived from environment variables
// Tries to derive from WORK_DIR, PULUMI_HOME, or other existing variables
// Falls back to /app only if no environment variables are available
func getAppBaseDir() string {
	// Try to derive from WORK_DIR first (e.g., /app/jobs -> /app)
	if workDir := os.Getenv("WORK_DIR"); workDir != "" {
		if base := filepath.Dir(workDir); base != "." && base != "/" {
			return base
		}
	}
	// Try to derive from PULUMI_HOME (e.g., /app/.pulumi -> /app)
	if pulumiHome := os.Getenv("PULUMI_HOME"); pulumiHome != "" {
		if base := filepath.Dir(pulumiHome); base != "." && base != "/" {
			return base
		}
	}
	// Try to derive from GOMODCACHE (e.g., /app/.go/pkg/mod -> /app)
	if gomodcache := os.Getenv("GOMODCACHE"); gomodcache != "" {
		// GOMODCACHE is /app/.go/pkg/mod, so we need to go up 3 levels
		if base := filepath.Dir(filepath.Dir(filepath.Dir(gomodcache))); base != "." && base != "/" {
			return base
		}
	}
	// Try to derive from GOPATH (e.g., /app/.go -> /app)
	if gopath := os.Getenv("GOPATH"); gopath != "" {
		if base := filepath.Dir(gopath); base != "." && base != "/" {
			return base
		}
	}
	// Try to derive from DATA_DIR (e.g., /app/data -> /app)
	if dataDir := os.Getenv("DATA_DIR"); dataDir != "" {
		if base := filepath.Dir(dataDir); base != "." && base != "/" {
			return base
		}
	}
	// Fallback to /app only if no environment variables are set
	return "/app"
}

// NewPulumiExecutor creates a new Pulumi executor
func NewPulumiExecutor(jobManager *JobManager, workDir string) *PulumiExecutor {
	log.Printf("Work directory: %s", workDir)
	return &PulumiExecutor{
		jobManager: jobManager,
		workDir:    workDir,
	}
}


// GetWorkDir returns the work directory path
func (pe *PulumiExecutor) GetWorkDir() string {
	return pe.workDir
}

// requiredPlugin describes a Pulumi resource plugin needed by the templates.
// Versions must match the entries in templates/go.mod.
type requiredPlugin struct {
	Name    string
	Version string
	Server  string // optional custom download URL (e.g. GitHub releases for third-party providers)
}

// requiredPlugins is the list of Pulumi resource plugins the templates depend on.
var requiredPlugins = []requiredPlugin{
	{Name: "command", Version: "1.1.3"},
	{Name: "kubernetes", Version: "4.24.1"},
	{Name: "ovh", Version: "2.11.0", Server: "github://api.github.com/ovh/pulumi-ovh"},
}

// CheckAndInstallPlugins verifies every required Pulumi resource plugin is healthy
// inside PULUMI_HOME/plugins. For each plugin it:
//  1. If the binary is missing, performs a full reinstall via the Pulumi CLI.
//  2. If the binary exists but PulumiPlugin.yaml is absent (common for third-party
//     providers like OVH), synthesizes the YAML without touching the working binary.
//  3. After a full reinstall, synthesizes PulumiPlugin.yaml if the installer did not
//     produce one.
func (pe *PulumiExecutor) CheckAndInstallPlugins() {
	pulumiHome := getEnvOrDefault("PULUMI_HOME", filepath.Join(getAppBaseDir(), ".pulumi"))
	pluginsDir := filepath.Join(pulumiHome, "plugins")

	log.Printf("[PLUGINS] Checking required Pulumi plugins in %s", pluginsDir)

	for _, p := range requiredPlugins {
		dirName := fmt.Sprintf("resource-%s-v%s", p.Name, p.Version)
		pluginDir := filepath.Join(pluginsDir, dirName)
		binaryPath := filepath.Join(pluginDir, fmt.Sprintf("pulumi-resource-%s", p.Name))
		yamlPath := filepath.Join(pluginDir, "PulumiPlugin.yaml")

		_, binaryErr := os.Stat(binaryPath)
		binaryMissing := os.IsNotExist(binaryErr)
		_, yamlErr := os.Stat(yamlPath)
		yamlMissing := os.IsNotExist(yamlErr)

		if binaryMissing {
			// Full reinstall: binary is absent so we need the CLI to download everything.
			log.Printf("[PLUGINS] Plugin %s v%s binary missing — installing...", p.Name, p.Version)
			if _, statErr := os.Stat(pluginDir); statErr == nil {
				if removeErr := os.RemoveAll(pluginDir); removeErr != nil {
					log.Printf("[PLUGINS] Warning: failed to remove incomplete plugin dir %s: %v", pluginDir, removeErr)
				}
			}
			if installErr := pe.installPlugin(p.Name, p.Version, p.Server, pulumiHome); installErr != nil {
				log.Printf("[PLUGINS] Warning: could not install plugin %s v%s: %v", p.Name, p.Version, installErr)
			} else {
				log.Printf("[PLUGINS] Plugin %s v%s installed successfully", p.Name, p.Version)
			}
			// Refresh YAML presence after reinstall.
			_, yamlErr = os.Stat(yamlPath)
			yamlMissing = os.IsNotExist(yamlErr)
		} else if yamlMissing {
			// Binary is fine but PulumiPlugin.yaml is missing (e.g. OVH provider).
			// Just synthesize the YAML — no need to delete the working binary.
			log.Printf("[PLUGINS] Plugin %s v%s PulumiPlugin.yaml missing — creating it...", p.Name, p.Version)
		} else {
			log.Printf("[PLUGINS] Plugin %s v%s OK", p.Name, p.Version)
		}

		// Synthesize PulumiPlugin.yaml when absent (after install or for an existing binary).
		if yamlMissing {
			if mkErr := os.MkdirAll(pluginDir, 0755); mkErr != nil {
				log.Printf("[PLUGINS] Warning: failed to create plugin dir %s: %v", pluginDir, mkErr)
				continue
			}
			content := fmt.Sprintf("name: %s\nversion: %s\nruntime: go\n", p.Name, p.Version)
			if writeErr := os.WriteFile(yamlPath, []byte(content), 0644); writeErr != nil {
				log.Printf("[PLUGINS] Warning: failed to write PulumiPlugin.yaml for %s v%s: %v", p.Name, p.Version, writeErr)
			} else {
				log.Printf("[PLUGINS] PulumiPlugin.yaml written for %s v%s", p.Name, p.Version)
			}
		}
	}

	log.Printf("[PLUGINS] Plugin check complete")
}

// installPlugin runs `pulumi plugin install resource <name> <version>` with the
// given PULUMI_HOME, with a 5-minute timeout. When server is non-empty it is
// passed as --server (required for third-party providers like OVH that are not
// in the default Pulumi registry).
func (pe *PulumiExecutor) installPlugin(name, version, server, pulumiHome string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	args := []string{"plugin", "install", "resource", name, version}
	if server != "" {
		args = append(args, "--server", server)
	}
	cmd := exec.CommandContext(ctx, "pulumi", args...)
	cmd.Env = append(os.Environ(), fmt.Sprintf("PULUMI_HOME=%s", pulumiHome))

	output, err := cmd.CombinedOutput()
	if len(output) > 0 {
		log.Printf("[PLUGINS] Install output for %s v%s: %s", name, version, strings.TrimSpace(string(output)))
	}
	if err != nil {
		return fmt.Errorf("pulumi plugin install resource %s %s: %w\nOutput: %s", name, version, err, string(output))
	}
	return nil
}

// getLocalBackendEnvVars returns environment variables for local file backend and Go cache
// When jobDir is provided, use job-local GOMODCACHE/GOCACHE to avoid embed "no matching files found"
// errors that occur when building from job directory with shared module cache
func getLocalBackendEnvVars(workDir ...string) map[string]string {
	envVars := map[string]string{
		"PULUMI_BACKEND_URL":                          "file://",
		"PULUMI_SKIP_UPDATE_CHECK":                    getEnvOrDefault("PULUMI_SKIP_UPDATE_CHECK", "true"),
		"PULUMI_DISABLE_AUTOMATIC_PLUGIN_ACQUISITION": getEnvOrDefault("PULUMI_DISABLE_AUTOMATIC_PLUGIN_ACQUISITION", "false"),
		"PULUMI_SKIP_CONFIRMATIONS":                   getEnvOrDefault("PULUMI_SKIP_CONFIRMATIONS", "true"),
	}

	// Set PULUMI_CONFIG_PASSPHRASE if not already set (required for file backend encryption)
	envVars["PULUMI_CONFIG_PASSPHRASE"] = getEnvOrDefault("PULUMI_CONFIG_PASSPHRASE", "passphrase")

	// Set PULUMI_HOME if not already set (required for plugin discovery)
	envVars["PULUMI_HOME"] = getEnvOrDefault("PULUMI_HOME", filepath.Join(getAppBaseDir(), ".pulumi"))

	// Set Go cache directories: use job-local cache when workDir provided to fix embed resolution
	// (must override Docker/env GOMODCACHE to avoid "no matching files found" in dependencies)
	baseDir := getAppBaseDir()
	if len(workDir) > 0 && workDir[0] != "" {
		jobDir := workDir[0]
		envVars["GOMODCACHE"] = filepath.Join(jobDir, ".gomodcache")
		envVars["GOCACHE"] = filepath.Join(jobDir, ".gocache")
		envVars["GOPATH"] = filepath.Join(jobDir, ".go")
	} else {
		envVars["GOMODCACHE"] = getEnvOrDefault("GOMODCACHE", filepath.Join(baseDir, ".go", "pkg", "mod"))
		envVars["GOCACHE"] = getEnvOrDefault("GOCACHE", filepath.Join(baseDir, ".go", "cache"))
		envVars["GOPATH"] = getEnvOrDefault("GOPATH", filepath.Join(baseDir, ".go"))
	}

	// Disable Go workspace mode to prevent interference with module resolution
	envVars["GOWORK"] = "off"

	// Disable VCS stamping - job dirs are template copies without .git, causing
	// "error obtaining VCS status: exit status 128" when go build runs
	envVars["GOFLAGS"] = getEnvOrDefault("GOFLAGS", "-buildvcs=false")

	return envVars
}

// getPulumiEnvVars returns environment variables for Pulumi operations including provider credentials
// workDir is optional - if provided, backend URL will be scoped to that directory
func getPulumiEnvVars(config *LabConfig, workDir ...string) map[string]string {
	envVars := getLocalBackendEnvVars(workDir...)

	// Add provider-specific credentials to environment variables
	// These are needed by the Pulumi program when it runs (e.g., during go build)
	if config != nil {
		provider := config.Provider
		if provider == "" {
			provider = "ovh" // Default to OVH for backward compatibility
		}

		// Add provider-specific environment variables
		switch provider {
		case "ovh":
			envVars["OVH_APPLICATION_KEY"] = config.OvhApplicationKey
			envVars["OVH_APPLICATION_SECRET"] = config.OvhApplicationSecret
			envVars["OVH_CONSUMER_KEY"] = config.OvhConsumerKey
			envVars["OVH_SERVICE_NAME"] = config.OvhServiceName
			// OVH_ENDPOINT is set via Pulumi config, not env var
			// Future providers can be added here:
			// case "aws":
			//     envVars["AWS_ACCESS_KEY_ID"] = config.AwsAccessKeyId
			//     envVars["AWS_SECRET_ACCESS_KEY"] = config.AwsSecretAccessKey
			//     ...
		}
	}

	return envVars
}


// getOrCreateStackInline gets or creates a Pulumi stack using inline program (pre-compiled)
// This avoids the Go compilation step on each job, providing significant performance improvements
func (pe *PulumiExecutor) getOrCreateStackInline(ctx context.Context, stackName, workDir, jobID string, config *LabConfig) (auto.Stack, error) {
	// Ensure workDir exists for state storage
	if err := os.MkdirAll(workDir, 0755); err != nil {
		return auto.Stack{}, fmt.Errorf("failed to ensure work directory exists: %w", err)
	}

	// Get environment variables including credentials
	envVars := getPulumiEnvVars(config, workDir)

	pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Using inline Pulumi program (pre-compiled) for stack '%s'...", stackName))

	// Create the inline program RunFunc
	program := internalPulumi.CreateLabProgram()

	// Try to select existing stack first
	stack, err := auto.SelectStackInlineSource(ctx, stackName, "easylab", program,
		auto.WorkDir(workDir),
		auto.EnvVars(envVars))
	if err == nil {
		pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Stack '%s' selected successfully (inline mode)", stackName))
		return stack, nil
	}

	// Stack doesn't exist, create it
	pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Stack '%s' does not exist, creating it (inline mode)...", stackName))
	stack, err = auto.UpsertStackInlineSource(ctx, stackName, "easylab", program,
		auto.WorkDir(workDir),
		auto.EnvVars(envVars))
	if err != nil {
		return auto.Stack{}, fmt.Errorf("failed to create inline stack: %w", err)
	}

	pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Stack '%s' created successfully (inline mode)", stackName))
	return stack, nil
}

// outputValueToString converts a Pulumi output value to string
func (pe *PulumiExecutor) outputValueToString(val interface{}) string {
	if val == nil {
		return ""
	}

	switch v := val.(type) {
	case string:
		return v
	case pulumi.StringOutput:
		// This shouldn't happen in Automation API outputs, but handle it
		return ""
	case map[string]interface{}:
		// Handle Pulumi ConfigValue format: {"Value": "...", "Secret": false}
		if value, ok := v["Value"].(string); ok {
			return value
		}
		// Map without Value key - marshal to JSON
		jsonBytes, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return string(jsonBytes)
	default:
		// For other types, try to marshal to JSON and extract string
		jsonBytes, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		// Try to unmarshal as string (handles JSON-encoded strings)
		var str string
		if err := json.Unmarshal(jsonBytes, &str); err == nil {
			return str
		}
		// Try to unmarshal as ConfigValue structure
		var configVal struct {
			Value  interface{} `json:"Value"`
			Secret bool        `json:"Secret"`
		}
		if err := json.Unmarshal(jsonBytes, &configVal); err == nil {
			if strVal, ok := configVal.Value.(string); ok {
				return strVal
			}
		}
		return string(jsonBytes)
	}
}

// setStackConfig sets all configuration values for a stack
func (pe *PulumiExecutor) setStackConfig(ctx context.Context, stack auto.Stack, config *LabConfig) error {
	configCommands := pe.getConfigCommands(config)

	for _, cmd := range configCommands {
		err := stack.SetConfig(ctx, cmd.key, auto.ConfigValue{
			Value:  cmd.value,
			Secret: cmd.secret,
		})
		if err != nil {
			return fmt.Errorf("failed to set config %s: %w", cmd.key, err)
		}
	}

	// Set provider-specific config only when not using an existing cluster
	if !config.UseExistingCluster {
		provider := config.Provider
		if provider == "" {
			provider = "ovh" // Default to OVH for backward compatibility
		}

		switch provider {
		case "ovh":
			endpoint := config.OvhEndpoint
			if endpoint == "" {
				endpoint = "ovh-eu" // Default per docs/ovhcloud.md
			}
			err := stack.SetConfig(ctx, "ovh:endpoint", auto.ConfigValue{
				Value:  endpoint,
				Secret: false,
			})
			if err != nil {
				return fmt.Errorf("failed to set config ovh:endpoint: %w", err)
			}
			// Future providers can be added here:
			// case "aws":
			//     err := stack.SetConfig(ctx, "aws:region", auto.ConfigValue{...})
			//     ...
		}
	}

	return nil
}

// JobPreparation holds the prepared resources for a Pulumi operation
type JobPreparation struct {
	Stack   auto.Stack
	Writer  *jobOutputWriter
	Context context.Context
	Cleanup func()
}

// prepareJob handles all common setup logic for Pulumi operations
func (pe *PulumiExecutor) prepareJob(jobID string, allowMissingDir bool) (*JobPreparation, error) {
	// Validate job exists
	job, exists := pe.jobManager.GetJob(jobID)
	if !exists {
		return nil, fmt.Errorf("job %s not found", jobID)
	}
	config := job.Config

	// Create context
	ctx := context.Background()

	// Update status to running
	pe.jobManager.UpdateJobStatus(jobID, JobStatusRunning)

	// Create job directory
	jobDir := filepath.Join(pe.workDir, jobID)

	// Handle directory creation based on allowMissingDir flag
	if allowMissingDir {
		// For destroy operations - check if directory exists first
		if _, err := os.Stat(jobDir); os.IsNotExist(err) {
			pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Job directory not found: %s. Creating it...", jobDir))
			if err := os.MkdirAll(jobDir, 0755); err != nil {
				pe.jobManager.SetError(jobID, fmt.Errorf("failed to create job directory: %w", err))
				return nil, err
			}
		}
	} else {
		// For create/preview operations - always create directory
		pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Creating job directory: %s", jobDir))
		if err := os.MkdirAll(jobDir, 0755); err != nil {
			pe.jobManager.SetError(jobID, fmt.Errorf("failed to create job directory: %w", err))
			return nil, err
		}
	}

	// BYOK mode: ensure external-kubeconfig.yaml exists (covers retry fallback when directory was regenerated)
	if config != nil && config.UseExistingCluster && config.ExternalKubeconfig != "" {
		kubeconfigPath := filepath.Join(jobDir, "external-kubeconfig.yaml")
		if err := os.WriteFile(kubeconfigPath, []byte(config.ExternalKubeconfig), 0600); err != nil {
			pe.jobManager.SetError(jobID, fmt.Errorf("failed to write external-kubeconfig.yaml: %w", err))
			return nil, err
		}
		pe.jobManager.AppendOutput(jobID, "External kubeconfig written for existing cluster mode")
	}

	// Env vars are passed per-workspace via auto.EnvVars inside getOrCreateStackInline.
	cleanup := func() {}

	// Get or create stack using inline program
	pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Initializing Pulumi stack '%s'...", config.StackName))
	var stack auto.Stack
	var err error
	stack, err = pe.getOrCreateStackInline(ctx, config.StackName, jobDir, jobID, config)
	if err != nil {
		cleanup() // Clean up env vars on error
		pe.jobManager.SetError(jobID, fmt.Errorf("failed to get or create stack: %w", err))
		return nil, err
	}

	// Set all config values
	pe.jobManager.AppendOutput(jobID, "Setting Pulumi configuration...")
	if err := pe.setStackConfig(ctx, stack, config); err != nil {
		pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Warning: failed to set some config: %v", err))
		// Continue anyway - some configs might already be set
	}

	// Create output writer for streaming
	outputWriter := &jobOutputWriter{
		jobID:      jobID,
		jobManager: pe.jobManager,
	}

	return &JobPreparation{
		Stack:   stack,
		Writer:  outputWriter,
		Context: ctx,
		Cleanup: cleanup,
	}, nil
}

// isJobDirectoryReady checks if the job directory exists and has all required files for retry
// Returns true if directory exists and contains Pulumi.yaml, main.go, and go.mod
func (pe *PulumiExecutor) isJobDirectoryReady(jobID string) bool {
	jobDir := filepath.Join(pe.workDir, jobID)

	// Check if directory exists
	if _, err := os.Stat(jobDir); os.IsNotExist(err) {
		return false
	}

	// Check for required files
	requiredFiles := []string{
		"Pulumi.yaml",
		"main.go",
		"go.mod",
	}

	for _, file := range requiredFiles {
		filePath := filepath.Join(jobDir, file)
		if _, err := os.Stat(filePath); os.IsNotExist(err) {
			return false
		}
	}

	return true
}

// prepareJobForRetry handles setup for retrying a failed job by reusing existing files
// This skips file generation and template processing, but still updates stack config
func (pe *PulumiExecutor) prepareJobForRetry(jobID string) (*JobPreparation, error) {
	// Validate job exists
	job, exists := pe.jobManager.GetJob(jobID)
	if !exists {
		return nil, fmt.Errorf("job %s not found", jobID)
	}
	config := job.Config

	// Check if job directory is ready
	if !pe.isJobDirectoryReady(jobID) {
		pe.jobManager.AppendOutput(jobID, "Job directory not ready, falling back to full regeneration...")
		// Fall back to normal preparation
		return pe.prepareJob(jobID, false)
	}

	// Create context
	ctx := context.Background()

	// Update status to running
	pe.jobManager.UpdateJobStatus(jobID, JobStatusRunning)

	// Get job directory
	jobDir := filepath.Join(pe.workDir, jobID)

	pe.jobManager.AppendOutput(jobID, "Reusing existing job directory and files...")

	// Env vars are passed per-workspace via auto.EnvVars inside getOrCreateStackInline.
	cleanup := func() {}

	// Get existing stack (should exist from previous run)
	pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Selecting existing Pulumi stack '%s'...", config.StackName))
	var stack auto.Stack
	var err error
	stack, err = pe.getOrCreateStackInline(ctx, config.StackName, jobDir, jobID, config)
	if err != nil {
		cleanup() // Clean up env vars on error
		pe.jobManager.SetError(jobID, fmt.Errorf("failed to get or create stack: %w", err))
		return nil, err
	}

	// Update stack configuration (credentials may have changed)
	pe.jobManager.AppendOutput(jobID, "Updating Pulumi configuration...")
	if err := pe.setStackConfig(ctx, stack, config); err != nil {
		pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Warning: failed to set some config: %v", err))
		// Continue anyway - some configs might already be set
	}

	// Create output writer for streaming
	outputWriter := &jobOutputWriter{
		jobID:      jobID,
		jobManager: pe.jobManager,
	}

	return &JobPreparation{
		Stack:   stack,
		Writer:  outputWriter,
		Context: ctx,
		Cleanup: cleanup,
	}, nil
}

// prepareDestroyJob handles setup for destroy operations with special handling for missing stacks
func (pe *PulumiExecutor) prepareDestroyJob(jobID string) (*JobPreparation, error) {
	// Validate job exists
	job, exists := pe.jobManager.GetJob(jobID)
	if !exists {
		return nil, fmt.Errorf("job %s not found", jobID)
	}

	job.mu.RLock()
	config := job.Config
	stackName := ""
	if config != nil {
		stackName = config.StackName
	}
	job.mu.RUnlock()

	if stackName == "" {
		return nil, fmt.Errorf("job %s has no stack name", jobID)
	}

	// Create context
	ctx := context.Background()

	// Update status to running
	pe.jobManager.UpdateJobStatus(jobID, JobStatusRunning)
	pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Destroy started at %s", time.Now().Format(time.RFC3339)))

	// Ensure job directory exists (needed for stack state storage)
	jobDir := filepath.Join(pe.workDir, jobID)
	if err := os.MkdirAll(jobDir, 0755); err != nil {
		pe.jobManager.SetError(jobID, fmt.Errorf("failed to create job directory: %w", err))
		return nil, err
	}

	cleanup := func() {}

	// Get environment variables including OVH credentials for Pulumi Automation API (scoped to job directory)
	pulumiEnvVars := getPulumiEnvVars(config, jobDir)

	// Check if .pulumi directory exists (required for file backend stack state)
	pulumiDir := filepath.Join(jobDir, ".pulumi")
	if _, err := os.Stat(pulumiDir); os.IsNotExist(err) {
		pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Warning: .pulumi directory not found at %s", pulumiDir))
		pe.jobManager.AppendOutput(jobID, "In file backend mode, stack state is stored in .pulumi directory.")
		pe.jobManager.AppendOutput(jobID, "If the job directory was cleaned up after deployment, stack state may be lost.")
		pe.jobManager.AppendOutput(jobID, "Attempting to select stack anyway (stack may have been manually deleted)...")
	} else {
		pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Found .pulumi directory at %s", pulumiDir))
	}

	// Try to select the stack using inline program
	pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Selecting Pulumi stack '%s'...", stackName))
	program := internalPulumi.CreateLabProgram()
	stack, err := auto.SelectStackInlineSource(ctx, stackName, "easylab", program,
		auto.WorkDir(jobDir),
		auto.EnvVars(pulumiEnvVars))
	if err != nil {
		// Selection failed - try to verify if stack actually exists by listing stacks
		pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Initial stack selection failed: %v. Verifying stack existence...", err))

		// Try to list stacks to verify if the stack exists
		workspace, wsErr := auto.NewLocalWorkspace(ctx,
			auto.WorkDir(jobDir),
			auto.EnvVars(pulumiEnvVars))
		if wsErr != nil {
			pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Warning: failed to create workspace for verification: %v", wsErr))
			pe.jobManager.AppendOutput(jobID, "Cannot verify stack existence. Assuming stack does not exist.")
			pe.jobManager.UpdateJobStatus(jobID, JobStatusDestroyed)
			pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Destroy completed at %s (stack did not exist or state is missing)", time.Now().Format(time.RFC3339)))
			cleanup()
			return nil, nil
		}

		stacks, listErr := workspace.ListStacks(ctx)
		if listErr != nil {
			pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Warning: failed to list stacks: %v", listErr))
			pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Diagnostic: .pulumi directory exists: %v", func() bool {
				_, err := os.Stat(pulumiDir)
				return err == nil
			}()))
			pe.jobManager.AppendOutput(jobID, "Cannot list stacks. Assuming stack does not exist.")
			pe.jobManager.UpdateJobStatus(jobID, JobStatusDestroyed)
			pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Destroy completed at %s (stack did not exist or state is missing)", time.Now().Format(time.RFC3339)))
			cleanup()
			return nil, nil
		}

		// Log all available stacks for diagnostic purposes
		if len(stacks) > 0 {
			pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Found %d stack(s) in workspace:", len(stacks)))
			for _, s := range stacks {
				pe.jobManager.AppendOutput(jobID, fmt.Sprintf("  - %s", s.Name))
			}
		} else {
			pe.jobManager.AppendOutput(jobID, "No stacks found in workspace.")
		}

		// Verify stack exists in the list
		stackExists := false
		for _, s := range stacks {
			if s.Name == stackName {
				stackExists = true
				break
			}
		}

		if !stackExists {
			pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Stack '%s' not found in workspace. It may have been already destroyed.", stackName))
			pe.jobManager.UpdateJobStatus(jobID, JobStatusDestroyed)
			pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Destroy completed at %s (stack did not exist)", time.Now().Format(time.RFC3339)))
			cleanup()
			return nil, nil
		}

		// Stack exists in list but selection failed - retry selection
		pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Stack '%s' exists in workspace but selection failed. Retrying selection...", stackName))
		stack, err = auto.SelectStackInlineSource(ctx, stackName, "easylab", program,
			auto.WorkDir(jobDir),
			auto.EnvVars(pulumiEnvVars))
		if err != nil {
			pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Error: retry also failed to select stack '%s': %v", stackName, err))
			pe.jobManager.AppendOutput(jobID, "This may occur if:")
			pe.jobManager.AppendOutput(jobID, "  1. The stack state is corrupted")
			pe.jobManager.AppendOutput(jobID, "  2. The stack name doesn't match exactly")
			pe.jobManager.AppendOutput(jobID, "  3. There are permission issues accessing the stack state")

			if _, statErr := os.Stat(pulumiDir); os.IsNotExist(statErr) {
				pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Diagnostic: Stack state directory (.pulumi) is MISSING at %s", pulumiDir))
				pe.jobManager.AppendOutput(jobID, "In file backend mode, stack state must be preserved after deployment for destroy operations.")
			} else {
				pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Diagnostic: Stack state directory (.pulumi) EXISTS at %s", pulumiDir))
				pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Diagnostic: Stack '%s' exists in workspace list but selection failed", stackName))
				pe.jobManager.AppendOutput(jobID, "The stack state may be corrupted.")
			}

			pe.jobManager.SetError(jobID, fmt.Errorf("failed to select stack '%s' for destruction: %w", stackName, err))
			cleanup()
			return nil, fmt.Errorf("failed to select stack '%s' after retry: %w", stackName, err)
		}
		pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Stack '%s' selected successfully on retry", stackName))
	}

	// Create output writer for streaming
	outputWriter := &jobOutputWriter{
		jobID:      jobID,
		jobManager: pe.jobManager,
	}

	return &JobPreparation{
		Stack:   stack,
		Writer:  outputWriter,
		Context: ctx,
		Cleanup: cleanup,
	}, nil
}

// extractKubeconfigFromOutputs extracts kubeconfig from outputs if cluster exists, or from config/file for BYOK mode
func (pe *PulumiExecutor) extractKubeconfigFromOutputs(jobID string, outputs auto.OutputMap) {
	job, exists := pe.jobManager.GetJob(jobID)
	if !exists || job.Config == nil {
		pe.checkLocalKubeconfigFile(jobID)
		return
	}
	config := job.Config

	// BYOK mode: use the user-provided kubeconfig from config
	if config.UseExistingCluster && config.ExternalKubeconfig != "" {
		pe.jobManager.AppendOutput(jobID, "Using provided kubeconfig (existing cluster mode)")
		if err := pe.jobManager.SetKubeconfig(jobID, config.ExternalKubeconfig); err != nil {
			pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Warning: failed to set kubeconfig: %v", err))
			return
		}
		pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Kubeconfig set successfully (length: %d chars)", len(config.ExternalKubeconfig)))
		return
	}

	// Verify that Kubernetes cluster was created before extracting kubeconfig
	if _, ok := outputs["kubeClusterId"]; ok {
		pe.jobManager.AppendOutput(jobID, "Kubernetes cluster found, extracting kubeconfig...")
		// Get kubeconfig from outputs
		if kubeconfigVal, ok := outputs["kubeconfig"]; ok {
			kubeconfig := pe.outputValueToString(kubeconfigVal)
			if kubeconfig != "" {
				// Validate that kubeconfig looks like valid YAML
				if !strings.Contains(kubeconfig, "apiVersion") && !strings.Contains(kubeconfig, "kind:") {
					pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Warning: kubeconfig may be invalid (length: %d chars)", len(kubeconfig)))
				}
				pe.jobManager.SetKubeconfig(jobID, kubeconfig)
				pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Kubeconfig extracted successfully (length: %d chars)", len(kubeconfig)))
			} else {
				pe.jobManager.AppendOutput(jobID, "Warning: kubeconfig output exists but is empty")
			}
		} else {
			pe.jobManager.AppendOutput(jobID, "Warning: Kubernetes cluster created but kubeconfig output not found, checking local file...")
			// Fallback to local file if output is missing
			pe.checkLocalKubeconfigFile(jobID)
		}
	} else {
		pe.jobManager.AppendOutput(jobID, "Kubernetes cluster not found in outputs, checking for local kubeconfig file...")
		// Check for kubeconfig.yaml or external-kubeconfig.yaml
		pe.checkLocalKubeconfigFile(jobID)
	}
}

// checkLocalKubeconfigFile checks for kubeconfig in the job directory (external-kubeconfig.yaml or kubeconfig.yaml)
func (pe *PulumiExecutor) checkLocalKubeconfigFile(jobID string) {
	jobDir := filepath.Join(pe.workDir, jobID)
	// Try external-kubeconfig.yaml first (BYOK), then kubeconfig.yaml (OVH-created)
	candidates := []string{"external-kubeconfig.yaml", "kubeconfig.yaml"}

	for _, candidate := range candidates {
		kubeconfigPath := filepath.Join(jobDir, candidate)
		if _, err := os.Stat(kubeconfigPath); os.IsNotExist(err) {
			continue
		}

		kubeconfigBytes, err := os.ReadFile(kubeconfigPath)
		if err != nil {
			pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Failed to read %s: %v", candidate, err))
			continue
		}

		kubeconfig := strings.TrimSpace(string(kubeconfigBytes))
		if kubeconfig == "" {
			pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Local %s is empty", candidate))
			continue
		}

		// Validate that kubeconfig looks like valid YAML
		if !strings.Contains(kubeconfig, "apiVersion") && !strings.Contains(kubeconfig, "kind:") {
			pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Warning: local kubeconfig may be invalid (length: %d chars)", len(kubeconfig)))
		}

		pe.jobManager.SetKubeconfig(jobID, kubeconfig)
		pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Kubeconfig loaded from %s (length: %d chars)", candidate, len(kubeconfig)))
		return
	}

	pe.jobManager.AppendOutput(jobID, "Local kubeconfig file not found (checked external-kubeconfig.yaml, kubeconfig.yaml)")
}

// Execute runs pulumi up for a given job
func (pe *PulumiExecutor) Execute(jobID string) error {
	// Prepare job with common setup
	prep, err := pe.prepareJob(jobID, false) // false = always create directory
	if err != nil {
		return err
	}
	// Cleanup env vars and flush writer after all Pulumi operations complete
	defer prep.Cleanup()
	defer prep.Writer.Flush()

	// Add execution-specific output
	pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Job started at %s", time.Now().Format(time.RFC3339)))

	// Run pulumi up with streaming output
	pe.jobManager.AppendOutput(jobID, "Running pulumi up...")
	upResult, err := prep.Stack.Up(prep.Context, optup.ProgressStreams(prep.Writer))
	if err != nil {
		pe.jobManager.SetError(jobID, fmt.Errorf("pulumi up failed: %w", err))

		// Even if pulumi up failed, try to extract kubeconfig if cluster was created
		pe.jobManager.AppendOutput(jobID, "Checking for kubeconfig despite deployment failure...")
		if len(upResult.Outputs) > 0 {
			pe.extractKubeconfigFromOutputs(jobID, upResult.Outputs)
		} else {
			// Try to refresh stack and get outputs directly
			pe.jobManager.AppendOutput(jobID, "Attempting to refresh stack to get outputs...")
			if _, refreshErr := prep.Stack.Refresh(prep.Context); refreshErr == nil {
				if outputs, outErr := prep.Stack.Outputs(prep.Context); outErr == nil {
					pe.extractKubeconfigFromOutputs(jobID, outputs)
				} else {
					pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Could not retrieve stack outputs: %v", outErr))
				}
			} else {
				pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Could not refresh stack: %v", refreshErr))
			}
		}

		// Persist failed job to disk
		if saveErr := pe.jobManager.SaveJob(jobID); saveErr != nil {
			log.Printf("Warning: failed to persist failed job %s: %v", jobID, saveErr)
			// Don't fail the job if persistence fails
		}
		return err
	}

	// Extract outputs from the result
	pe.jobManager.AppendOutput(jobID, "Extracting stack outputs...")

	// Extract kubeconfig if cluster was created
	pe.extractKubeconfigFromOutputs(jobID, upResult.Outputs)

	// Extract Coder configuration from stack outputs
	pe.jobManager.AppendOutput(jobID, "Extracting Coder configuration...")
	if coderURLVal, ok := upResult.Outputs["coderServerURL"]; ok {
		job, _ := pe.jobManager.GetJob(jobID) // We know job exists from prepareJob
		config := job.Config

		coderURL := pe.outputValueToString(coderURLVal)
		if coderURL != "" {
			coderSessionToken := ""
			coderOrganizationID := ""

			if tokenVal, ok := upResult.Outputs["coderSessionToken"]; ok {
				coderSessionToken = pe.outputValueToString(tokenVal)
			}
			if orgIDVal, ok := upResult.Outputs["coderOrganizationID"]; ok {
				coderOrganizationID = pe.outputValueToString(orgIDVal)
			}

			// Store Coder config in job
			if err := pe.jobManager.SetCoderConfig(jobID, coderURL, config.CoderAdminEmail, config.CoderAdminPassword, coderSessionToken, coderOrganizationID); err != nil {
				pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Warning: failed to store Coder config: %v", err))
			} else {
				pe.jobManager.AppendOutput(jobID, "Coder configuration extracted and stored successfully")
			}
		}
	}

	// Success
	pe.jobManager.UpdateJobStatus(jobID, JobStatusCompleted)
	pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Deployment completed successfully at %s", time.Now().Format(time.RFC3339)))

	// Clean up source files but preserve .pulumi directory for file backend
	// This ensures stack state is available for future destroy operations
	if err := pe.cleanupJobDirectory(jobID, true); err != nil {
		log.Printf("Warning: failed to cleanup job directory for %s: %v", jobID, err)
		// Don't fail the job if cleanup fails
	}

	// Persist completed job to disk
	if err := pe.jobManager.SaveJob(jobID); err != nil {
		log.Printf("Warning: failed to persist job %s: %v", jobID, err)
		// Don't fail the job if persistence fails
	}

	return nil
}

// ExecuteRetry runs pulumi up for a retried job, reusing existing configuration and files
func (pe *PulumiExecutor) ExecuteRetry(jobID string) error {
	// Prepare job with retry-optimized setup
	prep, err := pe.prepareJobForRetry(jobID)
	if err != nil {
		return err
	}
	// Cleanup env vars and flush writer after all Pulumi operations complete
	defer prep.Cleanup()
	defer prep.Writer.Flush()

	// Add execution-specific output
	pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Retry started at %s", time.Now().Format(time.RFC3339)))

	// Run pulumi up with streaming output
	pe.jobManager.AppendOutput(jobID, "Running pulumi up...")
	upResult, err := prep.Stack.Up(prep.Context, optup.ProgressStreams(prep.Writer))
	if err != nil {
		pe.jobManager.SetError(jobID, fmt.Errorf("pulumi up failed: %w", err))

		// Even if pulumi up failed, try to extract kubeconfig if cluster was created
		pe.jobManager.AppendOutput(jobID, "Checking for kubeconfig despite deployment failure...")
		if len(upResult.Outputs) > 0 {
			pe.extractKubeconfigFromOutputs(jobID, upResult.Outputs)
		} else {
			// Try to refresh stack and get outputs directly
			pe.jobManager.AppendOutput(jobID, "Attempting to refresh stack to get outputs...")
			if _, refreshErr := prep.Stack.Refresh(prep.Context); refreshErr == nil {
				if outputs, outErr := prep.Stack.Outputs(prep.Context); outErr == nil {
					pe.extractKubeconfigFromOutputs(jobID, outputs)
				} else {
					pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Could not retrieve stack outputs: %v", outErr))
				}
			} else {
				pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Could not refresh stack: %v", refreshErr))
			}
		}

		// Persist failed job to disk
		if saveErr := pe.jobManager.SaveJob(jobID); saveErr != nil {
			log.Printf("Warning: failed to persist failed job %s: %v", jobID, saveErr)
			// Don't fail the job if persistence fails
		}
		return err
	}

	// Extract outputs from the result
	pe.jobManager.AppendOutput(jobID, "Extracting stack outputs...")

	// Extract kubeconfig if cluster was created
	pe.extractKubeconfigFromOutputs(jobID, upResult.Outputs)

	// Extract Coder configuration from stack outputs
	pe.jobManager.AppendOutput(jobID, "Extracting Coder configuration...")
	if coderURLVal, ok := upResult.Outputs["coderServerURL"]; ok {
		job, _ := pe.jobManager.GetJob(jobID) // We know job exists from prepareJobForRetry
		config := job.Config

		coderURL := pe.outputValueToString(coderURLVal)
		if coderURL != "" {
			coderSessionToken := ""
			coderOrganizationID := ""

			if tokenVal, ok := upResult.Outputs["coderSessionToken"]; ok {
				coderSessionToken = pe.outputValueToString(tokenVal)
			}
			if orgIDVal, ok := upResult.Outputs["coderOrganizationID"]; ok {
				coderOrganizationID = pe.outputValueToString(orgIDVal)
			}

			// Store Coder config in job
			if err := pe.jobManager.SetCoderConfig(jobID, coderURL, config.CoderAdminEmail, config.CoderAdminPassword, coderSessionToken, coderOrganizationID); err != nil {
				pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Warning: failed to store Coder config: %v", err))
			} else {
				pe.jobManager.AppendOutput(jobID, "Coder configuration extracted and stored successfully")
			}
		}
	}

	// Success
	pe.jobManager.UpdateJobStatus(jobID, JobStatusCompleted)
	pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Deployment completed successfully at %s", time.Now().Format(time.RFC3339)))

	// Clean up source files but preserve .pulumi directory for file backend
	// This ensures stack state is available for future destroy operations
	if err := pe.cleanupJobDirectory(jobID, true); err != nil {
		log.Printf("Warning: failed to cleanup job directory for %s: %v", jobID, err)
		// Don't fail the job if cleanup fails
	}

	// Persist completed job to disk
	if err := pe.jobManager.SaveJob(jobID); err != nil {
		log.Printf("Warning: failed to persist job %s: %v", jobID, err)
		// Don't fail the job if persistence fails
	}

	return nil
}

// Preview runs pulumi preview for a given job (dry run)
func (pe *PulumiExecutor) Preview(jobID string) error {
	// Prepare job with common setup
	prep, err := pe.prepareJob(jobID, false) // false = always create directory
	if err != nil {
		return err
	}
	// Cleanup env vars and flush writer after all Pulumi operations complete
	defer prep.Cleanup()
	defer prep.Writer.Flush()

	// Add preview-specific output
	pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Dry run started at %s", time.Now().Format(time.RFC3339)))

	// Run pulumi preview with streaming output
	pe.jobManager.AppendOutput(jobID, "Running pulumi preview (dry run)...")
	_, err = prep.Stack.Preview(prep.Context, optpreview.ProgressStreams(prep.Writer))
	if err != nil {
		pe.jobManager.SetError(jobID, fmt.Errorf("pulumi preview failed: %w", err))
		// Persist failed job to disk
		if saveErr := pe.jobManager.SaveJob(jobID); saveErr != nil {
			log.Printf("Warning: failed to persist failed job %s: %v", jobID, saveErr)
			// Don't fail the job if persistence fails
		}
		return err
	}

	// Success - mark as dry-run-completed
	pe.jobManager.UpdateJobStatus(jobID, JobStatusDryRunCompleted)
	pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Dry run completed successfully at %s", time.Now().Format(time.RFC3339)))
	pe.jobManager.AppendOutput(jobID, "✅ Dry run passed! You can now launch the real deployment.")

	return nil
}

// Destroy runs pulumi destroy and removes the stack for a given job
func (pe *PulumiExecutor) Destroy(jobID string) error {
	// Prepare job with destroy-specific setup
	prep, err := pe.prepareDestroyJob(jobID)
	if err != nil {
		return err
	}

	// Special case: stack didn't exist, already handled in prepareDestroyJob
	if prep == nil {
		return nil
	}

	// Cleanup env vars and flush writer after all Pulumi operations complete
	defer prep.Cleanup()
	defer prep.Writer.Flush()

	// Get stack name and job directory for output
	job, _ := pe.jobManager.GetJob(jobID)
	job.mu.RLock()
	stackName := job.Config.StackName
	job.mu.RUnlock()
	jobDir := filepath.Join(pe.workDir, jobID)

	// Run pulumi destroy with streaming output
	pe.jobManager.AppendOutput(jobID, "Running pulumi destroy...")
	destroyResult, err := prep.Stack.Destroy(prep.Context, optdestroy.ProgressStreams(prep.Writer))
	if err != nil {
		// Destroy failed - don't continue with stack removal or mark as destroyed
		pe.jobManager.AppendOutput(jobID, fmt.Sprintf("ERROR: pulumi destroy failed: %v", err))
		pe.jobManager.AppendOutput(jobID, "Destroy operation failed. Resources may still exist in the cloud.")
		pe.jobManager.AppendOutput(jobID, "Stack state is preserved. You can retry the destroy operation.")
		pe.jobManager.SetError(jobID, fmt.Errorf("pulumi destroy failed: %w", err))

		// Persist failed job to disk so it can be retried
		if saveErr := pe.jobManager.SaveJob(jobID); saveErr != nil {
			log.Printf("Warning: failed to persist failed destroy job %s: %v", jobID, saveErr)
		}

		return fmt.Errorf("destroy failed: %w", err)
	}

	// Destroy succeeded - verify stack is empty before proceeding
	pe.jobManager.AppendOutput(jobID, "Destroy completed successfully. Verifying stack state...")
	if destroyResult.Summary.ResourceChanges != nil {
		pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Destroy summary: %+v", destroyResult.Summary))
	}

	// Get environment variables including OVH credentials (scoped to job directory)
	envVars := getPulumiEnvVars(job.Config, jobDir)

	// Remove the stack from the workspace only after successful destroy
	pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Removing stack '%s' from workspace...", stackName))
	workspace, wsErr := auto.NewLocalWorkspace(prep.Context,
		auto.WorkDir(jobDir),
		auto.EnvVars(envVars))
	if wsErr != nil {
		pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Warning: failed to create workspace for stack removal: %v", wsErr))
		pe.jobManager.AppendOutput(jobID, "Stack resources were destroyed, but stack metadata removal failed.")
		// Don't fail the job - resources are destroyed, just metadata cleanup failed
	} else {
		if removeErr := workspace.RemoveStack(prep.Context, stackName); removeErr != nil {
			pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Warning: failed to remove stack from workspace: %v", removeErr))
			pe.jobManager.AppendOutput(jobID, "Stack resources were destroyed, but stack metadata removal failed.")
			// Don't fail the job - resources are destroyed, just metadata cleanup failed
		} else {
			pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Stack '%s' removed from workspace successfully", stackName))
		}
	}

	pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Stack '%s' resources destroyed successfully.", stackName))

	// Success - mark as destroyed only after successful destroy
	pe.jobManager.UpdateJobStatus(jobID, JobStatusDestroyed)
	pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Destroy completed at %s", time.Now().Format(time.RFC3339)))
	pe.jobManager.AppendOutput(jobID, "✅ Stack destroyed successfully. You can recreate it using the same configuration.")

	// Persist destroyed job to disk
	if err := pe.jobManager.SaveJob(jobID); err != nil {
		log.Printf("Warning: failed to persist destroyed job %s: %v", jobID, err)
	}

	// Clean up the job directory completely since all resources have been destroyed
	// Stack state is no longer needed after destroy
	if err := pe.cleanupJobDirectory(jobID, false); err != nil {
		log.Printf("Warning: failed to cleanup job directory for %s: %v", jobID, err)
		// Don't fail the job if cleanup fails
	}

	return nil
}

// cleanupJobDirectory removes the job's working directory after successful completion
// If preservePulumiState is true, it preserves the .pulumi directory (needed for file backend destroy operations)
func (pe *PulumiExecutor) cleanupJobDirectory(jobID string, preservePulumiState bool) error {
	jobDir := filepath.Join(pe.workDir, jobID)

	// Check if directory exists
	if _, err := os.Stat(jobDir); os.IsNotExist(err) {
		// Directory already doesn't exist, nothing to clean up
		return nil
	}

	if preservePulumiState {
		// For file backend, preserve .pulumi directory but clean up source files
		// This allows destroy operations to find the stack state later
		// Remove source files and other temporary files, but preserve .pulumi
		entries, err := os.ReadDir(jobDir)
		if err != nil {
			return fmt.Errorf("failed to read job directory: %w", err)
		}

		removedCount := 0
		for _, entry := range entries {
			// Skip .pulumi directory
			if entry.Name() == ".pulumi" {
				continue
			}

			entryPath := filepath.Join(jobDir, entry.Name())
			if err := os.RemoveAll(entryPath); err != nil {
				pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Warning: failed to remove %s: %v", entry.Name(), err))
				continue
			}
			removedCount++
		}

		pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Cleaned up job directory (preserved .pulumi for stack state): %s", jobDir))
		if removedCount > 0 {
			pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Removed %d items, preserved .pulumi directory", removedCount))
		}
	} else {
		// Remove the entire job directory (after destroy, stack state no longer needed)
		if err := os.RemoveAll(jobDir); err != nil {
			return fmt.Errorf("failed to remove job directory %s: %w", jobDir, err)
		}

		pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Cleaned up job directory: %s", jobDir))
	}
	return nil
}

type configCommand struct {
	key    string
	value  string
	secret bool
}


func (pe *PulumiExecutor) getConfigCommands(config *LabConfig) []configCommand {
	var commands []configCommand

	coderNamespace := config.CoderNamespace
	if coderNamespace == "" {
		coderNamespace = "coder"
	}

	if config.UseExistingCluster {
		// BYOK mode: only Coder config + kubeconfig path
		commands = []configCommand{
			{"k8s:useExistingCluster", "true", false},
			{"coder:namespace", coderNamespace, false},
			{"coder:adminEmail", config.CoderAdminEmail, false},
			{"coder:adminPassword", config.CoderAdminPassword, true},
			{"coder:version", config.CoderVersion, false},
			{"coder:dbUser", config.CoderDbUser, false},
			{"coder:dbPassword", config.CoderDbPassword, true},
			{"coder:dbName", config.CoderDbName, false},
		}

		// The kubeconfig file path is relative to the job directory
		commands = append(commands, configCommand{"k8s:externalKubeconfigPath", "external-kubeconfig.yaml", false})
	} else {
		// Standard mode: full infrastructure config
		prefixedGatewayName := fmt.Sprintf("%s-%s", config.StackName, config.NetworkGatewayName)
		prefixedPrivateNetworkName := fmt.Sprintf("%s-%s", config.StackName, config.NetworkPrivateNetworkName)
		prefixedK8sClusterName := fmt.Sprintf("%s-%s", config.StackName, config.K8sClusterName)
		prefixedNodePoolName := fmt.Sprintf("%s-%s", config.StackName, config.NodePoolName)

		commands = []configCommand{
			{"network:gatewayName", prefixedGatewayName, false},
			{"network:gatewayModel", config.NetworkGatewayModel, false},
			{"network:privateNetworkName", prefixedPrivateNetworkName, false},
			{"network:region", config.NetworkRegion, false},
			{"network:networkMask", config.NetworkMask, false},
			{"network:networkStartIp", config.NetworkStartIP, false},
			{"network:networkEndIp", config.NetworkEndIP, false},
			{"nodepool:name", prefixedNodePoolName, false},
			{"nodepool:flavor", config.NodePoolFlavor, false},
			{"nodepool:desiredNodeCount", fmt.Sprintf("%d", config.NodePoolDesiredNodeCount), false},
			{"nodepool:minNodeCount", fmt.Sprintf("%d", config.NodePoolMinNodeCount), false},
			{"nodepool:maxNodeCount", fmt.Sprintf("%d", config.NodePoolMaxNodeCount), false},
			{"k8s:clusterName", prefixedK8sClusterName, false},
			{"coder:namespace", coderNamespace, false},
			{"coder:adminEmail", config.CoderAdminEmail, false},
			{"coder:adminPassword", config.CoderAdminPassword, true},
			{"coder:version", config.CoderVersion, false},
			{"coder:dbUser", config.CoderDbUser, false},
			{"coder:dbPassword", config.CoderDbPassword, true},
			{"coder:dbName", config.CoderDbName, false},
		}

		if config.NetworkID != "" {
			commands = append(commands, configCommand{"network:networkId", config.NetworkID, false})
		}
	}

	// Add Coder templates as JSON (multiple templates per lab)
	templates := config.GetCoderTemplates()
	if len(templates) > 0 {
		templatesJSON, err := json.Marshal(templates)
		if err != nil {
			log.Printf("Failed to marshal Coder templates: %v", err)
		} else {
			commands = append(commands, configCommand{"coder:templates", string(templatesJSON), false})
		}
	}

	return commands
}
