---
icon: lucide/container
title: Docker
---

# Docker Deployment Guide

This guide explains how to deploy the EasyLab application using Docker and Docker Compose.

## Prerequisites

- Docker and Docker Compose installed on your system
- At least 2GB of available RAM for the container

## Quick Start

1. **Download docker-compose.yml:**
   ```bash
   curl -fsSL https://raw.githubusercontent.com/yodamad/easylab/main/docker-compose.yml -o docker-compose.yml
   ```
   Or from GitLab:
   ```bash
   curl -fsSL https://gitlab.com/yodamad-workshops/easylab/-/raw/main/docker-compose.yml -o docker-compose.yml
   ```

2. **Set the admin password (required) and the data encryption key:**
   ```bash
   export LAB_ADMIN_PASSWORD="your-secure-password"
   export LAB_DATA_ENCRYPTION_KEY="$(openssl rand -base64 32)"
   # Optional: enables password sign-in for students
   export LAB_STUDENT_PASSWORD="your-student-password"
   ```
   Neither the admin password nor the encryption key has a default: compose refuses
   to start without the first, and the server exits without the second. Keep the key
   somewhere you can retrieve it — see [`LAB_DATA_ENCRYPTION_KEY`](#environment-variables).

3. **Start the application:**
   ```bash
   docker-compose up -d
   ```

4. **Access the application:**
   - Main application: http://localhost:8080
   - Health check: http://localhost:8080/health

5. **View logs:**
   ```bash
   docker-compose logs -f easylab
   ```

## Configuration

### Environment Variables

The application can be configured using the following environment variables:

- `LAB_ADMIN_PASSWORD` **(required)**: Admin password for the web interface. There is no default — the server refuses to start without it, and `docker-compose.yml` refuses to start the container.
- `LAB_STUDENT_PASSWORD` (optional): the student password for **this instance's own** student area. No default; leaving it unset disables student password login here, which is what you want when students sign in with Microsoft / GitHub / GitLab instead. It is the default for the **Student password** field on the Student portals admin page, which overrides it. It is *not* every lab's password: a lab running [its own student portal](student-portal.md) has a separate password of its own, generated per portal, so one handed out opens one lab.
- `TRUST_FORWARDED_PROTO`: Whether the `X-Forwarded-Proto` header from an incoming request is trusted to mark the session cookie `Secure` (default: `true`). Leave this at its default when the app sits behind a reverse proxy that sets/overwrites this header. Set to `false` if the app is ever reachable in a way where a client-supplied `X-Forwarded-Proto` header would reach the app unstripped — otherwise a client could suppress the cookie's `Secure` flag.
- `EASYLAB_MODE`: Which areas the instance serves — `all` (default: admin space and student space) or `admin` (admin space only, for when every lab has its own [in-lab student portal](student-portal.md)). The `--mode` flag takes precedence.
- `EASYLAB_PUBLIC_URL`: This instance's address as students reach it, e.g. `https://easylab.example.com`. In-lab student portals send students here for Microsoft / GitHub / GitLab sign-in; without it they offer password sign-in only. This is only the default: it can also be set, and overridden, from the admin's [Student portals](student-portal.md#the-student-portals-page) page.
- `EASYLAB_PORTAL_IMAGE`: Image the in-lab student portals run (default: `docker.io/yodamad/easylab:v<this version>`). A build without a version, such as `make dev`, has no default. Also settable from the Student portals page, which wins over this variable.
- `EASYLAB_SERVER_NODE_SELECTOR`: Node labels this server itself runs on, as comma-separated `key=value` pairs, e.g. `pool=control-plane`. Only useful when EasyLab runs inside a lab's own Kubernetes cluster, where it keeps each lab's [image pre-pull](admin-lab-management.md#image-pre-pull) off those nodes — no workspace lands there, so there is nothing for them to cache. The [Helm chart](helm.md#pinning-to-a-dedicated-node-pool) fills it in from its `nodeSelector` value, so a Helm deployment never sets it by hand. Unset means every node pre-pulls.
- `PORT`: Port to run the application on (default: 8080)
- `WORK_DIR`: Directory for job workspaces (default: /app/jobs)
- `DATA_DIR`: Directory for persisting job data (default: /app/data)
- `CLEANUP_INTERVAL_MINUTES`: How often (in minutes) the cleanup service checks for expired workspaces and scheduled lab deletions (default: 5)
- `IDLE_CHECK_INTERVAL_MINUTES`: How often (in minutes) idle workspaces are looked at for [automatic pausing](admin-lab-creation.md#pause-idle-workspaces) (default: 1). Deliberately shorter than `CLEANUP_INTERVAL_MINUTES`: a workspace must read as idle on two consecutive checks before it is paused, so this interval is what an admin waits beyond the timeout they configured. Each check is one small HTTP probe per running workspace, so raise it if a very large lab makes that traffic noticeable.
- `WORKSPACE_CREATE_CONCURRENCY`: Maximum number of workspace create/delete requests processed at the same time, across all labs (default: 20). Each request makes several sequential calls to the lab's Kubernetes API server, so this bounds how hard a burst of students starting workspaces together — e.g. at the start of a large workshop — or an admin bulk-deleting many workspaces can hit that cluster at once; extra requests wait briefly rather than firing all together. Raise it for a bigger/faster cluster, lower it for a smaller one.
- `PULUMI_EXECUTION_CONCURRENCY`: Maximum number of Pulumi executions (preview/up/destroy) running at the same time, across all labs (default: 5). Each execution spawns a full Pulumi engine process plus its resource provider plugins, so this bounds how much host CPU/memory a burst of concurrent lab creates, destroys, retries, or recreations can consume at once; extra executions wait briefly rather than all starting together. Raise it on a bigger host, lower it on a smaller one.
- `CLEANUP_DELETE_CONCURRENCY`: Maximum number of workspace deletions run at once per job during a single automatic cleanup pass (default: 5). Separate from `WORKSPACE_CREATE_CONCURRENCY` since cleanup runs on a background schedule rather than in response to a request.
- `LAB_DATA_ENCRYPTION_KEY` **(required when `DATA_DIR` is set)**: base64-encoded 32-byte key (AES-256) used to encrypt secrets at rest in persisted job files — cluster kubeconfigs and DNS provider credentials. Generate one with `openssl rand -base64 32`, supply it from a managed secret store, and keep it stable: a different key makes previously-encrypted kubeconfigs unreadable, so you must destroy/recreate affected labs after a change. The server **refuses to start** when a data directory is configured and this variable is unset — it will not generate a key for you, because a key stored beside the data it protects provides no protection at all. Setting it also lets EasyLab keep cloud provider credentials and DNS profiles across restarts with nothing to unlock — see [Credential storage](admin.md#credential-storage).

    !!! warning "Upgrading from a version that generated its own key"
        Earlier versions wrote a key to `<DATA_DIR>/.encryption_key` when this variable was unset. That file is the only thing that can decrypt the job files already in that directory, so before upgrading, set `LAB_DATA_ENCRYPTION_KEY` to its contents:
        ```bash
        export LAB_DATA_ENCRYPTION_KEY="$(cat <DATA_DIR>/.encryption_key)"
        ```
        Start the server, confirm your labs are listed, then delete the file. If you skip this the server will stop with a message naming the file rather than starting with unreadable labs.
- `PULUMI_CONFIG_PASSPHRASE` (recommended): passphrase Pulumi uses to encrypt secrets in local stack state. If unset it defaults to the insecure literal `passphrase`; set a strong value. The server logs a `[SECURITY]` warning at startup while the default is in use (existing stacks stay decryptable).

!!! note "Provider credentials are not written to lab state"
    OVHcloud and Azure API credentials are **never** persisted in job files. When a lab is destroyed, recreated, or retried, they are re-read from the credential store, so they must be available at that time: provided through the `OVH_*` / `AZURE_*` environment variables below, or saved from the admin UI with [credential storage](admin.md#credential-storage) set up. Without either, credentials entered in the UI live in memory only and are lost on restart.

**Azure AD student login** (optional — all three required to enable):

- `AZURE_AD_CLIENT_ID`: Application (client) ID of the Azure app registration
- `AZURE_AD_CLIENT_SECRET`: Client secret of the Azure app registration
- `AZURE_AD_TENANT_ID`: Directory (tenant) ID

See [Admin guide — Azure AD student authentication](admin.md#azure-ad-student-authentication-optional) for the full setup steps.

You can also set OVHcloud credentials via environment variables (`OVH_APPLICATION_KEY`, `OVH_APPLICATION_SECRET`, `OVH_CONSUMER_KEY`, `OVH_SERVICE_NAME`, `OVH_ENDPOINT`); see [OVHcloud configuration](ovhcloud.md).

### Environment file {#environment-file}

When running the EasyLab server binary directly (not only in Docker), you can load environment variables from a file at startup:

```bash
./easylab-server -env-file=./env.sh
```

The file format is standard `.env` style: one `KEY=VALUE` per line. Lines starting with `#` are comments; lines starting with `export ` are supported for compatibility with shell scripts. This allows you to keep secrets out of the command line and reuse a single file for local development.

A local `env.sh` needs at least an admin password and a data encryption key, since a data directory is always configured (`/tmp/easylab-data` by default) — `make dev` stops at startup without them:

```bash
export LAB_ADMIN_PASSWORD=dev-password
export LAB_DATA_ENCRYPTION_KEY=$(openssl rand -base64 32)
```

Keep that key in the file rather than regenerating it each time, or every restart leaves the previous run's labs unreadable. The repo's own `env.test.sh`, used by the Playwright suite, carries a fixed throwaway key for the same reason.

**Server flags** (when running the binary): `-port` (default: 8081), `-work-dir`, `-data-dir`, `-env-file`. Environment variables `WORK_DIR` and `DATA_DIR` override the defaults if set.

### Data Persistence

The Docker Compose setup includes two named volumes for data persistence:

- `lab_jobs`: Stores job workspaces and temporary files
- `lab_data`: Stores application data, job metadata, and configurations

**Important:** These volumes ensure that your lab deployments and application data survive container restarts and updates.

## Docker Commands

### Build the image manually:
```bash
docker build -t easylab .
```

**Multi-architecture (amd64 + arm64):** CI publishes each tag as `linux/amd64` and `linux/arm64` so the same tag works on typical cloud nodes and on arm64 (for example AWS Graviton). The GitLab pipeline builds both architectures with GoReleaser and passes `--build-arg SKIP_BUILD=true` so each image layer copies the matching prebuilt binary from `docker-prebuild/linux_${TARGETARCH}/main`.

**Local multi-arch with Buildx** (compile inside Docker per platform; default `SKIP_BUILD=false`):

```bash
docker buildx build --platform linux/amd64,linux/arm64 --build-arg SKIP_BUILD=false -t your-registry/easylab:tag --push .
```

**Local multi-arch reusing prebuilt binaries** (same layout as CI: run `goreleaser build --clean --snapshot -f .goreleaser.gitlab.yaml`, then copy each `dist/.../main` into `docker-prebuild/linux_amd64/main` and `docker-prebuild/linux_arm64/main` as in `.gitlab-ci.yml`, or use your own paths and adjust the Dockerfile):

```bash
docker buildx build --platform linux/amd64,linux/arm64 --build-arg SKIP_BUILD=true -t your-registry/easylab:tag --push .
```

For a **single** platform into the local Docker daemon, use one platform and `--load`, for example `--platform linux/amd64`.

### Run with Docker Compose:
```bash
# Start in detached mode
docker-compose up -d

# Start and view logs
docker-compose up

# Stop the application
docker-compose down

# Rebuild and restart
docker-compose up -d --build
```

### Run with Docker directly:
```bash
# Create volumes first
docker volume create lab_jobs
docker volume create lab_data

# Run the container
docker run -d \
  --name easylab \
  -p 8080:8080 \
  -v lab_jobs:/app/jobs \
  -v lab_data:/app/data \
  -e LAB_ADMIN_PASSWORD="your-password" \
  -e LAB_STUDENT_PASSWORD="your-student-password" \
  yodamad/easylab:v0.2.0
```

## Data Management

### Inspect volumes:
```bash
# List volumes
docker volume ls | grep lab

# Inspect volume details
docker volume inspect lab_data
```

### Clean up:
```bash
# Stop and remove containers
docker-compose down

# Remove volumes (WARNING: This deletes all data!)
docker volume rm lab_jobs lab_data

# Remove images
docker rmi yodamad/easylab:v0.2.0
```

## Health Monitoring

The application includes built-in health checks:

- Health endpoint: `GET /health`
- Docker health check runs every 30 seconds
- Container will restart automatically if unhealthy

Monitor the health status:
```bash
docker ps
docker-compose ps
```

## Security Considerations

1. **Change the default admin password** before deploying to production
2. **Use environment variables** for sensitive configuration
3. **Limit container privileges** (the container runs as non-root user)
4. **Use HTTPS in production** with a reverse proxy
5. **Regularly update** the Docker images

## Troubleshooting

### Common Issues:

1. **Port already in use:**
   ```bash
   # Change the port in docker-compose.yml
   ports:
     - "8081:8080"
   ```

2. **Permission denied:**
   - Ensure Docker daemon is running
   - Check that you have Docker permissions

3. **Data not persisting:**
   - Verify volumes are created: `docker volume ls`
   - Check volume mounts in `docker-compose ps`

4. **Application not starting:**
   ```bash
   # Check logs
   docker-compose logs easylab

   # Check health
   curl http://localhost:8080/health
   ```

### Logs and Debugging:

```bash
# View application logs
docker-compose logs -f easylab

# Enter container for debugging
docker-compose exec easylab sh

# Check running processes
docker-compose exec easylab ps aux
```

## Production Deployment

For production deployment, consider:

1. **Use a reverse proxy** (nginx, traefik) with SSL termination
2. **Set up monitoring** (Prometheus, Grafana)
3. **Configure log aggregation** (ELK stack, Loki)
4. **Implement backup strategies** for the data volumes
5. **Use Docker secrets** for sensitive configuration
6. **Set resource limits** in docker-compose.yml

Example production docker-compose.yml additions:
```yaml
services:
  easylab:
    deploy:
      resources:
        limits:
          memory: 1G
        reservations:
          memory: 512M
    restart: always
```

## Support

If you encounter issues:

1. Check the application logs: `docker-compose logs easylab`
2. Verify Docker and Docker Compose versions
3. Ensure all prerequisites are met
4. Check the main README.md for application-specific configuration
