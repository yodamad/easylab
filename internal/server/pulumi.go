package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
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
	jobManager  *JobManager
	workDir     string
	projectRoot string
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
	// Find project root at startup
	projectRoot := findProjectRoot()
	log.Printf("Project root: %s", projectRoot)
	log.Printf("Work directory: %s", workDir)

	return &PulumiExecutor{
		jobManager:  jobManager,
		workDir:     workDir,
		projectRoot: projectRoot,
	}
}

// GetWorkDir returns the work directory path
func (pe *PulumiExecutor) GetWorkDir() string {
	return pe.workDir
}

// getOrCreateStack gets or creates a Pulumi stack using Automation API
func (pe *PulumiExecutor) getOrCreateStack(ctx context.Context, stackName, workDir, jobID string) (auto.Stack, error) {
	// Try to select existing stack first
	stack, err := auto.SelectStackLocalSource(ctx, stackName, workDir, auto.WorkDir(workDir))
	if err != nil {
		// Stack doesn't exist, create it
		pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Stack '%s' does not exist, creating it...", stackName))
		stack, err = auto.UpsertStackLocalSource(ctx, stackName, workDir, auto.WorkDir(workDir))
		if err != nil {
			// Return empty stack on error - caller should check error first
			return stack, fmt.Errorf("failed to create stack: %w", err)
		}
		pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Stack '%s' created successfully", stackName))
	} else {
		pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Stack '%s' selected successfully", stackName))
	}

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

	// Set ovh:endpoint config
	err := stack.SetConfig(ctx, "ovh:endpoint", auto.ConfigValue{
		Value:  config.OvhEndpoint,
		Secret: false,
	})
	if err != nil {
		return fmt.Errorf("failed to set config ovh:endpoint: %w", err)
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
	pulumiYaml := `name: lab-as-code
runtime: go
description: OVHcloud Gateway and Managed Kubernetes infrastructure

config:
  ovh:endpoint: ` + config.OvhEndpoint + `
`
	if err := os.WriteFile(filepath.Join(jobDir, "Pulumi.yaml"), []byte(pulumiYaml), 0644); err != nil {
		pe.jobManager.SetError(jobID, fmt.Errorf("failed to write Pulumi.yaml: %w", err))
		return nil, err
	}

	// Copy source files
	pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Copying source files from %s...", pe.projectRoot))
	if err := pe.copySourceFiles(jobDir); err != nil {
		pe.jobManager.SetError(jobID, fmt.Errorf("failed to copy source files: %w", err))
		return nil, err
	}
	pe.jobManager.AppendOutput(jobID, "Source files copied successfully")

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
	stack, err := pe.getOrCreateStack(ctx, config.StackName, jobDir, jobID)
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
		pulumiYaml := `name: lab-as-code
runtime: go
description: OVHcloud Gateway and Managed Kubernetes infrastructure

config:
  ovh:endpoint: ` + config.OvhEndpoint + `
`
		if err := os.WriteFile(filepath.Join(jobDir, "Pulumi.yaml"), []byte(pulumiYaml), 0644); err != nil {
			pe.jobManager.SetError(jobID, fmt.Errorf("failed to write Pulumi.yaml: %w", err))
			return nil, err
		}

		// Copy source files (needed for pulumi destroy to know what to destroy)
		pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Copying source files from %s...", pe.projectRoot))
		if err := pe.copySourceFiles(jobDir); err != nil {
			pe.jobManager.SetError(jobID, fmt.Errorf("failed to copy source files: %w", err))
			return nil, err
		}
		pe.jobManager.AppendOutput(jobID, "Source files copied successfully")
	}

	// Set up environment variables
	originalEnv := make(map[string]string)
	envVars := map[string]string{
		"OVH_APPLICATION_KEY":    config.OvhApplicationKey,
		"OVH_APPLICATION_SECRET": config.OvhApplicationSecret,
		"OVH_CONSUMER_KEY":       config.OvhConsumerKey,
		"OVH_SERVICE_NAME":       config.OvhServiceName,
	}

	for key, value := range envVars {
		if original, exists := os.LookupEnv(key); exists {
			originalEnv[key] = original
		}
		os.Setenv(key, value)
	}

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

	// Try to select the stack (don't create if it doesn't exist)
	pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Selecting Pulumi stack '%s'...", stackName))
	stack, err := auto.SelectStackLocalSource(ctx, stackName, jobDir, auto.WorkDir(jobDir))
	if err != nil {
		pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Warning: failed to select stack: %v. Stack may not exist.", err))
		// For destroy operations, if stack doesn't exist, we consider it already destroyed
		pe.jobManager.UpdateJobStatus(jobID, JobStatusDestroyed)
		pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Destroy completed at %s (stack did not exist)", time.Now().Format(time.RFC3339)))
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

// findProjectRoot finds the project root by looking for go.mod
// Checks PROJECT_ROOT environment variable first for faster startup
func findProjectRoot() string {
	// Check environment variable first (fastest path)
	if projectRoot := os.Getenv("PROJECT_ROOT"); projectRoot != "" {
		if _, err := os.Stat(filepath.Join(projectRoot, "go.mod")); err == nil {
			return projectRoot
		}
		log.Printf("Warning: PROJECT_ROOT environment variable set but go.mod not found at %s, searching...", projectRoot)
	}

	// Walk up directory tree to find go.mod
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached filesystem root
			break
		}
		dir = parent
	}

	// Fallback to current directory
	cwd, _ := os.Getwd()
	return cwd
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
		return err
	}

	// Extract outputs from the result
	pe.jobManager.AppendOutput(jobID, "Extracting stack outputs...")

	// Get kubeconfig from outputs
	if kubeconfigVal, ok := upResult.Outputs["kubeconfig"]; ok {
		kubeconfig := pe.outputValueToString(kubeconfigVal)
		if kubeconfig != "" {
			// Validate that kubeconfig looks like valid YAML
			if !strings.Contains(kubeconfig, "apiVersion") && !strings.Contains(kubeconfig, "kind:") {
				pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Warning: kubeconfig may be invalid (length: %d chars)", len(kubeconfig)))
			}
			pe.jobManager.SetKubeconfig(jobID, kubeconfig)
			pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Kubeconfig extracted successfully (length: %d chars)", len(kubeconfig)))
		}
	}

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

	// Clean up the job directory since all necessary data has been extracted
	if err := pe.cleanupJobDirectory(jobID); err != nil {
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

	// Get stack name for output
	job, _ := pe.jobManager.GetJob(jobID)
	job.mu.RLock()
	stackName := job.Config.StackName
	job.mu.RUnlock()

	// Run pulumi destroy with streaming output
	pe.jobManager.AppendOutput(jobID, "Running pulumi destroy...")
	_, err = prep.Stack.Destroy(prep.Context, optdestroy.ProgressStreams(prep.Writer))
	if err != nil {
		pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Warning: pulumi destroy failed: %v. Continuing with stack removal...", err))
		// Continue with stack removal even if destroy failed
	}

	// Note: Stack removal is not necessary for local workspaces.
	// The stack metadata is stored in the workspace directory (jobDir),
	// which will be cleaned up when the job directory is removed.
	// The Destroy() operation above has already removed all resources.
	pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Stack '%s' resources destroyed. Stack metadata will be cleaned up with job directory.", stackName))

	// Success - mark as destroyed
	pe.jobManager.UpdateJobStatus(jobID, JobStatusDestroyed)
	pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Destroy completed at %s", time.Now().Format(time.RFC3339)))
	pe.jobManager.AppendOutput(jobID, "✅ Stack destroyed successfully. You can recreate it using the same configuration.")

	// Clean up the job directory since all resources have been destroyed
	if err := pe.cleanupJobDirectory(jobID); err != nil {
		log.Printf("Warning: failed to cleanup job directory for %s: %v", jobID, err)
		// Don't fail the job if cleanup fails
	}

	return nil
}

