package server

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- filterFlavorsByCPURAM tests ---

func TestFilterFlavorsByCPURAM_NoFilter(t *testing.T) {
	flavors := []ovhFlavor{
		{Name: "a", VCPUs: 2, RAM: 4},
		{Name: "b", VCPUs: 4, RAM: 8},
	}
	got := filterFlavorsByCPURAM(flavors, 0, 0, 0, 0)
	assert.Equal(t, flavors, got)
}

func TestFilterFlavorsByCPURAM_MinVCPUs(t *testing.T) {
	flavors := []ovhFlavor{
		{Name: "small", VCPUs: 1, RAM: 2},
		{Name: "medium", VCPUs: 4, RAM: 8},
		{Name: "large", VCPUs: 8, RAM: 16},
	}
	got := filterFlavorsByCPURAM(flavors, 4, 0, 0, 0)
	assert.Len(t, got, 2)
	assert.Equal(t, "medium", got[0].Name)
}

func TestFilterFlavorsByCPURAM_MaxVCPUs(t *testing.T) {
	flavors := []ovhFlavor{
		{Name: "small", VCPUs: 2, RAM: 4},
		{Name: "large", VCPUs: 16, RAM: 32},
	}
	got := filterFlavorsByCPURAM(flavors, 0, 4, 0, 0)
	assert.Len(t, got, 1)
	assert.Equal(t, "small", got[0].Name)
}

func TestFilterFlavorsByCPURAM_MinRAM(t *testing.T) {
	flavors := []ovhFlavor{
		{Name: "tiny", VCPUs: 2, RAM: 1},
		{Name: "normal", VCPUs: 4, RAM: 8},
	}
	got := filterFlavorsByCPURAM(flavors, 0, 0, 4, 0)
	assert.Len(t, got, 1)
	assert.Equal(t, "normal", got[0].Name)
}

func TestFilterFlavorsByCPURAM_MaxRAM(t *testing.T) {
	flavors := []ovhFlavor{
		{Name: "small", VCPUs: 2, RAM: 4},
		{Name: "big", VCPUs: 8, RAM: 64},
	}
	got := filterFlavorsByCPURAM(flavors, 0, 0, 0, 16)
	assert.Len(t, got, 1)
	assert.Equal(t, "small", got[0].Name)
}

func TestFilterFlavorsByCPURAM_Combined(t *testing.T) {
	flavors := []ovhFlavor{
		{Name: "micro", VCPUs: 1, RAM: 1},
		{Name: "small", VCPUs: 2, RAM: 4},
		{Name: "medium", VCPUs: 4, RAM: 8},
		{Name: "large", VCPUs: 8, RAM: 16},
	}
	// minVCPUs=2, maxVCPUs=4, minRAM=4, maxRAM=8 → small (2,4) and medium (4,8) both pass
	got := filterFlavorsByCPURAM(flavors, 2, 4, 4, 8)
	assert.Len(t, got, 2)
	// Verify micro and large are excluded
	names := make([]string, len(got))
	for i, f := range got {
		names[i] = f.Name
	}
	assert.Contains(t, names, "small")
	assert.Contains(t, names, "medium")
}

func TestFilterFlavorsByCPURAM_Empty(t *testing.T) {
	got := filterFlavorsByCPURAM(nil, 2, 0, 0, 0)
	assert.Empty(t, got)
}

// --- toSet tests ---

func TestToSet(t *testing.T) {
	items := []string{"a", "b", "c", "a"}
	s := toSet(items)
	assert.True(t, s["a"])
	assert.True(t, s["b"])
	assert.True(t, s["c"])
	assert.False(t, s["d"])
}

func TestToSet_Empty(t *testing.T) {
	s := toSet(nil)
	assert.Empty(t, s)
}

// --- moveToFront tests ---

func TestMoveToFront_Found(t *testing.T) {
	items := []string{"a", "b", "c", "d"}
	result := moveToFront(items, "c")
	assert.Equal(t, "c", result[0])
}

func TestMoveToFront_AlreadyFirst(t *testing.T) {
	items := []string{"a", "b", "c"}
	result := moveToFront(items, "a")
	assert.Equal(t, "a", result[0])
	assert.Equal(t, []string{"a", "b", "c"}, result)
}

func TestMoveToFront_NotFound(t *testing.T) {
	items := []string{"a", "b", "c"}
	result := moveToFront(items, "z")
	assert.Equal(t, []string{"a", "b", "c"}, result)
}

