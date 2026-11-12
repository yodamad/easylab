package server

import (
	"context"
	"fmt"
	"log"
	"maps"
	"net/url"
	"strings"
	"time"

	"easylab/ingress"
	"easylab/internal/clusterscan"
	"easylab/utils"

	"k8s.io/client-go/tools/clientcmd"
)

// Running two labs with two different domains on one Kubernetes cluster is a
// supported setup, but it only works once the two stop declaring the same
// objects. Every lab's Pulumi program creates the workspace namespace
// (k8s.InitNamespace) and, inside it, a wildcard Certificate named by the
// constant ingress.WildcardTLSSecretName. Two labs sharing that namespace
// collide on server-side apply — and worse than the error, they ask a single
// certificate to cover two unrelated domains, which no certificate can do.
//
// So a lab joining an occupied cluster is given a namespace of its own, and is
// pointed at the ingress controller, cert-manager and DNS-01 plumbing already
// installed there rather than installing a second copy over the top. Both
// decisions are taken here, at creation time, from the configuration of the labs
// already on that cluster — authoritative, since it describes the very cluster
// this lab is joining.
//
// Two things this deliberately does not fix, and warns about instead:
//
//   - ExternalDNS. Its namespace, credential Secret and Helm release are
//     fixed-name and are created outside the "DNS-01 already configured" guard
//     (ingress/https.go), so a second lab with ExternalDNS enabled still
//     conflicts. Fixing that means changing the Pulumi program.
//   - DNS credentials. Reusing the sitting lab's ClusterIssuer means reusing the
//     credentials it was built with, because one ClusterIssuer carries one
//     solver and one secret reference. A second domain in a zone those
//     credentials cannot write will never get a certificate.

// clusterNeighbour is a snapshot of one lab already sitting on the cluster a new
// lab is joining. The fields are copied out under the job's own lock rather than
// held as a pointer into live job state.
type clusterNeighbour struct {
	stackName            string
	domain               string
	namespace            string
	dnsProvider          string
	dnsZone              string
	clusterIssuerName    string
	certManagerNamespace string
	ingressNamespace     string
	ingressServiceName   string
	ingressClassName     string
	dnsCredentials       map[string]string
	usesExternalDNS      bool
}

// servesDNS01 reports whether this lab set up cert-manager's DNS-01 plumbing —
// the ClusterIssuer, the solver webhook and the credential secret — which is
// what a joining lab can reuse. A lab without a domain installs no cert-manager
// at all, and a lab without a DNS provider solves over HTTP-01 and creates no
// webhook or credential secret.
func (n clusterNeighbour) servesDNS01() bool {
	return n.domain != "" && n.dnsProvider != ""
}

// observedIssuers is what a look at the cluster itself found, which the job
// records cannot tell us.
//
// The distinction that matters is between "the cluster holds no such issuer" and
// "we could not find out": only the first is evidence of absence, and only the
// first may be acted on. A lookup that failed — unreachable cluster, no
// cert-manager CRDs, denied list — leaves known false, and every decision below
// falls back to what the job records say, exactly as it did before this check
// existed.
type observedIssuers struct {
	// known reports that the lookup succeeded, so absence is meaningful.
	known bool
	// usable holds the names of the ClusterIssuers that are Ready and carry a
	// DNS-01 solver. An issuer that exists but cannot solve DNS-01 is no more
	// reusable than one that is missing.
	usable map[string]bool
}

// has reports whether name is a ClusterIssuer this lab can reuse. It answers
// false for an unknown lookup, so callers must check known first where that
// difference matters.
func (o observedIssuers) has(name string) bool {
	return o.known && o.usable[name]
}

// newObservedIssuers records a successful lookup.
func newObservedIssuers(items []clusterscan.ClusterIssuerInfo) observedIssuers {
	usable := make(map[string]bool, len(items))
	for _, item := range items {
		if item.Usable() {
			usable[item.Name] = true
		}
	}
	return observedIssuers{known: true, usable: usable}
}

// clusterIssuerLookupTimeout bounds the one extra cluster read a lab creation
// makes. Lab creation is interactive, and a cluster that cannot answer in this
// long is better left unanswered — the caller degrades to the job records rather
// than keeping the admin waiting.
const clusterIssuerLookupTimeout = 15 * time.Second

