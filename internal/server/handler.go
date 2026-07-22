package server

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"easylab/internal/providers/workspace"
	"easylab/internal/tfparse"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	dnsregistry "easylab/internal/providers/dns"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

// Handler handles HTTP requests
type Handler struct {
	jobManager *JobManager
	pulumiExec *PulumiExecutor
	// newWorkspaceBackend builds the workspace backend for a lab from its kubeconfig
	// and namespace. Overridable in tests to inject a fake backend.
	newWorkspaceBackend func(kubeconfig, namespace string) (workspace.Backend, error)
	templates           map[string]*template.Template
	templatesMu         sync.RWMutex
	credentialsManager  *CredentialsManager
	ovhOptionsManager   *OVHOptionsManager
	azureOptionsManager *AzureOptionsManager
	feedbackStore       *FeedbackStore
	// pendingSecrets holds credentials captured in the creation wizard until the
	// lab's cluster exists to receive them (see pending_secrets.go).
	pendingSecrets              *pendingSecretStore
	azureADConfigurer           func(clientID, clientSecret, tenantID string)
	classicLoginConfigurer      func(disabled bool)
	adminGroupIDConfigurer      func(groupID string)
	classicAdminLoginConfigurer func(disabled bool)
}

// SetAzureADConfigurer wires a callback so the handler can update Azure AD OAuth config at runtime.
func (h *Handler) SetAzureADConfigurer(fn func(clientID, clientSecret, tenantID string)) {
	h.azureADConfigurer = fn
}

// SetClassicLoginConfigurer wires a callback to enable/disable password-based student login at runtime.
func (h *Handler) SetClassicLoginConfigurer(fn func(disabled bool)) {
	h.classicLoginConfigurer = fn
}

// SetAdminGroupIDConfigurer wires a callback so the handler can update the admin Azure AD group ID at runtime.
func (h *Handler) SetAdminGroupIDConfigurer(fn func(groupID string)) {
	h.adminGroupIDConfigurer = fn
}

// SetClassicAdminLoginConfigurer wires a callback to enable/disable password-based admin login at runtime.
func (h *Handler) SetClassicAdminLoginConfigurer(fn func(disabled bool)) {
	h.classicAdminLoginConfigurer = fn
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

// atoiForm parses s as an int; returns 0 on empty or invalid input.
func atoiForm(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// NewHandler creates a new HTTP handler
func NewHandler(jobManager *JobManager, pulumiExec *PulumiExecutor, credentialsManager *CredentialsManager, ovhOptionsManager *OVHOptionsManager, azureOptionsManager *AzureOptionsManager, feedbackStore *FeedbackStore) *Handler {
	h := &Handler{
		jobManager:          jobManager,
		pulumiExec:          pulumiExec,
		newWorkspaceBackend: workspace.Default,
		templates:           make(map[string]*template.Template),
		credentialsManager:  credentialsManager,
		ovhOptionsManager:   ovhOptionsManager,
		azureOptionsManager: azureOptionsManager,
		feedbackStore:       feedbackStore,
		pendingSecrets:      newPendingSecretStore(),
	}
	// Credentials captured in the wizard are written once the lab's cluster is up.
	// The executor owns that moment; the handler owns the cluster connection — so
	// the executor calls back here rather than growing a backend of its own.
	if pulumiExec != nil {
		pulumiExec.afterProvision = h.applyPendingSecrets
	}
	return h
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
			return fmt.Errorf("failed to parse multipart form: %w", err)
		}
		log.Printf("Form parsed successfully (multipart)")
		return nil
	}

	// Handle application/x-www-form-urlencoded or missing Content-Type
	// ParseForm works for both urlencoded forms and can handle missing Content-Type
	// by detecting the form data in the request body
	if err := r.ParseForm(); err != nil {
		log.Printf("Failed to parse form: %v", err)
		return fmt.Errorf("failed to parse form: %w", err)
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

// parseWorkspaceTemplatesFromForm extracts workspace template entries from the form.
// Expects template_N_name, template_N_image, template_N_git_repo, template_N_cpu,
// template_N_memory, template_N_disk_size, and repeated template_N_env_name /
// template_N_env_value pairs.
func parseWorkspaceTemplatesFromForm(r *http.Request) []WorkspaceTemplate {
	var templates []WorkspaceTemplate
	for i := 0; ; i++ {
		name := getFormValue(r, fmt.Sprintf("template_%d_name", i))
		if name == "" {
			break
		}
		t := WorkspaceTemplate{
			Name:          name,
			IDE:           getFormValue(r, fmt.Sprintf("template_%d_ide", i)),
			Image:         getFormValue(r, fmt.Sprintf("template_%d_image", i)),
			GitRepo:       getFormValue(r, fmt.Sprintf("template_%d_git_repo", i)),
			GitBranch:     getFormValue(r, fmt.Sprintf("template_%d_git_branch", i)),
			GitFolder:     getFormValue(r, fmt.Sprintf("template_%d_git_folder", i)),
			CPU:           getFormValue(r, fmt.Sprintf("template_%d_cpu", i)),
			Memory:        getFormValue(r, fmt.Sprintf("template_%d_memory", i)),
			DiskSize:      getFormValue(r, fmt.Sprintf("template_%d_disk_size", i)),
			StartupScript: getFormValue(r, fmt.Sprintf("template_%d_startup_script", i)),
			DotfilesRepo:  getFormValue(r, fmt.Sprintf("template_%d_dotfiles_repo", i)),
			Extensions:    splitList(getFormValue(r, fmt.Sprintf("template_%d_extensions", i))),
			GitAuthSecret: getFormValue(r, fmt.Sprintf("template_%d_git_auth_secret", i)),
		}

		envNames := r.Form[fmt.Sprintf("template_%d_env_name", i)]
		envValues := r.Form[fmt.Sprintf("template_%d_env_value", i)]
		if len(envNames) > 0 {
			t.Env = make(map[string]string)
			for j, en := range envNames {
				en = strings.TrimSpace(en)
				if en == "" {
					continue
				}
				ev := ""
				if j < len(envValues) {
					ev = envValues[j]
				}
				t.Env[en] = ev
			}
		}

		t.Sidecars = parseSidecarsFromForm(r, i)
		t.Mounts = parseMountsFromForm(r, i)

		templates = append(templates, t)
	}
	return templates
}

// splitList splits a comma/newline-separated string into trimmed, non-empty items.
func splitList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	fields := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' })
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// parseSidecarsFromForm reads index-aligned template_N_sidecar_* arrays.
// Ports are comma-separated ints; env is comma-separated KEY=VAL pairs.
func parseSidecarsFromForm(r *http.Request, i int) []WorkspaceSidecar {
	names := r.Form[fmt.Sprintf("template_%d_sidecar_name", i)]
	images := r.Form[fmt.Sprintf("template_%d_sidecar_image", i)]
	ports := r.Form[fmt.Sprintf("template_%d_sidecar_ports", i)]
	envs := r.Form[fmt.Sprintf("template_%d_sidecar_env", i)]
	privileged := r.Form[fmt.Sprintf("template_%d_sidecar_privileged", i)]
	capabilities := r.Form[fmt.Sprintf("template_%d_sidecar_capabilities", i)]
	var sidecars []WorkspaceSidecar
	for j, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		sc := WorkspaceSidecar{Name: n}
		if j < len(images) {
			sc.Image = strings.TrimSpace(images[j])
		}
		if j < len(ports) {
			for _, p := range splitList(ports[j]) {
				if n, err := strconv.Atoi(p); err == nil {
					sc.Ports = append(sc.Ports, n)
				}
			}
		}
		if j < len(envs) {
			for _, kv := range splitList(envs[j]) {
				if k, v, ok := strings.Cut(kv, "="); ok {
					if sc.Env == nil {
						sc.Env = map[string]string{}
					}
					sc.Env[strings.TrimSpace(k)] = strings.TrimSpace(v)
				}
			}
		}
		if j < len(privileged) {
			sc.Privileged = privileged[j] == "true"
		}
		if j < len(capabilities) {
			sc.Capabilities = splitList(capabilities[j])
		}
		sidecars = append(sidecars, sc)
	}
	return sidecars
}

// parseMountsFromForm reads index-aligned template_N_mount_* arrays.
func parseMountsFromForm(r *http.Request, i int) []WorkspaceMount {
	types := r.Form[fmt.Sprintf("template_%d_mount_type", i)]
	names := r.Form[fmt.Sprintf("template_%d_mount_name", i)]
	paths := r.Form[fmt.Sprintf("template_%d_mount_path", i)]
	var mounts []WorkspaceMount
	for j, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		m := WorkspaceMount{Name: n}
		if j < len(types) {
			m.Type = strings.TrimSpace(types[j])
		}
		if j < len(paths) {
			m.Path = strings.TrimSpace(paths[j])
		}
		if m.Path == "" {
			continue
		}
		mounts = append(mounts, m)
	}
	return mounts
}

// toWorkspaceSidecars maps server sidecar structs to backend types.
func toWorkspaceSidecars(in []WorkspaceSidecar) []workspace.Sidecar {
	if len(in) == 0 {
		return nil
	}
	out := make([]workspace.Sidecar, 0, len(in))
	for _, s := range in {
		out = append(out, workspace.Sidecar{
			Name:         s.Name,
			Image:        s.Image,
			Ports:        s.Ports,
			Env:          s.Env,
			Privileged:   s.Privileged,
			Capabilities: s.Capabilities,
		})
	}
	return out
}

// toWorkspaceMounts maps server mount structs to backend types.
func toWorkspaceMounts(in []WorkspaceMount) []workspace.Mount {
	if len(in) == 0 {
		return nil
	}
	out := make([]workspace.Mount, 0, len(in))
	for _, m := range in {
		out = append(out, workspace.Mount{Type: m.Type, Name: m.Name, Path: m.Path})
	}
	return out
}

// toWorkspaceDevcontainer maps the template's devcontainer config to the backend
// type. A disabled block is the same as none: the template's image is used.
func toWorkspaceDevcontainer(in *DevcontainerConfig) *workspace.DevcontainerSpec {
	if in == nil || !in.Enabled {
		return nil
	}
	return &workspace.DevcontainerSpec{
		Dir:                in.Dir,
		CacheRepo:          in.CacheRepo,
		RegistryAuthSecret: in.RegistryAuthSecret,
		FallbackImage:      in.FallbackImage,
		Insecure:           in.Insecure,
	}
}

