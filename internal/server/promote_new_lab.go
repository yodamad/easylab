package server

import (
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strings"

	"easylab/internal/clusterscan"
)

// Promoting a template to a lab that already exists is PromoteTemplate's job
// (promote.go). This file covers the other half, which is the common case: the
// lab the workshop is moving to does not exist yet.
//
// Rather than ask the admin to walk the seven-step New Lab wizard and re-enter
// everything the source lab already says, this page asks for the two things that
// genuinely differ between two editions of a workshop — which cluster, and which
// URL — and carries the rest over from the source lab as hidden fields.
//
// Those hidden fields are the wizard's own form field names, and the page submits
// to /api/labs, so processLabRequest builds the lab exactly as it would any other.
// Nothing about lab creation is reimplemented here: DNS profile resolution,
// validateDNSConfig, kubeconfigFromFormOrProfile, adoptSharedCluster,
// pendingSecrets, the audit entry and the Pulumi run are all the existing ones.

// promoteDetectContainerID is the one element that carries the cluster-shaped
// settings into the form, and the target the detect fragment replaces whole.
//
// It is a single container, swapped by outerHTML, for a reason worth keeping:
// createLabConfigFromForm reads these through r.FormValue, which returns the
// *first* value for a name. If the page carried a default install_nginx_ingress
// elsewhere in the form and the scan appended another, the default would silently
// win over what was found on the cluster — and EasyLab would install a second
// ingress controller over the top of the one already there. One container means
// one copy of each name, whatever order the DOM ends up in.
const promoteDetectContainerID = "promote-detect"

// promoteInheritedSettings is what the new lab takes from the source lab without
// the admin having to type it again. Deliberately only the settings that describe
// the *workshop* rather than the infrastructure it ran on: a cluster, a domain and
// DNS credentials are what the promotion is changing.
type promoteInheritedSettings struct {
	WorkspaceNamespace          string
	WorkspaceLifetimeHours      int
	WorkspaceIdleTimeoutMinutes int
	StudentPortal               bool
	AutoBake                    bool
}

// promoteNewLabView is what web/promote-new-lab.html renders.
type promoteNewLabView struct {
	SourceLabID    string
	SourceStack    string
	SourceStatus   string
	TemplateName   string
	TemplateIDE    string
	TemplateImage  string
	TemplateRepo   string
	IsDevcontainer bool

	// SuggestedStackName is a free name derived from the source lab's, so
	// promoting the same template twice does not silently produce two labs
	// called the same thing.
	SuggestedStackName string
	// TemplatesYAML is the single cloned template, for the templates_yaml field
	// that processLabRequest's YAML path parses.
	TemplatesYAML string

	Inherited promoteInheritedSettings
	// CredentialNames are the credentials the template expects to find in the new
	// lab's cluster. They are not collected here — see the comment on the warning
	// in the page — only named.
	CredentialNames []string

	ClusterProfiles       []wizardClusterProfile
	ClusterProfilesLocked bool
	DNSProfiles           []wizardDNSProfile
	DNSProfilesLocked     bool
	// SelectedDNSProfileID preselects the profile the source lab was built with,
	// when it still exists. The profile's *name* is all the source lab records, so
	// this is resolved back to an ID here.
	SelectedDNSProfileID string
	// DNSZone and AcmeEmail are offered as defaults: a new edition of a workshop
	// usually lives in the same zone and is requested by the same person.
	DNSZone   string
	AcmeEmail string

	// DetectContainer is the pre-scan state of the detect container: the cluster
	// settings at their safe defaults, ready to be replaced by a scan.
	DetectContainer template.HTML

	Storage credentialStorageView
}

