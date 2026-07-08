package ssl

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/tools"
)

const (
	// CertFileName is the default certificate file name
	CertFileName = "cert.pem"
	// KeyFileName is the default private key file name
	KeyFileName = "key.pem"
)

// CertManager handles SSL certificate operations
type CertManager struct {
	certsDir string        // ~/.zdev/certs
	mkcert   *tools.Mkcert // mkcert wrapper
}

// NewCertManager creates a new certificate manager
func NewCertManager(mkcertPath string) (*CertManager, error) {
	certsDir := config.GetCertsDir()
	if err := os.MkdirAll(certsDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create certs directory: %w", err)
	}

	return &CertManager{
		certsDir: certsDir,
		mkcert:   tools.NewMkcert(mkcertPath),
	}, nil
}

// GetCertPaths returns paths to cert and key files
func (c *CertManager) GetCertPaths() (certPath, keyPath string) {
	return filepath.Join(c.certsDir, CertFileName),
		filepath.Join(c.certsDir, KeyFileName)
}

// CertsExist checks if certificates exist
func (c *CertManager) CertsExist() bool {
	certPath, keyPath := c.GetCertPaths()

	if _, err := os.Stat(certPath); err != nil {
		return false
	}
	if _, err := os.Stat(keyPath); err != nil {
		return false
	}

	return true
}

// EnsureCerts generates wildcard certificates if they don't exist or if CA has changed
// Returns (certPath, keyPath, wasGenerated, error)
func (c *CertManager) EnsureCerts(ctx context.Context, domain string) (string, string, bool, error) {
	certPath, keyPath := c.GetCertPaths()

	// Check if certs already exist
	if c.CertsExist() {
		// Verify certs are valid for current CA
		valid, err := c.VerifyCertCA(ctx)
		if err != nil {
			// If we can't verify, assume they're OK
			return certPath, keyPath, false, nil
		}
		if valid {
			return certPath, keyPath, false, nil
		}
		// Certs exist but signed by different CA - need to regenerate
	}

	// Generate certificates with all required SANs
	domains := buildDomainList(domain)

	if err := c.mkcert.GenerateCert(ctx, certPath, keyPath, domains...); err != nil {
		return "", "", false, fmt.Errorf("failed to generate certificates: %w", err)
	}

	return certPath, keyPath, true, nil
}

// VerifyCertCA checks if the certificate was signed by the current mkcert CA
func (c *CertManager) VerifyCertCA(ctx context.Context) (bool, error) {
	certPath, _ := c.GetCertPaths()

	// Get the mkcert CA root directory
	caRoot, err := c.mkcert.GetCARoot(ctx)
	if err != nil {
		return false, err
	}

	caPath := caRoot + "/rootCA.pem"

	// Use openssl to verify the cert against the CA
	_, err = tools.RunTool(ctx, "openssl", "verify", "-CAfile", caPath, certPath)
	if err != nil {
		// Verification failed - cert not signed by current CA
		return false, nil
	}

	return true, nil
}

// buildDomainList builds the list of domains/SANs for the certificate
func buildDomainList(domain string) []string {
	return []string{
		"*." + domain,          // Wildcard for project domains (e.g., *.0ploy.dev)
		"*.shared." + domain,   // Wildcard for shared services (e.g., *.shared.0ploy.dev)
		domain,                 // Bare domain
		"localhost",            // Local development
		"127.0.0.1",            // IP-based access
	}
}
