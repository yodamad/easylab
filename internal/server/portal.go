package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"easylab/ingress"
	"easylab/internal/providers/workspace"
	"easylab/utils"
)

const (
	// EnvPortalImage overrides the image an in-lab student portal runs. It
	// defaults to the release image matching this server's own version.
	EnvPortalImage = "EASYLAB_PORTAL_IMAGE"
	// EnvPublicURL is this instance's address as students reach it. In-lab portals
	// send students here to sign in with Azure AD, GitHub or GitLab (see broker.go).
	EnvPublicURL = "EASYLAB_PUBLIC_URL"

	// portalImageRepository is where EasyLab's release images are published.
	portalImageRepository = "docker.io/yodamad/easylab"

	// portalDrainTimeout bounds collecting a portal's outbox while an admin page
	// waits on it: a slow cluster must not hold the page back.
	portalDrainTimeout = 5 * time.Second

	// minPortalPasswordLength is the floor for a student portal password an admin
	// types in place of the generated one. Deliberately low: this guards a lab's
	// sign-in page, not an account, the 5-attempt lockout in auth.go does the
	// heavy lifting, and an organiser has to be able to read it out to a room.
	minPortalPasswordLength = 8
)

// portalImage returns the image in-lab portals run: EASYLAB_PORTAL_IMAGE, else
// the release image of this server's version. It is "" for a development build
// with no override — there is no published image to match it.
func portalImage() string {
	if image := strings.TrimSpace(os.Getenv(EnvPortalImage)); image != "" {
		return image
	}
	if Version == "" || Version == "dev" {
		return ""
	}
	// Release images are tagged "v<version>"; Version itself carries no prefix.
	return portalImageRepository + ":v" + strings.TrimPrefix(Version, "v")
}

// portalTarget is what reaching a lab's portal takes, read once from the job.
type portalTarget struct {
	enabled    bool
	completed  bool
	kubeconfig string
	namespace  string
}

func portalTargetFor(job *Job) portalTarget {
	job.mu.RLock()
	defer job.mu.RUnlock()
	return portalTarget{
		enabled:    job.Config != nil && job.Config.StudentPortal,
		completed:  job.Status == JobStatusCompleted,
		kubeconfig: extractStringFromConfigValue(job.Kubeconfig),
		namespace:  job.workspaceNamespace(),
	}
}

// portalDeployerFor returns the portal side of a lab's backend, or false when
// the lab has no cluster to reach or its backend cannot host a portal.
func (h *Handler) portalDeployerFor(labID string, target portalTarget) (workspace.PortalDeployer, workspace.Backend, bool) {
	if target.kubeconfig == "" {
		return nil, nil, false
	}
	backend, err := h.workspaceBackendFor(labID, target.kubeconfig, target.namespace)
	if err != nil {
		log.Printf("[portal] skipping lab %s: failed to build backend: %v", labID, err)
		return nil, nil, false
	}
	pd, ok := backend.(workspace.PortalDeployer)
	return pd, backend, ok
}

