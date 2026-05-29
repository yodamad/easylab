# EasyLab — Claude Code Rules

## Project Overview

EasyLab is a Go web application that provisions cloud-based lab environments using Pulumi IaC on OVHcloud. It deploys Kubernetes clusters with Coder workspaces for workshop participants.

Two main entry points:
1. A Pulumi program (`main.go`) for infrastructure provisioning
2. A web server (`cmd/server/main.go`) with an HTMX-powered admin/student UI

### Tech Stack

- Go 1.26 with `html/template` for server-side rendering
- HTMX for dynamic UI interactions (no frontend framework)
- Pulumi SDK v3 for infrastructure as code (Go-based programs)
- OVHcloud provider for cloud resources (network, k8s, node pools)
- Coder SDK for workspace and template management
- `net/http` with `http.ServeMux` for routing (no external router)
- Playwright for E2E frontend testing
- Makefile for build, test, and CI tasks
- Docker + docker-compose for containerized deployment
- Kustomize manifests for Kubernetes deployment (`k8s-deployment/`)
- MkDocs for documentation (`docs/`)

### Folder Structure

```
main.go                  # Pulumi IaC entry point (OVH infra + k8s + Coder setup)
cmd/
  server/
    main.go              # Web server entry point (HTTP server with graceful shutdown)
internal/
  server/
    handler.go           # HTTP handlers (admin UI, student portal, credentials, labs)
    auth.go              # Authentication (admin password, student login, JWT cookies)
    credentials.go       # Provider credentials management (OVH, extensible)
    job.go               # Job/lab lifecycle management (create, status, persist)
    pulumi.go            # Pulumi executor (preview, execute, destroy, retry)
    cleanup.go           # Background workspace cleanup goroutine (every 5 min)
    feedback.go          # Student feedback form + FeedbackStore (JSON persistence)
    github.go            # GitHub API — fetch latest stable Coder releases
    azure_api.go         # Azure-specific admin API handlers
    azure_options.go     # Azure AD runtime config + AzureADConfig struct
    ovh_api.go           # OVH-specific admin API handlers
    ovh_options.go       # OVH options cache (regions, VM sizes) with admin filtering
  providers/
    provider.go          # Cloud provider interface
    registry.go          # Provider registry
    ovh/                 # OVH-specific provider implementation
    azure/               # Azure-specific provider implementation
    dns/
      provider.go        # DNS provider interface (GetCredentialFields, SetupCertManagerDNS01, CreateARecord)
      registry.go        # DNS provider registry
      ovh/               # OVH DNS implementation
      azure/             # Azure DNS implementation
  tfparse/
    variables.go         # Terraform .tf file parser — extracts variable definitions from ZIP uploads
  pulumi/
    program.go           # Pulumi program builder
coder/
  coder.go               # Coder API client (users, workspaces, templates)
  https.go               # HTTPS/TLS setup: ingress-nginx, cert-manager, DNS-01/HTTP-01 ACME
k8s/
  k8s.go                 # Kubernetes provider, namespace, external IP
  helm.go                # Helm release management
ovh/
  ovh.go                 # OVH resource provisioning (network, k8s cluster, node pools)
utils/
  config.go              # Pulumi config helpers and constants
  git.go                 # Git clone, zip utilities
  log.go                 # Logging helpers
templates/               # Pulumi template code (copied into job workspaces)
web/
  base.html              # Shared HTML base template
  static/
    style.css            # Global styles
    *.js                 # Page-specific JS (HTMX interactions)
tests/
  *.spec.ts              # Playwright E2E tests
  chaos/                 # Chaos testing specs
docs/                    # MkDocs documentation source
Makefile                 # Build, test, coverage, CI targets
```

---

## Guardrails

CRITICAL: Do NOT modify, refactor, rename, or restructure existing files unless the user EXPLICITLY asks for it. This applies to all code, tests, configs, and documentation. When in doubt, ASK before changing.

### Must NOT

- Modify files not directly related to the user's request
- Refactor or "improve" existing working code on own initiative
- Rename functions, variables, types, packages, or files without being asked
- Change API routes, request/response contracts, or HTTP status codes
- Alter Pulumi infrastructure code (`main.go`, `ovh/`, `k8s/`, `coder/`, `templates/`)
- Edit CI/CD pipelines (`.gitlab-ci.yml`, `.github/`), `Dockerfile`, or `docker-compose.yml`
- Add, remove, or upgrade Go or npm dependencies without explicit approval
- Change authentication or security logic (`internal/server/auth.go`)
- Modify the Makefile targets or build configuration
- Move files between directories or reorganize the project structure
- Remove backward-compatibility routes or aliases

### Should

