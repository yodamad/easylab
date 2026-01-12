# Build stage
FROM golang:1.25-alpine AS builder

# Set working directory
WORKDIR /app

# Install git (needed for go mod download)
RUN apk add --no-cache git

# Copy go mod and sum files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o main ./cmd/server

# Runtime stage
FROM alpine:latest

# Install ca-certificates for HTTPS requests
RUN apk --no-cache add ca-certificates

# Create app user with UID 1000 to match Kubernetes securityContext
RUN addgroup -g 1000 appgroup && adduser -u 1000 -G appgroup -S appuser

WORKDIR /app

# Copy the binary from builder stage
COPY --from=builder /app/main .

# Copy web static files
COPY --from=builder /app/web ./web

# Create directories for data persistence
RUN mkdir -p /app/data /app/jobs

# Change ownership of all files to appuser (including web directory)
RUN chown -R appuser:appgroup /app

# Ensure web directory has proper read permissions
RUN chmod -R 755 /app/web

# Switch to non-root user
USER appuser

# Expose port
EXPOSE 8080

# Set default environment variables
ENV PORT=8080
ENV WORK_DIR=/app/jobs
ENV DATA_DIR=/app/data

# Health check
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget --no-verbose --tries=1 --spider http://localhost:8080/health || exit 1

# Run the application
CMD ["./main", "--port", "8080", "--work-dir", "/app/jobs", "--data-dir", "/app/data"]
