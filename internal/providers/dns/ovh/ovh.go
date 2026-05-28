package ovh

import (
	"easylab/internal/providers/dns"
	"easylab/utils"
	"fmt"
	"strings"

	ovhpkg "github.com/ovh/pulumi-ovh/sdk/v2/go/ovh"
	ovhdomain "github.com/ovh/pulumi-ovh/sdk/v2/go/ovh/domain"
	k8s "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes"
	k8score "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/core/v1"
	helmv3 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/helm/v3"
	k8smeta "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/meta/v1"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// OVHDNSProvider implements dns.Provider for OVH DNS zones.
type OVHDNSProvider struct{}

func New() *OVHDNSProvider { return &OVHDNSProvider{} }

func init() {
	_ = dns.Register(New())
}

func (p *OVHDNSProvider) Name() string { return "ovh" }

func (p *OVHDNSProvider) GetCredentialFields() []dns.CredentialField {
	return []dns.CredentialField{
		{Name: utils.DNSOvhAppKey, Label: "OVH Application Key", Placeholder: "your-app-key", IsSecret: false},
		{Name: utils.DNSOvhAppSecret, Label: "OVH Application Secret", Placeholder: "your-app-secret", IsSecret: true},
		{Name: utils.DNSOvhConsumerKey, Label: "OVH Consumer Key", Placeholder: "your-consumer-key", IsSecret: true},
		{Name: utils.DNSOvhEndpoint, Label: "OVH Endpoint", Placeholder: "ovh-eu", IsSecret: false},
	}
}

// SetupCertManagerDNS01 installs cert-manager-webhook-ovh and returns the DNS-01 solver spec.
// credSecretName is the Kubernetes secret that holds the OVH API credentials.
func (p *OVHDNSProvider) SetupCertManagerDNS01(
	ctx *pulumi.Context,
	k8sProvider *k8s.Provider,
	zone string,
	credSecretName string,
	deps []pulumi.Resource,
) (dns.SolverSpec, *helmv3.Release, error) {

	webhookNs, err := k8score.NewNamespace(ctx, "cert-manager-webhook-ovh-ns", &k8score.NamespaceArgs{
		Metadata: &k8smeta.ObjectMetaArgs{
			Name: pulumi.String("cert-manager-webhook-ovh"),
		},
	}, pulumi.Provider(k8sProvider), pulumi.DependsOn(deps))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create webhook namespace: %w", err)
	}

	webhookDeps := append(deps, webhookNs)

	webhookRelease, err := helmv3.NewRelease(ctx, "cert-manager-webhook-ovh", &helmv3.ReleaseArgs{
		Chart:           pulumi.String("cert-manager-webhook-ovh"),
		Name:            pulumi.String("cert-manager-webhook-ovh"),
		Namespace:       webhookNs.Metadata.Name(),
		CreateNamespace: pulumi.Bool(false),
		RepositoryOpts: &helmv3.RepositoryOptsArgs{
			Repo: pulumi.String("https://aureq.github.io/cert-manager-webhook-ovh"),
		},
		Version: pulumi.String("0.9.10"),
		Values: pulumi.Map{
			"groupName": pulumi.String("acme.baarde.ch"),
		},
		Timeout: pulumi.Int(600),
	}, pulumi.Provider(k8sProvider), pulumi.DependsOn(webhookDeps))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to install cert-manager-webhook-ovh: %w", err)
	}

	endpoint := utils.DNSConfigOptional(ctx, utils.DNSOvhEndpoint)
	if endpoint == "" {
		endpoint = "ovh-eu"
	}

	solverSpec := dns.SolverSpec{
		"webhook": map[string]any{
			"groupName":  "acme.baarde.ch",
			"solverName": "ovh",
			"config": map[string]any{
				"endpoint":             endpoint,
				"authenticationMethod": "application",
				"applicationKeyRef": map[string]any{
					"name": credSecretName,
					"key":  "applicationKey",
				},
				"applicationSecretRef": map[string]any{
					"name": credSecretName,
					"key":  "applicationSecret",
				},
				"applicationConsumerKeyRef": map[string]any{
					"name": credSecretName,
					"key":  "consumerKey",
				},
			},
		},
	}

	return solverSpec, webhookRelease, nil
}

// CreateARecord creates an A record in the OVH DNS zone: subdomain.zone → ip.
// It uses an explicit OVH provider built from the dns:ovh* credentials so that
// the DNS token (which has /domain/zone/* rights) is used instead of the default
// cloud token (which typically only has /cloud/* rights).
func (p *OVHDNSProvider) CreateARecord(
	ctx *pulumi.Context,
	zone, subdomain string,
	ip pulumi.StringOutput,
	deps []pulumi.Resource,
) error {
	safe := strings.NewReplacer(".", "-", "*", "wildcard").Replace(subdomain)

	endpoint := utils.DNSConfigOptional(ctx, utils.DNSOvhEndpoint)
	if endpoint == "" {
		endpoint = "ovh-eu"
	}
	// Each call gets a uniquely named provider to avoid duplicate URN errors when
	// CreateARecord is called for both the main record and the wildcard.
	ovhProvider, err := ovhpkg.NewProvider(ctx, "ovh-dns-provider-"+safe, &ovhpkg.ProviderArgs{
		Endpoint:          pulumi.StringPtr(endpoint),
		ApplicationKey:    pulumi.StringPtr(utils.DNSConfigOptional(ctx, utils.DNSOvhAppKey)),
		ApplicationSecret: pulumi.StringPtr(utils.DNSConfigOptional(ctx, utils.DNSOvhAppSecret)),
		ConsumerKey:       pulumi.StringPtr(utils.DNSConfigOptional(ctx, utils.DNSOvhConsumerKey)),
	})
	if err != nil {
		return fmt.Errorf("failed to create OVH DNS provider: %w", err)
	}

	resourceName := "coder-dns-a-record-" + safe
	_, err = ovhdomain.NewZoneRecord(ctx, resourceName, &ovhdomain.ZoneRecordArgs{
		Zone:      pulumi.String(zone),
		Subdomain: pulumi.String(subdomain),
		Fieldtype: pulumi.String("A"),
		Ttl:       pulumi.Int(300),
		Target:    ip,
	}, pulumi.Provider(ovhProvider), pulumi.DependsOn(deps))
	if err != nil {
		return fmt.Errorf("failed to create DNS A record: %w", err)
	}
	return nil
}