// ServePromoteToNewLab renders the page that promotes one template into a lab that
// does not exist yet. GET /admin/promote?lab_id=<id>&template=<name>
//
// The source lab is only read, so — as with PromoteTemplate — it does not have to
// be up: a destroyed or failed lab can still be promoted from. (The entry point on
// the lab detail page needs a reachable cluster to render at all, but a bookmarked
// or shared link does not, so this tolerates any status and names it on the page.)
//
// Query parameters rather than path segments, both because /labs/ is a catch-all
// whose ServeLabDetail requires exactly two path segments, and because it keeps an
// admin-chosen template name out of the routing entirely.
func (h *Handler) ServePromoteToNewLab(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	sourceID := strings.TrimSpace(r.URL.Query().Get("lab_id"))
	templateName := strings.TrimSpace(r.URL.Query().Get("template"))
	if sourceID == "" || templateName == "" {
		http.Error(w, "lab_id and template are required", http.StatusBadRequest)
		return
	}

	source, exists := h.jobManager.GetJob(sourceID)
	if !exists {
		http.Error(w, "Lab not found", http.StatusNotFound)
		return
	}

	// Cloned under the source's read lock: the template's maps and slices belong
	// to it, and the copy must share nothing with them.
	source.mu.RLock()
	var original *WorkspaceTemplate
	sourceStack := ""
	sourceStatus := string(source.Status)
	var inherited promoteInheritedSettings
	dnsProfileName, dnsZone, acmeEmail := "", "", ""
	if source.Config != nil {
		cfg := source.Config
		sourceStack = cfg.StackName
		dnsProfileName = cfg.DNSProfile
		dnsZone = cfg.DNSZone
		acmeEmail = cfg.AcmeEmail
		inherited = promoteInheritedSettings{
			WorkspaceNamespace:          cfg.WorkspaceNamespace,
			WorkspaceLifetimeHours:      cfg.WorkspaceLifetimeHours,
			WorkspaceIdleTimeoutMinutes: cfg.WorkspaceIdleTimeoutMinutes,
			StudentPortal:               cfg.StudentPortal,
			AutoBake:                    cfg.AutoBake,
		}
		for _, t := range cfg.GetWorkspaceTemplates() {
			if t.Name == templateName {
				original = &t
				break
			}
		}
	}
	var promoted WorkspaceTemplate
	var cloneErr error
	if original != nil {
		promoted, cloneErr = cloneWorkspaceTemplate(*original)
	}
	source.mu.RUnlock()

	if original == nil {
		http.Error(w, "Template not found", http.StatusNotFound)
		return
	}
	if cloneErr != nil {
		log.Printf("ServePromoteToNewLab: lab %s: %v", sourceID, cloneErr)
		http.Error(w, "This template could not be read", http.StatusInternalServerError)
		return
	}

	templatesYAML, err := marshalWorkspaceTemplatesYAML([]WorkspaceTemplate{promoted})
	if err != nil {
		log.Printf("ServePromoteToNewLab: failed to render template %q of lab %s: %v", templateName, sourceID, err)
		http.Error(w, "This template could not be read", http.StatusInternalServerError)
		return
	}

	if inherited.WorkspaceNamespace == "" {
		inherited.WorkspaceNamespace = defaultWorkspaceNamespace
	}
	// A lab whose portal image this build does not ship cannot have a portal, so
	// inheriting the flag would promise one that never appears. Same gate as
	// ServeAdminUI's PortalAvailable.
	if inherited.StudentPortal && h.effectivePortalImage() == "" {
		inherited.StudentPortal = false
	}

	view := promoteNewLabView{
		SourceLabID:        sourceID,
		SourceStack:        sourceStack,
		SourceStatus:       sourceStatus,
		TemplateName:       promoted.Name,
		TemplateIDE:        promoted.IDE,
		TemplateImage:      promoted.Image,
		TemplateRepo:       promoted.GitRepo,
		IsDevcontainer:     promoted.Devcontainer != nil && promoted.Devcontainer.Enabled,
		SuggestedStackName: h.suggestedPromoteStackName(sourceStack, promoted.Name),
		TemplatesYAML:      templatesYAML,
		Inherited:          inherited,
		CredentialNames:    referencedCredentialNames([]WorkspaceTemplate{promoted}),
		DNSZone:            dnsZone,
		AcmeEmail:          acmeEmail,
		// Pre-scan: the cluster settings at their safe defaults.
		DetectContainer: template.HTML(renderPromoteDetect(nil, clusterSuggestion{}, false, "")),
		Storage:         h.credentialStorageView(),
	}
	view.ClusterProfiles, view.ClusterProfilesLocked = h.wizardClusterProfiles()
	view.DNSProfiles, view.DNSProfilesLocked = h.wizardDNSProfiles()
	view.SelectedDNSProfileID = dnsProfileIDByName(view.DNSProfiles, dnsProfileName)

	h.serveTemplate(w, "promote-new-lab.html", view)
}

