# Build stage
FROM pulumi/pulumi-go:latest AS builder

# Set working directory
WORKDIR /app

# Install git (needed for go mod download)
RUN apt-get update && apt-get install -y --no-install-recommends git && rm -rf /var/lib/apt/lists/*

# Copy go mod and sum files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o main ./cmd/server

# Runtime stage
FROM pulumi/pulumi-go:latest

# Install ca-certificates and wget for HTTPS requests and health checks
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates wget && rm -rf /var/lib/apt/lists/*

# Create app user with UID 1000 to match Kubernetes securityContext
RUN groupadd -g 1000 appgroup && useradd -u 1000 -g appgroup -m -s /bin/bash appuser

WORKDIR /app

# Copy the binary from builder stage
COPY --from=builder /app/main .

# Copy web static files
COPY --from=builder /app/web ./web

# Copy templates directory
COPY --from=builder /app/templates ./templates

# Create directories for data persistence and Go cache (including sumdb)
RUN mkdir -p /app/data /app/jobs /app/.go/pkg/mod /app/.go/pkg/sumdb /app/.go/cache

# Install required Pulumi plugins before switching to non-root user
# Set PULUMI_HOME to a writable location
ENV PULUMI_HOME=/app/.pulumi
RUN mkdir -p /app/.pulumi/plugins && \
    pulumi plugin install resource kubernetes v4.24.1 && \
    pulumi plugin install resource command v1.1.3 && \
    pulumi plugin install resource ovh v2.10.0 --server github://api.github.com/ovh/pulumi-ovh && \
    chown -R appuser:appgroup /app/.pulumi && \
    pulumi plugin ls --json | grep -q "ovh" || (echo "ERROR: pulumi-resource-ovh plugin not found after installation" && exit 1)

# Change ownership of all files to appuser (including web directory and Go cache)
RUN chown -R appuser:appgroup /app

# Ensure web directory has proper read permissions
RUN chmod -R 755 /app/web

# Ensure templates directory has proper read permissions
RUN chmod -R 755 /app/templates

# Switch to non-root user
USER appuser

# Expose port
EXPOSE 8080

# Set default environment variables
ENV PORT=8080
ENV WORK_DIR=/app/jobs
ENV DATA_DIR=/app/data
ENV TEMPLATES_DIR=/app/templates
ENV PULUMI_BACKEND_URL=file://
ENV PULUMI_SKIP_UPDATE_CHECK=true
ENV PULUMI_CONFIG_PASSPHRASE=passphrase
# Set Pulumi home directory for plugins
ENV PULUMI_HOME=/app/.pulumi
# Set Go cache directories to writable locations
ENV GOMODCACHE=/app/.go/pkg/mod
ENV GOCACHE=/app/.go/cache
# Set GOPATH to ensure all Go directories are under /app/.go
ENV GOPATH=/app/.go
# Ensure sumdb uses writable location (relative to GOPATH/pkg)
ENV GOSUMDB=sum.golang.org

# Health check
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget --no-verbose --tries=1 --spider http://localhost:8080/health || exit 1

# Run the application
CMD ["./main", "--port", "8080", "--work-dir", "/app/jobs", "--data-dir", "/app/data"]
