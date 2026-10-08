package server

import (
	"os"
	"testing"
)

func TestOVHCredentials_Fields(t *testing.T) {
	creds := &OVHCredentials{
		ApplicationKey:    "test-key",
		ApplicationSecret: "test-secret",
		ConsumerKey:       "test-consumer",
		ServiceName:       "test-service",
		Endpoint:          "ovh-eu",
	}

	if creds.ApplicationKey != "test-key" {
		t.Errorf("ApplicationKey = %s, want test-key", creds.ApplicationKey)
	}
	if creds.ApplicationSecret != "test-secret" {
		t.Errorf("ApplicationSecret = %s, want test-secret", creds.ApplicationSecret)
	}
	if creds.ConsumerKey != "test-consumer" {
		t.Errorf("ConsumerKey = %s, want test-consumer", creds.ConsumerKey)
	}
	if creds.ServiceName != "test-service" {
		t.Errorf("ServiceName = %s, want test-service", creds.ServiceName)
	}
	if creds.Endpoint != "ovh-eu" {
		t.Errorf("Endpoint = %s, want ovh-eu", creds.Endpoint)
	}
}

func TestCredentialsManager_SetAndGetCredentials(t *testing.T) {
	cm := NewCredentialsManager()

	creds := &OVHCredentials{
		ApplicationKey:    "test-key",
		ApplicationSecret: "test-secret",
		ConsumerKey:       "test-consumer",
		ServiceName:       "test-service",
		Endpoint:          "ovh-eu",
	}

	err := cm.SetCredentials(creds)
	if err != nil {
		t.Fatalf("SetCredentials() error = %v", err)
	}

	got, err := cm.GetCredentials("ovh")
	if err != nil {
		t.Fatalf("GetCredentials() error = %v", err)
	}

	ovhGot, ok := got.(*OVHCredentials)
	if !ok {
		t.Fatalf("GetCredentials() returned wrong type")
	}

	if ovhGot.ApplicationKey != creds.ApplicationKey {
		t.Errorf("ApplicationKey = %s, want %s", ovhGot.ApplicationKey, creds.ApplicationKey)
	}
	if ovhGot.ApplicationSecret != creds.ApplicationSecret {
		t.Errorf("ApplicationSecret = %s, want %s", ovhGot.ApplicationSecret, creds.ApplicationSecret)
	}
	if ovhGot.ConsumerKey != creds.ConsumerKey {
		t.Errorf("ConsumerKey = %s, want %s", ovhGot.ConsumerKey, creds.ConsumerKey)
	}
	if ovhGot.ServiceName != creds.ServiceName {
		t.Errorf("ServiceName = %s, want %s", ovhGot.ServiceName, creds.ServiceName)
	}
	if ovhGot.Endpoint != creds.Endpoint {
		t.Errorf("Endpoint = %s, want %s", ovhGot.Endpoint, creds.Endpoint)
	}
}

