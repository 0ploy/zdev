package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureDNSConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ZDEV_HOME", home)

	path, err := EnsureDNSConfig("example.dev")
	if err != nil {
		t.Fatalf("EnsureDNSConfig: %v", err)
	}

	wantPath := filepath.Join(home, "dnsmasq", "dnsmasq.conf")
	if path != wantPath {
		t.Errorf("path = %q, want %q", path, wantPath)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	content := string(data)

	// Authoritative wildcard for the domain, no upstream.
	for _, want := range []string{
		"address=/example.dev/127.0.0.1",
		"no-resolv",
		"no-hosts",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("config missing %q; got:\n%s", want, content)
		}
	}
}

func TestEnsureDNSConfig_RewritesDomain(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ZDEV_HOME", home)

	if _, err := EnsureDNSConfig("first.dev"); err != nil {
		t.Fatalf("first EnsureDNSConfig: %v", err)
	}
	path, err := EnsureDNSConfig("second.dev")
	if err != nil {
		t.Fatalf("second EnsureDNSConfig: %v", err)
	}

	data, _ := os.ReadFile(path)
	content := string(data)
	if strings.Contains(content, "first.dev") {
		t.Errorf("stale domain still present:\n%s", content)
	}
	if !strings.Contains(content, "address=/second.dev/127.0.0.1") {
		t.Errorf("new domain missing:\n%s", content)
	}
}
