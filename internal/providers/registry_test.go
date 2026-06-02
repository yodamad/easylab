package providers_test

import (
	"testing"

	"easylab/internal/providers"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockProvider is a minimal Provider implementation for testing.
type mockProvider struct {
	name string
}

func (m *mockProvider) Name() string                  { return m.name }
func (m *mockProvider) GetRequiredEnvVars() []string  { return nil }
func (m *mockProvider) GetPulumiConfigPrefix() string { return m.name + ":" }
func (m *mockProvider) CreateInfrastructure(_ *pulumi.Context, _ providers.ProviderConfig) (*providers.InfrastructureResult, error) {
	return nil, nil
}

func TestRegister_NilProvider(t *testing.T) {
	err := providers.Register(nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "nil")
}

func TestRegister_EmptyName(t *testing.T) {
	p := &mockProvider{name: ""}
	err := providers.Register(p)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "empty")
}

func TestRegister_Success(t *testing.T) {
	p := &mockProvider{name: "test-provider-unique-1"}
	err := providers.Register(p)
	require.NoError(t, err)
}

func TestRegister_Duplicate(t *testing.T) {
	p1 := &mockProvider{name: "test-provider-dup"}
	p2 := &mockProvider{name: "test-provider-dup"}
	require.NoError(t, providers.Register(p1))
	err := providers.Register(p2)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already registered")
}

func TestGet_NotFound(t *testing.T) {
	_, err := providers.Get("nonexistent-provider-xyz")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestGet_Found(t *testing.T) {
	name := "test-provider-get"
	require.NoError(t, providers.Register(&mockProvider{name: name}))

	p, err := providers.Get(name)
	require.NoError(t, err)
	assert.Equal(t, name, p.Name())
}

func TestIsRegistered_False(t *testing.T) {
	assert.False(t, providers.IsRegistered("not-registered-abc"))
}

func TestIsRegistered_True(t *testing.T) {
	name := "test-provider-isreg"
	require.NoError(t, providers.Register(&mockProvider{name: name}))
	assert.True(t, providers.IsRegistered(name))
}

func TestList_ContainsRegistered(t *testing.T) {
	name := "test-provider-list"
	require.NoError(t, providers.Register(&mockProvider{name: name}))

	names := providers.List()
	found := false
	for _, n := range names {
		if n == name {
			found = true
			break
		}
	}
	assert.True(t, found, "List() should contain registered provider %q", name)
}
