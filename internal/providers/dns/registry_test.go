package dns_test

import (
	"testing"

	"easylab/internal/providers/dns"

	k8s "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes"
	helmv3 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/helm/v3"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockDNSProvider struct {
	name string
}

func (m *mockDNSProvider) Name() string                   { return m.name }
func (m *mockDNSProvider) GetCredentialFields() []dns.CredentialField { return nil }
func (m *mockDNSProvider) SetupCertManagerDNS01(_ *pulumi.Context, _ *k8s.Provider, _, _ string, _ []pulumi.Resource) (dns.SolverSpec, *helmv3.Release, error) {
	return nil, nil, nil
}
func (m *mockDNSProvider) CreateARecord(_ *pulumi.Context, _, _ string, _ pulumi.StringOutput, _ []pulumi.Resource) error {
	return nil
}

func TestDNSRegister_NilProvider(t *testing.T) {
	err := dns.Register(nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "nil")
}

func TestDNSRegister_EmptyName(t *testing.T) {
	err := dns.Register(&mockDNSProvider{name: ""})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "empty")
}

func TestDNSRegister_Success(t *testing.T) {
	p := &mockDNSProvider{name: "dns-test-unique-1"}
	err := dns.Register(p)
	require.NoError(t, err)
}

func TestDNSRegister_Duplicate(t *testing.T) {
	name := "dns-test-dup"
	require.NoError(t, dns.Register(&mockDNSProvider{name: name}))
	err := dns.Register(&mockDNSProvider{name: name})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already registered")
}

func TestDNSGet_EmptyName(t *testing.T) {
	p, err := dns.Get("")
	assert.NoError(t, err)
	assert.Nil(t, p)
}

func TestDNSGet_NotFound(t *testing.T) {
	_, err := dns.Get("dns-nonexistent-xyz")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestDNSGet_Found(t *testing.T) {
	name := "dns-test-get"
	require.NoError(t, dns.Register(&mockDNSProvider{name: name}))

	p, err := dns.Get(name)
	require.NoError(t, err)
	assert.Equal(t, name, p.Name())
}

func TestDNSList_ContainsRegistered(t *testing.T) {
	name := "dns-test-list"
	require.NoError(t, dns.Register(&mockDNSProvider{name: name}))

	names := dns.List()
	found := false
	for _, n := range names {
		if n == name {
			found = true
			break
		}
	}
	assert.True(t, found, "List() should contain %q", name)
}