- Limit changes to the minimum scope needed to fulfill the request
- Explain what is intended to change BEFORE making edits to multiple files
- Preserve existing code style, naming conventions, and patterns
- Add new code in the appropriate existing package rather than creating new ones
- Run `make test` or `make lint` after changes when relevant

---

## Architecture

- HTML templates use a base/child pattern: `web/base.html` defines layout, page templates define blocks (`title`, `content`, `scripts`). Serve via `handler.serveTemplate()`.
- HTTP handlers live in `internal/server/` — use `net/http.HandlerFunc` and `http.ServeMux`. Do NOT introduce external routers (no gorilla/mux, no chi).
- HTMX drives all dynamic UI — prefer `hx-get`, `hx-post`, `hx-target`, `hx-swap` over writing custom JavaScript. JS files in `web/static/` are only for page-specific logic that HTMX cannot handle (e.g. encryption, cookie management).
- Pulumi programs are pure Go — infrastructure changes go in `ovh/`, `k8s/`, or `coder/`. The `templates/` directory is a self-contained Pulumi project copied into job workspaces.
- **Provider registry**: Cloud providers (`internal/providers/`) implement a `Provider` interface registered via `registry.go`. DNS providers (`internal/providers/dns/`) implement a separate `dns.Provider` interface for cert-manager DNS-01 challenges and A-record creation. Add new providers to the registry — never hardcode provider selection in handlers.
- **Feedback system**: `internal/server/feedback.go` manages student ratings/comments (1–5 rating, difficulty, recommendation, free text) via `FeedbackStore` persisted to JSON. Admin view at `/admin/feedback?lab_id=`.
- **Workspace cleanup**: `internal/server/cleanup.go` runs a background goroutine every 5 minutes that probes Coder reachability, enforces `WorkspaceLifetimeHours` per job, and records cleanup events for the stats dashboard.
- **Stats dashboard**: `ServeAdminStats` / `GetProjectStats` in `handler.go` aggregate deployment KPIs and monthly time-series data from job history (workspace snapshots + cleanup events).
- **BYO Kubernetes**: `LabConfig.UseExistingCluster` + `ExternalKubeconfig` fields allow skipping OVH provisioning when a cluster already exists.
- **HTTPS/TLS**: `coder/https.go` handles ingress-nginx and cert-manager installation, DNS-01/HTTP-01 ACME challenge setup, wildcard domains, and LoadBalancer IP resolution for OVHcloud.
- **Terraform variable detection**: `internal/tfparse/variables.go` parses Terraform `.tf` files from ZIP uploads to extract variable definitions used by the `DetectTemplateVariables` handler.
- **HTMX response helpers**: Use `writeToast()` for success/error toast notifications in HTMX responses; use `writeJSONError()` for JSON API error responses. Both are defined in `handler.go`.

---

## Go Conventions

- Use `html/template` with the base/child template pattern (`web/base.html` + page-specific templates)
- **Template caching**: Parse all HTML templates once at startup using `handler.templatesMu`. Never parse per-request — it is both a performance and a correctness constraint.
- Implement `http.HandlerFunc` for handlers — register on `http.ServeMux`, do NOT add external routers
- Wrap errors with context: `fmt.Errorf("failed to X: %w", err)`
- **Sanitize errors before clients**: Never expose internal error details in HTTP responses. Log full context server-side with `log.Printf`; return a safe, generic message to the client.
- Use `encoding/json` for JSON API responses, `handler.renderHTMLError()` for HTMX error responses
- Log at handler level with `log.Printf`, not deep in business logic
- Use context for request cancellation and timeouts (especially in Pulumi execution)
- **Concurrency**: `sync.RWMutex` is the project-wide pattern — used in `Handler.templatesMu`, `JobManager.mu`, `AuthHandler.mu`, and `CredentialsManager`. Use `RLock/RUnlock` for reads, `Lock/Unlock` for writes.
- **Form helpers**: Reuse `getFormValue()`, `atoiForm()`, and `escapeHTML()` from `handler.go` for form parsing and HTML escaping — don't reimplement these inline.
- **Context keys**: Use typed unexported context key types (e.g. `studentEmailContextKey`) — never raw strings — to avoid collisions when passing values through `context.WithValue`.
- **Table-driven tests**: Default to `[]struct{ name, input, expected }` with `t.Run(tt.name, ...)`. Use `testify/require` for fatal assertions, `testify/assert` for non-fatal.
- Do NOT modify existing files unless explicitly asked

---

## HTML / HTMX Conventions

- Use semantic HTML5 elements with HTMX attributes (`hx-get`, `hx-post`, `hx-target`, `hx-swap`)
- Follow the base template pattern: extend `web/base.html`, define `{{block "title"}}`, `{{block "content"}}`, `{{block "scripts"}}`
- Prefer HTMX interactions over custom JavaScript — JS is only for logic HTMX can't handle
- Use `hx-trigger="load, every 10s"` for polling job status updates
- Keep page-specific JS in `web/static/{page-name}.js`
- Style with the existing `web/static/style.css` — use existing CSS classes (`btn`, `error-message`, `success-message`, etc.)
- Escape dynamic content with `template.HTMLEscapeString` to prevent XSS
- Do NOT modify existing templates unless explicitly asked

