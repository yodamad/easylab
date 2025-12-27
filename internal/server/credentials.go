package server

import (
	"fmt"
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

// CredentialsManager manages OVH credentials in memory
type CredentialsManager struct {
	credentials *OVHCredentials
	mu          sync.RWMutex
}

// NewCredentialsManager creates a new credentials manager
func NewCredentialsManager() *CredentialsManager {
	return &CredentialsManager{
		credentials: nil,
	}
}

// SetCredentials stores OVH credentials in memory
func (cm *CredentialsManager) SetCredentials(creds *OVHCredentials) error {
	if creds.ApplicationKey == "" || creds.ApplicationSecret == "" ||
		creds.ConsumerKey == "" || creds.ServiceName == "" || creds.Endpoint == "" {
		return fmt.Errorf("all OVH credentials are required")
	}

	cm.mu.Lock()
	defer cm.mu.Unlock()

	cm.credentials = creds
	return nil
}

// GetCredentials retrieves OVH credentials from memory
func (cm *CredentialsManager) GetCredentials() (*OVHCredentials, error) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	if cm.credentials == nil {
		return nil, fmt.Errorf("OVH credentials not configured")
	}

	// Return a copy to prevent external modification
	return &OVHCredentials{
		ApplicationKey:    cm.credentials.ApplicationKey,
		ApplicationSecret: cm.credentials.ApplicationSecret,
		ConsumerKey:       cm.credentials.ConsumerKey,
		ServiceName:       cm.credentials.ServiceName,
		Endpoint:          cm.credentials.Endpoint,
	}, nil
}

// HasCredentials checks if credentials are configured
func (cm *CredentialsManager) HasCredentials() bool {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.credentials != nil
}

// ClearCredentials clears stored credentials (for testing/debugging)
func (cm *CredentialsManager) ClearCredentials() {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.credentials = nil
}

