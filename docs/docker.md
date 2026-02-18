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

2. **Set the admin password (optional but recommended):**
   ```bash
   export LAB_ADMIN_PASSWORD="your-secure-password"
   ```

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

- `LAB_ADMIN_PASSWORD`: Admin password for the web interface (default: "admin123")
- `PORT`: Port to run the application on (default: 8080)
- `WORK_DIR`: Directory for job workspaces (default: /app/jobs)
- `DATA_DIR`: Directory for persisting job data (default: /app/data)

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
  easylab
```

## Data Management

### Backup volumes:
```bash
# Create backup of job data
docker run --rm -v lab_data:/data -v $(pwd):/backup alpine tar czf /backup/lab_data_backup.tar.gz -C /data .

# Create backup of job workspaces
docker run --rm -v lab_jobs:/jobs -v $(pwd):/backup alpine tar czf /backup/lab_jobs_backup.tar.gz -C /jobs .
```

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
docker rmi easylab
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