// createLabConfigFromForm creates a LabConfig from form data and provider credentials.
func (h *Handler) createLabConfigFromForm(r *http.Request, providerCreds ProviderCredentials) *LabConfig {
	// Get stack name with default
	stackName := r.FormValue("stack_name")
	if stackName == "" {
		stackName = "dev"
	}

	useExistingCluster := r.FormValue("use_existing_cluster") == "true"

	templates := parseWorkspaceTemplatesFromForm(r)

	config := &LabConfig{
		StackName:          stackName,
		UseExistingCluster: useExistingCluster,

		WorkspaceNamespace: r.FormValue("workspace_namespace"),
		WorkspaceTemplates: templates,

		Domain:         r.FormValue("domain"),
		AcmeEmail:      r.FormValue("acme_email"),
		WildcardDomain: r.FormValue("wildcard_domain"),

		DNSProvider: r.FormValue("dns_provider"),
		DNSZone:     r.FormValue("dns_zone"),
	}

	installNginx := r.FormValue("install_nginx_ingress") == "true"
	config.InstallNginxIngress = &installNginx
	if !installNginx {
		config.NginxIngressNamespace = r.FormValue("nginx_ingress_namespace")
		config.NginxIngressServiceName = r.FormValue("nginx_ingress_service_name")
	}
	installCertM := r.FormValue("install_cert_manager") == "true"
	config.InstallCertManager = &installCertM
	if !installCertM {
		config.CertManagerNamespace = r.FormValue("cert_manager_namespace")
	}

	if config.DNSProvider != "" {
		if dnsP, _ := dnsregistry.Get(config.DNSProvider); dnsP != nil {
			config.DNSCredentials = make(map[string]string)
			for _, f := range dnsP.GetCredentialFields() {
				config.DNSCredentials[f.Name] = r.FormValue("dns_cred_" + f.Name)
			}
		}
	}

	workspaceLifetime, _ := strconv.Atoi(r.FormValue("workspace_lifetime_hours"))
	if r.FormValue("workspace_lifetime_unit") == "days" {
		workspaceLifetime *= 24
	}
	config.WorkspaceLifetimeHours = workspaceLifetime

	if labDeletionDateStr := r.FormValue("lab_deletion_date"); labDeletionDateStr != "" {
		if d, err := time.Parse("2006-01-02", labDeletionDateStr); err == nil {
			hour, minute := 23, 59
			if labDeletionTimeStr := r.FormValue("lab_deletion_time"); labDeletionTimeStr != "" {
				if t, err := time.Parse("15:04", labDeletionTimeStr); err == nil {
					hour, minute = t.Hour(), t.Minute()
				}
			}
			deletion := time.Date(d.Year(), d.Month(), d.Day(), hour, minute, 0, 0, time.Local)
			config.LabDeletionDate = &deletion
		}
	}

	if !useExistingCluster {
		// Parse integer fields only for new infrastructure
		desiredNodeCount, _ := strconv.Atoi(r.FormValue("nodepool_desired_node_count"))
		minNodeCount, _ := strconv.Atoi(r.FormValue("nodepool_min_node_count"))
		maxNodeCount, _ := strconv.Atoi(r.FormValue("nodepool_max_node_count"))

		// Get provider from form (default to "ovh" for backward compatibility)
		provider := r.FormValue("provider")
		if provider == "" {
			provider = "ovh"
		}

		config.Provider = provider
		config.NetworkGatewayName = r.FormValue("network_gateway_name")
		config.NetworkGatewayModel = r.FormValue("network_gateway_model")
		config.NetworkPrivateNetworkName = r.FormValue("network_private_network_name")
		config.NetworkRegion = r.FormValue("network_region")
		config.NetworkMask = r.FormValue("network_mask")
		config.NetworkStartIP = r.FormValue("network_start_ip")
		config.NetworkEndIP = r.FormValue("network_end_ip")
		config.NetworkID = r.FormValue("network_id")
		config.K8sClusterName = r.FormValue("k8s_cluster_name")
		config.NodePoolName = r.FormValue("nodepool_name")
		config.NodePoolFlavor = r.FormValue("nodepool_flavor")
		config.NodePoolDesiredNodeCount = desiredNodeCount
		config.NodePoolMinNodeCount = minNodeCount
		config.NodePoolMaxNodeCount = maxNodeCount

		// Copy provider-specific credentials into config
		switch c := providerCreds.(type) {
		case *OVHCredentials:
			config.OvhApplicationKey = c.ApplicationKey
			config.OvhApplicationSecret = c.ApplicationSecret
			config.OvhConsumerKey = c.ConsumerKey
			config.OvhServiceName = c.ServiceName
			config.OvhEndpoint = c.Endpoint
		case *AzureCredentials:
			config.AzureClientID = c.ClientID
			config.AzureClientSecret = c.ClientSecret
			config.AzureTenantID = c.TenantID
			config.AzureSubscriptionID = c.SubscriptionID
			config.AzureLocation = r.FormValue("azure_location")
		}
	}

	// Validate ACME email if provided
	if config.AcmeEmail != "" && !validateEmail(config.AcmeEmail) {
		log.Printf("Warning: Invalid email format in AcmeEmail: %s", config.AcmeEmail)
	}

	// Validate Git repository URLs for workspace templates
	for i, t := range config.WorkspaceTemplates {
		if t.GitRepo != "" && !validateURL(t.GitRepo) {
			log.Printf("Warning: Invalid Git repository URL for template %d: %s", i, t.GitRepo)
		}
	}

	return config
}