// dnsProfileIDByName finds the saved profile the source lab was built with. A lab
// records the profile's name, not its ID, and the profile may since have been
// renamed or deleted — in which case nothing is preselected and the admin picks.
func dnsProfileIDByName(profiles []wizardDNSProfile, name string) string {
	if strings.TrimSpace(name) == "" {
		return ""
	}
	for _, p := range profiles {
		if p.Name == name {
			return p.ID
		}
	}
	return ""
}

// suggestedPromoteStackNameFrom offers a lab name that is not already taken.
// Nothing in EasyLab enforces unique stack names and this does not start: it is a
// default the admin can overwrite. But a stack name is the prefix every cloud
// resource is named from, so handing over one that is visibly in use would be a
// poor default.
func suggestedPromoteStackNameFrom(base string, taken map[string]bool) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "promoted"
	}
	if !taken[base] {
		return base
	}
	for i := 2; i < 100; i++ {
		candidate := fmt.Sprintf("%s-%d", base, i)
		if !taken[candidate] {
			return candidate
		}
	}
	return base
}

func (h *Handler) suggestedPromoteStackName(sourceStack, templateName string) string {
	base := strings.TrimSpace(sourceStack)
	if base == "" {
		base = sanitizeDNSName(templateName)
	}

	taken := make(map[string]bool)
	for _, job := range h.jobManager.GetAllJobs() {
		job.mu.RLock()
		if job.Config != nil {
			taken[job.Config.StackName] = true
		}
		job.mu.RUnlock()
	}
	return suggestedPromoteStackNameFrom(base, taken)
}

// recordPromotedLabAudit records the promotion on a lab the promotion itself
// created, so the audit log answers "where did this template come from" for such a
// lab exactly as it does for one promoted into a lab that already existed.
//
// Called from processLabRequest, where the new lab's id is known. A no-op unless
// the request carries promoted_from, which only this page's form sends — so every
// other creation path behaves identically.
func (h *Handler) recordPromotedLabAudit(r *http.Request, jobID string) {
	source := strings.TrimSpace(getFormValue(r, "promoted_from"))
	tmpl := strings.TrimSpace(getFormValue(r, "promoted_template"))
	if source == "" || tmpl == "" {
		return
	}
	// Bounded before they reach the detail. These two come off a form parsed at
	// maxUploadSizeLarge, and the audit log is an append-only file that
	// AuditStore.Recent reads whole — so an unbounded value here would grow every
	// later read of the log, not just this one entry.
	h.recordAudit(adminActor(r), "admin", "lab.template_promote", jobID,
		fmt.Sprintf("%s from lab %s", truncateAuditValue(tmpl), truncateAuditValue(source)))
}

// maxPromoteAuditValueLength bounds one value inside an audit detail. Generous
// next to a template name or a job id, both of which are far shorter.
const maxPromoteAuditValueLength = 200

func truncateAuditValue(s string) string {
	runes := []rune(s)
	if len(runes) <= maxPromoteAuditValueLength {
		return s
	}
	return string(runes[:maxPromoteAuditValueLength]) + "…"
}

