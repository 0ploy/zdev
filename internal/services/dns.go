package services

import (
	"context"
	"fmt"

	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/runtime"
)

// DNSContainerName is the name of the local DNS fallback container.
const DNSContainerName = "zdev_dns"

// dnsDomainLabel records the domain the dnsmasq config was generated for.
// The config lives in a bind-mounted file whose HOST PATH is constant, so a
// domain change wouldn't move the config hash on its own; stamping the
// domain as a hashed label makes `startService` recreate the container when
// the domain changes (which is also when EnsureDNSConfig rewrites the file).
const dnsDomainLabel = "zdev.dns-domain"

// DNSServiceConfig holds configuration for the DNS fallback container.
type DNSServiceConfig struct {
	Image      string
	Domain     string
	ConfigPath string // host path to dnsmasq.conf, bind-mounted at /etc/dnsmasq.conf
	Port       int    // loopback host port published (container listens on 53)
}

// DNSContainerConfig builds the container config for the local DNS fallback.
//
// Unlike the web-UI shared services this publishes loopback host ports
// (UDP+TCP) and is NOT routed through Traefik or attached to project
// networks - the host queries it directly. It restarts unless-stopped
// because, once the host resolver points at it, *.<domain> resolution
// depends on it being up.
func DNSContainerConfig(cfg DNSServiceConfig) runtime.ContainerConfig {
	out := runtime.ContainerConfig{
		Name:        DNSContainerName,
		Image:       cfg.Image,
		NetworkName: SharedNetworkName,
		Ports: []string{
			fmt.Sprintf("127.0.0.1:%d:53/udp", cfg.Port),
			fmt.Sprintf("127.0.0.1:%d:53/tcp", cfg.Port),
		},
		Volumes: []runtime.VolumeMount{
			{Source: cfg.ConfigPath, Target: "/etc/dnsmasq.conf", ReadOnly: true},
		},
		RestartPolicy: "unless-stopped",
		Labels: map[string]string{
			"zdev.managed":        "true",
			"zdev.service":        "dns",
			dnsDomainLabel:        cfg.Domain,
			DozzleVisibilityLabel: "true",
			DozzleGroupLabel:      DozzleSharedGroup,
		},
	}
	runtime.StampConfigHash(&out)
	return out
}

// StartDNS starts (or recreates on config drift) the local DNS fallback
// container. Idempotent: safe to call on every project/services start so a
// crashed or removed container is brought back before anything relies on
// *.<domain> resolving.
func (m *Manager) StartDNS(ctx context.Context) error {
	configPath, err := config.EnsureDNSConfig(m.cfg.Domain)
	if err != nil {
		return fmt.Errorf("failed to write dns config: %w", err)
	}

	return m.startService(ctx, DNSContainerName, "DNS", m.cfg.DNS.Image, func() runtime.ContainerConfig {
		return DNSContainerConfig(DNSServiceConfig{
			Image:      m.cfg.DNS.Image,
			Domain:     m.cfg.Domain,
			ConfigPath: configPath,
			Port:       config.DNSResolverPort,
		})
	})
}

// StopDNS stops the local DNS fallback container.
func (m *Manager) StopDNS(ctx context.Context) error {
	return m.stopService(ctx, DNSContainerName, "DNS")
}

// DNSStatus reports whether the local DNS fallback container is running.
func (m *Manager) DNSStatus(ctx context.Context) (*ServiceStatus, error) {
	return m.getServiceStatus(ctx, DNSContainerName, "DNS")
}
