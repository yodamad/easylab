package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestAzureOptionsManager(t *testing.T) *AzureOptionsManager {
	t.Helper()
	return NewAzureOptionsManager(t.TempDir(), NewCredentialsManager())
}

func TestNewAzureOptionsManager(t *testing.T) {
	m := newTestAzureOptionsManager(t)
	assert.NotNil(t, m)
	assert.NotNil(t, m.config.VMSizes)
	assert.Empty(t, m.cachedRegionOrder)
}

func TestAzureOptionsManager_LoadConfig_MissingFile(t *testing.T) {
	m := newTestAzureOptionsManager(t)
	err := m.LoadConfig()
	assert.NoError(t, err)
}

func TestAzureOptionsManager_SaveAndLoadConfig(t *testing.T) {
	m := newTestAzureOptionsManager(t)

	cfg := AzureOptionsConfig{
		Regions: AzureItemConfig{
			Enabled: []string{"eastus", "westeurope"},
			Default: "eastus",
		},
		VMSizes: map[string]AzureItemConfig{
			"eastus": {Enabled: []string{"Standard_B2s", "Standard_D4s_v3"}, Default: "Standard_B2s"},
		},
	}
	m.SetConfig(cfg)
	require.NoError(t, m.SaveConfig())

	m2 := NewAzureOptionsManager(m.dataDir, NewCredentialsManager())
	got := m2.GetConfig()

	assert.Equal(t, []string{"eastus", "westeurope"}, got.Regions.Enabled)
	assert.Equal(t, "eastus", got.Regions.Default)
	assert.Equal(t, "Standard_B2s", got.VMSizes["eastus"].Default)
}

func TestAzureOptionsManager_SaveConfig_NoDataDir(t *testing.T) {
	m := &AzureOptionsManager{
		config:  AzureOptionsConfig{VMSizes: make(map[string]AzureItemConfig)},
		dataDir: "",
	}
	err := m.SaveConfig()
	assert.NoError(t, err)
}

func TestAzureOptionsManager_LoadConfig_NoDataDir(t *testing.T) {
	m := &AzureOptionsManager{dataDir: ""}
	err := m.LoadConfig()
	assert.NoError(t, err)
}

func TestAzureOptionsManager_LoadConfig_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "azure-options.json"), []byte("bad-json"), 0644)
	require.NoError(t, err)

	m := &AzureOptionsManager{
		dataDir:  dir,
		config:   AzureOptionsConfig{VMSizes: make(map[string]AzureItemConfig)},
		credsMgr: NewCredentialsManager(),
	}
	err = m.LoadConfig()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse")
}

func TestAzureOptionsManager_GetAzureADConfig(t *testing.T) {
	m := newTestAzureOptionsManager(t)
	m.SetConfig(AzureOptionsConfig{
		AzureAD: AzureADConfig{
			ClientID: "my-client",
			TenantID: "my-tenant",
		},
		VMSizes: make(map[string]AzureItemConfig),
	})

	got := m.GetAzureADConfig()
	assert.Equal(t, "my-client", got.ClientID)
	assert.Equal(t, "my-tenant", got.TenantID)
}

func TestAzureOptionsManager_SetAzureADConfig(t *testing.T) {
	m := newTestAzureOptionsManager(t)
	cfg := AzureADConfig{ClientID: "id", ClientSecret: "secret", TenantID: "tenant"}
	err := m.SetAzureADConfig(cfg)
	require.NoError(t, err)

	got := m.GetAzureADConfig()
	assert.Equal(t, "id", got.ClientID)
}

func TestAzureOptionsManager_GetConfig_ReturnsCopy(t *testing.T) {
	m := newTestAzureOptionsManager(t)
	m.SetConfig(AzureOptionsConfig{
		Regions: AzureItemConfig{Enabled: []string{"eastus"}},
		VMSizes: make(map[string]AzureItemConfig),
	})

	cfg := m.GetConfig()
	cfg.Regions.Enabled = append(cfg.Regions.Enabled, "westus")

	original := m.GetConfig()
	assert.Len(t, original.Regions.Enabled, 1, "GetConfig should return a copy")
}

func TestAzureOptionsManager_HasCache_Empty(t *testing.T) {
	m := newTestAzureOptionsManager(t)
	assert.False(t, m.HasCache())
}

func TestAzureOptionsManager_HasCache_NonEmpty(t *testing.T) {
	m := newTestAzureOptionsManager(t)
	m.mu.Lock()
	m.cachedRegionOrder = []string{"eastus"}
	m.mu.Unlock()
	assert.True(t, m.HasCache())
}

func TestAzureOptionsManager_GetCachedRegions_Empty(t *testing.T) {
	m := newTestAzureOptionsManager(t)
	assert.Empty(t, m.GetCachedRegions())
}

func TestAzureOptionsManager_GetCachedRegions_ReturnsCopy(t *testing.T) {
	m := newTestAzureOptionsManager(t)
	m.mu.Lock()
	m.cachedRegionOrder = []string{"eastus", "westeurope"}
	m.mu.Unlock()

	regions := m.GetCachedRegions()
	assert.Len(t, regions, 2)
	// Mutate the returned slice
	regions[0] = "modified"
	// Original should be untouched
	assert.Equal(t, "eastus", m.GetCachedRegions()[0])
}

