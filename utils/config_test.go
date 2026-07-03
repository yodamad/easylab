package utils

import (
	"os"
	"testing"
	"time"
)

func TestGetEnvOrDefault(t *testing.T) {
	tests := []struct {
		name         string
		key          string
		envValue     string
		defaultValue string
		want         string
	}{
		{
			name:         "env set returns env value",
			key:          "TEST_EASYLAB_PRESENT",
			envValue:     "from-env",
			defaultValue: "default",
			want:         "from-env",
		},
		{
			name:         "env not set returns default",
			key:          "TEST_EASYLAB_MISSING",
			envValue:     "",
			defaultValue: "fallback",
			want:         "fallback",
		},
		{
			name:         "empty default with missing env",
			key:          "TEST_EASYLAB_MISSING2",
			envValue:     "",
			defaultValue: "",
			want:         "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(tt.key, tt.envValue)
			got := GetEnvOrDefault(tt.key, tt.defaultValue)
			if got != tt.want {
				t.Errorf("GetEnvOrDefault(%q, %q) = %q, want %q", tt.key, tt.defaultValue, got, tt.want)
			}
		})
	}
}

func TestGetEnvOrDefault_EmptyEnvUsesDefault(t *testing.T) {
	key := "TEST_EASYLAB_EMPTY_VALUE"
	os.Unsetenv(key)
	got := GetEnvOrDefault(key, "my-default")
	if got != "my-default" {
		t.Errorf("GetEnvOrDefault() with unset env = %q, want %q", got, "my-default")
	}
}

func TestCoderAdminTokenLifetime(t *testing.T) {
	tests := []struct {
		name     string
		envValue string
		want     time.Duration
	}{
		{name: "unset uses default", envValue: "", want: DefaultCoderAdminTokenLifetime},
		{name: "valid duration overrides default", envValue: "168h", want: 168 * time.Hour},
		{name: "invalid duration falls back to default", envValue: "not-a-duration", want: DefaultCoderAdminTokenLifetime},
		{name: "non-positive duration falls back to default", envValue: "0h", want: DefaultCoderAdminTokenLifetime},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envValue == "" {
				os.Unsetenv("CODER_ADMIN_TOKEN_LIFETIME")
			} else {
				t.Setenv("CODER_ADMIN_TOKEN_LIFETIME", tt.envValue)
			}
			if got := CoderAdminTokenLifetime(); got != tt.want {
				t.Errorf("CoderAdminTokenLifetime() = %v, want %v", got, tt.want)
			}
		})
	}
}