// DetectClusterForPromote scans the cluster the new lab would run on and answers
// with the detect container: what is on the cluster, plus the settings worth taking
// from it as the hidden inputs the page's form submits.
//
// It is DetectCluster's twin (cluster_detect.go) and shares its scan and its
// suggestion mapping; what differs is the answer. The wizard's JSON reply is
// rendered by ~450 lines of JavaScript bound to the wizard object, so this page
// renders server-side instead and leaves that coupling where it is.
//
// Every failure path still renders the container with its defaults: the container
// *is* where install_nginx_ingress and install_cert_manager live, and dropping it
// would leave the form with neither — which createLabConfigFromForm reads as
// "something is already installed on this cluster".
//
// POST /api/promote/detect-cluster
func (h *Handler) DetectClusterForPromote(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	if err := h.parseForm(w, r, maxUploadSizeSmall); err != nil {
		log.Printf("Promote cluster detect: failed to parse the form: %v", err)
		fmt.Fprint(w, renderPromoteDetect(nil, clusterSuggestion{}, false, "The request could not be read."))
		return
	}

	// A DNS-01 ClusterIssuer is only worth adopting if this lab has a DNS provider
	// to answer challenges with, which on this page means a saved DNS profile.
	hasDNSProvider := strings.TrimSpace(getFormValue(r, "dns_profile")) != ""

	// Resolved here rather than in the browser, so a saved kubeconfig never
	// reaches it.
	kubeconfig, _, err := h.kubeconfigFromFormOrProfile(r)
	if err != nil {
		log.Printf("Promote cluster detect: failed to read the submitted kubeconfig: %v", err)
		fmt.Fprint(w, renderPromoteDetect(nil, clusterSuggestion{}, hasDNSProvider, clusterProfileErrorMessage(err)))
		return
	}
	if strings.TrimSpace(kubeconfig) == "" {
		fmt.Fprint(w, renderPromoteDetect(nil, clusterSuggestion{}, hasDNSProvider,
			"Pick a saved cluster, or upload or paste a kubeconfig, then run the detection."))
		return
	}

	report, probeErrs := h.scanCluster(r.Context(), kubeconfig, clusterscan.Options{
		WorkspaceNamespace: getFormValue(r, "workspace_namespace"),
	})

	invalidKubeconfig := false
	for _, probeErr := range probeErrs {
		if errors.Is(probeErr, clusterscan.ErrInvalidKubeconfig) {
			// Logged without the error: a parse failure can quote the file's own
			// bytes, credentials included.
			invalidKubeconfig = true
			log.Print("Promote cluster detect: the submitted kubeconfig could not be parsed")
			continue
		}
		log.Printf("Promote cluster detect probe: %v", probeErr)
	}

	if report == nil {
		message := "Could not reach the cluster with this kubeconfig — check the server URL, and that this machine can reach it."
		if invalidKubeconfig {
			message = "This does not look like a usable kubeconfig — check that it has a server URL and credentials."
		}
		fmt.Fprint(w, renderPromoteDetect(nil, clusterSuggestion{}, hasDNSProvider, message))
		return
	}

	h.recordAudit(adminActor(r), "admin", "cluster.detect", "", auditDetailForScan(report))
	fmt.Fprint(w, renderPromoteDetect(report, suggestedFromReport(report), hasDNSProvider, ""))
}

// promoteFinding is one row of the detect readout: what was looked for, what the
// cluster holds, and how the check went.
type promoteFinding struct {
	label string
	probe clusterscan.Probe
	value string
	state string
	// plain drops the monospace styling, for a value that is prose ("15 checks")
	// rather than an object name the admin could paste after kubectl get.
	plain bool
}

// promoteDetectBadge maps a probe status onto the three-way display the wizard
// uses: found / not found / couldn't check. "absent" is a real answer from the
// cluster; "forbidden", "timeout" and "error" say nothing about it.
func promoteDetectBadge(status clusterscan.Status) (key, text string) {
	switch status {
	case clusterscan.StatusOK:
		return "ok", "found"
	case clusterscan.StatusAbsent, clusterscan.StatusSkipped:
		return "absent", "not found"
	default:
		return "unknown", "couldn't check"
	}
}

