---
icon: lucide/clipboard-list
title: Helm recipes
---

# Helm Values Recipes

Complete, copy-pasteable values files for the situations people actually hit. Pick the one
matching your cluster, replace the `CHANGE_ME` placeholders, install. For what any
individual value means, see [Helm Chart Deployment](helm.md#key-values); for why a
certificate or DNS record behaves the way it does, [TLS and DNS](helm.md#exposing-easylab).

| Your situation | Recipe |
|----------------|--------|
| Cluster already has an ingress controller and cert-manager | [1 — Bring your own ingress and issuer](#recipe-1) |
| Bare cluster, hostname's DNS record created by hand, port 80 reachable | [2 — Chart installs Traefik + cert-manager (HTTP-01)](#recipe-2) |
| OVH DNS zone, want the certificate *and* the A record handled for you | [3 — OVH DNS-01 + ExternalDNS](#recipe-3) |
| AKS / Azure DNS zone | [4 — Azure DNS-01 + ExternalDNS](#recipe-4) |
| Admin console only; students use each lab's own portal | [5 — Admin-only release](#recipe-5) |
| Production: dedicated node pool, real storage class, sizing | [6 — Production overlay](#recipe-6) |

Recipes 5 and 6 are **overlays**: layer them on top of 1–4 by passing both files
(`-f values-ovh.yaml -f values-prod.yaml`), later files winning.

Every recipe needs the secrets from [Required secrets](helm.md#required-secrets); they are
spelled out in recipe 1 and abbreviated afterwards.

## Recipe 1 — Bring your own ingress and issuer {#recipe-1}

Nothing is installed but EasyLab. You own the ingress controller, cert-manager and the
`ClusterIssuer`, so you supply the `cert-manager.io/cluster-issuer` annotation yourself
(the chart only adds it when it creates the issuer). NGINX shown; for Traefik drop the
`ssl-redirect` annotation — the chart handles the redirect itself, see
[Redirecting HTTP to HTTPS](helm.md#redirecting-http-to-https).

```yaml
# values-existing.yaml
secrets:
  adminPassword: "CHANGE_ME"
  # openssl rand -base64 32
  dataEncryptionKey: "CHANGE_ME_BASE64_32_BYTES"

ingress:
  enabled: true
  # kubectl get ingressclass
  className: nginx
  host: easylab.example.com
  tls:
    enabled: true
    secretName: easylab-tls
  annotations:
    cert-manager.io/cluster-issuer: letsencrypt   # your existing issuer
    nginx.ingress.kubernetes.io/ssl-redirect: "true"

persistence:
  jobs:
    size: 5Gi
    storageClass: ""      # "" = cluster default
  data:
    size: 1Gi
    storageClass: ""
```

```bash
helm install easylab oci://registry-1.docker.io/yodamad/easylab-helm \
  --version __VERSION__ \
  -f values-existing.yaml
```

## Recipe 2 — Chart installs Traefik + cert-manager (HTTP-01) {#recipe-2}

For a cluster with neither. HTTP-01 needs no DNS credentials, but `ingress.host` must
already resolve to the ingress controller's LoadBalancer IP and answer on port 80 for the
ACME challenge — so this one still has a manual DNS step. It cannot issue a **wildcard**
certificate; Let's Encrypt only does those over DNS-01 ([recipe 3](#recipe-3) or
[4](#recipe-4)).

```yaml
# values-bare-http01.yaml
secrets:
  adminPassword: "CHANGE_ME"
  dataEncryptionKey: "CHANGE_ME_BASE64_32_BYTES"

traefik:
  enabled: true
cert-manager:
  enabled: true
  crds:
    enabled: true

certManager:
  # must match where cert-manager's pods actually run
  namespace: cert-manager
  clusterIssuer:
    create: true
    email: "you@example.com"
    # while testing, avoid production rate limits:
    # server: https://acme-staging-v02.api.letsencrypt.org/directory

ingress:
  enabled: true
  className: traefik      # matches the IngressClass traefik.enabled installs
  host: easylab.example.com
  tls:
    enabled: true
```

```bash
helm install easylab oci://registry-1.docker.io/yodamad/easylab-helm \
  --version __VERSION__ \
  --namespace cert-manager --create-namespace \
  -f values-bare-http01.yaml \
  --wait --timeout 5m
```

`--wait --timeout 5m` is not optional here: the `ClusterIssuer` is created by a
`post-install` hook, and without `--wait` it can fire before cert-manager's admission
webhook is up and fail. `--namespace cert-manager --create-namespace` is what makes
`certManager.namespace` line up — see [Namespaces](helm.md#namespaces).

## Recipe 3 — OVH DNS-01 + ExternalDNS {#recipe-3}

The fully automated path on an OVH DNS zone: DNS-01 gets the certificate (wildcards
included, no port-80 requirement) and ExternalDNS creates and maintains the A record, so
no manual DNS step remains. Both use the same three OVH API credentials, created at
[api.ovh.com/createToken](https://api.ovh.com/createToken/) with `GET`, `POST`, `PUT` and
`DELETE` on `/domain/zone/*`.

```yaml
# values-ovh.yaml
secrets:
  adminPassword: "CHANGE_ME"
  dataEncryptionKey: "CHANGE_ME_BASE64_32_BYTES"

traefik:
  enabled: true
cert-manager:
  enabled: true
  crds:
    enabled: true

certManager:
  namespace: cert-manager
  clusterIssuer:
    create: true
    email: "you@example.com"

dns:
  ovh:
    enabled: true
    # the zone itself, NOT the ingress host — ExternalDNS filters on it
    zone: "example.com"
    endpoint: ovh-eu
    credentials:
      applicationKey: "CHANGE_ME"
      applicationSecret: "CHANGE_ME"
      applicationConsumerKey: "CHANGE_ME"
  externalDns:
    enabled: true
    # log the intended changes without calling the OVH API; flip to false once
    # the logs show the record you expect
    dryRun: true

ingress:
  enabled: true
  className: traefik
  host: easylab.example.com    # or *.easylab.example.com for a wildcard
  tls:
    enabled: true
```

On a cluster that **already has cert-manager**, one command is enough. On a bare cluster
it must be split in two, because the OVH webhook subchart's own PKI is made of
`cert-manager.io/v1` objects that cannot be validated before the CRDs exist (the
[Troubleshooting](helm.md#troubleshooting) entry on missing cert-manager CRDs has the
details):

```bash
helm install easylab oci://registry-1.docker.io/yodamad/easylab-helm \
  --version __VERSION__ \
  --namespace cert-manager --create-namespace \
  -f values-ovh.yaml \
  --set dns.ovh.enabled=false --set dns.externalDns.enabled=false \
  --wait --timeout 5m \
&& helm upgrade easylab oci://registry-1.docker.io/yodamad/easylab-helm \
  --version __VERSION__ \
  --namespace cert-manager \
  -f values-ovh.yaml \
  --wait --timeout 5m
```

Pass `-f` to **both** phases — `helm upgrade` does not inherit the previous release's
values, so a bare `--set dns.ovh.enabled=true` would fall back to chart defaults and fail
on the missing credentials. Phase one turns `dns.externalDns.enabled` off as well: it
needs a DNS provider enabled, and the chart refuses to render otherwise.

Then verify, and switch `dryRun` off:

```bash
kubectl logs -n easylab -l app.kubernetes.io/component=externaldns
kubectl describe clusterissuer letsencrypt
kubectl get challenge -A
dig +short easylab.example.com
```

Only OVH's `application` authentication method is supported. OAuth2 credentials, external
account bindings and per-issuer ACME servers are not exposed; use the
[upstream chart](https://github.com/aureq/cert-manager-webhook-ovh) as its own release if
you need them.

## Recipe 4 — Azure DNS-01 + ExternalDNS {#recipe-4}

Same shape as recipe 3 on an Azure DNS zone, using cert-manager's built-in `azureDNS`
solver — no extra webhook, so no two-phase install. One service principal covers both the
solver and ExternalDNS; it needs permission to manage records in the zone, which the
built-in **DNS Zone Contributor** role on the zone's resource group grants. This is a
separate concern from the subscription-scoped credentials EasyLab uses to provision labs
(see [Azure cloud provider](azure.md)).

```yaml
# values-azure.yaml
secrets:
  adminPassword: "CHANGE_ME"
  dataEncryptionKey: "CHANGE_ME_BASE64_32_BYTES"

traefik:
  enabled: true
cert-manager:
  enabled: true
  crds:
    enabled: true

certManager:
  namespace: cert-manager
  clusterIssuer:
    create: true
    email: "you@example.com"

dns:
  azure:
    enabled: true
    zone: "example.com"             # Azure DNS hosted zone name
    tenantId: "CHANGE_ME"
    subscriptionId: "CHANGE_ME"
    resourceGroup: "CHANGE_ME"      # resource group holding the zone
    clientId: "CHANGE_ME"
    clientSecret: "CHANGE_ME"
  externalDns:
    enabled: true
    dryRun: true

ingress:
  enabled: true
  className: traefik
  host: easylab.example.com
  tls:
    enabled: true
```

```bash
helm install easylab oci://registry-1.docker.io/yodamad/easylab-helm \
  --version __VERSION__ \
  --namespace cert-manager --create-namespace \
  -f values-azure.yaml \
  --wait --timeout 5m
```

## Recipe 5 — Admin-only release {#recipe-5}

An overlay for the common workshop setup: this release serves the **admin console only**,
and each lab gets its [own student portal](student-portal.md) deployed inside the lab's
cluster. See [In-lab student portals](helm.md#in-lab-student-portals) for what each value
does.

```yaml
# values-admin-only.yaml
config:
  mode: admin
  # where portals send students for Microsoft / GitHub / GitLab sign-in;
  # defaults to the ingress host, so this is only needed if they differ
  publicUrl: "https://easylab.example.com"
  # defaults to this release's own image; the lab's nodes must be able to pull it
  portalImage: ""

secrets:
  # not needed with mode: admin — each portal generates its own student password
  studentPassword: ""
```

## Recipe 6 — Production overlay {#recipe-6}

An overlay with the settings that matter once a release is more than a demo: a dedicated
node pool, a named storage class, and sizing. See
[Pinning to a dedicated node pool](helm.md#pinning-to-a-dedicated-node-pool) for why the
node labels also change how labs pre-pull images.

```yaml
# values-prod.yaml
# keep EasyLab off the pool student workspaces run on
nodeSelector:
  pool: control-plane
tolerations:
  - key: dedicated
    operator: Equal
    value: easylab
    effect: NoSchedule

persistence:
  jobs:
    size: 20Gi          # Pulumi state and job logs grow with lab count
    # name one explicitly rather than relying on the cluster default, and pick a
    # network-attached provisioner — `kubectl get storageclass`
    storageClass: CHANGE_ME
  data:
    size: 2Gi
    storageClass: CHANGE_ME

resources:
  requests:
    memory: "1024Mi"
    cpu: "500m"
  limits:
    memory: "4096Mi"
    cpu: "3000m"

image:
  # pin the image instead of tracking the chart's appVersion. Note the leading
  # "v": app image tags carry it, chart versions do not.
  tag: "v__VERSION__"
  pullPolicy: IfNotPresent

# pin the chart's subcharts to the same pool
traefik:
  nodeSelector:
    pool: control-plane
cert-manager:
  nodeSelector:
    pool: control-plane
```

## Next steps

- [Namespaces](helm.md#namespaces) — which namespace each resource lands in, and the
  `--namespace` trap that stops certificates issuing.
- [Values reference](helm.md#key-values) — every value the chart accepts.
- [Upgrade](helm.md#upgrade) and [Uninstall](helm.md#uninstall) — including what
  `helm uninstall` leaves behind.
- [Troubleshooting](helm.md#troubleshooting) — symptom-first, for when one of these
  recipes does not behave.
