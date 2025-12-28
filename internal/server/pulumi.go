package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// PulumiExecutor handles Pulumi command execution
type PulumiExecutor struct {
	jobManager  *JobManager
	workDir     string
	projectRoot string
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
	job, exists := pe.jobManager.GetJob(jobID)
	if !exists {
		err := fmt.Errorf("job %s not found", jobID)
		log.Printf("Execute error: %v", err)
		return err
	}

	config := job.Config

	// Update status to running immediately
	pe.jobManager.UpdateJobStatus(jobID, JobStatusRunning)
	pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Job started at %s", time.Now().Format(time.RFC3339)))

	// Create temporary directory for this job
	jobDir := filepath.Join(pe.workDir, jobID)
	pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Creating job directory: %s", jobDir))

	if err := os.MkdirAll(jobDir, 0755); err != nil {
		pe.jobManager.SetError(jobID, fmt.Errorf("failed to create job directory: %w", err))
		return err
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
		return err
	}

	// Write Pulumi stack config
	pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Writing Pulumi stack config for stack '%s'...", config.StackName))
	stackConfig := pe.generateStackConfig(config)
	stackConfigFile := filepath.Join(jobDir, fmt.Sprintf("Pulumi.%s.yaml", config.StackName))
	if err := os.WriteFile(stackConfigFile, []byte(stackConfig), 0644); err != nil {
		pe.jobManager.SetError(jobID, fmt.Errorf("failed to write stack config: %w", err))
		return err
	}

	// Copy main.go and other source files to job directory
	pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Copying source files from %s...", pe.projectRoot))
	if err := pe.copySourceFiles(jobDir); err != nil {
		pe.jobManager.SetError(jobID, fmt.Errorf("failed to copy source files: %w", err))
		return err
	}
	pe.jobManager.AppendOutput(jobID, "Source files copied successfully")

	// Set up environment variables
	env := os.Environ()
	env = append(env, fmt.Sprintf("OVH_APPLICATION_KEY=%s", config.OvhApplicationKey))
	env = append(env, fmt.Sprintf("OVH_APPLICATION_SECRET=%s", config.OvhApplicationSecret))
	env = append(env, fmt.Sprintf("OVH_CONSUMER_KEY=%s", config.OvhConsumerKey))
	env = append(env, fmt.Sprintf("OVH_SERVICE_NAME=%s", config.OvhServiceName))

	// Initialize Pulumi stack
	pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Initializing Pulumi stack '%s'...", config.StackName))
	if err := pe.runCommand(jobID, jobDir, env, "pulumi", "stack", "init", config.StackName, "--non-interactive"); err != nil {
		// Stack might already exist, try to select it
		pe.jobManager.AppendOutput(jobID, "Stack may already exist, trying to select...")
	}

	// Select the stack
	pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Selecting Pulumi stack '%s'...", config.StackName))
	if err := pe.runCommand(jobID, jobDir, env, "pulumi", "stack", "select", config.StackName, "--non-interactive"); err != nil {
		pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Stack select warning: %v", err))
	}

	// Set all config values
	pe.jobManager.AppendOutput(jobID, "Setting Pulumi configuration...")
	configCommands := pe.getConfigCommands(config)
	for _, cmd := range configCommands {
		var err error
		if cmd.secret {
			err = pe.runCommand(jobID, jobDir, env, "pulumi", "config", "set", cmd.key, cmd.value, "--secret", "--non-interactive")
		} else {
			err = pe.runCommand(jobID, jobDir, env, "pulumi", "config", "set", cmd.key, cmd.value, "--non-interactive")
		}
		if err != nil {
			pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Config set %s warning: %v", cmd.key, err))
		}
	}

	// Run pulumi up with streaming output
	pe.jobManager.AppendOutput(jobID, "Running pulumi up --yes --non-interactive...")
	if err := pe.runCommandWithStreaming(jobID, jobDir, env, "pulumi", "up", "--yes", "--non-interactive"); err != nil {
		pe.jobManager.SetError(jobID, fmt.Errorf("pulumi up failed: %w", err))
		return err
	}

	// Extract kubeconfig from stack outputs
	pe.jobManager.AppendOutput(jobID, "Extracting kubeconfig...")
	kubeconfig, err := pe.getStackOutput(jobDir, env, "kubeconfig")
	if err != nil {
		pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Warning: failed to extract kubeconfig: %v", err))
	} else if kubeconfig != "" {
		// Validate that kubeconfig looks like valid YAML (should start with "apiVersion" or contain "kind:")
		if !strings.Contains(kubeconfig, "apiVersion") && !strings.Contains(kubeconfig, "kind:") {
			pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Warning: kubeconfig may be invalid (length: %d chars)", len(kubeconfig)))
		}
		pe.jobManager.SetKubeconfig(jobID, kubeconfig)
		pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Kubeconfig extracted successfully (length: %d chars)", len(kubeconfig)))
	}

	// Extract Coder configuration from stack outputs
	pe.jobManager.AppendOutput(jobID, "Extracting Coder configuration...")
	coderURL, err := pe.getStackOutput(jobDir, env, "coderServerURL")
	if err != nil {
		pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Warning: failed to extract coderServerURL: %v", err))
	} else if coderURL != "" {
		coderSessionToken, _ := pe.getStackOutput(jobDir, env, "coderSessionToken")
		coderOrganizationID, _ := pe.getStackOutput(jobDir, env, "coderOrganizationID")

		// Store Coder config in job
		if err := pe.jobManager.SetCoderConfig(jobID, coderURL, config.CoderAdminEmail, config.CoderAdminPassword, coderSessionToken, coderOrganizationID); err != nil {
			pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Warning: failed to store Coder config: %v", err))
		} else {
			pe.jobManager.AppendOutput(jobID, "Coder configuration extracted and stored successfully")
		}
	}

	// Success
	pe.jobManager.UpdateJobStatus(jobID, JobStatusCompleted)
	pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Deployment completed successfully at %s", time.Now().Format(time.RFC3339)))

	// Persist completed job to disk
	if err := pe.jobManager.SaveJob(jobID); err != nil {
		log.Printf("Warning: failed to persist job %s: %v", jobID, err)
		// Don't fail the job if persistence fails
	}

	return nil
}

