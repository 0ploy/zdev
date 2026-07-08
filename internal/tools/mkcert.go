package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/0ploy/zdev/internal/config"
)

// MkcertTool returns the ToolInfo for mkcert
func MkcertTool() ToolInfo {
	return ToolInfo{
		Name:        "mkcert",
		Version:     config.MkcertVersion,
		URLTemplate: config.MkcertURLTemplate,
		BinaryName:  "mkcert",
	}
}

// Mkcert wraps mkcert operations
type Mkcert struct {
	binaryPath string
}

// NewMkcert creates a new mkcert wrapper with the given binary path
func NewMkcert(binaryPath string) *Mkcert {
	return &Mkcert{binaryPath: binaryPath}
}

// GetCARoot returns the mkcert CA directory (runs `mkcert -CAROOT`)
func (m *Mkcert) GetCARoot(ctx context.Context) (string, error) {
	output, err := RunTool(ctx, m.binaryPath, "-CAROOT")
	if err != nil {
		return "", fmt.Errorf("failed to get CA root: %w", err)
	}
	return output, nil
}

// IsCAInitialized checks if rootCA.pem exists in CAROOT
func (m *Mkcert) IsCAInitialized(ctx context.Context) (bool, error) {
	caRoot, err := m.GetCARoot(ctx)
	if err != nil {
		return false, err
	}

	rootCAPem := filepath.Join(caRoot, "rootCA.pem")
	if _, err := os.Stat(rootCAPem); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to check CA file: %w", err)
	}

	return true, nil
}

// InstallCA runs `mkcert -install` to install the CA into the system trust store
// This is idempotent - safe to run multiple times
// On macOS: triggers a GUI password dialog
// On Linux: may fail without sudo
func (m *Mkcert) InstallCA(ctx context.Context) error {
	_, err := RunTool(ctx, m.binaryPath, "-install")
	if err != nil {
		return fmt.Errorf("failed to install CA: %w", err)
	}
	return nil
}

// GenerateCert generates a certificate for the given domains
// certPath and keyPath are the output file paths
func (m *Mkcert) GenerateCert(ctx context.Context, certPath, keyPath string, domains ...string) error {
	if len(domains) == 0 {
		return fmt.Errorf("at least one domain is required")
	}

	// Ensure the output directory exists
	certDir := filepath.Dir(certPath)
	if err := os.MkdirAll(certDir, 0755); err != nil {
		return fmt.Errorf("failed to create certificate directory: %w", err)
	}

	// Build arguments: -cert-file <cert> -key-file <key> <domains...>
	args := []string{
		"-cert-file", certPath,
		"-key-file", keyPath,
	}
	args = append(args, domains...)

	_, err := RunTool(ctx, m.binaryPath, args...)
	if err != nil {
		return fmt.Errorf("failed to generate certificate: %w", err)
	}

	return nil
}

// IsCATrusted checks if the mkcert CA is trusted by the system.
// It does this by verifying the certificate at certPath against the system trust store.
func (m *Mkcert) IsCATrusted(ctx context.Context, certPath string) (bool, error) {
	if runtime.GOOS == "darwin" {
		_, err := RunTool(ctx, "security", "verify-cert", "-c", certPath)
		if err != nil {
			return false, nil
		}
		return true, nil
	}

	// On Linux, use openssl verify against system trust store
	_, err := RunTool(ctx, "openssl", "verify", certPath)
	if err != nil {
		return false, nil
	}
	return true, nil
}
