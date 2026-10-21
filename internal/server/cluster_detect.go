package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"

	"easylab/internal/clusterscan"
)

// clusterDetectResponse is what the lab wizard's "Detect from cluster" panel
// renders. Report is everything the scan found, warnings included; Suggested is
// the subset that can safely be written into the form, keyed by the form's own
// field names so the front end can hand each value straight to setFieldValue.
//
// Mapping a Report onto form field names is deliberately done here rather than in
// clusterscan, which stays ignorant of LabConfig — the same split as
// devcontainerTemplate mapping a devcontainer.Result onto a WorkspaceTemplate.
type clusterDetectResponse struct {
	Report    *clusterscan.Report `json:"report"`
	Suggested clusterSuggestion   `json:"suggested"`
}

// clusterSuggestion holds values the scan is confident enough to prefill. Every
// pointer field is nil when the scan has no opinion, which is different from
// suggesting "false": the wizard must not overwrite a choice the admin made just
// because a probe came back empty.
type clusterSuggestion struct {
	InstallNginxIngress     *bool  `json:"install_nginx_ingress,omitempty"`
	NginxIngressNamespace   string `json:"nginx_ingress_namespace,omitempty"`
	NginxIngressServiceName string `json:"nginx_ingress_service_name,omitempty"`
	IngressClassName        string `json:"ingress_class_name,omitempty"`
	InstallCertManager      *bool  `json:"install_cert_manager,omitempty"`
	CertManagerNamespace    string `json:"cert_manager_namespace,omitempty"`
	ClusterIssuerName       string `json:"cluster_issuer_name,omitempty"`
	DNSAlreadyConfigured    *bool  `json:"dns_already_configured,omitempty"`
	WorkspaceNamespace      string `json:"workspace_namespace,omitempty"`
	StorageClass            string `json:"storage_class,omitempty"`
	NodeSelectorKey         string `json:"node_selector_key,omitempty"`
	NodeSelectorValue       string `json:"node_selector_value,omitempty"`
}

func boolPtr(b bool) *bool { return &b }

// suggestedFromReport decides which findings are safe to write into the form.
//
// The guiding rule is that silence beats a guess. A probe that was denied tells
// us nothing about the cluster, so nothing is suggested for it — in particular
// "no ingress controller found" never becomes "install one", because the admin
// may well have a controller these credentials simply cannot see.
func suggestedFromReport(rep *clusterscan.Report) clusterSuggestion {
	var s clusterSuggestion
	if rep == nil {
		return s
	}

	if rep.Ingress.OK() && rep.Ingress.Namespace != "" {
		s.InstallNginxIngress = boolPtr(false)
		s.NginxIngressNamespace = rep.Ingress.Namespace
		s.NginxIngressServiceName = rep.Ingress.Service
		s.IngressClassName = rep.Ingress.Class
	}

	if rep.CertManager.Installed {
		s.InstallCertManager = boolPtr(false)
		s.CertManagerNamespace = rep.CertManager.Namespace

		// Only an issuer that is Ready and has a DNS-01 solver is offered. Naming
		// one that cannot issue is the failure mode this whole feature exists to
		// prevent: cert-manager leaves the CertificateRequest pending forever and
		// the ingress controller quietly serves its own self-signed certificate.
		for _, issuer := range rep.Issuers.Items {
			if issuer.Usable() {
				s.ClusterIssuerName = issuer.Name
				s.DNSAlreadyConfigured = boolPtr(true)
				break
			}
		}
	}

	// Offering the namespace only when it already exists keeps the admin from
	// being handed a value that still has to be created.
	if rep.Namespaces.WorkspaceExists {
		s.WorkspaceNamespace = rep.Namespaces.WorkspaceName
	}

	// A node-local default loses a workspace's files the first time its pod moves,
	// so point at a network-attached class — but only if the cluster actually has
	// one to point at.
	if rep.Storage.NodeLocal {
		for _, sc := range rep.Storage.Classes {
			if !sc.NodeLocal {
				s.StorageClass = sc.Name
				break
			}
		}
	}

	if len(rep.Nodes.Labels) > 0 {
		top := rep.Nodes.Labels[0]
		s.NodeSelectorKey = top.Key
		if len(top.Values) > 0 {
			s.NodeSelectorValue = top.Values[0]
		}
	}

	return s
}

