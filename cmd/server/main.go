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
	)
	flag.Parse()

	// Create work directory if it doesn't exist
	if err := os.MkdirAll(*workDir, 0755); err != nil {
		log.Fatalf("Failed to create work directory: %v", err)
	}

	// Initialize components
	jobManager := server.NewJobManager()
	pulumiExec := server.NewPulumiExecutor(jobManager, *workDir)
	handler := server.NewHandler(jobManager, pulumiExec)
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

	// Protected routes (auth required)
	mux.HandleFunc("/", authHandler.RequireAuth(handler.ServeUI))
	mux.HandleFunc("/api/labs", authHandler.RequireAuth(handler.CreateLab))
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