// observeClusterIssuers looks up the ClusterIssuers on the cluster this lab is
// joining. A failure is not an error the admin should see: it only means the
// adoption decision falls back to the job records, which is what it did before
// this check existed, so it is logged and reported as unknown.
func (h *Handler) observeClusterIssuers(ctx context.Context, jobID string) observedIssuers {
	job, exists := h.jobManager.GetJob(jobID)
	if !exists {
		return observedIssuers{}
	}
	job.mu.RLock()
	var kubeconfig string
	if job.Config != nil {
		kubeconfig = job.Config.ExternalKubeconfig
	}
	job.mu.RUnlock()

	// No kubeconfig means this lab provisions its own cluster, where it is alone
	// by construction and there is nothing to adopt.
	if strings.TrimSpace(kubeconfig) == "" || h.listClusterIssuers == nil {
		return observedIssuers{}
	}

	ctx, cancel := context.WithTimeout(ctx, clusterIssuerLookupTimeout)
	defer cancel()

	items, err := h.listClusterIssuers(ctx, kubeconfig)
	if err != nil {
		log.Printf("Lab %s: could not list the cluster's ClusterIssuers, falling back to the other labs' "+
			"configuration to decide whether to reuse one: %v", jobID, err)
		return observedIssuers{}
	}
	return newObservedIssuers(items)
}

// clusterKey identifies the cluster a lab targets, so labs can be compared
// against one another. An empty key means the lab shares nothing: a lab that
// provisions its own cluster is alone on it by construction.
//
// The kubeconfig's server URL serves purely as an opaque comparison key. It is
// never logged, never rendered and never returned to a client.
func clusterKey(cfg *LabConfig) string {
	if cfg == nil || !cfg.UseExistingCluster {
		return ""
	}
	if kubeconfig := strings.TrimSpace(cfg.ExternalKubeconfig); kubeconfig != "" {
		if rest, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfig)); err == nil && rest.Host != "" {
			return "host:" + normalizeAPIServer(rest.Host)
		}
	}
	// An absent or unparseable kubeconfig leaves the saved profile name as the
	// next best identity: every lab picking the same profile lands on the same
	// cluster.
	if profile := strings.TrimSpace(cfg.ClusterProfile); profile != "" {
		return "profile:" + profile
	}
	return ""
}

// normalizeAPIServer reduces an API server URL to a form two kubeconfigs for the
// same cluster agree on. Without this the comparison is raw string equality, and
// a cluster reached as "https://API.example.com:443/" from one lab and
// "https://api.example.com" from another reads as two clusters — which would
// leave both labs in the same workspace namespace and, worse, let either one's
// destroy delete it out from under the other.
//
// It cannot catch every spelling (an IP and a DNS name for one cluster still
// differ), so it narrows the gap rather than closing it.
func normalizeAPIServer(host string) string {
	trimmed := strings.TrimSpace(host)
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" {
		return strings.ToLower(strings.TrimSuffix(trimmed, "/"))
	}

	scheme := strings.ToLower(parsed.Scheme)
	hostname := strings.ToLower(parsed.Hostname())
	port := parsed.Port()
	// The default port for the scheme and no port at all are the same endpoint.
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		hostname = hostname + ":" + port
	}
	return scheme + "://" + hostname + strings.TrimSuffix(parsed.Path, "/")
}

