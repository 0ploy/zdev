//go:build darwin

package resolver

import (
	"fmt"
	"os"
	"path/filepath"
)

// resolverFilePath is the macOS per-domain resolver file. macOS routes all
// queries for <domain> (and its subdomains) to the nameserver listed here,
// and mDNSResponder picks up changes to /etc/resolver immediately - no
// daemon restart needed.
func resolverFilePath(domain string) string {
	return filepath.Join("/etc/resolver", domain)
}

func resolverFileContent(port int) string {
	return fmt.Sprintf("# Managed by zdev - local DNS fallback.\nnameserver 127.0.0.1\nport %d\n", port)
}

// Supported reports whether zdev can configure split DNS here. macOS always
// supports /etc/resolver.
func Supported() (bool, string) {
	return true, ""
}

// IsInstalled reports whether the resolver file for domain exists.
func IsInstalled(domain string) (bool, error) {
	if err := validateDomain(domain); err != nil {
		return false, err
	}
	_, err := os.Stat(resolverFilePath(domain))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// Install writes /etc/resolver/<domain> so *.<domain> resolves via the
// local DNS fallback at 127.0.0.1:port. Requires sudo (one prompt).
func Install(domain string, port int) error {
	if err := validateDomain(domain); err != nil {
		return err
	}
	dst := resolverFilePath(domain)
	tmp, cleanup, err := writeTempConfig(resolverFileContent(port))
	if err != nil {
		return err
	}
	defer cleanup()

	explain := fmt.Sprintf("zdev needs sudo to route *.%s to the local DNS fallback by writing %s", domain, dst)
	script := fmt.Sprintf("mkdir -p /etc/resolver && cp %q %q && chmod 644 %q", tmp, dst, dst)
	return runSudoScript(explain, script)
}

// Remove deletes the resolver file, reverting to normal DNS for the domain.
func Remove(domain string) error {
	if err := validateDomain(domain); err != nil {
		return err
	}
	dst := resolverFilePath(domain)
	explain := fmt.Sprintf("zdev needs sudo to remove the local DNS fallback route at %s", dst)
	script := fmt.Sprintf("rm -f %q", dst)
	return runSudoScript(explain, script)
}
