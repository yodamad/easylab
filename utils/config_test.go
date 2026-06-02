package utils

import (
	"os"
	"testing"
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
