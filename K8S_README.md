# Lab-as-Code Kubernetes Deployment

This directory contains Kubernetes manifests for deploying the Lab-as-Code application with persistent volume support.

## Files Overview

- **`namespace.yaml`**: Creates the `lab-as-code` namespace
- **`pvc.yaml`**: PersistentVolumeClaims for jobs (10Gi) and data (5Gi) storage
- **`configmap.yaml`**: Non-sensitive configuration (ports, directories)
- **`secret.yaml`**: Sensitive configuration (OVH credentials, passwords)
- **`deployment.yaml`**: Application deployment with persistent volume mounts
- **`service.yaml`**: ClusterIP service to expose the application
- **`ingress.yaml`**: Ingress for external access (requires ingress controller)
- **`kustomization.yaml`**: Kustomize configuration for easy deployment
- **`deploy.sh`**: Automated deployment script

## Key Features

### 🔄 Persistent Storage
- **Jobs Storage**: `/app/jobs` mounted to `lab-as-code-jobs-pvc` (10Gi)
- **Data Storage**: `/app/data` mounted to `lab-as-code-data-pvc` (5Gi)
- Data persists across container restarts and pod rescheduling

### 🔒 Security
- Non-root user execution (appuser:appuser)
- SecurityContext with fsGroup for proper file permissions
- Secrets for sensitive data management

### 🚀 Health Checks
- Readiness probe on `/health`
- Liveness probe on `/health`
- Resource limits and requests

### 🌐 Networking
- Service exposed on port 80 internally
- Ingress for external access (customize host)
- TLS support ready (uncomment cert-manager annotations)

## Quick Start

```bash
# Build and tag your image
docker build -t your-registry/lab-as-code:latest ../
docker push your-registry/lab-as-code:latest

# Deploy
./deploy.sh

# Update image if needed
kubectl set image deployment/lab-as-code lab-as-code=your-registry/lab-as-code:latest -n lab-as-code
```

## Configuration

### Required Updates Before Production

1. **Update secrets** with real OVH credentials:
   ```bash
   kubectl edit secret lab-as-code-secrets -n lab-as-code
   ```

2. **Update ingress host**:
   ```yaml
   spec:
     rules:
     - host: your-domain.com
   ```

3. **Update image reference** in deployment.yaml or kustomization.yaml

### Storage Configuration

The PVCs use the default storage class. For production, consider:
- Specific storage classes for performance/durability
- Storage class annotations for backup policies
- PVC size adjustments based on usage

### Scaling Considerations

Currently configured for single replica. For multi-replica:
1. Change PVC accessMode to `ReadWriteMany` (if supported)
2. Update deployment replicas
3. Consider session affinity if needed

## Troubleshooting

```bash
# Check status
kubectl get all -n lab-as-code

# View logs
kubectl logs -f deployment/lab-as-code -n lab-as-code

# Check PVC status
kubectl get pvc -n lab-as-code

# Debug pods
kubectl describe pod -n lab-as-code
```