// lockPortal serializes portal writes for one lab and returns the unlock func.
func (h *Handler) lockPortal(labID string) func() {
	v, _ := h.portalLocks.LoadOrStore(labID, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func (h *Handler) setPortalError(labID, message string) {
	h.portalErrorsMu.Lock()
	defer h.portalErrorsMu.Unlock()
	if message == "" {
		delete(h.portalErrors, labID)
		return
	}
	h.portalErrors[labID] = message
}

func (h *Handler) portalError(labID string) string {
	h.portalErrorsMu.RLock()
	defer h.portalErrorsMu.RUnlock()
	return h.portalErrors[labID]
}

// ensurePortalBrokerSecret returns the lab's broker secret, generating and
// persisting one the first time its portal is deployed.
func (h *Handler) ensurePortalBrokerSecret(job *Job) (string, error) {
	job.mu.Lock()
	secret := job.PortalBrokerSecret
	if secret == "" {
		generated, err := GenerateWorkspaceToken()
		if err != nil {
			job.mu.Unlock()
			return "", fmt.Errorf("failed to generate portal broker secret: %w", err)
		}
		job.PortalBrokerSecret = generated
		secret = generated
	}
	job.mu.Unlock()
	return secret, nil
}

// ensurePortalPassword gives the lab its own student portal password, generating
// one the first time its portal needs it. It reports whether it had to, so the
// caller can persist the job.
//
// Generating here rather than at lab creation is what gets a lab that was
// deployed before passwords were per-portal onto one of its own: its next
// reconcile fills this in. Both fields are written together — a password without
// its hash would leave the portal refusing every sign-in.
func (h *Handler) ensurePortalPassword(job *Job) (bool, error) {
	job.mu.Lock()
	defer job.mu.Unlock()

	if job.Config == nil {
		return false, nil
	}
	if job.Config.StudentPortalPassword != "" && job.Config.StudentPortalPasswordHash != "" {
		return false, nil
	}

	password := job.Config.StudentPortalPassword
	if password == "" {
		generated, err := GeneratePortalPassword()
		if err != nil {
			return false, fmt.Errorf("failed to generate student portal password: %w", err)
		}
		password = generated
	}
	hash, err := HashStudentPassword(password)
	if err != nil {
		return false, err
	}
	job.Config.StudentPortalPassword = password
	job.Config.StudentPortalPasswordHash = hash
	return true, nil
}

// portalConfigFor builds the config a lab's portal is served from. Callers hold
// the lab's portal lock, which is also what keeps two of them from generating
// two different passwords for the same lab.
func (h *Handler) portalConfigFor(job *Job) (map[string][]byte, error) {
	generated, err := h.ensurePortalPassword(job)
	if err != nil {
		return nil, err
	}
	if generated {
		if saveErr := h.jobManager.SaveJob(job.ID); saveErr != nil {
			// Logged, not fatal: the portal still gets a working password now, and
			// the next reconcile generates a fresh one if this was lost.
			log.Printf("[portal] failed to persist generated portal password for lab %s: %v", job.ID, saveErr)
		}
	}

	var auth StudentAuthSnapshot
	if h.studentAuthSnapshot != nil {
		auth = h.studentAuthSnapshot()
	}

	// Mint the lab's notification signing key here, before any state reaches a
	// portal, so the key the portal verifies tokens with is the admin's and never
	// changes under it. Generating it on first use alone is not enough: a portal
	// that minted its own would lose it to the next state this builds (see
	// setPortalJob), and with it every token its workspaces already carry — which
	// an admin sending the lab a message is exactly what triggers.
	if _, generated, err := ensureNotifySecret(job); err != nil {
		// Logged, not fatal: the lab keeps working, it just has no in-IDE
		// notifications until a later sync manages to mint the key.
		log.Printf("[notify] failed to prepare the notification secret for lab %s: %v", job.ID, err)
	} else if generated {
		if err := h.jobManager.SaveJob(job.ID); err != nil {
			log.Printf("[notify] failed to persist the notification secret for lab %s: %v", job.ID, err)
		}
	}

	state := portalStateFromJob(job, auth, h.effectivePublicURL())
	// The portal's own address, so the workspaces it creates know where to read
	// their notifications (see workspaceNotifyAccess). Empty until the portal has
	// been deployed once, which is also when it first has students.
	h.portalErrorsMu.RLock()
	state.PortalURL = h.portalURLs[job.ID]
	h.portalErrorsMu.RUnlock()
	return encodePortalState(state)
}

// needsInClusterBuildCache reports whether any template builds its devcontainer
// against the lab's in-cluster registry without a pre-baked image to skip to.
func needsInClusterBuildCache(job *Job) bool {
	job.mu.RLock()
	defer job.mu.RUnlock()
	if job.Config == nil {
		return false
	}
	for _, t := range job.Config.WorkspaceTemplates {
		if t.Devcontainer != nil && t.Devcontainer.Enabled && t.Devcontainer.UseInClusterCache && t.Devcontainer.CacheRepo == "" {
			return true
		}
	}
	return false
}

// templateSecretNames lists the credential Secrets the templates reference by
// name and the backend reads when a student requests a workspace. The portal is
// granted read access to these and no other (see workspace.PortalSpec), so a
// template gaining a new kind of named credential has to be added here.
// ImagePullSecrets are not listed: the kubelet resolves those, not the portal.
func templateSecretNames(templates []WorkspaceTemplate) []string {
	seen := map[string]bool{}
	var names []string
	add := func(name string) {
		if name = strings.TrimSpace(name); name != "" && !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	for _, t := range templates {
		add(t.GitAuthSecret)
		if t.Devcontainer != nil {
			add(t.Devcontainer.RegistryAuthSecret)
			add(t.Devcontainer.ConfigAuthSecret)
		}
	}
	return names
}

// reconcilePortal brings a lab's in-lab student portal in line with the lab: it
// deploys or upgrades it and refreshes the state it serves students from. A lab
// that did not ask for a portal is left alone — removing one is an explicit
// action (see SetLabPortal), not something a reconcile does behind the admin.
//
// Best-effort like reconcilePrepull, but a failure is kept for the lab detail
// page: unlike a missed pre-pull, a portal that is not there is visible.
func (h *Handler) reconcilePortal(labID string) {
	job, exists := h.jobManager.GetJob(labID)
	if !exists {
		return
	}
	target := portalTargetFor(job)
	if !target.enabled || !target.completed {
		return
	}
	pd, backend, ok := h.portalDeployerFor(labID, target)
	if !ok {
		return
	}

	unlock := h.lockPortal(labID)
	defer unlock()

	if err := h.deployPortal(job, pd, backend); err != nil {
		log.Printf("[portal] failed to reconcile student portal for lab %s: %v", labID, err)
		h.setPortalError(labID, err.Error())
		return
	}
	h.setPortalError(labID, "")
}

// deployPortal runs one EnsurePortal for the job. Callers hold the lab's portal lock.
func (h *Handler) deployPortal(job *Job, pd workspace.PortalDeployer, backend workspace.Backend) error {
	image := h.effectivePortalImage()
	if image == "" {
		// Only a development build gets here: a release runs the image of its own
		// version. The portal is the same program as this server, started in
		// student mode, so it needs an image built from the same code — and none
		// is published for an unversioned build.
		return fmt.Errorf("this is a development build, so there is no published EasyLab image of the same version for the portal to run; students can keep using this instance's student space, or set a portal image built from this code on the Student portals settings page (or with %s)", EnvPortalImage)
	}

	hadSecret := func() bool {
		job.mu.RLock()
		defer job.mu.RUnlock()
		return job.PortalBrokerSecret != ""
	}()
	if _, err := h.ensurePortalBrokerSecret(job); err != nil {
		return err
	}
	if !hadSecret {
		if err := h.jobManager.SaveJob(job.ID); err != nil {
			log.Printf("[portal] failed to persist broker secret for lab %s: %v", job.ID, err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), clusterOperationTimeout)
	defer cancel()

	// The portal can only read Secrets, so the registry a devcontainer template
	// builds against — which generates its own credentials — is provisioned from
	// here rather than on a student's first request.
	if needsInClusterBuildCache(job) {
		if rc, ok := backend.(workspace.RegistryCacheProvider); ok {
			if _, err := rc.EnsureBuildCache(ctx); err != nil {
				log.Printf("[portal] failed to provision in-cluster registry cache for lab %s: %v", job.ID, err)
			}
		}
	}

	config, err := h.portalConfigFor(job)
	if err != nil {
		return err
	}

	job.mu.RLock()
	spec := workspace.PortalSpec{LabID: job.ID, Image: image, Config: config}
	if job.Config != nil {
		spec.SecretNames = templateSecretNames(job.Config.WorkspaceTemplates)
		spec.Domain = job.Config.Domain
		spec.ClusterIssuer = job.Config.ClusterIssuerName
		// The portal is served by the same controller as the lab's workspaces, and
		// reads that controller's Service for its own nip.io fallback.
		spec.IngressClass = job.Config.IngressClassName
		spec.IngressNamespace = job.Config.NginxIngressNamespace
		spec.IngressService = job.Config.NginxIngressServiceName
		// Same rule as a workspace (see RequestWorkspace): a lab with a DNS provider
		// got a wildcard certificate at provisioning, and the portal is served from it.
		if job.Config.DNSProvider != "" {
			spec.WildcardTLSSecret = ingress.WildcardTLSSecretName
		}
	}
	job.mu.RUnlock()
	if spec.ClusterIssuer == "" {
		spec.ClusterIssuer = utils.DefaultClusterIssuerName
	}

	portalURL, err := pd.EnsurePortal(ctx, spec)
	if err != nil {
		return fmt.Errorf("failed to deploy student portal: %w", err)
	}
	h.setPortalURL(job.ID, portalURL)

	// The config above was built before EnsurePortal told us the address, so on a
	// first deployment it went out without one and the portal would not know where
	// its workspaces should read their notifications. Write it again now that we
	// know. Not through syncPortalState: callers hold the lab's portal lock.
	if portalURL != "" {
		if refreshed, buildErr := h.portalConfigFor(job); buildErr != nil {
			log.Printf("[portal] failed to rebuild state with the portal address for lab %s: %v", job.ID, buildErr)
		} else if syncErr := pd.SyncPortalConfig(ctx, job.ID, refreshed); syncErr != nil {
			log.Printf("[portal] failed to sync the portal address for lab %s: %v", job.ID, syncErr)
		}
	}
	return nil
}

func (h *Handler) setPortalURL(labID, portalURL string) {
	h.portalErrorsMu.Lock()
	defer h.portalErrorsMu.Unlock()
	if portalURL == "" {
		delete(h.portalURLs, labID)
		return
	}
	h.portalURLs[labID] = portalURL
}

// BrokerPortal is the AuthHandler's broker lab resolver (see
// SetBrokerLabResolver): it returns where a lab's portal is and the secret
// shared with it, or false for a lab with no portal to sign students in to.
//
// The URL is the one the portal was deployed under — derived from the lab's own
// configuration — which is what makes it safe to redirect a signed identity to.
func (h *Handler) BrokerPortal(labID string) (portalURL, secret string, ok bool) {
	job, exists := h.jobManager.GetJob(labID)
	if !exists {
		return "", "", false
	}
	target := portalTargetFor(job)
	if !target.enabled || !target.completed {
		return "", "", false
	}
	job.mu.RLock()
	secret = job.PortalBrokerSecret
	job.mu.RUnlock()
	if secret == "" {
		return "", "", false
	}

	h.portalErrorsMu.RLock()
	portalURL = h.portalURLs[labID]
	h.portalErrorsMu.RUnlock()
	if portalURL == "" {
		// Not deployed by this process yet (it restarted since): ask the cluster.
		pd, _, supported := h.portalDeployerFor(labID, target)
		if !supported {
			return "", "", false
		}
		ctx, cancel := context.WithTimeout(context.Background(), portalDrainTimeout)
		defer cancel()
		status, err := pd.PortalStatus(ctx, labID)
		if err != nil || status.URL == "" {
			return "", "", false
		}
		portalURL = status.URL
		h.setPortalURL(labID, portalURL)
	}
	return portalURL, secret, true
}

// syncPortalState pushes a lab's current state to its portal without touching
// the deployment: the cheap path for a change that needs nothing else.
func (h *Handler) syncPortalState(labID string) {
	job, exists := h.jobManager.GetJob(labID)
	if !exists {
		return
	}
	target := portalTargetFor(job)
	if !target.enabled || !target.completed {
		return
	}
	pd, _, ok := h.portalDeployerFor(labID, target)
	if !ok {
		return
	}

	unlock := h.lockPortal(labID)
	defer unlock()

	config, err := h.portalConfigFor(job)
	if err != nil {
		log.Printf("[portal] failed to build state for lab %s: %v", labID, err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), clusterOperationTimeout)
	defer cancel()
	if err := pd.SyncPortalConfig(ctx, labID, config); err != nil {
		log.Printf("[portal] failed to sync state for lab %s: %v", labID, err)
	}
}

// syncAllPortals pushes the current state to every lab's portal. Called when
// something they all mirror changes: the student sign-in settings.
func (h *Handler) syncAllPortals() {
	for _, job := range h.jobManager.GetAllJobs() {
		h.syncPortalState(job.ID)
	}
}

// reconcilePortals reconciles every lab's portal and collects what each has to
// report. It rides the cleanup ticker, which is what upgrades the portals after
// this server was, and what bounds how stale the admin's view of a lab can get.
func (h *Handler) reconcilePortals() {
	for _, job := range h.jobManager.GetAllJobs() {
		target := portalTargetFor(job)
		if !target.enabled || !target.completed {
			continue
		}
		_, backend, ok := h.portalDeployerFor(job.ID, target)
		if !ok {
			continue
		}
		reachCtx, cancel := context.WithTimeout(context.Background(), clusterReachabilityTimeout)
		reachable := backend.Reachable(reachCtx)
		cancel()
		if !reachable {
			continue
		}
		h.reconcilePortal(job.ID)
		h.drainPortalOutbox(context.Background(), job.ID)
	}
}

// drainPortalOutbox collects what a lab's portal queued for the admin: feedback
// goes to the feedback store, audit entries to the audit log, and workspace
// events into the lab's history, each at the time it happened.
func (h *Handler) drainPortalOutbox(ctx context.Context, labID string) {
	job, exists := h.jobManager.GetJob(labID)
	if !exists {
		return
	}
	target := portalTargetFor(job)
	if !target.enabled {
		return
	}
	pd, _, ok := h.portalDeployerFor(labID, target)
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, clusterOperationTimeout)
	defer cancel()

	imported := false
	err := pd.DrainPortalOutbox(ctx, labID, func(id, raw string) error {
		changed, err := h.applyPortalRecord(labID, raw)
		if changed {
			imported = true
		}
		return err
	})
	if err != nil {
		log.Printf("[portal] failed to drain outbox of lab %s: %v", labID, err)
	}
	if imported {
		if err := h.jobManager.SaveJob(labID); err != nil {
			log.Printf("[portal] failed to persist imported events for lab %s: %v", labID, err)
		}
	}
}

// drainPortalOutboxForPage is drainPortalOutbox for an admin page about to show
// a lab's feedback or history: it bounds the wait, so an unreachable cluster
// costs the page a few seconds and a slightly older view rather than a hang.
func (h *Handler) drainPortalOutboxForPage(r *http.Request, labID string) {
	ctx, cancel := context.WithTimeout(r.Context(), portalDrainTimeout)
	defer cancel()
	h.drainPortalOutbox(ctx, labID)
}

// applyPortalRecord stores one outbox record, and reports whether it changed the
// lab's job (which then needs saving). An error leaves the record queued for the
// next drain, so only a failure worth retrying is returned as one: a record that
// can never be applied is logged and dropped instead.
//
// labID is the lab being drained and overrides whatever the record claims. The
// record was written by a process students talk to; it must not be able to file
// feedback or events under another lab.
func (h *Handler) applyPortalRecord(labID, raw string) (bool, error) {
	var record portalRecord
	if err := json.Unmarshal([]byte(raw), &record); err != nil {
		log.Printf("[portal] dropping malformed outbox record of lab %s: %v", labID, err)
		return false, nil
	}

	switch {
	case record.Kind == portalRecordFeedback && record.Feedback != nil:
		if h.feedbackStore == nil {
			return false, nil
		}
		f := *record.Feedback
		f.LabID = labID
		if _, err := h.feedbackStore.AddIfAbsent(f); err != nil {
			return false, fmt.Errorf("failed to import feedback: %w", err)
		}
		return false, nil

	case record.Kind == portalRecordWorkspaceEvent && record.WorkspaceEvent != nil:
		added, err := h.jobManager.ImportWorkspaceEvent(labID, *record.WorkspaceEvent)
		if err != nil {
			return false, fmt.Errorf("failed to import workspace event: %w", err)
		}
		return added, nil

	case record.Kind == portalRecordAudit && record.Audit != nil:
		if h.auditStore == nil {
			return false, nil
		}
		entry := *record.Audit
		entry.LabID = labID
		// A portal only ever acts on behalf of students.
		entry.Role = "student"
		if err := h.auditStore.Record(entry); err != nil {
			return false, fmt.Errorf("failed to import audit entry: %w", err)
		}
		return false, nil

	default:
		log.Printf("[portal] dropping outbox record of unknown kind %q for lab %s", record.Kind, labID)
		return false, nil
	}
}

// removePortal deletes a lab's portal from its cluster, after collecting what it
// still holds. Best-effort, and deliberately not gated on the lab's StudentPortal
// flag: it is also how a portal that was just switched off is cleaned up.
func (h *Handler) removePortal(labID string) {
	job, exists := h.jobManager.GetJob(labID)
	if !exists {
		return
	}
	target := portalTargetFor(job)
	pd, _, ok := h.portalDeployerFor(labID, target)
	if !ok {
		return
	}

	h.drainPortalOutbox(context.Background(), labID)

	unlock := h.lockPortal(labID)
	defer unlock()

	ctx, cancel := context.WithTimeout(context.Background(), clusterOperationTimeout)
	defer cancel()
	if err := pd.RemovePortal(ctx, labID); err != nil {
		log.Printf("[portal] failed to remove student portal of lab %s: %v", labID, err)
		return
	}
	h.setPortalError(labID, "")
	h.setPortalURL(labID, "")
}

// PortalDisplay is what the lab detail page shows about a lab's student portal.
type PortalDisplay struct {
	// Supported is false when the lab has no cluster to deploy a portal to yet.
	Supported bool
	Enabled   bool
	Deployed  bool
	Ready     bool
	URL       string
	Image     string
	// Error is the last deployment failure, "" when the last reconcile succeeded.
	Error string
	// BrokeredLogin is false when this instance has no public URL configured, in
	// which case the portal can only offer password login.
	BrokeredLogin bool
	// CentralPortal is whether this instance serves students itself, which is
	// where a lab without a portal of its own sends them.
	CentralPortal bool
	// HasPassword is whether this lab's portal has a password of its own yet. The
	// password itself is never put on the page — it is fetched on demand through
	// GetLabPortalPassword, so it stays out of the HTML and out of the audit trail
	// until an admin actually asks for it.
	HasPassword bool
}

// portalDisplayFor reads a lab's portal state for the lab detail page.
func (h *Handler) portalDisplayFor(ctx context.Context, job *Job) PortalDisplay {
	target := portalTargetFor(job)
	job.mu.RLock()
	hasPassword := job.Config != nil && job.Config.StudentPortalPassword != ""
	job.mu.RUnlock()
	display := PortalDisplay{
		Enabled:       target.enabled,
		Supported:     target.completed && target.kubeconfig != "",
		Error:         h.portalError(job.ID),
		BrokeredLogin: h.effectivePublicURL() != "",
		CentralPortal: h.mode != ModeAdmin,
		HasPassword:   hasPassword,
	}
	if !display.Supported || !display.Enabled {
		return display
	}
	pd, _, ok := h.portalDeployerFor(job.ID, target)
	if !ok {
		display.Supported = false
		return display
	}
	ctx, cancel := context.WithTimeout(ctx, portalDrainTimeout)
	defer cancel()
	status, err := pd.PortalStatus(ctx, job.ID)
	if err != nil {
		log.Printf("[portal] failed to read portal status of lab %s: %v", job.ID, err)
		return display
	}
	display.Deployed = status.Deployed
	display.Ready = status.Ready
	display.URL = status.URL
	display.Image = status.Image
	return display
}

// SetLabPortal turns a lab's in-lab student portal on or off, or redeploys it
// (POST api/labs/{id}/portal, enabled=true|false). Unlike the background
// reconcile it runs the deployment before answering, so the admin sees its
// outcome.
func (h *Handler) SetLabPortal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	pathParts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	// Expect: api/labs/{id}/portal or api/jobs/{id}/portal
	if len(pathParts) != 4 || (pathParts[1] != "labs" && pathParts[1] != "jobs") {
		writeJSONError(w, http.StatusBadRequest, "Invalid path")
		return
	}
	labID := pathParts[2]

	job, exists := h.jobManager.GetJob(labID)
	if !exists {
		writeJSONError(w, http.StatusNotFound, "Lab not found")
		return
	}

	if err := r.ParseForm(); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid request")
		return
	}
	enabled, err := strconv.ParseBool(strings.TrimSpace(r.FormValue("enabled")))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "enabled must be true or false")
		return
	}

	target := portalTargetFor(job)
	if enabled && (!target.completed || target.kubeconfig == "") {
		writeJSONError(w, http.StatusConflict, "The lab must be deployed before it can host a student portal")
		return
	}

	// Set directly rather than through updateJobConfig: that one reconciles in
	// the background, and this request reports the deployment's outcome itself.
	job.mu.Lock()
	if job.Config == nil {
		job.mu.Unlock()
		writeJSONError(w, http.StatusConflict, "Lab has no configuration")
		return
	}
	job.Config.StudentPortal = enabled
	job.mu.Unlock()

	action := "lab.portal.disable"
	if enabled {
		action = "lab.portal.enable"
		h.reconcilePortal(labID)
	} else {
		h.removePortal(labID)
	}
	if err := h.jobManager.SaveJob(labID); err != nil {
		log.Printf("Failed to persist student portal change for lab %s: %v", labID, err)
	}
	h.recordAudit(adminActor(r), "admin", action, labID, "")

	if portalErr := h.portalError(labID); enabled && portalErr != "" {
		// The cause names cluster resources; it is on the lab detail page for the
		// admin who can act on it, and in the log.
		writeJSONError(w, http.StatusBadGateway, "The student portal could not be deployed. See the lab's Student portal section for details.")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "enabled": enabled})
}

