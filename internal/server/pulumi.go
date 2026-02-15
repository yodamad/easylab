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
	jobManager       *JobManager
	workDir          string
	templatesDir     string
	moduleCacheReady bool // Indicates if Go module cache has been pre-warmed
	useInlineProgram bool // If true, use pre-compiled inline program instead of template copying
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
	// Get templates directory from environment variable or derive from base dir
	templatesDir := os.Getenv("TEMPLATES_DIR")
	if templatesDir == "" {
		templatesDir = filepath.Join(getAppBaseDir(), "templates")
	}
	log.Printf("Templates directory: %s", templatesDir)
	log.Printf("Work directory: %s", workDir)

	// Check if inline Pulumi program mode is enabled
	// This mode uses pre-compiled code instead of copying templates
	// Set USE_INLINE_PULUMI=true to enable (experimental)
	useInline := getEnvOrDefault("USE_INLINE_PULUMI", "false") == "true"
	if useInline {
		log.Printf("[PULUMI] Inline program mode ENABLED - using pre-compiled Pulumi program")
	} else {
		log.Printf("[PULUMI] Template mode - copying template files for each job")
	}

	return &PulumiExecutor{
		jobManager:       jobManager,
		workDir:          workDir,
		templatesDir:     templatesDir,
		moduleCacheReady: false,
		useInlineProgram: useInline,
	}
}

// PrewarmModuleCache pre-downloads Go modules from templates directory
// This should be called at application startup to avoid repeated downloads per job
func (pe *PulumiExecutor) PrewarmModuleCache() error {
	startTime := time.Now()
	log.Printf("[PREWARM] Starting Go module cache pre-warming from templates directory: %s", pe.templatesDir)

	// Verify templates directory exists
	if _, err := os.Stat(pe.templatesDir); os.IsNotExist(err) {
		return fmt.Errorf("templates directory not found: %s", pe.templatesDir)
	}

	// Get environment variables for Go module cache
	envVars := getLocalBackendEnvVars()

	// Ensure Go module cache directories exist
	if gomodcache, ok := envVars["GOMODCACHE"]; ok {
		if err := os.MkdirAll(gomodcache, 0755); err != nil {
			log.Printf("[PREWARM] Warning: failed to create GOMODCACHE directory %s: %v", gomodcache, err)
		} else {
			log.Printf("[PREWARM] Go module cache directory: %s", gomodcache)
		}
	}
	if gocache, ok := envVars["GOCACHE"]; ok {
		if err := os.MkdirAll(gocache, 0755); err != nil {
			log.Printf("[PREWARM] Warning: failed to create GOCACHE directory %s: %v", gocache, err)
		}
	}
	if gopath, ok := envVars["GOPATH"]; ok {
		if err := os.MkdirAll(gopath, 0755); err != nil {
			log.Printf("[PREWARM] Warning: failed to create GOPATH directory %s: %v", gopath, err)
		}
	}

	// Build environment for the command - start with current environment
	cmdEnv := os.Environ()

	// Add/override with all environment variables from getLocalBackendEnvVars
	for key, value := range envVars {
		// Remove existing entry if present
		for i, env := range cmdEnv {
			if strings.HasPrefix(env, key+"=") {
				cmdEnv = append(cmdEnv[:i], cmdEnv[i+1:]...)
				break
			}
		}
		cmdEnv = append(cmdEnv, fmt.Sprintf("%s=%s", key, value))
	}

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Run go mod download in templates directory
	log.Printf("[PREWARM] Running go mod download in templates directory...")
	downloadCmd := exec.CommandContext(ctx, "go", "mod", "download")
	downloadCmd.Dir = pe.templatesDir
	downloadCmd.Env = cmdEnv

	output, err := downloadCmd.CombinedOutput()
	if err != nil {
		log.Printf("[PREWARM] Warning: go mod download failed: %v\nOutput: %s", err, string(output))
		// Don't fail - modules might still be available from previous runs
	} else {
		log.Printf("[PREWARM] Go modules downloaded successfully")
	}

	// Verify critical subpackages are accessible
	log.Printf("[PREWARM] Verifying critical subpackages...")
	requiredSubpackages := []string{
		"github.com/ovh/pulumi-ovh/sdk/v2/go/ovh/cloudproject",
		"github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes",
		"github.com/pulumi/pulumi/sdk/v3/go/pulumi",
	}

	for _, pkg := range requiredSubpackages {
		listCmd := exec.CommandContext(ctx, "go", "list", pkg)
		listCmd.Dir = pe.templatesDir
		listCmd.Env = cmdEnv
		if _, listErr := listCmd.CombinedOutput(); listErr != nil {
			log.Printf("[PREWARM] Warning: subpackage %s not accessible: %v", pkg, listErr)
		} else {
			log.Printf("[PREWARM] Subpackage %s verified", pkg)
		}
	}

	pe.moduleCacheReady = true
	log.Printf("[PREWARM] Go module cache pre-warming completed in %v", time.Since(startTime))
	return nil
}