func TestMoveToFront_SingleElement(t *testing.T) {
	items := []string{"only"}
	result := moveToFront(items, "only")
	assert.Equal(t, []string{"only"}, result)
}

// --- OVHOptionsManager tests ---

func newTestOVHOptionsManager(t *testing.T) *OVHOptionsManager {
	t.Helper()
	cm := NewCredentialsManager()
	return NewOVHOptionsManager(t.TempDir(), cm)
}

func TestNewOVHOptionsManager(t *testing.T) {
	m := newTestOVHOptionsManager(t)
	assert.NotNil(t, m)
	assert.NotNil(t, m.config.Flavors)
	assert.Empty(t, m.cachedRegions)
}

func TestOVHOptionsManager_LoadConfig_MissingFile(t *testing.T) {
	m := newTestOVHOptionsManager(t)
	err := m.LoadConfig()
	assert.NoError(t, err)
}

func TestOVHOptionsManager_SaveAndLoadConfig(t *testing.T) {
	m := newTestOVHOptionsManager(t)

	cfg := OVHOptionsConfig{
		Regions: OVHItemConfig{
			Enabled: []string{"GRA7", "DE1"},
			Default: "GRA7",
		},
		Flavors: map[string]OVHItemConfig{
			"GRA7": {Enabled: []string{"b2-7", "b2-15"}, Default: "b2-7"},
		},
		FlavorMinVCPUs: 2,
		FlavorMaxVCPUs: 8,
		FlavorMinRAM:   4,
		FlavorMaxRAM:   32,
	}

	m.SetConfig(cfg)
	require.NoError(t, m.SaveConfig())

	m2 := NewOVHOptionsManager(m.dataDir, NewCredentialsManager())
	got := m2.GetConfig()

	assert.Equal(t, []string{"GRA7", "DE1"}, got.Regions.Enabled)
	assert.Equal(t, "GRA7", got.Regions.Default)
	assert.Equal(t, 2, got.FlavorMinVCPUs)
	assert.Equal(t, 8, got.FlavorMaxVCPUs)
	assert.Equal(t, "b2-7", got.Flavors["GRA7"].Default)
}

func TestOVHOptionsManager_GetConfig_ReturnsCopy(t *testing.T) {
	m := newTestOVHOptionsManager(t)
	m.SetConfig(OVHOptionsConfig{
		Regions: OVHItemConfig{Enabled: []string{"GRA7"}},
		Flavors: make(map[string]OVHItemConfig),
	})

	cfg := m.GetConfig()
	cfg.Regions.Enabled = append(cfg.Regions.Enabled, "DE1")

	original := m.GetConfig()
	assert.Len(t, original.Regions.Enabled, 1, "GetConfig should return a copy, not the original slice")
}

func TestOVHOptionsManager_HasCache_Empty(t *testing.T) {
	m := newTestOVHOptionsManager(t)
	assert.False(t, m.HasCache())
}

func TestOVHOptionsManager_HasCache_NonEmpty(t *testing.T) {
	m := newTestOVHOptionsManager(t)
	m.mu.Lock()
	m.cachedRegions = map[string]bool{"GRA7": true}
	m.mu.Unlock()
	assert.True(t, m.HasCache())
}

func TestOVHOptionsManager_GetCachedRegions_Empty(t *testing.T) {
	m := newTestOVHOptionsManager(t)
	assert.Empty(t, m.GetCachedRegions())
}

func TestOVHOptionsManager_GetCachedRegions_Sorted(t *testing.T) {
	m := newTestOVHOptionsManager(t)
	m.mu.Lock()
	m.cachedRegions = map[string]bool{"DE1": true, "GRA7": true, "BHS5": true}
	m.mu.Unlock()

	regions := m.GetCachedRegions()
	assert.True(t, sort.StringsAreSorted(regions))
	assert.Len(t, regions, 3)
}

func TestOVHOptionsManager_GetCachedFlavors_Missing(t *testing.T) {
	m := newTestOVHOptionsManager(t)
	flavors := m.GetCachedFlavors("GRA7")
	assert.Empty(t, flavors)
}

func TestOVHOptionsManager_GetCachedFlavors_ReturnsCopy(t *testing.T) {
	m := newTestOVHOptionsManager(t)
	m.mu.Lock()
	m.cachedFlavors = map[string][]ovhFlavor{
		"GRA7": {{Name: "b2-7", VCPUs: 2, RAM: 7}},
	}
	m.mu.Unlock()

	flavors := m.GetCachedFlavors("GRA7")
	assert.Len(t, flavors, 1)
	flavors[0].Name = "modified"

	// Original should be untouched
	original := m.GetCachedFlavors("GRA7")
	assert.Equal(t, "b2-7", original[0].Name)
}