// validateDNSConfig rejects a DNS-provider selection that cannot produce a valid
// A record: a missing zone, or a domain that does not sit inside the zone. Without
// this check the empty/mismatched zone only surfaces as an OVH 404 deep inside
// pulumi up (coder/https.go strips the zone off the domain to derive the subdomain).
func validateDNSConfig(cfg *LabConfig) error {
	if cfg.DNSProvider == "" {
		return nil
	}
	zone := strings.TrimSpace(cfg.DNSZone)
	if zone == "" {
		return fmt.Errorf("DNS Zone is required when a DNS provider is selected")
	}
	if cfg.Domain != "" && cfg.Domain != zone && !strings.HasSuffix(cfg.Domain, "."+zone) {
		return fmt.Errorf("domain %q is not inside DNS zone %q", cfg.Domain, zone)
	}
	return nil
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
		log.Printf("Failed to load template %s: %v", templateName, err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return err
	}

	// Prevent caching
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	// Execute the base template which includes all page-specific blocks
	if err := tmpl.ExecuteTemplate(w, "base", data); err != nil {
		log.Printf("Failed to execute template %s: %v", templateName, err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
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
		"student-feedback.html":  "web/student-feedback.html",
		"admin-feedback.html":    "web/admin-feedback.html",
		"credentials.html":       "web/credentials.html",
		"ovh-credentials.html":   "web/ovh-credentials.html", // Keep for backward compatibility
		"ovh-options.html":       "web/ovh-options.html",
		"azure-options.html":     "web/azure-options.html",
		"azure-provider.html":    "web/azure-provider.html",
		"azure-ad.html":          "web/azure-ad.html",
		"labs-list.html":         "web/labs-list.html",
		"lab-workspaces.html":    "web/lab-workspaces.html",
		"admin-stats.html":       "web/admin-stats.html",
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
	if h.ovhOptionsManager != nil {
		cfg := h.ovhOptionsManager.GetConfig()
		data["FlavorMinVCPUs"] = cfg.FlavorMinVCPUs
		data["FlavorMaxVCPUs"] = cfg.FlavorMaxVCPUs
		data["FlavorMinRAM"] = cfg.FlavorMinRAM
		data["FlavorMaxRAM"] = cfg.FlavorMaxRAM
	} else {
		data["FlavorMinVCPUs"] = 0
		data["FlavorMaxVCPUs"] = 0
		data["FlavorMinRAM"] = 0
		data["FlavorMaxRAM"] = 0
	}

	h.serveTemplate(w, "admin.html", data)
}

// processLabRequest handles common lab request processing logic
func (h *Handler) processLabRequest(w http.ResponseWriter, r *http.Request, isDryRun bool) {
	// Parse form data - handle both multipart and urlencoded (50MB for template files)
	if err := h.parseForm(w, r, 50<<20); err != nil {
		log.Printf("Failed to parse form: %v", err)
		h.renderHTMLError(w, "Form Parse Error", "Failed to parse form data, please try again.")
		return
	}

	useExistingCluster := r.FormValue("use_existing_cluster") == "true"

	// Resolve the workspace templates up front: a mistake in the YAML editor must
	// fail here, before any job or directory exists.
	templates, err := workspaceTemplatesFromRequest(r)
	if err != nil {
		log.Printf("Invalid workspace templates YAML: %v", err)
		h.renderHTMLError(w, "Invalid Workspace Templates YAML", err.Error())
		return
	}
	// An admin who validates first sees these in the editor's toast; one who goes
	// straight to Create does not, so leave a trail here. The message names the key
	// and template, never the URL, which is what carries the token.
	for _, warn := range gitCredentialWarnings(templates) {
		log.Printf("Workspace template warning (%s): %s", warn.Key, warn.Message)
	}

	// Credentials the admin entered in the wizard. Parsed here so a mistake fails
	// before any job exists; held aside and applied once the cluster is up (a lab
	// has no cluster to receive them yet). A dry run provisions nothing, so it
	// keeps none.
	wizardSecrets, err := parseWizardSecrets(r)
	if err != nil {
		log.Printf("Invalid wizard credentials: %v", err)
		h.renderHTMLError(w, "Invalid Credentials", err.Error())
		return
	}

	// Form path only: with no per-template field naming a credential, point every
	// template that clones a private repo at the wizard's git credential when there
	// is exactly one. The YAML path names git_auth_secret itself and is authoritative.
	if getFormValue(r, "templates_mode") != "yaml" {
		autolinkGitCredential(templates, wizardSecrets)
	}

	var providerCreds ProviderCredentials
	if !useExistingCluster {
		// Get provider from form (default to "ovh" for backward compatibility)
		provider := r.FormValue("provider")
		if provider == "" {
			provider = "ovh"
		}

		// Get provider credentials from in-memory storage
		var err error
		providerCreds, err = h.getProviderCredentials(w, provider)
		if err != nil {
			return
		}
	}

	// Create the lab config from the form. Workspace templates are pure
	// configuration (image + optional git repo + resources), so there are no
	// files to upload.
	initialConfig := h.createLabConfigFromForm(r, providerCreds)
	initialConfig.WorkspaceTemplates = templates

	// A DNS-provider selection with no (or a mismatched) zone can only fail deep
	// inside pulumi up, after minutes of provisioning. Reject it here, before any
	// job or job directory exists.
	if err := validateDNSConfig(initialConfig); err != nil {
		log.Printf("Invalid DNS configuration: %v", err)
		h.renderHTMLError(w, "DNS Configuration Error", err.Error())
		return
	}

	// Create job and job directory
	jobID := h.jobManager.CreateJob(initialConfig)
	// A dry run provisions no cluster, so its credentials would never be applied
	// and would only sit in memory. Keep them only for a real run.
	if !isDryRun {
		h.pendingSecrets.Put(jobID, wizardSecrets)
	}
	jobDir := filepath.Join(h.pulumiExec.GetWorkDir(), jobID)
	if err := os.MkdirAll(jobDir, 0755); err != nil {
		log.Printf("Failed to create job directory: %v", err)
		h.renderHTMLError(w, "Job Creation Error", "Failed to initialize job, please try again.")
		return
	}

	// Handle kubeconfig for BYOK mode
	if useExistingCluster {
		kubeconfigContent, err := h.readKubeconfigFromForm(r)
		if err != nil {
			log.Printf("Failed to read kubeconfig: %v", err)
			h.renderHTMLError(w, "Kubeconfig Error", "Failed to read kubeconfig, please check the file and try again.")
			return
		}
		if kubeconfigContent == "" {
			h.renderHTMLError(w, "Kubeconfig Required", "Please provide a kubeconfig file or paste its content")
			return
		}
		kubeconfigPath := filepath.Join(jobDir, "external-kubeconfig.yaml")
		if err := os.WriteFile(kubeconfigPath, []byte(kubeconfigContent), 0600); err != nil {
			log.Printf("Failed to write kubeconfig: %v", err)
			h.renderHTMLError(w, "Kubeconfig Error", "Failed to save kubeconfig, please try again.")
			return
		}
		h.updateJobConfig(jobID, func(config *LabConfig) {
			config.ExternalKubeconfig = kubeconfigContent
		})
	}

	_, html := h.executeLabJobWithID(initialConfig, isDryRun, jobID)

	// Return job status div for HTMX to display with proper polling
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprint(w, html)
}

// readKubeconfigFromForm reads kubeconfig content from either a file upload or textarea
func (h *Handler) readKubeconfigFromForm(r *http.Request) (string, error) {
	// Try file upload first
	file, _, err := r.FormFile("kubeconfig_file")
	if err == nil {
		defer file.Close()
		content, err := io.ReadAll(file)
		if err != nil {
			return "", fmt.Errorf("failed to read kubeconfig file: %w", err)
		}
		if len(content) > 0 {
			return string(content), nil
		}
	}

	// Fall back to textarea content
	return r.FormValue("kubeconfig_content"), nil
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
		http.Error(w, "Failed to parse form data", http.StatusBadRequest)
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
		statusHTML.WriteString(`<form style="display: inline-block; margin-left: 1rem;">`)
		statusHTML.WriteString(`<button type="button" class="btn btn-primary" onclick="retryJob('` + jobID + `')">`)
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
	// Extract job ID from path like /api/jobs/{id}/kubeconfig or /api/labs/{id}/kubeconfig
	pathParts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(pathParts) < 4 || pathParts[0] != "api" || pathParts[3] != "kubeconfig" {
		log.Printf("Invalid path for kubeconfig download: %s", r.URL.Path)
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}
	// Accept both "jobs" and "labs" as the second path segment
	if pathParts[1] != "jobs" && pathParts[1] != "labs" {
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

// GetCoderCredentials returns the lab's public workspace base URL and the
// namespace student workspaces run in for a completed lab. The route keeps its
// historical name/path for backward compatibility; it no longer returns any
// Coder admin credentials (Coder has been removed).
func (h *Handler) GetCoderCredentials(w http.ResponseWriter, r *http.Request) {
	pathParts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(pathParts) < 4 || pathParts[0] != "api" || pathParts[3] != "coder-credentials" {
		log.Printf("Invalid path for lab credentials: %s", r.URL.Path)
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}
	if pathParts[1] != "jobs" && pathParts[1] != "labs" {
		log.Printf("Invalid path for lab credentials: %s", r.URL.Path)
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}
	jobID := pathParts[2]

	job, exists := h.jobManager.GetJob(jobID)
	if !exists {
		log.Printf("Job not found: %s", jobID)
		http.Error(w, "Job not found", http.StatusNotFound)
		return
	}

	job.mu.RLock()
	status := job.Status
	domain := ""
	if job.Config != nil {
		domain = job.Config.Domain
	}
	kubeconfig := extractStringFromConfigValue(job.Kubeconfig)
	namespace := job.workspaceNamespace()
	job.mu.RUnlock()

	if status != JobStatusCompleted {
		http.Error(w, "Lab is not completed", http.StatusBadRequest)
		return
	}

	// Ask the backend where workspaces are actually served: a lab without a domain
	// is exposed via nip.io on the ingress LoadBalancer IP, which is only known at
	// runtime. An empty base URL means workspaces are in-cluster only.
	baseURL := ""
	if kubeconfig != "" {
		backend, err := h.newWorkspaceBackend(kubeconfig, namespace)
		if err != nil {
			log.Printf("Failed to build workspace backend for lab %s: %v", jobID, err)
		} else if d, scheme := backend.Routing(r.Context(), domain); d != "" {
			baseURL = scheme + "://" + d
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"url":       baseURL,
		"namespace": namespace,
	})
}

// ServeStudentDashboard serves the student dashboard page
func (h *Handler) ServeStudentDashboard(w http.ResponseWriter, r *http.Request) {
	email := studentEmailFromContext(r)
	initial := "?"
	if len(email) > 0 {
		initial = strings.ToUpper(string(email[0]))
	}
	h.serveTemplate(w, "student-dashboard.html", map[string]interface{}{
		"Email":           email,
		"Initial":         initial,
		"FeedbackSuccess": r.URL.Query().Get("feedback") == "1",
	})
}

// ListLabTemplates returns the workspace templates configured for a lab.
// Templates are pure configuration (no external API call): the template ID is
// its name.
func (h *Handler) ListLabTemplates(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	labID := r.URL.Query().Get("lab_id")
	if labID == "" {
		http.Error(w, "lab_id is required", http.StatusBadRequest)
		return
	}

	job, exists := h.jobManager.GetJob(labID)
	if !exists {
		http.Error(w, "Lab not found", http.StatusNotFound)
		return
	}

	job.mu.RLock()
	status := job.Status
	var templates []WorkspaceTemplate
	if job.Config != nil {
		templates = job.Config.GetWorkspaceTemplates()
	}
	job.mu.RUnlock()

	if status != JobStatusCompleted {
		http.Error(w, "Lab is not ready yet", http.StatusBadRequest)
		return
	}

	type templateOption struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	options := make([]templateOption, 0, len(templates))
	for _, t := range templates {
		options = append(options, templateOption{ID: t.Name, Name: t.Name})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(options)
}

// UploadTemplateToLab appends one or more workspace templates to an existing lab's
// configuration. It accepts the same payload the create-lab wizard sends — the
// "Build with a form" fields (template_N_*), a "Paste YAML" document
// (templates_yaml), or a devcontainer import (which the client turns into YAML) —
// and falls back to the legacy flat template_* fields for older callers. The route
// keeps its historical name for backward compatibility; it no longer uploads
// Terraform files.
func (h *Handler) UploadTemplateToLab(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	pathParts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	// Expect: api/labs/{id}/templates/upload or api/jobs/{id}/templates/upload
	if len(pathParts) < 5 || (pathParts[1] != "labs" && pathParts[1] != "jobs") {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}
	jobID := pathParts[2]

	job, exists := h.jobManager.GetJob(jobID)
	if !exists {
		http.Error(w, "Lab not found", http.StatusNotFound)
		return
	}

	job.mu.RLock()
	status := job.Status
	job.mu.RUnlock()

	if status != JobStatusCompleted {
		http.Error(w, "Lab is not ready yet", http.StatusBadRequest)
		return
	}

	// The drawer posts multipart FormData; ErrNotMultipart just means an older
	// urlencoded caller, which ParseForm (called below via getFormValue) handles.
	if err := r.ParseMultipartForm(32 << 20); err != nil && err != http.ErrNotMultipart {
		log.Printf("Failed to parse form for lab %s: %v", jobID, err)
		http.Error(w, "Failed to parse form data", http.StatusBadRequest)
		return
	}

	templates, err := templatesFromUploadRequest(r)
	if err != nil {
		// Resolve/parse failures are user-facing validation messages (bad YAML,
		// bad URL) — safe to return and useful to the admin.
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(templates) == 0 {
		http.Error(w, "template_name is required", http.StatusBadRequest)
		return
	}
	if err := validateWorkspaceTemplates(templates); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Append to the lab config, rejecting a name that clashes with an existing
	// template or another in the same batch, then persist.
	var dupName string
	h.updateJobConfig(jobID, func(config *LabConfig) {
		existing := make(map[string]bool, len(config.WorkspaceTemplates))
		for _, t := range config.WorkspaceTemplates {
			existing[t.Name] = true
		}
		for _, t := range templates {
			if existing[t.Name] {
				dupName = t.Name
				return
			}
			existing[t.Name] = true
		}
		config.WorkspaceTemplates = append(config.WorkspaceTemplates, templates...)
	})
	if dupName != "" {
		http.Error(w, fmt.Sprintf("A template named %q already exists", dupName), http.StatusConflict)
		return
	}
	if err := h.jobManager.SaveJob(jobID); err != nil {
		log.Printf("Failed to persist templates for lab %s: %v", jobID, err)
	}

	names := make([]string, len(templates))
	for i, t := range templates {
		names[i] = t.Name
	}
	w.Header().Set("Content-Type", "application/json")
	// "template" (first name) is kept for backward compatibility with older clients.
	json.NewEncoder(w).Encode(map[string]interface{}{"status": "ok", "template": names[0], "templates": names})
}

// templatesFromUploadRequest resolves the workspace templates to append to a lab.
// It prefers the create-lab wizard's payload (templates_mode + template_N_* fields,
// or templates_yaml) so the "Add Template" drawer matches lab creation, and falls
// back to the legacy flat template_* fields so older callers keep working.
func templatesFromUploadRequest(r *http.Request) ([]WorkspaceTemplate, error) {
	if getFormValue(r, "template_0_name") != "" ||
		getFormValue(r, "templates_mode") != "" ||
		getFormValue(r, "templates_yaml") != "" {
		return workspaceTemplatesFromRequest(r)
	}

	// Legacy: a single template described by flat template_* fields.
	name := strings.TrimSpace(r.FormValue("template_name"))
	if name == "" {
		return nil, nil
	}
	tmpl := WorkspaceTemplate{
		Name:          name,
		IDE:           strings.TrimSpace(r.FormValue("template_ide")),
		Image:         strings.TrimSpace(r.FormValue("template_image")),
		GitRepo:       strings.TrimSpace(r.FormValue("template_git_repo")),
		GitBranch:     strings.TrimSpace(r.FormValue("template_git_branch")),
		GitFolder:     strings.TrimSpace(r.FormValue("template_git_folder")),
		CPU:           strings.TrimSpace(r.FormValue("template_cpu")),
		Memory:        strings.TrimSpace(r.FormValue("template_memory")),
		DiskSize:      strings.TrimSpace(r.FormValue("template_disk_size")),
		StartupScript: r.FormValue("template_startup_script"),
		DotfilesRepo:  strings.TrimSpace(r.FormValue("template_dotfiles_repo")),
		Extensions:    splitList(r.FormValue("template_extensions")),
	}
	if tmpl.GitRepo != "" && !validateURL(tmpl.GitRepo) {
		return nil, fmt.Errorf("Invalid git repository URL")
	}
	return []WorkspaceTemplate{tmpl}, nil
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
	labID := getFormValue(r, "lab_id")
	templateIDStr := getFormValue(r, "template_id")

	// Email comes from the authenticated session
	email := studentEmailFromContext(r)
	if email == "" {
		http.Error(w, "Session email not found, please log in again", http.StatusUnauthorized)
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
	kubeconfig := extractStringFromConfigValue(job.Kubeconfig)
	namespace := job.workspaceNamespace()
	domain := ""
	var templates []WorkspaceTemplate
	if job.Config != nil {
		domain = job.Config.Domain
		templates = job.Config.GetWorkspaceTemplates()
	}
	job.mu.RUnlock()

	if status != JobStatusCompleted {
		http.Error(w, "Lab is not ready yet", http.StatusBadRequest)
		return
	}

	if kubeconfig == "" {
		http.Error(w, "Lab cluster configuration not available", http.StatusInternalServerError)
		return
	}

	// Generate the code-server password shown to the student. It stays within
	// [0-9a-zA-Z-] (see GenerateWorkspaceToken) — plenty of entropy, and safe to
	// carry through the shell-quoted container bootstrap.
	password, err := GenerateWorkspaceToken()
	if err != nil {
		log.Printf("Failed to generate workspace token: %v", err)
		http.Error(w, "Failed to generate workspace token", http.StatusInternalServerError)
		return
	}

	// Get sanitized username from email (before @) for use in resource names.
	username := usernameFromEmail(email)

	if len(templates) == 0 {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<div class="error-message">No templates available in this lab</div>`)
		return
	}

	// Resolve the selected template by name (template_id is the template name);
	// fall back to the first template.
	selected := templates[0]
	if templateIDStr != "" {
		found := false
		for _, t := range templates {
			if t.Name == templateIDStr {
				selected = t
				found = true
				break
			}
		}
		if !found {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprintf(w, `<div class="error-message">Selected template is not available in this lab</div>`)
			return
		}
	}

	backend, err := h.newWorkspaceBackend(kubeconfig, namespace)
	if err != nil {
		log.Printf("Failed to build workspace backend for lab %s: %v", labID, err)
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<div class="error-message">Unable to reach the lab cluster. Please contact the lab administrator.</div>`)
		return
	}

	// A git-backed workspace needs a persistent volume to clone into; default one.
	diskSize := selected.DiskSize
	if diskSize == "" && selected.GitRepo != "" {
		diskSize = "5Gi"
	}

	spec := workspace.Spec{
		LabID:            labID,
		Owner:            username,
		Template:         selected.Name,
		IDE:              selected.IDE,
		Image:            selected.Image,
		GitRepo:          selected.GitRepo,
		GitBranch:        selected.GitBranch,
		GitFolder:        selected.GitFolder,
		CPU:              selected.CPU,
		Memory:           selected.Memory,
		DiskSize:         diskSize,
		Env:              selected.Env,
		StartupScript:    selected.StartupScript,
		DotfilesRepo:     selected.DotfilesRepo,
		Extensions:       selected.Extensions,
		Sidecars:         toWorkspaceSidecars(selected.Sidecars),
		Mounts:           toWorkspaceMounts(selected.Mounts),
		ImagePullSecrets: selected.ImagePullSecrets,
		GitAuthSecret:    selected.GitAuthSecret,
		Devcontainer:     toWorkspaceDevcontainer(selected.Devcontainer),
		Domain:           domain,
		ClusterIssuer:    "letsencrypt-prod",
		Token:            password,
	}

	ws, err := backend.EnsureWorkspace(r.Context(), spec)
	if err != nil {
		// The cause is for the admin, not the student: it can name the lab's
		// credential Secrets, its namespace and its cluster, and there is nothing in
		// it a student could act on anyway. It goes to the log; they get the same
		// "ask your administrator" they get when the cluster is unreachable.
		log.Printf("Failed to ensure workspace for %s in lab %s: %v", email, labID, err)
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<div class="error-message">Could not create your workspace. Please contact the lab administrator.</div>`)
		return
	}

	workspaceURL := ws.URL
	workspaceName := ws.Name

	// Create workspace info structure for the client-side encrypted cookie.
	workspaceInfo := map[string]interface{}{
		"email":              email,
		"workspace_url":      workspaceURL,
		"password":           password, // code-server login password; encrypted client-side
		"encrypted_password": "",
		"workspace_name":     workspaceName,
		"lab_id":             labID,
		"created_at":         time.Now().Format(time.RFC3339),
	}
	if workspaceInfoJSON, jsonErr := json.Marshal(workspaceInfo); jsonErr == nil {
		isSecure := strings.HasPrefix(workspaceURL, "https://") || r.TLS != nil
		cookieName := fmt.Sprintf("workspace_info_%s_%s", labID, workspaceName)
		http.SetCookie(w, &http.Cookie{
			Name:     cookieName,
			Value:    url.QueryEscape(string(workspaceInfoJSON)),
			Path:     "/",
			MaxAge:   86400,
			HttpOnly: false,
			Secure:   isSecure,
			SameSite: http.SameSiteLaxMode,
		})
		log.Printf("Set %s cookie for email: %s", cookieName, email)
	} else {
		log.Printf("Failed to marshal workspace info: %v", jsonErr)
	}

	workspaceInfoForClient := map[string]interface{}{
		"email":          email,
		"workspace_url":  workspaceURL,
		"password":       password,
		"workspace_name": workspaceName,
		"lab_id":         labID,
		"created_at":     time.Now().Format(time.RFC3339),
	}
	workspaceInfoJSONForClient, _ := json.Marshal(workspaceInfoForClient)
	workspaceInfoJSONEscaped := template.HTMLEscapeString(string(workspaceInfoJSONForClient))

	title := "✅ Workspace Created Successfully!"
	if ws.Ready {
		title = "✅ Workspace Ready!"
	}

	w.Header().Set("Content-Type", "text/html")
	var response strings.Builder
	response.WriteString(`<div class="success-message">`)
	response.WriteString(fmt.Sprintf(`<h3>%s</h3>`, title))
	pollURL := fmt.Sprintf("/api/student/workspace/status?lab_id=%s&workspace_name=%s",
		url.QueryEscape(labID), url.QueryEscape(workspaceName))
	response.WriteString(fmt.Sprintf(`<div class="workspace-ready-status workspace-ready-status--starting" data-poll-url="%s"><span class="workspace-status-spinner"></span><span>Workspace is starting, this may take a moment...</span></div>`,
		template.HTMLEscapeString(pollURL)))
	response.WriteString(`<details class="credentials-box">`)
	response.WriteString(`<summary>Your Workspace Credentials</summary>`)
	if workspaceURL != "" {
		response.WriteString(fmt.Sprintf(`<div class="credential-item"><label>Workspace URL:</label><div class="value"><a href="%s" target="_blank">%s</a></div></div>`, workspaceURL, workspaceURL))
	}
	response.WriteString(fmt.Sprintf(`<div class="credential-item"><label>Email:</label><div class="value">%s</div></div>`, template.HTMLEscapeString(email)))
	response.WriteString(fmt.Sprintf(`<div class="credential-item"><label>Connection token:</label><div class="value">%s</div></div>`, template.HTMLEscapeString(password)))
	response.WriteString(`<p><strong>Important:</strong> Please save these credentials. You will need the token to open your workspace.</p>`)
	response.WriteString(`<p><small>Your workspace information can be encrypted and saved locally. Click "Encrypt & Save" below to store it securely.</small></p>`)
	response.WriteString(fmt.Sprintf(`<div data-workspace-info='%s' style="display:none;"></div>`, workspaceInfoJSONEscaped))
	response.WriteString(`<button onclick="encryptAndSaveWorkspaceInfo(this)" class="btn credentials-save-btn">Encrypt & Save Workspace Info</button>`)
	response.WriteString(`</details>`)
	response.WriteString(`</div>`)

	fmt.Fprint(w, response.String())
}

// WorkspaceStatus returns the current readiness status of a student workspace as an HTML partial.
// It is polled by HTMX after a workspace is created; polling stops once the workspace is running.
func (h *Handler) WorkspaceStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	labID := r.URL.Query().Get("lab_id")
	workspaceName := r.URL.Query().Get("workspace_name")
	// The owner is always the authenticated student — never trusted from the client.
	owner := usernameFromEmail(studentEmailFromContext(r))

	if labID == "" || workspaceName == "" || owner == "" {
		http.Error(w, "lab_id and workspace_name are required and you must be logged in", http.StatusBadRequest)
		return
	}

	job, exists := h.jobManager.GetJob(labID)
	if !exists {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, buildWorkspaceStatusHTML(labID, workspaceName, "unknown", ""))
		return
	}

	job.mu.RLock()
	status := job.Status
	kubeconfig := extractStringFromConfigValue(job.Kubeconfig)
	namespace := job.workspaceNamespace()
	job.mu.RUnlock()

	if status != JobStatusCompleted || kubeconfig == "" {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, buildWorkspaceStatusHTML(labID, workspaceName, "checking", ""))
		return
	}

	backend, err := h.newWorkspaceBackend(kubeconfig, namespace)
	if err != nil {
		log.Printf("WorkspaceStatus: failed to build backend for lab %s: %v", labID, err)
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, buildWorkspaceStatusHTML(labID, workspaceName, "checking", ""))
		return
	}

	ws, err := backend.GetWorkspace(r.Context(), workspaceName)
	// Authorization: a student may only see their own workspace.
	if err != nil || ws.Owner != owner {
		log.Printf("[debug] WorkspaceStatus: lookup/authz failed workspace=%s owner=%s error=%v", workspaceName, owner, err)
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, buildWorkspaceStatusHTML(labID, workspaceName, "checking", ""))
		return
	}

	readiness := ws.Phase
	wsURL := ""
	if ws.Ready {
		if workspaceDNSReady(r.Context(), ws) {
			readiness = "running"
			// Route through the OpenWorkspace redirect endpoint so the connection token
			// is appended at click time.
			wsURL = fmt.Sprintf("/api/student/workspace/open?lab_id=%s&workspace_name=%s",
				url.QueryEscape(labID), url.QueryEscape(ws.Name))
		} else {
			// The pod is up but the workspace hostname does not resolve yet — the DNS
			// record created during provisioning is still propagating. Keep polling
			// instead of handing the student a URL that would NXDOMAIN (which the
			// browser then negatively caches, making the workspace look broken).
			readiness = "dns_propagating"
		}
	}
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprint(w, buildWorkspaceStatusHTML(labID, ws.Name, readiness, wsURL))
}

// dnsPropagationGrace bounds how long the student UI holds the "ready" signal back
// while a freshly-created workspace hostname propagates through DNS. Past this
// window we fail open and show the workspace as ready anyway, so a lab whose DNS
// is managed manually (no provider automation) is never trapped in "propagating".
const dnsPropagationGrace = 3 * time.Minute

// dnsLookupTimeout caps a single reachability lookup so a slow resolver never
// stalls the status poll.
const dnsLookupTimeout = 2 * time.Second

// hostResolver is the subset of *net.Resolver used to probe workspace DNS. It is a
// package var so tests can substitute a deterministic resolver.
type hostResolver interface {
	LookupHost(ctx context.Context, host string) ([]string, error)
}

var workspaceDNSResolver hostResolver = net.DefaultResolver

// workspaceDNSReady reports whether a pod-ready workspace's public hostname
// resolves yet. A Deployment can report Ready seconds before the DNS A record
// created during provisioning has propagated; opening the URL then yields
// DNS_PROBE_FINISHED_NXDOMAIN. Gating the green "ready" state on resolution keeps
// students from clicking too early. It fails open once dnsPropagationGrace has
// elapsed so manually-managed DNS is never blocked forever, and returns true
// immediately when there is no public host to resolve (in-cluster workspaces).
func workspaceDNSReady(ctx context.Context, ws workspace.Workspace) bool {
	host := hostFromWorkspaceURL(ws.URL)
	if host == "" {
		return true
	}
	if time.Since(ws.UpdatedAt) > dnsPropagationGrace {
		return true
	}
	lctx, cancel := context.WithTimeout(ctx, dnsLookupTimeout)
	defer cancel()
	addrs, err := workspaceDNSResolver.LookupHost(lctx, host)
	return err == nil && len(addrs) > 0
}

// hostFromWorkspaceURL extracts the hostname from a workspace base URL, returning
// "" when there is no domain configured or the URL cannot be parsed.
func hostFromWorkspaceURL(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// usernameInvalidChars matches characters not allowed in a DNS-1123 label.
var usernameInvalidChars = regexp.MustCompile(`[^a-z0-9-]`)

// usernameFromEmail derives the sanitized, DNS-1123-safe workspace username from
// a student email. It matches the sanitization the kube backend applies to the
// owner label, so it can be compared against Workspace.Owner for authorization.
func usernameFromEmail(email string) string {
	if email == "" {
		return ""
	}
	local := strings.ToLower(strings.Split(email, "@")[0])
	return strings.Trim(usernameInvalidChars.ReplaceAllString(local, "-"), "-")
}

// autoLoginPage is a self-submitting form that logs a student into their
// code-server workspace. code-server has no password-in-URL support, so instead
// of dropping the student on its login screen we POST the workspace token to
// /login on their behalf; code-server sets its session cookie and lands them
// straight in the IDE. The token only ever travels in a POST body, never a URL.
var autoLoginPage = template.Must(template.New("workspaceAutoLogin").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Opening your workspace…</title>
</head>
<body onload="document.getElementById('login').submit()">
<p>Opening your workspace…</p>
<form id="login" method="POST" action="{{.LoginURL}}">
<input type="hidden" name="password" value="{{.Token}}">
<noscript><button type="submit">Continue to your workspace</button></noscript>
</form>
</body>
</html>`))

// OpenWorkspace logs the student into their code-server workspace: rather than
// redirecting to code-server's login page, it serves a self-submitting form that
// POSTs the workspace token to /login, so the student lands in the IDE without
// retyping the password shown in the portal.
func (h *Handler) OpenWorkspace(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	labID := r.URL.Query().Get("lab_id")
	workspaceName := r.URL.Query().Get("workspace_name")
	// The owner is always the authenticated student — never trusted from the client.
	owner := usernameFromEmail(studentEmailFromContext(r))

	if labID == "" || workspaceName == "" || owner == "" {
		http.Error(w, "lab_id and workspace_name are required and you must be logged in", http.StatusBadRequest)
		return
	}

	job, exists := h.jobManager.GetJob(labID)
	if !exists {
		http.Error(w, "lab not found", http.StatusNotFound)
		return
	}

	job.mu.RLock()
	kubeconfig := extractStringFromConfigValue(job.Kubeconfig)
	namespace := job.workspaceNamespace()
	job.mu.RUnlock()

	if kubeconfig == "" {
		http.Error(w, "lab is not ready", http.StatusServiceUnavailable)
		return
	}

	backend, err := h.newWorkspaceBackend(kubeconfig, namespace)
	if err != nil {
		log.Printf("OpenWorkspace: failed to build backend for lab %s: %v", labID, err)
		http.Error(w, "lab is not ready", http.StatusServiceUnavailable)
		return
	}

	ws, err := backend.GetWorkspace(r.Context(), workspaceName)
	// Authorization: a student may only open their own workspace.
	if err != nil || ws.Owner != owner || ws.OpenURL == "" {
		log.Printf("OpenWorkspace: lookup/authz failed workspace=%s owner=%s in lab %s: %v", workspaceName, owner, labID, err)
		http.Error(w, "workspace not available", http.StatusServiceUnavailable)
		return
	}

	// ws.OpenURL is the workspace base URL (ending in "/"); code-server's login
	// endpoint lives at /login. Serve the self-submitting form there so the token
	// is posted on the student's behalf instead of asked for on the login page.
	loginURL := strings.TrimRight(ws.OpenURL, "/") + "/login"
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := autoLoginPage.Execute(w, struct {
		LoginURL string
		Token    string
	}{LoginURL: loginURL, Token: ws.Token}); err != nil {
		log.Printf("OpenWorkspace: failed to render auto-login page for lab %s: %v", labID, err)
	}
}

// buildWorkspaceStatusHTML returns an HTML partial for the workspace readiness indicator.
// When the workspace is running, the returned HTML has no HTMX polling attributes so polling stops.
func buildWorkspaceStatusHTML(labID, workspaceName, status, workspaceURL string) string {
	switch status {
	case "running":
		if workspaceURL != "" {
			return fmt.Sprintf(`<div class="workspace-ready-status workspace-ready-status--ready"><span>✅ Workspace is ready!</span><a href="%s" target="_blank" class="btn-workspace-connect">Open in code-server</a></div>`,
				template.HTMLEscapeString(workspaceURL))
		}
		return `<div class="workspace-ready-status workspace-ready-status--ready"><span>✅ Workspace is ready!</span></div>`
	case "failed", "agents_failed":
		return `<div class="workspace-ready-status workspace-ready-status--error"><span>❌ Workspace failed to start. Please contact the lab administrator.</span></div>`
	case "canceled", "canceling":
		return `<div class="workspace-ready-status workspace-ready-status--error"><span>⚠ Workspace startup was canceled.</span></div>`
	default:
		pollURL := fmt.Sprintf("/api/student/workspace/status?lab_id=%s&workspace_name=%s",
			url.QueryEscape(labID), url.QueryEscape(workspaceName))
		var message string
		switch status {
		case "checking", "":
			message = "Checking workspace status..."
		case "agents_starting":
			message = "Workspace is provisioned, waiting for agent to be ready..."
		case "dns_propagating":
			message = "Workspace is up — waiting for DNS to propagate (this can take a minute)..."
		default:
			message = fmt.Sprintf("Workspace is %s, this may take a moment...", status)
		}
		return fmt.Sprintf(`<div class="workspace-ready-status workspace-ready-status--starting" data-poll-url="%s"><span class="workspace-status-spinner"></span><span>%s</span></div>`,
			template.HTMLEscapeString(pollURL), template.HTMLEscapeString(message))
	}
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
		switch c := creds.(type) {
		case *OVHCredentials:
			currentCreds = map[string]interface{}{
				"configured":   true,
				"provider":     provider,
				"service_name": c.ServiceName,
				"endpoint":     c.Endpoint,
			}
		case *AzureCredentials:
			currentCreds = map[string]interface{}{
				"configured":      true,
				"provider":        provider,
				"subscription_id": c.SubscriptionID,
				"tenant_id":       c.TenantID,
			}
		default:
			currentCreds = map[string]interface{}{"configured": true, "provider": provider}
		}
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
	case "azure":
		h.setAzureCredentialsFromForm(w, r)
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

// setAzureCredentialsFromForm handles Azure-specific credential setting
func (h *Handler) setAzureCredentialsFromForm(w http.ResponseWriter, r *http.Request) {
	creds := &AzureCredentials{
		ClientID:       getFormValue(r, "azure_client_id"),
		ClientSecret:   getFormValue(r, "azure_client_secret"),
		TenantID:       getFormValue(r, "azure_tenant_id"),
		SubscriptionID: getFormValue(r, "azure_subscription_id"),
	}

	if err := h.credentialsManager.SetCredentials(creds); err != nil {
		log.Printf("Failed to set Azure credentials: %v", err)
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `
			<div class="error-message">
				<h3>Failed to Save Credentials</h3>
				<p>%s</p>
				<p><small>Please ensure all fields are filled correctly.</small></p>
			</div>`, template.HTMLEscapeString(err.Error()))
		return
	}

	log.Printf("Azure credentials saved successfully")
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, `
		<div class="success-message">
			<p>✅ Azure credentials saved successfully</p>
		</div>`)
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

	// Handle provider-specific response (return non-secret info only)
	w.Header().Set("Content-Type", "application/json")
	switch provider {
	case "ovh":
		ovhCreds, ok := creds.(*OVHCredentials)
		if !ok {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": "Invalid credentials type"})
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"configured":          true,
			"provider":            provider,
			"service_name":        ovhCreds.ServiceName,
			"endpoint":            ovhCreds.Endpoint,
			"has_application_key": ovhCreds.ApplicationKey != "",
			"has_consumer_key":    ovhCreds.ConsumerKey != "",
		})
	case "azure":
		azCreds, ok := creds.(*AzureCredentials)
		if !ok {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": "Invalid credentials type"})
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"configured":        true,
			"provider":          provider,
			"subscription_id":   azCreds.SubscriptionID,
			"tenant_id":         azCreds.TenantID,
			"has_client_id":     azCreds.ClientID != "",
			"has_client_secret": azCreds.ClientSecret != "",
		})
	default:
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"configured": false,
			"provider":   provider,
			"error":      "Provider not supported",
		})
	}
}

// ServeOVHOptions serves the OVH options admin page
func (h *Handler) ServeOVHOptions(w http.ResponseWriter, r *http.Request) {
	cfg := h.ovhOptionsManager.GetConfig()
	regions := h.ovhOptionsManager.GetCachedRegions()
	hasCache := h.ovhOptionsManager.HasCache()

	type flavorData struct {
		Name  string
		VCPUs int
		RAM   int
	}
	type regionData struct {
		Name    string
		Enabled bool
		Default bool
		Flavors []struct {
			Name    string
			VCPUs   int
			RAM     int
			Enabled bool
			Default bool
		}
	}

	enabledRegionSet := toSet(cfg.Regions.Enabled)
	showAll := len(enabledRegionSet) == 0

	var regionsData []regionData
	for _, r := range regions {
		rd := regionData{
			Name:    r,
			Enabled: showAll || enabledRegionSet[r],
			Default: r == cfg.Regions.Default,
		}
		cachedFlavors := h.ovhOptionsManager.GetCachedFlavors(r)
		cachedFlavors = filterFlavorsByCPURAM(cachedFlavors, cfg.FlavorMinVCPUs, cfg.FlavorMaxVCPUs, cfg.FlavorMinRAM, cfg.FlavorMaxRAM)
		flavorCfg := cfg.Flavors[r]
		enabledFlavorSet := toSet(flavorCfg.Enabled)
		showAllFlavors := len(enabledFlavorSet) == 0
		for _, f := range cachedFlavors {
			rd.Flavors = append(rd.Flavors, struct {
				Name    string
				VCPUs   int
				RAM     int
				Enabled bool
				Default bool
			}{
				Name:    f.Name,
				VCPUs:   f.VCPUs,
				RAM:     f.RAM,
				Enabled: showAllFlavors || enabledFlavorSet[f.Name],
				Default: f.Name == flavorCfg.Default,
			})
		}
		regionsData = append(regionsData, rd)
	}

	data := map[string]interface{}{
		"HasCache": hasCache,
		"Regions":  regionsData,
		"Config":   cfg,
	}
	h.serveTemplate(w, "ovh-options.html", data)
}

// SaveOVHOptions handles saving the OVH options admin preferences
func (h *Handler) SaveOVHOptions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := r.ParseForm(); err != nil {
		log.Printf("SaveOVHOptions: failed to parse form: %v", err)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<div class="error-message"><p>Failed to parse form</p></div>`)
		return
	}

	cfg := OVHOptionsConfig{
		Regions: OVHItemConfig{
			Enabled: r.Form["region_enabled"],
			Default: r.FormValue("region_default"),
		},
		Flavors:        make(map[string]OVHItemConfig),
		FlavorMinVCPUs: atoiForm(r.FormValue("flavor_filter_min_vcpus")),
		FlavorMaxVCPUs: atoiForm(r.FormValue("flavor_filter_max_vcpus")),
		FlavorMinRAM:   atoiForm(r.FormValue("flavor_filter_min_ram")),
		FlavorMaxRAM:   atoiForm(r.FormValue("flavor_filter_max_ram")),
	}

	regions := h.ovhOptionsManager.GetCachedRegions()
	for _, region := range regions {
		enabledKey := fmt.Sprintf("flavor_enabled_%s", region)
		defaultKey := fmt.Sprintf("flavor_default_%s", region)
		enabled := r.Form[enabledKey]
		defaultVal := r.FormValue(defaultKey)
		if len(enabled) > 0 || defaultVal != "" {
			cfg.Flavors[region] = OVHItemConfig{
				Enabled: enabled,
				Default: defaultVal,
			}
		}
	}

	h.ovhOptionsManager.SetConfig(cfg)
	if err := h.ovhOptionsManager.SaveConfig(); err != nil {
		log.Printf("SaveOVHOptions: failed to save config: %v", err)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<div class="error-message"><p>Failed to save: %s</p></div>`, escapeHTML(err.Error()))
		return
	}

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", "/admin/ovh-options")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<div class="success-message"><p>OVH options saved successfully</p></div>`)
	} else {
		http.Redirect(w, r, "/admin/ovh-options", http.StatusSeeOther)
	}
}