// Preview runs pulumi preview for a given job (dry run)
func (pe *PulumiExecutor) Preview(jobID string) error {
	job, exists := pe.jobManager.GetJob(jobID)
	if !exists {
		err := fmt.Errorf("job %s not found", jobID)
		log.Printf("Preview error: %v", err)
		return err
	}

	config := job.Config

	// Update status to running immediately
	pe.jobManager.UpdateJobStatus(jobID, JobStatusRunning)
	pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Dry run started at %s", time.Now().Format(time.RFC3339)))

	// Create temporary directory for this job
	jobDir := filepath.Join(pe.workDir, jobID)
	pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Creating job directory: %s", jobDir))

	if err := os.MkdirAll(jobDir, 0755); err != nil {
		pe.jobManager.SetError(jobID, fmt.Errorf("failed to create job directory: %w", err))
		return err
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
		return err
	}

	// Write Pulumi stack config
	pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Writing Pulumi stack config for stack '%s'...", config.StackName))
	stackConfig := pe.generateStackConfig(config)
	stackConfigFile := filepath.Join(jobDir, fmt.Sprintf("Pulumi.%s.yaml", config.StackName))
	if err := os.WriteFile(stackConfigFile, []byte(stackConfig), 0644); err != nil {
		pe.jobManager.SetError(jobID, fmt.Errorf("failed to write stack config: %w", err))
		return err
	}

	// Copy main.go and other source files to job directory
	pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Copying source files from %s...", pe.projectRoot))
	if err := pe.copySourceFiles(jobDir); err != nil {
		pe.jobManager.SetError(jobID, fmt.Errorf("failed to copy source files: %w", err))
		return err
	}
	pe.jobManager.AppendOutput(jobID, "Source files copied successfully")

	// Set up environment variables
	env := os.Environ()
	env = append(env, fmt.Sprintf("OVH_APPLICATION_KEY=%s", config.OvhApplicationKey))
	env = append(env, fmt.Sprintf("OVH_APPLICATION_SECRET=%s", config.OvhApplicationSecret))
	env = append(env, fmt.Sprintf("OVH_CONSUMER_KEY=%s", config.OvhConsumerKey))
	env = append(env, fmt.Sprintf("OVH_SERVICE_NAME=%s", config.OvhServiceName))

	// Initialize Pulumi stack
	pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Initializing Pulumi stack '%s'...", config.StackName))
	if err := pe.runCommand(jobID, jobDir, env, "pulumi", "stack", "init", config.StackName, "--non-interactive"); err != nil {
		// Stack might already exist, try to select it
		pe.jobManager.AppendOutput(jobID, "Stack may already exist, trying to select...")
	}

	// Select the stack
	pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Selecting Pulumi stack '%s'...", config.StackName))
	if err := pe.runCommand(jobID, jobDir, env, "pulumi", "stack", "select", config.StackName, "--non-interactive"); err != nil {
		pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Stack select warning: %v", err))
	}

	// Set all config values
	pe.jobManager.AppendOutput(jobID, "Setting Pulumi configuration...")
	configCommands := pe.getConfigCommands(config)
	for _, cmd := range configCommands {
		var err error
		if cmd.secret {
			err = pe.runCommand(jobID, jobDir, env, "pulumi", "config", "set", cmd.key, cmd.value, "--secret", "--non-interactive")
		} else {
			err = pe.runCommand(jobID, jobDir, env, "pulumi", "config", "set", cmd.key, cmd.value, "--non-interactive")
		}
		if err != nil {
			pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Config set %s warning: %v", cmd.key, err))
		}
	}

	// Run pulumi preview with streaming output
	pe.jobManager.AppendOutput(jobID, "Running pulumi preview (dry run)...")
	if err := pe.runCommandWithStreaming(jobID, jobDir, env, "pulumi", "preview", "--non-interactive"); err != nil {
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
	job, exists := pe.jobManager.GetJob(jobID)
	if !exists {
		err := fmt.Errorf("job %s not found", jobID)
		log.Printf("Destroy error: %v", err)
		return err
	}

	job.mu.RLock()
	config := job.Config
	stackName := ""
	if config != nil {
		stackName = config.StackName
	}
	job.mu.RUnlock()

	if stackName == "" {
		err := fmt.Errorf("job %s has no stack name", jobID)
		log.Printf("Destroy error: %v", err)
		return err
	}

	// Update status to running immediately
	pe.jobManager.UpdateJobStatus(jobID, JobStatusRunning)
	pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Destroy started at %s", time.Now().Format(time.RFC3339)))

	// Find job directory
	jobDir := filepath.Join(pe.workDir, jobID)
	if _, err := os.Stat(jobDir); os.IsNotExist(err) {
		pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Job directory not found: %s. Creating it...", jobDir))
		if err := os.MkdirAll(jobDir, 0755); err != nil {
			pe.jobManager.SetError(jobID, fmt.Errorf("failed to create job directory: %w", err))
			return err
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
			return err
		}

		// Write Pulumi stack config
		stackConfig := pe.generateStackConfig(config)
		stackConfigFile := filepath.Join(jobDir, fmt.Sprintf("Pulumi.%s.yaml", stackName))
		if err := os.WriteFile(stackConfigFile, []byte(stackConfig), 0644); err != nil {
			pe.jobManager.SetError(jobID, fmt.Errorf("failed to write stack config: %w", err))
			return err
		}

		// Copy source files (needed for pulumi destroy to know what to destroy)
		pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Copying source files from %s...", pe.projectRoot))
		if err := pe.copySourceFiles(jobDir); err != nil {
			pe.jobManager.SetError(jobID, fmt.Errorf("failed to copy source files: %w", err))
			return err
		}
		pe.jobManager.AppendOutput(jobID, "Source files copied successfully")
	}

	// Set up environment variables
	env := os.Environ()
	env = append(env, fmt.Sprintf("OVH_APPLICATION_KEY=%s", config.OvhApplicationKey))
	env = append(env, fmt.Sprintf("OVH_APPLICATION_SECRET=%s", config.OvhApplicationSecret))
	env = append(env, fmt.Sprintf("OVH_CONSUMER_KEY=%s", config.OvhConsumerKey))
	env = append(env, fmt.Sprintf("OVH_SERVICE_NAME=%s", config.OvhServiceName))

	// Select the stack first
	pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Selecting Pulumi stack '%s'...", stackName))
	if err := pe.runCommand(jobID, jobDir, env, "pulumi", "stack", "select", stackName, "--non-interactive"); err != nil {
		pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Warning: failed to select stack: %v. Stack may not exist.", err))
		// Continue anyway - stack might not exist
	}

	// Run pulumi destroy with streaming output
	pe.jobManager.AppendOutput(jobID, "Running pulumi destroy --yes --non-interactive...")
	if err := pe.runCommandWithStreaming(jobID, jobDir, env, "pulumi", "destroy", "--yes", "--non-interactive"); err != nil {
		pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Warning: pulumi destroy failed: %v. Continuing with stack removal...", err))
		// Continue with stack removal even if destroy failed
	}

	// Remove the stack
	pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Removing Pulumi stack '%s'...", stackName))
	if err := pe.runCommand(jobID, jobDir, env, "pulumi", "stack", "rm", stackName, "--yes", "--non-interactive"); err != nil {
		pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Warning: failed to remove stack: %v", err))
		// Don't fail the entire operation if stack removal fails
	} else {
		pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Stack '%s' removed successfully", stackName))
	}

	// Success
	pe.jobManager.UpdateJobStatus(jobID, JobStatusCompleted)
	pe.jobManager.AppendOutput(jobID, fmt.Sprintf("Destroy completed at %s", time.Now().Format(time.RFC3339)))

	return nil
}

// runCommand runs a command and captures output
func (pe *PulumiExecutor) runCommand(jobID, dir string, env []string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = env

	output, err := cmd.CombinedOutput()
	if len(output) > 0 {
		pe.jobManager.AppendOutput(jobID, string(output))
	}
	return err
}

// runCommandWithStreaming runs a command and streams output in real-time
func (pe *PulumiExecutor) runCommandWithStreaming(jobID, dir string, env []string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = env

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("failed to create stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start command: %w", err)
	}

	// Stream output in goroutines
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		pe.streamOutput(jobID, stdout)
	}()

	go func() {
		defer wg.Done()
		pe.streamOutput(jobID, stderr)
	}()

	// Wait for output to be consumed
	wg.Wait()

	// Wait for command to complete
	return cmd.Wait()
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

	return commands
}

