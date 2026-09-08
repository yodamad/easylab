package azure

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setPulumiConfig exposes the given stack config to the program run by
// pulumi.RunErr — the Go SDK reads it from the PULUMI_CONFIG environment.
func setPulumiConfig(t *testing.T, cfg map[string]string) {
	t.Helper()
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	t.Setenv("PULUMI_CONFIG", string(raw))
}

// recordingMocks captures every resource registered by the program under test.
type recordingMocks struct {
	mu        sync.Mutex
	resources []pulumi.MockResourceArgs
}

func (m *recordingMocks) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	m.mu.Lock()
	m.resources = append(m.resources, args)
	m.mu.Unlock()
	return args.Name + "-id", args.Inputs, nil
}

func (m *recordingMocks) Call(args pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return args.Args, nil
}

func (m *recordingMocks) find(typ string) (pulumi.MockResourceArgs, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.resources {
		if r.TypeToken == typ {
			return r, true
		}
	}
	return pulumi.MockResourceArgs{}, false
}

const (
	recordSetType = "azure-native:dns:RecordSet"
	providerType  = "pulumi:providers:azure-native"
)

// pluginVersion is the azure-native plugin version a registration asks for. An
// empty version makes the engine fall back to the newest installed plugin, which
// is how the record used to end up on a plugin that no longer served its token.
func pluginVersion(args pulumi.MockResourceArgs) string {
	if args.RegisterRPC == nil {
		return ""
	}
	return args.RegisterRPC.Version
}

// plainString unwraps a property value that the engine may have marked secret.
func plainString(pv resource.PropertyValue) string {
	if pv.IsSecret() {
		pv = pv.SecretValue().Element
	}
	if !pv.IsString() {
		return ""
	}
	return pv.StringValue()
}

func TestAzureDNSProvider_CreateARecord(t *testing.T) {
	fullCreds := map[string]string{
		"dns:azureClientId":       "client-id",
		"dns:azureClientSecret":   "client-secret",
		"dns:azureTenantId":       "tenant-id",
		"dns:azureSubscriptionId": "subscription-id",
		"dns:azureResourceGroup":  "rg-dns",
	}

	tests := []struct {
		name             string
		config           map[string]string
		subdomain        string
		wantProvider     bool
		wantProviderName string
	}{
		{
			name:             "explicit provider when service principal credentials are set",
			config:           fullCreds,
			subdomain:        "lab",
			wantProvider:     true,
			wantProviderName: "azure-dns-provider-lab",
		},
		{
			name:             "wildcard subdomain gets its own provider name",
			config:           fullCreds,
			subdomain:        "*.lab",
			wantProvider:     true,
			wantProviderName: "azure-dns-provider-wildcard-lab",
		},
		{
			name: "falls back to the default provider when credentials are incomplete",
			config: map[string]string{
				"dns:azureClientId":      "client-id",
				"dns:azureResourceGroup": "rg-dns",
			},
			subdomain:    "lab",
			wantProvider: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setPulumiConfig(t, tt.config)
			mocks := &recordingMocks{}

			err := pulumi.RunErr(func(ctx *pulumi.Context) error {
				ip := pulumi.String("1.2.3.4").ToStringOutput()
				return New().CreateARecord(ctx, "example.com", tt.subdomain, ip, nil)
			}, pulumi.WithMocks("project", "stack", mocks))
			require.NoError(t, err)

			record, ok := mocks.find(recordSetType)
			require.True(t, ok, "no %s registered", recordSetType)
			assert.Equal(t, "example.com", plainString(record.Inputs["zoneName"]))
			assert.Equal(t, tt.subdomain, plainString(record.Inputs["relativeRecordSetName"]))

			prov, hasProvider := mocks.find(providerType)
			require.Equal(t, tt.wantProvider, hasProvider, "explicit provider presence")
			if !tt.wantProvider {
				assert.Empty(t, record.Provider, "record must use the default provider")
				return
			}

			assert.Equal(t, tt.wantProviderName, prov.Name)
			assert.NotEmpty(t, record.Provider, "record must reference the explicit provider")
			assert.Equal(t, "client-id", plainString(prov.Inputs["clientId"]))
			assert.Equal(t, "tenant-id", plainString(prov.Inputs["tenantId"]))
			assert.Equal(t, "subscription-id", plainString(prov.Inputs["subscriptionId"]))

			// Both must pin the same azure-native plugin version, otherwise the
			// engine serves the record from the newest installed plugin, which may
			// not know its token.
			assert.NotEmpty(t, pluginVersion(prov), "provider must pin a plugin version")
			assert.Equal(t, pluginVersion(record), pluginVersion(prov),
				"record and provider must resolve to the same azure-native plugin")
		})
	}
}