// RefreshOVHOptions triggers a cache refresh from the OVH API
func (h *Handler) RefreshOVHOptions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := h.ovhOptionsManager.RefreshFromAPI(); err != nil {
		log.Printf("RefreshOVHOptions: %v", err)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<div class="error-message"><p>Failed to refresh: %s</p></div>`, escapeHTML(err.Error()))
		return
	}

	w.Header().Set("HX-Redirect", "/admin/ovh-options")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, `<div class="success-message"><p>Cache refreshed successfully. Reloading...</p></div>`)
}

// ServeAzureOptions serves the Azure options admin page.
func (h *Handler) ServeAzureOptions(w http.ResponseWriter, r *http.Request) {
	if h.azureOptionsManager == nil {
		http.Error(w, "Azure options not available", http.StatusServiceUnavailable)
		return
	}

	cfg := h.azureOptionsManager.GetConfig()
	regions := h.azureOptionsManager.GetCachedRegions()
	hasCache := h.azureOptionsManager.HasCache()

	enabledRegionSet := toSet(cfg.Regions.Enabled)
	showAllRegions := len(enabledRegionSet) == 0

	type azureRegionRow struct {
		Name        string
		DisplayName string
		Enabled     bool
		Default     bool
	}

	var regionsData []azureRegionRow
	for _, name := range regions {
		regionsData = append(regionsData, azureRegionRow{
			Name:        name,
			DisplayName: h.azureOptionsManager.RegionDisplayName(name),
			Enabled:     showAllRegions || enabledRegionSet[name],
			Default:     name == cfg.Regions.Default,
		})
	}

	data := map[string]interface{}{
		"HasCache": hasCache,
		"Regions":  regionsData,
		"Config":   cfg,
	}
	h.serveTemplate(w, "azure-options.html", data)
}

// ServeAzureProvider serves the unified Azure provider page (credentials + options tabs).
func (h *Handler) ServeAzureProvider(w http.ResponseWriter, r *http.Request) {
	data := map[string]interface{}{
		"HasCache": false,
		"Regions":  nil,
		"Config":   nil,
	}
	if h.azureOptionsManager != nil {
		cfg := h.azureOptionsManager.GetConfig()
		regions := h.azureOptionsManager.GetCachedRegions()
		hasCache := h.azureOptionsManager.HasCache()
		enabledRegionSet := toSet(cfg.Regions.Enabled)
		showAllRegions := len(enabledRegionSet) == 0
		type azureRegionRow struct {
			Name        string
			DisplayName string
			Enabled     bool
			Default     bool
		}
		var regionsData []azureRegionRow
		for _, name := range regions {
			regionsData = append(regionsData, azureRegionRow{
				Name:        name,
				DisplayName: h.azureOptionsManager.RegionDisplayName(name),
				Enabled:     showAllRegions || enabledRegionSet[name],
				Default:     name == cfg.Regions.Default,
			})
		}
		data["HasCache"] = hasCache
		data["Regions"] = regionsData
		data["Config"] = cfg
	}
	h.serveTemplate(w, "azure-provider.html", data)
}

// ServeAzureAD serves the Azure AD OAuth configuration admin page.
func (h *Handler) ServeAzureAD(w http.ResponseWriter, r *http.Request) {
	if h.azureOptionsManager == nil {
		http.Error(w, "Azure options not available", http.StatusServiceUnavailable)
		return
	}
	azureAD := h.azureOptionsManager.GetAzureADConfig()
	h.serveTemplate(w, "azure-ad.html", azureAD)
}

// SaveAzureADConfig handles saving Azure AD OAuth configuration from the admin page.
func (h *Handler) SaveAzureADConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := r.ParseForm(); err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<div class="error-message"><p>Failed to parse form</p></div>`)
		return
	}

	clientID := strings.TrimSpace(r.FormValue("azure_ad_client_id"))
	clientSecret := strings.TrimSpace(r.FormValue("azure_ad_client_secret"))
	tenantID := strings.TrimSpace(r.FormValue("azure_ad_tenant_id"))
	adminGroupID := strings.TrimSpace(r.FormValue("azure_ad_admin_group_id"))
	disableClassic := r.FormValue("azure_ad_disable_classic_login") == "on"
	disableClassicAdmin := r.FormValue("azure_ad_disable_classic_admin_login") == "on"

	// Preserve existing secret when the field is left blank (password fields submit empty on re-display)
	if clientSecret == "" && h.azureOptionsManager != nil && clientID != "" {
		existing := h.azureOptionsManager.GetAzureADConfig()
		clientSecret = existing.ClientSecret
	}

	// Classic login flags can only be active when their prerequisites are met
	if clientID == "" {
		disableClassic = false
		adminGroupID = ""
		disableClassicAdmin = false
	}
	if adminGroupID == "" {
		disableClassicAdmin = false
	}

	cfg := AzureADConfig{
		ClientID:                 clientID,
		ClientSecret:             clientSecret,
		TenantID:                 tenantID,
		DisableClassicLogin:      disableClassic,
		AdminGroupID:             adminGroupID,
		DisableClassicAdminLogin: disableClassicAdmin,
	}

	if h.azureOptionsManager != nil {
		if err := h.azureOptionsManager.SetAzureADConfig(cfg); err != nil {
			log.Printf("SaveAzureADConfig: failed to persist: %v", err)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprintf(w, `<div class="error-message"><p>Failed to save Azure AD config: %s</p></div>`, escapeHTML(err.Error()))
			return
		}
	}

	if h.azureADConfigurer != nil {
		h.azureADConfigurer(clientID, clientSecret, tenantID)
	}
	if h.classicLoginConfigurer != nil {
		h.classicLoginConfigurer(disableClassic)
	}
	if h.adminGroupIDConfigurer != nil {
		h.adminGroupIDConfigurer(adminGroupID)
	}
	if h.classicAdminLoginConfigurer != nil {
		h.classicAdminLoginConfigurer(disableClassicAdmin)
	}

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", "/admin/azure-ad")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<div class="success-message"><p>Azure AD configuration saved</p></div>`)
	} else {
		http.Redirect(w, r, "/admin/azure-ad", http.StatusSeeOther)
	}
}