// DetectCluster scans the cluster a BYO lab would run on and reports what it
// found, so an admin does not have to look up the ingress namespace, the
// cert-manager namespace, the ClusterIssuer name and the default StorageClass by
// hand — and finds out about a missing permission here rather than when the first
// student asks for a workspace.
//
// The kubeconfig is used to build one client, read through, and dropped: it is
// never written to disk, never cached on the Handler, and never echoed back.
func (h *Handler) DetectCluster(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	if err := h.parseForm(w, r, maxUploadSizeSmall); err != nil {
		log.Printf("Cluster detect: failed to parse the form: %v", err)
		writeJSONError(w, http.StatusBadRequest, "the request could not be read")
		return
	}

	kubeconfig, err := h.readKubeconfigFromForm(r)
	if err != nil {
		log.Printf("Cluster detect: failed to read the submitted kubeconfig: %v", err)
		writeJSONError(w, http.StatusBadRequest, "the kubeconfig could not be read")
		return
	}
	if strings.TrimSpace(kubeconfig) == "" {
		writeJSONError(w, http.StatusBadRequest, "provide a kubeconfig first, then run the detection")
		return
	}

	report, probeErrs := h.scanCluster(r.Context(), kubeconfig, clusterscan.Options{
		WorkspaceNamespace: getFormValue(r, "workspace_namespace"),
	})

	invalidKubeconfig := false
	for _, probeErr := range probeErrs {
		if errors.Is(probeErr, clusterscan.ErrInvalidKubeconfig) {
			// Logged without the error, which for a parse failure can quote the
			// file's own bytes, credentials included.
			invalidKubeconfig = true
			log.Print("Cluster detect: the submitted kubeconfig could not be parsed")
			continue
		}
		log.Printf("Cluster detect probe: %v", probeErr)
	}

	// A nil report means the scan never got far enough to report anything: either
	// the kubeconfig is not usable or the cluster cannot be reached. Everything
	// else degrades into the report itself.
	if report == nil {
		if invalidKubeconfig {
			writeJSONError(w, http.StatusUnprocessableEntity,
				"this does not look like a usable kubeconfig — check that it has a server URL and credentials")
			return
		}
		writeJSONError(w, http.StatusUnprocessableEntity,
			"could not reach the cluster with this kubeconfig — check the server URL, and that this machine can reach it")
		return
	}

	h.recordAudit(adminActor(r), "admin", "cluster.detect", "", auditDetailForScan(report))

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(clusterDetectResponse{
		Report:    report,
		Suggested: suggestedFromReport(report),
	}); err != nil {
		log.Printf("Cluster detect: failed to encode the response: %v", err)
	}
}

// auditDetailForScan summarizes a scan for the audit log. It names only what was
// found on the cluster — never anything derived from the kubeconfig itself, which
// must not reach the log.
func auditDetailForScan(rep *clusterscan.Report) string {
	parts := []string{fmt.Sprintf("kubernetes %s, %d nodes", rep.Cluster.Version, rep.Nodes.Count)}
	if rep.Ingress.Namespace != "" {
		parts = append(parts, fmt.Sprintf("ingress %s/%s class %s",
			rep.Ingress.Namespace, rep.Ingress.Service, rep.Ingress.Class))
	} else {
		parts = append(parts, "no ingress controller found")
	}
	if rep.CertManager.Installed {
		parts = append(parts, fmt.Sprintf("cert-manager in %s, %d issuer(s)",
			rep.CertManager.Namespace, len(rep.Issuers.Items)))
	} else {
		parts = append(parts, "no cert-manager")
	}
	if n := len(rep.Warnings); n > 0 {
		parts = append(parts, fmt.Sprintf("%d warning(s)", n))
	}
	return strings.Join(parts, "; ")
}
