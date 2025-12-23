package server

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io"
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
	job.mu.RUnlock()

	w.Header().Set("Content-Type", "text/html")

	var statusHTML strings.Builder
	statusHTML.WriteString(`<div class="job-status">`)
	statusHTML.WriteString(fmt.Sprintf(`<div class="status-badge status-%s">%s</div>`, status, status))

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
