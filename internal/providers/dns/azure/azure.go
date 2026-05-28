package azure

import (
	"easylab/internal/providers/dns"
	"easylab/utils"
	"fmt"
	"strings"

	azurenetwork "github.com/pulumi/pulumi-azure-native-sdk/network"
	k8s "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes"
	helmv3 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/helm/v3"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// AzureDNSProvider implements dns.Provider for Azure DNS zones.
type AzureDNSProvider struct{}

func New() *AzureDNSProvider { return &AzureDNSProvider{} }

func init() {
	_ = dns.Register(New())
}

func (p *AzureDNSProvider) Name() string { return "azure" }

func (p *AzureDNSProvider) GetCredentialFields() []dns.CredentialField {
	return []dns.CredentialField{
		{Name: utils.DNSAzureTenantId, Label: "Azure Tenant ID", Placeholder: "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx", IsSecret: false},
		{Name: utils.DNSAzureSubscriptionId, Label: "Azure Subscription ID", Placeholder: "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx", IsSecret: false},
		{Name: utils.DNSAzureResourceGroup, Label: "Resource Group", Placeholder: "my-dns-resource-group", IsSecret: false},
		{Name: utils.DNSAzureClientId, Label: "Client ID (Service Principal)", Placeholder: "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx", IsSecret: false},
		{Name: utils.DNSAzureClientSecret, Label: "Client Secret", Placeholder: "", IsSecret: true},
	}
}

// SetupCertManagerDNS01 returns the Azure DNS native cert-manager solver spec.
// cert-manager has built-in Azure DNS support so no webhook Helm chart is needed.
func (p *AzureDNSProvider) SetupCertManagerDNS01(
	ctx *pulumi.Context,
	_ *k8s.Provider,
	zone string,
	credSecretName string,
	_ []pulumi.Resource,
) (dns.SolverSpec, *helmv3.Release, error) {
	clientID := utils.DNSConfigOptional(ctx, utils.DNSAzureClientId)
	subscriptionID := utils.DNSConfigOptional(ctx, utils.DNSAzureSubscriptionId)
	tenantID := utils.DNSConfigOptional(ctx, utils.DNSAzureTenantId)
	resourceGroup := utils.DNSConfigOptional(ctx, utils.DNSAzureResourceGroup)

	solverSpec := dns.SolverSpec{
		"azureDNS": map[string]any{
			"clientID":          clientID,
			"subscriptionID":    subscriptionID,
			"tenantID":          tenantID,
			"resourceGroupName": resourceGroup,
			"hostedZoneName":    zone,
			"environment":       "AzurePublicCloud",
			"clientSecretSecretRef": map[string]any{
				"name": credSecretName,
				"key":  "client-secret",
			},
		},
	}

	return solverSpec, nil, nil
}

// CreateARecord creates an A record in the Azure DNS zone: subdomain.zone → ip.
func (p *AzureDNSProvider) CreateARecord(
	ctx *pulumi.Context,
	zone, subdomain string,
	ip pulumi.StringOutput,
	deps []pulumi.Resource,
) error {
	safe := strings.NewReplacer(".", "-", "*", "wildcard").Replace(subdomain)
	resourceGroup := utils.DNSConfigOptional(ctx, utils.DNSAzureResourceGroup)

	ttl := 300.0
	_, err := azurenetwork.NewRecordSet(ctx, "coder-dns-a-record-"+safe, &azurenetwork.RecordSetArgs{
		ZoneName:              pulumi.String(zone),
		ResourceGroupName:     pulumi.String(resourceGroup),
		RelativeRecordSetName: pulumi.String(subdomain),
		RecordType:            pulumi.String("A"),
		ARecords: azurenetwork.ARecordArray{
			azurenetwork.ARecordArgs{Ipv4Address: ip.ToStringPtrOutput()},
		},
		Ttl: pulumi.Float64Ptr(ttl),
	}, pulumi.DependsOn(deps))
	if err != nil {
		return fmt.Errorf("failed to create Azure DNS A record: %w", err)
	}
	return nil
}