// cleanupJobDirectory removes the job's working directory after successful completion
func (pe *PulumiExecutor) cleanupJobDirectory(jobID string) error {
	jobDir := filepath.Join(pe.workDir, jobID)

	// Check if directory exists
	if _, err := os.Stat(jobDir); os.IsNotExist(err) {
		// Directory already doesn't exist, nothing to clean up
		return nil
	}

	// Remove the entire job directory
	if err := os.RemoveAll(jobDir); err != nil {
		return fmt.Errorf("failed to remove job directory %s: %w", jobDir, err)
	}

	pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Cleaned up job directory: %s", jobDir))
	return nil
}

type configCommand struct {
	key    string
	value  string
	secret bool
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

	// Add template file path if provided
	if config.TemplateFilePath != "" {
		commands = append(commands, configCommand{"coder:templateFilePath", config.TemplateFilePath, false})
	}

	return commands
}

func (pe *PulumiExecutor) copySourceFiles(jobDir string) error {
	// Create subdirectories
	dirs := []string{"coder", "k8s", "ovh", "utils"}
	for _, dir := range dirs {
		if err := os.MkdirAll(filepath.Join(jobDir, dir), 0755); err != nil {
			return fmt.Errorf("failed to create dir %s: %w", dir, err)
		}
	}

	// Copy main.go
	if err := pe.copyFile(filepath.Join(pe.projectRoot, "main.go"), filepath.Join(jobDir, "main.go")); err != nil {
		return fmt.Errorf("failed to copy main.go: %w", err)
	}

	// Copy go.mod and go.sum
	if err := pe.copyFile(filepath.Join(pe.projectRoot, "go.mod"), filepath.Join(jobDir, "go.mod")); err != nil {
		return fmt.Errorf("failed to copy go.mod: %w", err)
	}
	// go.sum might not exist, continue on error
	pe.copyFile(filepath.Join(pe.projectRoot, "go.sum"), filepath.Join(jobDir, "go.sum"))

	// Copy all source files from subdirectories
	for _, dir := range dirs {
		srcDir := filepath.Join(pe.projectRoot, dir)
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
