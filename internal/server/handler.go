package server

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"labascode/coder"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Handler handles HTTP requests
type Handler struct {
	jobManager         *JobManager
	pulumiExec         *PulumiExecutor
	templateCache      *template.Template
	credentialsManager *CredentialsManager
}

// NewHandler creates a new HTTP handler
func NewHandler(jobManager *JobManager, pulumiExec *PulumiExecutor, credentialsManager *CredentialsManager) *Handler {
	return &Handler{
		jobManager:         jobManager,
		pulumiExec:         pulumiExec,
		credentialsManager: credentialsManager,
	}
}

// ServeUI serves the main HTML UI (homepage)
func (h *Handler) ServeUI(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	// Read and parse template
	tmplPath := filepath.Join("web", "index.html")
	tmpl, err := template.ParseFiles(tmplPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load template: %v", err), http.StatusInternalServerError)
		return
	}

	// Prevent caching
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	if err := tmpl.Execute(w, nil); err != nil {
		http.Error(w, fmt.Sprintf("Failed to execute template: %v", err), http.StatusInternalServerError)
		return
	}
}

// ServeAdminUI serves the admin HTML UI
func (h *Handler) ServeAdminUI(w http.ResponseWriter, r *http.Request) {
	// Read and parse template
	tmplPath := filepath.Join("web", "admin.html")
	tmpl, err := template.ParseFiles(tmplPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load template: %v", err), http.StatusInternalServerError)
		return
	}

	// Check if credentials are configured
	hasCredentials := h.credentialsManager.HasCredentials()

	// Prevent caching
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	data := map[string]interface{}{
		"HasCredentials": hasCredentials,
	}

	if err := tmpl.Execute(w, data); err != nil {
		http.Error(w, fmt.Sprintf("Failed to execute template: %v", err), http.StatusInternalServerError)
		return
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

	// Parse form data - handle both multipart and urlencoded
	contentType := r.Header.Get("Content-Type")
	log.Printf("Content-Type: %s", contentType)

	if strings.HasPrefix(contentType, "multipart/form-data") {
		if err := r.ParseMultipartForm(10 << 20); err != nil { // 10MB max
			log.Printf("Failed to parse multipart form: %v", err)
			http.Error(w, fmt.Sprintf("Failed to parse form: %v", err), http.StatusBadRequest)
			return
		}
	} else {
		if err := r.ParseForm(); err != nil {
			log.Printf("Failed to parse form: %v", err)
			http.Error(w, fmt.Sprintf("Failed to parse form: %v", err), http.StatusBadRequest)
			return
		}
	}
	log.Printf("Form parsed successfully")

	// Get OVH credentials from in-memory storage
	ovhCreds, err := h.credentialsManager.GetCredentials()
	if err != nil {
		log.Printf("OVH credentials not configured: %v", err)
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `
			<div class="error-message">
				<h3>OVH Credentials Not Configured</h3>
				<p>Please configure your OVH credentials first before creating a lab.</p>
				<a href="/ovh-credentials" class="btn btn-primary">Configure OVH Credentials</a>
			</div>`)
		return
	}

	// Parse integer fields
	desiredNodeCount, _ := strconv.Atoi(r.FormValue("nodepool_desired_node_count"))
	minNodeCount, _ := strconv.Atoi(r.FormValue("nodepool_min_node_count"))
	maxNodeCount, _ := strconv.Atoi(r.FormValue("nodepool_max_node_count"))

	// Get stack name with default
	stackName := r.FormValue("stack_name")
	if stackName == "" {
		stackName = "dev"
	}

	config := &LabConfig{
		StackName: stackName,

		// Use credentials from in-memory storage
		OvhApplicationKey:    ovhCreds.ApplicationKey,
		OvhApplicationSecret: ovhCreds.ApplicationSecret,
		OvhConsumerKey:       ovhCreds.ConsumerKey,
		OvhServiceName:       ovhCreds.ServiceName,
		OvhEndpoint:          ovhCreds.Endpoint,

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
	}

	// Create job
	jobID := h.jobManager.CreateJob(config)
	log.Printf("Job created: %s", jobID)

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
			<h3>Job Created: %s</h3>
			<div id="job-status" hx-get="/api/jobs/%s/status" hx-trigger="load, every 2s" hx-swap="innerHTML">
				<p>Loading status...</p>
			</div>
		</div>`, jobID, jobID)
}

// DryRunLab handles dry run requests
func (h *Handler) DryRunLab(w http.ResponseWriter, r *http.Request) {
	log.Printf("DryRunLab called: method=%s, path=%s", r.Method, r.URL.Path)

	if r.Method != http.MethodPost {
		log.Printf("Method not allowed: %s", r.Method)
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse form data - handle both multipart and urlencoded
	contentType := r.Header.Get("Content-Type")
	log.Printf("Content-Type: %s", contentType)

	if strings.HasPrefix(contentType, "multipart/form-data") {
		if err := r.ParseMultipartForm(10 << 20); err != nil { // 10MB max
			log.Printf("Failed to parse multipart form: %v", err)
			http.Error(w, fmt.Sprintf("Failed to parse form: %v", err), http.StatusBadRequest)
			return
		}
	} else {
		if err := r.ParseForm(); err != nil {
			log.Printf("Failed to parse form: %v", err)
			http.Error(w, fmt.Sprintf("Failed to parse form: %v", err), http.StatusBadRequest)
			return
		}
	}
	log.Printf("Form parsed successfully")

	// Get OVH credentials from in-memory storage
	ovhCreds, err := h.credentialsManager.GetCredentials()
	if err != nil {
		log.Printf("OVH credentials not configured: %v", err)
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `
			<div class="error-message">
				<h3>OVH Credentials Not Configured</h3>
				<p>Please configure your OVH credentials first before running a dry run.</p>
				<a href="/ovh-credentials" class="btn btn-primary">Configure OVH Credentials</a>
			</div>`)
		return
	}

	// Parse integer fields
	desiredNodeCount, _ := strconv.Atoi(r.FormValue("nodepool_desired_node_count"))
	minNodeCount, _ := strconv.Atoi(r.FormValue("nodepool_min_node_count"))
	maxNodeCount, _ := strconv.Atoi(r.FormValue("nodepool_max_node_count"))

	// Get stack name with default
	stackName := r.FormValue("stack_name")
	if stackName == "" {
		stackName = "dev"
	}

	config := &LabConfig{
		StackName: stackName,

		// Use credentials from in-memory storage
		OvhApplicationKey:    ovhCreds.ApplicationKey,
		OvhApplicationSecret: ovhCreds.ApplicationSecret,
		OvhConsumerKey:       ovhCreds.ConsumerKey,
		OvhServiceName:       ovhCreds.ServiceName,
		OvhEndpoint:          ovhCreds.Endpoint,

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
	}

	// Create job
	jobID := h.jobManager.CreateJob(config)
	log.Printf("Dry run job created: %s", jobID)

	// Start Pulumi preview in a goroutine
	go func() {
		log.Printf("Starting Pulumi preview for job: %s", jobID)
		if err := h.pulumiExec.Preview(jobID); err != nil {
			log.Printf("Pulumi preview failed for job %s: %v", jobID, err)
			return
		}
		log.Printf("Pulumi preview completed for job: %s", jobID)
	}()

	// Return job status div for HTMX to display with proper polling
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, `
		<div class="job-created">
			<h3>Dry Run Started: %s</h3>
			<div id="job-status" hx-get="/api/jobs/%s/status" hx-trigger="load, every 2s" hx-swap="innerHTML">
				<p>Loading status...</p>
			</div>
		</div>`, jobID, jobID)
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
			<div id="job-status" hx-get="/api/jobs/%s/status" hx-trigger="load, every 2s" hx-swap="innerHTML">
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

	// Show download button if job completed successfully and kubeconfig is available
	if status == JobStatusCompleted && kubeconfig != "" {
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
		statusHTML.WriteString(fmt.Sprintf(`<div hx-get="/api/jobs/%s/status" hx-trigger="every 2s" hx-swap="outerHTML"></div>`, jobID))
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

	if status != JobStatusCompleted {
		http.Error(w, "Job not completed", http.StatusBadRequest)
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
	tmplPath := filepath.Join("web", "student-dashboard.html")
	tmpl, err := template.ParseFiles(tmplPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load template: %v", err), http.StatusInternalServerError)
		return
	}

	// Prevent caching
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	if err := tmpl.Execute(w, nil); err != nil {
		http.Error(w, fmt.Sprintf("Failed to execute template: %v", err), http.StatusInternalServerError)
	}
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
	contentType := r.Header.Get("Content-Type")
	if strings.Contains(contentType, "multipart/form-data") {
		if err := r.ParseMultipartForm(32 << 20); err != nil { // 32MB max
			log.Printf("Failed to parse multipart form: %v", err)
			http.Error(w, fmt.Sprintf("Failed to parse form: %v", err), http.StatusBadRequest)
			return
		}
	} else {
		if err := r.ParseForm(); err != nil {
			log.Printf("Failed to parse form: %v", err)
			http.Error(w, fmt.Sprintf("Failed to parse form: %v", err), http.StatusBadRequest)
			return
		}
	}

	// Try PostFormValue first (POST-only), then FormValue (includes query params)
	email := r.PostFormValue("email")
	if email == "" {
		email = r.FormValue("email")
	}

	labID := r.PostFormValue("lab_id")
	if labID == "" {
		labID = r.FormValue("lab_id")
	}

	// Debug logging - show all form values
	log.Printf("RequestWorkspace - Content-Type: %s", contentType)
	log.Printf("RequestWorkspace - PostForm: %v", r.PostForm)
	log.Printf("RequestWorkspace - Form: %v", r.Form)
	log.Printf("RequestWorkspace - Email: %q, LabID: %q", email, labID)

	// Validate email
	if email == "" {
		log.Printf("RequestWorkspace - Email validation failed. Available form keys: %v", getFormKeys(r))
		http.Error(w, fmt.Sprintf("Email is required. Received form data: %v", getFormKeys(r)), http.StatusBadRequest)
		return
	}

	// Validate lab ID
	if labID == "" {
		log.Printf("RequestWorkspace - LabID validation failed. Available form keys: %v", getFormKeys(r))
		http.Error(w, fmt.Sprintf("Lab ID is required. Received form data: %v", getFormKeys(r)), http.StatusBadRequest)
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
	job.mu.RUnlock()

	if status != JobStatusCompleted {
		http.Error(w, "Lab is not ready yet", http.StatusBadRequest)
		return
	}

	if coderURL == "" || coderSessionToken == "" || coderOrganizationID == "" {
		http.Error(w, "Coder configuration not available for this lab", http.StatusInternalServerError)
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

	// Get available templates
	templates, err := coder.GetTemplates(coderConfig)
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

	// Create user in Coder
	user, err := coder.CreateUser(coderConfig, email, username, password)
	if err != nil {
		log.Printf("Failed to create user: %v", err)
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<div class="error-message">Failed to create user: %s</div>`, template.HTMLEscapeString(err.Error()))
		return
	}

	// Create workspace for user
	workspaceName := fmt.Sprintf("%s-workspace", username)
	workspace, err := coder.CreateWorkspace(coderConfig, user.ID, templateID, workspaceName)
	if err != nil {
		log.Printf("Failed to create workspace: %v", err)
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<div class="error-message">Failed to create workspace: %s</div>`, template.HTMLEscapeString(err.Error()))
		return
	}

	// Build workspace URL
	workspaceURL := fmt.Sprintf("%s/@%s/%s", coderURL, user.Username, workspace.Name)

	// Return success response with credentials
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

// ServeOVHCredentials serves the OVH credentials configuration page
func (h *Handler) ServeOVHCredentials(w http.ResponseWriter, r *http.Request) {
	tmplPath := filepath.Join("web", "ovh-credentials.html")
	tmpl, err := template.ParseFiles(tmplPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load template: %v", err), http.StatusInternalServerError)
		return
	}

	// Get current credentials status (without exposing secrets)
	var currentCreds map[string]interface{}
	creds, err := h.credentialsManager.GetCredentials()
	if err == nil {
		currentCreds = map[string]interface{}{
			"configured":   true,
			"service_name": creds.ServiceName,
			"endpoint":     creds.Endpoint,
		}
	} else {
		currentCreds = map[string]interface{}{
			"configured": false,
		}
	}

	// Prevent caching
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	data := map[string]interface{}{
		"CurrentCreds": currentCreds,
	}

	if err := tmpl.Execute(w, data); err != nil {
		http.Error(w, fmt.Sprintf("Failed to execute template: %v", err), http.StatusInternalServerError)
	}
}

// SetOVHCredentials handles setting OVH credentials
func (h *Handler) SetOVHCredentials(w http.ResponseWriter, r *http.Request) {
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
	contentType := r.Header.Get("Content-Type")
	log.Printf("SetOVHCredentials - Content-Type: %s", contentType)

	if strings.HasPrefix(contentType, "multipart/form-data") {
		if err := r.ParseMultipartForm(10 << 20); err != nil { // 10MB max
			log.Printf("Failed to parse multipart form: %v", err)
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `
				<div class="error-message">
					<h3>Failed to Parse Form</h3>
					<p>%s</p>
				</div>`, template.HTMLEscapeString(err.Error()))
			return
		}
	} else {
		// Handle application/x-www-form-urlencoded (default for HTMX)
		if err := r.ParseForm(); err != nil {
			log.Printf("Failed to parse form: %v", err)
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `
				<div class="error-message">
					<h3>Failed to Parse Form</h3>
					<p>%s</p>
				</div>`, template.HTMLEscapeString(err.Error()))
			return
		}
	}

	// Debug: log received form values (without secrets)
	log.Printf("SetOVHCredentials - Form keys: %v", getFormKeys(r))

	// Try PostFormValue first (POST-only), then FormValue (includes query params)
	applicationKey := r.PostFormValue("ovh_application_key")
	if applicationKey == "" {
		applicationKey = r.FormValue("ovh_application_key")
	}

	applicationSecret := r.PostFormValue("ovh_application_secret")
	if applicationSecret == "" {
		applicationSecret = r.FormValue("ovh_application_secret")
	}

	consumerKey := r.PostFormValue("ovh_consumer_key")
	if consumerKey == "" {
		consumerKey = r.FormValue("ovh_consumer_key")
	}

	serviceName := r.PostFormValue("ovh_service_name")
	if serviceName == "" {
		serviceName = r.FormValue("ovh_service_name")
	}

	endpoint := r.PostFormValue("ovh_endpoint")
	if endpoint == "" {
		endpoint = r.FormValue("ovh_endpoint")
	}

	log.Printf("SetOVHCredentials - Service Name present: %v, value: %s", serviceName != "", serviceName)
	log.Printf("SetOVHCredentials - Endpoint present: %v, value: %s", endpoint != "", endpoint)
	log.Printf("SetOVHCredentials - ApplicationKey present: %v", applicationKey != "")
	log.Printf("SetOVHCredentials - ApplicationSecret present: %v", applicationSecret != "")
	log.Printf("SetOVHCredentials - ConsumerKey present: %v", consumerKey != "")

	creds := &OVHCredentials{
		ApplicationKey:    applicationKey,
		ApplicationSecret: applicationSecret,
		ConsumerKey:       consumerKey,
		ServiceName:       serviceName,
		Endpoint:          endpoint,
	}

	// Debug: log what we're trying to save (without secrets)
	log.Printf("SetOVHCredentials - Attempting to save credentials for service: %s, endpoint: %s", creds.ServiceName, creds.Endpoint)
	log.Printf("SetOVHCredentials - ApplicationKey empty: %v, ApplicationSecret empty: %v, ConsumerKey empty: %v",
		creds.ApplicationKey == "", creds.ApplicationSecret == "", creds.ConsumerKey == "")

	if err := h.credentialsManager.SetCredentials(creds); err != nil {
		log.Printf("Failed to set credentials: %v", err)
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `
			<div class="error-message">
				<h3>Failed to Save Credentials</h3>
				<p>%s</p>
				<p><small>Please ensure all fields are filled correctly.</small></p>
			</div>`, template.HTMLEscapeString(err.Error()))
		return
	}

	// Verify credentials were saved
	verifyCreds, verifyErr := h.credentialsManager.GetCredentials()
	if verifyErr != nil {
		log.Printf("Warning: Credentials saved but verification failed: %v", verifyErr)
	} else {
		log.Printf("Credentials verified - Service: %s, Endpoint: %s", verifyCreds.ServiceName, verifyCreds.Endpoint)
	}

	log.Printf("OVH credentials saved successfully for service: %s", creds.ServiceName)
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, `
		<div class="success-message">
			<p>✅ Credentials saved successfully</p>
		</div>`)
}

// GetOVHCredentials handles getting OVH credentials status
func (h *Handler) GetOVHCredentials(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	creds, err := h.credentialsManager.GetCredentials()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]bool{"configured": false})
		return
	}

	// Return limited info (not the actual secrets)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"configured":          true,
		"service_name":        creds.ServiceName,
		"endpoint":            creds.Endpoint,
		"has_application_key": creds.ApplicationKey != "",
		"has_consumer_key":    creds.ConsumerKey != "",
	})
}
