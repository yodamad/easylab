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
	"syscall"
	"time"
)

func main() {
	var (
		port    = flag.String("port", "8080", "Port to listen on")
		workDir = flag.String("work-dir", "/tmp/lab-as-code-jobs", "Directory for job workspaces")
		dataDir = flag.String("data-dir", "/tmp/lab-as-code-data", "Directory for persisting job data")
	)
	flag.Parse()

	// Create work directory if it doesn't exist
	if err := os.MkdirAll(*workDir, 0755); err != nil {
		log.Fatalf("Failed to create work directory: %v", err)
	}

	// Create data directory if it doesn't exist
	if err := os.MkdirAll(*dataDir, 0755); err != nil {
		log.Fatalf("Failed to create data directory: %v", err)
	}

	// Initialize components
	jobManager := server.NewJobManager(*dataDir)
	pulumiExec := server.NewPulumiExecutor(jobManager, *workDir)
	credentialsManager := server.NewCredentialsManager()
	handler := server.NewHandler(jobManager, pulumiExec, credentialsManager)
	authHandler := server.NewAuthHandler()

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
		log.Printf("Starting server on http://localhost%s", addr)
		log.Printf("Work directory: %s", *workDir)
		log.Printf("Data directory: %s", *dataDir)
		log.Printf("Set %s environment variable to configure admin password", server.EnvAdminPassword)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server failed: %v", err)
		}
	}()

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
