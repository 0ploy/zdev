package tools

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
// (the pinned release from internal/config/defaults.go plus how it unpacks).
type ToolInfo struct {
	config.ToolRelease
	BinaryName  string
	ArchiveType string   // "", "tar.gz", or "zip" - empty means bare binary
	ExtraFiles  []string // Additional files to extract from archive (e.g., mutagen-agents.tar.gz)
	// VersionArgs print the version (output contains Version); they identify
	// a copy installed before zdev recorded pins.
	VersionArgs []string
	// BeforeUpdate runs with the installed binary before a newer one replaces
	// it (mutagen stops its daemon, which a newer client can't talk to).
	BeforeUpdate func(ctx context.Context, oldPath string)
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
	if info.Mode()&0111 == 0 {
		return false
	}
	for _, extra := range tool.ExtraFiles {
		info, err := os.Stat(filepath.Join(m.binDir, extra))
		if err != nil || !info.Mode().IsRegular() {
			return false
		}
	}
	return true
}

// EnsureTool downloads a tool if not present, returns path to binary
// First checks system PATH, then zdev's bin directory, then downloads
func (m *Manager) EnsureTool(ctx context.Context, tool ToolInfo) (string, error) {
	// First, check if tool is in system PATH
	if path, found := FindInPath(tool.BinaryName); found {
		return path, nil
	}

	// Check if we already have it in our bin directory, from the pinned release
	toolPath := m.GetToolPath(tool)
	installed := m.ToolExists(tool)
	if installed && m.toolCurrent(ctx, tool) {
		return toolPath, nil
	}

	if installed {
		fmt.Fprintf(os.Stderr, "Updating %s to %s...\n", tool.Name, tool.Version)
		if tool.BeforeUpdate != nil {
			tool.BeforeUpdate(ctx, toolPath)
		}
	}
	if err := m.downloadTool(ctx, tool); err != nil {
		if installed {
			// The old copy is untouched (downloadTool replaces only after
			// verifying); keep working offline and retry next time.
			fmt.Fprintf(os.Stderr, "Warning: updating %s failed, keeping the installed one: %v\n", tool.Name, err)
			return toolPath, nil
		}
		return "", fmt.Errorf("failed to download %s: %w", tool.Name, err)
	}
	m.recordPin(tool)

	return toolPath, nil
}

// InstalledTool returns a usable copy without installing a missing one: the
// copy on PATH, or zdev's own, replaced first when its pin changed (a failed
// update keeps the old copy). For tools whose first install belongs to setup
// (mkcert), so later commands still pick up a newer pinned release.
func (m *Manager) InstalledTool(ctx context.Context, tool ToolInfo) (string, bool) {
	if path, found := FindInPath(tool.BinaryName); found {
		return path, true
	}
	if !m.ToolExists(tool) {
		return "", false
	}
	path, err := m.EnsureTool(ctx, tool)
	return path, err == nil
}

// The pin file next to a downloaded tool records the release it came from,
// so a changed pin in defaults.go replaces it: without it, a copy in
// ~/.zdev/bin would stay at whatever version was current on first use.
func (m *Manager) pinPath(tool ToolInfo) string {
	return filepath.Join(m.binDir, "."+tool.BinaryName+".pin")
}

func pinOf(tool ToolInfo) string {
	return tool.Version + " " + tool.Checksum(runtime.GOOS, runtime.GOARCH) + "\n"
}

func (m *Manager) recordPin(tool ToolInfo) {
	_ = os.WriteFile(m.pinPath(tool), []byte(pinOf(tool)), 0o644)
}

// toolCurrent reports whether the installed copy is the pinned release. A
// copy from before pin files (no file) counts when its version output
// contains the pinned version; it then gets its pin file.
func (m *Manager) toolCurrent(ctx context.Context, tool ToolInfo) bool {
	if b, err := os.ReadFile(m.pinPath(tool)); err == nil {
		return string(b) == pinOf(tool)
	}
	if len(tool.VersionArgs) == 0 {
		return false
	}
	out, err := RunTool(ctx, m.GetToolPath(tool), tool.VersionArgs...)
	if err != nil || !strings.Contains(out, tool.Version) {
		return false
	}
	m.recordPin(tool)
	return true
}

// downloadTool downloads and installs a tool
func (m *Manager) downloadTool(ctx context.Context, tool ToolInfo) error {
	url := tool.DownloadURL(runtime.GOOS, runtime.GOARCH)
	destPath := m.GetToolPath(tool)
	checksum := tool.Checksum(runtime.GOOS, runtime.GOARCH)
	if checksum == "" {
		return fmt.Errorf("no SHA-256 checksum configured for %s %s/%s", tool.Name, runtime.GOOS, runtime.GOARCH)
	}
	if _, err := hex.DecodeString(checksum); err != nil || len(checksum) != sha256.Size*2 {
		return fmt.Errorf("invalid SHA-256 checksum configured for %s %s/%s", tool.Name, runtime.GOOS, runtime.GOARCH)
	}

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

	// Hash while downloading, cap the response, and verify before extracting
	// or marking any bytes executable.
	const maxToolDownloadSize = int64(512 << 20)
	hasher := sha256.New()
	written, err := io.Copy(io.MultiWriter(tmpFile, hasher), io.LimitReader(resp.Body, maxToolDownloadSize+1))
	if err != nil {
		tmpFile.Close()
		return fmt.Errorf("failed to write file: %w", err)
	}
	if written > maxToolDownloadSize {
		tmpFile.Close()
		return fmt.Errorf("download exceeds %d bytes", maxToolDownloadSize)
	}
	if err := tmpFile.Sync(); err != nil {
		tmpFile.Close()
		return fmt.Errorf("failed to sync downloaded file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("failed to close downloaded file: %w", err)
	}
	actualChecksum := hex.EncodeToString(hasher.Sum(nil))
	if !strings.EqualFold(actualChecksum, checksum) {
		return fmt.Errorf("checksum mismatch: got %s, want %s", actualChecksum, checksum)
	}

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
	staged := make(map[string]string)
	defer func() {
		for _, path := range staged {
			_ = os.Remove(path)
		}
	}()

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
		_, wanted := filesToExtract[name]
		if !wanted || header.Typeflag != tar.TypeReg {
			continue
		}
		if extracted[name] {
			return fmt.Errorf("archive contains duplicate required file %q", name)
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
		if err := tmpFile.Close(); err != nil {
			os.Remove(tmpPath)
			return fmt.Errorf("failed to close extracted %s: %w", name, err)
		}

		// Make executable if it's the main binary
		if name == binaryName {
			if err := os.Chmod(tmpPath, 0755); err != nil {
				os.Remove(tmpPath)
				return fmt.Errorf("failed to make executable: %w", err)
			}
		}

		extracted[name] = true
		staged[name] = tmpPath
	}

	for name := range filesToExtract {
		if !extracted[name] {
			return fmt.Errorf("required file %q not found in archive", name)
		}
	}

	// Install supporting files first and the executable last. ToolExists only
	// sees a completed installation, so a failed extra-file move cannot leave
	// a binary that permanently skips repair on the next run.
	installOrder := append([]string(nil), extraFiles...)
	installOrder = append(installOrder, binaryName)
	for _, name := range installOrder {
		if err := os.Rename(staged[name], filesToExtract[name]); err != nil {
			return fmt.Errorf("failed to install %s: %w", name, err)
		}
		delete(staged, name)
	}

	return nil
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
