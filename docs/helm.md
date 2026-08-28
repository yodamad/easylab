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
- If you terminate TLS with cert-manager annotations (rather than a pre-existing secret), **cert-manager** and a configured `ClusterIssuer` must already be installed, or set `cert-manager.enabled=true` to have the chart install cert-manager for you. This chart can also create the `ClusterIssuer` itself (HTTP-01, Azure DNS-01, or OVH DNS-01) — see [Certificates for the EasyLab ingress](#certificates-for-the-easylab-ingress).

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
| `certManager.namespace` | Namespace cert-manager's controller runs in — must match wherever it's actually installed | `cert-manager` |
| `certManager.clusterIssuer.create` | Create a Let's Encrypt ClusterIssuer for the EasyLab ingress (HTTP-01, or Azure DNS-01 if `dns.azure.enabled=true`) | `false` |
| `certManager.clusterIssuer.name` | ClusterIssuer name | `letsencrypt` |
| `certManager.clusterIssuer.email` | ACME account email | `""` |
| `certManager.clusterIssuer.server` | ACME server URL | `https://acme-v02.api.letsencrypt.org/directory` |
| `dns.azure.enabled` | Use Azure DNS-01 (cert-manager's built-in `azureDNS` solver) instead of HTTP-01 | `false` |
| `dns.azure.zone` | Azure DNS hosted zone name | `""` |
| `dns.azure.tenantId` / `.subscriptionId` / `.resourceGroup` / `.clientId` / `.clientSecret` | Azure service principal credentials | `""` |
| `dns.ovh.enabled` | Install `cert-manager-webhook-ovh` and let it create its own OVH DNS-01 ClusterIssuer — see [Certificates for the EasyLab ingress](#certificates-for-the-easylab-ingress) | `false` |
| `dns.ovh.zone` | OVH DNS zone `ingress.host` is a subdomain of, e.g. `example.com` — only needed when `dns.externalDns.enabled=true`, see [Automatic DNS record for the EasyLab ingress](#automatic-dns-record-for-the-easylab-ingress-externaldns) | `""` |
| `cert-manager-webhook-ovh.*` | Raw values for the vendored OVH webhook chart (issuer name, ACME email, OVH credentials, etc.) — deeply nested, see [upstream chart values](https://github.com/aureq/cert-manager-webhook-ovh) | see `values.yaml` |
| `dns.externalDns.enabled` | Run a persistent ExternalDNS controller that keeps `ingress.host`'s A record pointed at the ingress controller's LoadBalancer IP — see [Automatic DNS record for the EasyLab ingress](#automatic-dns-record-for-the-easylab-ingress-externaldns) | `false` |
| `dns.externalDns.image.repository` / `.tag` | ExternalDNS container image | `registry.k8s.io/external-dns/external-dns` / `v0.22.0` |
| `dns.externalDns.policy` | `sync` also deletes the record if it's no longer needed; `upsert-only` never deletes | `sync` |
| `dns.externalDns.txtOwnerId` | Scopes ExternalDNS's TXT ownership registry so this install never touches records owned by another ExternalDNS instance sharing the same zone; empty defaults to the release fullname | `""` |
| `dns.externalDns.dryRun` | Log planned DNS changes without calling the provider API — recommended `true` for a first rollout | `false` |
| `dns.externalDns.resources` | Resource requests/limits for the ExternalDNS pod | see `values.yaml` |
| `resources.requests.memory` | Memory request | `1024Mi` |
| `resources.requests.cpu` | CPU request | `500m` |
| `resources.limits.memory` | Memory limit | `4096Mi` |
| `resources.limits.cpu` | CPU limit | `3000m` |
| `nodeSelector` | Pin the EasyLab server pod to nodes matching these labels (e.g. a dedicated node pool) | `{}` |
| `tolerations` | Tolerations for the EasyLab server pod, needed if its target node pool is tainted | `[]` |

!!! warning "Set an explicit data encryption key for persistent deployments"
    Because `config.dataDir` is set by default, persisted job files hold cluster kubeconfigs and DNS credentials. Provide a `LAB_DATA_ENCRYPTION_KEY` environment variable (a base64-encoded 32-byte key, e.g. from `openssl rand -base64 32`) through your secret / pod environment and keep it stable across upgrades, or previously-encrypted kubeconfigs become unreadable and the affected labs must be recreated. If `LAB_DATA_ENCRYPTION_KEY` is not set, the server auto-generates one and saves it to `<dataDir>/.encryption_key` so it still starts — but on Kubernetes this file only survives pod restarts if `dataDir` is backed by a persistent volume; without one, a new pod means a new key and unreadable existing job data. Explicitly setting the key via a Secret is strongly recommended for any real deployment. Setting a strong `PULUMI_CONFIG_PASSPHRASE` is likewise recommended (see [Docker — Environment Variables](docker.md#environment-variables) for details on both). Provider API credentials are held in memory only and are never written to lab state.

!!! note "X-Forwarded-Proto trust"
    The server trusts an incoming `X-Forwarded-Proto: https` header to mark the session cookie `Secure` (env var `TRUST_FORWARDED_PROTO`, default `true` — see [Docker — Environment Variables](docker.md#environment-variables)). This is safe with the standard ingress-terminated-TLS setup above, since the ingress controller sets this header itself and any client-supplied value is overwritten. Only set `TRUST_FORWARDED_PROTO=false` if you expose the pod in a way where a client-supplied header could reach it unmodified.

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

All `traefik` and `cert-manager` values can be passed under their respective keys — see the [Traefik chart values](https://github.com/traefik/traefik-helm-chart) and [cert-manager chart values](https://cert-manager.io/docs/installation/helm/) for the full list. This also covers scheduling: `traefik.nodeSelector`/`.tolerations` and `cert-manager.nodeSelector`/`.tolerations` pin those components to specific nodes the same way `nodeSelector`/`tolerations` pin the EasyLab pod itself (see [Pinning to a dedicated node pool](#pinning-to-a-dedicated-node-pool)), for example `--set traefik.nodeSelector.pool=control-plane`.

!!! warning "cert-manager CRDs are not updated by `helm upgrade`"
    `cert-manager.crds.enabled=true` installs the cert-manager CRDs on first install, but Helm does not update CRDs on `helm upgrade` — this is a general Helm/cert-manager limitation, not specific to this chart. Bumping the vendored cert-manager version may require manually applying the new CRDs (see [cert-manager's CRD upgrade docs](https://cert-manager.io/docs/installation/upgrading/)) before `helm upgrade` picks up the new version.

### Certificates for the EasyLab ingress

Beyond installing cert-manager itself, this chart can also create the `ClusterIssuer` that actually requests a certificate for the EasyLab ingress — closing the gap left by `cert-manager.enabled=true` alone (that only installs cert-manager; nothing issues a certificate until an issuer exists).

**Namespace alignment (read this first)**

`certManager.namespace` (and, for OVH, `cert-manager-webhook-ovh.certManager.namespace`) must match the namespace cert-manager's own pods actually run in. `cert-manager.enabled=true` installs cert-manager as a normal Helm dependency of this chart, which lands in whatever `--namespace` you pass to `helm install`/`helm upgrade` — **not** this chart's own `namespace.name` (EasyLab's own resources are explicitly namespaced independently of that flag; cert-manager's are not). Always pass `--namespace cert-manager --create-namespace` explicitly when using any of the options below, and verify with:

```bash
kubectl get pods -n cert-manager
```

If cert-manager is pre-installed separately, use its actual namespace instead (commonly `cert-manager`).

Enable only **one** ClusterIssuer mechanism at a time: `certManager.clusterIssuer.create` (HTTP-01 or Azure DNS-01) *or* `dns.ovh.enabled` (OVH DNS-01) — not both.

**HTTP-01 (default solver, no DNS provider)**

Needs no DNS credentials, but the EasyLab ingress host must be reachable on port 80 for the ACME challenge:

```bash
helm install easylab oci://registry-1.docker.io/yodamad/easylab-helm \
  --version __VERSION__ \
  --namespace cert-manager --create-namespace \
  --set secrets.adminPassword="SuperAdmin" \
  --set traefik.enabled=true \
  --set cert-manager.enabled=true \
  --set certManager.clusterIssuer.create=true \
  --set certManager.clusterIssuer.email="you@example.com" \
  --set ingress.enabled=true \
  --set ingress.host="easylab.example.com" \
  --set ingress.tls.enabled=true \
  --wait --timeout 5m
```

`--wait --timeout 5m` makes Helm wait for cert-manager's Deployments (webhook included) to be ready before the `post-install` hook that creates the `ClusterIssuer` fires — without it, the ClusterIssuer can fail to create on a fresh combined install because cert-manager's admission webhook isn't up yet. The `cert-manager.io/cluster-issuer` Ingress annotation is added automatically; no manual `ingress.annotations` `--set` is needed here.

**Azure DNS-01**

No port-80 requirement; uses cert-manager's built-in `azureDNS` solver:

```bash
helm install easylab oci://registry-1.docker.io/yodamad/easylab-helm \
  --version __VERSION__ \
  --namespace cert-manager --create-namespace \
  --set secrets.adminPassword="SuperAdmin" \
  --set traefik.enabled=true \
  --set cert-manager.enabled=true \
  --set certManager.clusterIssuer.create=true \
  --set certManager.clusterIssuer.email="you@example.com" \
  --set dns.azure.enabled=true \
  --set dns.azure.zone="example.com" \
  --set dns.azure.tenantId="..." \
  --set dns.azure.subscriptionId="..." \
  --set dns.azure.resourceGroup="..." \
  --set dns.azure.clientId="..." \
  --set dns.azure.clientSecret="..." \
  --set ingress.enabled=true \
  --set ingress.host="easylab.example.com" \
  --set ingress.tls.enabled=true \
  --wait --timeout 5m
```

**OVH DNS-01**

Vendors the [`cert-manager-webhook-ovh`](https://github.com/aureq/cert-manager-webhook-ovh) chart, which manages its own `ClusterIssuer`, credential `Secret`, and RBAC end-to-end — the same webhook EasyLab's own Pulumi-provisioned student clusters use for DNS-01. Its values are deeply nested, so a values file is easier than a long `--set` chain:

```yaml
# ovh-dns01-values.yaml
secrets:
  adminPassword: "SuperAdmin"

traefik:
  enabled: true
cert-manager:
  enabled: true

dns:
  ovh:
    enabled: true

cert-manager-webhook-ovh:
  issuers:
    - name: letsencrypt-ovh
      create: true
      kind: ClusterIssuer
      acmeServerUrl: https://acme-v02.api.letsencrypt.org/directory
      email: "you@example.com"
      ovhEndpointName: ovh-eu
      ovhAuthenticationMethod: application
      ovhAuthentication:
        applicationKey: "your-app-key"
        applicationSecret: "your-app-secret"
        applicationConsumerKey: "your-consumer-key"

ingress:
  enabled: true
  host: easylab.example.com
  tls:
    enabled: true
  # The auto-annotation only covers certManager.clusterIssuer.create (HTTP-01/
  # Azure) above — for OVH, match this to cert-manager-webhook-ovh.issuers[0].name:
  annotations:
    cert-manager.io/cluster-issuer: letsencrypt-ovh
```

```bash
helm install easylab oci://registry-1.docker.io/yodamad/easylab-helm \
  --version __VERSION__ \
  --namespace cert-manager --create-namespace \
  -f ovh-dns01-values.yaml \
  --wait --timeout 5m
```

Unlike the HTTP-01/Azure path above, the OVH issuer is created directly by the vendored webhook chart's own templates, not deferred to a post-install hook — `--wait --timeout 5m` reduces but does not fully eliminate a possible webhook-readiness race on a fresh combined install. If the first install fails on this, a `helm upgrade` retry with the same values succeeds once cert-manager is up. See the [`cert-manager-webhook-ovh` values reference](https://github.com/aureq/cert-manager-webhook-ovh/blob/main/charts/cert-manager-webhook-ovh/values.yaml) for the full set of `issuers[]` fields (OAuth2 authentication, EAB, alternate OVH endpoints, etc.).

!!! warning "Fresh cluster with `dns.ovh.enabled=true`: install in two steps"
    Combining `cert-manager.enabled=true` and `dns.ovh.enabled=true` in a single `helm install` on a cluster that has no cert-manager CRDs yet fails with errors like:
    ```
    Error: INSTALLATION FAILED: unable to build kubernetes objects from release manifest:
    [resource mapping not found for name: "letsencrypt-ovh" namespace: "" from "":
    no matches for kind "ClusterIssuer" in version "cert-manager.io/v1"
    ensure CRDs are installed first, ...]
    ```
    This is a general Helm/cert-manager limitation (related to the CRD-upgrade caveat above), not specific to bad values: Helm validates the *entire* combined manifest — EasyLab, cert-manager, and the `cert-manager-webhook-ovh` subchart — against the cluster's API discovery cache before applying anything. Since the `cert-manager-webhook-ovh` subchart's `Certificate`/`Issuer`/`ClusterIssuer` resources are validated in that same pass, and the cert-manager CRDs those kinds depend on don't exist on the API server yet, the whole install aborts before creating anything — including cert-manager itself.

    Fix by splitting into two steps. First, install cert-manager and its CRDs only, with the OVH webhook disabled (`dns.ovh.enabled` is the `condition:` gate for that entire dependency in `Chart.yaml`, so setting it `false` drops its templates from the render):
    ```bash
    helm install easylab oci://registry-1.docker.io/yodamad/easylab-helm \
      --version __VERSION__ \
      --namespace cert-manager --create-namespace \
      --set secrets.adminPassword="SuperAdmin" \
      --set traefik.enabled=true \
      --set cert-manager.enabled=true \
      --set cert-manager.crds.enabled=true \
      --set dns.ovh.enabled=false \
      --wait --timeout 5m
    ```
    Then upgrade with your full values file (`dns.ovh.enabled: true` included), now that the CRDs are registered:
    ```bash
    helm upgrade easylab oci://registry-1.docker.io/yodamad/easylab-helm \
      --version __VERSION__ \
      --namespace cert-manager \
      -f ovh-dns01-values.yaml \
      --wait --timeout 5m
    ```
    Verify the CRDs landed after step 1 if you want to confirm before upgrading: `kubectl get crd | grep cert-manager.io`.

Once any of the above is applied, check certificate status with:

```bash
kubectl describe clusterissuer <issuer-name>
kubectl describe certificate -n easylab easylab-tls
```

### Automatic DNS record for the EasyLab ingress (ExternalDNS)

Everything above (`certManager.clusterIssuer.create`, `dns.azure.enabled`, `dns.ovh.enabled`) gets you a **certificate** for `ingress.host` — it does not make the hostname resolve. DNS-01 challenges only prove domain ownership via an ephemeral ACME TXT record that cert-manager deletes again once validated; the actual **A record** pointing `ingress.host` at the ingress controller's LoadBalancer IP has always been a manual step, done once by hand in your DNS provider's console.

Setting `dns.externalDns.enabled=true` automates that: it runs a persistent [ExternalDNS](https://github.com/kubernetes-sigs/external-dns) controller that watches the EasyLab Ingress and keeps its A record in sync with the ingress controller's current LoadBalancer IP — including if that IP ever changes. It requires one of the OVH or Azure DNS-01 sections above to already be configured, since it reuses those same credentials (nothing new to enter, beyond the OVH zone below).

**OVH**, layered onto the `ovh-dns01-values.yaml` example above — add `dns.ovh.zone` (the zone `ingress.host` is a subdomain of, e.g. `labdevrel.ovh`) and `dns.externalDns.enabled`:

```yaml
dns:
  ovh:
    enabled: true
    zone: labdevrel.ovh   # NEW — ExternalDNS needs the zone itself, not the ingress host
  externalDns:
    enabled: true
    dryRun: true            # recommended for a first rollout — see below
```

**Azure** needs no new value — `dns.azure.zone` is already required for the DNS-01 solver and is reused as-is:

```yaml
dns:
  azure:
    enabled: true
    zone: example.com
  externalDns:
    enabled: true
    dryRun: true
```

```bash
helm upgrade easylab oci://registry-1.docker.io/yodamad/easylab-helm \
  --version __VERSION__ \
  --namespace easylab \
  -f your-values.yaml \
  --wait --timeout 5m
```

**Recommended: dry-run first.** With `dns.externalDns.dryRun=true`, ExternalDNS logs the DNS changes it would make without calling the OVH/Azure API. Confirm it identified the right record, then set `dryRun: false` and `helm upgrade` again to go live:

```bash
kubectl get deploy,pods -n easylab -l app.kubernetes.io/component=externaldns
kubectl logs -n easylab -l app.kubernetes.io/component=externaldns
```

Once live, confirm the hostname resolves without any manual step: `dig +short <ingress.host>`.

!!! note "Wildcard `ingress.host`"
    A wildcard host (e.g. `*.easylab.example.com`) works with ExternalDNS — both OVH and Azure DNS accept a `*` subdomain prefix for an A record. The one constraint is TLS: Let's Encrypt only issues wildcard certificates via DNS-01, never HTTP-01, so a wildcard `ingress.host` needs `dns.ovh.enabled` or `dns.azure.enabled` already set (which `dns.externalDns.enabled` requires anyway) — it will not work with the default HTTP-01 `certManager.clusterIssuer.create` path.

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

For a cluster that does not have an ingress controller or cert-manager yet, `traefik.enabled=true` installs Traefik with the default `ingress.className=traefik` — no need to override it. `certManager.clusterIssuer.create=true` has this chart create the `ClusterIssuer` too (HTTP-01 here — see [Certificates for the EasyLab ingress](#certificates-for-the-easylab-ingress) for Azure/OVH DNS-01 alternatives), so the certificate actually gets issued instead of the Ingress annotation pointing at a `ClusterIssuer` that doesn't exist:

```bash
helm install easylab oci://registry-1.docker.io/yodamad/easylab-helm \
  --version __VERSION__ \
  --namespace cert-manager --create-namespace \
  --set secrets.adminPassword="SuperAdmin" \
  --set traefik.enabled=true \
  --set cert-manager.enabled=true \
  --set certManager.clusterIssuer.create=true \
  --set certManager.clusterIssuer.email="you@example.com" \
  --set ingress.enabled=true \
  --set ingress.host="easylab.example.com" \
  --set ingress.tls.enabled=true \
  --wait --timeout 5m
```

`--namespace cert-manager --create-namespace` makes `certManager.namespace`'s default line up with where cert-manager actually lands (see the namespace alignment note above). `--wait --timeout 5m` lets cert-manager's webhook come up before the `ClusterIssuer` is created. The `cert-manager.io/cluster-issuer` annotation is now added to the Ingress automatically — no manual `ingress.annotations` `--set` needed.

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
