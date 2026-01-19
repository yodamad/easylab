package providers

import (
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// InfrastructureResult holds the results of infrastructure creation
type InfrastructureResult struct {
	KubeCluster pulumi.Resource
	NodePools   []pulumi.Resource
	Kubeconfig  pulumi.StringOutput
}

// Provider defines the interface that all cloud providers must implement
type Provider interface {
	// Name returns the provider identifier (e.g., "ovh", "aws", "azure")
	Name() string

	// GetRequiredEnvVars returns the list of environment variable names required by this provider
	GetRequiredEnvVars() []string

	// GetPulumiConfigPrefix returns the Pulumi config prefix for this provider (e.g., "ovh:", "aws:")
	GetPulumiConfigPrefix() string

	// CreateInfrastructure creates the provider-specific infrastructure (network, gateway, K8s cluster, node pools)
	// Returns the Kubernetes cluster resource, node pools, and kubeconfig
	CreateInfrastructure(ctx *pulumi.Context, config ProviderConfig) (*InfrastructureResult, error)
}

// ProviderConfig is an interface for provider-specific configuration
// Each provider implementation will have its own concrete type
type ProviderConfig interface {
	// GetProviderName returns the provider name
	GetProviderName() string
}