// labPortalPasswordLabID reads the lab ID out of an /api/labs/{id}/portal/password
// path, shared by the two handlers below.
func labPortalPasswordLabID(path string) (string, bool) {
	pathParts := strings.Split(strings.Trim(path, "/"), "/")
	// Expect: api/labs/{id}/portal/password or api/jobs/{id}/portal/password
	if len(pathParts) != 5 || (pathParts[1] != "labs" && pathParts[1] != "jobs") {
		return "", false
	}
	return pathParts[2], true
}

// GetLabPortalPassword returns the password students type on this lab's own
// student portal, so an admin can hand it out without shell access to the lab.
// It is the one password that lab's students share — distinct from a student's
// own per-workspace IDE password, which is personal and never shown here.
//
// Admin-only (registered behind RequireAuth) and audited, like the instance's
// own portal password in GetStudentPortalPassword.
func (h *Handler) GetLabPortalPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	labID, ok := labPortalPasswordLabID(r.URL.Path)
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "Invalid path")
		return
	}
	job, exists := h.jobManager.GetJob(labID)
	if !exists {
		writeJSONError(w, http.StatusNotFound, "Lab not found")
		return
	}

	job.mu.RLock()
	password := ""
	if job.Config != nil {
		password = job.Config.StudentPortalPassword
	}
	job.mu.RUnlock()

	h.recordAudit(adminActor(r), "admin", "lab.portal_password.view", labID, "")

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"password": password,
		"set":      password != "",
	})
}