// clusterNeighbours snapshots the labs already on the cluster identified by key,
// excluding the lab being created.
//
// A destroyed lab holds nothing, and a dry run provisioned nothing, so neither
// occupies the cluster. A *failed* lab is treated as occupying it: its automatic
// rollback usually removes its resources, but a rollback that itself failed
// leaves them in place, and the only cost of being wrong here is that the new
// lab picks a namespace it did not strictly have to.
func clusterNeighbours(jobs []*Job, key, excludeJobID string) []clusterNeighbour {
	if key == "" {
		return nil
	}
	var neighbours []clusterNeighbour
	for _, job := range jobs {
		if job == nil || job.ID == excludeJobID {
			continue
		}
		job.mu.RLock()
		cfg := job.Config
		if cfg == nil || job.Status == JobStatusDestroyed || job.Status == JobStatusDryRunCompleted {
			job.mu.RUnlock()
			continue
		}
		sameCluster := clusterKey(cfg) == key
		neighbour := clusterNeighbour{
			stackName:            cfg.StackName,
			domain:               cfg.Domain,
			namespace:            job.workspaceNamespace(),
			dnsProvider:          cfg.DNSProvider,
			dnsZone:              cfg.DNSZone,
			clusterIssuerName:    cfg.ClusterIssuerName,
			certManagerNamespace: cfg.CertManagerNamespace,
			ingressNamespace:     cfg.NginxIngressNamespace,
			ingressServiceName:   cfg.NginxIngressServiceName,
			ingressClassName:     cfg.IngressClassName,
			// Copied, not aliased: the snapshot outlives this lock, and a map
			// header would leave sameDNSCredentials iterating another job's live
			// credentials after the lock is released.
			dnsCredentials:  maps.Clone(cfg.DNSCredentials),
			usesExternalDNS: cfg.UseExternalDNS,
		}
		job.mu.RUnlock()
		if sameCluster {
			neighbours = append(neighbours, neighbour)
		}
	}
	return neighbours
}

// adoptSharedCluster prepares cfg to join a cluster that already has labs on it:
// it moves the lab into a namespace of its own and points it at the shared
// ingress controller, cert-manager and DNS-01 plumbing already there. It returns
// the notices to show the admin — each change it made, named, plus the hazards
// it cannot fix.
//
// It is a no-op for the first lab on a cluster and for a lab on its own
// dedicated cluster, so it is safe to call unconditionally.
func adoptSharedCluster(cfg *LabConfig, jobID string, neighbours []clusterNeighbour, issuers observedIssuers) []string {
	if cfg == nil || len(neighbours) == 0 {
		return nil
	}

	var notices []string
	notices = append(notices, resolveNamespaceClash(cfg, jobID, neighbours)...)

	// Only a lab with a domain of its own installs cert-manager or asks for a
	// certificate, so only such a lab has anything to reuse.
	if cfg.Domain == "" {
		return notices
	}

	host := dns01Host(neighbours)
	if host == nil {
		return notices
	}

	notices = append(notices, reuseSharedIngress(cfg, *host)...)
	notices = append(notices, reuseSharedCertManager(cfg, *host, jobID, issuers)...)
	notices = append(notices, sharedClusterHazards(cfg, *host)...)
	return notices
}

// dns01Host returns the lab whose DNS-01 plumbing a joining lab should reuse, or
// nil when no lab on the cluster set any up. The first match wins: the
// ClusterIssuer, webhook and credential secret are singletons under fixed names,
// so every lab that set them up set up the same ones.
func dns01Host(neighbours []clusterNeighbour) *clusterNeighbour {
	for i := range neighbours {
		if neighbours[i].servesDNS01() {
			return &neighbours[i]
		}
	}
	return nil
}

// resolveNamespaceClash moves the lab out of a namespace another lab on the
// cluster already holds. This is the change that actually makes a second domain
// possible: it is what keeps the two wildcard certificates, and the two sets of
// student workspaces and volumes, apart.
func resolveNamespaceClash(cfg *LabConfig, jobID string, neighbours []clusterNeighbour) []string {
	taken := make(map[string]bool, len(neighbours))
	holder := make(map[string]string, len(neighbours))
	for _, n := range neighbours {
		taken[n.namespace] = true
		if _, seen := holder[n.namespace]; !seen {
			holder[n.namespace] = n.stackName
		}
	}

	current := cfg.WorkspaceNamespace
	if current == "" {
		current = defaultWorkspaceNamespace
	}
	if !taken[current] {
		return nil
	}

	replacement := freeWorkspaceNamespace(current, cfg.StackName, jobID, taken)
	if replacement == "" {
		// Nothing was free, which takes a deliberately adversarial set of
		// existing namespaces. Leave the configuration alone and say so plainly
		// rather than deploying into a collision.
		return []string{fmt.Sprintf(
			"Lab %q already uses the workspace namespace %q on this cluster and no free alternative "+
				"could be derived. Set a different Workspace Namespace before deploying, or this lab will "+
				"collide with it.",
			holder[current], current)}
	}

	cfg.WorkspaceNamespace = replacement
	return []string{fmt.Sprintf(
		"Workspace namespace set to %q: lab %q already occupies %q on this cluster. Two labs cannot share "+
			"one namespace — they would both claim the wildcard certificate %q, and one certificate cannot "+
			"cover two domains.",
		replacement, holder[current], current, ingress.WildcardTLSSecretName)}
}

