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

// AzureCredentials holds Azure Service Principal credentials
type AzureCredentials struct {
	ClientID       string `json:"client_id"`
	ClientSecret   string `json:"client_secret"`
	TenantID       string `json:"tenant_id"`
	SubscriptionID string `json:"subscription_id"`
}

// ProviderCredentials is a generic interface for provider credentials
type ProviderCredentials interface {
	GetProviderName() string
}

// GetProviderName returns the provider name for OVH credentials
func (c *OVHCredentials) GetProviderName() string {
	return "ovh"
}

// GetProviderName returns the provider name for Azure credentials
func (c *AzureCredentials) GetProviderName() string {
	return "azure"
}

// loadOVHCredentialsFromEnv attempts to load OVH credentials from environment variables.
func loadOVHCredentialsFromEnv() *OVHCredentials {
	appKey := os.Getenv("OVH_APPLICATION_KEY")
	appSecret := os.Getenv("OVH_APPLICATION_SECRET")
	consumerKey := os.Getenv("OVH_CONSUMER_KEY")
	serviceName := os.Getenv("OVH_SERVICE_NAME")
	endpoint := os.Getenv("OVH_ENDPOINT")

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

// loadAzureCredentialsFromEnv attempts to load Azure credentials from environment variables.
func loadAzureCredentialsFromEnv() *AzureCredentials {
	clientID := os.Getenv("AZURE_CLIENT_ID")
	clientSecret := os.Getenv("AZURE_CLIENT_SECRET")
	tenantID := os.Getenv("AZURE_TENANT_ID")
	subscriptionID := os.Getenv("AZURE_SUBSCRIPTION_ID")

	if clientID == "" || clientSecret == "" || tenantID == "" || subscriptionID == "" {
		return nil
	}

	return &AzureCredentials{
		ClientID:       clientID,
		ClientSecret:   clientSecret,
		TenantID:       tenantID,
		SubscriptionID: subscriptionID,
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

	if creds := loadOVHCredentialsFromEnv(); creds != nil {
		if err := cm.SetCredentials(creds); err != nil {
			log.Printf("Warning: Failed to load OVH credentials from environment variables: %v", err)
		} else {
			log.Printf("[STARTUP] OVH credentials loaded from environment variables")
		}
	}

	if creds := loadAzureCredentialsFromEnv(); creds != nil {
		if err := cm.SetCredentials(creds); err != nil {
			log.Printf("Warning: Failed to load Azure credentials from environment variables: %v", err)
		} else {
			log.Printf("[STARTUP] Azure credentials loaded from environment variables")
		}
	}

	return cm
}

// SetCredentials stores provider credentials in memory
func (cm *CredentialsManager) SetCredentials(creds ProviderCredentials) error {
	switch c := creds.(type) {
	case *OVHCredentials:
		if c.ApplicationKey == "" || c.ApplicationSecret == "" ||
			c.ConsumerKey == "" || c.ServiceName == "" || c.Endpoint == "" {
			return fmt.Errorf("all OVH credentials are required")
		}
	case *AzureCredentials:
		if c.ClientID == "" || c.ClientSecret == "" || c.TenantID == "" || c.SubscriptionID == "" {
			return fmt.Errorf("all Azure credentials are required (client_id, client_secret, tenant_id, subscription_id)")
		}
	default:
		return fmt.Errorf("unsupported credentials type")
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

	switch c := creds.(type) {
	case *OVHCredentials:
		return &OVHCredentials{
			ApplicationKey:    c.ApplicationKey,
			ApplicationSecret: c.ApplicationSecret,
			ConsumerKey:       c.ConsumerKey,
			ServiceName:       c.ServiceName,
			Endpoint:          c.Endpoint,
		}, nil
	case *AzureCredentials:
		return &AzureCredentials{
			ClientID:       c.ClientID,
			ClientSecret:   c.ClientSecret,
			TenantID:       c.TenantID,
			SubscriptionID: c.SubscriptionID,
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

// GetAzureCredentials retrieves Azure service principal credentials.
func (cm *CredentialsManager) GetAzureCredentials() (*AzureCredentials, error) {
	creds, err := cm.GetCredentials("azure")
	if err != nil {
		return nil, err
	}
	az, ok := creds.(*AzureCredentials)
	if !ok {
		return nil, fmt.Errorf("invalid credentials type for azure")
	}
	return az, nil
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