func TestCredentialsManager_SetCredentials_Validation(t *testing.T) {
	tests := []struct {
		name    string
		creds   *OVHCredentials
		wantErr bool
	}{
		{
			name: "valid credentials",
			creds: &OVHCredentials{
				ApplicationKey:    "key",
				ApplicationSecret: "secret",
				ConsumerKey:       "consumer",
				ServiceName:       "service",
				Endpoint:          "endpoint",
			},
			wantErr: false,
		},
		{
			name: "missing ApplicationKey",
			creds: &OVHCredentials{
				ApplicationKey:    "",
				ApplicationSecret: "secret",
				ConsumerKey:       "consumer",
				ServiceName:       "service",
				Endpoint:          "endpoint",
			},
			wantErr: true,
		},
		{
			name: "missing ApplicationSecret",
			creds: &OVHCredentials{
				ApplicationKey:    "key",
				ApplicationSecret: "",
				ConsumerKey:       "consumer",
				ServiceName:       "service",
				Endpoint:          "endpoint",
			},
			wantErr: true,
		},
		{
			name: "missing ConsumerKey",
			creds: &OVHCredentials{
				ApplicationKey:    "key",
				ApplicationSecret: "secret",
				ConsumerKey:       "",
				ServiceName:       "service",
				Endpoint:          "endpoint",
			},
			wantErr: true,
		},
		{
			name: "missing ServiceName",
			creds: &OVHCredentials{
				ApplicationKey:    "key",
				ApplicationSecret: "secret",
				ConsumerKey:       "consumer",
				ServiceName:       "",
				Endpoint:          "endpoint",
			},
			wantErr: true,
		},
		{
			name: "missing Endpoint",
			creds: &OVHCredentials{
				ApplicationKey:    "key",
				ApplicationSecret: "secret",
				ConsumerKey:       "consumer",
				ServiceName:       "service",
				Endpoint:          "",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cm := NewCredentialsManager()
			err := cm.SetCredentials(tt.creds)
			if (err != nil) != tt.wantErr {
				t.Errorf("SetCredentials() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestCredentialsManager_GetCredentials_NotConfigured(t *testing.T) {
	cm := NewCredentialsManager()

	_, err := cm.GetCredentials("ovh")
	if err == nil {
		t.Error("GetCredentials() expected error for unconfigured credentials")
	}
}

func TestCredentialsManager_HasCredentials(t *testing.T) {
	cm := NewCredentialsManager()

	// Initially no credentials
	if cm.HasCredentials("ovh") {
		t.Error("HasCredentials() = true, want false for new manager")
	}

	// Set credentials
	creds := &OVHCredentials{
		ApplicationKey:    "key",
		ApplicationSecret: "secret",
		ConsumerKey:       "consumer",
		ServiceName:       "service",
		Endpoint:          "endpoint",
	}
	cm.SetCredentials(creds)

	// Now should have credentials
	if !cm.HasCredentials("ovh") {
		t.Error("HasCredentials() = false, want true after setting credentials")
	}
}

func TestCredentialsManager_ClearCredentials(t *testing.T) {
	cm := NewCredentialsManager()

	// Set credentials
	creds := &OVHCredentials{
		ApplicationKey:    "key",
		ApplicationSecret: "secret",
		ConsumerKey:       "consumer",
		ServiceName:       "service",
		Endpoint:          "endpoint",
	}
	cm.SetCredentials(creds)

	// Verify they're set
	if !cm.HasCredentials("ovh") {
		t.Fatal("Credentials should be set")
	}

	// Clear credentials
	cm.ClearCredentials("ovh")

	// Verify they're cleared
	if cm.HasCredentials("ovh") {
		t.Error("HasCredentials() = true after ClearCredentials()")
	}

	_, err := cm.GetCredentials("ovh")
	if err == nil {
		t.Error("GetCredentials() should return error after ClearCredentials()")
	}
}

func TestCredentialsManager_GetCredentialsReturnsCopy(t *testing.T) {
	cm := NewCredentialsManager()

	creds := &OVHCredentials{
		ApplicationKey:    "key",
		ApplicationSecret: "secret",
		ConsumerKey:       "consumer",
		ServiceName:       "service",
		Endpoint:          "endpoint",
	}
	cm.SetCredentials(creds)

	got1, _ := cm.GetCredentials("ovh")
	got2, _ := cm.GetCredentials("ovh")

	ovhGot1, ok1 := got1.(*OVHCredentials)
	ovhGot2, ok2 := got2.(*OVHCredentials)
	if !ok1 || !ok2 {
		t.Fatal("GetCredentials() returned wrong type")
	}

	// Modify got1
	ovhGot1.ApplicationKey = "modified"

	// got2 should still have original value
	if ovhGot2.ApplicationKey != "key" {
		t.Error("GetCredentials() does not return a copy")
	}
}

func TestLoadCredentialsFromEnv(t *testing.T) {
	// Save original env vars
	origAppKey := os.Getenv("OVH_APPLICATION_KEY")
	origAppSecret := os.Getenv("OVH_APPLICATION_SECRET")
	origConsumerKey := os.Getenv("OVH_CONSUMER_KEY")
	origServiceName := os.Getenv("OVH_SERVICE_NAME")
	origEndpoint := os.Getenv("OVH_ENDPOINT")

	// Restore env vars after test
	defer func() {
		os.Setenv("OVH_APPLICATION_KEY", origAppKey)
		os.Setenv("OVH_APPLICATION_SECRET", origAppSecret)
		os.Setenv("OVH_CONSUMER_KEY", origConsumerKey)
		os.Setenv("OVH_SERVICE_NAME", origServiceName)
		os.Setenv("OVH_ENDPOINT", origEndpoint)
	}()

	t.Run("all env vars set", func(t *testing.T) {
		os.Setenv("OVH_APPLICATION_KEY", "env-key")
		os.Setenv("OVH_APPLICATION_SECRET", "env-secret")
		os.Setenv("OVH_CONSUMER_KEY", "env-consumer")
		os.Setenv("OVH_SERVICE_NAME", "env-service")
		os.Setenv("OVH_ENDPOINT", "env-endpoint")

		creds := loadOVHCredentialsFromEnv()
		if creds == nil {
			t.Fatal("loadOVHCredentialsFromEnv() returned nil")
		}

		if creds.ApplicationKey != "env-key" {
			t.Errorf("ApplicationKey = %s, want env-key", creds.ApplicationKey)
		}
		if creds.ApplicationSecret != "env-secret" {
			t.Errorf("ApplicationSecret = %s, want env-secret", creds.ApplicationSecret)
		}
		if creds.ConsumerKey != "env-consumer" {
			t.Errorf("ConsumerKey = %s, want env-consumer", creds.ConsumerKey)
		}
		if creds.ServiceName != "env-service" {
			t.Errorf("ServiceName = %s, want env-service", creds.ServiceName)
		}
		if creds.Endpoint != "env-endpoint" {
			t.Errorf("Endpoint = %s, want env-endpoint", creds.Endpoint)
		}
	})

	t.Run("missing env var", func(t *testing.T) {
		os.Setenv("OVH_APPLICATION_KEY", "env-key")
		os.Setenv("OVH_APPLICATION_SECRET", "env-secret")
		os.Setenv("OVH_CONSUMER_KEY", "env-consumer")
		os.Setenv("OVH_SERVICE_NAME", "env-service")
		os.Unsetenv("OVH_ENDPOINT") // Missing endpoint

		creds := loadOVHCredentialsFromEnv()
		if creds != nil {
			t.Error("loadOVHCredentialsFromEnv() should return nil when env var is missing")
		}
	})
}

func TestCredentialsManager_ConcurrentAccess(t *testing.T) {
	cm := NewCredentialsManager()

	creds := &OVHCredentials{
		ApplicationKey:    "key",
		ApplicationSecret: "secret",
		ConsumerKey:       "consumer",
		ServiceName:       "service",
		Endpoint:          "endpoint",
	}

	done := make(chan bool)

	// Concurrent writes
	go func() {
		for i := 0; i < 100; i++ {
			cm.SetCredentials(creds)
		}
		done <- true
	}()

	// Concurrent reads
	go func() {
		for i := 0; i < 100; i++ {
			cm.GetCredentials("ovh")
			cm.HasCredentials("ovh")
		}
		done <- true
	}()

	// Concurrent clears
	go func() {
		for i := 0; i < 100; i++ {
			cm.ClearCredentials("ovh")
		}
		done <- true
	}()

	// Wait for all goroutines
	<-done
	<-done
	<-done
}

func TestAzureCredentials_GetProviderName(t *testing.T) {
	c := &AzureCredentials{ClientID: "id"}
	if got := c.GetProviderName(); got != "azure" {
		t.Errorf("GetProviderName() = %q, want azure", got)
	}
}

func TestCredentialsManager_GetOVHCredentials_NotConfigured(t *testing.T) {
	cm := NewCredentialsManager()
	_, err := cm.GetOVHCredentials()
	if err == nil {
		t.Error("GetOVHCredentials() should error when not configured")
	}
}

func TestCredentialsManager_GetAzureCredentials_NotConfigured(t *testing.T) {
	cm := NewCredentialsManager()
	_, err := cm.GetAzureCredentials()
	if err == nil {
		t.Error("GetAzureCredentials() should error when not configured")
	}
}

func TestCredentialsManager_GetAzureCredentials_Configured(t *testing.T) {
	cm := NewCredentialsManager()
	cm.SetCredentials(&AzureCredentials{
		ClientID:       "client",
		ClientSecret:   "secret",
		TenantID:       "tenant",
		SubscriptionID: "sub",
	})
	got, err := cm.GetAzureCredentials()
	if err != nil {
		t.Fatalf("GetAzureCredentials() error = %v", err)
	}
	if got.ClientID != "client" {
		t.Errorf("GetAzureCredentials() ClientID = %q, want client", got.ClientID)
	}
}

// Credentials saved from the UI come back after a restart once the vault opens.
func TestCredentialsManager_VaultRoundTrip(t *testing.T) {
	dataDir := t.TempDir()
	vault := openTestVault(t, dataDir, nil)
	if err := vault.SetPassphrase(testVaultPassphrase); err != nil {
		t.Fatalf("SetPassphrase() error = %v", err)
	}
	cm := &CredentialsManager{credentials: make(map[string]ProviderCredentials)}
	cm.AttachVault(vault)
	if err := cm.SetCredentials(testOVHCredentials()); err != nil {
		t.Fatalf("SetCredentials() error = %v", err)
	}

	// Restart: nothing in memory until the passphrase is entered.
	restartedVault := openTestVault(t, dataDir, nil)
	restarted := &CredentialsManager{credentials: make(map[string]ProviderCredentials)}
	restarted.AttachVault(restartedVault)
	if restarted.HasCredentials("ovh") {
		t.Fatal("credentials available while the vault is locked")
	}

	// Entered while locked: must be saved on unlock, not replaced by what is saved.
	azure := &AzureCredentials{ClientID: "c", ClientSecret: "s", TenantID: "t", SubscriptionID: "sub"}
	if err := restarted.SetCredentials(azure); err != nil {
		t.Fatalf("SetCredentials() while locked error = %v", err)
	}

	if err := restartedVault.Unlock(testVaultPassphrase); err != nil {
		t.Fatalf("Unlock() error = %v", err)
	}
	got, err := restarted.GetOVHCredentials()
	if err != nil {
		t.Fatalf("GetOVHCredentials() after unlock error = %v", err)
	}
	if *got != *testOVHCredentials() {
		t.Errorf("OVH credentials after unlock = %+v, want %+v", got, testOVHCredentials())
	}
	var savedAzure AzureCredentials
	found, err := restartedVault.GetProviderCredentials("azure", &savedAzure)
	if err != nil || !found {
		t.Fatalf("azure credentials entered while locked were not saved on unlock (found=%v, err=%v)", found, err)
	}
	if savedAzure != *azure {
		t.Errorf("saved azure credentials = %+v, want %+v", savedAzure, *azure)
	}

	// Clearing removes the saved copy too.
	restarted.ClearCredentials("ovh")
	var cleared OVHCredentials
	if found, _ := restartedVault.GetProviderCredentials("ovh", &cleared); found {
		t.Error("ClearCredentials() left the saved copy in the vault")
	}
}

// With LAB_DATA_ENCRYPTION_KEY the vault opens by itself, and what was saved in
// the UI replaces credentials that came from environment variables.
func TestCredentialsManager_VaultOverridesEnvironment(t *testing.T) {
	dataDir := t.TempDir()
	first := &CredentialsManager{credentials: make(map[string]ProviderCredentials)}
	first.AttachVault(openTestVault(t, dataDir, testEnvKey(1)))
	if err := first.SetCredentials(testOVHCredentials()); err != nil {
		t.Fatalf("SetCredentials() error = %v", err)
	}

	fromEnv := &OVHCredentials{ApplicationKey: "env", ApplicationSecret: "env", ConsumerKey: "env", ServiceName: "env", Endpoint: "ovh-ca"}
	restarted := &CredentialsManager{credentials: map[string]ProviderCredentials{"ovh": fromEnv}}
	restarted.AttachVault(openTestVault(t, dataDir, testEnvKey(1)))

	got, err := restarted.GetOVHCredentials()
	if err != nil {
		t.Fatalf("GetOVHCredentials() error = %v", err)
	}
	if *got != *testOVHCredentials() {
		t.Errorf("credentials = %+v, want the saved ones %+v", got, testOVHCredentials())
	}

	// A manager with no vault behaves exactly as before.
	plain := &CredentialsManager{credentials: make(map[string]ProviderCredentials)}
	plain.AttachVault(nil)
	if err := plain.SetCredentials(testOVHCredentials()); err != nil {
		t.Fatalf("SetCredentials() without vault error = %v", err)
	}
	plain.ClearCredentials("ovh")
	if plain.HasCredentials("ovh") {
		t.Error("ClearCredentials() without vault left credentials behind")
	}
}
