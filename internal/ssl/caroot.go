package ssl

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"

	"github.com/0ploy/zdev/internal/config"
)

// File names in config.GetTrustDir(). Public certificates only - the CA key
// never leaves mkcert's CAROOT.
const (
	// CAFileName is the mkcert root CA alone (for NODE_EXTRA_CA_CERTS,
	// which adds to Node's built-in roots).
	CAFileName = "ca.pem"
	// CABundleFileName is the host's public roots plus the mkcert root. It
	// is mounted over the images' system CA files, so it must be a complete
	// trust store - public HTTPS keeps working from containers.
	CABundleFileName = "ca-bundle.pem"
)

// CAPath is zdev's copy of the mkcert root CA.
func CAPath() string { return filepath.Join(config.GetTrustDir(), CAFileName) }

// CABundlePath is the bundle of the host's public roots plus the mkcert root.
func CABundlePath() string { return filepath.Join(config.GetTrustDir(), CABundleFileName) }

// hostCABundlePaths lists where the host keeps its public roots, first match
// wins. macOS: /etc/ssl/cert.pem (the keychain's mkcert root is NOT in it).
// Linux: the same order as Go's crypto/x509.
var hostCABundlePaths = func() []string {
	if goruntime.GOOS == "darwin" {
		return []string{"/etc/ssl/cert.pem"}
	}
	return []string{
		"/etc/ssl/certs/ca-certificates.crt",
		"/etc/pki/tls/certs/ca-bundle.crt",
		"/etc/ssl/ca-bundle.pem",
		"/etc/pki/tls/cacert.pem",
		"/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem",
		"/etc/ssl/cert.pem",
	}
}()

// SyncCATrust writes the trust files containers mount: the mkcert root CA
// and the bundle (host public roots + mkcert root). Without readable host
// roots no bundle is written - a bundle with only the zdev CA would break
// public HTTPS in every container - and containers get only ca.pem.
//
// Writes IN PLACE, never rename-into-place: the bundle is bind-mounted as
// single files over the images' system CA paths, which pins the inode - a
// replaced file would leave running containers on the old content.
func (c *CertManager) SyncCATrust(ctx context.Context) error {
	caRoot, err := c.mkcert.GetCARoot(ctx)
	if err != nil {
		return err
	}
	ca, err := os.ReadFile(filepath.Join(strings.TrimSpace(caRoot), "rootCA.pem"))
	if err != nil {
		return fmt.Errorf("failed to read mkcert CA: %w", err)
	}

	dir := config.GetTrustDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create trust directory: %w", err)
	}
	if err := writeIfChanged(filepath.Join(dir, CAFileName), ca); err != nil {
		return err
	}

	public, err := readHostCABundle()
	if err != nil {
		return err
	}
	return writeIfChanged(filepath.Join(dir, CABundleFileName), buildCABundle(public, ca))
}

// buildCABundle appends the zdev CA to the host's public roots, unless the
// host store already has it (Linux after mkcert -install).
func buildCABundle(public, ca []byte) []byte {
	public = bytes.TrimRight(public, "\n")
	ca = bytes.TrimSpace(ca)
	if bytes.Contains(public, ca) {
		return append(public, '\n')
	}
	out := make([]byte, 0, len(public)+len(ca)+32)
	out = append(out, public...)
	out = append(out, "\n\n# zdev local CA (mkcert)\n"...)
	out = append(out, ca...)
	return append(out, '\n')
}

func readHostCABundle() ([]byte, error) {
	for _, p := range hostCABundlePaths {
		data, err := os.ReadFile(p)
		if err == nil && bytes.Contains(data, []byte("BEGIN CERTIFICATE")) {
			return data, nil
		}
	}
	return nil, fmt.Errorf("no system CA bundle found (looked in %s); containers trust only the zdev CA via NODE_EXTRA_CA_CERTS", strings.Join(hostCABundlePaths, ", "))
}

// writeIfChanged writes data to path in place (truncate + write keeps the
// inode) when the content differs.
func writeIfChanged(path string, data []byte) error {
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, data) {
		return nil
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write %s: %w", path, err)
	}
	return nil
}