func TestAzureOptionsManager_RegionDisplayName_Known(t *testing.T) {
	m := newTestAzureOptionsManager(t)
	m.mu.Lock()
	m.cachedRegionDisplay = map[string]string{"eastus": "East US"}
	m.mu.Unlock()

	assert.Equal(t, "East US", m.RegionDisplayName("eastus"))
}

func TestAzureOptionsManager_RegionDisplayName_Unknown(t *testing.T) {
	m := newTestAzureOptionsManager(t)
	// Unknown region should fall back to the name itself
	assert.Equal(t, "unknown-region", m.RegionDisplayName("unknown-region"))
}

func TestAzureOptionsManager_GetCachedVMSizes_Empty(t *testing.T) {
	m := newTestAzureOptionsManager(t)
	sizes := m.GetCachedVMSizes("eastus")
	assert.Empty(t, sizes)
}

func TestAzureOptionsManager_GetCachedVMSizes_ReturnsCopy(t *testing.T) {
	m := newTestAzureOptionsManager(t)
	m.mu.Lock()
	m.cachedVMSizes = map[string][]azureVMSize{
		"eastus": {{Name: "Standard_B2s", VCPUs: 2, RAMGB: 4}},
	}
	m.mu.Unlock()

	sizes := m.GetCachedVMSizes("eastus")
	assert.Len(t, sizes, 1)
	sizes[0].Name = "modified"
	assert.Equal(t, "Standard_B2s", m.GetCachedVMSizes("eastus")[0].Name)
}

func TestAzureOptionsManager_GetRegionsForForm_NoCacheNoConfig(t *testing.T) {
	m := newTestAzureOptionsManager(t)
	regions, def := m.GetRegionsForForm()
	assert.Empty(t, regions)
	assert.Empty(t, def)
}

func TestAzureOptionsManager_GetRegionsForForm_AllWhenNoFilter(t *testing.T) {
	m := newTestAzureOptionsManager(t)
	m.mu.Lock()
	m.cachedRegionOrder = []string{"eastus", "westeurope", "eastasia"}
	m.mu.Unlock()

	regions, _ := m.GetRegionsForForm()
	assert.Len(t, regions, 3)
}

func TestAzureOptionsManager_GetRegionsForForm_WithFilter(t *testing.T) {
	m := newTestAzureOptionsManager(t)
	m.mu.Lock()
	m.cachedRegionOrder = []string{"eastus", "westeurope", "eastasia"}
	m.mu.Unlock()
	m.SetConfig(AzureOptionsConfig{
		Regions: AzureItemConfig{Enabled: []string{"eastus", "westeurope"}, Default: "eastus"},
		VMSizes: make(map[string]AzureItemConfig),
	})

	regions, def := m.GetRegionsForForm()
	assert.Len(t, regions, 2)
	assert.Equal(t, "eastus", regions[0], "default should be first")
	assert.Equal(t, "eastus", def)
}

func TestAzureOptionsManager_GetVMSizesForForm_Missing(t *testing.T) {
	m := newTestAzureOptionsManager(t)
	sizes, def := m.GetVMSizesForForm("eastus")
	assert.Nil(t, sizes)
	assert.Empty(t, def)
}

func TestAzureOptionsManager_GetVMSizesForForm_NoFilter(t *testing.T) {
	m := newTestAzureOptionsManager(t)
	m.mu.Lock()
	m.cachedVMSizes = map[string][]azureVMSize{
		"eastus": {
			{Name: "Standard_B2s", VCPUs: 2, RAMGB: 4},
			{Name: "Standard_D4s_v3", VCPUs: 4, RAMGB: 16},
		},
	}
	m.mu.Unlock()

	sizes, def := m.GetVMSizesForForm("eastus")
	assert.Len(t, sizes, 2)
	assert.Empty(t, def)
}

func TestAzureOptionsManager_GetVMSizesForForm_WithFilter(t *testing.T) {
	m := newTestAzureOptionsManager(t)
	m.mu.Lock()
	m.cachedVMSizes = map[string][]azureVMSize{
		"eastus": {
			{Name: "Standard_B2s", VCPUs: 2, RAMGB: 4},
			{Name: "Standard_D4s_v3", VCPUs: 4, RAMGB: 16},
			{Name: "Standard_E8s_v3", VCPUs: 8, RAMGB: 64},
		},
	}
	m.mu.Unlock()
	m.SetConfig(AzureOptionsConfig{
		VMSizes: map[string]AzureItemConfig{
			"eastus": {Enabled: []string{"Standard_B2s", "Standard_D4s_v3"}, Default: "Standard_D4s_v3"},
		},
	})

	sizes, def := m.GetVMSizesForForm("eastus")
	assert.Len(t, sizes, 2)
	assert.Equal(t, "Standard_D4s_v3", def)
	assert.Equal(t, "Standard_D4s_v3", sizes[0].Name, "default should be first")
}

func TestAzureOptionsManager_SetConfig_NilVMSizes(t *testing.T) {
	m := newTestAzureOptionsManager(t)
	m.SetConfig(AzureOptionsConfig{VMSizes: nil})
	got := m.GetConfig()
	assert.NotNil(t, got.VMSizes)
}