// renderPromoteDetect builds the detect container in whichever of its three states
// applies: before a scan (rep nil, errMsg empty), after a failed one (errMsg set),
// or after a successful one. All three carry the cluster settings, so the form is
// never left without them.
//
// The markup is the same the wizard's detect readout produces, so it reuses the
// .cluster-detect* and .finding* classes already in style.css. Every value is
// escaped: a cluster's object names are not under EasyLab's control.
func renderPromoteDetect(rep *clusterscan.Report, s clusterSuggestion, hasDNSProvider bool, errMsg string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<div id="%s" class="cluster-detect-result">`, promoteDetectContainerID)

	switch {
	case errMsg != "":
		fmt.Fprintf(&b, `<div class="error-message"><h3>Could not read this cluster</h3><p>%s</p></div>`,
			template.HTMLEscapeString(errMsg))
	case rep == nil:
		b.WriteString(`<p class="hint-text">Not scanned yet. Without a scan the lab is created as if the ` +
			`cluster were empty: EasyLab installs its own ingress controller and cert-manager.</p>`)
	default:
		b.WriteString(renderPromoteDetectReport(rep))
	}

	hidden, applied := promoteDetectHiddenInputs(s, hasDNSProvider)
	b.WriteString(hidden)

	if rep != nil && errMsg == "" {
		if len(applied) > 0 {
			fmt.Fprintf(&b, `<small class="hint-text">Creating the lab will reuse %s.</small>`,
				template.HTMLEscapeString(promoteJoinClause(applied)))
		} else {
			b.WriteString(`<small class="hint-text">Nothing on this cluster can be reused, so EasyLab ` +
				`installs its own ingress controller and cert-manager.</small>`)
		}
		if s.StorageClass != "" {
			fmt.Fprintf(&b, `<small class="hint-text">This cluster's default StorageClass keeps volumes on one node. `+
				`Set <code>storage_class: %s</code> on the template below, or a workspace loses its files the `+
				`first time its pod moves.</small>`,
				template.HTMLEscapeString(s.StorageClass))
		}
	}

	b.WriteString(`</div>`)
	return b.String()
}

// renderPromoteDetectReport renders the header, the findings and the warnings of a
// successful scan.
func renderPromoteDetectReport(rep *clusterscan.Report) string {
	var b strings.Builder

	title := "Couldn't read this cluster"
	meta := rep.Cluster.Detail
	if rep.Cluster.OK() {
		title = "Detected on this cluster"
		var parts []string
		if rep.Cluster.Version != "" {
			parts = append(parts, "Kubernetes "+rep.Cluster.Version)
		}
		if rep.Nodes.OK() {
			parts = append(parts, fmt.Sprintf("%d %s", rep.Nodes.Count, promotePlural(rep.Nodes.Count, "node", "nodes")))
		}
		meta = strings.Join(parts, ", ")
	}
	b.WriteString(`<div class="cluster-detect-header">`)
	fmt.Fprintf(&b, `<span class="cluster-detect-title">%s</span>`, template.HTMLEscapeString(title))
	fmt.Fprintf(&b, `<span class="cluster-detect-meta">%s</span>`, template.HTMLEscapeString(meta))
	b.WriteString(`</div>`)

	b.WriteString(`<ul class="cluster-detect-findings">`)
	for _, f := range promoteFindings(rep) {
		b.WriteString(renderPromoteFinding(f))
	}
	b.WriteString(`</ul>`)

	if len(rep.Warnings) > 0 {
		b.WriteString(`<div class="warning-message"><h3>Worth knowing</h3><ul>`)
		for _, warn := range rep.Warnings {
			fmt.Fprintf(&b, `<li>%s</li>`, template.HTMLEscapeString(warn.Message))
		}
		b.WriteString(`</ul></div>`)
	}

	return b.String()
}

