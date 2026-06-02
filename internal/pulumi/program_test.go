package pulumi

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveKubeconfigPath_Absolute(t *testing.T) {
	path := "/absolute/path/kubeconfig"
	got, err := resolveKubeconfigPath(path, "/some/job/dir")
	if err != nil {
		t.Fatalf("resolveKubeconfigPath() error = %v", err)
	}
	if got != path {
		t.Errorf("resolveKubeconfigPath() = %q, want %q", got, path)
	}
}

func TestResolveKubeconfigPath_RelativeWithJobDir(t *testing.T) {
	jobDir := t.TempDir()
	got, err := resolveKubeconfigPath("kubeconfig", jobDir)
	if err != nil {
		t.Fatalf("resolveKubeconfigPath() error = %v", err)
	}
	want := filepath.Join(jobDir, "kubeconfig")
	if got != want {
		t.Errorf("resolveKubeconfigPath() = %q, want %q", got, want)
	}
}

func TestResolveKubeconfigPath_RelativeNoJobDir(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	got, err := resolveKubeconfigPath("kubeconfig", "")
	if err != nil {
		t.Fatalf("resolveKubeconfigPath() error = %v", err)
	}
	want := filepath.Join(cwd, "kubeconfig")
	if got != want {
		t.Errorf("resolveKubeconfigPath() = %q, want %q", got, want)
	}
}
