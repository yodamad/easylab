package server

import (
	"encoding/json"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

func TestPulumiExecutor_outputValueToString(t *testing.T) {
	pe := &PulumiExecutor{}

	tests := []struct {
		name     string
		input    interface{}
		expected string
	}{
		{
			name:     "plain string",
			input:    "http://example.com",
			expected: "http://example.com",
		},
		{
			name:     "nil value",
			input:    nil,
			expected: "",
		},
		{
			name: "ConfigValue map format",
			input: map[string]interface{}{
				"Value":  "http://example.com",
				"Secret": false,
			},
			expected: "http://example.com",
		},
		{
			name: "ConfigValue map with secret true",
			input: map[string]interface{}{
				"Value":  "secret-token",
				"Secret": true,
			},
			expected: "secret-token",
		},
		{
			name:     "JSON-encoded string",
			input:    json.RawMessage(`"http://example.com"`),
			expected: "http://example.com",
		},
		{
			name:     "number",
			input:    123,
			expected: "123",
		},
		{
			name: "map without Value key",
			input: map[string]interface{}{
				"other": "value",
			},
			expected: `{"other":"value"}`,
		},
		{
			name:     "pulumi.StringOutput (should return empty)",
			input:    pulumi.String("test").ToStringOutput(),
			expected: "",
		},
		{
			name: "ConfigValue JSON marshaled",
			input: func() interface{} {
				jsonStr := `{"Value":"http://example.com","Secret":false}`
				var result interface{}
				json.Unmarshal([]byte(jsonStr), &result)
				return result
			}(),
			expected: "http://example.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := pe.outputValueToString(tt.input)
			if result != tt.expected {
				t.Errorf("outputValueToString() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func Test_extractStringFromConfigValue(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "plain string",
			input:    "http://example.com",
			expected: "http://example.com",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "ConfigValue JSON string",
			input:    `{"Value":"http://example.com","Secret":false}`,
			expected: "http://example.com",
		},
		{
			name:     "ConfigValue JSON with secret true",
			input:    `{"Value":"secret-token","Secret":true}`,
			expected: "secret-token",
		},
		{
			name:     "invalid JSON (not ConfigValue)",
			input:    `{"other":"value"}`,
			expected: `{"other":"value"}`,
		},
		{
			name:     "not JSON at all",
			input:    "just a regular string",
			expected: "just a regular string",
		},
		{
			name:     "malformed JSON",
			input:    `{"Value":"test"`,
			expected: `{"Value":"test"`,
		},
		{
			name:     "ConfigValue with non-string Value",
			input:    `{"Value":123,"Secret":false}`,
			expected: `{"Value":123,"Secret":false}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractStringFromConfigValue(tt.input)
			if result != tt.expected {
				t.Errorf("extractStringFromConfigValue() = %q, want %q", result, tt.expected)
			}
		})
	}
}
