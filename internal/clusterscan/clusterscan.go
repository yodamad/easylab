// Package clusterscan reads an existing Kubernetes cluster and reports what an
// admin would otherwise have to look up by hand before creating a lab on it:
// where the ingress controller lives and which class it serves, whether
// cert-manager and a usable ClusterIssuer are already present, what the default
// StorageClass is, which node labels can pin a pod, and whether the supplied
// credentials may actually do what EasyLab will ask of them.
//
// Everything here is read-only — the one call that is a POST,
// SelfSubjectAccessReview, creates no object.
//
// It is deliberately not a method set on providers/workspace/kube.Backend: a scan
// runs before any lab exists, with a kubeconfig that may well be wrong, and needs
// cluster-scoped reads (nodes, storage classes, ingress classes, namespaces) that
// the workspace write path never performs and should not imply it needs. Like
// internal/tfparse and internal/devcontainer this package stays standalone: it
// knows nothing about LabConfig or the admin form, and internal/server maps a
// Report onto its own field names.
package clusterscan

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Timeouts mirror the values the cleanup loop already runs with
// (internal/server/cleanup.go), so a scan cannot hold a request open longer than
// the background work that talks to the same clusters.
const (
	// probeTimeoutFast bounds the connectivity check, the one probe whose failure
	// aborts the scan — there is no point waiting long to learn the cluster is
	// unreachable.
	probeTimeoutFast = 5 * time.Second
	// probeTimeout bounds a single list or get.
	probeTimeout = 10 * time.Second
	// ScanTimeout bounds the whole scan, so one hanging API call cannot eat the
	// request even if its own deadline is somehow not honoured.
	ScanTimeout = 30 * time.Second
)

// defaultWorkspaceNamespace is the namespace student workspaces land in when a lab
// names none. Duplicated from providers/workspace/kube rather than imported: this
// package stays free of the workspace backend, and the value is part of three
// other files already.
const defaultWorkspaceNamespace = "workshops"

// ErrInvalidKubeconfig replaces the parse error from clientcmd, which can quote
// the kubeconfig's own bytes — including its credentials — into whatever log line
// or HTTP response the caller builds from it. Callers match on it to tell "this
// file is not a kubeconfig" apart from "this cluster cannot be reached".
var ErrInvalidKubeconfig = errors.New("the kubeconfig could not be parsed")

// Status says how a single probe went. "absent" and "forbidden" are deliberately
// different values: a cluster with no cert-manager and a cluster whose credentials
// may not look for one are not the same answer, and an admin reading the result
// needs to be able to tell them apart.
type Status string

const (
	StatusOK        Status = "ok"
	StatusAbsent    Status = "absent"
	StatusForbidden Status = "forbidden"
	StatusTimeout   Status = "timeout"
	StatusError     Status = "error"
	// StatusSkipped marks a probe that was never run because what it depends on
	// was not there — ClusterIssuers on a cluster without cert-manager.
	StatusSkipped Status = "skipped"
)

