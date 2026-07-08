package tools

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/0ploy/zdev/internal/config"
)

// ToolInfo describes a downloadable tool
type ToolInfo struct {
	Name        string
	Version     string
	URLTemplate string
	BinaryName  string
	ArchiveType string                                              // "", "tar.gz", or "zip" - empty means bare binary
	URLBuilder  func(template, version, goos, goarch string) string // Custom URL builder, nil uses default
	ExtraFiles  []string                                            // Additional files to extract from archive (e.g., mutagen-agents.tar.gz)
}

// Manager handles tool downloads and verification
type Manager struct {
	binDir string // ~/.zdev/bin
}

// NewManager creates a new tool manager
func NewManager() (*Manager, error) {
	binDir := filepath.Join(config.GetZdevHome(), "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create bin directory: %w", err)
	}

	return &Manager{binDir: binDir}, nil
}

// FindInPath checks if a tool exists in system PATH
// Returns the path if found, empty string otherwise
func FindInPath(name string) (string, bool) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "", false
	}
	return path, true
}

// GetToolPath returns the path to an installed tool in zdev's bin directory
func (m *Manager) GetToolPath(tool ToolInfo) string {
	return filepath.Join(m.binDir, tool.BinaryName)
}

// ToolExists checks if a tool is installed in zdev's bin directory
func (m *Manager) ToolExists(tool ToolInfo) bool {
	path := m.GetToolPath(tool)
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	// Check if it's executable
	return info.Mode()&0111 != 0
}

// EnsureTool downloads a tool if not present, returns path to binary
// First checks system PATH, then zdev's bin directory, then downloads
func (m *Manager) EnsureTool(ctx context.Context, tool ToolInfo) (string, error) {
	// First, check if tool is in system PATH
	if path, found := FindInPath(tool.BinaryName); found {
		return path, nil
	}

	// Check if we already have it in our bin directory
	toolPath := m.GetToolPath(tool)
	if m.ToolExists(tool) {
		return toolPath, nil
	}

	// Download the tool
	if err := m.downloadTool(ctx, tool); err != nil {
		return "", fmt.Errorf("failed to download %s: %w", tool.Name, err)
	}

	return toolPath, nil
}

// downloadTool downloads and installs a tool
func (m *Manager) downloadTool(ctx context.Context, tool ToolInfo) error {
	url := buildDownloadURL(tool)
	destPath := m.GetToolPath(tool)

	// Create HTTP request with context
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	// Download the file
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed with status %d", resp.StatusCode)
	}

	// Create temporary file
	tmpFile, err := os.CreateTemp(m.binDir, tool.BinaryName+".tmp.*")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath) // Clean up on failure

	// Write to temp file
	if _, err := io.Copy(tmpFile, resp.Body); err != nil {
		tmpFile.Close()
		return fmt.Errorf("failed to write file: %w", err)
	}
	tmpFile.Close()

	// Handle archive extraction if needed
	if tool.ArchiveType == "tar.gz" {
		if err := m.extractTarGz(tmpPath, tool.BinaryName, destPath, tool.ExtraFiles); err != nil {
			return fmt.Errorf("failed to extract archive: %w", err)
		}
		return nil
	}

	// Bare binary: make executable and move to final location
	if err := os.Chmod(tmpPath, 0755); err != nil {
		return fmt.Errorf("failed to make executable: %w", err)
	}

	// Move to final location (atomic on same filesystem)
	if err := os.Rename(tmpPath, destPath); err != nil {
		return fmt.Errorf("failed to install binary: %w", err)
	}

	return nil
}

// extractTarGz extracts a specific binary and optional extra files from a tar.gz archive
func (m *Manager) extractTarGz(archivePath, binaryName, destPath string, extraFiles []string) error {
	// Open the archive
	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("failed to open archive: %w", err)
	}
	defer f.Close()

	// Create gzip reader
	gzr, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("failed to create gzip reader: %w", err)
	}
	defer gzr.Close()

	// Create tar reader
	tr := tar.NewReader(gzr)

	// Build set of files to extract
	filesToExtract := make(map[string]string) // base name -> dest path
	filesToExtract[binaryName] = destPath
	for _, extra := range extraFiles {
		filesToExtract[extra] = filepath.Join(m.binDir, extra)
	}

	extracted := make(map[string]bool)

	// Find and extract the files
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("failed to read tar: %w", err)
		}

		// Check if this is a file we're looking for
		name := filepath.Base(header.Name)
		destFile, wanted := filesToExtract[name]
		if !wanted || header.Typeflag != tar.TypeReg {
			continue
		}

		// Create temp file for extraction
		tmpFile, err := os.CreateTemp(m.binDir, name+".extract.*")
		if err != nil {
			return fmt.Errorf("failed to create temp file: %w", err)
		}
		tmpPath := tmpFile.Name()

		// Copy the file content
		if _, err := io.Copy(tmpFile, tr); err != nil {
			tmpFile.Close()
			os.Remove(tmpPath)
			return fmt.Errorf("failed to extract %s: %w", name, err)
		}
		tmpFile.Close()

		// Make executable if it's the main binary
		if name == binaryName {
			if err := os.Chmod(tmpPath, 0755); err != nil {
				os.Remove(tmpPath)
				return fmt.Errorf("failed to make executable: %w", err)
			}
		}

		// Move to final location
		if err := os.Rename(tmpPath, destFile); err != nil {
			os.Remove(tmpPath)
			return fmt.Errorf("failed to install %s: %w", name, err)
		}

		extracted[name] = true
	}

	// Check if main binary was extracted
	if !extracted[binaryName] {
		return fmt.Errorf("binary %q not found in archive", binaryName)
	}

	return nil
}

// buildDownloadURL constructs the download URL for a tool
func buildDownloadURL(tool ToolInfo) string {
	goos := runtime.GOOS
	goarch := runtime.GOARCH

	// Use custom URL builder if provided
	if tool.URLBuilder != nil {
		return tool.URLBuilder(tool.URLTemplate, tool.Version, goos, goarch)
	}

	// Default: mkcert-style (version, version, os, arch)
	// URL template: https://github.com/FiloSottile/mkcert/releases/download/%s/mkcert-%s-%s-%s
	return fmt.Sprintf(tool.URLTemplate, tool.Version, tool.Version, goos, goarch)
}

// JustArch returns the architecture string for just downloads
// just uses x86_64/aarch64 instead of amd64/arm64
func JustArch(goarch string) string {
	switch goarch {
	case "amd64":
		return "x86_64"
	case "arm64":
		return "aarch64"
	default:
		return goarch
	}
}

// JustOS returns the OS string for just downloads
// just uses apple-darwin/unknown-linux-musl instead of darwin/linux
func JustOS(goos string) string {
	switch goos {
	case "darwin":
		return "apple-darwin"
	case "linux":
		return "unknown-linux-musl"
	default:
		return goos
	}
}

// RunTool executes a tool with the given arguments
func RunTool(ctx context.Context, toolPath string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, toolPath, args...)

	output, err := cmd.CombinedOutput()
	if err != nil {
		// Include output in error for debugging
		if len(output) > 0 {
			return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
		}
		return "", err
	}

	return strings.TrimSpace(string(output)), nil
}