// reuseSharedIngress points the lab at the ingress controller already running on
// the cluster. Installing a second one fails outright: the Helm release name is
// fixed, and Helm refuses a name that is still in use.
func reuseSharedIngress(cfg *LabConfig, host clusterNeighbour) []string {
	if cfg.InstallNginxIngress != nil && !*cfg.InstallNginxIngress {
		return nil // the admin already chose to reuse it
	}

	cfg.InstallNginxIngress = boolPtr(false)
	// Overwritten rather than only filled when empty: the early return above means
	// the admin had chosen to *install*, so any namespace they set was a target to
	// create, not where the controller already runs. Keeping it would send the
	// Service lookup to a namespace holding nothing.
	cfg.NginxIngressNamespace = host.ingressNamespace
	if cfg.NginxIngressServiceName == "" {
		cfg.NginxIngressServiceName = host.ingressServiceName
	}
	if cfg.IngressClassName == "" {
		cfg.IngressClassName = host.ingressClassName
	}
	return []string{fmt.Sprintf(
		"Reusing the ingress controller already on this cluster instead of installing another: lab %q "+
			"installed it, and a second install fails on the Helm release name.", host.stackName)}
}

// reuseSharedCertManager points the lab at the cert-manager, DNS-01 solver
// webhook, credential secret and ClusterIssuer already on the cluster. This is
// the conflict the admin actually hits first: the credential secret is one
// fixed-name object per DNS provider, and the second lab's stack cannot take
// fields the first lab's stack owns.
func reuseSharedCertManager(cfg *LabConfig, host clusterNeighbour, jobID string, issuers observedIssuers) []string {
	var notices []string

	if cfg.InstallCertManager == nil || *cfg.InstallCertManager {
		cfg.InstallCertManager = boolPtr(false)
		// Same reasoning as reuseSharedIngress: this branch only runs when the
		// admin had chosen to install, so their namespace was an install target,
		// not where cert-manager already runs.
		cfg.CertManagerNamespace = host.certManagerNamespace
		notices = append(notices, fmt.Sprintf(
			"Reusing the cert-manager already on this cluster instead of installing another: lab %q "+
				"installed it, and a second install fails on the Helm release name.", host.stackName))
	}

	// Deliberately not gated on DNSAlreadyConfigured. An admin who ticked
	// "Already configured" — or applied Detect from cluster, which sets it — has
	// said "reuse the issuer", and reusing it is only possible if the credentials
	// behind it can write this lab's zone. Where they cannot, honouring that
	// choice produces no error at deploy time and a dead lab afterwards: the OVH
	// webhook answers the challenge with the other lab's keys and the provider
	// returns 404 for a zone that account does not hold. So the credentials
	// decide, and the notice says what was overridden.
	if cfg.DNSProvider != "" && !cfg.DNSOwnIssuer {
		notices = append(notices, adoptIssuer(cfg, host, jobID, issuers)...)
	}

	return notices
}

