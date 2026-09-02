package server

import (
	"context"
	"easylab/coder"
	"easylab/internal/providers/workspace"
	"easylab/utils"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// bakeStatus tracks an in-flight or last-failed bake, keyed by "<labID>/<template>"
// on Handler.bakeStatuses. Deliberately not persisted — a restart mid-bake just
// leaves the admin needing to click "Rebuild" again, matching how in-flight Pulumi
// job state isn't durably tracked either. A successful bake is recorded on
// LabConfig.BakedImages instead (which is persisted); this only ever holds
// "building" or "failed".
type bakeStatus struct {
	State   string // "building" | "failed"
	Error   string
	Started time.Time
}

// bakeExecutionConcurrency returns the maximum number of pre-bake builds allowed to
// run at once, configurable via BAKE_EXECUTION_CONCURRENCY env var. Admin-triggered
// and rare, unlike workspace creates or Pulumi executions, so a small default is
// plenty of headroom.
func bakeExecutionConcurrency() int {
	if v := os.Getenv("BAKE_EXECUTION_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 3
}

// bakeTimeout bounds how long a bake is allowed to run before it is reported as
// failed, configurable via BAKE_TIMEOUT_MINUTES env var. Generous enough for a slow
// first build (large base image, many features) without leaving a stuck Job's
// status polling forever if something goes permanently wrong (e.g. the pod can
// never schedule).
func bakeTimeout() time.Duration {
	if v := os.Getenv("BAKE_TIMEOUT_MINUTES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Minute
		}
	}
	return 30 * time.Minute
}

// bakeRegistryReadyAttempts bounds how many times awaitBake retries verifying a
// baked image is actually pullable before giving up, configurable via
// BAKE_REGISTRY_READY_ATTEMPTS env var. Paired with bakeRegistryReadyInterval, the
// default (18 * 10s = 3 minutes) is generous for a freshly-issued TLS certificate
// (in-cluster case, HTTP-01) to finish propagating without waiting so long that a
// genuinely broken pull path looks like a hang rather than a failure.
func bakeRegistryReadyAttempts() int {
	if v := os.Getenv("BAKE_REGISTRY_READY_ATTEMPTS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 18
}

// bakeRegistryReadyInterval is the delay between bakeRegistryReadyAttempts retries,
// configurable via BAKE_REGISTRY_READY_INTERVAL_SECONDS env var.
func bakeRegistryReadyInterval() time.Duration {
	if v := os.Getenv("BAKE_REGISTRY_READY_INTERVAL_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return 10 * time.Second
}

var repoSegmentInvalid = regexp.MustCompile(`[^a-z0-9-]`)

// sanitizeRepoSegment makes template into a safe registry path segment for an
// external bake destination — lowercased, non [a-z0-9-] replaced with "-".
func sanitizeRepoSegment(s string) string {
	s = repoSegmentInvalid.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "-")
	return strings.Trim(s, "-")
}

// bakePathParts parses "api/labs/{id}/templates/{name}/bake[-status]" and returns
// the job ID and (URL-decoded) template name, or ok=false with the response already
// written on failure.
func bakePathParts(w http.ResponseWriter, r *http.Request) (jobID, templateName string, ok bool) {
	pathParts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(pathParts) < 6 || (pathParts[1] != "labs" && pathParts[1] != "jobs") {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return "", "", false
	}
	name, err := url.PathUnescape(pathParts[4])
	if err != nil {
		http.Error(w, "Invalid template name", http.StatusBadRequest)
		return "", "", false
	}
	return pathParts[2], name, true
}

// BakeTemplate starts (or restarts) a pre-bake of a devcontainer template: build it
// once into a normal image, push it, and record it so every subsequent student
// workspace for that template just pulls the image directly instead of running
// envbuilder. See docs/templates.md's "Hosting the cache registry in the cluster"
// / pre-baking section for the admin-facing explanation of the tradeoffs.
func (h *Handler) BakeTemplate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	jobID, templateName, ok := bakePathParts(w, r)
	if !ok {
		return
	}

	job, exists := h.jobManager.GetJob(jobID)
	if !exists {
		http.Error(w, "Lab not found", http.StatusNotFound)
		return
	}

	job.mu.RLock()
	status := job.Status
	kubeconfig := extractStringFromConfigValue(job.Kubeconfig)
	namespace := job.workspaceNamespace()
	domain := ""
	dnsProvider := ""
	clusterIssuerName := ""
	var templates []WorkspaceTemplate
	if job.Config != nil {
		domain = job.Config.Domain
		dnsProvider = job.Config.DNSProvider
		clusterIssuerName = job.Config.ClusterIssuerName
		templates = job.Config.GetWorkspaceTemplates()
	}
	job.mu.RUnlock()
	if clusterIssuerName == "" {
		clusterIssuerName = utils.DefaultClusterIssuerName
	}

	if status != JobStatusCompleted {
		http.Error(w, "Lab is not ready yet", http.StatusBadRequest)
		return
	}
	if kubeconfig == "" {
		http.Error(w, "Lab cluster configuration not available", http.StatusInternalServerError)
		return
	}

	var tmpl *WorkspaceTemplate
	for i := range templates {
		if templates[i].Name == templateName {
			tmpl = &templates[i]
			break
		}
	}
	if tmpl == nil || tmpl.Devcontainer == nil || !tmpl.Devcontainer.Enabled {
		h.renderHTMLError(w, "Cannot Bake", "Only enabled devcontainer templates can be pre-baked.")
		return
	}

	externalCacheRepo := strings.TrimSpace(tmpl.Devcontainer.CacheRepo)
	useInCluster := externalCacheRepo == "" && tmpl.Devcontainer.UseInClusterCache
	if externalCacheRepo == "" && !useInCluster {
		h.renderHTMLError(w, "Cannot Bake", "This template has no cache registry configured — set an external cache registry, or host it in-cluster, before baking.")
		return
	}
	if useInCluster && domain == "" {
		h.renderHTMLError(w, "Cannot Bake", "Baking to the in-cluster registry requires the lab to have a domain configured — a student workspace pulls the baked image over trusted HTTPS, which needs a certificate. Set an external cache registry instead, or configure a domain for this lab.")
		return
	}

	backend, err := h.workspaceBackendFor(jobID, kubeconfig, namespace)
	if err != nil {
		log.Printf("BakeTemplate: failed to build backend for lab %s: %v", jobID, err)
		h.renderHTMLError(w, "Cannot Bake", "Could not reach the lab cluster.")
		return
	}
	bp, ok := backend.(workspace.BakeProvider)
	if !ok {
		h.renderHTMLError(w, "Cannot Bake", "This lab's backend does not support pre-baking.")
		return
	}

	var destRepo, pullRepo string
	var pushInsecure, pullInsecure bool
	var pullRegistryAuthSecret string
	if useInCluster {
		rc, ok := backend.(workspace.RegistryCacheProvider)
		if !ok {
			h.renderHTMLError(w, "Cannot Bake", "This lab's backend does not support the in-cluster registry.")
			return
		}
		// EnsureBuildCache provisions the registry itself (Deployment/Service/PVC);
		// the Ingress in front of it needs that Service to already exist.
		if _, err := rc.EnsureBuildCache(r.Context()); err != nil {
			log.Printf("BakeTemplate: failed to provision registry for lab %s: %v", jobID, err)
			h.renderHTMLError(w, "Cannot Bake", "Could not provision the in-cluster registry.")
			return
		}
		wildcardTLSSecret := ""
		if dnsProvider != "" {
			wildcardTLSSecret = coder.WildcardTLSSecretName
		}
		if _, err := rc.EnsureRegistryIngress(r.Context(), domain, wildcardTLSSecret, clusterIssuerName); err != nil {
			log.Printf("BakeTemplate: failed to expose registry for lab %s: %v", jobID, err)
			h.renderHTMLError(w, "Cannot Bake", "Could not expose the in-cluster registry over HTTPS.")
			return
		}
		destRepo, pullRepo = rc.BakedImageRepo(jobID, templateName, domain)
		// envbuilder pushes over the registry's internal, plain-HTTP address — it is
		// a userspace HTTP client, unaffected by the missing TLS there. The kubelet's
		// later pull, by contrast, goes through the Ingress over real HTTPS and must
		// be verified like any other trusted registry — pushInsecure and pullInsecure
		// are deliberately different, not the same flag reused for both paths.
		pushInsecure = true
		pullInsecure = false
	} else {
		// A dedicated sub-path per lab and template: cache_repo is often shared
		// across every devcontainer template in a lab (and potentially across labs,
		// e.g. a shared org registry) — baking under the bare cache_repo:latest
		// would let two templates, or the same template name in two labs, silently
		// overwrite each other's baked image.
		destRepo = fmt.Sprintf("%s/baked-%s-%s", strings.TrimSuffix(externalCacheRepo, "/"), sanitizeRepoSegment(jobID), sanitizeRepoSegment(templateName))
		pullRepo = destRepo
		pushInsecure = tmpl.Devcontainer.Insecure
		pullInsecure = tmpl.Devcontainer.Insecure
		// The destination registry may require auth for reads too, not just the
		// push — verifying pullability must authenticate the same way the push did.
		pullRegistryAuthSecret = tmpl.Devcontainer.RegistryAuthSecret
	}

	dc := toWorkspaceDevcontainer(tmpl.Devcontainer)
	dc.CacheRepo = destRepo
	dc.Insecure = pushInsecure

	req := workspace.BakeRequest{
		LabID:         jobID,
		Template:      templateName,
		GitRepo:       tmpl.GitRepo,
		GitBranch:     tmpl.GitBranch,
		GitAuthSecret: tmpl.GitAuthSecret,
		CPU:           tmpl.CPU,
		Memory:        tmpl.Memory,
		CPULimit:      tmpl.CPULimit,
		MemoryLimit:   tmpl.MemoryLimit,
		Devcontainer:  dc,
	}
	if err := bp.EnsureBakeJob(r.Context(), req); err != nil {
		log.Printf("BakeTemplate: failed to start bake for lab %s template %s: %v", jobID, templateName, err)
		h.renderHTMLError(w, "Cannot Bake", "Could not start the bake job.")
		return
	}

	key := jobID + "/" + templateName
	h.bakeStatusesMu.Lock()
	h.bakeStatuses[key] = &bakeStatus{State: "building", Started: time.Now()}
	h.bakeStatusesMu.Unlock()

	h.recordAudit(adminActor(r), "admin", "lab.template_bake", jobID, templateName)

	go func() {
		h.bakeExecSem <- struct{}{}
		defer func() { <-h.bakeExecSem }()
		h.awaitBake(jobID, templateName, bp, pullRepo, pullInsecure, pullRegistryAuthSecret)
	}()

	w.Header().Set("Content-Type", "text/html")
	fmt.Fprint(w, h.renderBakeStatus(jobID, templateName))
}

// awaitBake polls the bake Job to completion (or failure/timeout), then records the
// result: LabConfig.BakedImages on success, bakeStatuses on failure. Runs in its own
// goroutine, bounded by bakeExecSem.
func (h *Handler) awaitBake(jobID, templateName string, bp workspace.BakeProvider, pullRepo string, pullInsecure bool, pullRegistryAuthSecret string) {
	key := jobID + "/" + templateName
	ctx, cancel := context.WithTimeout(context.Background(), bakeTimeout())
	defer cancel()

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	var final workspace.BakeState
	var timedOut bool
poll:
	for {
		select {
		case <-ctx.Done():
			timedOut = true
			break poll
		case <-ticker.C:
			state, err := bp.BakeJobStatus(ctx, jobID, templateName)
			if err != nil {
				log.Printf("Failed to poll bake status for lab %s template %s: %v", jobID, templateName, err)
				continue
			}
			if state == workspace.BakeStateBuilding || state == workspace.BakeStateNone {
				continue
			}
			final = state
			break poll
		}
	}

	if timedOut || final != workspace.BakeStateComplete {
		msg := "bake failed"
		if timedOut {
			msg = "bake timed out"
		}
		h.bakeStatusesMu.Lock()
		h.bakeStatuses[key] = &bakeStatus{State: "failed", Error: msg}
		h.bakeStatusesMu.Unlock()
		return
	}

	// The image is built and pushed, but that alone doesn't prove a student's pod
	// can actually pull it: BakeRemoteUser fetches the manifest over the exact same
	// path (host, scheme, TLS trust) a kubelet pull would use, so a failure here is
	// a direct, faithful signal that the pull would fail too — most commonly a
	// freshly-issued TLS certificate (in-cluster case) that hasn't finished
	// propagating yet, which is why this retries for a while rather than failing on
	// the first attempt. A persistent failure means the pull path is broken (e.g. a
	// stalled cert-manager Certificate/ClusterIssuer mismatch) — that must be
	// reported as a failed bake, not a false "baked" success handed to students.
	var remoteUser string
	var verifyErr error
	for attempt := 1; attempt <= bakeRegistryReadyAttempts(); attempt++ {
		remoteUser, verifyErr = bp.BakeRemoteUser(context.Background(), pullRepo, pullInsecure, pullRegistryAuthSecret)
		if verifyErr == nil {
			break
		}
		log.Printf("Baked image not yet pullable for lab %s template %s (attempt %d/%d): %v", jobID, templateName, attempt, bakeRegistryReadyAttempts(), verifyErr)
		time.Sleep(bakeRegistryReadyInterval())
	}
	if verifyErr != nil {
		h.bakeStatusesMu.Lock()
		h.bakeStatuses[key] = &bakeStatus{State: "failed", Error: fmt.Sprintf("image was built, but never became pullable: %v", verifyErr)}
		h.bakeStatusesMu.Unlock()
		// A confirmed-broken pull path invalidates any previously-recorded bake at
		// this same reference too — RequestWorkspace reads LabConfig.BakedImages
		// directly and has no visibility into this in-memory failure, so a stale
		// entry here would otherwise keep being handed to students indefinitely
		// (including one recorded before this verification step existed at all).
		// Falling back to no entry means RequestWorkspace uses the normal build
		// path again instead of a reference just proven unreachable.
		h.updateJobConfig(jobID, func(config *LabConfig) {
			delete(config.BakedImages, templateName)
		})
		go func() {
			if err := h.jobManager.SaveJob(jobID); err != nil {
				log.Printf("Failed to persist bake invalidation for lab %s template %s: %v", jobID, templateName, err)
			}
		}()
		return
	}

	h.updateJobConfig(jobID, func(config *LabConfig) {
		if config.BakedImages == nil {
			config.BakedImages = make(map[string]BakedImage)
		}
		config.BakedImages[templateName] = BakedImage{Image: pullRepo, RemoteUser: remoteUser, At: time.Now()}
	})
	go func() {
		if err := h.jobManager.SaveJob(jobID); err != nil {
			log.Printf("Failed to persist bake result for lab %s template %s: %v", jobID, templateName, err)
		}
	}()

	h.bakeStatusesMu.Lock()
	delete(h.bakeStatuses, key)
	h.bakeStatusesMu.Unlock()
}

// BakeTemplateStatus renders the current bake state for a template as an HTML
// fragment, self-polling like GetJobStatus while a build is in flight.
func (h *Handler) BakeTemplateStatus(w http.ResponseWriter, r *http.Request) {
	jobID, templateName, ok := bakePathParts(w, r)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprint(w, h.renderBakeStatus(jobID, templateName))
}

func (h *Handler) renderBakeStatus(jobID, templateName string) string {
	key := jobID + "/" + templateName
	h.bakeStatusesMu.RLock()
	st, inFlight := h.bakeStatuses[key]
	h.bakeStatusesMu.RUnlock()

	escapedJobID := template.HTMLEscapeString(jobID)
	escapedName := url.PathEscape(templateName)

	var b strings.Builder
	if inFlight && st.State == "building" {
		b.WriteString(`<span class="status-badge status-running">building</span>`)
		// Targets the stable outer container (#bake-status-<name>) and replaces its
		// whole content, not just this div (hx-swap="outerHTML" on itself would only
		// swap itself, leaving the badge above as a stale sibling — duplicating it
		// on every poll instead of replacing it).
		fmt.Fprintf(&b, `<div hx-get="/api/labs/%s/templates/%s/bake-status" hx-trigger="every 10s" hx-target="#bake-status-%s" hx-swap="innerHTML"></div>`, escapedJobID, escapedName, escapedName)
		return b.String()
	}
	if inFlight && st.State == "failed" {
		b.WriteString(`<span class="status-badge status-failed">failed</span>`)
		fmt.Fprintf(&b, `<div class="error-message">%s</div>`, template.HTMLEscapeString(st.Error))
		return b.String()
	}

	job, exists := h.jobManager.GetJob(jobID)
	if !exists || job.Config == nil {
		return ""
	}
	job.mu.RLock()
	baked, ok := job.Config.BakedImages[templateName]
	job.mu.RUnlock()
	if !ok {
		return `<span class="status-badge status-idle">not baked</span>`
	}
	fmt.Fprintf(&b, `<span class="status-badge status-completed">baked %s</span>`, template.HTMLEscapeString(baked.At.Format("2006-01-02 15:04")))
	return b.String()
}
