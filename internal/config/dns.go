package config

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/0ploy/zdev/internal/ui"
)

// DNSCheckResult contains the result of a DNS verification
type DNSCheckResult struct {
	Domain       string
	SystemDNS    []string // IPs resolved via system DNS
	PublicDNS    []string // IPs resolved via 1.1.1.1
	SystemError  error
	PublicError  error
	ResolvesTo127 bool
}

// VerifyDomainDNS checks that the given domain resolves to 127.0.0.1
// It first tries the system DNS, then falls back to Cloudflare's 1.1.1.1
// to help diagnose whether the issue is with the domain or local DNS config
//
// Returns:
// - (result, nil) if domain resolves to 127.0.0.1 via system DNS
// - (result, nil) if domain resolves to 127.0.0.1 via public DNS (with warning printed)
// - (result, error) if domain doesn't resolve to 127.0.0.1 anywhere
func VerifyDomainDNS(domain string) (*DNSCheckResult, error) {
	result := &DNSCheckResult{
		Domain: domain,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Try system DNS first
	systemIPs, systemErr := net.DefaultResolver.LookupHost(ctx, domain)
	result.SystemDNS = systemIPs
	result.SystemError = systemErr

	// Check if system DNS resolved to 127.0.0.1
	if systemErr == nil && containsLoopback(systemIPs) {
		result.ResolvesTo127 = true
		return result, nil
	}

	// System DNS didn't work or didn't resolve to 127.0.0.1
	// Try Cloudflare's public DNS (1.1.1.1) to diagnose the issue
	publicResolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			d := net.Dialer{Timeout: 5 * time.Second}
			return d.DialContext(ctx, "udp", "1.1.1.1:53")
		},
	}

	publicIPs, publicErr := publicResolver.LookupHost(ctx, domain)
	result.PublicDNS = publicIPs
	result.PublicError = publicErr

	// Check if public DNS resolved to 127.0.0.1
	if publicErr == nil && containsLoopback(publicIPs) {
		result.ResolvesTo127 = true
		// Domain is correctly configured, but system DNS isn't working.
		// This is the DNS rebinding-protection signature: the router
		// strips the loopback answer from upstream responses.
		msg := fmt.Sprintf("domain %q is correctly configured (resolves to 127.0.0.1 via public DNS)\n"+
			"but your system DNS cannot resolve it. Your router likely blocks answers\n"+
			"pointing at 127.0.0.1 (DNS rebinding protection).\n\n"+
			"Fix by enabling the local DNS fallback, which routes *.%s through a local\n"+
			"DNS container and bypasses your router for that domain only:\n"+
			"  zdev dns enable\n\n"+
			"Or run 'zdev systemcheck', which offers to set this up for you.",
			domain, domain)
		return result, fmt.Errorf("%s", ui.Color(msg, "red", false))
	}

	// Neither DNS resolved to 127.0.0.1
	if publicErr != nil && systemErr != nil {
		msg := fmt.Sprintf("domain %q could not be resolved.\n"+
			"  System DNS error: %v\n"+
			"  Public DNS error: %v\n\n"+
			"The domain may not exist or DNS servers are unreachable.",
			domain, systemErr, publicErr)
		return result, fmt.Errorf("%s", ui.Color(msg, "red", false))
	}

	// Domain resolves but not to 127.0.0.1
	resolvedIPs := systemIPs
	if len(resolvedIPs) == 0 {
		resolvedIPs = publicIPs
	}

	msg := fmt.Sprintf("domain %q does not resolve to 127.0.0.1\n"+
		"  Resolved to: %v\n\n"+
		"zdev requires the domain to have a wildcard DNS record pointing to 127.0.0.1.\n"+
		"The default domain %q should work out of the box.\n"+
		"If using a custom domain, ensure *.yourdomain.com resolves to 127.0.0.1",
		domain, resolvedIPs, DefaultDomain)
	return result, fmt.Errorf("%s", ui.Color(msg, "red", false))
}

// containsLoopback checks if any of the IPs is a loopback address (127.x.x.x or ::1)
func containsLoopback(ips []string) bool {
	for _, ip := range ips {
		parsed := net.ParseIP(ip)
		if parsed != nil && parsed.IsLoopback() {
			return true
		}
	}
	return false
}
