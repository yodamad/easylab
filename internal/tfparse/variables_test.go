package tfparse

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func TestParseVariablesFromBytes(t *testing.T) {
	hcl := []byte(`
variable "docker_image" {
  description = "Docker image to use"
  type        = string
  default     = "ubuntu:latest"
}

variable "cpu_count" {
  description = "Number of CPUs"
  type        = number
  default     = 2
}

variable "enable_gpu" {
  description = "Enable GPU support"
  type        = bool
  default     = false
  sensitive   = true
}

variable "api_key" {
  description = "API key for external service"
  type        = string
}

resource "docker_container" "workspace" {
  image = var.docker_image
}
`)

	vars, err := ParseVariablesFromBytes(hcl, "main.tf")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(vars) != 4 {
		t.Fatalf("expected 4 variables, got %d", len(vars))
	}

	tests := []struct {
		name        string
		description string
		typ         string
		dflt        string
		required    bool
		sensitive   bool
	}{
		{"docker_image", "Docker image to use", "string", "ubuntu:latest", false, false},
		{"cpu_count", "Number of CPUs", "number", "2", false, false},
		{"enable_gpu", "Enable GPU support", "bool", "false", false, true},
		{"api_key", "API key for external service", "string", "", true, false},
	}

	for i, tt := range tests {
		v := vars[i]
		if v.Name != tt.name {
			t.Errorf("var[%d]: name = %q, want %q", i, v.Name, tt.name)
		}
		if v.Description != tt.description {
			t.Errorf("var[%d] %s: description = %q, want %q", i, v.Name, v.Description, tt.description)
		}
		if v.Type != tt.typ {
			t.Errorf("var[%d] %s: type = %q, want %q", i, v.Name, v.Type, tt.typ)
		}
		if v.Default != tt.dflt {
			t.Errorf("var[%d] %s: default = %q, want %q", i, v.Name, v.Default, tt.dflt)
		}
		if v.Required != tt.required {
			t.Errorf("var[%d] %s: required = %v, want %v", i, v.Name, v.Required, tt.required)
		}
		if v.Sensitive != tt.sensitive {
			t.Errorf("var[%d] %s: sensitive = %v, want %v", i, v.Name, v.Sensitive, tt.sensitive)
		}
	}
}

func TestParseVariablesFromZip(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "template.zip")

	zf, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(zf)

	mainTF := []byte(`
variable "region" {
  description = "Cloud region"
  type        = string
  default     = "us-east-1"
}
`)
	varsTF := []byte(`
variable "instance_type" {
  description = "Instance type"
  type        = string
}
`)

	for name, content := range map[string][]byte{"main.tf": mainTF, "variables.tf": varsTF} {
		fw, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	w.Close()
	zf.Close()

	vars, err := ParseVariablesFromZip(zipPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(vars) != 2 {
		t.Fatalf("expected 2 variables, got %d", len(vars))
	}

	found := make(map[string]TFVariable)
	for _, v := range vars {
		found[v.Name] = v
	}

	if v, ok := found["region"]; !ok {
		t.Error("missing variable 'region'")
	} else if v.Required {
		t.Error("'region' should not be required (has default)")
	}

	if v, ok := found["instance_type"]; !ok {
		t.Error("missing variable 'instance_type'")
	} else if !v.Required {
		t.Error("'instance_type' should be required (no default)")
	}
}

func TestParseVariablesFromBytes_NoVariables(t *testing.T) {
	hcl := []byte(`
resource "docker_container" "workspace" {
  image = "ubuntu"
}
`)
	vars, err := ParseVariablesFromBytes(hcl, "main.tf")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vars) != 0 {
		t.Fatalf("expected 0 variables, got %d", len(vars))
	}
}
