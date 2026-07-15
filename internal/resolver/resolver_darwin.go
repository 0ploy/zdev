//go:build darwin

package resolver

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// resolverFilePath is the macOS per-domain resolver file. macOS routes all
// queries for <domain> (and its subdomains) to the nameserver listed here,
// and mDNSResponder picks up changes to /etc/resolver immediately - no
// daemon restart needed.
func resolverFilePath(domain string) string {
	return filepath.Join("/etc/resolver", domain)
}

func resolverFileContent(port int) string {
	return fmt.Sprintf("# %s - local DNS fallback.\nnameserver 127.0.0.1\nport %d\n", managedMarker, port)
}

const resolverDir = "/etc/resolver"

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

// Remove deletes every zdev-managed resolver file, reverting to normal DNS.
// It removes ALL files zdev wrote (identified by the managed marker), not
// just the current domain's, so changing the configured domain and then
// disabling still leaves no orphaned route behind. No-op (and no sudo
// prompt) when there is nothing to remove; leaves resolver files the user
// wrote by hand untouched.
func Remove() error {
	managed, err := managedResolverFiles()
	if err != nil {
		return err
	}
	if len(managed) == 0 {
		return nil
	}

	quoted := make([]string, len(managed))
	for i, p := range managed {
		quoted[i] = fmt.Sprintf("%q", p)
	}
	explain := fmt.Sprintf("zdev needs sudo to remove the local DNS fallback route(s): %s", strings.Join(managed, ", "))
	script := "rm -f " + strings.Join(quoted, " ")
	return runSudoScript(explain, script)
}

// managedResolverFiles returns the /etc/resolver files zdev created,
// identified by the managed marker in their contents. Files are world
// readable (mode 0644), so no privilege is needed to scan them.
func managedResolverFiles() ([]string, error) {
	entries, err := os.ReadDir(resolverDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var managed []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		p := filepath.Join(resolverDir, e.Name())
		data, err := os.ReadFile(p)
		if err != nil {
			continue // unreadable entry - not ours to reason about
		}
		if strings.Contains(string(data), managedMarker) {
			managed = append(managed, p)
		}
	}
	return managed, nil
}