func (pe *PulumiExecutor) generateStackConfig(config *LabConfig) string {
	// Prefix resource names with stack name
	prefixedGatewayName := fmt.Sprintf("%s-%s", config.StackName, config.NetworkGatewayName)
	prefixedPrivateNetworkName := fmt.Sprintf("%s-%s", config.StackName, config.NetworkPrivateNetworkName)
	prefixedK8sClusterName := fmt.Sprintf("%s-%s", config.StackName, config.K8sClusterName)
	prefixedNodePoolName := fmt.Sprintf("%s-%s", config.StackName, config.NodePoolName)

	var sb strings.Builder
	sb.WriteString("config:\n")
	sb.WriteString(fmt.Sprintf("  network:gatewayName: %q\n", prefixedGatewayName))
	sb.WriteString(fmt.Sprintf("  network:gatewayModel: %q\n", config.NetworkGatewayModel))
	sb.WriteString(fmt.Sprintf("  network:privateNetworkName: %q\n", prefixedPrivateNetworkName))
	sb.WriteString(fmt.Sprintf("  network:region: %q\n", config.NetworkRegion))
	sb.WriteString(fmt.Sprintf("  network:networkMask: %q\n", config.NetworkMask))
	sb.WriteString(fmt.Sprintf("  network:networkStartIp: %q\n", config.NetworkStartIP))
	sb.WriteString(fmt.Sprintf("  network:networkEndIp: %q\n", config.NetworkEndIP))
	if config.NetworkID != "" {
		sb.WriteString(fmt.Sprintf("  network:networkId: %q\n", config.NetworkID))
	}
	sb.WriteString(fmt.Sprintf("  nodepool:name: %q\n", prefixedNodePoolName))
	sb.WriteString(fmt.Sprintf("  nodepool:flavor: %q\n", config.NodePoolFlavor))
	sb.WriteString(fmt.Sprintf("  nodepool:desiredNodeCount: %d\n", config.NodePoolDesiredNodeCount))
	sb.WriteString(fmt.Sprintf("  nodepool:minNodeCount: %d\n", config.NodePoolMinNodeCount))
	sb.WriteString(fmt.Sprintf("  nodepool:maxNodeCount: %d\n", config.NodePoolMaxNodeCount))
	sb.WriteString(fmt.Sprintf("  k8s:clusterName: %q\n", prefixedK8sClusterName))
	sb.WriteString(fmt.Sprintf("  coder:adminEmail: %q\n", config.CoderAdminEmail))
	sb.WriteString(fmt.Sprintf("  coder:adminPassword: %q\n", config.CoderAdminPassword))
	sb.WriteString(fmt.Sprintf("  coder:version: %q\n", config.CoderVersion))
	sb.WriteString(fmt.Sprintf("  coder:dbUser: %q\n", config.CoderDbUser))
	sb.WriteString(fmt.Sprintf("  coder:dbPassword: %q\n", config.CoderDbPassword))
	sb.WriteString(fmt.Sprintf("  coder:dbName: %q\n", config.CoderDbName))
	sb.WriteString(fmt.Sprintf("  coder:templateName: %q\n", config.CoderTemplateName))
	return sb.String()
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

func (pe *PulumiExecutor) streamOutput(jobID string, r io.ReadCloser) {
	defer r.Close()
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		pe.jobManager.AppendOutput(jobID, line)
	}
}

