package workspace

import (
	"fmt"
	"sync"
)

// DefaultBackend is the backend name used when a caller does not specify one.
const DefaultBackend = "kube"

// Factory builds a Backend from a lab's kubeconfig and the namespace student
// workspaces should live in.
type Factory func(kubeconfig, namespace string) (Backend, error)

var (
	factoriesMu sync.RWMutex
	factories   = make(map[string]Factory)
)

// Register registers a workspace backend factory under a name.
func Register(name string, f Factory) error {
	if name == "" {
		return fmt.Errorf("workspace backend name cannot be empty")
	}
	if f == nil {
		return fmt.Errorf("workspace backend factory cannot be nil")
	}
	factoriesMu.Lock()
	defer factoriesMu.Unlock()
	if _, exists := factories[name]; exists {
		return fmt.Errorf("workspace backend %s is already registered", name)
	}
	factories[name] = f
	return nil
}

// New builds a backend by name. An empty name selects DefaultBackend.
func New(name, kubeconfig, namespace string) (Backend, error) {
	if name == "" {
		name = DefaultBackend
	}
	factoriesMu.RLock()
	f, exists := factories[name]
	factoriesMu.RUnlock()
	if !exists {
		return nil, fmt.Errorf("workspace backend %q not found", name)
	}
	return f(kubeconfig, namespace)
}

// Default builds the default backend from the given kubeconfig and namespace.
func Default(kubeconfig, namespace string) (Backend, error) {
	return New(DefaultBackend, kubeconfig, namespace)
}

// List returns the names of all registered workspace backends.
func List() []string {
	factoriesMu.RLock()
	defer factoriesMu.RUnlock()
	names := make([]string, 0, len(factories))
	for name := range factories {
		names = append(names, name)
	}
	return names
}
