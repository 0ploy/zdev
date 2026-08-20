package services

import (
	"testing"

	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/runtime"
)

func TestDNSContainerConfig(t *testing.T) {
	cfg := DNSContainerConfig(DNSServiceConfig{
		Image:      "dockurr/dnsmasq:latest",
		Domain:     "0ploy.dev",
		ConfigPath: "/home/u/.zdev/dnsmasq/dnsmasq.conf",
		Port:       5353,
	})

	if cfg.Name != DNSContainerName {
		t.Errorf("Name = %q, want %q", cfg.Name, DNSContainerName)
	}
	if cfg.RestartPolicy != "unless-stopped" {
		t.Errorf("RestartPolicy = %q, want unless-stopped", cfg.RestartPolicy)
	}

	// Ports are published on loopback only, UDP and TCP.
	wantPorts := map[string]bool{
		"127.0.0.1:5353:53/udp": false,
		"127.0.0.1:5353:53/tcp": false,
	}
	for _, p := range cfg.Ports {
		if _, ok := wantPorts[p]; ok {
			wantPorts[p] = true
		}
	}
	for p, seen := range wantPorts {
		if !seen {
			t.Errorf("missing port mapping %q; got %v", p, cfg.Ports)
		}
	}

	// Config is bind-mounted read-only at the path the image reads.
	if len(cfg.Volumes) != 1 {
		t.Fatalf("Volumes = %v, want 1 mount", cfg.Volumes)
	}
	if cfg.Volumes[0].Target != "/etc/dnsmasq.conf" || !cfg.Volumes[0].ReadOnly {
		t.Errorf("mount = %+v, want /etc/dnsmasq.conf read-only", cfg.Volumes[0])
	}

	if cfg.Labels[dnsDomainLabel] != "0ploy.dev" {
		t.Errorf("%s = %q, want 0ploy.dev", dnsDomainLabel, cfg.Labels[dnsDomainLabel])
	}
	if cfg.Labels[runtime.ConfigHashLabel] == "" {
		t.Error("config hash not stamped")
	}
}

func TestDNSContainerConfig_DomainChangesHash(t *testing.T) {
	base := DNSServiceConfig{Image: "img", ConfigPath: "/x/dnsmasq.conf", Port: 5353}

	a := DNSContainerConfig(withDomain(base, "a.dev"))
	b := DNSContainerConfig(withDomain(base, "b.dev"))

	if a.Labels[runtime.ConfigHashLabel] == b.Labels[runtime.ConfigHashLabel] {
		t.Error("expected different config hash for different domains (recreate on domain change)")
	}
}

func withDomain(c DNSServiceConfig, domain string) DNSServiceConfig {
	c.Domain = domain
	return c
}

func TestDNSContainerConfig_RestartPolicyNotInHost(t *testing.T) {
	// Guard: the restart policy must not leak into non-DNS services' hashes.
	// Confirms adding the field didn't perturb the explicit hash payload.
	mail := MailContainerConfig(MailServiceConfig{Image: "img", Domain: "0ploy.dev"})
	if mail.RestartPolicy != "" {
		t.Errorf("web-UI service unexpectedly has RestartPolicy %q", mail.RestartPolicy)
	}
	if mail.Labels[runtime.ConfigHashLabel] == "" {
		t.Error("mail hash missing")
	}
}

// TestDNSRegistryEntry pins the shape that lets the DNS fallback live in
// AllSharedServices alongside services that look nothing like it: no web
// UI, no project-network attachment, and a host-level Enabled gate. Every
// registry consumer relies on these being consistent.
func TestDNSRegistryEntry(t *testing.T) {
	def, ok := SharedServiceByContainer(DNSContainerName)
	if !ok {
		t.Fatal("DNS is missing from AllSharedServices")
	}

	if def.HasWebUI() {
		t.Error("DNS must not advertise a web UI - it has no Traefik route")
	}
	if def.JoinsProjectNetworks() {
		t.Error("DNS must not join project networks - the host queries it directly")
	}
	if def.Connect != nil || def.Disconnect != nil {
		t.Error("DNS must leave Connect/Disconnect nil")
	}
	if def.Start == nil || def.Stop == nil || def.Status == nil {
		t.Error("DNS must be startable, stoppable, and inspectable via the registry")
	}
	if def.Enabled == nil {
		t.Error("DNS must carry a host-level Enabled gate")
	}
	if !def.ProjectEnabled(&config.ProjectSharedConfig{}) {
		t.Error("DNS has no per-project opt-in: every project needs name resolution")
	}
	if def.Enabled(nil) {
		t.Error("DNS must report disabled for a nil Manager rather than panicking")
	}
}

// TestDNSStartsFirst guards the lifecycle order: nothing on a
// fallback-dependent host resolves until the DNS container is up, so it
// starts before the router (and, via the reverse walk, stops last).
func TestDNSStartsFirst(t *testing.T) {
	registry := AllSharedServices()
	if len(registry) == 0 {
		t.Fatal("empty shared service registry")
	}
	if registry[0].ContainerName != DNSContainerName {
		t.Errorf("expected DNS first in the registry, got %s", registry[0].ContainerName)
	}
}

// TestIsEnabledDefaultsToEnabled: services without an Enabled gate (every
// service but DNS) must be managed unconditionally.
func TestIsEnabledDefaultsToEnabled(t *testing.T) {
	for _, def := range AllSharedServices() {
		if def.ContainerName == DNSContainerName {
			continue
		}
		if def.Enabled != nil {
			t.Errorf("%s: unexpected Enabled gate; only DNS should have one", def.Name)
		}
		if !def.IsEnabled(nil) {
			t.Errorf("%s: ungated service reported disabled", def.Name)
		}
	}
}
