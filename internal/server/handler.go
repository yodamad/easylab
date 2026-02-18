package server

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"easylab/coder"
	"easylab/utils"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Handler handles HTTP requests
type Handler struct {
	jobManager         *JobManager
	pulumiExec         *PulumiExecutor
	templates          map[string]*template.Template
	templatesMu        sync.RWMutex
	credentialsManager *CredentialsManager
}

// emailRegex is a simple email validation regex
var emailRegex = regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$`)

// validateEmail validates an email address format
func validateEmail(email string) bool {
	if email == "" {
		return false
	}
	return emailRegex.MatchString(email)
}

// validateURL validates a URL format
func validateURL(urlStr string) bool {
	if urlStr == "" {
		return false
	}
	u, err := url.Parse(urlStr)
	return err == nil && u.Scheme != "" && u.Host != ""
}

// getFormValue gets a form value, trying PostFormValue first, then FormValue
func getFormValue(r *http.Request, key string) string {
	value := r.PostFormValue(key)
	if value == "" {
		value = r.FormValue(key)
	}
	return value
}

// NewHandler creates a new HTTP handler
func NewHandler(jobManager *JobManager, pulumiExec *PulumiExecutor, credentialsManager *CredentialsManager) *Handler {
	return &Handler{
		jobManager:         jobManager,
		pulumiExec:         pulumiExec,
		templates:          make(map[string]*template.Template),
		credentialsManager: credentialsManager,
	}
}

// deriveEncryptionKey derives a 32-byte AES-256 key from email and student password
func deriveEncryptionKey(email, studentPassword string) []byte {
	// Combine email and password
	combined := email + ":" + studentPassword
	// Hash with SHA-256 to get 32 bytes (AES-256 key size)
	hash := sha256.Sum256([]byte(combined))
	return hash[:]
}

// encryptWorkspacePassword encrypts the workspace password using AES-256-GCM
// Returns base64-encoded ciphertext with nonce prepended
func encryptWorkspacePassword(plaintext, email, studentPassword string) (string, error) {
	// Derive encryption key
	key := deriveEncryptionKey(email, studentPassword)

	// Create AES cipher
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("failed to create cipher: %w", err)
	}

	// Create GCM mode
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("failed to create GCM: %w", err)
	}

	// Generate random nonce
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("failed to generate nonce: %w", err)
	}

	// Encrypt and authenticate
	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)

	// Encode to base64 for storage
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// parseForm handles both multipart and urlencoded form data parsing
func (h *Handler) parseForm(w http.ResponseWriter, r *http.Request, maxSize int64) error {
	if maxSize == 0 {
		maxSize = 50 << 20 // 50MB default (increased for template file uploads)
	}

	contentType := r.Header.Get("Content-Type")
	log.Printf("Content-Type: %s", contentType)

	// Handle multipart/form-data
	if strings.HasPrefix(contentType, "multipart/form-data") {
		if err := r.ParseMultipartForm(maxSize); err != nil {
			log.Printf("Failed to parse multipart form: %v", err)
			return err
		}
		log.Printf("Form parsed successfully (multipart)")
		return nil
	}

	// Handle application/x-www-form-urlencoded or missing Content-Type
	// ParseForm works for both urlencoded forms and can handle missing Content-Type
	// by detecting the form data in the request body
	if err := r.ParseForm(); err != nil {
		log.Printf("Failed to parse form: %v", err)
		return err
	}
	log.Printf("Form parsed successfully (urlencoded or auto-detected)")
	return nil
}

// getOVHCredentials retrieves OVH credentials and returns an HTML error if not configured (backward compatibility)
func (h *Handler) getOVHCredentials(w http.ResponseWriter) (*OVHCredentials, error) {
	creds, err := h.credentialsManager.GetCredentials("ovh")
	if err != nil {
		log.Printf("OVH credentials not configured: %v", err)
		h.renderHTMLError(w, "OVH Credentials Not Configured", "Please configure your OVH credentials first.", `<a href="/credentials?provider=ovh" class="btn btn-primary">Configure OVH Credentials</a>`)
		return nil, err
	}
	return creds.(*OVHCredentials), nil
}

// getProviderCredentials retrieves provider credentials based on provider name
func (h *Handler) getProviderCredentials(w http.ResponseWriter, providerName string) (ProviderCredentials, error) {
	if providerName == "" {
		providerName = "ovh" // Default to OVH for backward compatibility
	}

	creds, err := h.credentialsManager.GetCredentials(providerName)
	if err != nil {
		log.Printf("%s credentials not configured: %v", providerName, err)
		h.renderHTMLError(w, fmt.Sprintf("%s Credentials Not Configured", strings.ToUpper(providerName)),
			fmt.Sprintf("Please configure your %s credentials first.", strings.ToUpper(providerName)),
			fmt.Sprintf(`<a href="/credentials?provider=%s" class="btn btn-primary">Configure Credentials</a>`, providerName))
		return nil, err
	}
	return creds, nil
}

// saveUploadedTemplateFile handles template file upload and saves it to the job directory
// Returns the file path relative to job directory, or empty string if no file was uploaded
func (h *Handler) saveUploadedTemplateFile(r *http.Request, jobDir string) (string, error) {
	// Check if template file was uploaded
	file, header, err := r.FormFile("template_file")
	if err != nil {
		if err == http.ErrMissingFile {
			// No file uploaded, this is optional
			return "", nil
		}
		return "", fmt.Errorf("failed to get uploaded file: %w", err)
	}
	defer file.Close()

	// Validate file extension
	filename := header.Filename
	if !strings.HasSuffix(strings.ToLower(filename), ".zip") && !strings.HasSuffix(strings.ToLower(filename), ".tf") {
		return "", fmt.Errorf("invalid file type: only .zip and .tf files are allowed")
	}

	// Create job directory if it doesn't exist
	if err := os.MkdirAll(jobDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create job directory: %w", err)
	}

	// Determine destination file path
	var destPath string
	var finalPath string
	if strings.HasSuffix(strings.ToLower(filename), ".tf") {
		// Save .tf file first, will be zipped later
		destPath = filepath.Join(jobDir, "template.tf")
		finalPath = "template.zip" // Will be created from .tf file
	} else {
		// Save .zip file directly
		destPath = filepath.Join(jobDir, "template.zip")
		finalPath = "template.zip"
	}

	// Create destination file
	destFile, err := os.Create(destPath)
	if err != nil {
		return "", fmt.Errorf("failed to create destination file: %w", err)
	}
	defer destFile.Close()

	// Copy uploaded file to destination
	_, err = io.Copy(destFile, file)
	if err != nil {
		return "", fmt.Errorf("failed to save uploaded file: %w", err)
	}
	destFile.Close()

	// If it's a .tf file, zip it
	if strings.HasSuffix(strings.ToLower(filename), ".tf") {
		zipPath, err := utils.ZipTerraformFile(destPath)
		if err != nil {
			return "", fmt.Errorf("failed to zip terraform file: %w", err)
		}
		// Remove the original .tf file
		os.Remove(destPath)
		// Verify zip file was created in job directory
		if !strings.HasPrefix(zipPath, jobDir) {
			// Zip was created elsewhere, move it to job directory
			finalZipPath := filepath.Join(jobDir, "template.zip")
			if err := os.Rename(zipPath, finalZipPath); err != nil {
				return "", fmt.Errorf("failed to move zip file to job directory: %w", err)
			}
		}
		return finalPath, nil
	}

	return finalPath, nil
}

// createLabConfigFromForm creates a LabConfig from form data and provider credentials
func (h *Handler) createLabConfigFromForm(r *http.Request, providerCreds ProviderCredentials, templateFilePath string) *LabConfig {
	// Parse integer fields
	desiredNodeCount, _ := strconv.Atoi(r.FormValue("nodepool_desired_node_count"))
	minNodeCount, _ := strconv.Atoi(r.FormValue("nodepool_min_node_count"))
	maxNodeCount, _ := strconv.Atoi(r.FormValue("nodepool_max_node_count"))

	// Get provider from form (default to "ovh" for backward compatibility)
	provider := r.FormValue("provider")
	if provider == "" {
		provider = "ovh"
	}

	// Get stack name with default
	stackName := r.FormValue("stack_name")
	if stackName == "" {
		stackName = "dev"
	}

	config := &LabConfig{
		StackName: stackName,
		Provider:  provider,

		NetworkGatewayName:        r.FormValue("network_gateway_name"),
		NetworkGatewayModel:       r.FormValue("network_gateway_model"),
		NetworkPrivateNetworkName: r.FormValue("network_private_network_name"),
		NetworkRegion:             r.FormValue("network_region"),
		NetworkMask:               r.FormValue("network_mask"),
		NetworkStartIP:            r.FormValue("network_start_ip"),
		NetworkEndIP:              r.FormValue("network_end_ip"),
		NetworkID:                 r.FormValue("network_id"),

		K8sClusterName: r.FormValue("k8s_cluster_name"),

		NodePoolName:             r.FormValue("nodepool_name"),
		NodePoolFlavor:           r.FormValue("nodepool_flavor"),
		NodePoolDesiredNodeCount: desiredNodeCount,
		NodePoolMinNodeCount:     minNodeCount,
		NodePoolMaxNodeCount:     maxNodeCount,

		CoderAdminEmail:    r.FormValue("coder_admin_email"),
		CoderAdminPassword: r.FormValue("coder_admin_password"),
		CoderVersion:       r.FormValue("coder_version"),
		CoderDbUser:        r.FormValue("coder_db_user"),
		CoderDbPassword:    r.FormValue("coder_db_password"),
		CoderDbName:        r.FormValue("coder_db_name"),
		CoderTemplateName:  r.FormValue("coder_template_name"),
		TemplateFilePath:   templateFilePath,

		// Template Source Configuration
		TemplateSource:    r.FormValue("template_source"),
		TemplateGitRepo:   r.FormValue("template_git_repo"),
		TemplateGitFolder: r.FormValue("template_git_folder"),
		TemplateGitBranch: r.FormValue("template_git_branch"),
	}

	// Validate email if provided
	if config.CoderAdminEmail != "" && !validateEmail(config.CoderAdminEmail) {
		// Will be validated when used, but log warning
		log.Printf("Warning: Invalid email format in CoderAdminEmail: %s", config.CoderAdminEmail)
	}

	// Validate Git repository URL if provided
	if config.TemplateSource == "git" && config.TemplateGitRepo != "" {
		if !validateURL(config.TemplateGitRepo) {
			log.Printf("Warning: Invalid Git repository URL format: %s", config.TemplateGitRepo)
		}
	}

	// Add provider-specific credentials
	if ovhCreds, ok := providerCreds.(*OVHCredentials); ok {
		config.OvhApplicationKey = ovhCreds.ApplicationKey
		config.OvhApplicationSecret = ovhCreds.ApplicationSecret
		config.OvhConsumerKey = ovhCreds.ConsumerKey
		config.OvhServiceName = ovhCreds.ServiceName
		config.OvhEndpoint = ovhCreds.Endpoint
	}
	// Future providers can be added here:
	// if awsCreds, ok := providerCreds.(*AWSCredentials); ok {
	//     config.AwsAccessKeyId = awsCreds.AccessKeyId
	//     ...
	// }

	return config
}

// executeLabJob creates a job and starts execution, returning the job ID and HTML response
func (h *Handler) executeLabJob(config *LabConfig, isDryRun bool) (string, string) {
	// Create job
	jobID := h.jobManager.CreateJob(config)
	return h.executeLabJobWithID(config, isDryRun, jobID)
}

// executeLabJobWithID starts execution for an existing job, returning the job ID and HTML response
func (h *Handler) executeLabJobWithID(config *LabConfig, isDryRun bool, jobID string) (string, string) {
	if isDryRun {
		log.Printf("Dry run job started: %s", jobID)
	} else {
		log.Printf("Job started: %s", jobID)
	}

	// Start execution in a goroutine
	go func() {
		if isDryRun {
			log.Printf("Starting Pulumi preview for job: %s", jobID)
			if err := h.pulumiExec.Preview(jobID); err != nil {
				log.Printf("Pulumi preview failed for job %s: %v", jobID, err)
				return
			}
			log.Printf("Pulumi preview completed for job: %s", jobID)
		} else {
			log.Printf("Starting Pulumi execution for job: %s", jobID)
			if err := h.pulumiExec.Execute(jobID); err != nil {
				log.Printf("Pulumi execution failed for job %s: %v", jobID, err)
				return
			}
			log.Printf("Pulumi execution completed for job: %s", jobID)
		}
	}()

	// Prepare response
	title := fmt.Sprintf("Job Created: %s", jobID)
	if isDryRun {
		title = fmt.Sprintf("Dry Run Started: %s", jobID)
	}

	html := fmt.Sprintf(`
		<div class="job-created">
			<h3>%s</h3>
			<div id="job-status" hx-get="/api/jobs/%s/status" hx-trigger="load, every 10s" hx-swap="innerHTML">
				<p>Loading status...</p>
			</div>
		</div>`, title, jobID)

	return jobID, html
}

// serveTemplate serves a template with optional data and no-cache headers
func (h *Handler) serveTemplate(w http.ResponseWriter, templateName string, data interface{}) error {
	// Use cached template
	tmpl, err := h.getTemplate(templateName)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load template: %v", err), http.StatusInternalServerError)
		return err
	}

	// Prevent caching
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	// Execute the base template which includes all page-specific blocks
	if err := tmpl.ExecuteTemplate(w, "base", data); err != nil {
		http.Error(w, fmt.Sprintf("Failed to execute template: %v", err), http.StatusInternalServerError)
		return err
	}

	return nil
}

// renderHTMLError renders a standardized HTML error message
func (h *Handler) renderHTMLError(w http.ResponseWriter, title, message string, optionalLink ...string) {
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, `<div class="error-message">`)
	fmt.Fprintf(w, `<h3>%s</h3>`, template.HTMLEscapeString(title))
	fmt.Fprintf(w, `<p>%s</p>`, template.HTMLEscapeString(message))
	if len(optionalLink) > 0 {
		fmt.Fprint(w, optionalLink[0])
	}
	fmt.Fprintf(w, `</div>`)
}

// getTemplate retrieves a cached template by filename, loading it lazily if needed
func (h *Handler) getTemplate(filename string) (*template.Template, error) {
	// Fast path: check cache first
	h.templatesMu.RLock()
	tmpl, ok := h.templates[filename]
	h.templatesMu.RUnlock()
	if ok {
		return tmpl, nil
	}

	// Slow path: load template
	h.templatesMu.Lock()
	defer h.templatesMu.Unlock()

	// Double-check after acquiring write lock
	if tmpl, ok := h.templates[filename]; ok {
		return tmpl, nil
	}

	// Map filename to full path
	templatePaths := map[string]string{
		"index.html":             "web/index.html",
		"admin.html":             "web/admin.html",
		"student-dashboard.html": "web/student-dashboard.html",
		"credentials.html":       "web/credentials.html",
		"ovh-credentials.html":   "web/ovh-credentials.html", // Keep for backward compatibility
		"labs-list.html":         "web/labs-list.html",
		"lab-workspaces.html":    "web/lab-workspaces.html",
	}

	tmplPath, ok := templatePaths[filename]
	if !ok {
		return nil, fmt.Errorf("template %s not found", filename)
	}

	var err error
	// Parse base template and page template together
	tmpl, err = template.ParseFiles("web/base.html", tmplPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load template %s: %w", tmplPath, err)
	}

	// Execute the base template which will include the page-specific blocks
	h.templates[filename] = tmpl
	return tmpl, nil
}

// ServeUI serves the main HTML UI (homepage)
func (h *Handler) ServeUI(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	h.serveTemplate(w, "index.html", nil)
}

// ServeAdminUI serves the admin HTML UI
func (h *Handler) ServeAdminUI(w http.ResponseWriter, r *http.Request) {
	// Check if credentials are configured
	hasCredentials := h.credentialsManager.HasCredentials("ovh")

	data := map[string]interface{}{
		"HasCredentials": hasCredentials,
	}

	h.serveTemplate(w, "admin.html", data)
}

// processLabRequest handles common lab request processing logic
func (h *Handler) processLabRequest(w http.ResponseWriter, r *http.Request, isDryRun bool) {
	// Parse form data - handle both multipart and urlencoded (50MB for template files)
	if err := h.parseForm(w, r, 50<<20); err != nil {
		log.Printf("Failed to parse form: %v", err)
		h.renderHTMLError(w, "Form Parse Error", fmt.Sprintf("Failed to parse form: %v", err))
		return
	}

	// Get provider from form (default to "ovh" for backward compatibility)
	provider := r.FormValue("provider")
	if provider == "" {
		provider = "ovh"
	}

	// Get provider credentials from in-memory storage
	providerCreds, err := h.getProviderCredentials(w, provider)
	if err != nil {
		return
	}

	// Create initial config without template file path
	initialConfig := h.createLabConfigFromForm(r, providerCreds, "")

	// Validate template source configuration
	templateSource := initialConfig.TemplateSource
	if templateSource == "" {
		// Backward compatibility: if no source specified, check for uploaded file
		templateSource = "upload"
	}

	if templateSource == "git" {
		// Validate Git repository URL is provided
		if initialConfig.TemplateGitRepo == "" {
			h.renderHTMLError(w, "Template Configuration Error", "Repository URL is required when using Git template source")
			return
		}
	} else if templateSource == "upload" {
		// Validate file upload is provided
		// This will be checked when saving the file
	}

	// Create job first to get jobID
	jobID := h.jobManager.CreateJob(initialConfig)

	// Create job directory
	jobDir := filepath.Join(h.pulumiExec.GetWorkDir(), jobID)
	if err := os.MkdirAll(jobDir, 0755); err != nil {
		log.Printf("Failed to create job directory: %v", err)
		h.renderHTMLError(w, "Job Creation Error", fmt.Sprintf("Failed to create job directory: %v", err))
		return
	}

	// Handle template file upload only if source is "upload"
	var templateFilePath string
	if templateSource == "upload" {
		var err error
		templateFilePath, err = h.saveUploadedTemplateFile(r, jobDir)
		if err != nil {
			log.Printf("Failed to save template file: %v", err)
			h.renderHTMLError(w, "Template Upload Error", fmt.Sprintf("Failed to save template file: %v", err))
			return
		}
		if templateFilePath == "" {
			h.renderHTMLError(w, "Template Configuration Error", "Template file is required when using upload source")
			return
		}
		// Update config with template file path (relative to job directory)
		initialConfig.TemplateFilePath = templateFilePath
		// Update job config
		h.updateJobConfig(jobID, func(config *LabConfig) {
			config.TemplateFilePath = templateFilePath
			config.TemplateSource = templateSource
		})
	} else {
		// Update job config with Git template info
		h.updateJobConfig(jobID, func(config *LabConfig) {
			config.TemplateSource = templateSource
			config.TemplateGitRepo = initialConfig.TemplateGitRepo
			config.TemplateGitFolder = initialConfig.TemplateGitFolder
			config.TemplateGitBranch = initialConfig.TemplateGitBranch
		})
	}

	_, html := h.executeLabJobWithID(initialConfig, isDryRun, jobID)

	// Return job status div for HTMX to display with proper polling
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprint(w, html)
}

// updateJobConfig updates a job's configuration using a callback function
func (h *Handler) updateJobConfig(jobID string, updater func(*LabConfig)) {
	job, exists := h.jobManager.GetJob(jobID)
	if exists {
		job.mu.Lock()
		if job.Config != nil {
			updater(job.Config)
		}
		job.mu.Unlock()
	}
}

// CreateLab handles lab creation requests
func (h *Handler) CreateLab(w http.ResponseWriter, r *http.Request) {
	log.Printf("CreateLab called: method=%s, path=%s", r.Method, r.URL.Path)

	if r.Method != http.MethodPost {
		log.Printf("Method not allowed: %s", r.Method)
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	h.processLabRequest(w, r, false)
}

// DryRunLab handles dry run requests
func (h *Handler) DryRunLab(w http.ResponseWriter, r *http.Request) {
	log.Printf("DryRunLab called: method=%s, path=%s", r.Method, r.URL.Path)

	if r.Method != http.MethodPost {
		log.Printf("Method not allowed: %s", r.Method)
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	h.processLabRequest(w, r, true)
}

// LaunchLab handles launching a real deployment after a successful dry run
func (h *Handler) LaunchLab(w http.ResponseWriter, r *http.Request) {
	log.Printf("LaunchLab called: method=%s, path=%s", r.Method, r.URL.Path)

	if r.Method != http.MethodPost {
		log.Printf("Method not allowed: %s", r.Method)
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse form data
	if err := r.ParseForm(); err != nil {
		log.Printf("Failed to parse form: %v", err)
		http.Error(w, fmt.Sprintf("Failed to parse form: %v", err), http.StatusBadRequest)
		return
	}

	jobID := r.FormValue("job_id")
	if jobID == "" {
		http.Error(w, "job_id is required", http.StatusBadRequest)
		return
	}

	// Get the job
	job, exists := h.jobManager.GetJob(jobID)
	if !exists {
		http.Error(w, "Job not found", http.StatusNotFound)
		return
	}

	// Check if job is in dry-run-completed status
	job.mu.RLock()
	status := job.Status
	job.mu.RUnlock()

	if status != JobStatusDryRunCompleted {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `
			<div class="error-message">
				<h3>Invalid Job Status</h3>
				<p>This job is not in dry-run-completed status. Current status: %s</p>
				<p>Only jobs that have completed a successful dry run can be launched.</p>
			</div>`, status)
		return
	}

	// Reset job status to pending and start execution
	h.jobManager.UpdateJobStatus(jobID, JobStatusPending)
	h.jobManager.AppendOutput(jobID, fmt.Sprintf("Launching real deployment at %s", time.Now().Format(time.RFC3339)))

	// Start Pulumi execution in a goroutine
	go func() {
		log.Printf("Starting Pulumi execution for job: %s", jobID)
		if err := h.pulumiExec.Execute(jobID); err != nil {
			log.Printf("Pulumi execution failed for job %s: %v", jobID, err)
			return
		}
		log.Printf("Pulumi execution completed for job: %s", jobID)
	}()

	// Return job status div for HTMX to display with proper polling
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, `
		<div class="job-created">
			<h3>Deployment Launched: %s</h3>
			<div id="job-status" hx-get="/api/jobs/%s/status" hx-trigger="load, every 10s" hx-swap="innerHTML">
				<p>Loading status...</p>
			</div>
		</div>`, jobID, jobID)
}

// GetJobStatus returns the current status of a job
func (h *Handler) GetJobStatus(w http.ResponseWriter, r *http.Request) {
	// Extract job ID from path like /api/jobs/{id}/status or /api/jobs/{id}
	pathParts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(pathParts) < 3 || pathParts[0] != "api" || pathParts[1] != "jobs" {
		log.Printf("Invalid path for job status: %s", r.URL.Path)
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}
	jobID := pathParts[2]
	log.Printf("GetJobStatus called for job: %s", jobID)

	job, exists := h.jobManager.GetJob(jobID)
	if !exists {
		log.Printf("Job not found: %s", jobID)
		http.Error(w, "Job not found", http.StatusNotFound)
		return
	}

	job.mu.RLock()
	status := job.Status
	output := job.Output
	errorMsg := job.Error
	kubeconfig := job.Kubeconfig
	job.mu.RUnlock()

	w.Header().Set("Content-Type", "text/html")

	var statusHTML strings.Builder
	statusHTML.WriteString(`<div class="job-status">`)
	statusHTML.WriteString(fmt.Sprintf(`<div class="status-badge status-%s">%s</div>`, status, status))

	// Show launch button if dry run completed successfully
	if status == JobStatusDryRunCompleted {
		statusHTML.WriteString(`<form hx-post="/api/labs/launch" hx-target="#job-status" hx-swap="outerHTML" style="display: inline-block; margin-left: 1rem;">`)
		statusHTML.WriteString(fmt.Sprintf(`<input type="hidden" name="job_id" value="%s">`, jobID))
		statusHTML.WriteString(`<button type="submit" class="btn btn-success">`)
		statusHTML.WriteString(`<span class="btn-icon">🚀</span> Launch Real Deployment`)
		statusHTML.WriteString(`</button>`)
		statusHTML.WriteString(`</form>`)
	}

	// Show retry button if job failed
	if status == JobStatusFailed {
		statusHTML.WriteString(`<form hx-post="/api/jobs/` + jobID + `/retry" hx-target="#job-status" hx-swap="outerHTML" style="display: inline-block; margin-left: 1rem;">`)
		statusHTML.WriteString(`<button type="submit" class="btn btn-primary">`)
		statusHTML.WriteString(`<span class="btn-icon">🔄</span> Retry Job`)
		statusHTML.WriteString(`</button>`)
		statusHTML.WriteString(`</form>`)
	}

	// Show download button if kubeconfig is available (for both completed and failed jobs)
	if kubeconfig != "" && (status == JobStatusCompleted || status == JobStatusFailed) {
		statusHTML.WriteString(fmt.Sprintf(`<a href="/api/jobs/%s/kubeconfig" class="btn btn-download" download="kubeconfig-%s.yaml">`, jobID, jobID))
		statusHTML.WriteString(`<span class="btn-icon">⬇</span> Download Kubeconfig`)
		statusHTML.WriteString(`</a>`)
	}

	if errorMsg != "" {
		statusHTML.WriteString(fmt.Sprintf(`<div class="error-message">%s</div>`, template.HTMLEscapeString(errorMsg)))
	}

	statusHTML.WriteString(`<div class="output-container">`)
	statusHTML.WriteString(`<pre class="output">`)
	for _, line := range output {
		statusHTML.WriteString(template.HTMLEscapeString(line))
		statusHTML.WriteString("\n")
	}
	statusHTML.WriteString(`</pre>`)
	statusHTML.WriteString(`</div>`)

	// Continue polling if job is still running
	if status == JobStatusPending || status == JobStatusRunning {
		statusHTML.WriteString(fmt.Sprintf(`<div hx-get="/api/jobs/%s/status" hx-trigger="every 10s" hx-swap="outerHTML"></div>`, jobID))
	}

	statusHTML.WriteString(`</div>`)

	fmt.Fprint(w, statusHTML.String())
}

// GetJobStatusJSON returns job status as JSON (for API clients)
func (h *Handler) GetJobStatusJSON(w http.ResponseWriter, r *http.Request) {
	// Extract job ID from path like /api/jobs/{id}/status or /api/jobs/{id}
	pathParts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(pathParts) < 3 || pathParts[0] != "api" || pathParts[1] != "jobs" {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}
	jobID := pathParts[2]

	job, exists := h.jobManager.GetJob(jobID)
	if !exists {
		http.Error(w, "Job not found", http.StatusNotFound)
		return
	}

	job.mu.RLock()
	defer job.mu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(job)
}

// ServeStatic serves static files
func (h *Handler) ServeStatic(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/static/")
	filePath := filepath.Join("web", "static", path)

	// Security: prevent directory traversal
	if strings.Contains(path, "..") {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	file, err := os.Open(filePath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()

	// Set content type based on file extension
	if strings.HasSuffix(path, ".css") {
		w.Header().Set("Content-Type", "text/css")
	} else if strings.HasSuffix(path, ".js") {
		w.Header().Set("Content-Type", "application/javascript")
	} else if strings.HasSuffix(path, ".png") {
		w.Header().Set("Content-Type", "image/png")
	}

	io.Copy(w, file)
}

// DownloadKubeconfig serves the kubeconfig file for download
func (h *Handler) DownloadKubeconfig(w http.ResponseWriter, r *http.Request) {
	// Extract job ID from path like /api/jobs/{id}/kubeconfig
	pathParts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(pathParts) < 4 || pathParts[0] != "api" || pathParts[1] != "jobs" || pathParts[3] != "kubeconfig" {
		log.Printf("Invalid path for kubeconfig download: %s", r.URL.Path)
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}
	jobID := pathParts[2]
	log.Printf("DownloadKubeconfig called for job: %s", jobID)

	job, exists := h.jobManager.GetJob(jobID)
	if !exists {
		log.Printf("Job not found: %s", jobID)
		http.Error(w, "Job not found", http.StatusNotFound)
		return
	}

	job.mu.RLock()
	kubeconfig := job.Kubeconfig
	status := job.Status
	job.mu.RUnlock()

	// Allow download for completed or failed jobs if kubeconfig is available
	if status != JobStatusCompleted && status != JobStatusFailed {
		http.Error(w, "Job not completed or failed", http.StatusBadRequest)
		return
	}

	if kubeconfig == "" {
		http.Error(w, "Kubeconfig not available", http.StatusNotFound)
		return
	}

	// Set headers for file download
	w.Header().Set("Content-Type", "application/x-yaml")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=kubeconfig-%s.yaml", jobID))
	w.Header().Set("Content-Length", strconv.Itoa(len(kubeconfig)))

	w.Write([]byte(kubeconfig))
}

// ServeStudentDashboard serves the student dashboard page
func (h *Handler) ServeStudentDashboard(w http.ResponseWriter, r *http.Request) {
	h.serveTemplate(w, "student-dashboard.html", nil)
}

// ListLabs returns a list of completed jobs (labs) available for workspace requests
func (h *Handler) ListLabs(w http.ResponseWriter, r *http.Request) {
	h.jobManager.mu.RLock()
	defer h.jobManager.mu.RUnlock()

	var completedLabs []*Job
	for _, job := range h.jobManager.jobs {
		job.mu.RLock()
		status := job.Status
		job.mu.RUnlock()
		if status == JobStatusCompleted {
			completedLabs = append(completedLabs, job)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(completedLabs)
}

// RequestWorkspace handles workspace request from students
func (h *Handler) RequestWorkspace(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse form data - handle both application/x-www-form-urlencoded and multipart/form-data
	if err := h.parseForm(w, r, 32<<20); err != nil {
		return
	}

	// Get form values using helper
	email := getFormValue(r, "email")
	labID := getFormValue(r, "lab_id")

	// Validate email
	if email == "" {
		http.Error(w, "Email is required", http.StatusBadRequest)
		return
	}
	if !validateEmail(email) {
		http.Error(w, "Invalid email format", http.StatusBadRequest)
		return
	}

	// Validate lab ID
	if labID == "" {
		http.Error(w, "Lab ID is required", http.StatusBadRequest)
		return
	}

	// Get the job
	job, exists := h.jobManager.GetJob(labID)
	if !exists {
		http.Error(w, "Lab not found", http.StatusNotFound)
		return
	}

	job.mu.RLock()
	status := job.Status
	coderURL := job.CoderURL
	coderSessionToken := job.CoderSessionToken
	coderOrganizationID := job.CoderOrganizationID
	coderAdminEmail := job.CoderAdminEmail
	coderAdminPassword := job.CoderAdminPassword
	job.mu.RUnlock()

	if status != JobStatusCompleted {
		http.Error(w, "Lab is not ready yet", http.StatusBadRequest)
		return
	}

	// Clean up any malformed ConfigValue JSON strings that might have been saved
	coderURL = extractStringFromConfigValue(coderURL)
	coderSessionToken = extractStringFromConfigValue(coderSessionToken)
	coderOrganizationID = extractStringFromConfigValue(coderOrganizationID)
	coderAdminEmail = extractStringFromConfigValue(coderAdminEmail)
	coderAdminPassword = extractStringFromConfigValue(coderAdminPassword)

	if coderURL == "" || coderSessionToken == "" || coderOrganizationID == "" {
		http.Error(w, "Coder configuration not available for this lab", http.StatusInternalServerError)
		return
	}

	if coderAdminEmail == "" || coderAdminPassword == "" {
		http.Error(w, "Coder admin credentials not available for this lab", http.StatusInternalServerError)
		return
	}

	// Generate secure password for student
	password, err := GenerateSecurePassword()
	if err != nil {
		log.Printf("Failed to generate password: %v", err)
		http.Error(w, "Failed to generate password", http.StatusInternalServerError)
		return
	}

	// Get username from email (before @)
	username := strings.Split(email, "@")[0]
	// Sanitize username (remove special characters)
	username = strings.ToLower(strings.ReplaceAll(username, ".", "-"))

	// Create Coder client config
	coderConfig := coder.CoderClientConfig{
		ServerURL:      coderURL,
		SessionToken:   coderSessionToken,
		OrganizationID: coderOrganizationID,
	}

	// Get available templates with automatic token refresh
	templates, err := coder.GetTemplatesWithRetry(coderConfig, coderAdminEmail, coderAdminPassword)
	if err != nil {
		log.Printf("Failed to get templates: %v", err)
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<div class="error-message">Failed to get templates: %s</div>`, template.HTMLEscapeString(err.Error()))
		return
	}

	if len(templates) == 0 {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<div class="error-message">No templates available in this lab</div>`)
		return
	}

	// Use first available template
	templateID := templates[0].ID

	// Create user in Coder with automatic token refresh
	user, updatedConfig, err := coder.CreateUserWithRetry(coderConfig, coderAdminEmail, coderAdminPassword, email, username, password)
	if err != nil {
		log.Printf("Failed to create user: %v", err)
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<div class="error-message">Failed to create user: %s</div>`, template.HTMLEscapeString(err.Error()))
		return
	}
	// Update config in case token was refreshed
	coderConfig = updatedConfig

	// Create workspace for user with automatic token refresh
	workspaceName := fmt.Sprintf("%s-workspace", username)
	workspace, updatedConfig, err := coder.CreateWorkspaceWithRetry(coderConfig, coderAdminEmail, coderAdminPassword, user.ID, templateID, workspaceName)
	if err != nil {
		log.Printf("Failed to create workspace: %v", err)
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<div class="error-message">Failed to create workspace: %s</div>`, template.HTMLEscapeString(err.Error()))
		return
	}

	// Build workspace URL
	workspaceURL := fmt.Sprintf("%s/@%s/%s", coderURL, user.Username, workspace.Name)

	// Create workspace info structure for cookie
	// Note: password will be encrypted client-side using email + student password
	workspaceInfo := map[string]interface{}{
		"email":              email,
		"workspace_url":      workspaceURL,
		"password":           password, // Will be encrypted client-side before storing in cookie
		"encrypted_password": "",       // Will be set by client-side encryption
		"workspace_name":     workspace.Name,
		"lab_id":             labID,
		"created_at":         time.Now().Format(time.RFC3339),
	}

	// Encode workspace info as JSON
	workspaceInfoJSON, err := json.Marshal(workspaceInfo)
	if err != nil {
		log.Printf("Failed to marshal workspace info: %v", err)
		// Continue without cookie if marshaling fails
	} else {
		// Determine if we're using HTTPS (check if URL scheme is https or if request is secure)
		isSecure := strings.HasPrefix(coderURL, "https://") || r.TLS != nil

		// URL-encode the JSON string for cookie value (cookies need URL encoding for special characters)
		cookieValue := url.QueryEscape(string(workspaceInfoJSON))

		// Set cookie with workspace info
		cookie := &http.Cookie{
			Name:     "workspace_info",
			Value:    cookieValue,
			Path:     "/",
			MaxAge:   86400, // 1 day in seconds
			HttpOnly: false, // Allow JavaScript to read it
			Secure:   isSecure,
			SameSite: http.SameSiteLaxMode,
		}
		http.SetCookie(w, cookie)
		log.Printf("Set workspace_info cookie for email: %s", email)
	}

	// Return success response with credentials
	// Include workspace info as JSON in a data attribute for client-side encryption
	workspaceInfoForClient := map[string]interface{}{
		"email":          email,
		"workspace_url":  workspaceURL,
		"password":       password,
		"workspace_name": workspace.Name,
		"lab_id":         labID,
		"created_at":     time.Now().Format(time.RFC3339),
	}
	workspaceInfoJSONForClient, _ := json.Marshal(workspaceInfoForClient)
	workspaceInfoJSONEscaped := template.HTMLEscapeString(string(workspaceInfoJSONForClient))

	w.Header().Set("Content-Type", "text/html")
	var response strings.Builder
	response.WriteString(`<div class="success-message">`)
	response.WriteString(`<h3>✅ Workspace Created Successfully!</h3>`)
	response.WriteString(`<div class="credentials-box">`)
	response.WriteString(`<h3>Your Workspace Credentials</h3>`)
	response.WriteString(fmt.Sprintf(`<div class="credential-item"><label>Workspace URL:</label><div class="value"><a href="%s" target="_blank">%s</a></div></div>`, workspaceURL, workspaceURL))
	response.WriteString(fmt.Sprintf(`<div class="credential-item"><label>Email:</label><div class="value">%s</div></div>`, template.HTMLEscapeString(email)))
	response.WriteString(fmt.Sprintf(`<div class="credential-item"><label>Password:</label><div class="value">%s</div></div>`, template.HTMLEscapeString(password)))
	response.WriteString(`<p><strong>Important:</strong> Please save these credentials. You will need them to access your workspace.</p>`)
	response.WriteString(`<p><small>Your workspace information can be encrypted and saved locally. Click "Encrypt & Save" below to store it securely.</small></p>`)
	response.WriteString(fmt.Sprintf(`<div data-workspace-info='%s' style="display:none;"></div>`, workspaceInfoJSONEscaped))
	response.WriteString(`<button onclick="encryptAndSaveWorkspaceInfo(this)" class="btn" style="margin-top: 1rem;">Encrypt & Save Workspace Info</button>`)
	response.WriteString(`</div>`)
	response.WriteString(`</div>`)

	fmt.Fprint(w, response.String())
}

// getFormKeys returns all keys from both PostForm and Form
func getFormKeys(r *http.Request) []string {
	keys := make(map[string]bool)
	for k := range r.PostForm {
		keys[k] = true
	}
	for k := range r.Form {
		keys[k] = true
	}
	result := make([]string, 0, len(keys))
	for k := range keys {
		result = append(result, k)
	}
	return result
}

// getPostFormKeys returns all keys from PostForm only
func getPostFormKeys(r *http.Request) []string {
	keys := make([]string, 0, len(r.PostForm))
	for k := range r.PostForm {
		keys = append(keys, k)
	}
	return keys
}

// extractStringFromConfigValue extracts a string value from a Pulumi ConfigValue JSON string
// If the input is already a plain string, it returns it as-is
// If it's a JSON object like {"Value":"...","Secret":false}, it extracts the Value field
func extractStringFromConfigValue(val string) string {
	if val == "" {
		return ""
	}
	// Check if it looks like a JSON object (starts with {)
	if !strings.HasPrefix(strings.TrimSpace(val), "{") {
		return val // Already a plain string
	}
	// Try to unmarshal as ConfigValue structure
	var configVal struct {
		Value  interface{} `json:"Value"`
		Secret bool        `json:"Secret"`
	}
	if err := json.Unmarshal([]byte(val), &configVal); err == nil {
		if strVal, ok := configVal.Value.(string); ok {
			return strVal
		}
	}
	// If unmarshaling failed or Value is not a string, return original value
	return val
}

// ServeCredentials serves the provider credentials configuration page
func (h *Handler) ServeCredentials(w http.ResponseWriter, r *http.Request) {
	// Get provider from query parameter (default to "ovh" for backward compatibility)
	provider := r.URL.Query().Get("provider")
	if provider == "" {
		provider = "ovh"
	}

	// Get current credentials status (without exposing secrets)
	var currentCreds map[string]interface{}
	creds, err := h.credentialsManager.GetCredentials(provider)
	if err == nil {
		if ovhCreds, ok := creds.(*OVHCredentials); ok {
			currentCreds = map[string]interface{}{
				"configured":   true,
				"provider":     provider,
				"service_name": ovhCreds.ServiceName,
				"endpoint":     ovhCreds.Endpoint,
			}
		}
		// Future: Add handling for other provider types here
	} else {
		currentCreds = map[string]interface{}{
			"configured": false,
			"provider":   provider,
		}
	}

	data := map[string]interface{}{
		"CurrentCreds": currentCreds,
		"Provider":     provider,
	}

	h.serveTemplate(w, "credentials.html", data)
}

// ServeOVHCredentials serves the OVH credentials configuration page (backward compatibility)
func (h *Handler) ServeOVHCredentials(w http.ResponseWriter, r *http.Request) {
	// Redirect to new credentials page with OVH provider
	http.Redirect(w, r, "/credentials?provider=ovh", http.StatusMovedPermanently)
}

// SetCredentials handles setting provider credentials
func (h *Handler) SetCredentials(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusMethodNotAllowed)
		fmt.Fprintf(w, `
			<div class="error-message">
				<h3>Method Not Allowed</h3>
				<p>Only POST requests are accepted.</p>
			</div>`)
		return
	}

	// Parse form data - handle both multipart and urlencoded
	if err := h.parseForm(w, r, 10<<20); err != nil {
		// Return HTML error for HTMX compatibility
		log.Printf("SetCredentials - Failed to parse form: %v", err)
		w.WriteHeader(http.StatusBadRequest)
		h.renderHTMLError(w, "Failed to Parse Form", err.Error())
		return
	}

	// Get provider from form data
	provider := getFormValue(r, "provider")
	if provider == "" {
		provider = "ovh" // Default to OVH for backward compatibility
	}

	// Handle provider-specific credential creation
	switch provider {
	case "ovh":
		h.setOVHCredentialsFromForm(w, r)
	default:
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `
			<div class="error-message">
				<h3>Unsupported Provider</h3>
				<p>Provider "%s" is not yet supported.</p>
			</div>`, template.HTMLEscapeString(provider))
	}
}

// setOVHCredentialsFromForm handles OVH-specific credential setting
func (h *Handler) setOVHCredentialsFromForm(w http.ResponseWriter, r *http.Request) {
	// Get form values using helper
	applicationKey := getFormValue(r, "ovh_application_key")
	applicationSecret := getFormValue(r, "ovh_application_secret")
	consumerKey := getFormValue(r, "ovh_consumer_key")
	serviceName := getFormValue(r, "ovh_service_name")
	endpoint := getFormValue(r, "ovh_endpoint")

	creds := &OVHCredentials{
		ApplicationKey:    applicationKey,
		ApplicationSecret: applicationSecret,
		ConsumerKey:       consumerKey,
		ServiceName:       serviceName,
		Endpoint:          endpoint,
	}

	if err := h.credentialsManager.SetCredentials(creds); err != nil {
		log.Printf("Failed to set OVH credentials: %v", err)
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `
			<div class="error-message">
				<h3>Failed to Save Credentials</h3>
				<p>%s</p>
				<p><small>Please ensure all fields are filled correctly.</small></p>
			</div>`, template.HTMLEscapeString(err.Error()))
		return
	}

	log.Printf("OVH credentials saved successfully")
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, `
		<div class="success-message">
			<p>✅ OVH credentials saved successfully</p>
		</div>`)
}

// SetOVHCredentials handles setting OVH credentials (backward compatibility)
func (h *Handler) SetOVHCredentials(w http.ResponseWriter, r *http.Request) {
	h.SetCredentials(w, r)
}

// GetCredentials handles getting provider credentials status
func (h *Handler) GetCredentials(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Get provider from query parameter
	provider := r.URL.Query().Get("provider")
	if provider == "" {
		provider = "ovh" // Default to OVH for backward compatibility
	}

	creds, err := h.credentialsManager.GetCredentials(provider)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"configured": false,
			"provider":   provider,
		})
		return
	}

	// Handle provider-specific response
	switch provider {
	case "ovh":
		ovhCreds, ok := creds.(*OVHCredentials)
		if !ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": "Invalid credentials type"})
			return
		}

		// Return limited info (not the actual secrets)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"configured":          true,
			"provider":            provider,
			"service_name":        ovhCreds.ServiceName,
			"endpoint":            ovhCreds.Endpoint,
			"has_application_key": ovhCreds.ApplicationKey != "",
			"has_consumer_key":    ovhCreds.ConsumerKey != "",
		})
	default:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"configured": false,
			"provider":   provider,
			"error":      "Provider not supported",
		})
	}
}

// GetOVHCredentials handles getting OVH credentials status (backward compatibility)
func (h *Handler) GetOVHCredentials(w http.ResponseWriter, r *http.Request) {
	// Add provider=ovh to query and delegate to GetCredentials
	q := r.URL.Query()
	q.Set("provider", "ovh")
	r.URL.RawQuery = q.Encode()
	h.GetCredentials(w, r)
}

// ListProviders returns the list of available providers
func (h *Handler) ListProviders(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Define available providers with their configuration
	providers := []map[string]interface{}{
		{
			"id":      "ovh",
			"name":    "OVHcloud",
			"enabled": true,
			"fields": []map[string]interface{}{
				{"name": "ovh_application_key", "label": "Application Key", "type": "password", "required": true},
				{"name": "ovh_application_secret", "label": "Application Secret", "type": "password", "required": true},
				{"name": "ovh_consumer_key", "label": "Consumer Key", "type": "password", "required": true},
				{"name": "ovh_service_name", "label": "Service Name (Project ID)", "type": "text", "required": true},
				{"name": "ovh_endpoint", "label": "OVH Endpoint", "type": "select", "required": true, "options": []string{"ovh-eu", "ovh-us", "ovh-ca"}},
			},
		},
		// Future providers can be added here
		{
			"id":      "aws",
			"name":    "Amazon Web Services",
			"enabled": false,
			"fields": []map[string]interface{}{
				{"name": "aws_access_key_id", "label": "Access Key ID", "type": "password", "required": true},
				{"name": "aws_secret_access_key", "label": "Secret Access Key", "type": "password", "required": true},
				{"name": "aws_region", "label": "Region", "type": "select", "required": true, "options": []string{"us-east-1", "us-west-2", "eu-west-1", "eu-central-1"}},
			},
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(providers)
}

// ServeLabsList serves the labs list page
func (h *Handler) ServeLabsList(w http.ResponseWriter, r *http.Request) {
	// Get all jobs (labs)
	allJobs := h.jobManager.GetAllJobs()

	// Helper function to shorten lab ID
	shortenLabID := func(id string) string {
		if len(id) <= 16 {
			return id
		}
		return id[:8] + "..." + id[len(id)-4:]
	}

	// Prepare lab data for template (without sensitive info)
	type LabDisplay struct {
		ID            string
		IDShort       string
		Status        string
		CreatedAt     string
		UpdatedAt     string
		StackName     string
		IsDryRun      bool
		HasError      bool
		ErrorMsg      string
		HasKubeconfig bool
		IsDestroyed   bool
	}

	labsDisplay := make([]LabDisplay, 0, len(allJobs))
	for _, job := range allJobs {
		job.mu.RLock()
		status := string(job.Status)
		stackName := ""
		if job.Config != nil {
			stackName = job.Config.StackName
		}
		isDryRun := job.Status == JobStatusDryRunCompleted
		hasError := job.Error != ""
		errorMsg := job.Error
		hasKubeconfig := job.Kubeconfig != ""
		isDestroyed := job.Status == JobStatusDestroyed
		createdAt := job.CreatedAt.Format("2006-01-02 15:04:05")
		updatedAt := job.UpdatedAt.Format("2006-01-02 15:04:05")
		job.mu.RUnlock()

		labsDisplay = append(labsDisplay, LabDisplay{
			ID:            job.ID,
			IDShort:       shortenLabID(job.ID),
			Status:        status,
			CreatedAt:     createdAt,
			UpdatedAt:     updatedAt,
			StackName:     stackName,
			IsDryRun:      isDryRun,
			HasError:      hasError,
			ErrorMsg:      errorMsg,
			HasKubeconfig: hasKubeconfig,
			IsDestroyed:   isDestroyed,
		})
	}

	data := map[string]interface{}{
		"Labs":  labsDisplay,
		"Count": len(labsDisplay),
	}

	h.serveTemplate(w, "labs-list.html", data)
}

// ServeLabWorkspaces serves the workspaces page for a lab
func (h *Handler) ServeLabWorkspaces(w http.ResponseWriter, r *http.Request) {
	// Extract lab ID from path like /labs/{id}/workspaces
	pathParts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(pathParts) < 3 || pathParts[0] != "labs" || pathParts[2] != "workspaces" {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}

	labID := pathParts[1]

	// Get the lab (job)
	job, exists := h.jobManager.GetJob(labID)
	if !exists {
		http.Error(w, "Lab not found", http.StatusNotFound)
		return
	}

	job.mu.RLock()
	status := job.Status
	coderURL := job.CoderURL
	coderSessionToken := job.CoderSessionToken
	coderOrganizationID := job.CoderOrganizationID
	coderAdminEmail := job.CoderAdminEmail
	coderAdminPassword := job.CoderAdminPassword
	templateName := ""
	if job.Config != nil {
		templateName = job.Config.CoderTemplateName
	}
	stackName := ""
	if job.Config != nil {
		stackName = job.Config.StackName
	}
	job.mu.RUnlock()

	if status != JobStatusCompleted {
		http.Error(w, "Lab is not ready yet", http.StatusBadRequest)
		return
	}

	// Clean up any malformed ConfigValue JSON strings
	coderURL = extractStringFromConfigValue(coderURL)
	coderSessionToken = extractStringFromConfigValue(coderSessionToken)
	coderOrganizationID = extractStringFromConfigValue(coderOrganizationID)
	coderAdminEmail = extractStringFromConfigValue(coderAdminEmail)
	coderAdminPassword = extractStringFromConfigValue(coderAdminPassword)

	if coderURL == "" || coderSessionToken == "" || coderOrganizationID == "" {
		http.Error(w, "Lab Coder configuration not available", http.StatusInternalServerError)
		return
	}

	// Get template ID from template name
	coderConfig := coder.CoderClientConfig{
		ServerURL:      coderURL,
		SessionToken:   coderSessionToken,
		OrganizationID: coderOrganizationID,
	}

	templates, err := coder.GetTemplatesWithRetry(coderConfig, coderAdminEmail, coderAdminPassword)
	if err != nil {
		log.Printf("Failed to get templates: %v", err)
		http.Error(w, fmt.Sprintf("Failed to get templates: %s", err.Error()), http.StatusInternalServerError)
		return
	}

	var templateID uuid.UUID
	found := false
	for _, tmpl := range templates {
		if tmpl.Name == templateName {
			templateID = tmpl.ID
			found = true
			break
		}
	}

	if !found {
		http.Error(w, "Template not found in Coder", http.StatusNotFound)
		return
	}

	// Get workspaces for this template
	workspaces, updatedConfig, err := coder.ListWorkspacesWithRetry(coderConfig, coderAdminEmail, coderAdminPassword, templateID)
	if err != nil {
		log.Printf("Failed to list workspaces: %v", err)
		http.Error(w, fmt.Sprintf("Failed to list workspaces: %s", err.Error()), http.StatusInternalServerError)
		return
	}

	// Update config if token was refreshed
	coderConfig = updatedConfig

	// Prepare workspace data for template
	type WorkspaceDisplay struct {
		ID        string
		Name      string
		Owner     string
		Status    string
		CreatedAt string
		UpdatedAt string
	}

	workspacesDisplay := make([]WorkspaceDisplay, 0, len(workspaces))
	for _, ws := range workspaces {
		createdAt := ""
		if !ws.CreatedAt.IsZero() {
			createdAt = ws.CreatedAt.Format("2006-01-02 15:04:05")
		}
		updatedAt := ""
		if !ws.UpdatedAt.IsZero() {
			updatedAt = ws.UpdatedAt.Format("2006-01-02 15:04:05")
		}

		workspacesDisplay = append(workspacesDisplay, WorkspaceDisplay{
			ID:        ws.ID.String(),
			Name:      ws.Name,
			Owner:     ws.OwnerName,
			Status:    string(ws.LatestBuild.Job.Status),
			CreatedAt: createdAt,
			UpdatedAt: updatedAt,
		})
	}

	data := map[string]interface{}{
		"LabID":      labID,
		"StackName":  stackName,
		"Workspaces": workspacesDisplay,
		"Count":      len(workspacesDisplay),
	}

	h.serveTemplate(w, "lab-workspaces.html", data)
}

// ListLabWorkspaces returns JSON list of workspaces for a lab
func (h *Handler) ListLabWorkspaces(w http.ResponseWriter, r *http.Request) {
	// Extract lab ID from path like /api/labs/{id}/workspaces
	pathParts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(pathParts) < 4 || pathParts[0] != "api" || pathParts[1] != "labs" || pathParts[3] != "workspaces" {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}

	labID := pathParts[2]

	// Get the lab (job)
	job, exists := h.jobManager.GetJob(labID)
	if !exists {
		http.Error(w, "Lab not found", http.StatusNotFound)
		return
	}

	job.mu.RLock()
	status := job.Status
	coderURL := job.CoderURL
	coderSessionToken := job.CoderSessionToken
	coderOrganizationID := job.CoderOrganizationID
	coderAdminEmail := job.CoderAdminEmail
	coderAdminPassword := job.CoderAdminPassword
	templateName := ""
	if job.Config != nil {
		templateName = job.Config.CoderTemplateName
	}
	job.mu.RUnlock()

	if status != JobStatusCompleted {
		http.Error(w, "Lab is not ready yet", http.StatusBadRequest)
		return
	}

	// Clean up any malformed ConfigValue JSON strings
	coderURL = extractStringFromConfigValue(coderURL)
	coderSessionToken = extractStringFromConfigValue(coderSessionToken)
	coderOrganizationID = extractStringFromConfigValue(coderOrganizationID)
	coderAdminEmail = extractStringFromConfigValue(coderAdminEmail)
	coderAdminPassword = extractStringFromConfigValue(coderAdminPassword)

	if coderURL == "" || coderSessionToken == "" || coderOrganizationID == "" {
		http.Error(w, "Lab Coder configuration not available", http.StatusInternalServerError)
		return
	}

	// Get template ID from template name
	coderConfig := coder.CoderClientConfig{
		ServerURL:      coderURL,
		SessionToken:   coderSessionToken,
		OrganizationID: coderOrganizationID,
	}

	templates, err := coder.GetTemplatesWithRetry(coderConfig, coderAdminEmail, coderAdminPassword)
	if err != nil {
		log.Printf("Failed to get templates: %v", err)
		http.Error(w, fmt.Sprintf("Failed to get templates: %s", err.Error()), http.StatusInternalServerError)
		return
	}

	var templateID uuid.UUID
	found := false
	for _, tmpl := range templates {
		if tmpl.Name == templateName {
			templateID = tmpl.ID
			found = true
			break
		}
	}

	if !found {
		http.Error(w, "Template not found in Coder", http.StatusNotFound)
		return
	}

	// Get workspaces for this template
	workspaces, _, err := coder.ListWorkspacesWithRetry(coderConfig, coderAdminEmail, coderAdminPassword, templateID)
	if err != nil {
		log.Printf("Failed to list workspaces: %v", err)
		http.Error(w, fmt.Sprintf("Failed to list workspaces: %s", err.Error()), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(workspaces)
}

// writeJSONError writes a JSON error response for DeleteWorkspace
func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": false,
		"message": message,
	})
}

// DeleteWorkspace handles workspace deletion requests
func (h *Handler) DeleteWorkspace(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	// Parse form data
	if err := r.ParseForm(); err != nil {
		log.Printf("Failed to parse form: %v", err)
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("Failed to parse form: %v", err))
		return
	}

	// Check if this is a bulk delete request
	workspaceIDsStr := r.FormValue("workspace_ids")
	if workspaceIDsStr != "" {
		// Bulk delete
		var workspaceIDs []string
		if err := json.Unmarshal([]byte(workspaceIDsStr), &workspaceIDs); err != nil {
			writeJSONError(w, http.StatusBadRequest, "Invalid workspace_ids format")
			return
		}

		labID := r.FormValue("lab_id")
		if labID == "" {
			writeJSONError(w, http.StatusBadRequest, "lab_id is required")
			return
		}

		// Get the lab (job)
		job, exists := h.jobManager.GetJob(labID)
		if !exists {
			writeJSONError(w, http.StatusNotFound, "Lab not found")
			return
		}

		job.mu.RLock()
		coderURL := job.CoderURL
		coderSessionToken := job.CoderSessionToken
		coderOrganizationID := job.CoderOrganizationID
		coderAdminEmail := job.CoderAdminEmail
		coderAdminPassword := job.CoderAdminPassword
		job.mu.RUnlock()

		// Clean up any malformed ConfigValue JSON strings
		coderURL = extractStringFromConfigValue(coderURL)
		coderSessionToken = extractStringFromConfigValue(coderSessionToken)
		coderOrganizationID = extractStringFromConfigValue(coderOrganizationID)
		coderAdminEmail = extractStringFromConfigValue(coderAdminEmail)
		coderAdminPassword = extractStringFromConfigValue(coderAdminPassword)

		coderConfig := coder.CoderClientConfig{
			ServerURL:      coderURL,
			SessionToken:   coderSessionToken,
			OrganizationID: coderOrganizationID,
		}

		var errors []string
		for _, wsIDStr := range workspaceIDs {
			wsID, err := uuid.Parse(wsIDStr)
			if err != nil {
				errors = append(errors, fmt.Sprintf("Invalid workspace ID %s: %v", wsIDStr, err))
				continue
			}

			_, err = coder.DeleteWorkspaceWithRetry(coderConfig, coderAdminEmail, coderAdminPassword, wsID)
			if err != nil {
				errors = append(errors, fmt.Sprintf("Failed to delete workspace %s: %v", wsIDStr, err))
			}
		}

		if len(errors) > 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusPartialContent)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"errors":  errors,
			})
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"message": fmt.Sprintf("Successfully deleted %d workspace(s)", len(workspaceIDs)),
		})
		return
	}

	// Single workspace delete
	workspaceIDStr := r.FormValue("workspace_id")
	if workspaceIDStr == "" {
		// Try to extract from URL path: /api/labs/{lab_id}/workspaces/{workspace_id}/delete
		pathParts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(pathParts) >= 6 && pathParts[5] == "delete" {
			workspaceIDStr = pathParts[4]
		} else {
			writeJSONError(w, http.StatusBadRequest, "workspace_id is required")
			return
		}
	}

	workspaceID, err := uuid.Parse(workspaceIDStr)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid workspace ID")
		return
	}

	labID := r.FormValue("lab_id")
	if labID == "" {
		// Try to extract from URL path
		pathParts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(pathParts) >= 3 {
			labID = pathParts[2]
		} else {
			writeJSONError(w, http.StatusBadRequest, "lab_id is required")
			return
		}
	}

	// Get the lab (job)
	job, exists := h.jobManager.GetJob(labID)
	if !exists {
		writeJSONError(w, http.StatusNotFound, "Lab not found")
		return
	}

	job.mu.RLock()
	coderURL := job.CoderURL
	coderSessionToken := job.CoderSessionToken
	coderOrganizationID := job.CoderOrganizationID
	coderAdminEmail := job.CoderAdminEmail
	coderAdminPassword := job.CoderAdminPassword
	job.mu.RUnlock()

	// Clean up any malformed ConfigValue JSON strings
	coderURL = extractStringFromConfigValue(coderURL)
	coderSessionToken = extractStringFromConfigValue(coderSessionToken)
	coderOrganizationID = extractStringFromConfigValue(coderOrganizationID)
	coderAdminEmail = extractStringFromConfigValue(coderAdminEmail)
	coderAdminPassword = extractStringFromConfigValue(coderAdminPassword)

	coderConfig := coder.CoderClientConfig{
		ServerURL:      coderURL,
		SessionToken:   coderSessionToken,
		OrganizationID: coderOrganizationID,
	}

	_, err = coder.DeleteWorkspaceWithRetry(coderConfig, coderAdminEmail, coderAdminPassword, workspaceID)
	if err != nil {
		log.Printf("Failed to delete workspace: %v", err)
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to delete workspace: %s", err.Error()))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "Workspace deleted successfully",
	})
}

// DestroyStack handles stack destruction requests
func (h *Handler) DestroyStack(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse form data
	if err := r.ParseForm(); err != nil {
		log.Printf("Failed to parse form: %v", err)
		http.Error(w, fmt.Sprintf("Failed to parse form: %v", err), http.StatusBadRequest)
		return
	}

	jobID := r.FormValue("job_id")
	if jobID == "" {
		http.Error(w, "job_id is required", http.StatusBadRequest)
		return
	}

	// Get the job
	job, exists := h.jobManager.GetJob(jobID)
	if !exists {
		http.Error(w, "Job not found", http.StatusNotFound)
		return
	}

	// Check if job has a stack name
	job.mu.RLock()
	stackName := ""
	if job.Config != nil {
		stackName = job.Config.StackName
	}
	job.mu.RUnlock()

	if stackName == "" {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `
			<div class="error-message">
				<h3>No Stack Associated</h3>
				<p>This job does not have an associated stack to destroy.</p>
			</div>`)
		return
	}

	// Start destruction in a goroutine
	go func() {
		log.Printf("Starting stack destruction for job: %s, stack: %s", jobID, stackName)
		if err := h.pulumiExec.Destroy(jobID); err != nil {
			log.Printf("Stack destruction failed for job %s: %v", jobID, err)
			h.jobManager.SetError(jobID, fmt.Errorf("destroy failed: %w", err))
			// Persist failed job to disk
			if saveErr := h.jobManager.SaveJob(jobID); saveErr != nil {
				log.Printf("Warning: failed to persist failed job %s: %v", jobID, saveErr)
				// Don't fail the job if persistence fails
			}
			return
		}
		log.Printf("Stack destruction completed for job: %s", jobID)

		// Mark job as destroyed instead of removing it
		if err := h.jobManager.UpdateJobStatus(jobID, JobStatusDestroyed); err != nil {
			log.Printf("Warning: failed to mark job %s as destroyed: %v", jobID, err)
		} else {
			// Persist the destroyed job
			if err := h.jobManager.SaveJob(jobID); err != nil {
				log.Printf("Warning: failed to persist destroyed job %s: %v", jobID, err)
			}
		}
	}()

	// Redirect to admin page to view destroy progress (like CreateLab)
	http.Redirect(w, r, fmt.Sprintf("/admin?job=%s", jobID), http.StatusSeeOther)
}

// RecreateLab handles recreating a lab from a destroyed job
func (h *Handler) RecreateLab(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse form data
	if err := r.ParseForm(); err != nil {
		log.Printf("Failed to parse form: %v", err)
		http.Error(w, fmt.Sprintf("Failed to parse form: %v", err), http.StatusBadRequest)
		return
	}

	jobID := r.FormValue("job_id")
	if jobID == "" {
		http.Error(w, "job_id is required", http.StatusBadRequest)
		return
	}

	// Get the destroyed job
	job, exists := h.jobManager.GetJob(jobID)
	if !exists {
		http.Error(w, "Job not found", http.StatusNotFound)
		return
	}

	// Check if job is destroyed
	job.mu.RLock()
	status := job.Status
	config := job.Config
	job.mu.RUnlock()

	if status != JobStatusDestroyed {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `
			<div class="error-message">
				<h3>Invalid Job Status</h3>
				<p>This job is not destroyed. Current status: %s</p>
				<p>Only destroyed jobs can be recreated.</p>
			</div>`, status)
		return
	}

	if config == nil {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `
			<div class="error-message">
				<h3>No Configuration Available</h3>
				<p>This job does not have configuration data available for recreation.</p>
			</div>`)
		return
	}

	// Get OVH credentials from in-memory storage (they might have changed)
	ovhCreds, err := h.getOVHCredentials(w)
	if err != nil {
		return
	}

	// Update config with current credentials (in case they changed)
	config.OvhApplicationKey = ovhCreds.ApplicationKey
	config.OvhApplicationSecret = ovhCreds.ApplicationSecret
	config.OvhConsumerKey = ovhCreds.ConsumerKey
	config.OvhServiceName = ovhCreds.ServiceName
	config.OvhEndpoint = ovhCreds.Endpoint

	// Create new job with the same configuration
	newJobID := h.jobManager.CreateJob(config)
	log.Printf("Recreating lab from destroyed job %s as new job: %s", jobID, newJobID)

	// Start Pulumi execution in a goroutine
	go func() {
		log.Printf("Starting Pulumi execution for recreated job: %s", newJobID)
		if err := h.pulumiExec.Execute(newJobID); err != nil {
			log.Printf("Pulumi execution failed for recreated job %s: %v", newJobID, err)
			return
		}
		log.Printf("Pulumi execution completed for recreated job: %s", newJobID)
	}()

	// Redirect to admin page to view recreation progress (like CreateLab)
	http.Redirect(w, r, fmt.Sprintf("/admin?job=%s", newJobID), http.StatusSeeOther)
}

// RetryJob handles retrying a failed job
func (h *Handler) RetryJob(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract job ID from path like /api/jobs/{id}/retry or /api/labs/{id}/retry
	pathParts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(pathParts) < 4 || pathParts[0] != "api" || (pathParts[1] != "jobs" && pathParts[1] != "labs") || pathParts[3] != "retry" {
		log.Printf("Invalid path for job retry: %s", r.URL.Path)
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}
	jobID := pathParts[2]
	log.Printf("RetryJob called for job: %s", jobID)

	// Get the job
	job, exists := h.jobManager.GetJob(jobID)
	if !exists {
		log.Printf("Job not found: %s", jobID)
		http.Error(w, "Job not found", http.StatusNotFound)
		return
	}

	// Check if job is failed
	job.mu.RLock()
	status := job.Status
	config := job.Config
	job.mu.RUnlock()

	if status != JobStatusFailed {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `
			<div class="error-message">
				<h3>Invalid Job Status</h3>
				<p>This job is not in failed status. Current status: %s</p>
				<p>Only failed jobs can be retried.</p>
			</div>`, status)
		return
	}

	if config == nil {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `
			<div class="error-message">
				<h3>No Configuration Available</h3>
				<p>This job does not have configuration data available for retry.</p>
			</div>`)
		return
	}

	// Get OVH credentials from in-memory storage (they might have changed)
	ovhCreds, err := h.getOVHCredentials(w)
	if err != nil {
		return
	}

	// Update config with current credentials (in case they changed)
	config.OvhApplicationKey = ovhCreds.ApplicationKey
	config.OvhApplicationSecret = ovhCreds.ApplicationSecret
	config.OvhConsumerKey = ovhCreds.ConsumerKey
	config.OvhServiceName = ovhCreds.ServiceName
	config.OvhEndpoint = ovhCreds.Endpoint

	// Reset job for retry
	if err := h.jobManager.ResetJobForRetry(jobID); err != nil {
		log.Printf("Failed to reset job for retry: %v", err)
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `
			<div class="error-message">
				<h3>Failed to Reset Job</h3>
				<p>%s</p>
			</div>`, template.HTMLEscapeString(err.Error()))
		return
	}

	// Update job config with current credentials
	job.mu.Lock()
	job.Config = config
	job.mu.Unlock()

	// Add retry message to output
	h.jobManager.AppendOutput(jobID, fmt.Sprintf("Retrying job at %s", time.Now().Format(time.RFC3339)))

	// Start Pulumi execution in a goroutine using retry-optimized path
	go func() {
		log.Printf("Starting Pulumi execution for retried job: %s", jobID)
		if err := h.pulumiExec.ExecuteRetry(jobID); err != nil {
			log.Printf("Pulumi execution failed for retried job %s: %v", jobID, err)
			return
		}
		log.Printf("Pulumi execution completed for retried job: %s", jobID)
	}()

	// Return job status div for HTMX to display with proper polling
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, `
		<div class="job-created">
			<h3>Job Retried: %s</h3>
			<div id="job-status" hx-get="/api/jobs/%s/status" hx-trigger="load, every 10s" hx-swap="innerHTML">
				<p>Loading status...</p>
			</div>
		</div>`, jobID, jobID)
}
