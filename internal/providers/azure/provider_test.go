package azure

import (
	"testing"

	"easylab/internal/providers"
)

type wrongAzureConfig struct{}

func (w *wrongAzureConfig) GetProviderName() string { return "wrong" }

var _ providers.ProviderConfig = (*wrongAzureConfig)(nil)

func TestAzureProvider_Name(t *testing.T) {
	p := NewAzureProvider()
	if got := p.Name(); got != "azure" {
		t.Errorf("Name() = %q, want %q", got, "azure")
	}
}

func TestAzureProvider_GetRequiredEnvVars(t *testing.T) {
	p := NewAzureProvider()
	vars := p.GetRequiredEnvVars()
	if len(vars) == 0 {
		t.Error("GetRequiredEnvVars() returned empty slice")
	}
	expected := map[string]bool{
		"AZURE_CLIENT_ID":       true,
		"AZURE_CLIENT_SECRET":   true,
		"AZURE_TENANT_ID":       true,
		"AZURE_SUBSCRIPTION_ID": true,
	}
	for _, v := range vars {
		if !expected[v] {
			t.Errorf("GetRequiredEnvVars() unexpected var: %q", v)
		}
	}
}

func TestAzureProvider_GetPulumiConfigPrefix(t *testing.T) {
	p := NewAzureProvider()
	prefix := p.GetPulumiConfigPrefix()
	if prefix == "" {
		t.Error("GetPulumiConfigPrefix() returned empty string")
	}
}

func TestAzureConfig_GetProviderName(t *testing.T) {
	c := &AzureConfig{Location: "eastus"}
	if got := c.GetProviderName(); got != "azure" {
		t.Errorf("GetProviderName() = %q, want %q", got, "azure")
	}
}

func TestAzureProvider_CreateInfrastructure_WrongConfig(t *testing.T) {
	p := NewAzureProvider()
	_, err := p.CreateInfrastructure(nil, &wrongAzureConfig{})
	if err == nil {
		t.Error("CreateInfrastructure() should error with wrong config type")
	}
}
