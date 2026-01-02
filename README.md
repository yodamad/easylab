# Lab-as-Code

A web application for managing cloud infrastructure labs with OVHcloud integration. Provides an admin interface for creating and managing infrastructure labs, and a student interface for requesting and accessing development workspaces.

## Overview

Lab-as-Code is a comprehensive platform that simplifies cloud infrastructure lab management. It enables:

- **Admin Interface**: Create and manage cloud infrastructure labs with OVHcloud
- **Student Interface**: Request and access development workspaces
- **Infrastructure as Code**: Automated provisioning using Pulumi and OVHcloud
- **Persistent Storage**: Job data and workspace persistence across deployments
- **Multi-deployment Options**: Run locally, with Docker, or on Kubernetes

## Architecture

The application consists of two main components:

### 1. Web Application (`cmd/server/`)
- **Go-based HTTP server** serving web interfaces and API endpoints
- **Admin interface** for infrastructure lab management
- **Student interface** for workspace access
- **Pulumi integration** for infrastructure provisioning
- **Persistent job storage** for tracking deployments

### 2. Infrastructure Provisioning (`main.go`)
- **Pulumi project** for OVHcloud infrastructure provisioning
- **Kubernetes cluster creation** with managed node pools
- **Network configuration** with private networks and gateways
- **Coder integration** for development workspaces

## Quick Start

### Local Development
```bash
# Install dependencies
go mod tidy

# Run the web application
go run cmd/server/main.go

# Access at http://localhost:8080
```

### Docker Deployment
```bash
# Set admin password
export LAB_ADMIN_PASSWORD="your-secure-password"

# Start with Docker Compose
docker-compose up -d

# Access at http://localhost:8080
```

### Kubernetes Deployment
```bash
# Build and push image
docker build -t lab-as-code:latest .
docker push your-registry/lab-as-code:latest

# Deploy to Kubernetes
cd k8s-deployment
./deploy.sh

# Configure OVH credentials
kubectl edit secret lab-as-code-secrets -n lab-as-code
```

## Configuration

### Environment Variables

#### Core Application Settings
- `PORT`: HTTP server port (default: `8080`)
- `WORK_DIR`: Directory for job workspaces (default: `/tmp/lab-as-code-jobs`)
- `DATA_DIR`: Directory for application data (default: `/tmp/lab-as-code-data`)

#### Authentication
- `LAB_ADMIN_PASSWORD`: Password for admin interface (default: `admin123`)
- `LAB_STUDENT_PASSWORD`: Password for student interface (default: `student123`)

#### OVHcloud Integration
- `OVH_ENDPOINT`: OVHcloud API endpoint (default: `ovh-eu`)
- `OVH_APPLICATION_KEY`: OVHcloud application key
- `OVH_APPLICATION_SECRET`: OVHcloud application secret
- `OVH_CONSUMER_KEY`: OVHcloud consumer key
- `OVH_SERVICE_NAME`: OVHcloud project/service name

### Configuration Files

- `Pulumi.yaml`: Pulumi project configuration
- `Pulumi.dev.yaml`: Development stack configuration
- `docker-compose.yml`: Docker Compose configuration
- `k8s-deployment/`: Kubernetes manifests and configuration

## Application Features

### Admin Interface
- **Lab Creation**: Design and deploy infrastructure labs
- **OVHcloud Integration**: Direct integration with OVHcloud APIs
- **Job Management**: Monitor deployment status and logs
- **Kubeconfig Access**: Download cluster configurations

### Student Interface
- **Workspace Requests**: Request access to development environments
- **Lab Catalog**: Browse available infrastructure labs
- **Session Management**: Secure access to provisioned resources

### Infrastructure Provisioning
- **Kubernetes Clusters**: Automated K8s cluster creation
- **Network Setup**: Private networks and gateways
- **Node Pools**: Configurable worker node pools
- **Coder Integration**: Development workspace provisioning

## Deployment Options

### 1. Local Development

#### Prerequisites
- Go 1.24+ installed
- OVHcloud account (optional, for infrastructure provisioning)

#### Setup
```bash
# Clone repository
git clone <repository-url>
cd lab-as-code

# Install dependencies
go mod tidy

# Set environment variables (optional)
export LAB_ADMIN_PASSWORD="your-password"
export OVH_APPLICATION_KEY="your-key"
# ... other OVH credentials

# Run the application
go run cmd/server/main.go
```

#### Access
- Application: http://localhost:8080
- Admin Interface: http://localhost:8080/admin (requires admin password)
- Student Interface: http://localhost:8080/student/login
- Health Check: http://localhost:8080/health

### 2. Docker Deployment

#### Prerequisites
- Docker and Docker Compose installed
- 2GB+ available RAM

#### Quick Start
```bash
# Set required passwords
export LAB_ADMIN_PASSWORD="your-secure-password"
export LAB_STUDENT_PASSWORD="your-student-password"

# Optional: Set OVH credentials for infrastructure provisioning
export OVH_APPLICATION_KEY="your-key"
export OVH_APPLICATION_SECRET="your-secret"
export OVH_CONSUMER_KEY="your-consumer-key"
export OVH_SERVICE_NAME="your-service-name"

# Start the application
docker-compose up -d

# View logs
docker-compose logs -f lab-as-code
```

#### Configuration
The Docker setup includes:
- **Persistent volumes** for jobs (`lab_jobs`) and data (`lab_data`)
- **Health checks** with automatic container restart
- **Environment-based configuration** via docker-compose.yml

