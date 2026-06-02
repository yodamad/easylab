package utils

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func TestCreateZipFile_Success(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "test.zip")

	writer, file, absPath, err := createZipFile(zipPath)
	if err != nil {
		t.Fatalf("createZipFile() error = %v", err)
	}
	defer file.Close()
	defer writer.Close()

	if writer == nil {
		t.Error("createZipFile() writer is nil")
	}
	if absPath == "" {
		t.Error("createZipFile() absPath is empty")
	}

	// Verify file was created
	if _, err := os.Stat(zipPath); err != nil {
		t.Errorf("createZipFile() file not created: %v", err)
	}
}

func TestCreateZipFile_InvalidPath(t *testing.T) {
	_, _, _, err := createZipFile("/nonexistent/dir/test.zip")
	if err == nil {
		t.Error("createZipFile() should error for invalid path")
	}
}

func TestAddFileToZip_Success(t *testing.T) {
	dir := t.TempDir()

	// Create a source file
	srcFile := filepath.Join(dir, "source.tf")
	if err := os.WriteFile(srcFile, []byte("terraform {}"), 0644); err != nil {
		t.Fatal(err)
	}

	zipPath := filepath.Join(dir, "test.zip")
	writer, file, _, err := createZipFile(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	err = addFileToZip(writer, srcFile, "source.tf")
	writer.Close()
	if err != nil {
		t.Fatalf("addFileToZip() error = %v", err)
	}

	// Verify zip has an entry
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatalf("failed to open zip: %v", err)
	}
	defer r.Close()
	if len(r.File) != 1 {
		t.Errorf("zip has %d files, want 1", len(r.File))
	}
	if r.File[0].Name != "source.tf" {
		t.Errorf("zip entry name = %q, want source.tf", r.File[0].Name)
	}
}

func TestAddFileToZip_MissingFile(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "test.zip")
	writer, file, _, err := createZipFile(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	defer writer.Close()

	err = addFileToZip(writer, "/nonexistent/file.tf", "file.tf")
	if err == nil {
		t.Error("addFileToZip() should error for missing file")
	}
}

func TestZipTerraformFile_Success(t *testing.T) {
	dir := t.TempDir()
	tfFile := filepath.Join(dir, "main.tf")
	if err := os.WriteFile(tfFile, []byte(`terraform { required_version = ">= 1.0" }`), 0644); err != nil {
		t.Fatal(err)
	}

	absPath, err := ZipTerraformFile(tfFile)
	if err != nil {
		t.Fatalf("ZipTerraformFile() error = %v", err)
	}
	if absPath == "" {
		t.Error("ZipTerraformFile() absPath is empty")
	}

	// Verify the zip contains the .tf file
	r, err := zip.OpenReader(filepath.Join(dir, "main.zip"))
	if err != nil {
		t.Fatalf("failed to open zip: %v", err)
	}
	defer r.Close()
	if len(r.File) != 1 {
		t.Errorf("zip has %d files, want 1", len(r.File))
	}
	if r.File[0].Name != "main.tf" {
		t.Errorf("zip entry name = %q, want main.tf", r.File[0].Name)
	}
}

func TestZipTerraformFile_MissingFile(t *testing.T) {
	_, err := ZipTerraformFile("/nonexistent/dir/main.tf")
	if err == nil {
		t.Error("ZipTerraformFile() should error for missing file")
	}
}