// SetLabPortalPassword replaces a lab portal's password with one the admin
// chose, or — when the form carries no password — rotates it to a freshly
// generated one.
//
// The new password reaches the deployed portal through the state sync, so it is
// in force within a reconcile cycle. Students already signed in keep their
// sessions; the next sign-in needs the new password.
func (h *Handler) SetLabPortalPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	labID, ok := labPortalPasswordLabID(r.URL.Path)
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "Invalid path")
		return
	}
	job, exists := h.jobManager.GetJob(labID)
	if !exists {
		writeJSONError(w, http.StatusNotFound, "Lab not found")
		return
	}

	if err := r.ParseForm(); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid request")
		return
	}

	password := strings.TrimSpace(r.FormValue("password"))
	rotated := password == ""
	if rotated {
		generated, err := GeneratePortalPassword()
		if err != nil {
			log.Printf("Failed to generate portal password for lab %s: %v", labID, err)
			writeJSONError(w, http.StatusInternalServerError, "Failed to generate a password")
			return
		}
		password = generated
	} else if len(password) < minPortalPasswordLength {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("The password must be at least %d characters", minPortalPasswordLength))
		return
	}

	hash, err := HashStudentPassword(password)
	if err != nil {
		log.Printf("Failed to hash portal password for lab %s: %v", labID, err)
		writeJSONError(w, http.StatusInternalServerError, "Failed to set the password")
		return
	}

	job.mu.Lock()
	if job.Config == nil {
		job.mu.Unlock()
		writeJSONError(w, http.StatusConflict, "Lab has no configuration")
		return
	}
	job.Config.StudentPortalPassword = password
	job.Config.StudentPortalPasswordHash = hash
	job.mu.Unlock()

	if err := h.jobManager.SaveJob(labID); err != nil {
		log.Printf("Failed to persist portal password for lab %s: %v", labID, err)
		writeJSONError(w, http.StatusInternalServerError, "Failed to save the password")
		return
	}
	// Reaches the lab's deployed portal, and is a no-op for a lab without one.
	go h.syncPortalState(labID)

	action := "lab.portal_password.set"
	if rotated {
		action = "lab.portal_password.rotate"
	}
	h.recordAudit(adminActor(r), "admin", action, labID, "")

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "password": password})
}
