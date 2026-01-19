package server

import (
	"fmt"
	"log"
	"os"
	"sync"
)

// OVHCredentials holds OVH API credentials
type OVHCredentials struct {
	ApplicationKey    string `json:"application_key"`
	ApplicationSecret string `json:"application_secret"`
	ConsumerKey       string `json:"consumer_key"`
	ServiceName       string `json:"service_name"`
	Endpoint          string `json:"endpoint"`
}

// ProviderCredentials is a generic interface for provider credentials
// For now, we keep OVHCredentials as the concrete type, but this allows for future expansion
type ProviderCredentials interface {
	GetProviderName() string
}

// GetProviderName returns the provider name for OVH credentials
func (c *OVHCredentials) GetProviderName() string {
	return "ovh"
}

// loadCredentialsFromEnv attempts to load OVH credentials from environment variables
func loadCredentialsFromEnv() *OVHCredentials {
	appKey := os.Getenv("OVH_APPLICATION_KEY")
	appSecret := os.Getenv("OVH_APPLICATION_SECRET")
	consumerKey := os.Getenv("OVH_CONSUMER_KEY")
	serviceName := os.Getenv("OVH_SERVICE_NAME")
	endpoint := os.Getenv("OVH_ENDPOINT")

	// Check if all required environment variables are present
	if appKey == "" || appSecret == "" || consumerKey == "" || serviceName == "" || endpoint == "" {
		return nil
	}

	return &OVHCredentials{
		ApplicationKey:    appKey,
		ApplicationSecret: appSecret,
		ConsumerKey:       consumerKey,
		ServiceName:       serviceName,
		Endpoint:          endpoint,
	}
}

// CredentialsManager manages credentials for multiple providers in memory
type CredentialsManager struct {
	credentials map[string]ProviderCredentials // key is provider name
	mu          sync.RWMutex
}

// NewCredentialsManager creates a new credentials manager and loads credentials from environment variables
func NewCredentialsManager() *CredentialsManager {
	cm := &CredentialsManager{
		credentials: make(map[string]ProviderCredentials),
	}

	// Try to load OVH credentials from environment variables
	if creds := loadCredentialsFromEnv(); creds != nil {
		if err := cm.SetCredentials(creds); err != nil {
			log.Printf("Warning: Failed to load OVH credentials from environment variables: %v", err)
		} else {
			log.Printf("[STARTUP] OVH credentials loaded from environment variables")
		}
	}

	return cm
}

// SetCredentials stores provider credentials in memory
func (cm *CredentialsManager) SetCredentials(creds ProviderCredentials) error {
	ovhCreds, ok := creds.(*OVHCredentials)
	if !ok {
		return fmt.Errorf("unsupported credentials type")
	}

	if ovhCreds.ApplicationKey == "" || ovhCreds.ApplicationSecret == "" ||
		ovhCreds.ConsumerKey == "" || ovhCreds.ServiceName == "" || ovhCreds.Endpoint == "" {
		return fmt.Errorf("all OVH credentials are required")
	}

	cm.mu.Lock()
	defer cm.mu.Unlock()

	providerName := creds.GetProviderName()
	cm.credentials[providerName] = creds
	return nil
}

// GetCredentials retrieves credentials for a specific provider
func (cm *CredentialsManager) GetCredentials(providerName string) (ProviderCredentials, error) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	if providerName == "" {
		providerName = "ovh" // Default to OVH for backward compatibility
	}

	creds, exists := cm.credentials[providerName]
	if !exists {
		return nil, fmt.Errorf("%s credentials not configured", providerName)
	}

	// Return OVH credentials as a copy to prevent external modification
	if ovhCreds, ok := creds.(*OVHCredentials); ok {
		return &OVHCredentials{
			ApplicationKey:    ovhCreds.ApplicationKey,
			ApplicationSecret: ovhCreds.ApplicationSecret,
			ConsumerKey:       ovhCreds.ConsumerKey,
			ServiceName:       ovhCreds.ServiceName,
			Endpoint:          ovhCreds.Endpoint,
		}, nil
	}

	return creds, nil
}

// GetOVHCredentials retrieves OVH credentials (backward compatibility method)
func (cm *CredentialsManager) GetOVHCredentials() (*OVHCredentials, error) {
	creds, err := cm.GetCredentials("ovh")
	if err != nil {
		return nil, err
	}
	return creds.(*OVHCredentials), nil
}

// HasCredentials checks if credentials are configured for a provider
func (cm *CredentialsManager) HasCredentials(providerName string) bool {
	if providerName == "" {
		providerName = "ovh" // Default to OVH for backward compatibility
	}
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	_, exists := cm.credentials[providerName]
	return exists
}

// ClearCredentials clears stored credentials for a provider (for testing/debugging)
func (cm *CredentialsManager) ClearCredentials(providerName string) {
	if providerName == "" {
		providerName = "ovh" // Default to OVH for backward compatibility
	}
	cm.mu.Lock()
	defer cm.mu.Unlock()
	delete(cm.credentials, providerName)
}