---

## No Inline Styles

NEVER use `style="..."` attributes in HTML templates. All styling must be CSS classes in `web/static/style.css`.

```html
<!-- BAD -->
<textarea style="width: 100%; font-family: monospace;"></textarea>

<!-- GOOD -->
<textarea class="monospace"></textarea>
```

The only acceptable inline style is `style="display: none;"` for elements toggled by JavaScript at runtime.

When new visual styles are needed:
1. Add a CSS class in `web/static/style.css`
2. Reuse existing classes when possible (check the file first)
3. Apply the class via the `class` attribute in HTML

---

## Error Handling

- Always wrap errors with context: `fmt.Errorf("failed to X: %w", err)`
- Return errors up the call stack; let the HTTP handler decide the response format
- For HTML responses (HTMX), use `handler.renderHTMLError()` for consistent styling
- For JSON API responses, use `writeJSONError()` or `json.NewEncoder(w).Encode()`
- Log errors with `log.Printf` at the handler level, not deep in business logic

---

## Testing

- Go unit tests go next to the code they test (`*_test.go` in the same package)
- **Default to table-driven tests**: structure test cases as `[]struct{ name, input, expected }`, iterate with `t.Run(tt.name, ...)`, and add `t.Parallel()` for independent cases.
- Use `testify/require` for assertions that must abort the test on failure; use `testify/assert` to collect multiple failures in a single run.
- When adding a new exported function or changing a signature, update or add a `_test.go` in the same package.
- Playwright E2E tests go in `tests/*.spec.ts`; chaos tests go in `tests/chaos/`
- Use `make test-backend` for Go tests, `make test-frontend` for Playwright
- Run `make test-race` before committing any concurrent code changes (`go test -race` is the enforcement gate for data races)
- Coverage threshold is 50% — check with `make coverage-check`

---

## Security

- Provider credentials (OVH keys, etc.) are stored in-memory only via `CredentialsManager`
- Admin auth uses bcrypt-hashed passwords with cookie-based sessions
- Student passwords are generated server-side with `crypto/rand`
- Always sanitize user input in templates with `template.HTMLEscapeString()`
- Prevent directory traversal in static file serving (`strings.Contains(path, "..")`)
- **Error sanitization**: Never return internal error messages to HTTP clients. Log with `log.Printf` server-side; send a generic safe message to the user.
- **Backend authorization**: Auth checks happen in `auth.go` middleware (`authHandler.RequireAuth()`, `authHandler.RequireStudentAuth()`). Never bypass these by checking cookies manually in handlers.
- **Race detection**: Run `make test-race` on any PR that touches concurrent code. The `-race` flag in the Makefile is the enforcement gate.
- **Dependency auditing**: Run `govulncheck ./...` before adding or upgrading dependencies to catch known CVEs.

---

## Misc Conventions

- Keep backward compatibility for OVH-specific API routes alongside generic provider routes
- Job/lab state transitions: `pending -> running -> completed/failed`; `failed -> retry`; `completed -> destroyed -> recreated`
- Use the existing `LabConfig` struct for all lab configuration — extend it, don't replace it
- Run `make dev` for hot-reload development (requires `air`)

---

## Documentation

When creating or modifying a user-facing or operator-facing feature, update the relevant docs in `docs/`:

| Area | Doc file |
|------|----------|
| Admin UI, lab creation, credentials, lab/workspace management | `docs/admin.md` |
| Student feedback collection and admin feedback view | `docs/admin.md` |
| Azure AD authentication setup and admin group configuration | `docs/admin.md` |
| Student portal, login, workspace access | `docs/student.md` |
| OVHcloud setup, regions, flavors, infra | `docs/ovhcloud.md` |
| DNS provider configuration and TLS/HTTPS setup | `docs/ovhcloud.md` |
| Docker / docker-compose usage | `docs/docker.md` |
| Helm / Kubernetes deployment | `docs/helm.md` |
| Product overview, getting started | `docs/index.md` |

- **New feature**: Add a short section or bullet describing the feature and how to use it.
- **Changed behavior**: Update existing text, steps, or checklists to match current behavior.
- **UI changes**: Update described steps and consider adding/replacing screenshots in `docs/screens/`.
- Docs use MkDocs (Markdown). Reference screenshots as `![Alt](screens/filename.png)`.

When adding a new cloud provider or DNS provider, document its required credential fields and any setup steps in the relevant provider doc.

If the user explicitly asks not to touch docs, skip documentation updates for that change.
