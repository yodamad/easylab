package ovh

import (
	"testing"

	"easylab/internal/providers"
)

func TestOVHProvider_Name(t *testing.T) {
	p := NewOVHProvider()
	if got := p.Name(); got != "ovh" {
		t.Errorf("Name() = %q, want %q", got, "ovh")
	}
}

func TestOVHProvider_GetRequiredEnvVars(t *testing.T) {
	p := NewOVHProvider()
	vars := p.GetRequiredEnvVars()
	if len(vars) == 0 {
		t.Error("GetRequiredEnvVars() returned empty slice")
	}
	expected := map[string]bool{
		"OVH_APPLICATION_KEY":    true,
		"OVH_APPLICATION_SECRET": true,
		"OVH_CONSUMER_KEY":       true,
		"OVH_SERVICE_NAME":       true,
	}
	for _, v := range vars {
		if !expected[v] {
			t.Errorf("GetRequiredEnvVars() unexpected var: %q", v)
		}
	}
}

func TestOVHProvider_GetPulumiConfigPrefix(t *testing.T) {
	p := NewOVHProvider()
	prefix := p.GetPulumiConfigPrefix()
	if prefix == "" {
		t.Error("GetPulumiConfigPrefix() returned empty string")
	}
}

func TestOVHConfig_GetProviderName(t *testing.T) {
	c := &OVHConfig{ServiceName: "my-service"}
	if got := c.GetProviderName(); got != "ovh" {
		t.Errorf("GetProviderName() = %q, want %q", got, "ovh")
	}
}

// wrongConfig implements providers.ProviderConfig but is not *OVHConfig
type wrongConfig struct{}

func (w *wrongConfig) GetProviderName() string { return "wrong" }

func TestOVHProvider_CreateInfrastructure_WrongConfig(t *testing.T) {
	p := NewOVHProvider()
	_, err := p.CreateInfrastructure(nil, &wrongConfig{})
	if err == nil {
		t.Error("CreateInfrastructure() should error with wrong config type")
	}
}

var _ providers.ProviderConfig = (*wrongConfig)(nil)