// IsModuleCacheReady returns whether the module cache has been pre-warmed
func (pe *PulumiExecutor) IsModuleCacheReady() bool {
	return pe.moduleCacheReady
}

// GetWorkDir returns the work directory path
func (pe *PulumiExecutor) GetWorkDir() string {
	return pe.workDir
}

// getLocalBackendEnvVars returns environment variables for local file backend and Go cache
// workDir parameter is kept for API compatibility but not used for backend URL
// The backend URL should be "file://" which creates .pulumi directory in the workspace (WorkDir)
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

	// Set Go cache directories if not already set (use writable locations)
	envVars["GOMODCACHE"] = getEnvOrDefault("GOMODCACHE", filepath.Join(getAppBaseDir(), ".go", "pkg", "mod"))
	envVars["GOCACHE"] = getEnvOrDefault("GOCACHE", filepath.Join(getAppBaseDir(), ".go", "cache"))
	envVars["GOPATH"] = getEnvOrDefault("GOPATH", filepath.Join(getAppBaseDir(), ".go"))

	// Disable Go workspace mode to prevent interference with module resolution
	envVars["GOWORK"] = "off"

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

// getOrCreateStack gets or creates a Pulumi stack using Automation API
func (pe *PulumiExecutor) getOrCreateStack(ctx context.Context, stackName, workDir, jobID string, config *LabConfig) (auto.Stack, error) {
	// Ensure workDir exists and is writable before proceeding
	if err := os.MkdirAll(workDir, 0755); err != nil {
		return auto.Stack{}, fmt.Errorf("failed to ensure work directory exists: %w", err)
	}

	// Verify workDir is writable
	if err := os.WriteFile(filepath.Join(workDir, ".pulumi-test"), []byte("test"), 0644); err != nil {
		return auto.Stack{}, fmt.Errorf("work directory is not writable: %w", err)
	}
	os.Remove(filepath.Join(workDir, ".pulumi-test"))

	// Get environment variables including OVH credentials
	envVars := getPulumiEnvVars(config, workDir)

	// First, try to select the stack to check if it exists
	pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Checking if stack '%s' exists in %s...", stackName, workDir))
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
	Cleanup func()            // Cleanup function to restore env vars after Pulumi operations complete
	EnvVars map[string]string // Environment variables to pass to Pulumi operations (Up, Preview, Destroy)
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

	// When using inline program mode, skip template file generation and Go module download
	// The program is pre-compiled into the binary, so we only need the job directory for state
	if pe.useInlineProgram {
		pe.jobManager.AppendOutput(jobID, "Using inline program mode - skipping template generation and Go module download")
	} else {
		// Write Pulumi.yaml (needed for local source mode)
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
	}

	// Set up environment variables - both OS env vars and Pulumi Automation API env vars
	// Pulumi CLI reads from OS environment, so we need to set these as OS env vars too
	originalEnv := make(map[string]string)

	// Get Pulumi backend environment variables first (scoped to job directory)
	pulumiBackendEnvVars := getLocalBackendEnvVars(jobDir)

	// Combine OVH credentials and Pulumi backend env vars
	envVars := map[string]string{
		"OVH_APPLICATION_KEY":    config.OvhApplicationKey,
		"OVH_APPLICATION_SECRET": config.OvhApplicationSecret,
		"OVH_CONSUMER_KEY":       config.OvhConsumerKey,
		"OVH_SERVICE_NAME":       config.OvhServiceName,
	}

	// Add Pulumi backend environment variables to OS environment
	for key, value := range pulumiBackendEnvVars {
		if original, exists := os.LookupEnv(key); exists {
			originalEnv[key] = original
		}
		os.Setenv(key, value)
		envVars[key] = value // Track for cleanup
	}

	// Save original values and set new ones
	for key, value := range envVars {
		if original, exists := os.LookupEnv(key); exists {
			if _, alreadyTracked := originalEnv[key]; !alreadyTracked {
				originalEnv[key] = original
			}
		}
		os.Setenv(key, value)
	}

	// Create cleanup function to restore original env vars after Pulumi operations complete
	// This must be called by the caller (Execute, Preview, Destroy) after their operations finish
	cleanup := func() {
		for key, value := range originalEnv {
			os.Setenv(key, value)
		}
		for key := range envVars {
			if _, wasSet := originalEnv[key]; !wasSet {
				os.Unsetenv(key)
			}
		}
	}

	// Get or create stack - use inline program if enabled
	pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Initializing Pulumi stack '%s'...", config.StackName))
	var stack auto.Stack
	var err error
	if pe.useInlineProgram {
		stack, err = pe.getOrCreateStackInline(ctx, config.StackName, jobDir, jobID, config)
	} else {
		stack, err = pe.getOrCreateStack(ctx, config.StackName, jobDir, jobID, config)
	}
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
		EnvVars: envVars,
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

	// Set up environment variables - both OS env vars and Pulumi Automation API env vars
	// Pulumi CLI reads from OS environment, so we need to set these as OS env vars too
	originalEnv := make(map[string]string)

	// Get Pulumi backend environment variables first (scoped to job directory)
	pulumiBackendEnvVars := getLocalBackendEnvVars(jobDir)

	// Combine OVH credentials and Pulumi backend env vars
	envVars := map[string]string{
		"OVH_APPLICATION_KEY":    config.OvhApplicationKey,
		"OVH_APPLICATION_SECRET": config.OvhApplicationSecret,
		"OVH_CONSUMER_KEY":       config.OvhConsumerKey,
		"OVH_SERVICE_NAME":       config.OvhServiceName,
	}

	// Add Pulumi backend environment variables to OS environment
	for key, value := range pulumiBackendEnvVars {
		if original, exists := os.LookupEnv(key); exists {
			originalEnv[key] = original
		}
		os.Setenv(key, value)
		envVars[key] = value // Track for cleanup
	}

	// Save original values and set new ones
	for key, value := range envVars {
		if original, exists := os.LookupEnv(key); exists {
			if _, alreadyTracked := originalEnv[key]; !alreadyTracked {
				originalEnv[key] = original
			}
		}
		os.Setenv(key, value)
	}

	// Create cleanup function to restore original env vars after Pulumi operations complete
	// This must be called by the caller (ExecuteRetry) after their operations finish
	cleanup := func() {
		for key, value := range originalEnv {
			os.Setenv(key, value)
		}
		for key := range envVars {
			if _, wasSet := originalEnv[key]; !wasSet {
				os.Unsetenv(key)
			}
		}
	}

	// Get existing stack (should exist from previous run)
	// getOrCreateStack will create it if it doesn't exist
	pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Selecting existing Pulumi stack '%s'...", config.StackName))
	var stack auto.Stack
	var err error
	if pe.useInlineProgram {
		stack, err = pe.getOrCreateStackInline(ctx, config.StackName, jobDir, jobID, config)
	} else {
		stack, err = pe.getOrCreateStack(ctx, config.StackName, jobDir, jobID, config)
	}
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
		EnvVars: envVars,
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

	// CRITICAL FIX: Ensure Pulumi.yaml exists even if directory exists
	// After cleanupJobDirectory(jobID, true), .pulumi is preserved but Pulumi.yaml is removed
	// Pulumi requires Pulumi.yaml to select stacks, so we must regenerate it
	pulumiYamlPath := filepath.Join(jobDir, "Pulumi.yaml")
	if _, err := os.Stat(pulumiYamlPath); os.IsNotExist(err) {
		pe.jobManager.AppendOutput(jobID, "Pulumi.yaml not found, regenerating it...")
		pulumiYaml := pe.generatePulumiYaml(config)
		if err := os.WriteFile(pulumiYamlPath, []byte(pulumiYaml), 0644); err != nil {
			pe.jobManager.SetError(jobID, fmt.Errorf("failed to write Pulumi.yaml: %w", err))
			return nil, err
		}
		pe.jobManager.AppendOutput(jobID, "Pulumi.yaml regenerated successfully")
	}

	// Ensure source files exist (needed for pulumi destroy to know what to destroy)
	// Check if main.go exists as indicator of source files
	mainGoPath := filepath.Join(jobDir, "main.go")
	if _, err := os.Stat(mainGoPath); os.IsNotExist(err) {
		pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Source files not found, regenerating from templates in %s...", pe.templatesDir))
		if err := pe.generateSourceFiles(jobDir, config); err != nil {
			pe.jobManager.SetError(jobID, fmt.Errorf("failed to generate source files: %w", err))
			return nil, err
		}
		pe.jobManager.AppendOutput(jobID, "Source files regenerated successfully")

		// Pre-download Go modules if we just regenerated source files
		pe.jobManager.AppendOutput(jobID, "Pre-downloading Go modules...")
		if err := pe.downloadGoModules(jobDir, jobID); err != nil {
			pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Warning: failed to pre-download Go modules: %v. Pulumi will download them during compilation.", err))
			// Don't fail - Pulumi can still download modules during compilation
		} else {
			pe.jobManager.AppendOutput(jobID, "Go modules downloaded successfully")
		}
	}

	// Set up environment variables - both OS env vars and Pulumi Automation API env vars
	// Pulumi CLI reads from OS environment, so we need to set these as OS env vars too
	originalEnv := make(map[string]string)

	// Get Pulumi backend environment variables first
	pulumiBackendEnvVars := getLocalBackendEnvVars()

	// Combine OVH credentials and Pulumi backend env vars
	osEnvVars := map[string]string{
		"OVH_APPLICATION_KEY":    config.OvhApplicationKey,
		"OVH_APPLICATION_SECRET": config.OvhApplicationSecret,
		"OVH_CONSUMER_KEY":       config.OvhConsumerKey,
		"OVH_SERVICE_NAME":       config.OvhServiceName,
	}

	// Add Pulumi backend environment variables to OS environment
	for key, value := range pulumiBackendEnvVars {
		if original, exists := os.LookupEnv(key); exists {
			originalEnv[key] = original
		}
		os.Setenv(key, value)
		osEnvVars[key] = value // Track for cleanup
	}

	// Set OVH credentials as OS environment variables
	for key, value := range osEnvVars {
		if original, exists := os.LookupEnv(key); exists {
			if _, alreadyTracked := originalEnv[key]; !alreadyTracked {
				originalEnv[key] = original
			}
		}
		os.Setenv(key, value)
	}

	// Create cleanup function to restore original env vars after Pulumi operations complete
	// This must be called by the caller (Destroy) after their operations finish
	cleanup := func() {
		for key, value := range originalEnv {
			os.Setenv(key, value)
		}
		for key := range osEnvVars {
			if _, wasSet := originalEnv[key]; !wasSet {
				os.Unsetenv(key)
			}
		}
	}

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

	// Try to select the stack (don't create if it doesn't exist)
	pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Selecting Pulumi stack '%s'...", stackName))
	stack, err := auto.SelectStackLocalSource(ctx, stackName, jobDir,
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
			// Can't verify, assume stack doesn't exist
			pe.jobManager.AppendOutput(jobID, "Cannot verify stack existence. Assuming stack does not exist.")
			pe.jobManager.UpdateJobStatus(jobID, JobStatusDestroyed)
			pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Destroy completed at %s (stack did not exist or state is missing)", time.Now().Format(time.RFC3339)))
			cleanup() // Clean up env vars before returning
			return nil, nil
		}

		stacks, listErr := workspace.ListStacks(ctx)
		if listErr != nil {
			pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Warning: failed to list stacks: %v", listErr))
			pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Diagnostic: Pulumi.yaml exists: %v", func() bool {
				_, err := os.Stat(filepath.Join(jobDir, "Pulumi.yaml"))
				return err == nil
			}()))
			pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Diagnostic: .pulumi directory exists: %v", func() bool {
				_, err := os.Stat(pulumiDir)
				return err == nil
			}()))
			// Can't list stacks, assume stack doesn't exist
			pe.jobManager.AppendOutput(jobID, "Cannot list stacks. Assuming stack does not exist.")
			pe.jobManager.UpdateJobStatus(jobID, JobStatusDestroyed)
			pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Destroy completed at %s (stack did not exist or state is missing)", time.Now().Format(time.RFC3339)))
			cleanup() // Clean up env vars before returning
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
			// Stack doesn't exist in the list - it's truly gone
			pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Stack '%s' not found in workspace. It may have been already destroyed.", stackName))
			pe.jobManager.UpdateJobStatus(jobID, JobStatusDestroyed)
			pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Destroy completed at %s (stack did not exist)", time.Now().Format(time.RFC3339)))
			cleanup() // Clean up env vars before returning
			return nil, nil
		}

		// Stack exists in list but selection failed - retry selection
		pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Stack '%s' exists in workspace but selection failed. Retrying selection...", stackName))
		stack, err = auto.SelectStackLocalSource(ctx, stackName, jobDir,
			auto.WorkDir(jobDir),
			auto.EnvVars(pulumiEnvVars))
		if err != nil {
			// Retry also failed - provide detailed error message with diagnostics
			pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Error: retry also failed to select stack '%s': %v", stackName, err))
			pe.jobManager.AppendOutput(jobID, "This may occur if:")
			pe.jobManager.AppendOutput(jobID, "  1. The stack state is corrupted")
			pe.jobManager.AppendOutput(jobID, "  2. The stack name doesn't match exactly")
			pe.jobManager.AppendOutput(jobID, "  3. There are permission issues accessing the stack state")
			pe.jobManager.AppendOutput(jobID, "  4. Pulumi.yaml is missing or has incorrect project name")

			// Diagnostic information
			pulumiYamlPath := filepath.Join(jobDir, "Pulumi.yaml")
			if _, statErr := os.Stat(pulumiYamlPath); os.IsNotExist(statErr) {
				pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Diagnostic: Pulumi.yaml is MISSING at %s", pulumiYamlPath))
				pe.jobManager.AppendOutput(jobID, "This should have been regenerated. This may indicate a bug in the destroy preparation.")
			} else {
				pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Diagnostic: Pulumi.yaml exists at %s", pulumiYamlPath))
			}

			if _, statErr := os.Stat(pulumiDir); os.IsNotExist(statErr) {
				pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Diagnostic: Stack state directory (.pulumi) is MISSING at %s", pulumiDir))
				pe.jobManager.AppendOutput(jobID, "In file backend mode, stack state must be preserved after deployment for destroy operations.")
			} else {
				pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Diagnostic: Stack state directory (.pulumi) EXISTS at %s", pulumiDir))
				pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Diagnostic: Stack '%s' exists in workspace list but selection failed", stackName))
				pe.jobManager.AppendOutput(jobID, "The stack state may be corrupted or there may be a project name mismatch.")
				pe.jobManager.AppendOutput(jobID, "Check that Pulumi.yaml has the correct project name (should be 'easylab').")
			}

			// Return error instead of silently failing - this is a real problem
			pe.jobManager.SetError(jobID, fmt.Errorf("failed to select stack '%s' for destruction: %w", stackName, err))
			cleanup() // Clean up env vars on error
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
		EnvVars: pulumiEnvVars,
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

	return fmt.Sprintf(`name: easylab
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

	// Verify that all required package directories exist and contain .go files
	requiredPackages := []string{"coder", "k8s", "ovh", "utils"}
	for _, pkg := range requiredPackages {
		pkgDir := filepath.Join(jobDir, pkg)
		if _, err := os.Stat(pkgDir); os.IsNotExist(err) {
			return fmt.Errorf("required package directory %s does not exist", pkg)
		}
		// Check if directory contains at least one .go file
		entries, err := os.ReadDir(pkgDir)
		if err != nil {
			return fmt.Errorf("failed to read package directory %s: %w", pkg, err)
		}
		hasGoFile := false
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".go") {
				hasGoFile = true
				break
			}
		}
		if !hasGoFile {
			return fmt.Errorf("package directory %s does not contain any .go files", pkg)
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
// If module cache has been pre-warmed, this function skips the download step and only runs go mod tidy
// Environment variable SKIP_GO_MOD_VERIFY=true can be used to skip verification in production
func (pe *PulumiExecutor) downloadGoModules(jobDir string, jobID string) error {
	// Check if module cache is already pre-warmed
	skipDownload := pe.moduleCacheReady
	if skipDownload {
		pe.jobManager.AppendOutput(jobID, "Module cache is pre-warmed, skipping full download...")
	}

	// Check if go mod verify should be skipped (via environment variable)
	skipVerify := getEnvOrDefault("SKIP_GO_MOD_VERIFY", "false") == "true"
	if skipVerify {
		pe.jobManager.AppendOutput(jobID, "SKIP_GO_MOD_VERIFY is set, skipping go mod verify...")
	}

	// Get all environment variables including PULUMI_HOME and Go cache settings (scoped to job directory)
	envVars := getLocalBackendEnvVars(jobDir)

	// Ensure Go module cache directory exists and is writable
	if gomodcache, ok := envVars["GOMODCACHE"]; ok {
		if err := os.MkdirAll(gomodcache, 0755); err != nil {
			pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Warning: failed to create GOMODCACHE directory %s: %v", gomodcache, err))
		} else {
			pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Go module cache directory: %s", gomodcache))
		}
	}
	if gocache, ok := envVars["GOCACHE"]; ok {
		if err := os.MkdirAll(gocache, 0755); err != nil {
			pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Warning: failed to create GOCACHE directory %s: %v", gocache, err))
		}
	}
	if gopath, ok := envVars["GOPATH"]; ok {
		if err := os.MkdirAll(gopath, 0755); err != nil {
			pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Warning: failed to create GOPATH directory %s: %v", gopath, err))
		}
	}

	// Build environment for the command - start with current environment
	cmdEnv := os.Environ()

	// Add/override with all environment variables from getLocalBackendEnvVars
	// This includes PULUMI_HOME, GOMODCACHE, GOCACHE, GOPATH, etc.
	for key, value := range envVars {
		// Remove existing entry if present
		for i, env := range cmdEnv {
			if strings.HasPrefix(env, key+"=") {
				cmdEnv = append(cmdEnv[:i], cmdEnv[i+1:]...)
				break
			}
		}
		cmdEnv = append(cmdEnv, fmt.Sprintf("%s=%s", key, value))
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

	// Run go mod tidy first to ensure go.mod is correct
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	pe.jobManager.AppendOutput(jobID, "Running go mod tidy to ensure module files are correct...")
	tidyCmd := exec.CommandContext(ctx, "go", "mod", "tidy")
	tidyCmd.Dir = jobDir
	tidyCmd.Env = cmdEnv
	tidyOutput, tidyErr := tidyCmd.CombinedOutput()
	tidyOutputStr := string(tidyOutput)
	if tidyErr != nil {
		pe.jobManager.AppendOutput(jobID, fmt.Sprintf("go mod tidy output: %s", tidyOutputStr))
		pe.jobManager.AppendOutput(jobID, fmt.Sprintf("go mod tidy failed: %v", tidyErr))
		
		// Check if the error indicates package resolution issues - these are fatal
		if strings.Contains(tidyOutputStr, "no required module provides package") ||
			strings.Contains(tidyOutputStr, "cannot find package") ||
			strings.Contains(tidyOutputStr, "package") && strings.Contains(tidyOutputStr, "is not in") {
			return fmt.Errorf("go mod tidy failed with package resolution error: %w\nOutput: %s", tidyErr, tidyOutputStr)
		}
		
		// For other errors, log as warning but continue
		pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Warning: go mod tidy failed with non-fatal error: %v. Continuing with download.", tidyErr))
	} else {
		pe.jobManager.AppendOutput(jobID, "go mod tidy completed successfully")
	}

	// Run go mod download with timeout to download all modules (skip if cache pre-warmed)
	// This downloads the entire module including all subpackages
	if !skipDownload {
		pe.jobManager.AppendOutput(jobID, "Downloading Go modules...")
		cmd := exec.CommandContext(ctx, "go", "mod", "download")
		cmd.Dir = jobDir
		cmd.Env = cmdEnv

		// Capture output for logging
		output, err := cmd.CombinedOutput()
		if err != nil {
			// Log the output even on error for debugging
			pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Go mod download output: %s", string(output)))
			return fmt.Errorf("go mod download failed: %w\nOutput: %s", err, string(output))
		}

		pe.jobManager.AppendOutput(jobID, "Go modules downloaded successfully")

		// Force Go to resolve subpackages by using 'go list' on them
		// This ensures the packages are downloaded and available in the module cache
		// before Pulumi tries to compile. 'go list' will download packages if needed.
		pe.jobManager.AppendOutput(jobID, "Verifying required subpackages are accessible...")
		requiredSubpackages := []string{
			"github.com/ovh/pulumi-ovh/sdk/v2/go/ovh/cloudproject",
		}

		for _, pkg := range requiredSubpackages {
			// Use 'go list' to verify the package exists - this will download it if needed
			// and verify it's accessible with the current go.mod
			listCmd := exec.CommandContext(ctx, "go", "list", "-m", "-f", "{{.Path}}", pkg)
			listCmd.Dir = jobDir
			listCmd.Env = cmdEnv
			if listOutput, listErr := listCmd.CombinedOutput(); listErr != nil {
				outputStr := string(listOutput)
				pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Warning: go list -m failed for %s: %v", pkg, listErr))
				pe.jobManager.AppendOutput(jobID, fmt.Sprintf("go list output: %s", outputStr))
			}

			// Also try 'go list' without -m to verify the package path is valid
			listPkgCmd := exec.CommandContext(ctx, "go", "list", pkg)
			listPkgCmd.Dir = jobDir
			listPkgCmd.Env = cmdEnv
			if pkgOutput, pkgErr := listPkgCmd.CombinedOutput(); pkgErr != nil {
				outputStr := string(pkgOutput)
				pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Warning: go list failed for package %s: %v", pkg, pkgErr))
				pe.jobManager.AppendOutput(jobID, fmt.Sprintf("go list output: %s", outputStr))
				// Check if this is a fatal package resolution error
				if strings.Contains(outputStr, "no required module provides package") {
					return fmt.Errorf("required subpackage %s is not available: %w\nOutput: %s\n\nThis usually means the module github.com/ovh/pulumi-ovh/sdk/v2 is not properly downloaded or the subpackage path is incorrect.", pkg, pkgErr, outputStr)
				}
			} else {
				pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Subpackage %s verified successfully", pkg))
			}
		}
	} else {
		pe.jobManager.AppendOutput(jobID, "Skipping go mod download (using pre-warmed cache)")
	}

	// Skip go mod verify when cache is pre-warmed or explicitly disabled via env var
	if !skipDownload && !skipVerify {
		// Run go mod verify to catch issues early
		pe.jobManager.AppendOutput(jobID, "Verifying Go modules...")
		verifyCmd := exec.CommandContext(ctx, "go", "mod", "verify")
		verifyCmd.Dir = jobDir
		verifyCmd.Env = cmdEnv
		verifyOutput, verifyErr := verifyCmd.CombinedOutput()
		if verifyErr != nil {
			pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Warning: go mod verify failed: %v", verifyErr))
			pe.jobManager.AppendOutput(jobID, fmt.Sprintf("go mod verify output: %s", string(verifyOutput)))
			// Don't fail on verify errors - they're warnings, not critical
		} else {
			pe.jobManager.AppendOutput(jobID, "Go modules verified successfully")
		}
	} else if skipVerify && !skipDownload {
		pe.jobManager.AppendOutput(jobID, "Skipping go mod verify (SKIP_GO_MOD_VERIFY=true)")
	}

	return nil
}
