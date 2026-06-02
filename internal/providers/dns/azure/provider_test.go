package azure

import (
	"testing"
)

func TestAzureDNSProvider_Name(t *testing.T) {
	p := New()
	if got := p.Name(); got != "azure" {
		t.Errorf("Name() = %q, want azure", got)
	}
}

func TestAzureDNSProvider_GetCredentialFields(t *testing.T) {
	p := New()
	fields := p.GetCredentialFields()
	if len(fields) == 0 {
		t.Error("GetCredentialFields() returned empty slice")
	}
	for _, f := range fields {
		if f.Name == "" {
			t.Errorf("credential field has empty Name: %+v", f)
		}
		if f.Label == "" {
			t.Errorf("credential field has empty Label: %+v", f)
		}
	}
}