// SaveAzureOptions handles saving Azure options admin preferences.
func (h *Handler) SaveAzureOptions(w http.ResponseWriter, r *http.Request) {
	if h.azureOptionsManager == nil {
		http.Error(w, "Azure options not available", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := r.ParseForm(); err != nil {
		log.Printf("SaveAzureOptions: failed to parse form: %v", err)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<div class="error-message"><p>Failed to parse form</p></div>`)
		return
	}

	cfg := AzureOptionsConfig{
		Regions: AzureItemConfig{
			Enabled: r.Form["region_enabled"],
			Default: r.FormValue("region_default"),
		},
		VMSizes: make(map[string]AzureItemConfig),
	}

	for _, region := range h.azureOptionsManager.GetCachedRegions() {
		enabledKey := fmt.Sprintf("vmsize_enabled_%s", region)
		defaultKey := fmt.Sprintf("vmsize_default_%s", region)
		enabled := r.Form[enabledKey]
		defaultVal := r.FormValue(defaultKey)
		if len(enabled) > 0 || defaultVal != "" {
			cfg.VMSizes[region] = AzureItemConfig{
				Enabled: enabled,
				Default: defaultVal,
			}
		}
	}

	h.azureOptionsManager.SetConfig(cfg)
	if err := h.azureOptionsManager.SaveConfig(); err != nil {
		log.Printf("SaveAzureOptions: failed to save config: %v", err)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<div class="error-message"><p>Failed to save: %s</p></div>`, escapeHTML(err.Error()))
		return
	}

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", "/admin/azure-options")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<div class="success-message"><p>Azure options saved successfully</p></div>`)
	} else {
		http.Redirect(w, r, "/admin/azure-options", http.StatusSeeOther)
	}
}

