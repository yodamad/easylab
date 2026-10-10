package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"easylab/internal/providers/workspace/kube" // also registers the kube workspace backend
	"easylab/internal/server"
	"easylab/utils"
	"encoding/base64"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// loadEnvFile loads environment variables from a file.
// The file path is passed as a parameter.
// The file format is standard .env format: KEY=VALUE (one per line).
// Lines starting with # are treated as comments and ignored.
// Empty lines are ignored.
func loadEnvFile(envFile string) error {
	if envFile == "" {
		return nil // No env file specified, skip loading
	}

	file, err := os.Open(envFile)
	if err != nil {
		return fmt.Errorf("failed to open env file %s: %w", envFile, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	lineNum := 0
	loadedCount := 0

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Handle export KEY=VALUE format (for compatibility with shell scripts)
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimPrefix(line, "export ")
			line = strings.TrimSpace(line)
		}

		// Parse KEY=VALUE format
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			log.Printf("Warning: Skipping invalid line %d in env file %s: %s", lineNum, envFile, line)
			continue
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		// Remove quotes if present
		if len(value) >= 2 {
			if (strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`)) ||
				(strings.HasPrefix(value, `'`) && strings.HasSuffix(value, `'`)) {
				value = value[1 : len(value)-1]
			}
		}

		if key == "" {
			log.Printf("Warning: Skipping line %d in env file %s: empty key", lineNum, envFile)
			continue
		}

		// Set the environment variable
		if err := os.Setenv(key, value); err != nil {
			log.Printf("Warning: Failed to set environment variable %s from line %d: %v", key, lineNum, err)
			continue
		}
		loadedCount++
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error reading env file %s: %w", envFile, err)
	}

	log.Printf("[STARTUP] Loaded %d environment variables from %s", loadedCount, envFile)
	return nil
}

// generatedKeyFileName is the file, inside dataDir, that holds an
// auto-generated LAB_DATA_ENCRYPTION_KEY when the operator hasn't provided
// one explicitly. Reusing this file across restarts keeps previously
// persisted encrypted job data decryptable.
const generatedKeyFileName = ".encryption_key"

// loadOrGenerateEncryptionKey returns the raw (decoded) key to use when
// LAB_DATA_ENCRYPTION_KEY is not set in the environment. It reuses a
// previously generated key file under dataDir if present, otherwise it
// generates a new random 32-byte key and persists it there (mode 0600) so
// subsequent restarts pick up the same key.
func loadOrGenerateEncryptionKey(dataDir string) ([]byte, error) {
	keyPath := filepath.Join(dataDir, generatedKeyFileName)

	if existing, err := os.ReadFile(keyPath); err == nil {
		key, decodeErr := base64.StdEncoding.DecodeString(strings.TrimSpace(string(existing)))
		if decodeErr != nil {
			return nil, fmt.Errorf("existing generated key file %s is not valid base64: %w", keyPath, decodeErr)
		}
		log.Printf("[STARTUP] LAB_DATA_ENCRYPTION_KEY not set; reusing previously generated key from %s", keyPath)
		return key, nil
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("failed to read generated key file %s: %w", keyPath, err)
	}

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("failed to generate encryption key: %w", err)
	}
	encoded := base64.StdEncoding.EncodeToString(key)
	if err := os.WriteFile(keyPath, []byte(encoded), 0600); err != nil {
		return nil, fmt.Errorf("failed to persist generated encryption key to %s: %w", keyPath, err)
	}
	log.Printf("[STARTUP] LAB_DATA_ENCRYPTION_KEY not set; generated a new key and saved it to %s. For production, set LAB_DATA_ENCRYPTION_KEY explicitly from a managed secret store instead of relying on this file.", keyPath)
	return key, nil
}

// initDataEncryption configures at-rest encryption for persisted job files from
// LAB_DATA_ENCRYPTION_KEY (base64-encoded 32 bytes / AES-256). When persistence
// is enabled (dataDir is set) and no key is provided, one is generated and
// persisted under dataDir (see loadOrGenerateEncryptionKey) so the server can
// still start and encrypted data remains decryptable across restarts.
func initDataEncryption(dataDir string) error {
	raw := strings.TrimSpace(os.Getenv("LAB_DATA_ENCRYPTION_KEY"))
	var key []byte
	if raw == "" {
		if dataDir == "" {
			return nil // persistence disabled, no key needed
		}
		generated, err := loadOrGenerateEncryptionKey(dataDir)
		if err != nil {
			return fmt.Errorf("failed to obtain data encryption key: %w", err)
		}
		key = generated
	} else {
		decoded, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return fmt.Errorf("LAB_DATA_ENCRYPTION_KEY must be base64-encoded: %w", err)
		}
		key = decoded
	}
	if err := server.InitDataEncryption(key); err != nil {
		return fmt.Errorf("invalid LAB_DATA_ENCRYPTION_KEY: %w", err)
	}
	log.Printf("[STARTUP] Job data at-rest encryption enabled")
	return nil
}

// explicitEncryptionKey returns the decoded LAB_DATA_ENCRYPTION_KEY when the
// operator set it in the environment, and nil when the key in use is the one
// auto-generated under dataDir. The credential vault is only ever saved to disk
// under a key that does not sit beside it, so it must tell the two apart.
func explicitEncryptionKey() []byte {
	raw := strings.TrimSpace(os.Getenv("LAB_DATA_ENCRYPTION_KEY"))
	if raw == "" {
		return nil
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil // initDataEncryption has already rejected it
	}
	return key
}

func main() {
	// Pulumi SDK v3.243+ calls slog.SetDefault(discardHandler{}) in its init(),
	// which silently swallows all standard log.Printf output. Restore stderr.
	log.SetOutput(os.Stderr)

	startTime := time.Now()

	var (
		port    = flag.String("port", "8081", "Port to listen on")
		workDir = flag.String("work-dir", utils.DEFAULT_WORK_DIR, "Directory for job workspaces")
		dataDir = flag.String("data-dir", utils.DEFAULT_DATA_DIR, "Directory for persisting job data")
		envFile = flag.String("env-file", "", "Path to environment file to load at startup")
		modeArg = flag.String("mode", "", "Areas to serve: all (default), admin, or student (in-lab portal). Defaults to "+server.EnvMode)
	)
	flag.Parse()

	// Load environment variables from file if specified
	if *envFile != "" {
		if err := loadEnvFile(*envFile); err != nil {
			log.Fatalf("Failed to load env file: %v", err)
		}
	}

	if *modeArg == "" {
		*modeArg = os.Getenv(server.EnvMode)
	}
	mode, err := server.ParseMode(*modeArg)
	if err != nil {
		log.Fatalf("Invalid mode: %v", err)
	}
	log.Printf("[STARTUP] Mode: %s", mode)

	// An in-lab student portal shares none of the admin's state: no jobs on
	// disk, no Pulumi, no provider credentials. It has its own, much shorter, startup.
	if mode == server.ModeStudent {
		if err := runStudentPortal(*port); err != nil {
			log.Fatalf("Student portal failed: %v", err)
		}
		return
	}

	// Get default values from environment variables if set, otherwise use hardcoded defaults
	defaultWorkDir := os.Getenv("WORK_DIR")
	if (*workDir == "" || *workDir == utils.DEFAULT_WORK_DIR) && defaultWorkDir != "" {
		*workDir = defaultWorkDir
	}

	defaultDataDir := os.Getenv("DATA_DIR")
	if (*dataDir == "" || *dataDir == utils.DEFAULT_DATA_DIR) && defaultDataDir != "" {
		*dataDir = defaultDataDir
	}

	appCtx, appCancel := context.WithCancel(context.Background())
	defer appCancel()

	log.Printf("[STARTUP] Starting application initialization...")

	// Create work directory if it doesn't exist
	dirStart := time.Now()
	if err := os.MkdirAll(*workDir, 0755); err != nil {
		log.Fatalf("Failed to create work directory: %v", err)
	}
	if err := os.MkdirAll(*dataDir, 0755); err != nil {
		log.Fatalf("Failed to create data directory: %v", err)
	}
	log.Printf("[STARTUP] Directory creation took %v", time.Since(dirStart))

	// Initialize independent components in parallel
	var (
		jobManager          *server.JobManager
		credentialsManager  *server.CredentialsManager
		authHandler         *server.AuthHandler
		ovhOptionsManager   *server.OVHOptionsManager
		azureOptionsManager *server.AzureOptionsManager
		pulumiExec          *server.PulumiExecutor
		handler             *server.Handler
	)

	var wg sync.WaitGroup
	wg.Add(3)

	// Initialize jobManager
	go func() {
		defer wg.Done()
		jobManager = server.NewJobManager(*dataDir)
	}()

	// Initialize credentialsManager
	go func() {
		defer wg.Done()
		credentialsManager = server.NewCredentialsManager()
	}()

	// Initialize authHandler
	go func() {
		defer wg.Done()
		var err error
		authHandler, err = server.NewAuthHandler()
		if err != nil {
			log.Fatalf("Failed to initialize auth handler: %v", err)
		}
	}()

	// Wait for independent components
	parallelStart := time.Now()
	wg.Wait()
	log.Printf("[STARTUP] Parallel component initialization took %v", time.Since(parallelStart))

	// Initialize at-rest encryption for persisted job files before any job is
	// loaded or saved. Mandatory when persistence is enabled.
	if err := initDataEncryption(*dataDir); err != nil {
		log.Fatalf("%v", err)
	}
	if p := os.Getenv("PULUMI_CONFIG_PASSPHRASE"); p == "" || p == "passphrase" {
		log.Printf("[SECURITY] PULUMI_CONFIG_PASSPHRASE is unset or the insecure default \"passphrase\" — set a strong value to protect secrets in Pulumi stack state. Existing stacks remain decryptable.")
	}

	// Credential vault: provider credentials and DNS profiles saved across
	// restarts. Attached before the options managers below so they see saved
	// credentials from the start.
	credentialVault := server.NewCredentialVault(*dataDir, explicitEncryptionKey())
	credentialsManager.AttachVault(credentialVault)
	log.Printf("[STARTUP] Credential storage: %s", credentialVault.Status().State)

	// Initialize OVH options manager (depends on credentialsManager)
	ovhOptionsStart := time.Now()
	ovhOptionsManager = server.NewOVHOptionsManager(*dataDir, credentialsManager)
	log.Printf("[STARTUP] OVHOptionsManager initialization took %v", time.Since(ovhOptionsStart))

	// Initialize Azure options manager (depends on credentialsManager)
	azureOptionsStart := time.Now()
	azureOptionsManager = server.NewAzureOptionsManager(*dataDir, credentialsManager)
	log.Printf("[STARTUP] AzureOptionsManager initialization took %v", time.Since(azureOptionsStart))

	// Initialize pulumiExec (depends on jobManager)
	pulumiStart := time.Now()
	pulumiExec = server.NewPulumiExecutor(jobManager, *workDir)
	log.Printf("[STARTUP] PulumiExecutor initialization took %v", time.Since(pulumiStart))

	// Check and install required Pulumi plugins at startup.
	go func() {
		pluginStart := time.Now()
		pulumiExec.CheckAndInstallPlugins()
		log.Printf("[STARTUP] Pulumi plugin check completed in %v", time.Since(pluginStart))
	}()

	// Initialize feedback store (persists alongside job data)
	feedbackStore, err := server.NewFeedbackStore(filepath.Join(*dataDir, "feedback"))
	if err != nil {
		log.Fatalf("Failed to initialize feedback store: %v", err)
	}

	// Initialize audit log store (persists alongside job data)
	auditStore, err := server.NewAuditStore(filepath.Join(*dataDir, "audit"))
	if err != nil {
		log.Fatalf("Failed to initialize audit store: %v", err)
	}

	// Initialize handler (depends on all components)
	handlerStart := time.Now()
	handler = server.NewHandler(jobManager, pulumiExec, credentialsManager, ovhOptionsManager, azureOptionsManager, feedbackStore)
	handler.SetAzureADConfigurer(authHandler.ConfigureAzureAD)
	handler.SetClassicLoginConfigurer(authHandler.SetClassicLoginDisabled)
	handler.SetAdminGroupIDConfigurer(authHandler.SetAdminGroupID)
	handler.SetClassicAdminLoginConfigurer(authHandler.SetClassicAdminLoginDisabled)
	handler.SetAuditStore(auditStore)
	handler.SetCredentialVault(credentialVault)
	log.Printf("[STARTUP] Handler initialization took %v", time.Since(handlerStart))

	// Apply persisted Azure AD config (overrides env vars if set via UI)
	if azureAD := azureOptionsManager.GetAzureADConfig(); azureAD.ClientID != "" && azureAD.ClientSecret != "" && azureAD.TenantID != "" {
		authHandler.ConfigureAzureAD(azureAD.ClientID, azureAD.ClientSecret, azureAD.TenantID)
		authHandler.SetClassicLoginDisabled(azureAD.DisableClassicLogin)
		if azureAD.AdminGroupID != "" {
			authHandler.SetAdminGroupID(azureAD.AdminGroupID)
		}
		authHandler.SetClassicAdminLoginDisabled(azureAD.DisableClassicAdminLogin)
		log.Printf("[STARTUP] Azure AD config loaded from persisted storage")
	}

	// GitHub student login settings (persisted, secret encrypted at rest — must
	// come after initDataEncryption above).
	githubAuthStore := server.NewGitHubAuthStore(*dataDir)
	handler.SetGitHubAuth(githubAuthStore, authHandler.ConfigureGitHub)
	if githubCfg := githubAuthStore.Get(); githubCfg.Enabled() {
		authHandler.ConfigureGitHub(githubCfg)
		log.Printf("[STARTUP] GitHub login config loaded from persisted storage")
	}

	// GitLab student login settings, persisted the same way.
	gitlabAuthStore := server.NewGitLabAuthStore(*dataDir)
	handler.SetGitLabAuth(gitlabAuthStore, authHandler.ConfigureGitLab)
	if gitlabCfg := gitlabAuthStore.Get(); gitlabCfg.Enabled() {
		authHandler.ConfigureGitLab(gitlabCfg)
		log.Printf("[STARTUP] GitLab login config loaded from persisted storage")
	}

	go handler.StartWorkspaceCleanup(appCtx)

	// In-lab student portals mirror this instance's student sign-in settings, and
	// send students here for the providers only this instance is registered with.
	// Both the public URL and the portal image default from the environment and
	// can be overridden from the Student portals admin page (persisted).
	publicURL := os.Getenv(server.EnvPublicURL)
	portalSettings := server.NewPortalSettingsStore(*dataDir)
	handler.SetMode(mode)
	handler.SetPortalAuth(publicURL, authHandler.StudentAuthSnapshot)
	handler.SetPortalSettingsStore(portalSettings)
	authHandler.SetBrokerLabResolver(handler.BrokerPortal)
	authHandler.SetBrokerOnly(mode == server.ModeAdmin)
	if publicURL == "" && portalSettings.Get().PublicURL == "" {
		log.Printf("[STARTUP] No public address configured: in-lab student portals will offer password login only. Set it on the Student portals admin page or with %s", server.EnvPublicURL)
	}

	// Setup routes
	mux := buildMux(mode, handler, authHandler)

	// Configure server with timeouts
	addr := fmt.Sprintf(":%s", *port)
	srv := newHTTPServer(addr, mux)

	// Start server in goroutine
	go func() {
		log.Printf("[STARTUP] Total initialization time: %v", time.Since(startTime))
		log.Printf("Starting server on http://localhost%s", addr)
		log.Printf("Work directory: %s", *workDir)
		log.Printf("Data directory: %s", *dataDir)
		log.Printf("Set %s environment variable to configure admin password", server.EnvAdminPassword)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server failed: %v", err)
		}
	}()

	// Load persisted jobs asynchronously after server starts (non-blocking)
	if *dataDir != "" {
		go func() {
			log.Printf("Loading persisted jobs from %s...", *dataDir)
			if err := jobManager.LoadJobs(); err != nil {
				log.Printf("Warning: failed to load persisted jobs: %v", err)
			}
		}()
	}

	// Pre-populate OVH options cache asynchronously if credentials are available
	if credentialsManager.HasCredentials("ovh") {
		go func() {
			refreshStart := time.Now()
			if err := ovhOptionsManager.RefreshFromAPI(); err != nil {
				log.Printf("[STARTUP] Warning: OVH options cache refresh failed: %v", err)
			} else {
				log.Printf("[STARTUP] OVH options cache refresh completed in %v", time.Since(refreshStart))
			}
		}()
	}

	// Pre-populate Azure options cache asynchronously if credentials are available
	if credentialsManager.HasCredentials("azure") {
		go func() {
			refreshStart := time.Now()
			if err := azureOptionsManager.RefreshFromAPI(); err != nil {
				log.Printf("[STARTUP] Warning: Azure options cache refresh failed: %v", err)
			} else {
				log.Printf("[STARTUP] Azure options cache refresh completed in %v", time.Since(refreshStart))
			}
		}()
	}

	// Wait for interrupt signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	appCancel()
	log.Println("Shutting down server...")

	// Graceful shutdown with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("Server forced to shutdown: %v", err)
	}

	log.Println("Server exited")
}

// buildMux registers the routes of the areas mode serves. The paths themselves
// do not depend on the mode — an area is either all there or absent — so a
// combined instance and a split admin/student pair answer on the same URLs.
//
// The route functions live in this file on purpose: the Playwright web server
// builds it on its own (go build cmd/server/main.go).
func buildMux(mode server.Mode, handler *server.Handler, authHandler *server.AuthHandler) *http.ServeMux {
	mux := http.NewServeMux()
	registerCommonRoutes(mux, handler)
	if mode.ServesStudent() {
		registerStudentRoutes(mux, handler, authHandler)
	}
	if mode == server.ModeStudent {
		registerPortalSignInRoutes(mux, authHandler)
	} else {
		registerProviderSignInRoutes(mux, authHandler)
	}
	if mode.ServesAdmin() {
		registerAdminRoutes(mux, handler, authHandler)
	}
	return mux
}

// registerCommonRoutes registers what every mode serves, none of it behind auth.
func registerCommonRoutes(mux *http.ServeMux, handler *server.Handler) {
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})
	mux.HandleFunc("/static/", handler.ServeStatic) // Static files don't need auth

	// Public homepage (no auth required)
	mux.HandleFunc("/", handler.ServeUI)
}

// registerStudentRoutes registers the student portal (public login, protected dashboard).
func registerStudentRoutes(mux *http.ServeMux, handler *server.Handler, authHandler *server.AuthHandler) {
	mux.HandleFunc("/student/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			authHandler.HandleStudentLogin(w, r)
		} else {
			authHandler.ServeStudentLogin(w, r)
		}
	})
	mux.HandleFunc("/student/logout", authHandler.HandleStudentLogout)
	mux.HandleFunc("/student/dashboard", authHandler.RequireStudentAuth(handler.ServeStudentDashboard))
	mux.HandleFunc("/student/workspaces", authHandler.RequireStudentAuth(handler.ServeStudentWorkspaces))
	mux.HandleFunc("/student/feedback", authHandler.RequireStudentAuth(handler.ServeFeedback))
	mux.HandleFunc("/api/student/labs", authHandler.RequireStudentAuth(handler.ListLabs))
	mux.HandleFunc("/api/student/labs/templates", authHandler.RequireStudentAuth(handler.ListLabTemplates))
	mux.HandleFunc("/api/student/workspaces", authHandler.RequireStudentAuth(handler.ListStudentWorkspaces))
	mux.HandleFunc("/api/student/workspace/request", authHandler.RequireStudentAuth(handler.RequestWorkspace))
	mux.HandleFunc("/api/student/workspace/status", authHandler.RequireStudentAuth(handler.WorkspaceStatus))
	mux.HandleFunc("/api/student/workspace/open", authHandler.RequireStudentAuth(handler.OpenWorkspace))
	mux.HandleFunc("/api/student/workspace/delete", authHandler.RequireStudentAuth(handler.DeleteStudentWorkspace))
	mux.HandleFunc("/api/student/workspace/access", authHandler.RequireStudentAuth(handler.WorkspaceTeacherAccess))
	mux.HandleFunc("/api/student/feedback", authHandler.RequireStudentAuth(handler.SubmitFeedback))
}

// registerProviderSignInRoutes registers the student sign-in flows this instance
// runs against Azure AD, GitHub and GitLab — its callbacks are the ones
// registered with them. A combined instance opens its own student sessions from
// them; both it and an admin-only instance also run them on behalf of the in-lab
// student portals (see internal/server/broker.go).
func registerProviderSignInRoutes(mux *http.ServeMux, authHandler *server.AuthHandler) {
	mux.HandleFunc("/student/auth/azure/login", authHandler.HandleAzureADLogin)
	mux.HandleFunc("/student/auth/azure/callback", authHandler.HandleAzureADCallback)
	mux.HandleFunc("/student/auth/github/login", authHandler.HandleGitHubLogin)
	mux.HandleFunc("/student/auth/github/callback", authHandler.HandleGitHubCallback)
	mux.HandleFunc("/student/auth/gitlab/login", authHandler.HandleGitLabLogin)
	mux.HandleFunc("/student/auth/gitlab/callback", authHandler.HandleGitLabCallback)
}

// registerPortalSignInRoutes registers the same login paths on an in-lab student
// portal, where they hand the sign-in to the central instance and take its
// answer back on the broker callback.
func registerPortalSignInRoutes(mux *http.ServeMux, authHandler *server.AuthHandler) {
	mux.HandleFunc("/student/auth/azure/login", authHandler.HandlePortalProviderLogin("azure"))
	mux.HandleFunc("/student/auth/github/login", authHandler.HandlePortalProviderLogin("github"))
	mux.HandleFunc("/student/auth/gitlab/login", authHandler.HandlePortalProviderLogin("gitlab"))
	mux.HandleFunc("/student/auth/broker/callback", authHandler.HandlePortalBrokerCallback)
}

// registerAdminRoutes registers the admin area: its login, then everything behind it.
func registerAdminRoutes(mux *http.ServeMux, handler *server.Handler, authHandler *server.AuthHandler) {
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			authHandler.HandleLogin(w, r)
		} else {
			authHandler.ServeLogin(w, r)
		}
	})
	mux.HandleFunc("/logout", authHandler.HandleLogout)

	// Admin Azure AD login routes (public — redirect to /admin on success)
	mux.HandleFunc("/admin/auth/azure/login", authHandler.HandleAdminAzureADLogin)
	mux.HandleFunc("/admin/auth/azure/callback", authHandler.HandleAdminAzureADCallback)

	// Protected routes (auth required)
	mux.HandleFunc("/admin", authHandler.RequireAuth(handler.ServeAdminUI))
	mux.HandleFunc("/admin/feedback", authHandler.RequireAuth(handler.ServeAdminLabFeedback))
	mux.HandleFunc("/admin/feedback/export", authHandler.RequireAuth(handler.ExportLabFeedbackCSV))
	mux.HandleFunc("/admin/audit-log", authHandler.RequireAuth(handler.ServeAuditLog))
	mux.HandleFunc("/admin/stats", authHandler.RequireAuth(handler.ServeAdminStats))
	mux.HandleFunc("/api/admin/stats", authHandler.RequireAuth(handler.GetProjectStats))
	mux.HandleFunc("/labs", authHandler.RequireAuth(handler.ServeLabsList))
	// Backward compatibility route
	mux.HandleFunc("/jobs", authHandler.RequireAuth(handler.ServeLabsList))

	// New generic credentials routes
	mux.HandleFunc("/credentials", authHandler.RequireAuth(handler.ServeCredentials))
	mux.HandleFunc("/api/credentials", authHandler.RequireAuth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			handler.SetCredentials(w, r)
		} else if r.Method == http.MethodGet {
			handler.GetCredentials(w, r)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	}))
	mux.HandleFunc("/api/providers", authHandler.RequireAuth(handler.ListProviders))

	// Saved DNS profiles and the encrypted credential storage behind them
	mux.HandleFunc("/admin/dns", authHandler.RequireAuth(handler.ServeDNSProfiles))
	mux.HandleFunc("/api/dns-profiles", authHandler.RequireAuth(handler.SaveDNSProfile))
	mux.HandleFunc("/api/dns-profiles/fields", authHandler.RequireAuth(handler.DNSProfileFields))
	mux.HandleFunc("/api/dns-profiles/delete", authHandler.RequireAuth(handler.DeleteDNSProfile))
	mux.HandleFunc("/api/credential-vault/passphrase", authHandler.RequireAuth(handler.SetVaultPassphrase))
	mux.HandleFunc("/api/credential-vault/unlock", authHandler.RequireAuth(handler.UnlockVault))
	mux.HandleFunc("/api/credential-vault/lock", authHandler.RequireAuth(handler.LockVault))
	mux.HandleFunc("/api/credential-vault/reset", authHandler.RequireAuth(handler.ResetVault))

	// Backward compatibility routes for OVH-specific endpoints
	mux.HandleFunc("/ovh-credentials", authHandler.RequireAuth(handler.ServeOVHCredentials))
	mux.HandleFunc("/api/ovh-credentials", authHandler.RequireAuth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			handler.SetOVHCredentials(w, r)
		} else if r.Method == http.MethodGet {
			handler.GetOVHCredentials(w, r)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	}))
	mux.HandleFunc("/api/ovh/regions", authHandler.RequireAuth(handler.GetOVHRegions))
	mux.HandleFunc("/api/ovh/flavors", authHandler.RequireAuth(handler.GetOVHFlavors))
	mux.HandleFunc("/admin/ovh-options", authHandler.RequireAuth(handler.ServeOVHOptions))
	mux.HandleFunc("/api/ovh-options", authHandler.RequireAuth(handler.SaveOVHOptions))
	mux.HandleFunc("/api/ovh-options/refresh", authHandler.RequireAuth(handler.RefreshOVHOptions))

	// Azure-specific routes
	mux.HandleFunc("/azure-credentials", authHandler.RequireAuth(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/credentials?provider=azure", http.StatusMovedPermanently)
	}))
	mux.HandleFunc("/admin/azure-options", authHandler.RequireAuth(handler.ServeAzureOptions))
	mux.HandleFunc("/admin/azure-provider", authHandler.RequireAuth(handler.ServeAzureProvider))
	mux.HandleFunc("/admin/azure-ad", authHandler.RequireAuth(handler.ServeAzureAD))
	mux.HandleFunc("/api/azure/locations", authHandler.RequireAuth(handler.GetAzureLocations))
	mux.HandleFunc("/api/azure/vm-sizes", authHandler.RequireAuth(handler.GetAzureVMSizes))
	mux.HandleFunc("/api/azure-options/region-vm-sizes", authHandler.RequireAuth(handler.GetAzureOptionsRegionVMSizeHTML))
	mux.HandleFunc("/api/azure-options", authHandler.RequireAuth(handler.SaveAzureOptions))
	mux.HandleFunc("/api/azure-options/refresh", authHandler.RequireAuth(handler.RefreshAzureOptions))
	mux.HandleFunc("/api/azure-ad-config", authHandler.RequireAuth(handler.SaveAzureADConfig))
	mux.HandleFunc("/admin/github", authHandler.RequireAuth(handler.ServeGitHubAuth))
	mux.HandleFunc("/api/github-auth-config", authHandler.RequireAuth(handler.SaveGitHubAuthConfig))
	mux.HandleFunc("/admin/gitlab", authHandler.RequireAuth(handler.ServeGitLabAuth))
	mux.HandleFunc("/api/gitlab-auth-config", authHandler.RequireAuth(handler.SaveGitLabAuthConfig))
	mux.HandleFunc("/admin/student-portals", authHandler.RequireAuth(handler.ServePortalSettings))
	mux.HandleFunc("/api/portal-settings", authHandler.RequireAuth(handler.SavePortalSettings))
	mux.HandleFunc("/api/student-portal-password", authHandler.RequireAuth(handler.GetStudentPortalPassword))
	mux.HandleFunc("/api/templates/detect-variables", authHandler.RequireAuth(handler.DetectTemplateVariables))
	mux.HandleFunc("/api/templates/detect-devcontainer", authHandler.RequireAuth(handler.DetectDevcontainer))
	mux.HandleFunc("/api/labs", authHandler.RequireAuth(handler.CreateLab))
	mux.HandleFunc("/api/labs/templates/yaml", authHandler.RequireAuth(handler.ServeWorkspaceTemplatesYAML))
	mux.HandleFunc("/api/labs/templates/yaml/validate", authHandler.RequireAuth(handler.ValidateWorkspaceTemplatesYAML))
	mux.HandleFunc("/api/labs/dry-run", authHandler.RequireAuth(handler.DryRunLab))
	mux.HandleFunc("/api/labs/launch", authHandler.RequireAuth(handler.LaunchLab))
	mux.HandleFunc("/api/labs/recreate", authHandler.RequireAuth(handler.RecreateLab))
	mux.HandleFunc("/api/stacks/destroy", authHandler.RequireAuth(handler.DestroyStack))
	routeLabRequest := labRequestRouter(handler)
	mux.HandleFunc("/api/labs/", authHandler.RequireAuth(routeLabRequest))
	// Backward compatibility route
	mux.HandleFunc("/api/jobs/", authHandler.RequireAuth(routeLabRequest))
	// Per-lab pages: the old dedicated workspaces page now redirects into the
	// consolidated lab detail page (see ServeLabWorkspaces/ServeLabDetail).
	mux.HandleFunc("/labs/", authHandler.RequireAuth(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/workspaces") {
			handler.ServeLabWorkspaces(w, r)
			return
		}
		handler.ServeLabDetail(w, r)
	}))
}

// newHTTPServer configures the HTTP server with the timeouts every mode uses.
func newHTTPServer(addr string, mux *http.ServeMux) *http.Server {
	return &http.Server{
		Addr:         addr,
		Handler:      noStoreByDefault(mux),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 5 * time.Minute, // long enough for log-streaming and kubeconfig responses
		IdleTimeout:  60 * time.Second,
	}
}

// runStudentPortal runs the server as the student portal of a single lab, from
// inside that lab's cluster (--mode student). Which lab, and in which namespace,
// comes from the environment the admin set on the portal's Deployment; everything
// else it knows about the lab is read from the cluster (see server.PortalRuntime).
func runStudentPortal(port string) error {
	labID := strings.TrimSpace(os.Getenv(kube.EnvPortalLabID))
	if labID == "" {
		return fmt.Errorf("%s is not set", kube.EnvPortalLabID)
	}
	namespace := strings.TrimSpace(os.Getenv(kube.EnvPortalNamespace))

	backend, err := kube.NewInCluster(namespace)
	if err != nil {
		return fmt.Errorf("failed to reach the lab cluster: %w", err)
	}

	jobManager := server.NewJobManager("") // a mirror of the admin's lab, never persisted here
	authHandler := server.NewStudentAuthHandler()
	handler := server.NewHandler(jobManager, nil, nil, nil, nil, nil)
	handler.SetMode(server.ModeStudent)

	runtime, err := server.NewPortalRuntime(labID, backend, jobManager, authHandler)
	if err != nil {
		return fmt.Errorf("failed to start portal runtime: %w", err)
	}
	handler.UsePortalRuntime(runtime)

	appCtx, appCancel := context.WithCancel(context.Background())
	defer appCancel()
	runtimeDone := make(chan struct{})
	go func() {
		defer close(runtimeDone)
		runtime.Run(appCtx)
	}()

	addr := fmt.Sprintf(":%s", port)
	srv := newHTTPServer(addr, buildMux(server.ModeStudent, handler, authHandler))
	go func() {
		log.Printf("Starting student portal for lab %s on http://localhost%s", labID, addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server failed: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("Shutting down student portal...")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	shutdownErr := srv.Shutdown(ctx)

	// Stop the runtime only once no request can report anything more: its last
	// act is to write out what is still queued for the admin.
	appCancel()
	<-runtimeDone

	if shutdownErr != nil {
		return fmt.Errorf("server forced to shutdown: %w", shutdownErr)
	}
	log.Println("Student portal exited")
	return nil
}

// labRoute names one of the endpoints under /api/labs/{id}/... (and the
// backward-compatible /api/jobs/{id}/... prefix).
//
// Resolving the route is kept separate from dispatching it so the precedence
// between the cases can be tested on its own. That matters here more than it
// looks: several of these patterns overlap — "/secrets/delete" also ends with
// "/delete", and "/workspaces/{id}/delete" contains both "/workspaces" and
// "delete" — so the routes are ordered, and reordering them silently sends a
// request somewhere else.
type labRoute int

const (
	// routeJobStatus is the fallback: anything not matched below.
	routeJobStatus labRoute = iota
	routeJobStatusJSON
	routeListWorkspaces
	routeDeleteWorkspace
	routeDeleteLabSecret
	routeSaveLabSecret
	routeServeLabSecrets
	routeDeleteLab
	routeRetryJob
	routeUploadTemplate
	routeRemoveTemplate
	routePromoteTemplate
	routeSetTemplateAvailability
	routeSetLabAvailability
	routeBakeTemplate
	routeBakeTemplateStatus
	routeCoderCredentials
	routeKubeconfig
	routeRecreateCredentials
	routeUpdateLabLifecycle
	routeRetryJobWithConfig
	routeOpenWorkspace
	routeSetLabPortal
)

// resolveLabRoute picks the endpoint for a request. The case order is the
// contract; see labRoute.
func resolveLabRoute(path, method, format string) labRoute {
	switch {
	case strings.Contains(path, "/workspaces") && !strings.Contains(path, "/delete") && method == http.MethodGet:
		return routeListWorkspaces
	// Opening a workspace must be matched before the delete below, which takes any
	// workspace path containing "delete" — including one whose workspace name does
	// (the name embeds the student's username).
	case strings.Contains(path, "/workspaces/") && strings.HasSuffix(path, "/open") && method == http.MethodPost:
		return routeOpenWorkspace
	case strings.Contains(path, "/workspaces/") && strings.Contains(path, "delete") && method == http.MethodPost:
		return routeDeleteWorkspace

	// Secrets must be matched before the generic "/delete" below, which
	// "/secrets/delete" also ends with. DeleteLab happens to reject the path
	// anyway — it requires "delete" to be the segment straight after the lab ID —
	// but that is a second check in internal/server, and far too fine a thread to
	// leave this route hanging from.
	case strings.HasSuffix(path, "/secrets/delete") && method == http.MethodPost:
		return routeDeleteLabSecret
	case strings.HasSuffix(path, "/secrets") && method == http.MethodPost:
		return routeSaveLabSecret
	case strings.HasSuffix(path, "/secrets") && method == http.MethodGet:
		return routeServeLabSecrets

	case strings.HasSuffix(path, "/delete") && !strings.Contains(path, "/workspaces") && method == http.MethodPost:
		return routeDeleteLab
	case strings.HasSuffix(path, "/retry-with-config") && method == http.MethodPost:
		return routeRetryJobWithConfig
	case strings.HasSuffix(path, "/retry"):
		return routeRetryJob
	case strings.HasSuffix(path, "/templates/upload") && method == http.MethodPost:
		return routeUploadTemplate
	// "/remove", not "/delete": a template path ending in "/delete" would be
	// taken by routeDeleteLab above.
	case strings.Contains(path, "/templates/") && strings.HasSuffix(path, "/remove") && method == http.MethodPost:
		return routeRemoveTemplate
	case strings.Contains(path, "/templates/") && strings.HasSuffix(path, "/promote") && method == http.MethodPost:
		return routePromoteTemplate
	// The template form must be matched before the lab-wide one below, which
	// "/templates/{name}/availability" also ends with.
	case strings.Contains(path, "/templates/") && strings.HasSuffix(path, "/availability") && method == http.MethodPost:
		return routeSetTemplateAvailability
	case strings.HasSuffix(path, "/availability") && method == http.MethodPost:
		return routeSetLabAvailability
	// bake-status must be matched before the plainer /bake below, which
	// "/bake-status" does not actually share a suffix with (HasSuffix is exact),
	// but keeping the more specific check first matches this file's existing
	// convention (see /secrets/delete above /secrets).
	case strings.HasSuffix(path, "/bake-status") && method == http.MethodGet:
		return routeBakeTemplateStatus
	case strings.HasSuffix(path, "/bake") && method == http.MethodPost:
		return routeBakeTemplate
	case strings.HasSuffix(path, "/recreate-credentials") && method == http.MethodGet:
		return routeRecreateCredentials
	case strings.HasSuffix(path, "/coder-credentials") && method == http.MethodGet:
		return routeCoderCredentials
	case strings.HasSuffix(path, "/kubeconfig"):
		return routeKubeconfig
	case strings.HasSuffix(path, "/lifecycle") && method == http.MethodPost:
		return routeUpdateLabLifecycle
	// Anchored on the segment count as well as the suffix: every workspace and
	// template route sits above and so never gets here, but a path this check
	// could be confused by must not start meaning "deploy a portal" the day one of
	// those cases moves.
	case strings.HasSuffix(path, "/portal") && strings.Count(strings.Trim(path, "/"), "/") == 3 && method == http.MethodPost:
		return routeSetLabPortal
	case format == "json":
		return routeJobStatusJSON
	default:
		return routeJobStatus
	}
}

// labRequestRouter is the shared handler for /api/labs/{id}/... and the
// backward-compatible /api/jobs/{id}/... prefix.
func labRequestRouter(h *server.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch resolveLabRoute(r.URL.Path, r.Method, r.URL.Query().Get("format")) {
		case routeListWorkspaces:
			h.ListLabWorkspaces(w, r)
		case routeOpenWorkspace:
			h.OpenWorkspaceAsAdmin(w, r)
		case routeDeleteWorkspace:
			h.DeleteWorkspace(w, r)
		case routeDeleteLabSecret:
			h.DeleteLabSecret(w, r)
		case routeSaveLabSecret:
			h.SaveLabSecret(w, r)
		case routeServeLabSecrets:
			h.ServeLabSecrets(w, r)
		case routeDeleteLab:
			h.DeleteLab(w, r)
		case routeRetryJob:
			h.RetryJob(w, r)
		case routeRetryJobWithConfig:
			h.RetryJobWithConfig(w, r)
		case routeUploadTemplate:
			h.UploadTemplateToLab(w, r)
		case routeRemoveTemplate:
			h.RemoveTemplateFromLab(w, r)
		case routePromoteTemplate:
			h.PromoteTemplate(w, r)
		case routeSetTemplateAvailability:
			h.SetTemplateAvailability(w, r)
		case routeSetLabAvailability:
			h.SetLabAvailability(w, r)
		case routeBakeTemplate:
			h.BakeTemplate(w, r)
		case routeBakeTemplateStatus:
			h.BakeTemplateStatus(w, r)
		case routeRecreateCredentials:
			h.ServeRecreateCredentials(w, r)
		case routeCoderCredentials:
			h.GetCoderCredentials(w, r)
		case routeKubeconfig:
			h.DownloadKubeconfig(w, r)
		case routeJobStatusJSON:
			h.GetJobStatusJSON(w, r)
		case routeUpdateLabLifecycle:
			h.UpdateLabLifecycle(w, r)
		case routeSetLabPortal:
			h.SetLabPortal(w, r)
		default:
			h.GetJobStatus(w, r)
		}
	}
}

// noStoreByDefault makes "not cacheable" the default for every response, so a
// handler has to opt in to caching rather than opt out. Without it the HTMX
// fragment and JSON endpoints send no cache headers at all, which leaves them
// heuristically cacheable — the same trap that made stale static assets survive a
// deploy. Handlers that set their own Cache-Control (serveTemplate, the login
// pages, ServeStatic) overwrite this via Header().Set and are unaffected.
func noStoreByDefault(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}
