package ssl

import (
	"context"
	"crypto/x509"
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

// Signer is the subset of mkcert CertManager needs. *tools.Mkcert satisfies
// it; tests substitute an in-process CA so `make test` needs no mkcert binary.
type Signer interface {
	GetCARoot(ctx context.Context) (string, error)
	GenerateCert(ctx context.Context, certPath, keyPath string, domains ...string) error
}

// CertManager handles SSL certificate operations
type CertManager struct {
	certsDir string         // ~/.zdev/certs
	mkcert   Signer         // mkcert wrapper
	roots    *x509.CertPool // mkcert CA, loaded once per manager
}

// NewCertManager creates a new certificate manager
func NewCertManager(mkcertPath string) (*CertManager, error) {
	return NewCertManagerWithSigner(tools.NewMkcert(mkcertPath))
}

// NewCertManagerWithSigner creates a certificate manager with a custom signer.
func NewCertManagerWithSigner(signer Signer) (*CertManager, error) {
	certsDir := config.GetCertsDir()
	if err := os.MkdirAll(certsDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create certs directory: %w", err)
	}
	return &CertManager{certsDir: certsDir, mkcert: signer}, nil
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

	// Keep the trust files containers mount in step with the CA that signs
	// the certs (and the host's public roots). Runs on first setup and on
	// every router start. Best effort: without them only HTTPS calls from
	// containers fail to verify.
	if err := c.SyncCATrust(ctx); err != nil {
		fmt.Printf("Warning: failed to update the CA files for containers: %v\n", err)
	}

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
	cert, _, err := readCert(certPath)
	if err != nil {
		return false, err
	}
	return c.signedByCurrentCA(ctx, cert)
}

// buildDomainList builds the list of domains/SANs for the certificate
func buildDomainList(domain string) []string {
	return []string{
		"*." + domain,        // Wildcard for project domains (e.g., *.0ploy.dev)
		"*.shared." + domain, // Wildcard for shared services (e.g., *.shared.0ploy.dev)
		domain,               // Bare domain
		"localhost",          // Local development
		"127.0.0.1",          // IP-based access
	}
}
