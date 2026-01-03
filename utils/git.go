package utils

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5"
)

// createZipFile creates a zip file and returns the writer and absolute path
func createZipFile(zipFilePath string) (*zip.Writer, *os.File, string, error) {
	zipFile, err := os.Create(zipFilePath)
	if err != nil {
		return nil, nil, "", fmt.Errorf("failed to create zip file: %w", err)
	}

	zipWriter := zip.NewWriter(zipFile)

	absPath, err := filepath.Abs(zipFilePath)
	if err != nil {
		zipWriter.Close()
		zipFile.Close()
		return nil, nil, "", fmt.Errorf("failed to get absolute path: %w", err)
	}

	return zipWriter, zipFile, absPath, nil
}

// addFileToZip adds a file to a zip writer
func addFileToZip(zipWriter *zip.Writer, filePath string, zipEntryName string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	fileInfo, err := file.Stat()
	if err != nil {
		return fmt.Errorf("failed to get file info: %w", err)
	}

	header, err := zip.FileInfoHeader(fileInfo)
	if err != nil {
		return fmt.Errorf("failed to create zip header: %w", err)
	}
	header.Name = zipEntryName
	header.Method = zip.Deflate

	writer, err := zipWriter.CreateHeader(header)
	if err != nil {
		return fmt.Errorf("failed to create zip entry: %w", err)
	}

	_, err = io.Copy(writer, file)
	if err != nil {
		return fmt.Errorf("failed to write file to zip: %w", err)
	}

	return nil
}

func CloneFolderFromGitAndZipIt(repoUrl string, folder string) (string, error) {
	// Create temporary directory for clone
	tempDir, err := os.MkdirTemp("", "git-clone-*")
	if err != nil {
		return "", fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tempDir)

	// Clone repository
	_, err = git.PlainClone(tempDir, false, &git.CloneOptions{
		URL: repoUrl,
	})
	if err != nil {
		return "", fmt.Errorf("failed to clone repository: %w", err)
	}

	// Build path to target folder
	targetPath := filepath.Join(tempDir, folder)
	if _, err := os.Stat(targetPath); err != nil {
		return "", fmt.Errorf("folder '%s' not found in repository: %w", folder, err)
	}

	// Create zip file
	zipFileName := strings.ReplaceAll(folder, "/", "-") + ".zip"
	zipWriter, zipFile, absPath, err := createZipFile(zipFileName)
	if err != nil {
		return "", err
	}
	defer zipFile.Close()
	defer zipWriter.Close()

	// Walk through target folder and add files to zip
	err = filepath.Walk(targetPath, func(filePath string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Create relative path for zip entry
		relPath, err := filepath.Rel(targetPath, filePath)
		if err != nil {
			return err
		}

		if info.IsDir() {
			relPath = relPath + "/"
			// Create directory entry in zip
			_, err := zipWriter.Create(relPath)
			return err
		}

		// Add file to zip
		return addFileToZip(zipWriter, filePath, relPath)
	})

	if err != nil {
		return "", fmt.Errorf("failed to add files to zip: %w", err)
	}

	return absPath, nil
}

// ZipTerraformFile creates a zip archive from a single .tf file
// The zip will contain the .tf file at the root level, which is the expected structure for Coder templates
func ZipTerraformFile(tfFilePath string) (string, error) {
	// Create zip file in the same directory as the .tf file
	zipFilePath := strings.TrimSuffix(tfFilePath, filepath.Ext(tfFilePath)) + ".zip"
	zipWriter, zipFile, absPath, err := createZipFile(zipFilePath)
	if err != nil {
		return "", err
	}
	defer zipFile.Close()
	defer zipWriter.Close()

	// Get the base name of the .tf file (e.g., "main.tf")
	baseName := filepath.Base(tfFilePath)

	// Add the .tf file to zip
	if err := addFileToZip(zipWriter, tfFilePath, baseName); err != nil {
		return "", fmt.Errorf("failed to add terraform file to zip: %w", err)
	}

	return absPath, nil
}
