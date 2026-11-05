---
icon: lucide/package
title: Helm
---

# Helm Chart Deployment

EasyLab ships as a Helm chart published on Docker Hub as an OCI artifact. Beyond EasyLab
itself, the chart can install everything a bare cluster needs to serve it over HTTPS: an
ingress controller, cert-manager, a Let's Encrypt `ClusterIssuer`, a DNS-01 solver and a
controller that keeps the hostname's A record pointed at the right IP.

| Always installed | Installed on request |
|------------------|----------------------|
| Deployment, Service, ConfigMap, Secret, two PVCs, the namespace, plus an Ingress and a Traefik HTTP→HTTPS Middleware when `ingress.enabled` | Traefik (`traefik.enabled`) · cert-manager (`cert-manager.enabled`) · a Let's Encrypt `ClusterIssuer` (`certManager.clusterIssuer.create`) · the OVH DNS-01 solver webhook (`dns.ovh.enabled`) · ExternalDNS (`dns.externalDns.enabled`) |

**Start from a [recipe](helm-recipes.md)**, not from this page: pick the complete values
file matching your cluster, replace the `CHANGE_ME` placeholders, and come back here only
if something misbehaves.

## Prerequisites

- Kubernetes **v1.24+** and Helm **3.8+** (OCI support).
- An **ingress controller** whose IngressClass matches `ingress.className` (default
  `traefik`), if you set `ingress.enabled=true` — the recommended way to expose EasyLab.
  Most managed offerings, OVHcloud Managed Kubernetes included, ship with **no** ingress
  controller; only some distributions (k3s, for example) preinstall Traefik. Either
  install one yourself, or let the chart do it with `traefik.enabled=true`
  ([recipe 2](helm-recipes.md#recipe-2)). For NGINX or anything else, install it
  separately and set `ingress.className` to match.
- **cert-manager** plus a `ClusterIssuer`, if the Ingress gets its certificate from
  cert-manager rather than a secret you create yourself. The chart can install both —
  see [Certificates for the EasyLab ingress](#certificates-for-the-easylab-ingress).
- A **`ReadWriteOnce` StorageClass**. The chart claims two PVCs (jobs and data) and the
  Deployment uses the `Recreate` strategy, so leave `replicaCount` at `1`.
- A **base64-encoded 32-byte encryption key** — see [Required secrets](#required-secrets).
  The pod will not start without it.

Application images are **multi-platform**: every tag of `docker.io/yodamad/easylab` is a
manifest list for `linux/amd64` and `linux/arm64`, so Kubernetes picks the right variant
and no architecture-specific values are needed. (Custom builds are a different story — see
the [`exec format error`](#troubleshooting) entry in Troubleshooting.)

## Install

The smallest install that works, reachable via port-forward:

```bash
helm install easylab oci://registry-1.docker.io/yodamad/easylab-helm \
  --version __VERSION__ \
  --set secrets.adminPassword="your-secure-password" \
  --set secrets.dataEncryptionKey="$(openssl rand -base64 32)"
```

```bash
kubectl port-forward -n easylab svc/easylab-service 8080:80
# then open http://localhost:8080
```

For anything beyond a first look, use a values file instead of `--set` — every
[recipe](helm-recipes.md) is written that way:

```bash
helm install easylab oci://registry-1.docker.io/yodamad/easylab-helm \
  --version __VERSION__ \
  -f my-values.yaml
```

Chart versions follow [SemVer](https://semver.org/) without the `v` prefix. List them on
[Docker Hub](https://hub.docker.com/r/yodamad/easylab-helm/tags?name=&ordering=-name&page_size=25)
or with `helm show chart oci://registry-1.docker.io/yodamad/easylab-helm --version __VERSION__`.

## Values file recipes {#recipes}

Complete, copy-pasteable values files live on their own page:
**[Helm values recipes](helm-recipes.md)**.

| Your situation | Recipe |
|----------------|--------|
| Cluster already has an ingress controller and cert-manager | [1 — Bring your own ingress and issuer](helm-recipes.md#recipe-1) |
| Bare cluster, hostname's DNS record created by hand, port 80 reachable | [2 — Chart installs Traefik + cert-manager (HTTP-01)](helm-recipes.md#recipe-2) |
| OVH DNS zone, want the certificate *and* the A record handled for you | [3 — OVH DNS-01 + ExternalDNS](helm-recipes.md#recipe-3) |
| AKS / Azure DNS zone | [4 — Azure DNS-01 + ExternalDNS](helm-recipes.md#recipe-4) |
| Admin console only; students use each lab's own portal | [5 — Admin-only release](helm-recipes.md#recipe-5) |
| Production: dedicated node pool, real storage class, sizing | [6 — Production overlay](helm-recipes.md#recipe-6) |

The rest of this page is the reference behind them: what each value means, which namespace
things land in, and what to do when one misbehaves.

## Namespaces {#namespaces}

Up to three namespaces are in play, and mixing them up is the most common cause of a
certificate that never issues. **EasyLab's own resources ignore `--namespace`** — they go
wherever `namespace.name` says, which the chart creates itself when `namespace.create` is
`true` (the default).

| Resource | Namespace | Value |
|----------|-----------|-------|
| Deployment, Service, Ingress, ConfigMap, Secret, PVCs, Middleware, ExternalDNS | `easylab` | `namespace.name` |
| Helm release metadata, Traefik, cert-manager, the OVH solver webhook, the OVH DNS credential Secret and its RBAC | whatever `--namespace` says | `--namespace` |
| Azure DNS credential Secret | `cert-manager` | `certManager.namespace` |

What follows from that:

- `certManager.namespace` must match where cert-manager's pods **actually run**. With
  `cert-manager.enabled=true` they land in the release namespace, so pass
  `--namespace cert-manager --create-namespace` on install and keep passing
  `--namespace cert-manager` on every upgrade. Verify with `kubectl get pods -n cert-manager`.
- Do **not** pass `--namespace easylab --create-namespace` while `namespace.create` is
  `true` — the chart would then try to create a namespace that already exists. Either let
  the chart own it, or set `namespace.create=false` and manage it yourself.
- `kubectl` commands for EasyLab itself always use `-n easylab` (or your
  `namespace.name`), whatever the release namespace is.

## Required secrets {#required-secrets}

Two sets of credentials exist and are easy to confuse:

| Values | What they are for |
|--------|-------------------|
| `secrets.ovh.*` | Credentials **EasyLab uses to provision labs** — creating OVH networks, Kubernetes clusters and node pools. Optional; they can also be entered in the admin UI. |
| `dns.ovh.credentials.*` / `dns.azure.*` | Credentials **the chart uses for this release's own hostname** — the DNS-01 solver and ExternalDNS. Unrelated to lab provisioning. |

??? warning "`secrets.dataEncryptionKey` is required — the pod will not start without it"
    Because `config.dataDir` is set by default, persisted job files hold cluster
    kubeconfigs and DNS credentials — so `secrets.dataEncryptionKey` is **required** and
    the pod will not start without it. Use a base64-encoded 32-byte key
    (`openssl rand -base64 32`); the chart puts it in the release Secret as
    `LAB_DATA_ENCRYPTION_KEY`.

    Keep it **stable across upgrades**: a different key makes previously-encrypted
    kubeconfigs unreadable and the affected labs must be recreated. EasyLab deliberately
    does not generate one of its own — a key written into `dataDir` would sit beside the
    data it protects, and on Kubernetes would be lost whenever a pod restarted without a
    persistent volume. If you are upgrading a `dataDir` volume that still holds a
    `.encryption_key` file from an older version, set `secrets.dataEncryptionKey` to that
    file's contents **before** upgrading (see
    [Docker — Environment Variables](docker.md#environment-variables)). Setting a strong
    `PULUMI_CONFIG_PASSPHRASE` is likewise recommended. Provider API credentials are never
    written to lab state.

??? tip "Keeping saved credentials across pod restarts (the key also does this)"
    With `LAB_DATA_ENCRYPTION_KEY` set, cloud provider credentials and DNS profiles saved
    in the admin UI are stored encrypted under it and come back automatically after a pod
    restart (see [Credential storage](admin.md#credential-storage)). The Secret can be fed
    by whatever already manages secrets in your cluster — a plain Kubernetes Secret,
    External Secrets Operator, or an OpenBao / Vault agent injector — with no change to
    EasyLab: it only reads the environment variable. Without the variable, an admin can
    set a passphrase in the UI instead and re-enter it after each restart.

??? note "`X-Forwarded-Proto: https` is trusted by default (safe behind an ingress)"
    The server trusts an incoming `X-Forwarded-Proto: https` header to mark the session
    cookie `Secure` (env var `TRUST_FORWARDED_PROTO`, default `true` — see
    [Docker — Environment Variables](docker.md#environment-variables)). This is safe with
    the standard ingress-terminated-TLS setup, since the ingress controller sets the header
    itself and overwrites any client-supplied value. Only disable it if you expose the pod
    in a way where a client-supplied header could reach it unmodified.

## Values reference

### Why some keys are kebab-case and others camelCase {#key-naming}

The chart's **own** values are camelCase (`certManager`, `nodeSelector`, `replicaCount`,
`dns.externalDns`). The kebab-case top-level keys — `cert-manager`,
`cert-manager-webhook-ovh` — are **subchart names**, and Helm requires a subchart's values
to sit under a key spelled exactly like its dependency. They cannot be camelCased without
aliasing the dependency, which would break every existing values file. `traefik` is a
subchart too; it just happens to be one word.

So the rule is: **kebab-case means "passed through to that upstream chart", camelCase means
"this chart reads it"**. Three pairs are easy to trip over:

| Looks duplicated | Actually | Watch out |
|------------------|----------|-----------|
| `cert-manager.*` / `certManager.*` | Subchart (install cert-manager) vs. this chart (which namespace it runs in, and the ClusterIssuer to create) | `cert-manager.enabled=true` installs it; `certManager.clusterIssuer.create=true` is what actually issues a certificate. Enabling one without the other is the most common mistake |
| `certManager.namespace` / `cert-manager-webhook-ovh.certManager.namespace` | Same namespace, but the second is inside the subchart's own scope, so the parent cannot fill it in | Set both if you move cert-manager off the default `cert-manager` namespace |
| `ingress.className` / `traefik.ingressClass.name` | This chart's Ingress vs. the IngressClass Traefik registers | They must match. Both default to `traefik`, so leave them alone unless you rename one |

One inconsistency is the chart's own: the third OVH credential is
`secrets.ovh.consumerKey` for lab provisioning but
`dns.ovh.credentials.applicationConsumerKey` for DNS-01, because the latter mirrors the
upstream webhook's `applicationConsumerKeyRef` field. Same OVH credential, two names —
both end up as `OVH_CONSUMER_KEY`. See [Required secrets](#required-secrets) for why the
two credential sets are separate in the first place.

### Key values

**Core**

| Parameter | Description | Default |
|-----------|-------------|---------|
| `namespace.create` | Create a dedicated namespace | `true` |
| `namespace.name` | Namespace EasyLab's own resources go in, regardless of `--namespace` | `easylab` |
| `image.repository` | Docker image repository | `docker.io/yodamad/easylab` |
| `image.tag` | Image tag (defaults to `v` + chart appVersion when appVersion has no leading `v`, to match Git-tag Docker images) | `""` |
| `image.pullPolicy` | Image pull policy | `Always` |
| `replicaCount` | Number of replicas — keep at `1`, the PVCs are `ReadWriteOnce` and the Deployment strategy is `Recreate` | `1` |
| `runtime.openFilesLimit` | Process `ulimit -n` before starting the app (helps Pulumi/fsnotify in pods) | `65536` |
| `securityContext.runAsNonRoot` / `.runAsUser` / `.runAsGroup` / `.fsGroup` | Pod security context; `fsGroup` is what makes the PVCs writable | `true` / `1000` / `1000` / `1000` |

**Application config**

| Parameter | Description | Default |
|-----------|-------------|---------|
| `config.port` | Application port | `"8080"` |
| `config.workDir` | Job workspace directory | `"/app/jobs"` |
| `config.dataDir` | Data persistence directory | `"/app/data"` |
| `config.mode` | Areas served: `all` or `admin` — see [In-lab student portals](#in-lab-student-portals) | `""` (`all`) |
| `config.publicUrl` | This release's address as students reach it, used by in-lab student portals for Microsoft / GitHub / GitLab sign-in | `""` (the ingress host when `ingress.enabled`) |
| `config.portalImage` | Image the in-lab student portals run | `""` (this release's own image) |

**Secrets**

| Parameter | Description | Default |
|-----------|-------------|---------|
| `secrets.create` | Create a Kubernetes secret | `true` |
| `secrets.adminPassword` | Admin login password | `""` |
| `secrets.studentPassword` | Student login password for this instance's own student area (each in-lab portal has its own) | `""` |
| `secrets.dataEncryptionKey` | **Required** with `config.dataDir` set (the default): base64-encoded 32-byte key encrypting kubeconfigs and DNS credentials in persisted job files | `""` |
| `secrets.ovh.applicationKey` / `.applicationSecret` / `.consumerKey` / `.serviceName` | OVH credentials EasyLab uses to **provision labs** | `""` |
| `secrets.ovh.endpoint` | OVH API endpoint | `"ovh-eu"` |

**Storage, service and scheduling**

| Parameter | Description | Default |
|-----------|-------------|---------|
| `persistence.jobs.size` / `.storageClass` | PVC for job state and Pulumi stacks; grows with lab count | `1Gi` / `""` |
| `persistence.data.size` / `.storageClass` | PVC for the credential vault, feedback and job history | `200Mi` / `""` |
| `service.type` / `.port` / `.annotations` | Kubernetes Service | `ClusterIP` / `80` / `{}` |
| `resources.requests.memory` / `.cpu` | Resource requests | `1024Mi` / `500m` |
| `resources.limits.memory` / `.cpu` | Resource limits | `4096Mi` / `3000m` |
| `nodeSelector` | Pin the EasyLab server pod to nodes matching these labels (e.g. a dedicated node pool). Labs' [image pre-pull](admin-lab-management.md#image-pre-pull) leaves those nodes alone | `{}` |
| `tolerations` | Tolerations for the EasyLab server pod, needed if its target node pool is tainted | `[]` |

**Ingress**

| Parameter | Description | Default |
|-----------|-------------|---------|
| `ingress.enabled` | Enable ingress | `false` |
| `ingress.className` | Ingress class name | `traefik` |
| `ingress.annotations` | Ingress annotations — yours **replace** the chart's on conflict | `{}` |
| `ingress.host` | Ingress hostname | `easylab.example.com` |
| `ingress.tls.enabled` / `.secretName` | Enable TLS, and the secret holding the certificate | `false` / `easylab-tls` |
| `ingress.httpsRedirect.enabled` | Redirect HTTP to HTTPS with a Traefik `redirectScheme` Middleware — created only when `ingress.className` is `traefik` and `ingress.tls.enabled=true`, see [Redirecting HTTP to HTTPS](#redirecting-http-to-https) | `true` |
| `ingress.httpsRedirect.permanent` | `true` issues a 301 (remembered by browsers), `false` a 302 | `true` |
| `ingress.httpsRedirect.apiVersion` | Middleware CRD API group — Traefik v3 uses `traefik.io/v1alpha1`, Traefik v2 `traefik.containo.us/v1alpha1` | `traefik.io/v1alpha1` |

### Bundled components and certificates

| Parameter | Description | Default |
|-----------|-------------|---------|
| `traefik.enabled` | Install Traefik as part of this chart (IngressClass name pinned to `traefik`, matching `ingress.className`'s default) | `false` |
| `cert-manager.enabled` | Install cert-manager as part of this chart | `false` |
| `cert-manager.crds.enabled` | Install cert-manager CRDs (required on first install) | `true` |
| `certManager.namespace` | Namespace cert-manager's controller runs in — must match wherever it is actually installed, see [Namespaces](#namespaces) | `cert-manager` |
| `certManager.clusterIssuer.create` | Create a Let's Encrypt ClusterIssuer for the EasyLab ingress — HTTP-01, or DNS-01 if `dns.azure.enabled` / `dns.ovh.enabled`. Required for all three solvers | `false` |
| `certManager.clusterIssuer.name` | ClusterIssuer name | `letsencrypt` |
| `certManager.clusterIssuer.email` | ACME account email | `""` |
| `certManager.clusterIssuer.server` | ACME server URL | `https://acme-v02.api.letsencrypt.org/directory` |

Any `traefik.*` or `cert-manager.*` value is passed straight to those subcharts — see the
[Traefik chart values](https://github.com/traefik/traefik-helm-chart) and
[cert-manager chart values](https://cert-manager.io/docs/installation/helm/). That covers
scheduling too: `traefik.nodeSelector` / `.tolerations` and `cert-manager.nodeSelector` /
`.tolerations` pin those components the same way `nodeSelector` / `tolerations` pin
EasyLab itself ([recipe 6](helm-recipes.md#recipe-6)).

### DNS and certificate automation

| Parameter | Description | Default |
|-----------|-------------|---------|
| `dns.azure.enabled` | Use Azure DNS-01 (cert-manager's built-in `azureDNS` solver) instead of HTTP-01 | `false` |
| `dns.azure.zone` | Azure DNS hosted zone name | `""` |
| `dns.azure.tenantId` / `.subscriptionId` / `.resourceGroup` / `.clientId` / `.clientSecret` | Azure service principal credentials | `""` |
| `dns.ovh.enabled` | Install the `cert-manager-webhook-ovh` DNS-01 solver and use it for `certManager.clusterIssuer` | `false` |
| `dns.ovh.zone` | OVH DNS zone `ingress.host` is a subdomain of, e.g. `example.com` — only needed when `dns.externalDns.enabled=true` | `""` |
| `dns.ovh.endpoint` | OVH API endpoint name: `ovh-eu`, `ovh-ca`, `kimsufi-eu`, `kimsufi-ca`, `soyoustart-eu`, `soyoustart-ca`, `runabove-ca` | `ovh-eu` |
| `dns.ovh.credentials.applicationKey` / `.applicationSecret` / `.applicationConsumerKey` | OVH API credentials for the DNS-01 solver and ExternalDNS, from [api.ovh.com/createToken](https://api.ovh.com/createToken/) with `GET`/`POST`/`PUT`/`DELETE` on `/domain/zone/*`. The chart stores them in its own Secret and points the ClusterIssuer at it | `""` |
| `cert-manager-webhook-ovh.*` | Raw values for the vendored OVH webhook chart — only the solver webhook itself (`groupName`, `certManager.namespace`, `certManager.serviceAccountName`). Its `issuers` list is deliberately empty and must stay empty: this chart owns the issuer. See [upstream chart values](https://github.com/aureq/cert-manager-webhook-ovh) | see `values.yaml` |
| `dns.externalDns.enabled` | Run a persistent ExternalDNS controller that keeps `ingress.host`'s A record pointed at the ingress controller's LoadBalancer IP | `false` |
| `dns.externalDns.image.repository` / `.tag` | ExternalDNS container image | `registry.k8s.io/external-dns/external-dns` / `v0.22.0` |
| `dns.externalDns.policy` | `sync` also deletes the record if it is no longer needed; `upsert-only` never deletes | `sync` |
| `dns.externalDns.txtOwnerId` | Scopes ExternalDNS's TXT ownership registry so this install never touches records owned by another ExternalDNS instance sharing the zone; empty defaults to the release fullname | `""` |
| `dns.externalDns.dryRun` | Log planned DNS changes without calling the provider API — recommended `true` for a first rollout | `false` |
| `dns.externalDns.resources` | Resource requests/limits for the ExternalDNS pod | see `values.yaml` |

The chart **fails the render** rather than installing something that silently never
issues a certificate. You will be told, with a message naming the value to fix, if you
enable both DNS providers at once, enable `dns.externalDns` without one, omit
`dns.ovh.zone` or the OVH credentials where they are needed, set an
`ingress.annotations."cert-manager.io/cluster-issuer"` that disagrees with
`certManager.clusterIssuer.name`, or still carry a legacy
`cert-manager-webhook-ovh.issuers[]` block.

## Exposing EasyLab

The chart creates a standard **Kubernetes Ingress** and defaults `ingress.className` to
`traefik`, matching Traefik's default IngressClass on many clusters (for example
[k3s](https://docs.k3s.io/networking#traefik-ingress-controller) and typical
[Traefik Helm](https://doc.traefik.io/traefik/getting-started/install-traefik/) installs).
Check what your cluster has with `kubectl get ingressclass` and set `ingress.className` to
match, or set `traefik.enabled=true` to have the chart install Traefik for you.

If your Traefik install uses non-default entrypoint names, add them yourself:

```yaml
ingress:
  annotations:
    traefik.ingress.kubernetes.io/router.entrypoints: web,websecure
```

Without `ingress.enabled`, reach the pod with
`kubectl port-forward -n easylab svc/easylab-service 8080:80`.

### Redirecting HTTP to HTTPS {#redirecting-http-to-https}

When `ingress.className` is `traefik` and `ingress.tls.enabled=true`, the chart creates a
Traefik `redirectScheme` **Middleware** and references it from the Ingress automatically —
nothing to set. It renders a `<release>-https-redirect` Middleware plus this annotation:

```yaml
traefik.ingress.kubernetes.io/router.middlewares: easylab-easylab-https-redirect@kubernetescrd
```

??? warning "Serving EasyLab over plain HTTP breaks login entirely — not just cosmetics"
    This is not cosmetic. The login page hashes the password with
    [`crypto.subtle`](https://developer.mozilla.org/en-US/docs/Web/API/Crypto/subtle)
    (Web Crypto), which browsers expose **only in a secure context**. Over `http://`,
    `crypto.subtle` is `undefined` and the login form fails with
    `TypeError: Cannot read properties of undefined (reading 'digest')` in the console,
    showing only a generic "An error occurred. Please try again." to the user. If port 80
    answers without redirecting, any client that reaches the `http://` URL — from a
    bookmark, a history entry or a typed hostname — cannot log in at all. The default
    `permanent: true` (301) means the browser remembers the redirect, which is what
    repairs a client already stuck on such a URL.

The Middleware is deliberately **not** created when `ingress.tls.enabled=false`, since
redirecting to HTTPS with no certificate configured only breaks the site a different way.
Turn it off with `ingress.httpsRedirect.enabled=false`.

If you set `traefik.ingress.kubernetes.io/router.middlewares` yourself under
`ingress.annotations`, your value **replaces** the chart's rather than adding to it —
include `<namespace>-<release>-https-redirect@kubernetescrd` in your own comma-separated
chain to keep the redirect.

**Other ingress controllers** express this as their own annotation instead; the chart does
not manage it for them. For NGINX, `nginx.ingress.kubernetes.io/ssl-redirect: "true"`
([recipe 1](helm-recipes.md#recipe-1)).

### Certificates for the EasyLab ingress {#certificates-for-the-easylab-ingress}

`cert-manager.enabled=true` only installs cert-manager — nothing issues a certificate
until an issuer exists. `certManager.clusterIssuer.create=true` closes that gap by having
the chart create the `ClusterIssuer` for the EasyLab ingress, and annotate the Ingress
with it. Do not add a `cert-manager.io/cluster-issuer` annotation of your own alongside
it: yours wins on merge, so a stale one would point cert-manager at an issuer that does
not exist (the chart refuses to render if the two disagree).

All three solvers go through that one ClusterIssuer, so `create=true` is required for any
of them, OVH included. What is mutually exclusive is the DNS provider — enable at most one
of `dns.azure.enabled` / `dns.ovh.enabled`.

| Solver | Needs | Wildcard certs | Recipe |
|--------|-------|----------------|--------|
| HTTP-01 (default) | `ingress.host` resolving and answering on port 80 | No | [2](helm-recipes.md#recipe-2) |
| OVH DNS-01 | three OVH API credentials; installs the solver webhook | Yes | [3](helm-recipes.md#recipe-3) |
| Azure DNS-01 | an Azure service principal | Yes | [4](helm-recipes.md#recipe-4) |

The issuer is created by a `post-install,post-upgrade` hook weighted to run after
cert-manager's own `startupapicheck`, so it is not applied until cert-manager's webhook
answers. One consequence: if `startupapicheck` fails, the release fails without creating
the issuer, rather than creating an issuer nothing can use.

Check status with:

```bash
kubectl describe clusterissuer letsencrypt        # or your certManager.clusterIssuer.name
kubectl describe certificate -n easylab easylab-tls
kubectl get challenge -A
```

A `Ready` ClusterIssuer is **not** proof the solver works — a webhook solver is only
called once a certificate is actually requested. With OVH, check the whole path:

```bash
kubectl get apiservice v1alpha1.acme.easylab.local
kubectl logs -n <release-namespace> deploy/easylab-cert-manager-webhook-ovh
```

### Automatic DNS record for the EasyLab ingress (ExternalDNS) {#automatic-dns-record-for-the-easylab-ingress-externaldns}

Everything above gets you a **certificate** for `ingress.host` — it does not make the
hostname resolve. DNS-01 challenges only prove domain ownership through an ephemeral ACME
TXT record that cert-manager deletes again once validated; the actual **A record**
pointing `ingress.host` at the ingress controller's LoadBalancer IP has always been a
manual step.

`dns.externalDns.enabled=true` automates it: a persistent
[ExternalDNS](https://github.com/kubernetes-sigs/external-dns) controller watches the
EasyLab Ingress and keeps its A record in sync with the ingress controller's current
LoadBalancer IP, including when that IP changes. It requires exactly one of
`dns.ovh.enabled` / `dns.azure.enabled`, since it reuses those credentials — for OVH, add
`dns.ovh.zone` (the zone, not the ingress host); for Azure, `dns.azure.zone` is already
required by the solver and is reused as-is. Both are shown in
[recipe 3](helm-recipes.md#recipe-3) and [recipe 4](helm-recipes.md#recipe-4).

**Dry-run first.** With `dns.externalDns.dryRun=true` ExternalDNS logs the changes it
would make without calling the provider API. Confirm it found the right record, then set
`dryRun: false` and `helm upgrade` again:

```bash
kubectl get deploy,pods -n easylab -l app.kubernetes.io/component=externaldns
kubectl logs -n easylab -l app.kubernetes.io/component=externaldns
dig +short easylab.example.com
```

??? note "A wildcard `ingress.host` works, but needs DNS-01 — never HTTP-01"
    A wildcard host (e.g. `*.easylab.example.com`) works with ExternalDNS — both OVH and
    Azure DNS accept a `*` subdomain prefix for an A record. The constraint is TLS: Let's
    Encrypt only issues wildcard certificates via DNS-01, never HTTP-01, so a wildcard
    `ingress.host` needs `dns.ovh.enabled` or `dns.azure.enabled` (which
    `dns.externalDns.enabled` requires anyway) and will not work with the HTTP-01 path.

## In-lab student portals

A lab can have its own [student portal](student-portal.md), deployed by EasyLab inside the
lab's cluster. Three values control how this release takes part — see
[recipe 5](helm-recipes.md#recipe-5) for the overlay:

- `config.mode` — `admin` serves the admin space only, so students use each lab's portal;
  empty (`all`) also keeps the central student space.
- `config.publicUrl` — where portals send students for Microsoft / GitHub / GitLab
  sign-in. Defaults to the ingress host when `ingress.enabled` is true.
- `config.portalImage` — defaults to this release's own image. The lab's nodes must be
  able to pull it.

`secrets.studentPassword` is the password for the **admin instance's own** student area.
Each in-lab portal has its own, generated when it is deployed and shown on the lab's page,
which the portal checks locally against a hash EasyLab copies to it — so a password handed
to one lab's students opens that lab and no other.

## Pinning to a dedicated node pool

To keep the EasyLab server off the node pool student workspaces run on (see
[Splitting EasyLab and workspaces across node pools](admin-lab-creation.md#splitting-easylab-and-workspaces-across-node-pools)),
label a "control-plane" pool and target it with `nodeSelector`, plus a matching toleration
if the pool is tainted — [recipe 6](helm-recipes.md#recipe-6) has the values.

Setting `nodeSelector` also tells the server where *not* to cache workspace images: the
chart passes those labels to it as `EASYLAB_SERVER_NODE_SELECTOR`, and every lab's
[image pre-pull](admin-lab-management.md#image-pre-pull) then skips the pool the server
runs on, which never runs a workspace. Nothing else to configure — and it only applies to
labs on this same cluster, since the labels are matched against the lab cluster's nodes.

## Upgrade

```bash
helm upgrade easylab oci://registry-1.docker.io/yodamad/easylab-helm \
  --version __VERSION__ \
  --namespace <same as install> \
  -f my-values.yaml
```

`helm upgrade` does **not** inherit the previous release's values — always pass the same
`-f` files, and the same `--namespace`. Keep `secrets.dataEncryptionKey` identical across
upgrades.

??? warning "cert-manager CRDs are not updated by `helm upgrade`"
    `cert-manager.crds.enabled=true` installs the cert-manager CRDs on first install, but
    Helm does not update CRDs on upgrade — a general Helm/cert-manager limitation, not
    specific to this chart. Bumping the vendored cert-manager version may require applying
    the new CRDs by hand (see
    [cert-manager's CRD upgrade docs](https://cert-manager.io/docs/installation/upgrading/))
    before `helm upgrade` picks up the new version.

??? warning "Migrating from a values file with a `cert-manager-webhook-ovh.issuers[]` block"
    OVH DNS-01 used to be configured through that nested block, which created its own
    `ClusterIssuer` named `letsencrypt-ovh`. It is gone: filling it in now fails the render
    with a message pointing at `dns.ovh.credentials`. Two things to know before upgrading:

    - **The issuer is renamed** to `certManager.clusterIssuer.name` (default
      `letsencrypt`). On upgrade Helm deletes the old `letsencrypt-ovh` ClusterIssuer and
      its `letsencrypt-ovh-ovh-credentials` Secret — they were release resources — and the
      hook creates the new one afterwards, so there is a short window with no OVH issuer.
      EasyLab's own ingress is re-annotated for you, but **any other `Ingress` or
      `Certificate` still annotated `cert-manager.io/cluster-issuer: letsencrypt-ovh` stops
      renewing** (existing certificates keep serving until they expire). To avoid touching
      them, set `certManager.clusterIssuer.name: letsencrypt-ovh` and keep the old name.
    - **A new ACME account is registered** when the name changes, because
      `privateKeySecretRef` follows the issuer name. Harmless in itself, but it counts
      against Let's Encrypt's new-account rate limits, so do not rename back and forth.

## Uninstall

```bash
helm uninstall easylab --namespace <same as install> --cascade foreground --wait --timeout 5m
```

`--cascade foreground --wait` is worth the extra minute whenever `dns.ovh.enabled=true`:
it makes Helm finish deleting each resource before starting the next instead of firing
every delete at once, which avoids the stuck-namespace case below.

Three things `helm uninstall` leaves behind:

```bash
# 1. PersistentVolumeClaims (they hold your lab state — delete deliberately)
kubectl delete pvc -n easylab -l app.kubernetes.io/name=easylab

# 2. The ClusterIssuer: created by a Helm hook, so it is not part of the release
#    manifest and neither uninstall nor rollback removes it
kubectl delete clusterissuer letsencrypt        # or your certManager.clusterIssuer.name

# 3. With dns.ovh.enabled, the webhook's APIService — see the warning below
kubectl get apiservices | grep -v 'True'
```

A left-behind ClusterIssuer with `dns.ovh.enabled=true` points at a solver webhook that no
longer exists, and any `Certificate` still referencing it waits forever.

??? warning "Namespace stuck in `Terminating` after uninstall (`dns.ovh.enabled=true`)"
    With `namespace.create=true` (the default) the namespace is itself a release resource,
    so `helm uninstall` deletes it — and the `cert-manager-webhook-ovh` subchart registers
    an **aggregated APIService** (`v1alpha1.<groupName>`, default
    `v1alpha1.acme.easylab.local`) whose backing Service lives *inside that same namespace*.

    If the webhook's pod and Service go away while the APIService object still exists, the
    API aggregation layer reports that group unavailable. Namespace finalization needs a
    complete API discovery pass to enumerate what is left to delete, so the failing group
    wedges the namespace in `Terminating` indefinitely. It blocks **every** namespace
    deletion on the cluster for as long as the stale APIService is there, not only
    EasyLab's.

    Confirm and recover:
    ```bash
    kubectl get ns easylab -o json | jq '.status.conditions[] | select(.status=="True")'
    kubectl get apiservices | grep -v 'True'
    kubectl delete apiservice v1alpha1.acme.easylab.local
    ```
    A `NamespaceDeletionDiscoveryFailure` condition is this problem; the namespace
    finalizes within seconds of deleting the APIService. A
    `NamespaceDeletionContentFailure` is a different one — leftover objects holding
    finalizers, which you can find with:
    ```bash
    kubectl api-resources --verbs=list --namespaced -o name \
      | xargs -n1 kubectl get -n easylab --ignore-not-found --no-headers 2>/dev/null
    ```

    Do **not** clear the namespace's own finalizers
    (`kubectl patch ns easylab -p '{"metadata":{"finalizers":[]}}' --type=merge`). That
    drops the namespace record while its contents still exist, orphaning resources that
    are then very hard to track down.

    Avoid it next time with `--cascade foreground --wait` as above, or by setting
    `namespace.create=false` and managing the namespace yourself, so `helm uninstall`
    never tries to delete it.

## Generate raw Kubernetes manifests

To deploy with plain `kubectl` instead of Helm, render the chart with `helm template`. The
same `--set` flags and `-f` files work identically; only the destination changes.

```bash
helm template easylab oci://registry-1.docker.io/yodamad/easylab-helm \
  --version __VERSION__ \
  -f my-values.yaml \
  > easylab-manifests.yaml

kubectl apply -f easylab-manifests.yaml
```

??? note "The ClusterIssuer loses its ordering guarantee under `kubectl apply`"
    `helm template` does render the `ClusterIssuer` (pass `--no-hooks` to leave it out),
    but its `helm.sh/hook` annotations mean nothing to `kubectl`. Applied in one pass, it
    is created at the same time as cert-manager instead of after its webhook is up, so it
    may fail; re-apply once cert-manager is running.

## Troubleshooting

??? warning "`exec /app/main: exec format error`"
    The image's architecture does not match your nodes (for example an **arm64** image on
    **amd64** workers). Published tags are multi-arch, so this only happens with a
    **custom** image built on Apple Silicon with plain `docker build` and no platform flag.

    Build and push with an explicit platform matching your cluster (most cloud clusters are
    `linux/amd64`):
    ```bash
    docker buildx build --platform linux/amd64 -t your-registry/easylab:your-tag --push .
    ```
    For arm64 nodes (AWS Graviton, for example) use `--platform linux/arm64`, or publish
    both in one tag like CI does:
    ```bash
    docker buildx build --platform linux/amd64,linux/arm64 -t your-registry/easylab:your-tag --push .
    ```
    `--load` only supports a single platform; multi-arch builds must be pushed to a
    registry.

??? warning "Fresh cluster with `dns.ovh.enabled=true`: `no matches for kind Certificate`"
    Only applies to a bare cluster where this chart installs cert-manager too. If
    cert-manager is **already** there, a single `helm install` with `dns.ovh.enabled=true`
    works fine.

    Combining `cert-manager.enabled=true` and `dns.ovh.enabled=true` in one `helm install`
    on a cluster with no cert-manager CRDs yet fails with:
    ```
    Error: INSTALLATION FAILED: unable to build kubernetes objects from release manifest:
    [resource mapping not found for name: "easylab-cert-manager-webhook-ovh-ca" namespace: "" from "":
    no matches for kind "Certificate" in version "cert-manager.io/v1"
    ensure CRDs are installed first, ...]
    ```
    This is a general Helm/cert-manager limitation, not bad values: Helm resolves every
    kind in the *entire* combined manifest — EasyLab, cert-manager and the
    `cert-manager-webhook-ovh` subchart — against the cluster's API discovery before
    applying anything. The webhook subchart's own PKI (a self-signed `Issuer`, a CA
    `Certificate` and its serving `Certificate`) is made of `cert-manager.io/v1` kinds and
    is **not** optional, so the whole install aborts before creating anything, cert-manager
    included. Deferring the EasyLab ClusterIssuer to a hook, which this chart does, is not
    enough to avoid it.

    The fix is the two-phase command in [recipe 3](helm-recipes.md#recipe-3): install with the webhook
    disabled so the CRDs land, then upgrade with the same values file now that they are
    registered. Confirm between phases with `kubectl get crd | grep cert-manager.io`.

??? warning "The certificate never issues"
    Walk the chain outwards, in this order:
    ```bash
    kubectl get certificate -n easylab                 # Ready?
    kubectl describe certificaterequest -n easylab
    kubectl get challenge -A                           # pending means the solver is stuck
    kubectl describe clusterissuer letsencrypt
    kubectl get pods -n cert-manager                   # does certManager.namespace match?
    ```
    The usual causes: `certManager.namespace` not matching where cert-manager actually runs
    ([Namespaces](#namespaces)); an `ingress.annotations` issuer name that disagrees with
    the issuer the chart created; HTTP-01 on a host that does not resolve or is not
    reachable on port 80; or a wildcard host on the HTTP-01 path, which Let's Encrypt never
    issues.

??? warning "ExternalDNS logs no changes, or creates nothing"
    Almost always `dns.ovh.zone`: it must be the **zone** (`example.com`), not the ingress
    host (`easylab.example.com`). Pass the hostname and every zone lookup comes back empty
    and ExternalDNS silently creates no record. Also check `dryRun` is off once you are
    past verification, and that the ingress controller's Service actually has an external
    IP:
    ```bash
    kubectl logs -n easylab -l app.kubernetes.io/component=externaldns
    kubectl get svc -A -o wide | grep LoadBalancer
    ```

??? warning "Login fails with a generic 'An error occurred' message"
    The browser reached EasyLab over plain `http://`, where `crypto.subtle` is unavailable.
    See [Redirecting HTTP to HTTPS](#redirecting-http-to-https) — the browser console shows
    `Cannot read properties of undefined (reading 'digest')`.

??? warning "Pod stuck in `CrashLoopBackOff` or never becomes ready"
    ```bash
    kubectl logs -n easylab deploy/easylab
    kubectl describe pod -n easylab -l app.kubernetes.io/name=easylab
    kubectl get pvc -n easylab
    ```
    Most common: `secrets.dataEncryptionKey` missing (the server refuses to start — see
    [Required secrets](#required-secrets)), or a PVC `Pending` because no `ReadWriteOnce`
    StorageClass is set as the cluster default.