// RefreshAzureOptions triggers a cache refresh from the Azure API.
func (h *Handler) RefreshAzureOptions(w http.ResponseWriter, r *http.Request) {
	if h.azureOptionsManager == nil {
		http.Error(w, "Azure options not available", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := h.azureOptionsManager.RefreshFromAPI(); err != nil {
		log.Printf("RefreshAzureOptions: %v", err)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<div class="error-message"><p>Failed to refresh: %s</p></div>`, escapeHTML(err.Error()))
		return
	}

	w.Header().Set("HX-Redirect", "/admin/azure-options")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, `<div class="success-message"><p>Cache refreshed successfully. Reloading...</p></div>`)
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
		{
			"id":      "azure",
			"name":    "Microsoft Azure",
			"enabled": true,
			"fields": []map[string]interface{}{
				{"name": "azure_client_id", "label": "Client ID (Application ID)", "type": "password", "required": true},
				{"name": "azure_client_secret", "label": "Client Secret", "type": "password", "required": true},
				{"name": "azure_tenant_id", "label": "Tenant ID (Directory ID)", "type": "password", "required": true},
				{"name": "azure_subscription_id", "label": "Subscription ID", "type": "text", "required": true},
			},
		},
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
		ID                        string
		IDShort                   string
		Status                    string
		CreatedAt                 string
		UpdatedAt                 string
		StackName                 string
		IsDryRun                  bool
		HasError                  bool
		ErrorMsg                  string
		HasKubeconfig             bool
		IsDestroyed               bool
		WorkspaceLifetimeHours    int
		LabDeletionDate           string
		HasDeletionDate           bool
		WorkspaceTemplateNamesCSV string
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
		workspaceLifetimeHours := 0
		if job.Config != nil {
			workspaceLifetimeHours = job.Config.WorkspaceLifetimeHours
		}
		labDeletionDate := ""
		hasLabDeletionDate := false
		if job.Config != nil && job.Config.LabDeletionDate != nil {
			labDeletionDate = job.Config.LabDeletionDate.Format("Jan 02, 2006 at 15:04")
			hasLabDeletionDate = true
		}
		var templateNames []string
		if job.Config != nil {
			for _, t := range job.Config.WorkspaceTemplates {
				templateNames = append(templateNames, t.Name)
			}
		}
		job.mu.RUnlock()

		labsDisplay = append(labsDisplay, LabDisplay{
			ID:                        job.ID,
			IDShort:                   shortenLabID(job.ID),
			Status:                    status,
			CreatedAt:                 createdAt,
			UpdatedAt:                 updatedAt,
			StackName:                 stackName,
			IsDryRun:                  isDryRun,
			HasError:                  hasError,
			ErrorMsg:                  errorMsg,
			HasKubeconfig:             hasKubeconfig,
			IsDestroyed:               isDestroyed,
			WorkspaceLifetimeHours:    workspaceLifetimeHours,
			LabDeletionDate:           labDeletionDate,
			HasDeletionDate:           hasLabDeletionDate,
			WorkspaceTemplateNamesCSV: strings.Join(templateNames, ", "),
		})
	}

	data := map[string]interface{}{
		"Labs":  labsDisplay,
		"Count": len(labsDisplay),
	}

	h.serveTemplate(w, "labs-list.html", data)
}

// TemplateStatus summarizes a configured workspace template and how many live
// student workspaces on the lab were created from it.
type TemplateStatus struct {
	Name         string
	IDE          string
	Image        string
	RunningCount int
	HasRunning   bool
}

// buildTemplateStatus correlates a lab's configured templates with the live
// workspaces on its cluster. For each template it reports how many running
// workspaces were created from it (matched via Workspace.Template). Workspaces
// whose Template is empty or does not match a configured template — for example
// ones created before template attribution was added — are counted in
// unattributed rather than dropped.
func buildTemplateStatus(templates []WorkspaceTemplate, workspaces []workspace.Workspace) (statuses []TemplateStatus, unattributed int) {
	known := make(map[string]bool, len(templates))
	for _, t := range templates {
		known[t.Name] = true
	}

	counts := make(map[string]int, len(templates))
	for _, ws := range workspaces {
		if ws.Template != "" && known[ws.Template] {
			counts[ws.Template]++
			continue
		}
		unattributed++
	}

	statuses = make([]TemplateStatus, 0, len(templates))
	for _, t := range templates {
		ide := t.IDE
		if ide == "" || ide == workspace.IDEOpenVSCode {
			ide = workspace.DefaultIDEKind
		}
		image := t.Image
		if image == "" && t.Devcontainer != nil {
			image = "devcontainer"
		}
		n := counts[t.Name]
		statuses = append(statuses, TemplateStatus{
			Name:         t.Name,
			IDE:          ide,
			Image:        image,
			RunningCount: n,
			HasRunning:   n > 0,
		})
	}
	return statuses, unattributed
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
	kubeconfig := extractStringFromConfigValue(job.Kubeconfig)
	namespace := job.workspaceNamespace()
	stackName := ""
	var templates []WorkspaceTemplate
	if job.Config != nil {
		stackName = job.Config.StackName
		templates = job.Config.GetWorkspaceTemplates()
	}
	job.mu.RUnlock()

	if status != JobStatusCompleted {
		http.Error(w, "Lab is not ready yet", http.StatusBadRequest)
		return
	}

	if kubeconfig == "" {
		http.Error(w, "Lab cluster configuration not available", http.StatusInternalServerError)
		return
	}

	backend, err := h.newWorkspaceBackend(kubeconfig, namespace)
	if err != nil {
		log.Printf("ServeLabWorkspaces: failed to build backend for lab %s: %v", labID, err)
		http.Error(w, "Failed to reach lab cluster", http.StatusInternalServerError)
		return
	}

	workspaces, err := backend.ListWorkspaces(r.Context(), labID)
	if err != nil {
		log.Printf("Failed to list workspaces: %v", err)
		http.Error(w, "Failed to list workspaces", http.StatusInternalServerError)
		return
	}

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
			ID:        ws.ID,
			Name:      ws.Name,
			Owner:     ws.Owner,
			Status:    ws.Phase,
			CreatedAt: createdAt,
			UpdatedAt: updatedAt,
		})
	}

	templateStatuses, unattributed := buildTemplateStatus(templates, workspaces)

	data := map[string]interface{}{
		"LabID":        labID,
		"StackName":    stackName,
		"Workspaces":   workspacesDisplay,
		"Count":        len(workspacesDisplay),
		"Templates":    templateStatuses,
		"Unattributed": unattributed,
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
	kubeconfig := extractStringFromConfigValue(job.Kubeconfig)
	namespace := job.workspaceNamespace()
	job.mu.RUnlock()

	if status != JobStatusCompleted {
		http.Error(w, "Lab is not ready yet", http.StatusBadRequest)
		return
	}

	if kubeconfig == "" {
		http.Error(w, "Lab cluster configuration not available", http.StatusInternalServerError)
		return
	}

	backend, err := h.newWorkspaceBackend(kubeconfig, namespace)
	if err != nil {
		log.Printf("ListLabWorkspaces: failed to build backend for lab %s: %v", labID, err)
		http.Error(w, "Failed to reach lab cluster", http.StatusInternalServerError)
		return
	}

	workspaces, err := backend.ListWorkspaces(r.Context(), labID)
	if err != nil {
		log.Printf("Failed to list workspaces: %v", err)
		http.Error(w, "Failed to list workspaces", http.StatusInternalServerError)
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
		kubeconfig := extractStringFromConfigValue(job.Kubeconfig)
		namespace := job.workspaceNamespace()
		job.mu.RUnlock()

		backend, err := h.newWorkspaceBackend(kubeconfig, namespace)
		if err != nil {
			log.Printf("DeleteWorkspace: failed to build backend for lab %s: %v", labID, err)
			writeJSONError(w, http.StatusInternalServerError, "Failed to reach lab cluster")
			return
		}

		var delErrors []string
		for _, wsID := range workspaceIDs {
			if err := backend.DeleteWorkspace(r.Context(), labID, wsID); err != nil {
				delErrors = append(delErrors, fmt.Sprintf("Failed to delete workspace %s: %v", wsID, err))
			}
		}

		if len(delErrors) > 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusPartialContent)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"errors":  delErrors,
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
	kubeconfig := extractStringFromConfigValue(job.Kubeconfig)
	namespace := job.workspaceNamespace()
	job.mu.RUnlock()

	backend, err := h.newWorkspaceBackend(kubeconfig, namespace)
	if err != nil {
		log.Printf("DeleteWorkspace: failed to build backend for lab %s: %v", labID, err)
		writeJSONError(w, http.StatusInternalServerError, "Failed to reach lab cluster")
		return
	}

	if err := backend.DeleteWorkspace(r.Context(), labID, workspaceIDStr); err != nil {
		log.Printf("Failed to delete workspace: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "Failed to delete workspace")
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
		http.Error(w, "Failed to parse form data", http.StatusBadRequest)
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

// parseRecreateDeletionDate reads the deletion schedule the admin entered in the
// recreate prompt. A blank date means "no automatic deletion" and returns
// (nil, nil). A date is rejected unless it is in the future: the destroyed lab's
// original date is already in the past, and reusing any past date would make the
// recreated lab vanish on the very next cleanup tick — the bug this prompt exists
// to prevent. Field names mirror the creation wizard (lab_deletion_date /
// lab_deletion_time).
func parseRecreateDeletionDate(r *http.Request) (*time.Time, error) {
	dateStr := strings.TrimSpace(r.FormValue("lab_deletion_date"))
	if dateStr == "" {
		return nil, nil
	}
	d, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		return nil, fmt.Errorf("the deletion date is not a valid date")
	}
	hour, minute := 23, 59
	if timeStr := strings.TrimSpace(r.FormValue("lab_deletion_time")); timeStr != "" {
		t, err := time.Parse("15:04", timeStr)
		if err != nil {
			return nil, fmt.Errorf("the deletion time is not a valid time")
		}
		hour, minute = t.Hour(), t.Minute()
	}
	deletion := time.Date(d.Year(), d.Month(), d.Day(), hour, minute, 0, 0, time.Local)
	if !deletion.After(time.Now()) {
		return nil, fmt.Errorf("the deletion date must be in the future")
	}
	return &deletion, nil
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
		http.Error(w, "Failed to parse form data", http.StatusBadRequest)
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

	// A lab with a scheduled deletion date was destroyed with that date now in the
	// past. Reusing it would delete the recreated lab on the very next cleanup tick,
	// so the admin re-enters a fresh date in the recreate prompt. A blank date
	// disables automatic deletion; a past date is rejected.
	if config.LabDeletionDate != nil {
		newDeletion, err := parseRecreateDeletionDate(r)
		if err != nil {
			log.Printf("Invalid recreate deletion date for job %s: %v", jobID, err)
			h.renderHTMLError(w, "Invalid Deletion Date", err.Error())
			return
		}
		config.LabDeletionDate = newDeletion
	}

	// Get OVH credentials only when not using existing cluster (BYOK doesn't need them)
	if !config.UseExistingCluster {
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
	}

	// Tokens the admin re-entered in the recreate prompt. The old cluster's
	// Secrets went with it, so recreation is the one moment they must be supplied
	// again. Parsed before the job exists so a mistake does not leave a half-made
	// lab behind.
	recreateSecrets, err := parseWizardSecrets(r)
	if err != nil {
		log.Printf("Invalid recreate credentials: %v", err)
		h.renderHTMLError(w, "Invalid Credentials", err.Error())
		return
	}

	// Create new job with the same configuration
	newJobID := h.jobManager.CreateJob(config)
	h.pendingSecrets.Put(newJobID, recreateSecrets)
	log.Printf("Recreating lab from destroyed job %s as new job: %s", jobID, newJobID)

	// Prepare the new job directory. Workspace templates are pure configuration,
	// so there are no template files to copy over.
	newJobDir := filepath.Join(h.pulumiExec.GetWorkDir(), newJobID)
	if err := os.MkdirAll(newJobDir, 0755); err != nil {
		log.Printf("Failed to create new job directory for %s: %v", newJobID, err)
		http.Error(w, "Failed to prepare job directory", http.StatusInternalServerError)
		return
	}

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

	// Get OVH credentials only when not using existing cluster (BYOK doesn't need them)
	if !config.UseExistingCluster {
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
	}

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

// buildWorkspaceName creates a valid Coder workspace name from lab name, labID,
// and template name. Coder enforces a maximum of 32 characters and the pattern
// ^[a-zA-Z0-9]([a-zA-Z0-9-]{0,30}[a-zA-Z0-9])?$.
//
// Workspace names are unique per user in Coder, so username is not needed.
// The resulting format is: {labName}-{templateName}-{shortLabID}
// where shortLabID is the last 10 characters of labID for uniqueness.
// If the full name exceeds 32 chars the lab name is truncated first, then the
// template name, so the unique suffix is always preserved.
func buildWorkspaceName(labName, labID string, templateName string) string {
	const maxLen = 32

	// sanitizeForName lowercases and replaces non-alphanumeric chars with hyphens.
	sanitize := func(s string) string {
		sanitized := strings.ToLower(s)
		var sb strings.Builder
		prevHyphen := false
		for _, r := range sanitized {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
				sb.WriteRune(r)
				prevHyphen = false
			} else if !prevHyphen && sb.Len() > 0 {
				sb.WriteRune('-')
				prevHyphen = true
			}
		}
		return strings.TrimRight(sb.String(), "-")
	}

	labName = sanitize(labName)
	if labName == "" {
		labName = "lab"
	}

	templatePart := sanitize(templateName)
	if templatePart == "" {
		templatePart = "default"
	}

	// Preserve the last 10 chars of labID for uniqueness (nanosecond timestamp suffix).
	shortLabID := labID
	if len(shortLabID) > 10 {
		shortLabID = shortLabID[len(shortLabID)-10:]
	}

	// Fixed overhead: two hyphens + shortLabID
	overhead := 2 + len(shortLabID)
	budget := maxLen - overhead
	if budget < 2 {
		budget = 2
	}

	// Split budget between lab name and template name
	half := budget / 2
	trimmedLabName := labName
	trimmedTemplate := templatePart

	if len(trimmedLabName) > half {
		trimmedLabName = strings.TrimRight(trimmedLabName[:half], "-")
	}
	remaining := budget - len(trimmedLabName)
	if len(trimmedTemplate) > remaining {
		trimmedTemplate = strings.TrimRight(trimmedTemplate[:remaining], "-")
	}
	if trimmedTemplate == "" {
		trimmedTemplate = "t"
	}

	name := trimmedLabName + "-" + trimmedTemplate + "-" + shortLabID
	if len(name) > maxLen {
		name = name[:maxLen]
		name = strings.TrimRight(name, "-")
	}
	return name
}

// DeleteLab removes a destroyed or failed lab from the list and its persisted file.
func (h *Handler) DeleteLab(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract lab ID from path: /api/labs/{id}/delete or /api/jobs/{id}/delete
	pathParts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(pathParts) < 4 || pathParts[0] != "api" || (pathParts[1] != "labs" && pathParts[1] != "jobs") || pathParts[3] != "delete" {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}
	labID := pathParts[2]

	job, exists := h.jobManager.GetJob(labID)
	if !exists {
		http.Error(w, "Lab not found", http.StatusNotFound)
		return
	}

	job.mu.RLock()
	status := job.Status
	job.mu.RUnlock()

	if status != JobStatusDestroyed && status != JobStatusFailed {
		http.Error(w, "Lab can only be removed when destroyed or failed", http.StatusBadRequest)
		return
	}

	if err := h.jobManager.RemoveJob(labID); err != nil {
		log.Printf("Failed to remove lab %s: %v", labID, err)
		http.Error(w, fmt.Sprintf("Failed to remove lab: %v", err), http.StatusInternalServerError)
		return
	}
	// Drop any credentials still waiting for a cluster that will now never exist.
	h.pendingSecrets.Discard(labID)

	http.Redirect(w, r, "/labs", http.StatusSeeOther)
}

// DetectTemplateVariables parses uploaded .tf/.zip files or clones a Git repo
// to extract Terraform variable blocks from template source files.
func (h *Handler) DetectTemplateVariables(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	source := r.FormValue("source")

	var variables []tfparse.TFVariable
	var err error

	switch source {
	case "upload":
		variables, err = h.detectVariablesFromUpload(r)
	case "git":
		variables, err = h.detectVariablesFromGit(r)
	default:
		writeJSONError(w, http.StatusBadRequest, "invalid source: must be 'upload' or 'git'")
		return
	}

	if err != nil {
		log.Printf("Failed to detect template variables: %v", err)
		writeJSONError(w, http.StatusUnprocessableEntity, fmt.Sprintf("failed to detect variables: %v", err))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(variables)
}

func (h *Handler) detectVariablesFromUpload(r *http.Request) ([]tfparse.TFVariable, error) {
	if err := r.ParseMultipartForm(50 << 20); err != nil {
		return nil, fmt.Errorf("failed to parse form: %w", err)
	}

	file, header, err := r.FormFile("template_file")
	if err != nil {
		return nil, fmt.Errorf("no file uploaded: %w", err)
	}
	defer file.Close()

	tmpDir, err := os.MkdirTemp("", "detect-vars-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	tmpPath := filepath.Join(tmpDir, header.Filename)
	out, err := os.Create(tmpPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file: %w", err)
	}
	if _, err := io.Copy(out, file); err != nil {
		out.Close()
		return nil, fmt.Errorf("failed to write temp file: %w", err)
	}
	out.Close()

	filename := strings.ToLower(header.Filename)
	if strings.HasSuffix(filename, ".zip") {
		return tfparse.ParseVariablesFromZip(tmpPath)
	}
	if strings.HasSuffix(filename, ".tf") {
		return tfparse.ParseVariablesFromFile(tmpPath)
	}
	return nil, fmt.Errorf("unsupported file type: expected .tf or .zip")
}

func (h *Handler) detectVariablesFromGit(r *http.Request) ([]tfparse.TFVariable, error) {
	repoURL := r.FormValue("git_repo")
	folder := r.FormValue("git_folder")
	branch := r.FormValue("git_branch")

	if repoURL == "" {
		return nil, fmt.Errorf("git_repo is required")
	}
	if branch == "" {
		branch = "main"
	}

	tmpDir, err := os.MkdirTemp("", "detect-vars-git-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	cloneOpts := &git.CloneOptions{
		URL:           repoURL,
		ReferenceName: plumbing.NewBranchReferenceName(branch),
		SingleBranch:  true,
		Depth:         1,
	}

	if _, err := git.PlainClone(tmpDir, false, cloneOpts); err != nil {
		return nil, fmt.Errorf("failed to clone repository: %w", err)
	}

	targetDir := tmpDir
	if folder != "" {
		targetDir = filepath.Join(tmpDir, folder)
		if _, err := os.Stat(targetDir); err != nil {
			return nil, fmt.Errorf("folder '%s' not found in repository: %w", folder, err)
		}
	}

	entries, err := os.ReadDir(targetDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read directory: %w", err)
	}

	seen := make(map[string]tfparse.TFVariable)
	var order []string

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".tf") {
			continue
		}
		vars, parseErr := tfparse.ParseVariablesFromFile(filepath.Join(targetDir, entry.Name()))
		if parseErr != nil {
			log.Printf("Warning: failed to parse %s: %v", entry.Name(), parseErr)
			continue
		}
		for _, v := range vars {
			if _, exists := seen[v.Name]; !exists {
				order = append(order, v.Name)
			}
			seen[v.Name] = v
		}
	}

	result := make([]tfparse.TFVariable, 0, len(order))
	for _, name := range order {
		result = append(result, seen[name])
	}
	return result, nil
}

// ServeAdminStats serves the admin statistics page.
// Route: GET /admin/stats
func (h *Handler) ServeAdminStats(w http.ResponseWriter, r *http.Request) {
	jobs := h.jobManager.GetAllJobs()

	seen := make(map[string]struct{})
	var projects []string
	for _, job := range jobs {
		if job.Config != nil && job.Config.StackName != "" {
			if _, ok := seen[job.Config.StackName]; !ok {
				seen[job.Config.StackName] = struct{}{}
				projects = append(projects, job.Config.StackName)
			}
		}
	}

	type StatsPageData struct {
		Projects        []string
		SelectedProject string
	}

	h.serveTemplate(w, "admin-stats.html", StatsPageData{
		Projects:        projects,
		SelectedProject: r.URL.Query().Get("project"),
	})
}

// GetProjectStats returns KPI totals and time-series JSON for deployed labs.
// Accepts project=<stackName> or project=__all__ for aggregated view.
// Route: GET /api/admin/stats?project=<stackName>
func (h *Handler) GetProjectStats(w http.ResponseWriter, r *http.Request) {
	project := r.URL.Query().Get("project")
	if project == "" {
		http.Error(w, "project query param required", http.StatusBadRequest)
		return
	}

	type monthBucket struct {
		succeeded int
		failed    int
		destroyed int
		cleaned   int
	}

	type projectSummary struct {
		Name   string `json:"name"`
		Total  int    `json:"total"`
		Active int    `json:"active"`
		Failed int    `json:"failed"`
	}

	type statsResponse struct {
		TotalWorkspaces int              `json:"total_workspaces"`
		TotalActive     int              `json:"total_active"`
		TotalFailed     int              `json:"total_failed"`
		TotalCleaned    int              `json:"total_cleaned"`
		Labels          []string         `json:"labels"`
		Succeeded       []int            `json:"succeeded"`
		Failed          []int            `json:"failed"`
		Destroyed       []int            `json:"destroyed"`
		Cleaned         []int            `json:"cleaned"`
		Projects        []projectSummary `json:"projects,omitempty"`
	}

	// isRealDeployment returns true for completed, failed, or destroyed jobs (excludes dry-runs and in-progress).
	isRealDeployment := func(job *Job) bool {
		return job.Status == JobStatusCompleted || job.Status == JobStatusFailed || job.Status == JobStatusDestroyed
	}

	buckets := make(map[string]*monthBucket)
	var orderedMonths []string
	addMonth := func(month string) {
		if _, ok := buckets[month]; !ok {
			buckets[month] = &monthBucket{}
			orderedMonths = append(orderedMonths, month)
		}
	}

	// Per-project KPI tallies for the __all__ summary table.
	perProject := make(map[string]*projectSummary)

	var resp statsResponse

	jobs := h.jobManager.GetAllJobs()
	// GetAllJobs returns newest-first; iterate in reverse for chronological order.
	for i := len(jobs) - 1; i >= 0; i-- {
		job := jobs[i]
		if job.Config == nil {
			continue
		}
		if !isRealDeployment(job) {
			continue
		}

		stackName := job.Config.StackName

		// Per-project summary used for __all__ table.
		if _, ok := perProject[stackName]; !ok {
			perProject[stackName] = &projectSummary{Name: stackName}
		}
		ps := perProject[stackName]
		ps.Total++
		if job.Status == JobStatusCompleted {
			ps.Active++
		}
		if job.Status == JobStatusFailed {
			ps.Failed++
		}

		// KPIs and time-series are scoped to the selected project (or all).
		if project != "__all__" && stackName != project {
			continue
		}

		if job.Status == JobStatusCompleted {
			resp.TotalActive++
		}
		if job.Status == JobStatusFailed {
			resp.TotalFailed++
		}

		month := job.CreatedAt.Format("2006-01")
		addMonth(month)
		switch job.Status {
		case JobStatusCompleted:
			buckets[month].succeeded++
		case JobStatusFailed:
			buckets[month].failed++
		case JobStatusDestroyed:
			buckets[month].destroyed++
		}

		// Cleanup events — bucketed by the event timestamp, not job creation.
		// Also accumulate total workspaces: latest snapshot (current count) + all cleaned.
		job.mu.RLock()
		latestSnapshot := 0
		for _, snap := range job.WorkspaceSnapshots {
			if snap.Count > latestSnapshot {
				latestSnapshot = snap.Count
			}
		}
		jobCleaned := 0
		for _, evt := range job.CleanupEvents {
			jobCleaned += evt.Count
			evtMonth := evt.At.Format("2006-01")
			addMonth(evtMonth)
			buckets[evtMonth].cleaned += evt.Count
		}
		job.mu.RUnlock()
		resp.TotalCleaned += jobCleaned
		// Total workspaces ever used for this job = current (latest snapshot) + already cleaned.
		resp.TotalWorkspaces += latestSnapshot + jobCleaned
	}

	// Build time-series arrays in chronological order.
	resp.Labels = orderedMonths
	resp.Succeeded = make([]int, len(orderedMonths))
	resp.Failed = make([]int, len(orderedMonths))
	resp.Destroyed = make([]int, len(orderedMonths))
	resp.Cleaned = make([]int, len(orderedMonths))
	for i, m := range orderedMonths {
		resp.Succeeded[i] = buckets[m].succeeded
		resp.Failed[i] = buckets[m].failed
		resp.Destroyed[i] = buckets[m].destroyed
		resp.Cleaned[i] = buckets[m].cleaned
	}

	// Build projects summary slice for __all__ view, sorted by total descending.
	if project == "__all__" {
		for _, ps := range perProject {
			resp.Projects = append(resp.Projects, *ps)
		}
		sort.Slice(resp.Projects, func(i, j int) bool {
			return resp.Projects[i].Total > resp.Projects[j].Total
		})
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Printf("failed to encode stats response: %v", err)
	}
}