func TestOVHOptionsManager_GetRegionsForForm_NoCacheNoConfig(t *testing.T) {
	m := newTestOVHOptionsManager(t)
	regions, def := m.GetRegionsForForm()
	assert.Empty(t, regions)
	assert.Empty(t, def)
}

func TestOVHOptionsManager_GetRegionsForForm_AllWhenNoFilter(t *testing.T) {
	m := newTestOVHOptionsManager(t)
	m.mu.Lock()
	m.cachedRegions = map[string]bool{"GRA7": true, "DE1": true, "BHS5": true}
	m.mu.Unlock()

	regions, _ := m.GetRegionsForForm()
	assert.Len(t, regions, 3)
	assert.True(t, sort.StringsAreSorted(regions))
}

func TestOVHOptionsManager_GetRegionsForForm_WithEnabledFilter(t *testing.T) {
	m := newTestOVHOptionsManager(t)
	m.mu.Lock()
	m.cachedRegions = map[string]bool{"GRA7": true, "DE1": true, "BHS5": true}
	m.mu.Unlock()
	m.SetConfig(OVHOptionsConfig{
		Regions: OVHItemConfig{Enabled: []string{"GRA7", "BHS5"}, Default: "GRA7"},
		Flavors: make(map[string]OVHItemConfig),
	})

	regions, def := m.GetRegionsForForm()
	assert.Len(t, regions, 2)
	assert.Equal(t, "GRA7", regions[0], "default region should be moved to front")
	assert.Equal(t, "GRA7", def)
}

func TestOVHOptionsManager_GetFlavorsForForm_Missing(t *testing.T) {
	m := newTestOVHOptionsManager(t)
	flavors, def := m.GetFlavorsForForm("GRA7")
	assert.Nil(t, flavors)
	assert.Empty(t, def)
}

func TestOVHOptionsManager_GetFlavorsForForm_NoFilter(t *testing.T) {
	m := newTestOVHOptionsManager(t)
	m.mu.Lock()
	m.cachedFlavors = map[string][]ovhFlavor{
		"GRA7": {
			{Name: "b2-7", VCPUs: 2, RAM: 7},
			{Name: "b2-15", VCPUs: 4, RAM: 15},
		},
	}
	m.mu.Unlock()

	flavors, def := m.GetFlavorsForForm("GRA7")
	assert.Len(t, flavors, 2)
	assert.Empty(t, def)
}

func TestOVHOptionsManager_GetFlavorsForForm_WithFilter(t *testing.T) {
	m := newTestOVHOptionsManager(t)
	m.mu.Lock()
	m.cachedFlavors = map[string][]ovhFlavor{
		"GRA7": {
			{Name: "b2-7", VCPUs: 2, RAM: 7},
			{Name: "b2-15", VCPUs: 4, RAM: 15},
			{Name: "b2-30", VCPUs: 8, RAM: 30},
		},
	}
	m.mu.Unlock()
	m.SetConfig(OVHOptionsConfig{
		Flavors: map[string]OVHItemConfig{
			"GRA7": {Enabled: []string{"b2-7", "b2-30"}, Default: "b2-7"},
		},
	})

	flavors, def := m.GetFlavorsForForm("GRA7")
	assert.Len(t, flavors, 2)
	assert.Equal(t, "b2-7", def)
	assert.Equal(t, "b2-7", flavors[0].Name, "default flavor should be moved to front")
}

func TestOVHOptionsManager_SaveConfig_NoDataDir(t *testing.T) {
	m := &OVHOptionsManager{
		config:  OVHOptionsConfig{Flavors: make(map[string]OVHItemConfig)},
		dataDir: "",
	}
	err := m.SaveConfig()
	assert.NoError(t, err)
}

func TestOVHOptionsManager_LoadConfig_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "ovh-options.json"), []byte("not-json"), 0644)
	require.NoError(t, err)

	cm := NewCredentialsManager()
	m := &OVHOptionsManager{
		dataDir:  dir,
		config:   OVHOptionsConfig{Flavors: make(map[string]OVHItemConfig)},
		credsMgr: cm,
	}
	err = m.LoadConfig()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse")
}
