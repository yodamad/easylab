package azure

import "easylab/internal/providers"

// AzureConfig holds Azure-specific configuration for Pulumi programs.
type AzureConfig struct {
	SubscriptionID string
	Location       string
}

// GetProviderName returns the provider name.
func (c *AzureConfig) GetProviderName() string {
	return "azure"
}

// Ensure AzureConfig implements ProviderConfig.
var _ providers.ProviderConfig = (*AzureConfig)(nil)
