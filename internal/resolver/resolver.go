// Package resolver configures the host operating system to route DNS
// queries for a single domain to a local resolver (the zdev DNS fallback
// container at 127.0.0.1:<port>), bypassing the network router entirely.
//
// This is the compatibility path for routers that enforce DNS rebinding
// protection - they strip upstream answers that point at loopback, which
// breaks zdev's wildcard-record trick (*.0ploy.dev -> 127.0.0.1). Split
// DNS sidesteps the router: only the configured domain is redirected, so
// all other name resolution is untouched.
//
//   - macOS: /etc/resolver/<domain> (per-domain, first-class OS support)
//   - Linux: a systemd-resolved routing-domain drop-in
//
// Whether the fallback is active is derived from IsInstalled, not stored in
// config, so it can never drift from what the OS actually resolves. All
// mutating operations require elevated privileges and shell out to sudo.
package resolver

import (
	"errors"
	"fmt"
	"regexp"
)

// ErrUnsupported is returned by the mutating operations on platforms with
// no automated split-DNS mechanism zdev knows how to drive.
var ErrUnsupported = errors.New("automatic host DNS configuration is not supported on this platform")

// domainPattern guards the domain before it is interpolated into file
// paths and sudo shell scripts. Labels are alphanumeric with internal
// hyphens, separated by dots.
var domainPattern = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?)+$`)

// validateDomain rejects anything that isn't a plain hostname, so it is
// safe to interpolate into paths and shell commands run under sudo.
func validateDomain(domain string) error {
	if !domainPattern.MatchString(domain) {
		return fmt.Errorf("invalid domain %q for resolver configuration", domain)
	}
	return nil
}