// promoteFindings builds the readout's rows. Mirrors the wizard's eight rows, and
// like it treats "the probe worked but found nothing" as "not found" rather than
// as a value.
func promoteFindings(rep *clusterscan.Report) []promoteFinding {
	ing := rep.Ingress
	findings := []promoteFinding{{
		label: "Ingress controller",
		probe: ing.Probe,
		value: promoteOrQuestionMark(ing.Namespace) + "/" + promoteOrQuestionMark(ing.Service),
		state: promotePrefixed("class ", ing.Class),
	}}

	// The address gets a row of its own: it is what DNS points at, and a hostname
	// where an IP was expected rules some setups out entirely.
	address := ing.Probe
	if ing.OK() && ing.ExternalIP == "" && ing.ExternalHost == "" {
		address = clusterscan.Probe{Status: clusterscan.StatusAbsent, Detail: ing.Detail}
	}
	addressState := ""
	if ing.ExternalIP == "" {
		addressState = "hostname only, no IP"
	}
	findings = append(findings, promoteFinding{
		label: "External address",
		probe: address,
		value: promoteFirstNonEmpty(ing.ExternalIP, ing.ExternalHost),
		state: addressState,
	})

	cm := rep.CertManager.Probe
	if rep.CertManager.OK() && !rep.CertManager.Installed {
		cm = clusterscan.Probe{Status: clusterscan.StatusAbsent, Detail: rep.CertManager.Detail}
	}
	findings = append(findings, promoteFinding{label: "cert-manager", probe: cm, value: rep.CertManager.Namespace})

	findings = append(findings, promoteIssuerFinding(rep))

	storage := rep.Storage.Probe
	if rep.Storage.OK() && rep.Storage.Default == "" {
		storage = clusterscan.Probe{Status: clusterscan.StatusAbsent, Detail: rep.Storage.Detail}
	}
	storageState := ""
	if rep.Storage.NodeLocal {
		storageState = "node-local"
	}
	findings = append(findings, promoteFinding{
		label: "Default StorageClass",
		probe: storage,
		value: rep.Storage.Default,
		state: storageState,
	})

	nodes := rep.Nodes.Probe
	if rep.Nodes.OK() && len(rep.Nodes.Labels) == 0 {
		nodes = clusterscan.Probe{Status: clusterscan.StatusAbsent, Detail: rep.Nodes.Detail}
	}
	labelValue, labelState := "", ""
	if len(rep.Nodes.Labels) > 0 {
		top := rep.Nodes.Labels[0]
		labelValue = top.Key + "=" + strings.Join(top.Values, "|")
		if n := len(rep.Nodes.Labels); n > 1 {
			labelState = fmt.Sprintf("%d keys", n)
		}
	}
	findings = append(findings, promoteFinding{label: "Node labels", probe: nodes, value: labelValue, state: labelState})

	nsState := "exists"
	if rep.Namespaces.OK() && !rep.Namespaces.WorkspaceExists {
		nsState = "will be created"
	}
	findings = append(findings, promoteFinding{
		label: "Workspace namespace",
		probe: rep.Namespaces.Probe,
		value: rep.Namespaces.WorkspaceName,
		state: nsState,
	})

	perms := rep.Permissions
	permState := "all granted"
	if n := len(perms.Missing); n > 0 {
		permState = fmt.Sprintf("%d missing", n)
	}
	permValue := ""
	if n := len(perms.Checks); n > 0 {
		permValue = fmt.Sprintf("%d %s", n, promotePlural(n, "check", "checks"))
	}
	findings = append(findings, promoteFinding{
		label: "Permissions",
		probe: perms.Probe,
		value: permValue,
		state: permState,
		plain: true,
	})

	return findings
}

// promoteIssuerFinding names the ClusterIssuer that would actually be used, and
// says why the others are not.
func promoteIssuerFinding(rep *clusterscan.Report) promoteFinding {
	probe := rep.Issuers.Probe
	items := rep.Issuers.Items
	if rep.Issuers.OK() && len(items) == 0 {
		probe = clusterscan.Probe{Status: clusterscan.StatusAbsent, Detail: rep.Issuers.Detail}
	}

	var shown *clusterscan.ClusterIssuerInfo
	for i := range items {
		if items[i].Usable() {
			shown = &items[i]
			break
		}
	}
	if shown == nil && len(items) > 0 {
		shown = &items[0]
	}
	if shown == nil {
		return promoteFinding{label: "ClusterIssuer", probe: probe}
	}

	value := shown.Name
	if n := len(items); n > 1 {
		value += fmt.Sprintf(" +%d", n-1)
	}
	var tags []string
	switch {
	case shown.HasDNS01:
		tags = append(tags, "DNS-01")
	case shown.HasHTTP01:
		tags = append(tags, "HTTP-01 only")
	}
	if shown.Staging {
		tags = append(tags, "staging")
	}
	if shown.Ready {
		tags = append(tags, "ready")
	} else {
		tags = append(tags, "not ready")
	}
	return promoteFinding{label: "ClusterIssuer", probe: probe, value: value, state: strings.Join(tags, ", ")}
}

