package clusterscan

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// listLimit caps the cluster-wide lists. A workshop cluster is small; a scan that
// walks thousands of nodes or namespaces is answering the wrong question anyway.
const listLimit = 500

// maxLabelSuggestions and maxLabelValues cap what the node-selector pickers offer,
// so a cluster with machine-generated labels cannot flood the form.
const (
	maxLabelSuggestions = 40
	maxLabelValues      = 10
)

// minSupportedMinor is the Kubernetes minor version below which the IngressClass
// and cert-manager behaviour EasyLab relies on starts to differ.
const minSupportedMinor = 25

// Ingress controller flavours, keyed by the IngressClass's spec.controller.
const (
	flavourTraefik = "traefik"
	flavourUnknown = "unknown"
)

var ingressControllerFlavours = map[string]string{
	"traefik.io/ingress-controller":          flavourTraefik,
	"traefik.containo.us/ingress-controller": flavourTraefik,
	"k8s.io/ingress-nginx":                   "ingress-nginx",
	"nginx.org/ingress-controller":           "nginx-inc",
	"haproxy.org/ingress-controller":         "haproxy",
	"cilium.io/ingress-controller":           "cilium",
	"ingress.k8s.aws/alb":                    "aws-alb",
	"openshift.io/ingress-to-route":          "openshift",
}

// wellKnownIngressServices are the (namespace, name) pairs ingress controllers are
// conventionally installed under. Used to pick between several LoadBalancers, and
// as the fallback when listing Services cluster-wide is denied.
var wellKnownIngressServices = []struct{ Namespace, Name string }{
	{"traefik", "traefik"},
	{"kube-system", "traefik"},
	{"ingress-nginx", "ingress-nginx-controller"},
	{"ingress-nginx", "ingress-nginx"},
	{"nginx-ingress", "nginx-ingress-controller"},
	{"kube-system", "nginx-ingress-controller"},
	{"haproxy-controller", "haproxy-ingress"},
	{"istio-system", "istio-ingressgateway"},
}

// nodeLocalProvisioners place a volume on the node that first used it, so a
// workspace rescheduled elsewhere comes back empty. Workspaces are persistent by
// default, which makes this worth warning about rather than silently accepting.
var nodeLocalProvisioners = map[string]bool{
	"rancher.io/local-path":            true,
	"kubernetes.io/no-provisioner":     true,
	"k8s.io/minikube-hostpath":         true,
	"docker.io/hostpath":               true,
	"microk8s.io/hostpath":             true,
	"openebs.io/local":                 true,
	"local.csi.openebs.io":             true,
	"csi.hostpath.k8s.io":              true,
	"hostpath.csi.k8s.io":              true,
	"topolvm.io":                       true,
	"kubevirt.io/hostpath-provisioner": true,
}

// Label keys that say nothing about which pool a node belongs to: either unique
// per node, or identical across every node in the cluster.
var droppedLabelPrefixes = []string{
	"beta.kubernetes.io/",
	"failure-domain.beta.kubernetes.io/",
	"node.kubernetes.io/",
	"csi.",
	"topology.gke.io/",
	"topology.ebs.csi.aws.com/",
	"topology.cinder.csi.openstack.org/",
}

var droppedLabelKeys = map[string]bool{
	"kubernetes.io/hostname": true,
	"kubernetes.io/os":       true,
	"kubernetes.io/arch":     true,
}

// keptLabelKeys survive the prefix drop above because they are genuinely useful
// selectors despite matching one of its patterns.
var keptLabelKeys = map[string]bool{
	"topology.kubernetes.io/zone":   true,
	"topology.kubernetes.io/region": true,
}

var clusterIssuerGVR = schema.GroupVersionResource{
	Group: "cert-manager.io", Version: "v1", Resource: "clusterissuers",
}

const certManagerGroupVersion = "cert-manager.io/v1"

