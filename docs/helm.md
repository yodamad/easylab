---
icon: lucide/package
title: Helm
---

# Helm Chart Deployment

EasyLab is available as a Helm chart published on Docker Hub as an OCI artifact.

## Prerequisites

- Kubernetes cluster (v1.24+)
- Helm 3.8+ (OCI support required)

## Image CPU architecture (`exec format error`)

If the pod exits immediately with `exec /app/main: exec format error`, the image’s architecture does not match your nodes (for example an **arm64** image on **amd64** workers). That often happens when the image is built on Apple Silicon with plain `docker build` and no platform flag.

**Fix:** build and push with an explicit platform that matches your cluster (most cloud clusters are `linux/amd64`):

```bash
docker buildx build --platform linux/amd64 -t your-registry/easylab:your-tag --push .
```

For **arm64** nodes (for example AWS Graviton), use `--platform linux/arm64` instead. CI images built on typical `linux/amd64` GitLab runners already match common clusters.

## Install

```bash
helm install easylab oci://registry-1.docker.io/yodamad/easylab-helm \
  --version __VERSION__ \
  --set secrets.adminPassword="your-secure-password"
```

Versions follow [SemVer](https://semver.org/) without the `v` prefix.

## Available versions

Check available versions on [Docker Hub](https://hub.docker.com/r/yodamad/easylab-helm/tags?name=&ordering=-name&page_size=25) or with:

```bash
helm show chart oci://registry-1.docker.io/yodamad/easylab-helm --version __VERSION__
```

## Configuration

All configuration is done through `values.yaml` overrides. You can either pass `--set` flags or provide a custom values file:

```bash
helm install easylab oci://registry-1.docker.io/yodamad/easylab-helm \
  --version __VERSION__ \
  -f my-values.yaml
```

### Key values

| Parameter | Description | Default |
|-----------|-------------|---------|
| `namespace.create` | Create a dedicated namespace | `true` |
| `namespace.name` | Namespace name | `easylab` |
| `image.repository` | Docker image repository | `docker.io/yodamad/easylab` |
| `image.tag` | Docker image tag (defaults to `v` + chart appVersion when appVersion has no leading `v`, to match Git-tag Docker images) | `""` |
| `image.pullPolicy` | Image pull policy | `Always` |
| `replicaCount` | Number of replicas | `1` |
| `config.port` | Application port | `"8080"` |
| `config.workDir` | Job workspace directory | `"/app/jobs"` |
| `config.dataDir` | Data persistence directory | `"/app/data"` |
| `secrets.create` | Create a Kubernetes secret | `true` |
| `secrets.adminPassword` | Admin login password | `""` |
| `secrets.studentPassword` | Student login password | `""` |
| `secrets.ovh.applicationKey` | OVH application key | `""` |
| `secrets.ovh.applicationSecret` | OVH application secret | `""` |
| `secrets.ovh.consumerKey` | OVH consumer key | `""` |
| `secrets.ovh.serviceName` | OVH service name | `""` |
| `secrets.ovh.endpoint` | OVH API endpoint | `"ovh-eu"` |
| `persistence.jobs.size` | PVC size for jobs storage | `1Gi` |
| `persistence.jobs.storageClass` | Storage class for jobs PVC | `""` |
| `persistence.data.size` | PVC size for data storage | `200Mi` |
| `persistence.data.storageClass` | Storage class for data PVC | `""` |
| `service.type` | Kubernetes service type | `ClusterIP` |
| `service.port` | Service port | `80` |
| `service.annotations` | Service annotations | `{}` |
| `ingress.enabled` | Enable ingress | `false` |
| `ingress.className` | Ingress class name | `traefik` |
| `ingress.annotations` | Ingress annotations | `{}` |
| `ingress.host` | Ingress hostname | `easylab.example.com` |
| `ingress.tls.enabled` | Enable TLS | `false` |
| `ingress.tls.secretName` | TLS secret name | `easylab-tls` |
| `resources.requests.memory` | Memory request | `1024Mi` |
| `resources.requests.cpu` | CPU request | `500m` |
| `resources.limits.memory` | Memory limit | `4096Mi` |
| `resources.limits.cpu` | CPU limit | `3000m` |

## Examples

### Minimal install

```bash
helm install easylab oci://registry-1.docker.io/yodamad/easylab-helm \
  --version __VERSION__ \
  --set secrets.adminPassword="SuperAdmin"
```

### With ingress and TLS

```bash
helm install easylab oci://registry-1.docker.io/yodamad/easylab-helm \
  --version __VERSION__ \
  --set secrets.adminPassword="SuperAdmin" \
  --set ingress.enabled=true \
  --set ingress.host="easylab.example.com" \
  --set ingress.tls.enabled=true \
  --set ingress.className=nginx \
  --set ingress.annotations."cert-manager\.io/cluster-issuer"=letsencrypt
```

### With OVH credentials

```bash
helm install easylab oci://registry-1.docker.io/yodamad/easylab-helm \
  --version __VERSION__ \
  --set secrets.adminPassword="SuperAdmin" \
  --set secrets.ovh.applicationKey="your-key" \
  --set secrets.ovh.applicationSecret="your-secret" \
  --set secrets.ovh.consumerKey="your-consumer-key" \
  --set secrets.ovh.serviceName="your-service-name"
```

### Using a custom values file

Create a `my-values.yaml`:

```yaml
namespace:
  name: my-lab

secrets:
  adminPassword: "SuperAdmin"
  studentPassword: "StudentPass"
  ovh:
    applicationKey: "your-key"
    applicationSecret: "your-secret"
    consumerKey: "your-consumer-key"
    serviceName: "your-service-name"

ingress:
  enabled: true
  host: easylab.mycompany.com
  tls:
    enabled: true

persistence:
  jobs:
    size: 5Gi
    storageClass: longhorn
  data:
    size: 1Gi
    storageClass: longhorn
```

Then install:

```bash
helm install easylab oci://registry-1.docker.io/yodamad/easylab-helm \
  --version __VERSION__ \
  -f my-values.yaml
```

## Upgrade

```bash
helm upgrade easylab oci://registry-1.docker.io/yodamad/easylab-helm \
  --version __VERSION__ \
  -f my-values.yaml
```

## Uninstall

```bash
helm uninstall easylab
```

!!! warning "PersistentVolumeClaims are not deleted by `helm uninstall`"
    To fully clean up, delete the PVCs manually:
    ```bash
    kubectl delete pvc -n easylab -l app.kubernetes.io/name=easylab
    ```

## Generate raw Kubernetes manifests

If you prefer deploying with plain `kubectl` instead of Helm, you can use `helm template` to render the chart into standard Kubernetes YAML manifests.

### Render to stdout

```bash
helm template easylab oci://registry-1.docker.io/yodamad/easylab-helm \
  --version __VERSION__ \
  --set secrets.adminPassword="SuperAdmin" \
  > easylab-manifests.yaml
```

### Render with custom values

```bash
helm template easylab oci://registry-1.docker.io/yodamad/easylab-helm \
  --version __VERSION__ \
  -f my-values.yaml \
  > easylab-manifests.yaml
```

### Apply with kubectl

```bash
kubectl apply -f easylab-manifests.yaml
```

!!! tip "All Helm values work with `helm template`"
    The same `--set` flags and `-f values.yaml` files used with `helm install` work identically with `helm template`. The only difference is that the output goes to a file instead of being applied to the cluster.
