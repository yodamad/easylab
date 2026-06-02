package ovh

import (
	"testing"
)

func TestOVHDNSProvider_Name(t *testing.T) {
	p := New()
	if got := p.Name(); got != "ovh" {
		t.Errorf("Name() = %q, want ovh", got)
	}
}

func TestOVHDNSProvider_GetCredentialFields(t *testing.T) {
	p := New()
	fields := p.GetCredentialFields()
	if len(fields) == 0 {
		t.Error("GetCredentialFields() returned empty slice")
	}
	// Each field must have a non-empty Name and Label
	for _, f := range fields {
		if f.Name == "" {
			t.Errorf("credential field has empty Name: %+v", f)
		}
		if f.Label == "" {
			t.Errorf("credential field has empty Label: %+v", f)
		}
	}
}