// getStackOutput retrieves a specific output from the Pulumi stack
func (pe *PulumiExecutor) getStackOutput(dir string, env []string, outputName string) (string, error) {
	// Use --json flag to get structured output, which handles multiline strings better
	cmd := exec.Command("pulumi", "stack", "output", "--json", "--show-secrets", "--non-interactive")
	cmd.Dir = dir
	cmd.Env = env

	output, err := cmd.Output()
	if err != nil {
		// Fallback to single output if JSON fails
		cmd = exec.Command("pulumi", "stack", "output", outputName, "--non-interactive", "--show-secrets")
		cmd.Dir = dir
		cmd.Env = env
		output, err = cmd.Output()
		if err != nil {
			return "", fmt.Errorf("failed to get stack output %s: %w", outputName, err)
		}
		result := strings.TrimSpace(string(output))
		// Try JSON decode if it looks like JSON string
		if len(result) > 0 && result[0] == '"' && result[len(result)-1] == '"' {
			var decoded string
			if err := json.Unmarshal([]byte(result), &decoded); err == nil {
				result = decoded
			}
		}
		return result, nil
	}

	// Parse JSON output
	var outputs map[string]interface{}
	if err := json.Unmarshal(output, &outputs); err != nil {
		return "", fmt.Errorf("failed to parse stack outputs JSON: %w", err)
	}

	value, ok := outputs[outputName]
	if !ok {
		return "", fmt.Errorf("output %s not found", outputName)
	}

	// Convert value to string, handling different types
	var result string
	switch v := value.(type) {
	case string:
		result = v
	case []byte:
		result = string(v)
	default:
		// For other types, marshal to JSON and then unmarshal as string
		jsonBytes, err := json.Marshal(v)
		if err != nil {
			return "", fmt.Errorf("failed to marshal output %s: %w", outputName, err)
		}
		// Unmarshal as string to handle JSON-encoded strings
		if err := json.Unmarshal(jsonBytes, &result); err != nil {
			result = string(jsonBytes)
		}
	}

	return result, nil
}
