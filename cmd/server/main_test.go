package main

import (
	"bytes"
	"easylab/internal/server"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadEnvFile_EmptyPath(t *testing.T) {
	err := loadEnvFile("")
	if err != nil {
		t.Errorf("loadEnvFile(\"\") error = %v, want nil", err)
	}
}

func TestLoadEnvFile_NonExistentFile(t *testing.T) {
	err := loadEnvFile("/nonexistent/file.env")
	if err == nil {
		t.Error("loadEnvFile() with non-existent file should return error")
	}
	// Check that error message contains expected text (since error is wrapped)
	if err != nil {
		errMsg := err.Error()
		if !strings.Contains(errMsg, "failed to open") && !strings.Contains(errMsg, "no such file") {
			// Try to unwrap and check underlying error
			var pathErr *os.PathError
			if errors.As(err, &pathErr) {
				if !os.IsNotExist(pathErr.Err) {
					t.Errorf("loadEnvFile() error = %v, want file not exist error", err)
				}
			} else if errMsg == "" {
				t.Error("loadEnvFile() should return descriptive error for non-existent file")
			}
		}
	}
}

func TestLoadEnvFile_ValidFile(t *testing.T) {
	// Create a temporary directory
	tmpDir := t.TempDir()
	envFile := filepath.Join(tmpDir, "test.env")

	// Write test content
	content := `TEST_KEY1=value1
TEST_KEY2=value2
TEST_KEY3=value3
`
	if err := os.WriteFile(envFile, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to create test env file: %v", err)
	}

	// Clean up environment variables after test
	defer func() {
		os.Unsetenv("TEST_KEY1")
		os.Unsetenv("TEST_KEY2")
		os.Unsetenv("TEST_KEY3")
	}()

	// Load the env file
	err := loadEnvFile(envFile)
	if err != nil {
		t.Fatalf("loadEnvFile() error = %v", err)
	}

	// Verify environment variables were set
	if got := os.Getenv("TEST_KEY1"); got != "value1" {
		t.Errorf("TEST_KEY1 = %s, want value1", got)
	}
	if got := os.Getenv("TEST_KEY2"); got != "value2" {
		t.Errorf("TEST_KEY2 = %s, want value2", got)
	}
	if got := os.Getenv("TEST_KEY3"); got != "value3" {
		t.Errorf("TEST_KEY3 = %s, want value3", got)
	}
}

func TestLoadEnvFile_WithComments(t *testing.T) {
	tmpDir := t.TempDir()
	envFile := filepath.Join(tmpDir, "test.env")

	content := `# This is a comment
TEST_KEY1=value1
# Another comment
TEST_KEY2=value2
# Comment at end
`
	if err := os.WriteFile(envFile, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to create test env file: %v", err)
	}

	defer func() {
		os.Unsetenv("TEST_KEY1")
		os.Unsetenv("TEST_KEY2")
	}()

	err := loadEnvFile(envFile)
	if err != nil {
		t.Fatalf("loadEnvFile() error = %v", err)
	}

	// Verify only non-comment lines were loaded
	if got := os.Getenv("TEST_KEY1"); got != "value1" {
		t.Errorf("TEST_KEY1 = %s, want value1", got)
	}
	if got := os.Getenv("TEST_KEY2"); got != "value2" {
		t.Errorf("TEST_KEY2 = %s, want value2", got)
	}
}

func TestLoadEnvFile_WithEmptyLines(t *testing.T) {
	tmpDir := t.TempDir()
	envFile := filepath.Join(tmpDir, "test.env")

	content := `TEST_KEY1=value1

TEST_KEY2=value2

TEST_KEY3=value3
`
	if err := os.WriteFile(envFile, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to create test env file: %v", err)
	}

	defer func() {
		os.Unsetenv("TEST_KEY1")
		os.Unsetenv("TEST_KEY2")
		os.Unsetenv("TEST_KEY3")
	}()

	err := loadEnvFile(envFile)
	if err != nil {
		t.Fatalf("loadEnvFile() error = %v", err)
	}

	// Verify all variables were loaded despite empty lines
	if got := os.Getenv("TEST_KEY1"); got != "value1" {
		t.Errorf("TEST_KEY1 = %s, want value1", got)
	}
	if got := os.Getenv("TEST_KEY2"); got != "value2" {
		t.Errorf("TEST_KEY2 = %s, want value2", got)
	}
	if got := os.Getenv("TEST_KEY3"); got != "value3" {
		t.Errorf("TEST_KEY3 = %s, want value3", got)
	}
}

func TestLoadEnvFile_WithExportFormat(t *testing.T) {
	tmpDir := t.TempDir()
	envFile := filepath.Join(tmpDir, "test.env")

	content := `export TEST_KEY1=value1
TEST_KEY2=value2
export TEST_KEY3=value3
`
	if err := os.WriteFile(envFile, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to create test env file: %v", err)
	}

	defer func() {
		os.Unsetenv("TEST_KEY1")
		os.Unsetenv("TEST_KEY2")
		os.Unsetenv("TEST_KEY3")
	}()

	err := loadEnvFile(envFile)
	if err != nil {
		t.Fatalf("loadEnvFile() error = %v", err)
	}

	// Verify all variables were loaded, including export format
	if got := os.Getenv("TEST_KEY1"); got != "value1" {
		t.Errorf("TEST_KEY1 = %s, want value1", got)
	}
	if got := os.Getenv("TEST_KEY2"); got != "value2" {
		t.Errorf("TEST_KEY2 = %s, want value2", got)
	}
	if got := os.Getenv("TEST_KEY3"); got != "value3" {
		t.Errorf("TEST_KEY3 = %s, want value3", got)
	}
}

func TestLoadEnvFile_WithQuotedValues(t *testing.T) {
	tmpDir := t.TempDir()
	envFile := filepath.Join(tmpDir, "test.env")

	content := `TEST_KEY1="quoted value"
TEST_KEY2='single quoted'
TEST_KEY3=unquoted value
TEST_KEY4="value with spaces"
`
	if err := os.WriteFile(envFile, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to create test env file: %v", err)
	}

	defer func() {
		os.Unsetenv("TEST_KEY1")
		os.Unsetenv("TEST_KEY2")
		os.Unsetenv("TEST_KEY3")
		os.Unsetenv("TEST_KEY4")
	}()

	err := loadEnvFile(envFile)
	if err != nil {
		t.Fatalf("loadEnvFile() error = %v", err)
	}

	// Verify quoted values have quotes removed
	if got := os.Getenv("TEST_KEY1"); got != "quoted value" {
		t.Errorf("TEST_KEY1 = %s, want 'quoted value'", got)
	}
	if got := os.Getenv("TEST_KEY2"); got != "single quoted" {
		t.Errorf("TEST_KEY2 = %s, want 'single quoted'", got)
	}
	if got := os.Getenv("TEST_KEY3"); got != "unquoted value" {
		t.Errorf("TEST_KEY3 = %s, want 'unquoted value'", got)
	}
	if got := os.Getenv("TEST_KEY4"); got != "value with spaces" {
		t.Errorf("TEST_KEY4 = %s, want 'value with spaces'", got)
	}
}

func TestLoadEnvFile_WithWhitespace(t *testing.T) {
	tmpDir := t.TempDir()
	envFile := filepath.Join(tmpDir, "test.env")

	content := `  TEST_KEY1  =  value1  
TEST_KEY2=value2
  TEST_KEY3=value3
`
	if err := os.WriteFile(envFile, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to create test env file: %v", err)
	}

	defer func() {
		os.Unsetenv("TEST_KEY1")
		os.Unsetenv("TEST_KEY2")
		os.Unsetenv("TEST_KEY3")
	}()

	err := loadEnvFile(envFile)
	if err != nil {
		t.Fatalf("loadEnvFile() error = %v", err)
	}

	// Verify whitespace is trimmed
	if got := os.Getenv("TEST_KEY1"); got != "value1" {
		t.Errorf("TEST_KEY1 = %s, want value1", got)
	}
	if got := os.Getenv("TEST_KEY2"); got != "value2" {
		t.Errorf("TEST_KEY2 = %s, want value2", got)
	}
	if got := os.Getenv("TEST_KEY3"); got != "value3" {
		t.Errorf("TEST_KEY3 = %s, want value3", got)
	}
}

func TestLoadEnvFile_WithInvalidLines(t *testing.T) {
	tmpDir := t.TempDir()
	envFile := filepath.Join(tmpDir, "test.env")

	content := `TEST_KEY1=value1
INVALID_LINE_NO_EQUALS
TEST_KEY2=value2
ANOTHER_INVALID
TEST_KEY3=value3
`
	if err := os.WriteFile(envFile, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to create test env file: %v", err)
	}

	defer func() {
		os.Unsetenv("TEST_KEY1")
		os.Unsetenv("TEST_KEY2")
		os.Unsetenv("TEST_KEY3")
	}()

	err := loadEnvFile(envFile)
	if err != nil {
		t.Fatalf("loadEnvFile() error = %v", err)
	}

	// Verify valid lines were loaded despite invalid ones
	if got := os.Getenv("TEST_KEY1"); got != "value1" {
		t.Errorf("TEST_KEY1 = %s, want value1", got)
	}
	if got := os.Getenv("TEST_KEY2"); got != "value2" {
		t.Errorf("TEST_KEY2 = %s, want value2", got)
	}
	if got := os.Getenv("TEST_KEY3"); got != "value3" {
		t.Errorf("TEST_KEY3 = %s, want value3", got)
	}
}

func TestLoadEnvFile_WithEmptyKey(t *testing.T) {
	tmpDir := t.TempDir()
	envFile := filepath.Join(tmpDir, "test.env")

	content := `TEST_KEY1=value1
=value_without_key
TEST_KEY2=value2
`
	if err := os.WriteFile(envFile, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to create test env file: %v", err)
	}

	defer func() {
		os.Unsetenv("TEST_KEY1")
		os.Unsetenv("TEST_KEY2")
	}()

	err := loadEnvFile(envFile)
	if err != nil {
		t.Fatalf("loadEnvFile() error = %v", err)
	}

	// Verify only valid keys were loaded
	if got := os.Getenv("TEST_KEY1"); got != "value1" {
		t.Errorf("TEST_KEY1 = %s, want value1", got)
	}
	if got := os.Getenv("TEST_KEY2"); got != "value2" {
		t.Errorf("TEST_KEY2 = %s, want value2", got)
	}
}

func TestLoadEnvFile_OverwritesExistingEnv(t *testing.T) {
	// Set an existing environment variable
	os.Setenv("TEST_OVERWRITE", "old_value")
	defer os.Unsetenv("TEST_OVERWRITE")

	tmpDir := t.TempDir()
	envFile := filepath.Join(tmpDir, "test.env")

	content := `TEST_OVERWRITE=new_value
`
	if err := os.WriteFile(envFile, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to create test env file: %v", err)
	}

	err := loadEnvFile(envFile)
	if err != nil {
		t.Fatalf("loadEnvFile() error = %v", err)
	}

	// Verify the value was overwritten
	if got := os.Getenv("TEST_OVERWRITE"); got != "new_value" {
		t.Errorf("TEST_OVERWRITE = %s, want new_value", got)
	}
}

func TestLoadEnvFile_ComplexRealWorldExample(t *testing.T) {
	tmpDir := t.TempDir()
	envFile := filepath.Join(tmpDir, "test.env")

	content := `# OVHcloud credentials
OVH_APPLICATION_KEY=your-key
OVH_APPLICATION_SECRET=your-secret
OVH_CONSUMER_KEY=your-consumer-key
OVH_SERVICE_NAME=your-service-name
OVH_ENDPOINT=ovh-eu

# Application settings
LAB_ADMIN_PASSWORD="your-secure-password"
LAB_STUDENT_PASSWORD=student-password
WORK_DIR=/app/jobs
DATA_DIR=/app/data

# Commented out setting
# DISABLED_SETTING=disabled
`
	if err := os.WriteFile(envFile, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to create test env file: %v", err)
	}

	defer func() {
		os.Unsetenv("OVH_APPLICATION_KEY")
		os.Unsetenv("OVH_APPLICATION_SECRET")
		os.Unsetenv("OVH_CONSUMER_KEY")
		os.Unsetenv("OVH_SERVICE_NAME")
		os.Unsetenv("OVH_ENDPOINT")
		os.Unsetenv("LAB_ADMIN_PASSWORD")
		os.Unsetenv("LAB_STUDENT_PASSWORD")
		os.Unsetenv("WORK_DIR")
		os.Unsetenv("DATA_DIR")
	}()

	err := loadEnvFile(envFile)
	if err != nil {
		t.Fatalf("loadEnvFile() error = %v", err)
	}

	// Verify all variables were loaded correctly
	tests := []struct {
		key   string
		value string
	}{
		{"OVH_APPLICATION_KEY", "your-key"},
		{"OVH_APPLICATION_SECRET", "your-secret"},
		{"OVH_CONSUMER_KEY", "your-consumer-key"},
		{"OVH_SERVICE_NAME", "your-service-name"},
		{"OVH_ENDPOINT", "ovh-eu"},
		{"LAB_ADMIN_PASSWORD", "your-secure-password"},
		{"LAB_STUDENT_PASSWORD", "student-password"},
		{"WORK_DIR", "/app/jobs"},
		{"DATA_DIR", "/app/data"},
	}

	for _, tt := range tests {
		if got := os.Getenv(tt.key); got != tt.value {
			t.Errorf("%s = %s, want %s", tt.key, got, tt.value)
		}
	}

	// Verify commented out setting was not loaded
	if got := os.Getenv("DISABLED_SETTING"); got != "" {
		t.Errorf("DISABLED_SETTING should not be set, got %s", got)
	}
}

func TestLoadEnvFile_EmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	envFile := filepath.Join(tmpDir, "test.env")

	// Create empty file
	if err := os.WriteFile(envFile, []byte(""), 0644); err != nil {
		t.Fatalf("Failed to create test env file: %v", err)
	}

	err := loadEnvFile(envFile)
	if err != nil {
		t.Fatalf("loadEnvFile() with empty file should not return error, got %v", err)
	}
}