// probeCluster is the connectivity check. Its error is the one that aborts a scan.
func (s *Scanner) probeCluster() (ClusterFinding, []Warning, error) {
	info, err := s.disco.ServerVersion()
	probe, logErr := classify(err)
	if err != nil {
		// Even an "absent"-classified failure is fatal here: there is no API server
		// to talk to, so report it as an error rather than a missing feature.
		if probe.Status == StatusAbsent {
			probe = Probe{Status: StatusError, Detail: "couldn't reach the cluster"}
			logErr = err
		}
		return ClusterFinding{Probe: probe}, nil, logErr
	}

	finding := ClusterFinding{
		Probe:    probe,
		Version:  info.GitVersion,
		Platform: info.Platform,
	}
	var warns []Warning
	if minor := parseMinor(info.Minor); minor > 0 && minor < minSupportedMinor {
		warns = append(warns, Warning{
			Key:     "cluster.version_old",
			Message: fmt.Sprintf("This cluster runs Kubernetes %s. EasyLab is tested against 1.%d and newer; older versions handle IngressClasses differently and may not route workspaces.", info.GitVersion, minSupportedMinor),
		})
	}
	return finding, warns, nil
}

// probeNodes counts the nodes and works out which labels could pin a pod.
func (s *Scanner) probeNodes(ctx context.Context, timeout time.Duration) (NodeFinding, []Warning, []error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	list, err := s.core.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: listLimit})
	probe, logErr := classify(err)
	if err != nil {
		return NodeFinding{Probe: probe}, nil, errList(logErr)
	}

	finding := NodeFinding{
		Probe:  probe,
		Count:  len(list.Items),
		Labels: nodeLabelSuggestions(list.Items),
	}
	var warns []Warning
	if finding.Count == 1 {
		warns = append(warns, Warning{
			Key:     WarnNodesSingle,
			Message: "This cluster has a single node, so there is nothing for a node selector to separate — EasyLab and the workspaces will share it.",
		})
	}
	return finding, warns, nil
}

// nodeLabelSuggestions reduces every node label down to the ones that identify a
// pool. A key that takes a different value on every node (a hostname, a machine
// ID) cannot select a group, and a key every node shares does not narrow anything
// — both are dropped, so the pickers offer only labels worth typing.
func nodeLabelSuggestions(nodes []corev1.Node) []NodeLabel {
	if len(nodes) == 0 {
		return nil
	}
	values := map[string]map[string]int{}
	for _, n := range nodes {
		for k, v := range n.Labels {
			if !usefulLabelKey(k) {
				continue
			}
			if values[k] == nil {
				values[k] = map[string]int{}
			}
			values[k][v]++
		}
	}

	out := make([]NodeLabel, 0, len(values))
	for k, vs := range values {
		// One distinct value per node means the key identifies the node, not a
		// group of them — a hostname or a machine id. Only worth acting on above
		// two nodes: on a two-node cluster a genuine two-pool label looks exactly
		// the same, and dropping it would hide the one selector that matters.
		if len(nodes) > 2 && len(vs) == len(nodes) {
			continue
		}
		label := NodeLabel{Key: k}
		for v, n := range vs {
			label.Values = append(label.Values, v)
			label.Nodes += n
		}
		sort.Strings(label.Values)
		if len(label.Values) > maxLabelValues {
			label.Values = label.Values[:maxLabelValues]
		}
		out = append(out, label)
	}

	// Fewest distinct values first — the clearest pool split — then by key, so the
	// order is stable across runs.
	sort.Slice(out, func(i, j int) bool {
		if len(out[i].Values) != len(out[j].Values) {
			return len(out[i].Values) < len(out[j].Values)
		}
		return out[i].Key < out[j].Key
	})
	if len(out) > maxLabelSuggestions {
		out = out[:maxLabelSuggestions]
	}
	return out
}

func usefulLabelKey(k string) bool {
	if keptLabelKeys[k] {
		return true
	}
	if droppedLabelKeys[k] {
		return false
	}
	for _, p := range droppedLabelPrefixes {
		if strings.HasPrefix(k, p) {
			return false
		}
	}
	return true
}

