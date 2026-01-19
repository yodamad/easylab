package providers

import (
	"fmt"
	"sync"
)

var (
	providers = make(map[string]Provider)
	mu        sync.RWMutex
)

// Register registers a provider implementation
func Register(provider Provider) error {
	if provider == nil {
		return fmt.Errorf("cannot register nil provider")
	}

	name := provider.Name()
	if name == "" {
		return fmt.Errorf("provider name cannot be empty")
	}

	mu.Lock()
	defer mu.Unlock()

	if _, exists := providers[name]; exists {
		return fmt.Errorf("provider %s is already registered", name)
	}

	providers[name] = provider
	return nil
}

// Get retrieves a provider by name
func Get(name string) (Provider, error) {
	mu.RLock()
	defer mu.RUnlock()

	provider, exists := providers[name]
	if !exists {
		return nil, fmt.Errorf("provider %s not found", name)
	}

	return provider, nil
}

// List returns all registered provider names
func List() []string {
	mu.RLock()
	defer mu.RUnlock()

	names := make([]string, 0, len(providers))
	for name := range providers {
		names = append(names, name)
	}
	return names
}

// IsRegistered checks if a provider is registered
func IsRegistered(name string) bool {
	mu.RLock()
	defer mu.RUnlock()

	_, exists := providers[name]
	return exists
}
