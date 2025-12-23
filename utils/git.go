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
	zipFile, err := os.Create(zipFileName)
	if err != nil {
		return "", fmt.Errorf("failed to create zip file: %w", err)
	}
	defer zipFile.Close()

	// Create zip writer
	zipWriter := zip.NewWriter(zipFile)
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
		}

		// Create zip header
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = relPath

		// Write file to zip
		if !info.IsDir() {
			writer, err := zipWriter.CreateHeader(header)
			if err != nil {
				return err
			}

			file, err := os.Open(filePath)
			if err != nil {
				return err
			}
			defer file.Close()

			_, err = io.Copy(writer, file)
			if err != nil {
				return err
			}
		} else {
			_, err := zipWriter.Create(header.Name)
			if err != nil {
				return err
			}
		}

		return nil
	})

	if err != nil {
		return "", fmt.Errorf("failed to add files to zip: %w", err)
	}
	// return zip file path
	absPath, err := filepath.Abs(zipFileName)
	if err != nil {
		return "", fmt.Errorf("failed to get absolute path: %w", err)
	}
	return absPath, nil
}
