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

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/optdestroy"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/optpreview"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/optup"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// PulumiExecutor handles Pulumi command execution
type PulumiExecutor struct {
	jobManager   *JobManager
	workDir      string
	templatesDir string
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

// NewPulumiExecutor creates a new Pulumi executor
func NewPulumiExecutor(jobManager *JobManager, workDir string) *PulumiExecutor {
	// Get templates directory from environment variable or use default
	templatesDir := os.Getenv("TEMPLATES_DIR")
	if templatesDir == "" {
		templatesDir = "/app/templates"
	}
	log.Printf("Templates directory: %s", templatesDir)
	log.Printf("Work directory: %s", workDir)

	return &PulumiExecutor{
		jobManager:   jobManager,
		workDir:      workDir,
		templatesDir: templatesDir,
	}
}

// GetWorkDir returns the work directory path
func (pe *PulumiExecutor) GetWorkDir() string {
	return pe.workDir
}

// getLocalBackendEnvVars returns environment variables for local file backend and Go cache
func getLocalBackendEnvVars() map[string]string {
	envVars := map[string]string{
		"PULUMI_BACKEND_URL":       "file://",
		"PULUMI_SKIP_UPDATE_CHECK": "true",
	}

	// Set Go cache directories if not already set (use writable locations)
	if gomodcache := os.Getenv("GOMODCACHE"); gomodcache == "" {
		envVars["GOMODCACHE"] = "/app/.go/pkg/mod"
	}
	if gocache := os.Getenv("GOCACHE"); gocache == "" {
		envVars["GOCACHE"] = "/app/.go/cache"
	}
	// Set GOPATH to ensure sumdb and other Go directories use writable location
	if gopath := os.Getenv("GOPATH"); gopath == "" {
		envVars["GOPATH"] = "/app/.go"
	}

	return envVars
}

// getPulumiEnvVars returns environment variables for Pulumi operations including provider credentials
func getPulumiEnvVars(config *LabConfig) map[string]string {
	envVars := getLocalBackendEnvVars()

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

// getOrCreateStack gets or creates a Pulumi stack using Automation API
func (pe *PulumiExecutor) getOrCreateStack(ctx context.Context, stackName, workDir, jobID string, config *LabConfig) (auto.Stack, error) {
	// Get environment variables including OVH credentials
	envVars := getPulumiEnvVars(config)

	// First, try to select the stack to check if it exists
	pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Checking if stack '%s' exists...", stackName))
	stack, err := auto.SelectStackLocalSource(ctx, stackName, workDir,
		auto.WorkDir(workDir),
		auto.EnvVars(envVars))
	if err == nil {
		// Stack exists, return it
		pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Stack '%s' exists, selected successfully", stackName))
		return stack, nil
	}

	// If selection failed, check if it's because the stack doesn't exist
	// Try to list stacks to verify
	workspace, wsErr := auto.NewLocalWorkspace(ctx,
		auto.WorkDir(workDir),
		auto.EnvVars(envVars))
	if wsErr != nil {
		return auto.Stack{}, fmt.Errorf("failed to create workspace: %w", wsErr)
	}

	stacks, listErr := workspace.ListStacks(ctx)
	if listErr != nil {
		// If we can't list stacks, assume stack doesn't exist and try to create it
		pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Stack '%s' does not exist (could not verify), creating it...", stackName))
		stack, createErr := auto.UpsertStackLocalSource(ctx, stackName, workDir,
			auto.WorkDir(workDir),
			auto.EnvVars(envVars))
		if createErr != nil {
			return auto.Stack{}, fmt.Errorf("failed to create stack: %w", createErr)
		}
		pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Stack '%s' created successfully", stackName))
		return stack, nil
	}

	// Verify stack doesn't exist in the list
	stackExists := false
	for _, s := range stacks {
		if s.Name == stackName {
			stackExists = true
			break
		}
	}

	if stackExists {
		// Stack exists in list but selection failed - try again with more context
		pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Stack '%s' exists in workspace, retrying selection...", stackName))
		stack, err := auto.SelectStackLocalSource(ctx, stackName, workDir,
			auto.WorkDir(workDir),
			auto.EnvVars(envVars))
		if err != nil {
			return auto.Stack{}, fmt.Errorf("failed to select existing stack: %w", err)
		}
		pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Stack '%s' selected successfully", stackName))
		return stack, nil
	}

	// Stack doesn't exist, create it
	pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Stack '%s' does not exist, creating it...", stackName))
	stack, err = auto.UpsertStackLocalSource(ctx, stackName, workDir,
		auto.WorkDir(workDir),
		auto.EnvVars(envVars))
	if err != nil {
		return auto.Stack{}, fmt.Errorf("failed to create stack: %w", err)
	}
	pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Stack '%s' created successfully", stackName))
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

	// Set provider-specific config
	provider := config.Provider
	if provider == "" {
		provider = "ovh" // Default to OVH for backward compatibility
	}

	switch provider {
	case "ovh":
		// Set ovh:endpoint config
		err := stack.SetConfig(ctx, "ovh:endpoint", auto.ConfigValue{
			Value:  config.OvhEndpoint,
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

	return nil
}

// JobPreparation holds the prepared resources for a Pulumi operation
type JobPreparation struct {
	Stack   auto.Stack
	Writer  *jobOutputWriter
	Context context.Context
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

	// Write Pulumi.yaml
	pe.jobManager.AppendOutput(jobID, "Writing Pulumi.yaml...")
	pulumiYaml := pe.generatePulumiYaml(config)
	if err := os.WriteFile(filepath.Join(jobDir, "Pulumi.yaml"), []byte(pulumiYaml), 0644); err != nil {
		pe.jobManager.SetError(jobID, fmt.Errorf("failed to write Pulumi.yaml: %w", err))
		return nil, err
	}

	// Generate source files from templates
	pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Generating source files from templates in %s...", pe.templatesDir))
	if err := pe.generateSourceFiles(jobDir, config); err != nil {
		pe.jobManager.SetError(jobID, fmt.Errorf("failed to generate source files: %w", err))
		return nil, err
	}
	pe.jobManager.AppendOutput(jobID, "Source files generated successfully")

	// Pre-download Go modules to avoid hanging during compilation
	pe.jobManager.AppendOutput(jobID, "Pre-downloading Go modules...")
	if err := pe.downloadGoModules(jobDir, jobID); err != nil {
		pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Warning: failed to pre-download Go modules: %v. Pulumi will download them during compilation.", err))
		// Don't fail - Pulumi can still download modules during compilation
	} else {
		pe.jobManager.AppendOutput(jobID, "Go modules downloaded successfully")
	}

	// Set up environment variables
	originalEnv := make(map[string]string)
	envVars := map[string]string{
		"OVH_APPLICATION_KEY":    config.OvhApplicationKey,
		"OVH_APPLICATION_SECRET": config.OvhApplicationSecret,
		"OVH_CONSUMER_KEY":       config.OvhConsumerKey,
		"OVH_SERVICE_NAME":       config.OvhServiceName,
	}

	// Save original values and set new ones
	for key, value := range envVars {
		if original, exists := os.LookupEnv(key); exists {
			originalEnv[key] = original
		}
		os.Setenv(key, value)
	}

	// Restore original env vars when done
	defer func() {
		for key, value := range originalEnv {
			os.Setenv(key, value)
		}
		for key := range envVars {
			if _, wasSet := originalEnv[key]; !wasSet {
				os.Unsetenv(key)
			}
		}
	}()

	// Get or create stack
	pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Initializing Pulumi stack '%s'...", config.StackName))
	stack, err := pe.getOrCreateStack(ctx, config.StackName, jobDir, jobID, config)
	if err != nil {
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
	defer outputWriter.Flush()

	return &JobPreparation{
		Stack:   stack,
		Writer:  outputWriter,
		Context: ctx,
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

	// Find job directory
	jobDir := filepath.Join(pe.workDir, jobID)
	if _, err := os.Stat(jobDir); os.IsNotExist(err) {
		pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Job directory not found: %s. Creating it...", jobDir))
		if err := os.MkdirAll(jobDir, 0755); err != nil {
			pe.jobManager.SetError(jobID, fmt.Errorf("failed to create job directory: %w", err))
			return nil, err
		}

		// Write Pulumi.yaml
		pulumiYaml := pe.generatePulumiYaml(config)
		if err := os.WriteFile(filepath.Join(jobDir, "Pulumi.yaml"), []byte(pulumiYaml), 0644); err != nil {
			pe.jobManager.SetError(jobID, fmt.Errorf("failed to write Pulumi.yaml: %w", err))
			return nil, err
		}

		// Generate source files from templates (needed for pulumi destroy to know what to destroy)
		pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Generating source files from templates in %s...", pe.templatesDir))
		if err := pe.generateSourceFiles(jobDir, config); err != nil {
			pe.jobManager.SetError(jobID, fmt.Errorf("failed to generate source files: %w", err))
			return nil, err
		}
		pe.jobManager.AppendOutput(jobID, "Source files generated successfully")

		// Pre-download Go modules to avoid hanging during compilation
		pe.jobManager.AppendOutput(jobID, "Pre-downloading Go modules...")
		if err := pe.downloadGoModules(jobDir, jobID); err != nil {
			pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Warning: failed to pre-download Go modules: %v. Pulumi will download them during compilation.", err))
			// Don't fail - Pulumi can still download modules during compilation
		} else {
			pe.jobManager.AppendOutput(jobID, "Go modules downloaded successfully")
		}
	}

	// Set up environment variables
	originalEnv := make(map[string]string)
	osEnvVars := map[string]string{
		"OVH_APPLICATION_KEY":    config.OvhApplicationKey,
		"OVH_APPLICATION_SECRET": config.OvhApplicationSecret,
		"OVH_CONSUMER_KEY":       config.OvhConsumerKey,
		"OVH_SERVICE_NAME":       config.OvhServiceName,
	}

	for key, value := range osEnvVars {
		if original, exists := os.LookupEnv(key); exists {
			originalEnv[key] = original
		}
		os.Setenv(key, value)
	}

	defer func() {
		for key, value := range originalEnv {
			os.Setenv(key, value)
		}
		for key := range osEnvVars {
			if _, wasSet := originalEnv[key]; !wasSet {
				os.Unsetenv(key)
			}
		}
	}()

	// Get environment variables including OVH credentials for Pulumi Automation API
	pulumiEnvVars := getPulumiEnvVars(config)

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

	// Try to select the stack (don't create if it doesn't exist)
	pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Selecting Pulumi stack '%s'...", stackName))
	stack, err := auto.SelectStackLocalSource(ctx, stackName, jobDir,
		auto.WorkDir(jobDir),
		auto.EnvVars(pulumiEnvVars))
	if err != nil {
		// Provide detailed error message for file backend
		pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Error: failed to select stack '%s': %v", stackName, err))
		pe.jobManager.AppendOutput(jobID, "This may occur if:")
		pe.jobManager.AppendOutput(jobID, "  1. The stack was never created")
		pe.jobManager.AppendOutput(jobID, "  2. The .pulumi directory was deleted (stack state lost)")
		pe.jobManager.AppendOutput(jobID, "  3. The stack name doesn't match")

		// Check if .pulumi directory exists to provide more specific guidance
		if _, statErr := os.Stat(pulumiDir); os.IsNotExist(statErr) {
			pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Stack state directory (.pulumi) is missing at %s", pulumiDir))
			pe.jobManager.AppendOutput(jobID, "In file backend mode, stack state must be preserved after deployment for destroy operations.")
		} else {
			// .pulumi exists but stack selection failed - might be wrong stack name or corrupted state
			pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Stack state directory exists but stack '%s' could not be found", stackName))
		}

		// For destroy operations, if stack doesn't exist, we consider it already destroyed
		pe.jobManager.UpdateJobStatus(jobID, JobStatusDestroyed)
		pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Destroy completed at %s (stack did not exist or state is missing)", time.Now().Format(time.RFC3339)))
		return nil, nil // Special case: return nil to indicate stack didn't exist
	}

	// Create output writer for streaming
	outputWriter := &jobOutputWriter{
		jobID:      jobID,
		jobManager: pe.jobManager,
	}
	defer outputWriter.Flush()

	return &JobPreparation{
		Stack:   stack,
		Writer:  outputWriter,
		Context: ctx,
	}, nil
}

// extractKubeconfigFromOutputs extracts kubeconfig from outputs if cluster exists
func (pe *PulumiExecutor) extractKubeconfigFromOutputs(jobID string, outputs auto.OutputMap) {
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
		pe.jobManager.AppendOutput(jobID, "Kubernetes cluster not found in outputs, checking for local kubeconfig.yaml file...")
		// Check if kubeconfig.yaml exists locally as fallback
		pe.checkLocalKubeconfigFile(jobID)
	}
}

// checkLocalKubeconfigFile checks if kubeconfig.yaml exists in the job directory and reads it
func (pe *PulumiExecutor) checkLocalKubeconfigFile(jobID string) {
	jobDir := filepath.Join(pe.workDir, jobID)
	kubeconfigPath := filepath.Join(jobDir, "kubeconfig.yaml")

	// Check if file exists
	if _, err := os.Stat(kubeconfigPath); os.IsNotExist(err) {
		pe.jobManager.AppendOutput(jobID, "Local kubeconfig.yaml file not found")
		return
	}

	// Read the file
	kubeconfigBytes, err := os.ReadFile(kubeconfigPath)
	if err != nil {
		pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Failed to read local kubeconfig.yaml: %v", err))
		return
	}

	kubeconfig := strings.TrimSpace(string(kubeconfigBytes))
	if kubeconfig == "" {
		pe.jobManager.AppendOutput(jobID, "Local kubeconfig.yaml file is empty")
		return
	}

	// Validate that kubeconfig looks like valid YAML
	if !strings.Contains(kubeconfig, "apiVersion") && !strings.Contains(kubeconfig, "kind:") {
		pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Warning: local kubeconfig may be invalid (length: %d chars)", len(kubeconfig)))
	}

	pe.jobManager.SetKubeconfig(jobID, kubeconfig)
	pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Kubeconfig loaded from local file (length: %d chars)", len(kubeconfig)))
}

// Execute runs pulumi up for a given job
func (pe *PulumiExecutor) Execute(jobID string) error {
	// Prepare job with common setup
	prep, err := pe.prepareJob(jobID, false) // false = always create directory
	if err != nil {
		return err
	}

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

// Preview runs pulumi preview for a given job (dry run)
func (pe *PulumiExecutor) Preview(jobID string) error {
	// Prepare job with common setup
	prep, err := pe.prepareJob(jobID, false) // false = always create directory
	if err != nil {
		return err
	}

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

	// Get stack name and job directory for output
	job, _ := pe.jobManager.GetJob(jobID)
	job.mu.RLock()
	stackName := job.Config.StackName
	job.mu.RUnlock()
	jobDir := filepath.Join(pe.workDir, jobID)

	// Run pulumi destroy with streaming output
	pe.jobManager.AppendOutput(jobID, "Running pulumi destroy...")
	_, err = prep.Stack.Destroy(prep.Context, optdestroy.ProgressStreams(prep.Writer))
	if err != nil {
		pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Warning: pulumi destroy failed: %v. Continuing with stack removal...", err))
		// Continue with stack removal even if destroy failed
	}

	// Get environment variables including OVH credentials
	envVars := getPulumiEnvVars(job.Config)

	// Remove the stack from the workspace
	pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Removing stack '%s' from workspace...", stackName))
	workspace, wsErr := auto.NewLocalWorkspace(prep.Context,
		auto.WorkDir(jobDir),
		auto.EnvVars(envVars))
	if wsErr != nil {
		pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Warning: failed to create workspace for stack removal: %v", wsErr))
		// Continue with cleanup even if workspace creation failed
	} else {
		if removeErr := workspace.RemoveStack(prep.Context, stackName); removeErr != nil {
			pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Warning: failed to remove stack from workspace: %v", removeErr))
			// Continue with cleanup even if stack removal failed
		} else {
			pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Stack '%s' removed from workspace successfully", stackName))
		}
	}

	pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Stack '%s' resources destroyed and stack removed.", stackName))

	// Success - mark as destroyed
	pe.jobManager.UpdateJobStatus(jobID, JobStatusDestroyed)
	pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Destroy completed at %s", time.Now().Format(time.RFC3339)))
	pe.jobManager.AppendOutput(jobID, "✅ Stack destroyed successfully. You can recreate it using the same configuration.")

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

// generatePulumiYaml generates the Pulumi.yaml content based on the provider
func (pe *PulumiExecutor) generatePulumiYaml(config *LabConfig) string {
	provider := config.Provider
	if provider == "" {
		provider = "ovh" // Default to OVH for backward compatibility
	}

	description := "Multi-cloud Gateway and Managed Kubernetes infrastructure"
	configSection := ""

	switch provider {
	case "ovh":
		description = "OVHcloud Gateway and Managed Kubernetes infrastructure"
		configSection = fmt.Sprintf("  ovh:endpoint: %s\n", config.OvhEndpoint)
		// Future providers can be added here:
		// case "aws":
		//     description = "AWS Gateway and Managed Kubernetes infrastructure"
		//     configSection = fmt.Sprintf("  aws:region: %s\n", config.AwsRegion)
	}

	return fmt.Sprintf(`name: lab-as-code
runtime: go
description: %s

config:
%s`, description, configSection)
}

func (pe *PulumiExecutor) getConfigCommands(config *LabConfig) []configCommand {
	// Prefix resource names with stack name
	prefixedGatewayName := fmt.Sprintf("%s-%s", config.StackName, config.NetworkGatewayName)
	prefixedPrivateNetworkName := fmt.Sprintf("%s-%s", config.StackName, config.NetworkPrivateNetworkName)
	prefixedK8sClusterName := fmt.Sprintf("%s-%s", config.StackName, config.K8sClusterName)
	prefixedNodePoolName := fmt.Sprintf("%s-%s", config.StackName, config.NodePoolName)

	commands := []configCommand{
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
		{"coder:adminEmail", config.CoderAdminEmail, false},
		{"coder:adminPassword", config.CoderAdminPassword, true}, // secret
		{"coder:version", config.CoderVersion, false},
		{"coder:dbUser", config.CoderDbUser, false},
		{"coder:dbPassword", config.CoderDbPassword, true}, // secret
		{"coder:dbName", config.CoderDbName, false},
		{"coder:templateName", config.CoderTemplateName, false},
	}

	if config.NetworkID != "" {
		commands = append(commands, configCommand{"network:networkId", config.NetworkID, false})
	}

	// Add template source configuration
	if config.TemplateSource != "" {
		commands = append(commands, configCommand{"coder:templateSource", config.TemplateSource, false})
	}

	// Add template file path if provided (for upload source)
	if config.TemplateFilePath != "" {
		commands = append(commands, configCommand{"coder:templateFilePath", config.TemplateFilePath, false})
	}

	// Add Git template configuration if source is git
	if config.TemplateSource == "git" {
		if config.TemplateGitRepo != "" {
			commands = append(commands, configCommand{"coder:templateGitRepo", config.TemplateGitRepo, false})
		}
		if config.TemplateGitFolder != "" {
			commands = append(commands, configCommand{"coder:templateGitFolder", config.TemplateGitFolder, false})
		}
		if config.TemplateGitBranch != "" {
			commands = append(commands, configCommand{"coder:templateGitBranch", config.TemplateGitBranch, false})
		}
	}

	return commands
}

func (pe *PulumiExecutor) generateSourceFiles(jobDir string, config *LabConfig) error {
	// Verify templates directory exists
	if _, err := os.Stat(pe.templatesDir); os.IsNotExist(err) {
		return fmt.Errorf("templates directory not found: %s", pe.templatesDir)
	}

	// Determine provider (default to ovh for backward compatibility)
	provider := config.Provider
	if provider == "" {
		provider = "ovh"
	}

	// Create subdirectories - common directories plus provider-specific
	dirs := []string{"coder", "k8s", "utils"}
	// Add provider-specific directory
	providerDir := provider
	// For now, we use "ovh" directory, but structure allows for "providers/ovh" in future
	dirs = append(dirs, providerDir)

	for _, dir := range dirs {
		if err := os.MkdirAll(filepath.Join(jobDir, dir), 0755); err != nil {
			return fmt.Errorf("failed to create dir %s: %w", dir, err)
		}
	}

	// Copy main.go from templates
	if err := pe.copyFile(filepath.Join(pe.templatesDir, "main.go"), filepath.Join(jobDir, "main.go")); err != nil {
		return fmt.Errorf("failed to copy main.go: %w", err)
	}

	// Copy go.mod and go.sum from templates
	if err := pe.copyFile(filepath.Join(pe.templatesDir, "go.mod"), filepath.Join(jobDir, "go.mod")); err != nil {
		return fmt.Errorf("failed to copy go.mod: %w", err)
	}
	// go.sum might not exist, continue on error
	pe.copyFile(filepath.Join(pe.templatesDir, "go.sum"), filepath.Join(jobDir, "go.sum"))

	// Copy all source files from template subdirectories
	for _, dir := range dirs {
		srcDir := filepath.Join(pe.templatesDir, dir)
		dstDir := filepath.Join(jobDir, dir)
		if err := pe.copyDir(srcDir, dstDir); err != nil {
			return fmt.Errorf("failed to copy dir %s: %w", dir, err)
		}
	}

	return nil
}

func (pe *PulumiExecutor) copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0644)
}

func (pe *PulumiExecutor) copyDir(srcDir, dstDir string) error {
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		src := filepath.Join(srcDir, entry.Name())
		dst := filepath.Join(dstDir, entry.Name())
		if err := pe.copyFile(src, dst); err != nil {
			return err
		}
	}
	return nil
}

// downloadGoModules pre-downloads Go modules to avoid hanging during Pulumi compilation
func (pe *PulumiExecutor) downloadGoModules(jobDir string, jobID string) error {
	// Get Go environment variables
	envVars := getLocalBackendEnvVars()

	// Build environment for the command - start with current environment
	cmdEnv := os.Environ()

	// Add/override with Go-related environment variables
	for key, value := range envVars {
		// Add all Go-related env vars (GOMODCACHE, GOCACHE, GOPATH)
		if strings.HasPrefix(key, "GO") {
			// Remove existing entry if present
			for i, env := range cmdEnv {
				if strings.HasPrefix(env, key+"=") {
					cmdEnv = append(cmdEnv[:i], cmdEnv[i+1:]...)
					break
				}
			}
			cmdEnv = append(cmdEnv, fmt.Sprintf("%s=%s", key, value))
		}
	}

	// Get job config to include provider credentials in environment
	job, exists := pe.jobManager.GetJob(jobID)
	if exists && job.Config != nil {
		provider := job.Config.Provider
		if provider == "" {
			provider = "ovh" // Default to OVH for backward compatibility
		}

		// Add provider-specific credentials to command environment
		switch provider {
		case "ovh":
			cmdEnv = append(cmdEnv, fmt.Sprintf("OVH_APPLICATION_KEY=%s", job.Config.OvhApplicationKey))
			cmdEnv = append(cmdEnv, fmt.Sprintf("OVH_APPLICATION_SECRET=%s", job.Config.OvhApplicationSecret))
			cmdEnv = append(cmdEnv, fmt.Sprintf("OVH_CONSUMER_KEY=%s", job.Config.OvhConsumerKey))
			cmdEnv = append(cmdEnv, fmt.Sprintf("OVH_SERVICE_NAME=%s", job.Config.OvhServiceName))
			// Future providers can be added here
		}
	}

	// Run go mod download with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "go", "mod", "download")
	cmd.Dir = jobDir
	cmd.Env = cmdEnv

	// Capture output for logging
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Log the output even on error for debugging
		pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Go mod download output: %s", string(output)))
		return fmt.Errorf("go mod download failed: %w", err)
	}

	return nil
}