// probeIngress locates the ingress controller and the class it serves.
func (s *Scanner) probeIngress(ctx context.Context, timeout time.Duration) (IngressFinding, []Warning, []error) {
	var (
		finding IngressFinding
		warns   []Warning
		errs    []error
	)

	classes, classErr := s.listIngressClasses(ctx, timeout)
	classProbe, classLogErr := classify(classErr)
	finding.ClassesProbe = classProbe
	finding.Classes = classes
	if classLogErr != nil {
		errs = append(errs, classLogErr)
	}

	svc, svcErr := s.findIngressService(ctx, timeout)
	svcProbe, svcLogErr := classify(svcErr)
	finding.Probe = svcProbe
	if svcLogErr != nil {
		errs = append(errs, svcLogErr)
	}

	if svc != nil {
		finding.Namespace = svc.Namespace
		finding.Service = svc.Name
		finding.ServiceType = string(svc.Spec.Type)
		for _, ing := range svc.Status.LoadBalancer.Ingress {
			if finding.ExternalIP == "" && ing.IP != "" {
				finding.ExternalIP = ing.IP
			}
			if finding.ExternalHost == "" && ing.Hostname != "" {
				finding.ExternalHost = ing.Hostname
			}
		}
	} else if svcErr == nil {
		// Nothing denied the lookup; there simply is no controller Service.
		finding.Probe = Probe{Status: StatusAbsent}
	}

	if chosen := pickIngressClass(classes, serviceFlavour(svc)); chosen != nil {
		finding.Class = chosen.Name
		finding.Controller = chosen.Controller
		finding.Flavour = chosen.Flavour
	} else if f := serviceFlavour(svc); f != "" {
		finding.Flavour = f
	}

	warns = append(warns, ingressWarnings(finding, classes)...)
	return finding, warns, errs
}