// adoptIssuer decides how this lab gets a working DNS-01 ClusterIssuer on a
// cluster that already has one.
//
// A ClusterIssuer carries exactly one solver with one credential secret
// reference, so reusing the sitting lab's issuer means issuing this lab's
// certificates with the sitting lab's credentials. That is right when the
// credentials are the same and wrong when they are not — and "wrong" here is
// silent: the challenge simply never completes. So when they differ, this lab
// gets an issuer and a credential secret of its own instead, pointed at the
// solver webhook already installed on the cluster, which is cluster-wide
// infrastructure and serves any number of issuers.
func adoptIssuer(cfg *LabConfig, host clusterNeighbour, jobID string, issuers observedIssuers) []string {
	// A lab that supplied no credentials of its own has nothing to build an
	// issuer from, so reusing the sitting lab's is the only option open to it.
	hasOwnCredentials := len(cfg.DNSCredentials) > 0 && !allValuesEmpty(cfg.DNSCredentials)

	if !hasOwnCredentials || sameDNSCredentials(cfg.DNSCredentials, host.dnsCredentials) {
		issuer := cfg.ClusterIssuerName
		if issuer == "" {
			issuer = host.clusterIssuerName
		}
		if issuer == "" {
			issuer = utils.DefaultClusterIssuerName
		}

		// The job records say a lab set this issuer up. Only the cluster says
		// whether it is still there, and the two disagree after a lab whose
		// rollback or destroy took the ClusterIssuer with it while leaving
		// cert-manager, the solver webhook and the credential secret standing —
		// which is exactly what a *failed* lab leaves behind, and failed labs are
		// counted as neighbours on purpose (see clusterNeighbours).
		//
		// Adopting an issuer that is not there skips creating the one object that
		// is missing. Nothing fails at deploy time: the lab comes up clean and
		// every certificate sits Pending for ever on "Referenced ClusterIssuer not
		// found". So where the cluster has been read and says the issuer is gone,
		// the job records do not get the last word.
		if !issuers.known || issuers.has(issuer) {
			cfg.DNSAlreadyConfigured = true
			if cfg.ClusterIssuerName == "" {
				cfg.ClusterIssuerName = host.clusterIssuerName
			}
			reason := "this lab's DNS credentials are the same ones"
			if !hasOwnCredentials {
				reason = "this lab supplied no DNS credentials of its own, so certificates are issued with that lab's"
			}
			return []string{fmt.Sprintf(
				"DNS-01 marked as already configured, reusing ClusterIssuer %q: lab %q created it along with the "+
					"solver webhook and the credential secret, and %s. "+
					"Recreating them is what produces the \"had an apply conflict\" failure on %q.",
				issuer, host.stackName, reason, "dns-credentials-"+cfg.DNSProvider)}
		}

		// The issuer is gone and this lab has no credentials to build a
		// replacement with, so neither reusing nor owning one can work. Adopting
		// is left in place as the least surprising of two broken outcomes — the
		// alternative recreates the webhook and credential secret that *are* still
		// there and fails the deploy on an apply conflict — and the notice says
		// plainly what the admin has to do.
		if !hasOwnCredentials {
			cfg.DNSAlreadyConfigured = true
			if cfg.ClusterIssuerName == "" {
				cfg.ClusterIssuerName = host.clusterIssuerName
			}
			return []string{fmt.Sprintf(
				"ClusterIssuer %q is not on this cluster, or is not Ready with a DNS-01 solver: lab %q set it up, "+
					"but it has since been removed — a failed or destroyed lab takes the issuer with it and leaves "+
					"cert-manager and the solver webhook standing. This lab supplied no DNS credentials of its own, "+
					"so EasyLab cannot build a replacement issuer: enter this lab's DNS credentials to get an issuer "+
					"of its own, or create %q on the cluster. Without one, every workspace certificate stays Pending.",
				issuer, host.stackName, issuer)}
		}

		return ownIssuer(cfg, host, jobID, fmt.Sprintf(
			"ClusterIssuer %q is no longer on this cluster — lab %q set it up, but a failed or destroyed lab "+
				"has since taken it with it, leaving cert-manager and the solver webhook standing. Reusing a "+
				"missing issuer is what leaves every workspace certificate Pending on \"Referenced "+
				"ClusterIssuer not found\"",
			issuer, host.stackName),
			"reusing that issuer would have produced a lab whose certificates never issue")
	}

	return ownIssuer(cfg, host, jobID, fmt.Sprintf(
		"Its DNS credentials differ from that lab's, and one ClusterIssuer can only carry one set — so sharing "+
			"its issuer would issue this lab's certificates with credentials that cannot write zone %q",
		cfg.DNSZone),
		"reusing that issuer would have failed silently, with the OVH API returning 404 for a zone the "+
			"other lab's account does not hold")
}

