package ovh

import "easylab/internal/providers"

// OVHConfig holds OVH-specific configuration
type OVHConfig struct {
	ServiceName string
}

// GetProviderName returns the provider name
func (c *OVHConfig) GetProviderName() string {
	return "ovh"
}

// Ensure OVHConfig implements ProviderConfig
var _ providers.ProviderConfig = (*OVHConfig)(nil)