func TestLoadEnvFile_OnlyCommentsAndEmptyLines(t *testing.T) {
	tmpDir := t.TempDir()
	envFile := filepath.Join(tmpDir, "test.env")

	content := `# Comment line 1

# Comment line 2

# Comment line 3
`
	if err := os.WriteFile(envFile, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to create test env file: %v", err)
	}

	err := loadEnvFile(envFile)
	if err != nil {
		t.Fatalf("loadEnvFile() error = %v", err)
	}

	// No environment variables should be set
	// This is a sanity check - we can't easily verify no vars were set
	// but we can verify the function doesn't error
}

func TestInitDataEncryption_NoDataDirNoKey(t *testing.T) {
	t.Setenv("LAB_DATA_ENCRYPTION_KEY", "")

	if err := initDataEncryption(""); err != nil {
		t.Fatalf("initDataEncryption(\"\") error = %v, want nil (persistence disabled)", err)
	}
}

// A key is mandatory once persistence is on: job files hold cluster kubeconfigs
// and DNS credentials, and EasyLab must never generate a key into the directory
// it protects.
func TestInitDataEncryption_RefusesWithoutKey(t *testing.T) {
	t.Setenv("LAB_DATA_ENCRYPTION_KEY", "")
	tmpDir := t.TempDir()
	t.Cleanup(func() { _ = server.InitDataEncryption(nil) })

	err := initDataEncryption(tmpDir)
	if err == nil {
		t.Fatal("initDataEncryption() with a data dir and no key = nil, want an error")
	}
	if !strings.Contains(err.Error(), "openssl rand -base64 32") {
		t.Errorf("error should tell the operator how to generate a key, got: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(tmpDir, legacyGeneratedKeyFileName)); !os.IsNotExist(statErr) {
		t.Errorf("no key file may be written beside the data, stat err = %v", statErr)
	}
}

// A data directory carried over from a version that generated its own key is
// still decryptable, but only with that key — so say where it is instead of
// failing with generic advice.
func TestInitDataEncryption_PointsAtLegacyKeyFile(t *testing.T) {
	t.Setenv("LAB_DATA_ENCRYPTION_KEY", "")
	tmpDir := t.TempDir()
	keyPath := filepath.Join(tmpDir, legacyGeneratedKeyFileName)
	if err := os.WriteFile(keyPath, []byte(base64.StdEncoding.EncodeToString(make([]byte, 32))), 0600); err != nil {
		t.Fatalf("failed to write legacy key file: %v", err)
	}

	err := initDataEncryption(tmpDir)
	if err == nil {
		t.Fatal("initDataEncryption() with a legacy key file = nil, want an error")
	}
	if !strings.Contains(err.Error(), keyPath) {
		t.Errorf("error should name the legacy key file %s, got: %v", keyPath, err)
	}
}

func TestInitDataEncryption_ExplicitKeyAccepted(t *testing.T) {
	tmpDir := t.TempDir()
	key := make([]byte, 32)
	t.Setenv("LAB_DATA_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(key))
	t.Cleanup(func() { _ = server.InitDataEncryption(nil) })

	if err := initDataEncryption(tmpDir); err != nil {
		t.Fatalf("initDataEncryption() with explicit key error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(tmpDir, legacyGeneratedKeyFileName)); !os.IsNotExist(err) {
		t.Fatalf("expected no key file beside the data, stat err = %v", err)
	}
}

func TestInitDataEncryption_InvalidExplicitKey(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("LAB_DATA_ENCRYPTION_KEY", "not-valid-base64!!!")

	if err := initDataEncryption(tmpDir); err == nil {
		t.Fatal("initDataEncryption() with invalid base64 key error = nil, want error")
	}
}

// The credential vault takes its key from here rather than reading the
// environment itself, so a malformed or absent value must read as "no key".
func TestExplicitEncryptionKey(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)

	tests := []struct {
		name string
		env  string
		want []byte
	}{
		{name: "unset", env: "", want: nil},
		{name: "blank", env: "   ", want: nil},
		{name: "not base64", env: "not base64!", want: nil},
		{name: "set", env: base64.StdEncoding.EncodeToString(key), want: key},
		{name: "set with surrounding space", env: " " + base64.StdEncoding.EncodeToString(key) + "\n", want: key},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("LAB_DATA_ENCRYPTION_KEY", tt.env)
			if got := explicitEncryptionKey(); !bytes.Equal(got, tt.want) {
				t.Errorf("explicitEncryptionKey() = %v, want %v", got, tt.want)
			}
		})
	}

	// A key file left in the data directory by an older version is not a key:
	// only the environment counts, which is what keeps the vault's contract.
	t.Setenv("LAB_DATA_ENCRYPTION_KEY", "")
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, legacyGeneratedKeyFileName),
		[]byte(base64.StdEncoding.EncodeToString(key)), 0600); err != nil {
		t.Fatalf("failed to write legacy key file: %v", err)
	}
	if got := explicitEncryptionKey(); got != nil {
		t.Errorf("explicitEncryptionKey() with a legacy key file = %v, want nil", got)
	}
}
