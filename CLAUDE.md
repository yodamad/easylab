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
  providers/
    provider.go          # Cloud provider interface
    registry.go          # Provider registry
    ovh/                 # OVH-specific provider implementation
  pulumi/
    program.go           # Pulumi program builder
coder/
  coder.go               # Coder API client (users, workspaces, templates)
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

---

## Go Conventions

- Use `html/template` with the base/child template pattern (`web/base.html` + page-specific templates)
- Implement `http.HandlerFunc` for handlers — register on `http.ServeMux`, do NOT add external routers
- Wrap errors with context: `fmt.Errorf("failed to X: %w", err)`
- Use `encoding/json` for JSON API responses, `handler.renderHTMLError()` for HTMX error responses
- Log at handler level with `log.Printf`, not deep in business logic
- Use context for request cancellation and timeouts (especially in Pulumi execution)
- Protect concurrent state with `sync.RWMutex` (see `Job.mu` pattern in `job.go`)
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
- Playwright E2E tests go in `tests/*.spec.ts`
- Chaos tests go in `tests/chaos/`
- Use `make test-backend` for Go tests, `make test-frontend` for Playwright
- Use `make test-race` before committing concurrent code changes
- Coverage threshold is 50% — check with `make coverage-check`

---

## Security

- Provider credentials (OVH keys, etc.) are stored in-memory only via `CredentialsManager`
- Admin auth uses bcrypt-hashed passwords with cookie-based sessions
- Student passwords are generated server-side with `crypto/rand`
- Always sanitize user input in templates with `template.HTMLEscapeString()`
- Prevent directory traversal in static file serving (`strings.Contains(path, "..")`)

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
| Student portal, login, workspace access | `docs/student.md` |
| OVHcloud setup, regions, flavors, infra | `docs/ovhcloud.md` |
| Docker / docker-compose usage | `docs/docker.md` |
| Helm / Kubernetes deployment | `docs/helm.md` |
| Product overview, getting started | `docs/index.md` |

- **New feature**: Add a short section or bullet describing the feature and how to use it.
- **Changed behavior**: Update existing text, steps, or checklists to match current behavior.
- **UI changes**: Update described steps and consider adding/replacing screenshots in `docs/screens/`.
- Docs use MkDocs (Markdown). Reference screenshots as `![Alt](screens/filename.png)`.

If the user explicitly asks not to touch docs, skip documentation updates for that change.