#### Data Persistence
- Job workspaces persist in the `lab_jobs` volume
- Application data (job metadata, configurations) persist in the `lab_data` volume

### 3. Kubernetes Deployment

#### Prerequisites
- Running Kubernetes cluster (can use infrastructure from this project)
- `kubectl` configured
- Docker registry access (for container images)

#### Quick Deployment
```bash
# Build and push container image
docker build -t lab-as-code:latest .
docker tag lab-as-code:latest your-registry/lab-as-code:latest
docker push your-registry/lab-as-code:latest

# Deploy to Kubernetes
cd k8s-deployment
./deploy.sh

# Configure secrets with OVH credentials
kubectl edit secret lab-as-code-secrets -n lab-as-code
```

#### Manual Deployment
```bash
cd k8s-deployment

# Apply manifests in order
kubectl apply -f namespace.yaml
kubectl apply -f pvc.yaml
kubectl apply -f configmap.yaml
kubectl apply -f secret.yaml
kubectl apply -f deployment.yaml
kubectl apply -f service.yaml
kubectl apply -f ingress.yaml
```

#### Configuration
- **Persistent Volumes**: 10Gi for jobs, 5Gi for data
- **Security Context**: Non-root user execution
- **Health Probes**: Readiness and liveness checks on `/health`
- **Resource Limits**: Configured CPU and memory limits

#### Access
- **Internal Service**: `http://lab-as-code-service.lab-as-code.svc.cluster.local`
- **External Access**: Configure ingress with your domain

## Infrastructure Provisioning

This repository includes a Pulumi project for provisioning OVHcloud infrastructure.

### Prerequisites
- [Pulumi CLI](https://www.pulumi.com/docs/get-started/install/)
- Go 1.24+
- OVHcloud account with API credentials

### Setup
```bash
# Install dependencies
go mod tidy

# Initialize Pulumi stack
pulumi stack init dev

# Configure OVHcloud credentials
pulumi config set ovhServiceName "your-project-id"
pulumi config set ovh:endpoint "ovh-eu"
pulumi config set ovh:applicationKey "your-app-key" --secret
pulumi config set ovh:applicationSecret "your-app-secret" --secret
pulumi config set ovh:consumerKey "your-consumer-key" --secret
```

### Infrastructure Configuration
- `location`: OVHcloud region (default: `GRA11`)
- `nodePoolCount`: Number of node pools (default: `1`)
- `vmSize`: VM flavor (default: `b2-7`)
- `gatewayModel`: Gateway size (default: `s`)
- `minNodes`/`maxNodes`/`desiredNodes`: Node pool scaling

### Deployment
```bash
# Preview infrastructure
pulumi preview

# Deploy infrastructure
pulumi up

# Destroy when done
pulumi destroy
```

### Outputs
- Kubernetes cluster details and kubeconfig
- Network and gateway information
- Node pool configurations

## Project Structure

```
├── cmd/server/           # Web application
│   ├── main.go          # Application entry point
│   └── ...
├── internal/server/      # Application logic
├── k8s-deployment/       # Kubernetes manifests
├── web/                  # Static web assets
├── utils/               # Shared utilities
├── coder/               # Coder integration
├── ovh/                 # OVHcloud integration
├── k8s/                 # Kubernetes utilities
├── Pulumi.yaml          # Infrastructure config
├── docker-compose.yml   # Docker config
├── Dockerfile          # Container build
└── README.md           # This file
```

## Development

### Building
```bash
# Build web application
go build -o lab-as-code cmd/server/main.go

# Build Docker image
docker build -t lab-as-code .

# Build infrastructure (Pulumi)
go build -o infrastructure main.go
```

### Testing
```bash
# Run tests
go test ./...

# Run with race detection
go test -race ./...

# E2E tests (requires Docker)
npm test  # From playwright-report directory
```

### Contributing
1. Fork the repository
2. Create a feature branch
3. Make your changes
4. Add tests if applicable
5. Submit a pull request

## Troubleshooting

### Application Issues

1. **Cannot access admin interface**: Check `LAB_ADMIN_PASSWORD` environment variable
2. **OVH credentials not working**: Verify API credentials and endpoint
3. **Jobs not persisting**: Check data directory permissions and disk space
4. **Port already in use**: Change `PORT` environment variable

### Docker Issues

1. **Container won't start**: Check logs with `docker-compose logs`
2. **Data not persisting**: Verify Docker volumes exist with `docker volume ls`
3. **Health check failing**: Ensure port 8080 is accessible internally

### Kubernetes Issues

1. **Pods not starting**: Check events with `kubectl describe pod`
2. **PVC pending**: Verify storage class availability
3. **Service not accessible**: Check service and ingress configuration

### Infrastructure Issues

1. **Pulumi authentication errors**: Verify OVHcloud API credentials
2. **Region not available**: Check supported regions for your OVHcloud account
3. **Quota exceeded**: Review OVHcloud project limits

## Security Considerations

- Change default passwords before production deployment
- Use environment variables for sensitive configuration
- Regularly update Docker images and dependencies
- Implement HTTPS in production environments
- Monitor and audit infrastructure access

## Support

- **Documentation**: This README and individual component docs
- **Issues**: GitHub Issues for bug reports and feature requests
- **OVHcloud Docs**: https://docs.ovh.com/
- **Pulumi Docs**: https://www.pulumi.com/docs/

## License

[Add license information here]