// renderPromoteFinding renders one row. A found row leaves the state column empty
// unless it can qualify the value: a monospace value already proves it was found.
func renderPromoteFinding(f promoteFinding) string {
	key, text := promoteDetectBadge(f.probe.Status)

	valueClass := "finding-value"
	if f.plain {
		valueClass += " finding-value-plain"
	}
	value := f.value
	if f.probe.Status != clusterscan.StatusOK || value == "" {
		// An em dash reads as "nothing here" without posing as a value.
		value = "—"
		valueClass = "finding-value finding-value-plain finding-value-empty"
	}

	state := text
	if f.probe.Status == clusterscan.StatusOK {
		state = f.state
	}

	titleAttr := ""
	if f.probe.Detail != "" {
		titleAttr = fmt.Sprintf(` title="%s"`, template.HTMLEscapeString(f.probe.Detail))
	}

	return fmt.Sprintf(`<li class="finding finding-%s">`+
		`<span class="finding-label">%s</span>`+
		`<span class="%s">%s</span>`+
		`<span class="finding-state"%s>%s</span>`+
		`</li>`,
		key,
		template.HTMLEscapeString(f.label),
		valueClass, template.HTMLEscapeString(value),
		titleAttr, template.HTMLEscapeString(state))
}

// promoteDetectHiddenInputs turns the scan's suggestions into the hidden inputs
// the form submits, and the plain-English list of what they reuse.
//
// The two install flags are always emitted, because createLabConfigFromForm reads
// an absent one as false — which means "it is already installed here". Defaulting
// them to "true" is what keeps a scan that found nothing, or never ran, from
// quietly claiming an ingress controller the cluster does not have.
//
// A suggestion the scan did not make falls back to that default:
// suggestedFromReport is deliberately silent where a probe was denied, and turning
// that silence into "reuse" would be a decision nobody made.
//
// Two settings are deliberately not emitted here. workspace_namespace belongs to
// its own visible input on the page — the scan reports on it instead, so there is
// never a second copy of the name. storage_class is not a lab setting at all; it
// goes on a workspace template, so it is only named in the readout.
func promoteDetectHiddenInputs(s clusterSuggestion, hasDNSProvider bool) (string, []string) {
	var b strings.Builder
	var applied []string

	hidden := func(name, value string) {
		fmt.Fprintf(&b, `<input type="hidden" name="%s" value="%s">`,
			template.HTMLEscapeString(name), template.HTMLEscapeString(value))
	}

	reuseIngress := s.InstallNginxIngress != nil && !*s.InstallNginxIngress
	hidden("install_nginx_ingress", promoteBoolValue(!reuseIngress))
	if reuseIngress {
		hidden("nginx_ingress_namespace", s.NginxIngressNamespace)
		hidden("nginx_ingress_service_name", s.NginxIngressServiceName)
		hidden("ingress_class_name", s.IngressClassName)
		applied = append(applied, "the ingress controller already on this cluster")
	}

	reuseCertManager := s.InstallCertManager != nil && !*s.InstallCertManager
	hidden("install_cert_manager", promoteBoolValue(!reuseCertManager))
	if reuseCertManager {
		hidden("cert_manager_namespace", s.CertManagerNamespace)
		applied = append(applied, "the cert-manager already on this cluster")
	}

	// Only alongside a reused cert-manager, and only with a DNS provider to answer
	// challenges: EasyLab requests certificates from this issuer by name instead
	// of creating one, and naming an issuer on a cert-manager it is also
	// installing cannot work.
	adoptIssuer := reuseCertManager && hasDNSProvider &&
		s.DNSAlreadyConfigured != nil && *s.DNSAlreadyConfigured && s.ClusterIssuerName != ""
	hidden("dns_already_configured", promoteBoolValue(adoptIssuer))
	if adoptIssuer {
		hidden("cluster_issuer_name", s.ClusterIssuerName)
		applied = append(applied, fmt.Sprintf("the existing ClusterIssuer %q", s.ClusterIssuerName))
	}

	return b.String(), applied
}

func promoteBoolValue(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func promotePlural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func promoteOrQuestionMark(s string) string {
	if s == "" {
		return "?"
	}
	return s
}

func promotePrefixed(prefix, s string) string {
	if s == "" {
		return ""
	}
	return prefix + s
}

func promoteFirstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// promoteJoinClause renders a list the way prose does: "a, b and c".
func promoteJoinClause(parts []string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	default:
		return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
	}
}
