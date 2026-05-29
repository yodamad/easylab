package server

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// AzureItemConfig holds enabled items and a default for a category (regions or VM sizes per region).
type AzureItemConfig struct {
	Enabled []string `json:"enabled"`
	Default string   `json:"default"`
}

// AzureADConfig holds Azure AD OAuth configuration for student and admin login.
type AzureADConfig struct {
	ClientID            string `json:"client_id"`
	ClientSecret        string `json:"client_secret"`
	TenantID            string `json:"tenant_id"`
	DisableClassicLogin bool   `json:"disable_classic_login,omitempty"`
	// AdminGroupID, when set, enables Azure AD login for admins restricted to direct members of this group.
	AdminGroupID string `json:"admin_group_id,omitempty"`
	// DisableClassicAdminLogin hides the password form on the admin login page (only when AdminGroupID is set).
	DisableClassicAdminLogin bool `json:"disable_classic_admin_login,omitempty"`
}

// AzureOptionsConfig is persisted admin preferences for Azure regions and VM sizes.
type AzureOptionsConfig struct {
	Regions  AzureItemConfig            `json:"regions"`
	VMSizes  map[string]AzureItemConfig `json:"vm_sizes"`
	AzureAD  AzureADConfig              `json:"azure_ad,omitempty"`
}

// AzureOptionsManager caches Azure locations and VM sizes and applies admin filtering.
type AzureOptionsManager struct {
	cachedRegionOrder   []string
	cachedRegionDisplay map[string]string
	cachedVMSizes       map[string][]azureVMSize
	config              AzureOptionsConfig
	dataDir             string
	credsMgr            *CredentialsManager
	mu                  sync.RWMutex
}

type azureVMSize struct {
	Name   string
	VCPUs  int
	RAMGB  int
}

// NewAzureOptionsManager creates a manager and loads persisted config from disk.
func NewAzureOptionsManager(dataDir string, credsMgr *CredentialsManager) *AzureOptionsManager {
	m := &AzureOptionsManager{
		cachedRegionDisplay: make(map[string]string),
		cachedVMSizes:     make(map[string][]azureVMSize),
		config: AzureOptionsConfig{
			VMSizes: make(map[string]AzureItemConfig),
		},
		dataDir:  dataDir,
		credsMgr: credsMgr,
	}
	if err := m.LoadConfig(); err != nil {
		log.Printf("[AZURE-OPTIONS] Warning: failed to load config: %v", err)
	}
	return m
}

func (m *AzureOptionsManager) configPath() string {
	return filepath.Join(m.dataDir, "azure-options.json")
}

// LoadConfig reads admin preferences from disk. Missing file is not an error.
func (m *AzureOptionsManager) LoadConfig() error {
	if m.dataDir == "" {
		return nil
	}
	data, err := os.ReadFile(m.configPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read azure-options config: %w", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := json.Unmarshal(data, &m.config); err != nil {
		return fmt.Errorf("failed to parse azure-options config: %w", err)
	}
	if m.config.VMSizes == nil {
		m.config.VMSizes = make(map[string]AzureItemConfig)
	}
	return nil
}

// SaveConfig persists admin preferences to disk.
func (m *AzureOptionsManager) SaveConfig() error {
	if m.dataDir == "" {
		return nil
	}
	m.mu.RLock()
	data, err := json.MarshalIndent(m.config, "", "  ")
	m.mu.RUnlock()
	if err != nil {
		return fmt.Errorf("failed to marshal azure-options config: %w", err)
	}
	if err := os.MkdirAll(m.dataDir, 0755); err != nil {
		return fmt.Errorf("failed to create data dir: %w", err)
	}
	if err := os.WriteFile(m.configPath(), data, 0644); err != nil {
		return fmt.Errorf("failed to write azure-options config: %w", err)
	}
	return nil
}

// GetAzureADConfig returns the persisted Azure AD OAuth configuration.
func (m *AzureOptionsManager) GetAzureADConfig() AzureADConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.config.AzureAD
}

// SetAzureADConfig updates the Azure AD OAuth configuration and persists it.
func (m *AzureOptionsManager) SetAzureADConfig(cfg AzureADConfig) error {
	m.mu.Lock()
	m.config.AzureAD = cfg
	m.mu.Unlock()
	return m.SaveConfig()
}

// SetConfig replaces admin preferences (from the admin save handler).
func (m *AzureOptionsManager) SetConfig(cfg AzureOptionsConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cfg.VMSizes == nil {
		cfg.VMSizes = make(map[string]AzureItemConfig)
	}
	m.config = cfg
}

// GetConfig returns a copy of the current config.
func (m *AzureOptionsManager) GetConfig() AzureOptionsConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cfg := AzureOptionsConfig{
		Regions: AzureItemConfig{
			Enabled: append([]string(nil), m.config.Regions.Enabled...),
			Default: m.config.Regions.Default,
		},
		VMSizes: make(map[string]AzureItemConfig, len(m.config.VMSizes)),
	}
	for k, v := range m.config.VMSizes {
		cfg.VMSizes[k] = AzureItemConfig{
			Enabled: append([]string(nil), v.Enabled...),
			Default: v.Default,
		}
	}
	return cfg
}

