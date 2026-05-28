package dns

import (
	k8s "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes"
	helmv3 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/helm/v3"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// CredentialField describes a form field required by a DNS provider.
type CredentialField struct {
	Name        string // config key suffix (e.g. "ovhAppKey") — used as form name "dns_cred_<Name>"
	Label       string
	Placeholder string
	IsSecret    bool
}

// SolverSpec is the raw map used as the dns01 block inside a cert-manager ClusterIssuer solver.
type SolverSpec map[string]any

// Provider defines the interface all DNS providers must implement.
type Provider interface {
	// Name returns the provider identifier (e.g. "ovh", "cloudflare").
	Name() string

	// GetCredentialFields returns the form fields to display for this provider.
	GetCredentialFields() []CredentialField

	// SetupCertManagerDNS01 installs any required cert-manager webhook Helm chart
	// and returns the solver spec to embed in the ClusterIssuer dns01 block.
	// credSecretName is the Kubernetes secret that holds this provider's API credentials.
	SetupCertManagerDNS01(
		ctx *pulumi.Context,
		k8sProvider *k8s.Provider,
		zone string,
		credSecretName string,
		deps []pulumi.Resource,
	) (SolverSpec, *helmv3.Release, error)

	// CreateARecord creates a DNS A record: subdomain.zone → ip.
	CreateARecord(
		ctx *pulumi.Context,
		zone, subdomain string,
		ip pulumi.StringOutput,
		deps []pulumi.Resource,
	) error
}