func (s *Scanner) listIngressClasses(ctx context.Context, timeout time.Duration) ([]IngressClassInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	list, err := s.core.NetworkingV1().IngressClasses().List(ctx, metav1.ListOptions{Limit: listLimit})
	if err != nil {
		return nil, err
	}
	out := make([]IngressClassInfo, 0, len(list.Items))
	for _, ic := range list.Items {
		flavour, ok := ingressControllerFlavours[ic.Spec.Controller]
		if !ok {
			flavour = flavourUnknown
		}
		out = append(out, IngressClassInfo{
			Name:       ic.Name,
			Controller: ic.Spec.Controller,
			Flavour:    flavour,
			Default:    ic.Annotations["ingressclass.kubernetes.io/is-default-class"] == "true",
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// findIngressService looks for the controller's Service. One cluster-wide list
// answers it and also feeds the cert-manager namespace fallback; when listing is
// denied it degrades to getting the conventional locations by name, which still
// finds a stock install.
func (s *Scanner) findIngressService(ctx context.Context, timeout time.Duration) (*corev1.Service, error) {
	svcs, err := s.listServices(ctx, timeout)
	if err == nil {
		return pickIngressService(svcs), nil
	}
	if !isForbidden(err) {
		return nil, err
	}

	for _, wk := range wellKnownIngressServices {
		svc, getErr := s.getService(ctx, timeout, wk.Namespace, wk.Name)
		if getErr == nil {
			return svc, nil
		}
	}
	// Report the original denial: that is the honest reason the scan found nothing.
	return nil, err
}

func (s *Scanner) listServices(ctx context.Context, timeout time.Duration) ([]corev1.Service, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	list, err := s.core.CoreV1().Services("").List(ctx, metav1.ListOptions{Limit: listLimit})
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}

func (s *Scanner) getService(ctx context.Context, timeout time.Duration, namespace, name string) (*corev1.Service, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	return s.core.CoreV1().Services(namespace).Get(ctx, name, metav1.GetOptions{})
}

// pickIngressService chooses the controller Service: a recognisable controller by
// label first, then a conventional location, then the cluster's only LoadBalancer.
func pickIngressService(svcs []corev1.Service) *corev1.Service {
	for i := range svcs {
		if serviceFlavour(&svcs[i]) != "" {
			return &svcs[i]
		}
	}
	for _, wk := range wellKnownIngressServices {
		for i := range svcs {
			if svcs[i].Namespace == wk.Namespace && svcs[i].Name == wk.Name {
				return &svcs[i]
			}
		}
	}
	var only *corev1.Service
	for i := range svcs {
		if svcs[i].Spec.Type != corev1.ServiceTypeLoadBalancer {
			continue
		}
		if only != nil {
			return nil // ambiguous; let the admin say which
		}
		only = &svcs[i]
	}
	return only
}

// serviceFlavour recognises a controller from the labels a stock install carries.
func serviceFlavour(svc *corev1.Service) string {
	if svc == nil {
		return ""
	}
	switch svc.Labels["app.kubernetes.io/name"] {
	case "traefik":
		return flavourTraefik
	case "ingress-nginx":
		if c := svc.Labels["app.kubernetes.io/component"]; c == "" || c == "controller" {
			return "ingress-nginx"
		}
	}
	return ""
}

// pickIngressClass prefers the class belonging to the controller that was actually
// found, then the cluster default, then the only one.
func pickIngressClass(classes []IngressClassInfo, flavour string) *IngressClassInfo {
	if len(classes) == 0 {
		return nil
	}
	if flavour != "" {
		for i := range classes {
			if classes[i].Flavour == flavour {
				return &classes[i]
			}
		}
	}
	for i := range classes {
		if classes[i].Default {
			return &classes[i]
		}
	}
	if len(classes) == 1 {
		return &classes[0]
	}
	for i := range classes {
		if classes[i].Flavour == flavourTraefik {
			return &classes[i]
		}
	}
	return &classes[0]
}

func ingressWarnings(f IngressFinding, classes []IngressClassInfo) []Warning {
	var warns []Warning

	if f.Probe.Status == StatusAbsent && f.ClassesProbe.Status != StatusForbidden && len(classes) == 0 {
		return append(warns, Warning{
			Key:     WarnIngressAbsent,
			Message: "No ingress controller was found. Leave the Ingress Controller choice on \"Install Traefik ingress\" and EasyLab will deploy one.",
		})
	}

	if f.Flavour != "" && f.Flavour != flavourTraefik {
		warns = append(warns, Warning{
			Key:     WarnIngressNonTraefik,
			Message: fmt.Sprintf("This cluster's ingress controller is %s, serving the class %q — not Traefik. EasyLab will stamp that class onto every workspace ingress, so fill in the Ingress Class field (the scan does this for you) and leave it alone.", f.Flavour, f.Class),
		})
	}

	switch {
	case f.ExternalIP == "" && f.ExternalHost != "":
		warns = append(warns, Warning{
			Key:     WarnIngressHostnameOnly,
			Message: fmt.Sprintf("The ingress controller is reachable at the hostname %q but has no external IP. The Quick start mode needs an IP (it encodes one in the workspace URL), so set a custom domain for this lab instead.", f.ExternalHost),
		})
	case f.ExternalIP == "" && f.ServiceType == string(corev1.ServiceTypeLoadBalancer):
		warns = append(warns, Warning{
			Key:     WarnIngressNoAddress,
			Message: "The ingress controller's LoadBalancer has no external address yet. If it never gets one, workspaces will only be reachable from inside the cluster.",
		})
	case f.Service != "" && f.ServiceType != "" && f.ServiceType != string(corev1.ServiceTypeLoadBalancer):
		warns = append(warns, Warning{
			Key:     WarnIngressNotLoadBalancer,
			Message: fmt.Sprintf("The ingress controller's Service is a %s, not a LoadBalancer, so it has no external address for EasyLab to point DNS at.", f.ServiceType),
		})
	}

	if distinct := distinctControllers(classes); distinct > 1 {
		warns = append(warns, Warning{
			Key:     WarnIngressMultiple,
			Message: fmt.Sprintf("This cluster has %d ingress controllers. EasyLab picked the class %q — change the Ingress Class field if the workspaces should go through another one.", distinct, f.Class),
		})
	}
	return warns
}

func distinctControllers(classes []IngressClassInfo) int {
	seen := map[string]bool{}
	for _, c := range classes {
		seen[c.Controller] = true
	}
	return len(seen)
}

// probeCertManager checks for cert-manager, finds its namespace, and lists the
// ClusterIssuers. The three are one chain: each step is only worth running if the
// one before it found something.
func (s *Scanner) probeCertManager(ctx context.Context, timeout time.Duration) (CertManagerFinding, IssuerFinding, []Warning, []Warning, []error) {
	var (
		cm      CertManagerFinding
		issuers IssuerFinding
		cmWarns []Warning
		isWarns []Warning
		errs    []error
	)

	// Discovery, not a CRD client: apiextensions-apiserver is not a dependency, and
	// whether the API group is served is exactly the question being asked.
	_, err := s.disco.ServerResourcesForGroupVersion(certManagerGroupVersion)
	probe, logErr := classify(err)
	cm.Probe = probe
	if logErr != nil {
		errs = append(errs, logErr)
	}

	if probe.Status != StatusOK {
		issuers.Probe = Probe{Status: StatusSkipped}
		if probe.Status == StatusAbsent {
			cmWarns = append(cmWarns, Warning{
				Key:     WarnCertManagerAbsent,
				Message: "cert-manager is not installed. Leave the cert-manager choice on \"Install cert-manager\" and EasyLab will deploy it when this lab has a domain.",
			})
		}
		return cm, issuers, cmWarns, isWarns, errs
	}

	cm.Installed = true
	cm.Namespace, cm.NamespaceFrom = s.findCertManagerNamespace(ctx, timeout)

	items, issErr := s.listClusterIssuers(ctx, timeout)
	issProbe, issLogErr := classify(issErr)
	issuers.Probe = issProbe
	issuers.Items = items
	if issLogErr != nil {
		errs = append(errs, issLogErr)
	}
	isWarns = append(isWarns, issuerWarnings(issuers)...)

	return cm, issuers, cmWarns, isWarns, errs
}

// findCertManagerNamespace asks the cheapest exact source first. The validating
// webhook configuration is cluster-scoped and names the namespace outright, which
// beats hunting for a Deployment.
func (s *Scanner) findCertManagerNamespace(ctx context.Context, timeout time.Duration) (string, string) {
	if ns := s.namespaceFromWebhook(ctx, timeout); ns != "" {
		return ns, "webhook"
	}
	if svcs, err := s.listServices(ctx, timeout); err == nil {
		for i := range svcs {
			if svcs[i].Name == "cert-manager-webhook" {
				return svcs[i].Namespace, "service"
			}
		}
	}
	if ns := s.namespaceFromDeployment(ctx, timeout); ns != "" {
		return ns, "deployment"
	}
	if _, err := s.getNamespace(ctx, timeout, "cert-manager"); err == nil {
		return "cert-manager", "default"
	}
	return "", ""
}

func (s *Scanner) namespaceFromWebhook(ctx context.Context, timeout time.Duration) string {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cfg, err := s.core.AdmissionregistrationV1().ValidatingWebhookConfigurations().Get(ctx, "cert-manager-webhook", metav1.GetOptions{})
	if err != nil {
		return ""
	}
	for _, w := range cfg.Webhooks {
		if w.ClientConfig.Service != nil && w.ClientConfig.Service.Namespace != "" {
			return w.ClientConfig.Service.Namespace
		}
	}
	return ""
}

func (s *Scanner) namespaceFromDeployment(ctx context.Context, timeout time.Duration) string {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	list, err := s.core.AppsV1().Deployments("").List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/name=cert-manager",
		Limit:         listLimit,
	})
	if err != nil || len(list.Items) == 0 {
		return ""
	}
	return list.Items[0].Namespace
}

func (s *Scanner) listClusterIssuers(ctx context.Context, timeout time.Duration) ([]ClusterIssuerInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	list, err := s.dyn.Resource(clusterIssuerGVR).List(ctx, metav1.ListOptions{Limit: listLimit})
	if err != nil {
		return nil, err
	}
	out := make([]ClusterIssuerInfo, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, clusterIssuerInfo(list.Items[i].Object))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func clusterIssuerInfo(obj map[string]any) ClusterIssuerInfo {
	info := ClusterIssuerInfo{}
	info.Name, _, _ = unstructured.NestedString(obj, "metadata", "name")

	for _, kind := range []string{"acme", "ca", "selfSigned", "vault", "venafi"} {
		if _, found, _ := unstructured.NestedMap(obj, "spec", kind); found {
			info.Kind = kind
			break
		}
	}

	info.ACMEServer, _, _ = unstructured.NestedString(obj, "spec", "acme", "server")
	info.Staging = strings.Contains(info.ACMEServer, "acme-staging")

	if solvers, found, _ := unstructured.NestedSlice(obj, "spec", "acme", "solvers"); found {
		for _, raw := range solvers {
			solver, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if _, ok := solver["dns01"]; ok {
				info.HasDNS01 = true
			}
			if _, ok := solver["http01"]; ok {
				info.HasHTTP01 = true
			}
		}
	}

	if conds, found, _ := unstructured.NestedSlice(obj, "status", "conditions"); found {
		for _, raw := range conds {
			cond, ok := raw.(map[string]any)
			if !ok || cond["type"] != "Ready" {
				continue
			}
			info.Ready = cond["status"] == "True"
			if msg, ok := cond["message"].(string); ok {
				info.Message = truncate(msg, 200)
			}
		}
	}
	return info
}

func issuerWarnings(f IssuerFinding) []Warning {
	if f.Probe.Status != StatusOK || len(f.Items) == 0 {
		return nil
	}

	var warns []Warning
	var anyDNS01, anyUsable, anyProd bool
	var notReady []string
	for _, it := range f.Items {
		if it.HasDNS01 {
			anyDNS01 = true
		}
		if it.Usable() {
			anyUsable = true
		}
		if it.Kind == "acme" && !it.Staging {
			anyProd = true
		}
		if !it.Ready {
			notReady = append(notReady, it.Name)
		}
	}

	if !anyDNS01 {
		warns = append(warns, Warning{
			Key:     WarnIssuerNoDNS01,
			Message: "None of this cluster's ClusterIssuers has a DNS-01 solver, so none of them can issue the wildcard certificate a lab shares across its workspaces. Leave \"Is DNS-01 already set up?\" on \"Set it up for me\".",
		})
	}
	if len(notReady) > 0 && !anyUsable {
		warns = append(warns, Warning{
			Key:     WarnIssuerNotReady,
			Message: fmt.Sprintf("The ClusterIssuer(s) %s are not Ready, so nothing would issue certificates. EasyLab has not filled in an issuer name — fix them, or let EasyLab set DNS-01 up itself.", strings.Join(notReady, ", ")),
		})
	}
	if !anyProd && anyDNS01 {
		warns = append(warns, Warning{
			Key:     WarnIssuerStagingOnly,
			Message: "Every ACME ClusterIssuer on this cluster points at Let's Encrypt staging. Workspaces will work, but browsers will show their certificates as untrusted.",
		})
	}
	return warns
}

// probeStorage reports where workspace volumes would come from.
func (s *Scanner) probeStorage(ctx context.Context, timeout time.Duration) (StorageFinding, []Warning, []error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	list, err := s.core.StorageV1().StorageClasses().List(ctx, metav1.ListOptions{Limit: listLimit})
	probe, logErr := classify(err)
	if err != nil {
		return StorageFinding{Probe: probe}, nil, errList(logErr)
	}

	finding := StorageFinding{Probe: probe}
	defaults := 0
	for _, sc := range list.Items {
		isDefault := sc.Annotations["storageclass.kubernetes.io/is-default-class"] == "true" ||
			sc.Annotations["storageclass.beta.kubernetes.io/is-default-class"] == "true"
		info := StorageClassInfo{
			Name:        sc.Name,
			Provisioner: sc.Provisioner,
			Default:     isDefault,
			NodeLocal:   nodeLocalProvisioners[sc.Provisioner],
		}
		if sc.VolumeBindingMode != nil {
			info.BindingMode = string(*sc.VolumeBindingMode)
		}
		finding.Classes = append(finding.Classes, info)

		if isDefault {
			defaults++
			if finding.Default == "" {
				finding.Default = sc.Name
				finding.Provisioner = sc.Provisioner
				finding.NodeLocal = info.NodeLocal
				finding.BindingMode = info.BindingMode
			}
		}
	}
	sort.Slice(finding.Classes, func(i, j int) bool { return finding.Classes[i].Name < finding.Classes[j].Name })

	var warns []Warning
	switch {
	case len(finding.Classes) == 0:
		warns = append(warns, Warning{
			Key:     WarnStorageAbsent,
			Message: "This cluster has no StorageClass, so every workspace's volume would stay Pending and no workspace would start. Install a storage provisioner, or set the workspace templates to Ephemeral.",
		})
	case finding.Default == "":
		warns = append(warns, Warning{
			Key:     WarnStorageAbsent,
			Message: "No StorageClass is marked as the cluster default, so workspace volumes would stay Pending. Set a default, or name a storage class on each workspace template.",
		})
	case finding.NodeLocal:
		warns = append(warns, Warning{
			Key:     WarnStorageNodeLocal,
			Message: fmt.Sprintf("The default StorageClass %q uses %s, which keeps a volume on one node — a workspace rescheduled elsewhere comes back empty. Set a network-attached storage class on the workspace templates.", finding.Default, finding.Provisioner),
		})
	}
	if defaults > 1 {
		warns = append(warns, Warning{
			Key:     WarnStorageMultipleDefaults,
			Message: fmt.Sprintf("%d StorageClasses are marked as default. Kubernetes picks one unpredictably, so name the storage class on each workspace template instead.", defaults),
		})
	}
	return finding, warns, nil
}

// probeNamespaces lists the namespaces and reports whether this lab's workspace
// namespace already exists.
func (s *Scanner) probeNamespaces(ctx context.Context, timeout time.Duration, workspaceNS string) (NamespaceFinding, []Warning, []error) {
	finding := NamespaceFinding{WorkspaceName: workspaceNS}

	list, err := s.listNamespaces(ctx, timeout)
	if err == nil {
		finding.Probe = Probe{Status: StatusOK}
		finding.Names = list
		for _, n := range list {
			if n == workspaceNS {
				finding.WorkspaceExists = true
				break
			}
		}
		return finding, namespaceWarnings(finding), nil
	}

	// Listing every namespace is often denied where getting one by name is not, and
	// the only question that matters is whether this lab's namespace is there.
	if isForbidden(err) {
		if _, getErr := s.getNamespace(ctx, timeout, workspaceNS); getErr == nil {
			finding.Probe = Probe{Status: StatusOK}
			finding.WorkspaceExists = true
			return finding, namespaceWarnings(finding), nil
		}
	}

	probe, logErr := classify(err)
	finding.Probe = probe
	return finding, nil, errList(logErr)
}

func namespaceWarnings(f NamespaceFinding) []Warning {
	if !f.WorkspaceExists {
		// A missing namespace is fine: the lab's Pulumi program creates it.
		return nil
	}
	return []Warning{{
		Key:     WarnNamespaceExists,
		Message: fmt.Sprintf("The namespace %q already exists on this cluster. EasyLab creates the workspace namespace as part of deploying a lab, so if another lab or an earlier deploy owns this one, give this lab a namespace of its own.", f.WorkspaceName),
	}}
}

func (s *Scanner) listNamespaces(ctx context.Context, timeout time.Duration) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	list, err := s.core.CoreV1().Namespaces().List(ctx, metav1.ListOptions{Limit: listLimit})
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(list.Items))
	for _, ns := range list.Items {
		out = append(out, ns.Name)
	}
	sort.Strings(out)
	return out, nil
}

func (s *Scanner) getNamespace(ctx context.Context, timeout time.Duration, name string) (*corev1.Namespace, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	return s.core.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
}

// permissionTargets says which namespaces the permission pre-flight should ask
// about, and whether cert-manager's own resources are worth asking about at all.
type permissionTargets struct {
	WorkspaceNamespace string
	IngressNamespace   string
	CertManager        bool
}

// permissionWorkers bounds the SelfSubjectAccessReview fan-out. The checks are
// independent and cheap; a handful at a time keeps the whole pre-flight inside a
// couple of round trips without opening a connection per check.
const permissionWorkers = 8

// probePermissions asks the API server whether these credentials may do what
// EasyLab will ask of them, rather than finding out when the first student
// requests a workspace. The checks mirror what the workspace backend actually
// does: everything it creates in the workspace namespace, the portal's Role in the
// ingress controller's namespace, and the cluster-scoped reads a lab needs.
func (s *Scanner) probePermissions(ctx context.Context, timeout time.Duration, t permissionTargets) (PermissionFinding, []Warning, []error) {
	ws := t.WorkspaceNamespace
	ingressNS := t.IngressNamespace
	if ingressNS == "" {
		ingressNS = flavourTraefik
	}

	checks := []PermissionCheck{
		{Group: "apps", Resource: "deployments", Namespace: ws, Verb: "create"},
		{Resource: "services", Namespace: ws, Verb: "create"},
		{Group: "networking.k8s.io", Resource: "ingresses", Namespace: ws, Verb: "create"},
		{Resource: "persistentvolumeclaims", Namespace: ws, Verb: "create"},
		{Resource: "secrets", Namespace: ws, Verb: "create"},
		{Resource: "configmaps", Namespace: ws, Verb: "create"},
		{Resource: "serviceaccounts", Namespace: ws, Verb: "create"},
		{Group: "rbac.authorization.k8s.io", Resource: "roles", Namespace: ws, Verb: "create"},
		{Group: "rbac.authorization.k8s.io", Resource: "rolebindings", Namespace: ws, Verb: "create"},
		{Group: "batch", Resource: "jobs", Namespace: ws, Verb: "create"},
		{Group: "apps", Resource: "daemonsets", Namespace: ws, Verb: "create"},
		// The in-lab student portal needs to read the ingress controller's Service
		// from its own namespace, which means a Role over there.
		{Group: "rbac.authorization.k8s.io", Resource: "roles", Namespace: ingressNS, Verb: "create"},
		// Cluster-scoped: the lab's Pulumi program creates the workspace namespace,
		// and a scan reads nodes and storage classes.
		{Resource: "namespaces", Verb: "create"},
		{Resource: "nodes", Verb: "list"},
		{Group: "storage.k8s.io", Resource: "storageclasses", Verb: "list"},
	}
	if t.CertManager {
		checks = append(checks, PermissionCheck{Group: "cert-manager.io", Resource: "clusterissuers", Verb: "create"})
	}

	var (
		mu   sync.Mutex
		errs []error
		sem  = make(chan struct{}, permissionWorkers)
		wg   sync.WaitGroup
	)
	for i := range checks {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			allowed, reason, err := s.canI(ctx, timeout, checks[i])
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			checks[i].Allowed = allowed
			checks[i].Reason = truncate(reason, 200)
		}(i)
	}
	wg.Wait()

	finding := PermissionFinding{Checks: checks}
	if len(errs) == len(checks) {
		// Not one review came back: report that rather than claiming every
		// permission is missing.
		probe, _ := classify(errs[0])
		finding.Probe = probe
		finding.Checks = nil
		return finding, nil, errs
	}
	finding.Probe = Probe{Status: StatusOK}

	for _, c := range checks {
		if !c.Allowed {
			finding.Missing = append(finding.Missing, c.String())
		}
	}

	var warns []Warning
	if len(finding.Missing) > 0 {
		warns = append(warns, Warning{
			Key:     WarnPermissionsMissing,
			Message: fmt.Sprintf("This kubeconfig is missing %d of the permissions EasyLab needs: %s. The lab will deploy, but these operations will fail.", len(finding.Missing), strings.Join(finding.Missing, "; ")),
		})
	}
	return finding, warns, errs
}

func (s *Scanner) canI(ctx context.Context, timeout time.Duration, c PermissionCheck) (bool, string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	review := &authv1.SelfSubjectAccessReview{
		Spec: authv1.SelfSubjectAccessReviewSpec{
			ResourceAttributes: &authv1.ResourceAttributes{
				Namespace: c.Namespace,
				Verb:      c.Verb,
				Group:     c.Group,
				Resource:  c.Resource,
			},
		},
	}
	resp, err := s.core.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, review, metav1.CreateOptions{})
	if err != nil {
		return false, "", err
	}
	return resp.Status.Allowed, resp.Status.Reason, nil
}

func isForbidden(err error) bool {
	probe, _ := classify(err)
	return probe.Status == StatusForbidden
}

// errList wraps a single optional error, keeping the probe bodies free of nil
// checks when they return their error slice.
func errList(err error) []error {
	if err == nil {
		return nil
	}
	return []error{err}
}

func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}

// parseMinor reads the leading digits of a Kubernetes minor version, which can
// carry a build suffix ("30+").
func parseMinor(minor string) int {
	end := 0
	for end < len(minor) && minor[end] >= '0' && minor[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0
	}
	n, err := strconv.Atoi(minor[:end])
	if err != nil {
		return 0
	}
	return n
}
