package server

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/ovh/go-ovh/ovh"
)

// OVHItemConfig holds the enabled items and default for a category (regions or flavors per region).
type OVHItemConfig struct {
	Enabled []string `json:"enabled"`
	Default string   `json:"default"`
}

// OVHOptionsConfig is the persisted admin preferences for regions and flavors.
type OVHOptionsConfig struct {
	Regions OVHItemConfig            `json:"regions"`
	Flavors map[string]OVHItemConfig `json:"flavors"`
}

// OVHOptionsManager caches OVH regions/flavors in memory and applies admin filtering preferences.
type OVHOptionsManager struct {
	cachedRegions map[string]bool
	cachedFlavors map[string][]ovhFlavor // region -> flavors
	config        OVHOptionsConfig
	dataDir       string
	credsMgr      *CredentialsManager
	mu            sync.RWMutex
}

// NewOVHOptionsManager creates a new manager and loads persisted config from disk.
func NewOVHOptionsManager(dataDir string, credsMgr *CredentialsManager) *OVHOptionsManager {
	m := &OVHOptionsManager{
		cachedRegions: make(map[string]bool),
		cachedFlavors: make(map[string][]ovhFlavor),
		config: OVHOptionsConfig{
			Flavors: make(map[string]OVHItemConfig),
		},
		dataDir:  dataDir,
		credsMgr: credsMgr,
	}
	if err := m.LoadConfig(); err != nil {
		log.Printf("[OVH-OPTIONS] Warning: failed to load config: %v", err)
	}
	return m
}

func (m *OVHOptionsManager) configPath() string {
	return filepath.Join(m.dataDir, "ovh-options.json")
}

// LoadConfig reads the admin preferences from disk. Missing file is not an error.
func (m *OVHOptionsManager) LoadConfig() error {
	if m.dataDir == "" {
		return nil
	}
	data, err := os.ReadFile(m.configPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read ovh-options config: %w", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := json.Unmarshal(data, &m.config); err != nil {
		return fmt.Errorf("failed to parse ovh-options config: %w", err)
	}
	if m.config.Flavors == nil {
		m.config.Flavors = make(map[string]OVHItemConfig)
	}
	return nil
}

// SaveConfig persists the admin preferences to disk.
func (m *OVHOptionsManager) SaveConfig() error {
	if m.dataDir == "" {
		return nil
	}
	m.mu.RLock()
	data, err := json.MarshalIndent(m.config, "", "  ")
	m.mu.RUnlock()
	if err != nil {
		return fmt.Errorf("failed to marshal ovh-options config: %w", err)
	}
	if err := os.MkdirAll(m.dataDir, 0755); err != nil {
		return fmt.Errorf("failed to create data dir: %w", err)
	}
	if err := os.WriteFile(m.configPath(), data, 0644); err != nil {
		return fmt.Errorf("failed to write ovh-options config: %w", err)
	}
	return nil
}

// SetConfig replaces the admin preferences (called from the admin save handler).
func (m *OVHOptionsManager) SetConfig(cfg OVHOptionsConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cfg.Flavors == nil {
		cfg.Flavors = make(map[string]OVHItemConfig)
	}
	m.config = cfg
}

// GetConfig returns a copy of the current config.
func (m *OVHOptionsManager) GetConfig() OVHOptionsConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cfg := OVHOptionsConfig{
		Regions: OVHItemConfig{
			Enabled: append([]string(nil), m.config.Regions.Enabled...),
			Default: m.config.Regions.Default,
		},
		Flavors: make(map[string]OVHItemConfig, len(m.config.Flavors)),
	}
	for k, v := range m.config.Flavors {
		cfg.Flavors[k] = OVHItemConfig{
			Enabled: append([]string(nil), v.Enabled...),
			Default: v.Default,
		}
	}
	return cfg
}