// Probe is one check's outcome.
//
// Detail is a pre-sanitized sentence written for an admin. The raw API error never
// reaches it, so a Report is always safe to serialize to a browser; the errors
// themselves come back from Scan separately, for the caller to log.
type Probe struct {
	Status Status `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// OK reports whether this probe found what it was looking for.
func (p Probe) OK() bool { return p.Status == StatusOK }

// Warning is something worth telling the admin about the cluster. Key is stable
// and assertable in tests; Message is the prose shown in the wizard. Same shape as
// devcontainer.Warning, so the front end renders both with one code path.
type Warning struct {
	Key     string `json:"key"`
	Message string `json:"message"`
}

// Warning keys. Stable identifiers — the UI may branch on them and the tests do.
const (
	WarnIngressNonTraefik       = "ingress.non_traefik"
	WarnIngressHostnameOnly     = "ingress.hostname_only"
	WarnIngressNoAddress        = "ingress.no_external_address"
	WarnIngressNotLoadBalancer  = "ingress.not_load_balancer"
	WarnIngressMultiple         = "ingress.multiple_controllers"
	WarnIngressAbsent           = "ingress.absent"
	WarnStorageNodeLocal        = "storageclass.node_local"
	WarnStorageAbsent           = "storageclass.absent"
	WarnStorageMultipleDefaults = "storageclass.multiple_defaults"
	WarnCertManagerAbsent       = "certmanager.absent"
	WarnIssuerNoDNS01           = "clusterissuer.no_dns01"
	WarnIssuerNotReady          = "clusterissuer.not_ready"
	WarnIssuerStagingOnly       = "clusterissuer.staging_only"
	WarnNamespaceExists         = "namespace.exists"
	WarnPermissionsMissing      = "permissions.missing"
	WarnNodesSingle             = "nodes.single"
)

// ClusterFinding is the connectivity check: if this one fails nothing else was run.
type ClusterFinding struct {
	Probe
	Version  string `json:"version,omitempty"`
	Platform string `json:"platform,omitempty"`
}

// IngressClassInfo is one IngressClass on the cluster.
type IngressClassInfo struct {
	Name       string `json:"name"`
	Controller string `json:"controller"`
	Flavour    string `json:"flavour"`
	Default    bool   `json:"default"`
}

// IngressFinding describes the ingress controller a lab on this cluster would be
// routed through. Probe covers locating its Service; ClassesProbe covers listing
// the IngressClasses, because credentials often allow one and not the other.
type IngressFinding struct {
	Probe
	Classes      []IngressClassInfo `json:"classes,omitempty"`
	ClassesProbe Probe              `json:"classes_probe"`

	Class      string `json:"class,omitempty"`
	Controller string `json:"controller,omitempty"`
	Flavour    string `json:"flavour,omitempty"`
	Namespace  string `json:"namespace,omitempty"`
	Service    string `json:"service,omitempty"`
	// ServiceType matters because only a LoadBalancer yields an address EasyLab
	// can point DNS at.
	ServiceType string `json:"service_type,omitempty"`
	ExternalIP  string `json:"external_ip,omitempty"`
	// ExternalHost is the LoadBalancer's hostname, set by cloud load balancers that
	// do not expose an IP at all. It is reported because the nip.io quick-start mode
	// encodes an address in the hostname and so cannot work without an IP.
	ExternalHost string `json:"external_hostname,omitempty"`
}

// CertManagerFinding reports whether cert-manager is installed and where.
type CertManagerFinding struct {
	Probe
	Installed bool   `json:"installed"`
	Namespace string `json:"namespace,omitempty"`
	// NamespaceFrom records how the namespace was identified (webhook, service,
	// deployment, default), so an admin can judge how much to trust it.
	NamespaceFrom string `json:"namespace_from,omitempty"`
}

// ClusterIssuerInfo is one cert-manager ClusterIssuer.
type ClusterIssuerInfo struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	ACMEServer string `json:"acme_server,omitempty"`
	Staging    bool   `json:"staging"`
	HasDNS01   bool   `json:"has_dns01"`
	HasHTTP01  bool   `json:"has_http01"`
	Ready      bool   `json:"ready"`
	Message    string `json:"message,omitempty"`
}

// Usable reports whether this issuer can actually issue the wildcard certificate a
// lab wants. An issuer that is not Ready, or that has no DNS-01 solver, leaves
// cert-manager's CertificateRequest stalled with no error anywhere — which is why
// only a usable one is ever suggested as a lab's ClusterIssuer.
func (c ClusterIssuerInfo) Usable() bool { return c.Ready && c.HasDNS01 }

// IssuerFinding lists the ClusterIssuers already on the cluster.
type IssuerFinding struct {
	Probe
	Items []ClusterIssuerInfo `json:"items,omitempty"`
}

// StorageClassInfo is one StorageClass.
type StorageClassInfo struct {
	Name        string `json:"name"`
	Provisioner string `json:"provisioner"`
	BindingMode string `json:"binding_mode,omitempty"`
	Default     bool   `json:"default"`
	// NodeLocal marks a provisioner whose volumes live on one node, so a workspace
	// rescheduled elsewhere comes back empty.
	NodeLocal bool `json:"node_local"`
}

// StorageFinding describes where workspace volumes would be provisioned from.
type StorageFinding struct {
	Probe
	Classes     []StorageClassInfo `json:"classes,omitempty"`
	Default     string             `json:"default,omitempty"`
	Provisioner string             `json:"provisioner,omitempty"`
	NodeLocal   bool               `json:"node_local"`
	BindingMode string             `json:"binding_mode,omitempty"`
}

// NodeLabel is one label key found on the cluster's nodes, with the values it
// takes — a ready-made suggestion for a node selector.
type NodeLabel struct {
	Key    string   `json:"key"`
	Values []string `json:"values"`
	Nodes  int      `json:"nodes"`
}

// NodeFinding reports the node count and the label keys worth pinning a pod to.
type NodeFinding struct {
	Probe
	Count  int         `json:"count"`
	Labels []NodeLabel `json:"labels,omitempty"`
}

// NamespaceFinding reports the cluster's namespaces and whether the one this lab
// would use is already there.
type NamespaceFinding struct {
	Probe
	Names           []string `json:"names,omitempty"`
	WorkspaceName   string   `json:"workspace_namespace"`
	WorkspaceExists bool     `json:"workspace_namespace_exists"`
}

// PermissionCheck is one SelfSubjectAccessReview answer.
type PermissionCheck struct {
	Group     string `json:"group,omitempty"`
	Resource  string `json:"resource"`
	Namespace string `json:"namespace,omitempty"`
	Verb      string `json:"verb"`
	Allowed   bool   `json:"allowed"`
	Reason    string `json:"reason,omitempty"`
}

// String renders a check the way the UI lists a missing one.
func (p PermissionCheck) String() string {
	if p.Namespace == "" {
		return fmt.Sprintf("%s %s (cluster-wide)", p.Verb, p.Resource)
	}
	return fmt.Sprintf("%s %s in %s", p.Verb, p.Resource, p.Namespace)
}

// PermissionFinding is the pre-flight: may these credentials do what EasyLab will
// ask of them? It answers "may I", not "does it exist" — a read probe that came
// back forbidden alongside an allowed check here means the object simply is not
// there.
type PermissionFinding struct {
	Probe
	Checks  []PermissionCheck `json:"checks,omitempty"`
	Missing []string          `json:"missing,omitempty"`
}

// Report is everything a scan found. Each finding carries its own Probe, so one
// check being denied never hides the others.
type Report struct {
	Cluster     ClusterFinding     `json:"cluster"`
	Ingress     IngressFinding     `json:"ingress"`
	CertManager CertManagerFinding `json:"cert_manager"`
	Issuers     IssuerFinding      `json:"issuers"`
	Storage     StorageFinding     `json:"storage"`
	Nodes       NodeFinding        `json:"nodes"`
	Namespaces  NamespaceFinding   `json:"namespaces"`
	Permissions PermissionFinding  `json:"permissions"`
	Warnings    []Warning          `json:"warnings,omitempty"`
}

// Options tunes a scan.
type Options struct {
	// WorkspaceNamespace is the namespace whose existence and permissions are
	// checked. Empty means the lab default.
	WorkspaceNamespace string
	// ProbeTimeout overrides the per-probe deadline. A seam for tests, not
	// something the server sets.
	ProbeTimeout time.Duration
}

// Scanner holds the clients a scan reads through.
type Scanner struct {
	core kubernetes.Interface
	dyn  dynamic.Interface
	// disco is kept separately from core.Discovery() because ServerVersion() takes
	// no context: its deadline can only come from the REST client it was built
	// with, so it needs a client configured with the connectivity timeout.
	disco discovery.DiscoveryInterface
}

// New builds a Scanner from a kubeconfig's contents.
func New(kubeconfig string) (*Scanner, error) {
	if strings.TrimSpace(kubeconfig) == "" {
		return nil, errors.New("kubeconfig is empty")
	}
	cfg, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfig))
	if err != nil {
		// Not wrapped on purpose — see ErrInvalidKubeconfig.
		return nil, ErrInvalidKubeconfig
	}
	cfg.Timeout = probeTimeout

	core, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to build kubernetes client: %w", err)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to build dynamic client: %w", err)
	}

	discoCfg := rest.CopyConfig(cfg)
	discoCfg.Timeout = probeTimeoutFast
	disco, err := discovery.NewDiscoveryClientForConfig(discoCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to build discovery client: %w", err)
	}

	return &Scanner{core: core, dyn: dyn, disco: disco}, nil
}

// newScanner is the injectable constructor used by tests, mirroring kube's
// newBackend. It takes the clientset's own discovery client, which the fake
// clientset implements.
func newScanner(core kubernetes.Interface, dyn dynamic.Interface) *Scanner {
	return &Scanner{core: core, dyn: dyn, disco: core.Discovery()}
}

// classify turns an API error into a probe status and decides whether it is worth
// logging. It is the single place a raw cluster error is allowed to become a
// message, which is what keeps API server addresses and request bodies out of
// every Report.
func classify(err error) (Probe, error) {
	switch {
	case err == nil:
		return Probe{Status: StatusOK}, nil
	case apierrors.IsForbidden(err), apierrors.IsUnauthorized(err):
		return Probe{Status: StatusForbidden, Detail: "couldn't check: these credentials are not allowed to read this"}, err
	case apierrors.IsNotFound(err), meta.IsNoMatchError(err):
		// Not an error: the thing simply is not installed.
		return Probe{Status: StatusAbsent}, nil
	case apierrors.IsTimeout(err), apierrors.IsServerTimeout(err),
		errors.Is(err, context.DeadlineExceeded):
		return Probe{Status: StatusTimeout, Detail: "couldn't check: timed out"}, err
	default:
		return Probe{Status: StatusError, Detail: "couldn't check"}, err
	}
}

// ScanKubeconfig builds a Scanner from a kubeconfig's contents, scans with it and
// drops the client — the entry point for a caller that holds a kubeconfig and
// wants one answer, and which should not keep a client built from credentials it
// was only handed to look once.
//
// A kubeconfig that cannot be used comes back the same way an unreachable cluster
// does: a nil Report and the reason in the error slice. Match ErrInvalidKubeconfig
// to tell the two apart.
func ScanKubeconfig(ctx context.Context, kubeconfig string, opts Options) (*Report, []error) {
	scanner, err := New(kubeconfig)
	if err != nil {
		return nil, []error{err}
	}
	return Scan(ctx, scanner, opts)
}

// Scan runs every probe and returns what it found plus the raw probe errors, which
// the caller logs. The errors deliberately stay out of the Report.
//
// Connectivity is checked first and alone: if the API server cannot be reached,
// every other probe would fail for the same reason, and the caller wants to say
// "could not connect" rather than hand back a report full of crosses. Every probe
// after that degrades on its own — one denied check never stops the rest.
func Scan(ctx context.Context, s *Scanner, opts Options) (*Report, []error) {
	wsNamespace := strings.TrimSpace(opts.WorkspaceNamespace)
	if wsNamespace == "" {
		wsNamespace = defaultWorkspaceNamespace
	}
	timeout := opts.ProbeTimeout
	if timeout <= 0 {
		timeout = probeTimeout
	}

	ctx, cancel := context.WithTimeout(ctx, ScanTimeout)
	defer cancel()

	rep := &Report{}

	cluster, warns, err := s.probeCluster()
	rep.Cluster = cluster
	if err != nil {
		return nil, []error{err}
	}
	clusterWarns := warns

	// Each group writes only its own Report field, so no lock is needed; warnings
	// and errors are collected per-group and merged in a fixed order below, which
	// keeps the output deterministic for the tests.
	var (
		wg                                                   sync.WaitGroup
		nodeWarns, ingressWarns, certWarns                   []Warning
		issuerWarns, storageWarns, nsWarns                   []Warning
		nodeErrs, ingressErrs, certErrs, storageErrs, nsErrs []error
	)

	run := func(fn func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fn()
		}()
	}

	run(func() {
		rep.Nodes, nodeWarns, nodeErrs = s.probeNodes(ctx, timeout)
	})
	run(func() {
		rep.Ingress, ingressWarns, ingressErrs = s.probeIngress(ctx, timeout)
	})
	run(func() {
		// cert-manager, its namespace and its issuers are one chain: each step only
		// makes sense if the one before it found something.
		rep.CertManager, rep.Issuers, certWarns, issuerWarns, certErrs = s.probeCertManager(ctx, timeout)
	})
	run(func() {
		rep.Storage, storageWarns, storageErrs = s.probeStorage(ctx, timeout)
	})
	run(func() {
		rep.Namespaces, nsWarns, nsErrs = s.probeNamespaces(ctx, timeout, wsNamespace)
	})

	wg.Wait()

	// Permissions run last because the set of checks depends on what was found:
	// the portal needs a Role in the ingress controller's namespace, and
	// ClusterIssuers are only worth asking about if cert-manager is there.
	permFinding, permWarns, permErrs := s.probePermissions(ctx, timeout, permissionTargets{
		WorkspaceNamespace: wsNamespace,
		IngressNamespace:   rep.Ingress.Namespace,
		CertManager:        rep.CertManager.Installed,
	})
	rep.Permissions = permFinding

	for _, w := range [][]Warning{clusterWarns, nodeWarns, ingressWarns, certWarns, issuerWarns, storageWarns, nsWarns, permWarns} {
		rep.Warnings = append(rep.Warnings, w...)
	}

	var errs []error
	for _, e := range [][]error{nodeErrs, ingressErrs, certErrs, storageErrs, nsErrs, permErrs} {
		errs = append(errs, e...)
	}
	return rep, errs
}
