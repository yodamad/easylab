package dns

import (
	"fmt"
	"sync"
)

var (
	providers = make(map[string]Provider)
	mu        sync.RWMutex
)

// Register registers a DNS provider implementation.
func Register(p Provider) error {
	if p == nil {
		return fmt.Errorf("cannot register nil DNS provider")
	}
	name := p.Name()
	if name == "" {
		return fmt.Errorf("DNS provider name cannot be empty")
	}
	mu.Lock()
	defer mu.Unlock()
	if _, exists := providers[name]; exists {
		return fmt.Errorf("DNS provider %s is already registered", name)
	}
	providers[name] = p
	return nil
}

// Get retrieves a DNS provider by name. Returns nil, nil if name is empty.
func Get(name string) (Provider, error) {
	if name == "" {
		return nil, nil
	}
	mu.RLock()
	defer mu.RUnlock()
	p, exists := providers[name]
	if !exists {
		return nil, fmt.Errorf("DNS provider %q not found", name)
	}
	return p, nil
}

// List returns all registered DNS provider names.
func List() []string {
	mu.RLock()
	defer mu.RUnlock()
	names := make([]string, 0, len(providers))
	for name := range providers {
		names = append(names, name)
	}
	return names
}
