//go:build linux

package resolver

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// dropInPath is the systemd-resolved drop-in that configures split DNS for
// the domain. One file per domain keeps enable/disable self-contained.
func dropInPath(domain string) string {
	return filepath.Join("/etc/systemd/resolved.conf.d", "zdev-"+domain+".conf")
}

// dropInContent routes <domain> to the local resolver. Domains=~<domain>
// is a routing-only domain: systemd-resolved sends names under it to the
// DNS server in this scope (127.0.0.1:port) while everything else keeps
// using the link's normal DNS (its default ~. catch-all route). no-resolv
// on the dnsmasq side is therefore safe - unrelated queries never arrive.
func dropInContent(domain string, port int) string {
	return fmt.Sprintf("# %s - local DNS fallback.\n[Resolve]\nDNS=127.0.0.1:%d\nDomains=~%s\n", managedMarker, port, domain)
}

// dropInGlob matches every zdev-managed resolved.conf drop-in, regardless of
// which domain it was written for.
const dropInGlob = "/etc/systemd/resolved.conf.d/zdev-*.conf"

// systemdResolvedActive reports whether systemd-resolved is the active
// resolver. Split DNS via a drop-in only works when it is.
func systemdResolvedActive() bool {
	if err := exec.Command("systemctl", "is-active", "--quiet", "systemd-resolved").Run(); err == nil {
		return true
	}
	// Fall back to the stub file check for setups where systemctl is not
	// on PATH but resolved is running.
	if _, err := os.Stat("/run/systemd/resolve/stub-resolv.conf"); err == nil {
		return true
	}
	return false
}

// Supported reports whether zdev can configure split DNS here. On Linux
// that requires systemd-resolved; other resolvers (plain resolvconf,
// NetworkManager+dnsmasq) have no per-domain mechanism zdev drives, so the
// caller should fall back to printing manual instructions.
func Supported() (bool, string) {
	if systemdResolvedActive() {
		return true, ""
	}
	return false, "systemd-resolved is not active; zdev cannot configure split DNS automatically on this system"
}

// IsInstalled reports whether the drop-in for domain exists.
func IsInstalled(domain string) (bool, error) {
	if err := validateDomain(domain); err != nil {
		return false, err
	}
	_, err := os.Stat(dropInPath(domain))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// Install writes the systemd-resolved drop-in and restarts the service so
// *.<domain> resolves via the local DNS fallback at 127.0.0.1:port.
// Requires sudo (one prompt).
func Install(domain string, port int) error {
	if err := validateDomain(domain); err != nil {
		return err
	}
	if ok, reason := Supported(); !ok {
		return fmt.Errorf("%s", reason)
	}
	dst := dropInPath(domain)
	tmp, cleanup, err := writeTempConfig(dropInContent(domain, port))
	if err != nil {
		return err
	}
	defer cleanup()

	explain := fmt.Sprintf("zdev needs sudo to route *.%s to the local DNS fallback by writing %s", domain, dst)
	script := fmt.Sprintf("mkdir -p /etc/systemd/resolved.conf.d && cp %q %q && chmod 644 %q && systemctl restart systemd-resolved", tmp, dst, dst)
	return runSudoScript(explain, script)
}

// Remove deletes every zdev-managed drop-in and restarts systemd-resolved.
// It removes ALL drop-ins zdev wrote (matched by the zdev- prefix), not just
// the current domain's, so changing the configured domain and then disabling
// still leaves no orphaned route behind. No-op (and no sudo prompt) when
// there is nothing to remove.
func Remove() error {
	matches, err := filepath.Glob(dropInGlob)
	if err != nil {
		return err
	}
	if len(matches) == 0 {
		return nil
	}

	quoted := make([]string, len(matches))
	for i, p := range matches {
		quoted[i] = fmt.Sprintf("%q", p)
	}
	explain := fmt.Sprintf("zdev needs sudo to remove the local DNS fallback route(s): %s", strings.Join(matches, ", "))
	script := "rm -f " + strings.Join(quoted, " ") + " && systemctl restart systemd-resolved"
	return runSudoScript(explain, script)
}