// ownIssuer gives the lab a ClusterIssuer and credential secret of its own,
// against the solver webhook already installed on the cluster. why is the reason
// this lab cannot reuse the sitting lab's issuer, as a sentence without its final
// stop; it goes into the notice so the admin sees which of the cases applied.
// overrideDetail is appended, in the same form, only when this overrode an
// explicit "Already configured" choice and so owes the admin an explanation.
func ownIssuer(cfg *LabConfig, host clusterNeighbour, jobID, why, overrideDetail string) []string {
	// Reaching here with the flag set means the admin asked to reuse an issuer
	// that cannot serve this lab's zone, or one that is not there. Clearing it is
	// what lets ingress/https.go take the own-issuer branch at all, since it
	// treats "already configured" as the stronger signal.
	overrodeExplicitChoice := cfg.DNSAlreadyConfigured
	cfg.DNSAlreadyConfigured = false
	cfg.DNSOwnIssuer = true
	suffix := labObjectSuffix(cfg.StackName, jobID)
	// An explicit name is honoured only when it is genuinely this lab's own. A
	// lab that owns its issuer destroys it, so letting "its own" issuer be the
	// cluster-wide one — or the sitting lab's — would tear down the issuer every
	// other lab on the cluster requests certificates from.
	if sharedIssuerName(cfg.ClusterIssuerName, host.clusterIssuerName) {
		cfg.ClusterIssuerName = "easylab-" + suffix
	}
	if sharedSecretName(cfg.DNSCredentialSecretName, cfg.DNSProvider) {
		cfg.DNSCredentialSecretName = "dns-credentials-" + cfg.DNSProvider + "-" + suffix
	}
	notice := fmt.Sprintf(
		"This lab gets its own ClusterIssuer %q and credential secret %q, reusing the DNS-01 solver webhook "+
			"lab %q installed. %s.",
		cfg.ClusterIssuerName, cfg.DNSCredentialSecretName, host.stackName, why)
	if overrodeExplicitChoice {
		notice += fmt.Sprintf(" This overrides the \"Already configured\" setting: %s.", overrideDetail)
	}
	return []string{notice}
}

// sharedIssuerName reports whether a ClusterIssuer name belongs to the cluster
// at large rather than to one lab: unset, the project-wide default, or the name
// the sitting lab created. Such an issuer must never be destroyed with a single
// lab, so a lab claiming to own its issuer may not be using one of these names.
func sharedIssuerName(name, neighbourIssuer string) bool {
	name = strings.TrimSpace(name)
	return name == "" ||
		name == utils.DefaultClusterIssuerName ||
		(neighbourIssuer != "" && name == neighbourIssuer)
}

// sharedSecretName reports the same for a DNS credential secret: the
// provider-wide name is every lab's, so one lab may not destroy it.
func sharedSecretName(name, provider string) bool {
	name = strings.TrimSpace(name)
	return name == "" || name == "dns-credentials-"+provider
}

// labObjectSuffix is the per-lab tail of a cluster-scoped object's name: readable
// where the stack name allows, unique either way.
func labObjectSuffix(stackName, jobID string) string {
	stack := namespaceSegment(stackName)
	unique := jobIDSegment(jobID)
	switch {
	case stack != "" && unique != "":
		return stack + "-" + unique
	case stack != "":
		return stack
	default:
		return unique
	}
}

// sharedClusterHazards reports what joining this cluster cannot fix by
// configuration alone, so the admin hears it now rather than from a student with
// an untrusted certificate.
func sharedClusterHazards(cfg *LabConfig, host clusterNeighbour) []string {
	var notices []string

	// ExternalDNS has no reuse switch: ingress/https.go creates its namespace,
	// credential secret and Helm release outside the DNS-01 guard.
	if cfg.UseExternalDNS || host.usesExternalDNS {
		notices = append(notices, "ExternalDNS cannot be shared between labs on one cluster: its namespace, "+
			"credential secret and Helm release all have fixed names and are created even when DNS-01 is "+
			"already configured. Use the wildcard A record strategy for one of the two labs.")
	}

	return notices
}

// sameDNSCredentials compares two credential sets without reading or reporting
// any value: only whether they would drive the same ClusterIssuer.
func sameDNSCredentials(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if other, ok := b[k]; !ok || other != v {
			return false
		}
	}
	return true
}

