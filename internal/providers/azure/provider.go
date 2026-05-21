package azure

import (
	"easylab/azure"
	"easylab/internal/providers"
	"fmt"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// AzureProvider implements the Provider interface for Microsoft Azure.
type AzureProvider struct{}

// NewAzureProvider creates a new Azure provider instance.
func NewAzureProvider() *AzureProvider {
	return &AzureProvider{}
}

// Name returns the provider identifier.
func (p *AzureProvider) Name() string {
	return "azure"
}

// GetRequiredEnvVars returns the list of environment variable names required by Azure.
func (p *AzureProvider) GetRequiredEnvVars() []string {
	return []string{
		"AZURE_CLIENT_ID",
		"AZURE_CLIENT_SECRET",
		"AZURE_TENANT_ID",
		"AZURE_SUBSCRIPTION_ID",
	}
}

// GetPulumiConfigPrefix returns the Pulumi config prefix for Azure.
func (p *AzureProvider) GetPulumiConfigPrefix() string {
	return "azure-native:"
}

// CreateInfrastructure creates Azure-specific infrastructure (Resource Group + AKS cluster).
func (p *AzureProvider) CreateInfrastructure(ctx *pulumi.Context, config providers.ProviderConfig) (*providers.InfrastructureResult, error) {
	azureCfg, ok := config.(*AzureConfig)
	if !ok {
		return nil, fmt.Errorf("invalid config type for Azure provider: expected *AzureConfig")
	}

	rg, err := azure.InitResourceGroup(ctx, azureCfg.Location, ctx.Stack())
	if err != nil {
		return nil, fmt.Errorf("failed to create resource group: %w", err)
	}

	// ClusterConfig is read from Pulumi stack config inside the program.
	// The provider just wires up the resource graph; concrete config values
	// are injected via setStackConfig before the program runs.
	clusterCfg := azure.ClusterConfig{
		ClusterName:  ctx.Stack(),
		NodePoolName: "default",
		VMSize:       "Standard_DS2_v2",
		NodeCount:    1,
	}

	cluster, err := azure.InitManagedKubernetesCluster(ctx, rg, clusterCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create AKS cluster: %w", err)
	}

	kubeconfig := azure.GetKubeconfig(ctx, cluster, rg)

	return &providers.InfrastructureResult{
		KubeCluster: cluster,
		NodePools:   []pulumi.Resource{},
		Kubeconfig:  kubeconfig,
	}, nil
}
