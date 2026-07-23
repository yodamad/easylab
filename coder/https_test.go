package coder

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSubdomainOf(t *testing.T) {
	tests := []struct {
		name   string
		domain string
		zone   string
		want   string
	}{
		{
			name:   "subdomain inside the zone is stripped down to its record name",
			domain: "lab.ai-backbone.soprasteria.com",
			zone:   "ai-backbone.soprasteria.com",
			want:   "lab",
		},
		{
			name:   "multi-label subdomain keeps every label",
			domain: "lab.eu.example.com",
			zone:   "example.com",
			want:   "lab.eu",
		},
		{
			name:   "domain equal to the zone is the apex record",
			domain: "example.com",
			zone:   "example.com",
			want:   "example.com",
		},
		{
			name:   "empty zone leaves the domain untouched",
			domain: "lab.example.com",
			zone:   "",
			want:   "lab.example.com",
		},
		{
			name:   "domain outside the zone is left untouched",
			domain: "lab.example.org",
			zone:   "example.com",
			want:   "lab.example.org",
		},
		{
			// "notexample.com" ends with "example.com" as a string but is a
			// different domain; trimming it would build a bogus record name.
			name:   "suffix match that is not a label boundary is left untouched",
			domain: "notexample.com",
			zone:   "example.com",
			want:   "notexample.com",
		},
		{
			// The coder:wildcardDomain override is entered as a full wildcard FQDN
			// and reduced to a record name the same way.
			name:   "wildcard override is reduced to a wildcard record name",
			domain: "*.lab.example.com",
			zone:   "example.com",
			want:   "*.lab",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, subdomainOf(tt.domain, tt.zone))
		})
	}
}

func TestWildcardOf(t *testing.T) {
	tests := []struct {
		name      string
		subdomain string
		want      string
	}{
		{name: "record name", subdomain: "lab", want: "*.lab"},
		{name: "multi-label record name", subdomain: "lab.eu", want: "*.lab.eu"},
		{name: "full domain", subdomain: "lab.example.com", want: "*.lab.example.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, wildcardOf(tt.subdomain))
		})
	}
}
