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
)

// Handler handles HTTP requests
type Handler struct {
	jobManager    *JobManager
	pulumiExec    *PulumiExecutor
	templateCache *template.Template
}

// NewHandler creates a new HTTP handler
func NewHandler(jobManager *JobManager, pulumiExec *PulumiExecutor) *Handler {
	return &Handler{
		jobManager: jobManager,
		pulumiExec: pulumiExec,
	}
}

// ServeUI serves the main HTML UI
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
	log.Printf("OVH Application Key present: %v", r.FormValue("ovh_application_key") != "")

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

		OvhApplicationKey:    r.FormValue("ovh_application_key"),
		OvhApplicationSecret: r.FormValue("ovh_application_secret"),
		OvhConsumerKey:       r.FormValue("ovh_consumer_key"),
		OvhServiceName:       r.FormValue("ovh_service_name"),

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

		OvhEndpoint: r.FormValue("ovh_endpoint"),
	}

	// Validate required fields
	if config.OvhApplicationKey == "" || config.OvhApplicationSecret == "" ||
		config.OvhConsumerKey == "" || config.OvhServiceName == "" {
		http.Error(w, "Missing required OVH credentials", http.StatusBadRequest)
		return
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
