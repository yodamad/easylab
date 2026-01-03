package main

import (
	"context"
	"flag"
	"fmt"
	"labascode/internal/server"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

func main() {
	startTime := time.Now()

	var (
		port    = flag.String("port", "8080", "Port to listen on")
		workDir = flag.String("work-dir", "/tmp/lab-as-code-jobs", "Directory for job workspaces")
		dataDir = flag.String("data-dir", "/tmp/lab-as-code-data", "Directory for persisting job data")
	)
	flag.Parse()

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
		jobManager         *server.JobManager
		credentialsManager *server.CredentialsManager
		authHandler        *server.AuthHandler
		pulumiExec         *server.PulumiExecutor
		handler            *server.Handler
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
		authHandler = server.NewAuthHandler()
	}()

	// Wait for independent components
	parallelStart := time.Now()
	wg.Wait()
	log.Printf("[STARTUP] Parallel component initialization took %v", time.Since(parallelStart))

	// Initialize pulumiExec (depends on jobManager)
	pulumiStart := time.Now()
	pulumiExec = server.NewPulumiExecutor(jobManager, *workDir)
	log.Printf("[STARTUP] PulumiExecutor initialization took %v", time.Since(pulumiStart))

	// Initialize handler (depends on all components)
	handlerStart := time.Now()
	handler = server.NewHandler(jobManager, pulumiExec, credentialsManager)
	log.Printf("[STARTUP] Handler initialization took %v", time.Since(handlerStart))

	// Setup routes
	mux := http.NewServeMux()

	// Public routes (no auth required)
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			authHandler.HandleLogin(w, r)
		} else {
			authHandler.ServeLogin(w, r)
		}
	})
	mux.HandleFunc("/logout", authHandler.HandleLogout)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})
	mux.HandleFunc("/static/", handler.ServeStatic) // Static files don't need auth

	// Student routes (public login, protected dashboard)
	mux.HandleFunc("/student/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			authHandler.HandleStudentLogin(w, r)
		} else {
			authHandler.ServeStudentLogin(w, r)
		}
	})
	mux.HandleFunc("/student/logout", authHandler.HandleStudentLogout)
	mux.HandleFunc("/student/dashboard", authHandler.RequireStudentAuth(handler.ServeStudentDashboard))
	mux.HandleFunc("/api/student/labs", authHandler.RequireStudentAuth(handler.ListLabs))
	mux.HandleFunc("/api/student/workspace/request", authHandler.RequireStudentAuth(handler.RequestWorkspace))

	// Public homepage (no auth required)
	mux.HandleFunc("/", handler.ServeUI)

	// Protected routes (auth required)
	mux.HandleFunc("/admin", authHandler.RequireAuth(handler.ServeAdminUI))
	mux.HandleFunc("/jobs", authHandler.RequireAuth(handler.ServeJobsList))
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
	mux.HandleFunc("/api/labs", authHandler.RequireAuth(handler.CreateLab))
	mux.HandleFunc("/api/labs/dry-run", authHandler.RequireAuth(handler.DryRunLab))
	mux.HandleFunc("/api/labs/launch", authHandler.RequireAuth(handler.LaunchLab))
	mux.HandleFunc("/api/labs/recreate", authHandler.RequireAuth(handler.RecreateLab))
	mux.HandleFunc("/api/stacks/destroy", authHandler.RequireAuth(handler.DestroyStack))
	mux.HandleFunc("/api/jobs/", authHandler.RequireAuth(func(w http.ResponseWriter, r *http.Request) {
		// Check if this is a kubeconfig download request
		if strings.HasSuffix(r.URL.Path, "/kubeconfig") {
			handler.DownloadKubeconfig(w, r)
			return
		}
		if r.URL.Query().Get("format") == "json" {
			handler.GetJobStatusJSON(w, r)
		} else {
			handler.GetJobStatus(w, r)
		}
	}))

	// Configure server with timeouts
	addr := fmt.Sprintf(":%s", *port)
	srv := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

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

	// Wait for interrupt signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("Shutting down server...")

	// Graceful shutdown with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("Server forced to shutdown: %v", err)
	}

	log.Println("Server exited")
}
