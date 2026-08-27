---
icon: lucide/package
title: Helm
---

# Helm Chart Deployment

EasyLab is available as a Helm chart published on Docker Hub as an OCI artifact.

## Prerequisites

- Kubernetes cluster (v1.24+)
- Helm 3.8+ (OCI support required)
- To reach the EasyLab UI, an **Ingress controller** matching `ingress.className` must already be running in the cluster if you set `ingress.enabled=true` (the recommended way to expose it). The chart defaults `ingress.className` to `traefik`, which is only preinstalled on some distributions (for example k3s) — most managed Kubernetes offerings, including OVHcloud Managed Kubernetes, ship with **no** ingress controller out of the box. Either install Traefik yourself beforehand, or set `traefik.enabled=true` to have this chart install it for you — see [Optional infrastructure components](#optional-infrastructure-components). If you use a different ingress controller (for example NGINX), install it separately and set `ingress.className` to match.
- If you terminate TLS with cert-manager annotations (rather than a pre-existing secret), **cert-manager** and a configured `ClusterIssuer` must already be installed, or set `cert-manager.enabled=true` to have the chart install cert-manager for you.

## App image platforms (multi-arch)

Tags of the **application** image published from this project’s CI (for example `docker.io/yodamad/easylab`) are **multi-platform**: each tag is a manifest list for `linux/amd64` and `linux/arm64`. Kubernetes (and `docker pull`) selects the variant that matches the node or host. You do not need separate Helm values per architecture—`image.repository` and `image.tag` stay the same.

## Image CPU architecture (`exec format error`)

If the pod exits immediately with `exec /app/main: exec format error`, the image’s architecture does not match your nodes (for example an **arm64** image on **amd64** workers). That often happens when you build a **custom** image on Apple Silicon with plain `docker build` and no platform flag.

**Fix for custom builds:** build and push with an explicit platform that matches your cluster (most cloud clusters are `linux/amd64`):

```bash
docker buildx build --platform linux/amd64 -t your-registry/easylab:your-tag --push .
```

For **arm64** nodes (for example AWS Graviton), use `--platform linux/arm64` instead. To publish **both** architectures in one tag (like CI does), use:

```bash
docker buildx build --platform linux/amd64,linux/arm64 -t your-registry/easylab:your-tag --push .
```

(`--load` only supports a single platform; multi-arch builds must be pushed to a registry.)

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
| `runtime.openFilesLimit` | Process `ulimit -n` before starting the app (helps Pulumi/fsnotify in pods) | `65536` |
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
| `traefik.enabled` | Install Traefik as part of this chart (IngressClass name pinned to `traefik`, matching `ingress.className`'s default) | `false` |
| `cert-manager.enabled` | Install cert-manager as part of this chart | `false` |
| `cert-manager.crds.enabled` | Install cert-manager CRDs (required on first install) | `true` |
| `resources.requests.memory` | Memory request | `1024Mi` |
| `resources.requests.cpu` | CPU request | `500m` |
| `resources.limits.memory` | Memory limit | `4096Mi` |
| `resources.limits.cpu` | CPU limit | `3000m` |
| `nodeSelector` | Pin the EasyLab server pod to nodes matching these labels (e.g. a dedicated node pool) | `{}` |
| `tolerations` | Tolerations for the EasyLab server pod, needed if its target node pool is tainted | `[]` |

!!! warning "Set an explicit data encryption key for persistent deployments"
    Because `config.dataDir` is set by default, persisted job files hold cluster kubeconfigs and DNS credentials. Provide a `LAB_DATA_ENCRYPTION_KEY` environment variable (a base64-encoded 32-byte key, e.g. from `openssl rand -base64 32`) through your secret / pod environment and keep it stable across upgrades, or previously-encrypted kubeconfigs become unreadable and the affected labs must be recreated. If `LAB_DATA_ENCRYPTION_KEY` is not set, the server auto-generates one and saves it to `<dataDir>/.encryption_key` so it still starts — but on Kubernetes this file only survives pod restarts if `dataDir` is backed by a persistent volume; without one, a new pod means a new key and unreadable existing job data. Explicitly setting the key via a Secret is strongly recommended for any real deployment. Setting a strong `PULUMI_CONFIG_PASSPHRASE` is likewise recommended (see [Docker — Environment Variables](docker.md#environment-variables) for details on both). Provider API credentials are held in memory only and are never written to lab state.

### Optional infrastructure components

By default (`traefik.enabled=false`, `cert-manager.enabled=false`), this chart does not install an ingress controller or cert-manager for you — it assumes a controller matching `ingress.className` (default `traefik`) and, if you rely on cert-manager annotations for TLS, cert-manager is **already installed** in your cluster. If they are not, you can let the chart install Traefik (matches the default `ingress.className` out of the box) and cert-manager instead:

```yaml
# Install Traefik alongside EasyLab (IngressClass name pinned to "traefik")
traefik:
  enabled: true

# Install cert-manager alongside EasyLab (CRDs included)
cert-manager:
  enabled: true
  crds:
    enabled: true
```

Or via `--set` flags:

```bash
helm install easylab oci://registry-1.docker.io/yodamad/easylab-helm \
  --version __VERSION__ \
  --set traefik.enabled=true \
  --set cert-manager.enabled=true \
  --set secrets.adminPassword="SuperAdmin"
```

If your cluster already has an ingress controller and/or cert-manager installed — including a different controller such as NGINX — leave the relevant flag at `false` (default) and configure `ingress.className` to match your existing controller.

All `traefik` and `cert-manager` values can be passed under their respective keys — see the [Traefik chart values](https://github.com/traefik/traefik-helm-chart) and [cert-manager chart values](https://cert-manager.io/docs/installation/helm/) for the full list.

### Exposing with Traefik

The chart creates a standard **Kubernetes Ingress** and defaults `ingress.className` to **`traefik`**, which matches Traefik’s default IngressClass on many clusters (for example [k3s](https://docs.k3s.io/networking#traefik-ingress-controller) and typical [Traefik Helm](https://doc.traefik.io/traefik/getting-started/install-traefik/) installs). If your cluster has no ingress controller yet, set `traefik.enabled=true` to have this chart install Traefik for you — see [Optional infrastructure components](#optional-infrastructure-components).

**Prerequisites**

- Traefik running with the Kubernetes Ingress provider enabled (either pre-existing, or installed via `traefik.enabled=true`).
- An IngressClass whose name matches `ingress.className` (default `traefik`). Check with:

  ```bash
  kubectl get ingressclass
  ```

  If your class is named differently (for example `traefik-internal`), set `--set ingress.className=traefik-internal` or the same field in your values file.

**Expose EasyLab**

1. Keep the app service internal: leave `service.type` as `ClusterIP` (default).
2. Enable ingress and set your hostname:

   ```yaml
   ingress:
     enabled: true
     host: easylab.example.com
     className: traefik
   ```

3. Optional: add Traefik-specific annotations under `ingress.annotations` if your install uses non-default entrypoint names, for example:

   ```yaml
   ingress:
     annotations:
       traefik.ingress.kubernetes.io/router.entrypoints: web,websecure
   ```

   Adjust names to match your Traefik static configuration (`entryPoints`).

TLS can be enabled with `ingress.tls` and a TLS secret in the same namespace, or with cert-manager annotations on the Ingress (same pattern as other ingress controllers).

## Examples

### Minimal install

```bash
helm install easylab oci://registry-1.docker.io/yodamad/easylab-helm \
  --version __VERSION__ \
  --set secrets.adminPassword="SuperAdmin"
```

### With Traefik ingress and TLS (cert-manager)

`ingress.className` defaults to `traefik`; set it explicitly here only if your IngressClass name differs.

```bash
helm install easylab oci://registry-1.docker.io/yodamad/easylab-helm \
  --version __VERSION__ \
  --set secrets.adminPassword="SuperAdmin" \
  --set ingress.enabled=true \
  --set ingress.host="easylab.example.com" \
  --set ingress.tls.enabled=true \
  --set ingress.className=traefik \
  --set ingress.annotations."cert-manager\.io/cluster-issuer"=letsencrypt
```

### With nginx ingress and TLS

If you use the NGINX Ingress Controller instead, set `ingress.className` to your NGINX IngressClass (often `nginx`).

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

### Fresh cluster (with Traefik and cert-manager)

For a cluster that does not have an ingress controller or cert-manager yet, `traefik.enabled=true` installs Traefik with the default `ingress.className=traefik` — no need to override it:

```bash
helm install easylab oci://registry-1.docker.io/yodamad/easylab-helm \
  --version __VERSION__ \
  --set secrets.adminPassword="SuperAdmin" \
  --set traefik.enabled=true \
  --set cert-manager.enabled=true \
  --set ingress.enabled=true \
  --set ingress.host="easylab.example.com" \
  --set ingress.tls.enabled=true \
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

### Pinning to a dedicated node pool

To keep the EasyLab server off the node pool student workspaces run on (see
[Splitting EasyLab and workspaces across node pools](admin.md#splitting-easylab-and-workspaces-across-node-pools)),
label a "control-plane" pool and target it:

```bash
helm install easylab oci://registry-1.docker.io/yodamad/easylab-helm \
  --version __VERSION__ \
  --set secrets.adminPassword="SuperAdmin" \
  --set nodeSelector.pool="control-plane"
```

If that pool is also tainted, add a matching toleration:

```bash
  --set tolerations[0].key="dedicated" \
  --set tolerations[0].operator="Equal" \
  --set tolerations[0].value="easylab" \
  --set tolerations[0].effect="NoSchedule"
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
  className: traefik
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