// RefreshFromAPI loads subscription locations into the cache (VM size cache is cleared).
func (m *AzureOptionsManager) RefreshFromAPI() error {
	creds, err := m.credsMgr.GetAzureCredentials()
	if err != nil {
		return fmt.Errorf("Azure credentials not configured: %w", err)
	}
	token, err := azureAcquireToken(creds.TenantID, creds.ClientID, creds.ClientSecret)
	if err != nil {
		return fmt.Errorf("failed to acquire Azure token: %w", err)
	}
	locations, displays, err := azureListSubscriptionLocations(token, creds.SubscriptionID)
	if err != nil {
		return fmt.Errorf("failed to list Azure locations: %w", err)
	}

	sort.Strings(locations)

	m.mu.Lock()
	m.cachedRegionOrder = locations
	m.cachedRegionDisplay = displays
	m.cachedVMSizes = make(map[string][]azureVMSize)
	m.mu.Unlock()

	log.Printf("[AZURE-OPTIONS] Cache refreshed: %d regions loaded", len(locations))
	return nil
}

// HasCache returns true if at least one region is cached.
func (m *AzureOptionsManager) HasCache() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.cachedRegionOrder) > 0
}

// GetCachedRegions returns sorted region names.
func (m *AzureOptionsManager) GetCachedRegions() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := append([]string(nil), m.cachedRegionOrder...)
	return out
}

// RegionDisplayName returns the Azure display label for a location name, if known.
func (m *AzureOptionsManager) RegionDisplayName(name string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if d := m.cachedRegionDisplay[name]; d != "" {
		return d
	}
	return name
}

// EnsureVMSizesCached fetches VM sizes for a region if not already cached (thread-safe).
func (m *AzureOptionsManager) EnsureVMSizesCached(region string) error {
	m.mu.Lock()
	if _, ok := m.cachedVMSizes[region]; ok {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()

	creds, err := m.credsMgr.GetAzureCredentials()
	if err != nil {
		return fmt.Errorf("Azure credentials not configured: %w", err)
	}
	token, err := azureAcquireToken(creds.TenantID, creds.ClientID, creds.ClientSecret)
	if err != nil {
		return fmt.Errorf("failed to acquire Azure token: %w", err)
	}
	sizes, err := azureListVMSizes(token, creds.SubscriptionID, region)
	if err != nil {
		return fmt.Errorf("failed to list VM sizes for %s: %w", region, err)
	}

	m.mu.Lock()
	m.cachedVMSizes[region] = sizes
	m.mu.Unlock()
	return nil
}

// GetCachedVMSizes returns cached VM sizes for a region (may be empty until EnsureVMSizesCached).
func (m *AzureOptionsManager) GetCachedVMSizes(region string) []azureVMSize {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s := m.cachedVMSizes[region]
	out := make([]azureVMSize, len(s))
	copy(out, s)
	return out
}

// GetRegionsForForm returns filtered region names with default first (mirrors OVH behavior).
func (m *AzureOptionsManager) GetRegionsForForm() (regions []string, defaultRegion string) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	allRegions := append([]string(nil), m.cachedRegionOrder...)
	if len(m.config.Regions.Enabled) > 0 {
		enabledSet := toSet(m.config.Regions.Enabled)
		var filtered []string
		for _, r := range allRegions {
			if enabledSet[r] {
				filtered = append(filtered, r)
			}
		}
		allRegions = filtered
	}

	defaultRegion = m.config.Regions.Default
	if defaultRegion != "" {
		allRegions = moveToFront(allRegions, defaultRegion)
	}

	return allRegions, defaultRegion
}

// GetVMSizesForForm returns VM sizes for a region with filtering from admin prefs.
func (m *AzureOptionsManager) GetVMSizesForForm(region string) (sizes []azureVMSize, defaultSize string) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	cached, ok := m.cachedVMSizes[region]
	if !ok {
		return nil, ""
	}
	result := make([]azureVMSize, len(cached))
	copy(result, cached)

	if vmCfg, ok := m.config.VMSizes[region]; ok && len(vmCfg.Enabled) > 0 {
		enabledSet := toSet(vmCfg.Enabled)
		var filtered []azureVMSize
		for _, s := range result {
			if enabledSet[s.Name] {
				filtered = append(filtered, s)
			}
		}
		result = filtered
		defaultSize = vmCfg.Default
	} else {
		if cfg, ok := m.config.VMSizes[region]; ok {
			defaultSize = cfg.Default
		}
	}

	if defaultSize != "" {
		for i, s := range result {
			if s.Name == defaultSize {
				result[0], result[i] = result[i], result[0]
				break
			}
		}
	}

	return result, defaultSize
}
