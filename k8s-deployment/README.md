# EasyLab Kubernetes Deployment

This directory contains Kubernetes manifests for deploying the EasyLab application with persistent volume support.

## Files Overview

- **`namespace.yaml`**: Creates the `easylab` namespace
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
- **Jobs Storage**: `/app/jobs` mounted to `easylab-jobs-pvc` (10Gi)
- **Data Storage**: `/app/data` mounted to `easylab-data-pvc` (5Gi)
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
docker build -t your-registry/easylab:latest ../
docker push your-registry/easylab:latest

# Deploy
./deploy.sh

# Update image if needed
kubectl set image deployment/easylab easylab=your-registry/easylab:latest -n easylab
```

## Configuration

### Required Updates Before Production

1. **Update secrets** with real OVH credentials:
   ```bash
   kubectl edit secret easylab-secrets -n easylab
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
kubectl get all -n easylab

# View logs
kubectl logs -f deployment/easylab -n easylab

# Check PVC status
kubectl get pvc -n easylab

# Debug pods
kubectl describe pod -n easylab
```