// RefreshFromAPI fetches regions and flavors from the OVH API and populates the cache.
func (m *OVHOptionsManager) RefreshFromAPI() error {
	creds, err := m.credsMgr.GetOVHCredentials()
	if err != nil {
		return fmt.Errorf("OVH credentials not configured: %w", err)
	}

	client, err := ovh.NewClient(creds.Endpoint, creds.ApplicationKey, creds.ApplicationSecret, creds.ConsumerKey)
	if err != nil {
		return fmt.Errorf("failed to create OVH client: %w", err)
	}

	var regions []string
	endpoint := fmt.Sprintf("/cloud/project/%s/capabilities/kube/regions", creds.ServiceName)
	if err := client.Get(endpoint, &regions); err != nil {
		return fmt.Errorf("failed to fetch OVH regions: %w", err)
	}

	flavorsMap := make(map[string][]ovhFlavor, len(regions))
	for _, region := range regions {
		var flavors []ovhFlavor
		ep := fmt.Sprintf("/cloud/project/%s/capabilities/kube/flavors?region=%s", creds.ServiceName, region)
		if err := client.Get(ep, &flavors); err != nil {
			log.Printf("[OVH-OPTIONS] Warning: failed to fetch flavors for region %s: %v", region, err)
			continue
		}
		sort.Slice(flavors, func(i, j int) bool { return flavors[i].Name < flavors[j].Name })
		flavorsMap[region] = flavors
	}

	regionsSet := make(map[string]bool, len(regions))
	for _, r := range regions {
		regionsSet[r] = true
	}

	m.mu.Lock()
	m.cachedRegions = regionsSet
	m.cachedFlavors = flavorsMap
	m.mu.Unlock()

	log.Printf("[OVH-OPTIONS] Cache refreshed: %d regions loaded", len(regions))
	return nil
}

// HasCache returns true if at least one region is cached.
func (m *OVHOptionsManager) HasCache() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.cachedRegions) > 0
}

// GetCachedRegions returns the full list of cached region names, sorted.
func (m *OVHOptionsManager) GetCachedRegions() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	regions := make([]string, 0, len(m.cachedRegions))
	for r := range m.cachedRegions {
		regions = append(regions, r)
	}
	sort.Strings(regions)
	return regions
}

// GetCachedFlavors returns the cached flavors for a given region.
func (m *OVHOptionsManager) GetCachedFlavors(region string) []ovhFlavor {
	m.mu.RLock()
	defer m.mu.RUnlock()
	flavors := m.cachedFlavors[region]
	out := make([]ovhFlavor, len(flavors))
	copy(out, flavors)
	return out
}

// GetRegionsForForm returns the filtered/sorted region list with the default first.
// If the admin config has enabled regions, only those are shown; otherwise all cached regions.
func (m *OVHOptionsManager) GetRegionsForForm() (regions []string, defaultRegion string) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	allRegions := make([]string, 0, len(m.cachedRegions))
	for r := range m.cachedRegions {
		allRegions = append(allRegions, r)
	}
	sort.Strings(allRegions)

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

// GetFlavorsForForm returns the filtered/sorted flavor list for a region with the default first.
func (m *OVHOptionsManager) GetFlavorsForForm(region string) (flavors []ovhFlavor, defaultFlavor string) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	cached, ok := m.cachedFlavors[region]
	if !ok {
		return nil, ""
	}
	result := make([]ovhFlavor, len(cached))
	copy(result, cached)

	if flavorCfg, ok := m.config.Flavors[region]; ok && len(flavorCfg.Enabled) > 0 {
		enabledSet := toSet(flavorCfg.Enabled)
		var filtered []ovhFlavor
		for _, f := range result {
			if enabledSet[f.Name] {
				filtered = append(filtered, f)
			}
		}
		result = filtered
		defaultFlavor = flavorCfg.Default
	} else {
		for _, fc := range m.config.Flavors {
			if fc.Default != "" {
				defaultFlavor = fc.Default
				break
			}
		}
	}

	if cfg, ok := m.config.Flavors[region]; ok {
		defaultFlavor = cfg.Default
	}

	if defaultFlavor != "" {
		for i, f := range result {
			if f.Name == defaultFlavor {
				result[0], result[i] = result[i], result[0]
				break
			}
		}
	}

	return result, defaultFlavor
}

func toSet(items []string) map[string]bool {
	s := make(map[string]bool, len(items))
	for _, item := range items {
		s[item] = true
	}
	return s
}

func moveToFront(items []string, target string) []string {
	for i, item := range items {
		if item == target {
			items[0], items[i] = items[i], items[0]
			break
		}
	}
	return items
}
