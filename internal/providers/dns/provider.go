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
	// certManagerNamespace is the namespace cert-manager's own controller runs in
	// (defaults to "cert-manager" when empty) — a webhook chart's RBAC must grant
	// that exact ServiceAccount access, or DNS-01 challenges never resolve.
	//
	// installWebhook false returns the solver spec without installing anything,
	// for a lab joining a cluster whose webhook is already there. The webhook is
	// cluster-wide and serves any number of ClusterIssuers, each carrying its own
	// credentials through the solver spec, so a joining lab builds its own issuer
	// against the existing install. The returned Helm release is then nil, which
	// callers already handle for providers that install no webhook at all.
	SetupCertManagerDNS01(
		ctx *pulumi.Context,
		k8sProvider *k8s.Provider,
		zone string,
		credSecretName string,
		certManagerNamespace string,
		installWebhook bool,
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