// maxNamespaceLength is the DNS-1123 label limit a Kubernetes namespace name is
// held to.
const maxNamespaceLength = 63

// freeWorkspaceNamespace derives a namespace no lab on the cluster holds,
// preferring a name an admin can recognise in kubectl output over an opaque one.
// It returns "" when every candidate is taken.
func freeWorkspaceNamespace(base, stackName, jobID string, taken map[string]bool) string {
	stack := namespaceSegment(stackName)
	unique := jobIDSegment(jobID)

	var candidates []string
	if stack != "" {
		candidates = append(candidates, base+"-"+stack)
	}
	if unique != "" {
		if stack != "" {
			candidates = append(candidates, base+"-"+stack+"-"+unique)
		}
		candidates = append(candidates, base+"-"+unique)
	}

	for _, candidate := range candidates {
		if candidate = truncateNamespace(candidate); candidate != "" && !taken[candidate] {
			return candidate
		}
	}
	return ""
}

// namespaceSegment reduces a string to something usable inside a namespace name.
// It builds on sanitizeDNSName and additionally drops the dots that are legal in
// an object name but not in a namespace's DNS-1123 label.
func namespaceSegment(s string) string {
	return strings.Trim(strings.ReplaceAll(sanitizeDNSName(s), ".", "-"), "-")
}

// jobIDSegment shortens a job id ("job-<uuid>") into a namespace-safe suffix
// that is unique in practice.
func jobIDSegment(jobID string) string {
	segment := namespaceSegment(strings.TrimPrefix(jobID, "job-"))
	segment = strings.ReplaceAll(segment, "-", "")
	if len(segment) > 8 {
		segment = segment[:8]
	}
	return segment
}

// truncateNamespace trims a derived name to the DNS-1123 label limit, keeping it
// a valid label rather than one ending in a dash.
func truncateNamespace(name string) string {
	if len(name) > maxNamespaceLength {
		name = name[:maxNamespaceLength]
	}
	return strings.Trim(name, "-")
}

// ownsDNSIssuer reports whether this lab created its own ClusterIssuer and DNS
// credential secret rather than using the cluster-wide ones. Such a lab must
// destroy them with itself; a lab on the shared names must not.
//
// The flag alone is not enough to decide this, and getting it wrong is
// irreversible for every other lab on the cluster: the names are checked too, so
// a lab whose issuer or secret is still a cluster-wide name keeps the exclusion
// whatever its flag says. A lab that genuinely owns both is the only one that
// releases either — releasing one and not the other would still strand a live
// copy of its DNS credentials, or an issuer with no credentials behind it.
func (pe *PulumiExecutor) ownsDNSIssuer(jobID string) bool {
	if pe == nil || pe.jobManager == nil {
		return false
	}
	job, exists := pe.jobManager.GetJob(jobID)
	if !exists {
		return false
	}
	job.mu.RLock()
	defer job.mu.RUnlock()

	cfg := job.Config
	if cfg == nil || !cfg.DNSOwnIssuer {
		return false
	}
	return !sharedIssuerName(cfg.ClusterIssuerName, "") && !sharedSecretName(cfg.DNSCredentialSecretName, cfg.DNSProvider)
}

// workspaceNamespaceShared reports whether a lab other than this one occupies
// this lab's workspace namespace on the same cluster. It gates the namespace's
// exclusion from a destroy: the namespace must survive a sibling lab's teardown,
// but a lab that is alone in its namespace has to be able to take it with it,
// or every destroy leaves a namespace and its volumes behind.
func (pe *PulumiExecutor) workspaceNamespaceShared(jobID string) bool {
	if pe == nil || pe.jobManager == nil {
		return false
	}
	job, exists := pe.jobManager.GetJob(jobID)
	if !exists {
		return false
	}
	job.mu.RLock()
	cfg := job.Config
	namespace := job.workspaceNamespace()
	key := clusterKey(cfg)
	job.mu.RUnlock()

	if key == "" {
		return false
	}
	for _, neighbour := range clusterNeighbours(pe.jobManager.GetAllJobs(), key, jobID) {
		if neighbour.namespace == namespace {
			return true
		}
	}
	return false
}
