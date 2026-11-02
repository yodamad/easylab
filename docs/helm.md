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
  --set secrets.adminPassword="your-secure-password" \
  --set secrets.dataEncryptionKey="$(openssl rand -base64 32)"
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
| `config.mode` | Areas served: `all` or `admin` — see [In-lab student portals](#in-lab-student-portals) | `""` (`all`) |
| `config.publicUrl` | This release's address as students reach it, used by in-lab student portals for Microsoft / GitHub / GitLab sign-in | `""` (the ingress host when `ingress.enabled`) |
| `config.portalImage` | Image the in-lab student portals run | `""` (this release's own image) |
| `secrets.create` | Create a Kubernetes secret | `true` |
| `secrets.adminPassword` | Admin login password | `""` |
| `secrets.studentPassword` | Student login password for this instance's own student area (each in-lab portal has its own) | `""` |
| `secrets.dataEncryptionKey` | **Required** with `config.dataDir` set (the default): base64-encoded 32-byte key encrypting kubeconfigs and DNS credentials in persisted job files | `""` |
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
| `ingress.httpsRedirect.enabled` | Redirect HTTP to HTTPS with a Traefik `redirectScheme` Middleware — created only when `ingress.className` is `traefik` and `ingress.tls.enabled=true`, see [Redirecting HTTP to HTTPS](#redirecting-http-to-https) | `true` |
| `ingress.httpsRedirect.permanent` | `true` issues a 301 (remembered by browsers), `false` a 302 | `true` |
| `ingress.httpsRedirect.apiVersion` | Middleware CRD API group — Traefik v3 uses `traefik.io/v1alpha1`, Traefik v2 `traefik.containo.us/v1alpha1` | `traefik.io/v1alpha1` |
| `traefik.enabled` | Install Traefik as part of this chart (IngressClass name pinned to `traefik`, matching `ingress.className`'s default) | `false` |
| `cert-manager.enabled` | Install cert-manager as part of this chart | `false` |
| `cert-manager.crds.enabled` | Install cert-manager CRDs (required on first install) | `true` |
| `certManager.namespace` | Namespace cert-manager's controller runs in — must match wherever it's actually installed | `cert-manager` |
| `certManager.clusterIssuer.create` | Create a Let's Encrypt ClusterIssuer for the EasyLab ingress — HTTP-01, or DNS-01 if `dns.azure.enabled=true` / `dns.ovh.enabled=true`. Required for all three solvers | `false` |
| `certManager.clusterIssuer.name` | ClusterIssuer name | `letsencrypt` |
| `certManager.clusterIssuer.email` | ACME account email | `""` |
| `certManager.clusterIssuer.server` | ACME server URL | `https://acme-v02.api.letsencrypt.org/directory` |
| `dns.azure.enabled` | Use Azure DNS-01 (cert-manager's built-in `azureDNS` solver) instead of HTTP-01 | `false` |
| `dns.azure.zone` | Azure DNS hosted zone name | `""` |
| `dns.azure.tenantId` / `.subscriptionId` / `.resourceGroup` / `.clientId` / `.clientSecret` | Azure service principal credentials | `""` |
| `dns.ovh.enabled` | Install the `cert-manager-webhook-ovh` DNS-01 solver and use it for `certManager.clusterIssuer` — see [Certificates for the EasyLab ingress](#certificates-for-the-easylab-ingress) | `false` |
| `dns.ovh.zone` | OVH DNS zone `ingress.host` is a subdomain of, e.g. `example.com` — only needed when `dns.externalDns.enabled=true`, see [Automatic DNS record for the EasyLab ingress](#automatic-dns-record-for-the-easylab-ingress-externaldns) | `""` |
| `dns.ovh.endpoint` | OVH API endpoint name: `ovh-eu`, `ovh-ca`, `kimsufi-eu`, `kimsufi-ca`, `soyoustart-eu`, `soyoustart-ca`, `runabove-ca` | `ovh-eu` |
| `dns.ovh.credentials.applicationKey` / `.applicationSecret` / `.applicationConsumerKey` | OVH API credentials for the DNS-01 solver, from [api.ovh.com/createToken](https://api.ovh.com/createToken/) with `GET`/`POST`/`PUT`/`DELETE` on `/domain/zone/*`. The chart stores them in its own Secret and points the ClusterIssuer at it | `""` |
| `cert-manager-webhook-ovh.*` | Raw values for the vendored OVH webhook chart — only the solver webhook itself (`groupName`, `certManager.namespace`, `certManager.serviceAccountName`). Its `issuers` list is deliberately empty and must stay empty: this chart owns the issuer. See [upstream chart values](https://github.com/aureq/cert-manager-webhook-ovh) | see `values.yaml` |
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
| `nodeSelector` | Pin the EasyLab server pod to nodes matching these labels (e.g. a dedicated node pool). Labs' [image pre-pull](admin-lab-management.md#image-pre-pull) leaves those nodes alone | `{}` |
| `tolerations` | Tolerations for the EasyLab server pod, needed if its target node pool is tainted | `[]` |

!!! warning "A data encryption key is required"
    Because `config.dataDir` is set by default, persisted job files hold cluster kubeconfigs and DNS credentials — so `secrets.dataEncryptionKey` is **required** and the pod will not start without it. Set it to a base64-encoded 32-byte key (`openssl rand -base64 32`); the chart puts it in the release Secret as `LAB_DATA_ENCRYPTION_KEY`:
    ```bash
    helm install easylab ... --set secrets.dataEncryptionKey="$(openssl rand -base64 32)"
    ```
    Keep it stable across upgrades: a different key makes previously-encrypted kubeconfigs unreadable and the affected labs must be recreated. EasyLab deliberately does not generate a key of its own — one written into `dataDir` would sit beside the data it protects, and on Kubernetes would also be lost whenever a pod restarted without a persistent volume. If you are upgrading a `dataDir` volume that still holds a `.encryption_key` file from an older version, set `secrets.dataEncryptionKey` to that file's contents before upgrading (see [Docker — Environment Variables](docker.md#environment-variables)). Setting a strong `PULUMI_CONFIG_PASSPHRASE` is likewise recommended. Provider API credentials are never written to lab state.

!!! tip "Keeping credentials across pod restarts"
    With `LAB_DATA_ENCRYPTION_KEY` set from a Secret, cloud provider credentials and DNS profiles saved in the admin UI are stored encrypted under it and are available again automatically after a pod restart (see [Credential storage](admin.md#credential-storage)). The Secret can be fed by whatever already manages secrets in your cluster — a plain Kubernetes Secret, External Secrets Operator, or an OpenBao / Vault agent injector — with no change to EasyLab: it only reads the environment variable. Without the variable, an admin can set a passphrase in the UI instead and enter it once after each restart.

!!! note "X-Forwarded-Proto trust"
    The server trusts an incoming `X-Forwarded-Proto: https` header to mark the session cookie `Secure` (env var `TRUST_FORWARDED_PROTO`, default `true` — see [Docker — Environment Variables](docker.md#environment-variables)). This is safe with the standard ingress-terminated-TLS setup above, since the ingress controller sets this header itself and any client-supplied value is overwritten. Only set `TRUST_FORWARDED_PROTO=false` if you expose the pod in a way where a client-supplied header could reach it unmodified.

### In-lab student portals

A lab can have its own [student portal](student-portal.md), deployed by EasyLab inside the
lab's cluster. Three values control how this release takes part:

```yaml
config:
  # "admin": this release serves the admin space only; students use each lab's portal.
  # Leave empty to also keep the central student space.
  mode: admin
  # Where portals send students for Microsoft / GitHub / GitLab sign-in.
  # Defaults to the ingress host when ingress.enabled is true.
  publicUrl: https://easylab.example.com
  # Defaults to this release's own image. The lab's nodes must be able to pull it.
  portalImage: ""
```

`secrets.studentPassword` is the password for the **admin instance's own** student area.
Each in-lab portal has its own, generated when it is deployed and shown on the lab's page,
which the portal checks locally against a hash EasyLab copies to it — so a password handed
to one lab's students opens that lab and no other. Set `config.mode: admin` if you want
every lab reachable only through its own portal.

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

All three solvers go through the same single ClusterIssuer, so `certManager.clusterIssuer.create=true` is required for any of them — including OVH. What is mutually exclusive is the DNS provider: enable at most one of `dns.azure.enabled` / `dns.ovh.enabled` (the chart fails the render if both are on).

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

A ready-to-edit values file for this scenario (AKS + Azure DNS-01 + ExternalDNS, with every value you must fill in marked `xxx`) ships with the chart as `helm/easylab/values-azure-example.yaml`.

**OVH DNS-01**

Vendors the [`cert-manager-webhook-ovh`](https://github.com/aureq/cert-manager-webhook-ovh) chart as the DNS-01 solver — the same webhook EasyLab's own Pulumi-provisioned student clusters use. The chart creates the `ClusterIssuer` that uses it, the credential `Secret` and the RBAC letting the webhook read that Secret, so the configuration is the same shape as the Azure one above: three credentials and a flag.

```yaml
# ovh-dns01-values.yaml
secrets:
  adminPassword: "SuperAdmin"

traefik:
  enabled: true
cert-manager:
  enabled: true

certManager:
  clusterIssuer:
    create: true
    email: "you@example.com"

dns:
  ovh:
    enabled: true
    credentials:
      applicationKey: "your-app-key"
      applicationSecret: "your-app-secret"
      applicationConsumerKey: "your-consumer-key"

ingress:
  enabled: true
  host: easylab.example.com
  tls:
    enabled: true
```

Create the three credentials at [api.ovh.com/createToken](https://api.ovh.com/createToken/) with `GET`, `POST`, `PUT` and `DELETE` on `/domain/zone/*`. The ingress is annotated with the issuer automatically — do not add a `cert-manager.io/cluster-issuer` annotation of your own, since a stale one would win on merge and point cert-manager at an issuer that does not exist (the chart refuses to render if yours disagrees with `certManager.clusterIssuer.name`).

```bash
helm install easylab oci://registry-1.docker.io/yodamad/easylab-helm \
  --version __VERSION__ \
  --namespace cert-manager --create-namespace \
  -f ovh-dns01-values.yaml \
  --wait --timeout 5m
```

The issuer is created by a `post-install,post-upgrade` hook weighted to run after cert-manager's own `startupapicheck`, so it is not applied until cert-manager's webhook answers — the same treatment the HTTP-01/Azure paths get. One consequence: if `startupapicheck` fails, the release now fails without creating the issuer, rather than creating an issuer that nothing can use.

Only OVH's `application` authentication method is supported here. OAuth2 credentials, external account bindings and per-issuer ACME servers are not exposed by this chart; use the [upstream chart](https://github.com/aureq/cert-manager-webhook-ovh) directly as its own release if you need them.

!!! warning "Migrating from a values file with a `cert-manager-webhook-ovh.issuers[]` block"
    OVH DNS-01 used to be configured through that nested block, which created its own `ClusterIssuer` named `letsencrypt-ovh`. It is gone: filling it in now fails the render with a message pointing at `dns.ovh.credentials`. Two things to know before upgrading:

    - **The issuer is renamed** to `certManager.clusterIssuer.name` (default `letsencrypt`). On upgrade Helm deletes the old `letsencrypt-ovh` ClusterIssuer and its `letsencrypt-ovh-ovh-credentials` Secret — they were release resources — and the hook creates the new one afterwards, so there is a short window with no OVH issuer. EasyLab's own ingress is re-annotated for you, but **any other `Ingress` or `Certificate` still annotated `cert-manager.io/cluster-issuer: letsencrypt-ovh` stops renewing** (existing certificates keep serving until they expire). To avoid touching them, set `certManager.clusterIssuer.name: letsencrypt-ovh` and keep the old name.
    - **A new ACME account is registered** when the name changes, because `privateKeySecretRef` follows the issuer name. Harmless in itself, but it counts against Let's Encrypt's new-account rate limits, so don't rename back and forth.

!!! warning "Fresh cluster with `dns.ovh.enabled=true`: two phases, one command"
    If cert-manager is **already** on the cluster, nothing special is needed — a single `helm install` with `dns.ovh.enabled=true` works. The rest of this box is only for a bare cluster where this chart installs cert-manager too.

    Combining `cert-manager.enabled=true` and `dns.ovh.enabled=true` in a single `helm install` on a cluster that has no cert-manager CRDs yet fails with errors like:
    ```
    Error: INSTALLATION FAILED: unable to build kubernetes objects from release manifest:
    [resource mapping not found for name: "easylab-cert-manager-webhook-ovh-ca" namespace: "" from "":
    no matches for kind "Certificate" in version "cert-manager.io/v1"
    ensure CRDs are installed first, ...]
    ```
    This is a general Helm/cert-manager limitation (related to the CRD-upgrade caveat above), not bad values: Helm resolves every kind in the *entire* combined manifest — EasyLab, cert-manager and the `cert-manager-webhook-ovh` subchart — against the cluster's API discovery before applying anything. The webhook subchart's own PKI (a self-signed `Issuer`, a CA `Certificate` and its serving `Certificate`) is made of `cert-manager.io/v1` kinds and is **not** optional, so the whole install aborts before creating anything — including cert-manager itself. Deferring the EasyLab ClusterIssuer to a hook, which this chart does, is not enough to avoid it.

    Run both phases as one command: install with the webhook disabled so the CRDs land, then upgrade with the same values file now that they are registered.
    ```bash
    helm install easylab oci://registry-1.docker.io/yodamad/easylab-helm \
      --version __VERSION__ \
      --namespace cert-manager --create-namespace \
      -f ovh-dns01-values.yaml \
      --set dns.ovh.enabled=false \
      --wait --timeout 5m \
    && helm upgrade easylab oci://registry-1.docker.io/yodamad/easylab-helm \
      --version __VERSION__ \
      --namespace cert-manager \
      -f ovh-dns01-values.yaml \
      --wait --timeout 5m
    ```
    Pass `-f` to **both** phases. `helm upgrade` does not inherit the previous release's values, so an upgrade with only `--set dns.ovh.enabled=true` falls back to the chart defaults and fails on the missing credentials.

    Verify the CRDs landed after phase one if you want to confirm before the upgrade: `kubectl get crd | grep cert-manager.io`.

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

### Redirecting HTTP to HTTPS

When `ingress.className` is `traefik` and `ingress.tls.enabled=true`, the chart creates a Traefik `redirectScheme` **Middleware** and references it from the Ingress automatically. Nothing extra to set:

```yaml
ingress:
  enabled: true
  host: easylab.example.com
  className: traefik
  tls:
    enabled: true
```

renders a `<release>-https-redirect` Middleware plus this annotation on the Ingress:

```yaml
traefik.ingress.kubernetes.io/router.middlewares: easylab-easylab-https-redirect@kubernetescrd
```

!!! warning "Serving EasyLab over plain HTTP breaks login"
    This is not cosmetic. The login page hashes the password with [`crypto.subtle`](https://developer.mozilla.org/en-US/docs/Web/API/Crypto/subtle) (Web Crypto), which browsers expose **only in a secure context**. Over `http://`, `crypto.subtle` is `undefined` and the login form fails with `TypeError: Cannot read properties of undefined (reading 'digest')` in the browser console, showing only a generic "An error occurred. Please try again." to the user. If port 80 answers without redirecting, any client that reaches the `http://` URL — from a bookmark, a history entry, or a typed hostname — cannot log in at all. The default `permanent: true` (301) means the browser remembers the redirect, which is what repairs a client already stuck on such a URL.

The Middleware is deliberately **not** created when `ingress.tls.enabled=false`, since redirecting to HTTPS with no certificate configured only breaks the site a different way. Turn the redirect off with `ingress.httpsRedirect.enabled=false`.

If you set `traefik.ingress.kubernetes.io/router.middlewares` yourself under `ingress.annotations`, your value **replaces** the chart's rather than adding to it — include `<namespace>-<release>-https-redirect@kubernetescrd` in your own comma-separated chain to keep the redirect.

**Other ingress controllers** express this as their own annotation instead — the chart does not manage it for them. For NGINX:

```yaml
ingress:
  className: nginx
  annotations:
    nginx.ingress.kubernetes.io/ssl-redirect: "true"
```

## Examples

### Minimal install

```bash
helm install easylab oci://registry-1.docker.io/yodamad/easylab-helm \
  --version __VERSION__ \
  --set secrets.adminPassword="SuperAdmin"
```

### With Traefik ingress and TLS (cert-manager)

`ingress.className` defaults to `traefik`; set it explicitly here only if your IngressClass name differs. HTTP is redirected to HTTPS automatically — see [Redirecting HTTP to HTTPS](#redirecting-http-to-https).

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
[Splitting EasyLab and workspaces across node pools](admin-lab-creation.md#splitting-easylab-and-workspaces-across-node-pools)),
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

Setting `nodeSelector` also tells the server where *not* to cache workspace
images: the chart passes those labels to it as `EASYLAB_SERVER_NODE_SELECTOR`, and
every lab's [image pre-pull](admin-lab-management.md#image-pre-pull) then skips the
pool the server runs on, which never runs a workspace. Nothing else to configure —
and it only applies to labs on this same cluster, since the labels are matched
against the lab cluster's nodes.

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
helm uninstall easylab --cascade foreground --wait --timeout 5m
```

`--cascade foreground --wait` is worth the extra minute whenever `dns.ovh.enabled=true` — it makes Helm finish deleting each resource before starting the next, instead of firing all the deletes off at once. See the namespace warning below for what that avoids.

!!! warning "PersistentVolumeClaims are not deleted by `helm uninstall`"
    To fully clean up, delete the PVCs manually:
    ```bash
    kubectl delete pvc -n easylab -l app.kubernetes.io/name=easylab
    ```

!!! warning "The ClusterIssuer is not deleted by `helm uninstall` either"
    It is created by a Helm hook, and hook resources are not part of the release manifest, so neither `helm uninstall` nor `helm rollback` removes it. Left behind with `dns.ovh.enabled=true` it points at a solver webhook that no longer exists, and any `Certificate` still referencing it waits forever. Delete it by hand:
    ```bash
    kubectl delete clusterissuer letsencrypt    # or your certManager.clusterIssuer.name
    ```

!!! warning "Namespace stuck in `Terminating` after uninstall (`dns.ovh.enabled=true`)"
    With `namespace.create=true` (the default) the namespace is itself a release resource, so `helm uninstall` deletes it — and the `cert-manager-webhook-ovh` subchart registers an **aggregated APIService** (`v1alpha1.<groupName>`, default `v1alpha1.acme.easylab.local`) whose backing Service lives *inside that same namespace*.

    If the webhook's pod and Service go away while the APIService object still exists, the API aggregation layer reports that group unavailable. Namespace finalization needs a complete API discovery pass to enumerate what is left to delete, so the failing group wedges the namespace in `Terminating` indefinitely. It blocks **every** namespace deletion on the cluster for as long as the stale APIService is there, not only EasyLab's.

    Confirm and recover:
    ```bash
    kubectl get ns easylab -o json | jq '.status.conditions[] | select(.status=="True")'
    kubectl get apiservices | grep -v 'True'
    kubectl delete apiservice v1alpha1.acme.easylab.local
    ```
    A `NamespaceDeletionDiscoveryFailure` condition is this problem; the namespace finalizes within seconds of deleting the APIService. A `NamespaceDeletionContentFailure` is a different one — leftover objects holding finalizers, which you can find with:
    ```bash
    kubectl api-resources --verbs=list --namespaced -o name \
      | xargs -n1 kubectl get -n easylab --ignore-not-found --no-headers 2>/dev/null
    ```

    Do **not** clear the namespace's own finalizers (`kubectl patch ns easylab -p '{"metadata":{"finalizers":[]}}' --type=merge`). That drops the namespace record while its contents still exist, orphaning resources that are then very hard to track down.

    Avoid it next time with `--cascade foreground --wait` as above, or by setting `namespace.create=false` and managing the namespace yourself, so `helm uninstall` never tries to delete it.

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
