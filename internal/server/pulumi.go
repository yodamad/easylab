package server

import (
	"bufio"
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
func findProjectRoot() string {
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
		pe.jobManager.SetKubeconfig(jobID, kubeconfig)
		pe.jobManager.AppendOutput(jobID, "Kubeconfig extracted successfully")
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
	commands := []configCommand{
		{"network:gatewayName", config.NetworkGatewayName, false},
		{"network:gatewayModel", config.NetworkGatewayModel, false},
		{"network:privateNetworkName", config.NetworkPrivateNetworkName, false},
		{"network:region", config.NetworkRegion, false},
		{"network:networkMask", config.NetworkMask, false},
		{"network:networkStartIp", config.NetworkStartIP, false},
		{"network:networkEndIp", config.NetworkEndIP, false},
		{"nodepool:name", config.NodePoolName, false},
		{"nodepool:flavor", config.NodePoolFlavor, false},
		{"nodepool:desiredNodeCount", fmt.Sprintf("%d", config.NodePoolDesiredNodeCount), false},
		{"nodepool:minNodeCount", fmt.Sprintf("%d", config.NodePoolMinNodeCount), false},
		{"nodepool:maxNodeCount", fmt.Sprintf("%d", config.NodePoolMaxNodeCount), false},
		{"k8s:clusterName", config.K8sClusterName, false},
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
	var sb strings.Builder
	sb.WriteString("config:\n")
	sb.WriteString(fmt.Sprintf("  network:gatewayName: %q\n", config.NetworkGatewayName))
	sb.WriteString(fmt.Sprintf("  network:gatewayModel: %q\n", config.NetworkGatewayModel))
	sb.WriteString(fmt.Sprintf("  network:privateNetworkName: %q\n", config.NetworkPrivateNetworkName))
	sb.WriteString(fmt.Sprintf("  network:region: %q\n", config.NetworkRegion))
	sb.WriteString(fmt.Sprintf("  network:networkMask: %q\n", config.NetworkMask))
	sb.WriteString(fmt.Sprintf("  network:networkStartIp: %q\n", config.NetworkStartIP))
	sb.WriteString(fmt.Sprintf("  network:networkEndIp: %q\n", config.NetworkEndIP))
	if config.NetworkID != "" {
		sb.WriteString(fmt.Sprintf("  network:networkId: %q\n", config.NetworkID))
	}
	sb.WriteString(fmt.Sprintf("  nodepool:name: %q\n", config.NodePoolName))
	sb.WriteString(fmt.Sprintf("  nodepool:flavor: %q\n", config.NodePoolFlavor))
	sb.WriteString(fmt.Sprintf("  nodepool:desiredNodeCount: %d\n", config.NodePoolDesiredNodeCount))
	sb.WriteString(fmt.Sprintf("  nodepool:minNodeCount: %d\n", config.NodePoolMinNodeCount))
	sb.WriteString(fmt.Sprintf("  nodepool:maxNodeCount: %d\n", config.NodePoolMaxNodeCount))
	sb.WriteString(fmt.Sprintf("  k8s:clusterName: %q\n", config.K8sClusterName))
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
	cmd := exec.Command("pulumi", "stack", "output", outputName, "--non-interactive")
	cmd.Dir = dir
	cmd.Env = env

	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get stack output %s: %w", outputName, err)
	}

	return strings.TrimSpace(string(output)), nil
}
