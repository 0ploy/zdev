package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadGlobalConfigFrom(t *testing.T, content string) *GlobalConfig {
	t.Helper()
	home := t.TempDir()
	t.Setenv("ZDEV_HOME", home)
	if err := os.WriteFile(filepath.Join(home, GlobalConfigFilename), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadGlobalConfig()
	if err != nil {
		t.Fatalf("LoadGlobalConfig: %v", err)
	}
	return cfg
}

// A generated config must not pin images: a pinned default would freeze
// there and the next release's default would never take effect.
func TestGeneratedGlobalConfigFollowsDefaultImages(t *testing.T) {
	cfg := loadGlobalConfigFrom(t, generateDefaultGlobalConfig())
	want := newDefaultGlobalConfig()
	if cfg.Shared != want.Shared || cfg.DNS != want.DNS {
		t.Errorf("generated config changes the defaults:\n got %+v %+v\nwant %+v %+v", cfg.Shared, cfg.DNS, want.Shared, want.DNS)
	}
	if pins := cfg.StaleImagePins(); len(pins) != 0 {
		t.Errorf("generated config reports stale pins: %+v", pins)
	}
}

func TestStaleImagePins(t *testing.T) {
	cfg := loadGlobalConfigFrom(t, `version: 1
shared:
  router:
    image: traefik:v1.0
  mail:
    image: registry.example.com/mailpit:1.0
  logs:
    image: `+LogsImage+`
dns:
  image: dockurr/dnsmasq@sha256:0123
`)
	pins := cfg.StaleImagePins()
	got := map[string]string{}
	for _, p := range pins {
		got[p.Key] = p.Configured
	}
	if got["shared.router.image"] != "traefik:v1.0" {
		t.Errorf("an older tag of the default router image is not reported: %+v", pins)
	}
	if got["dns.image"] != "dockurr/dnsmasq@sha256:0123" {
		t.Errorf("a digest of the default dns image is not reported: %+v", pins)
	}
	if _, ok := got["shared.mail.image"]; ok {
		t.Errorf("another repository is a deliberate choice, reported anyway: %+v", pins)
	}
	if _, ok := got["shared.logs.image"]; ok {
		t.Errorf("the default image is reported: %+v", pins)
	}
	if len(pins) != 2 {
		t.Errorf("want 2 stale pins, got %+v", pins)
	}

	hint := cfg.StaleImagePinsHint()
	for _, s := range []string{"shared.router.image: traefik:v1.0 (default: " + RouterImage + ")", "Delete these lines", "Warning: "} {
		if !strings.Contains(hint, s) {
			t.Errorf("hint lacks %q:\n%s", s, hint)
		}
	}
}

func TestStaleImagePinsHintEmpty(t *testing.T) {
	cfg := newDefaultGlobalConfig()
	if hint := cfg.StaleImagePinsHint(); hint != "" {
		t.Errorf("defaults produce a hint: %q", hint)
	}
}

func TestImageRepository(t *testing.T) {
	for ref, want := range map[string]string{
		"traefik:v3.7":                    "traefik",
		"traefik":                         "traefik",
		"axllent/mailpit:latest":          "axllent/mailpit",
		"localhost:5000/traefik:v3.7":     "localhost:5000/traefik",
		"localhost:5000/traefik":          "localhost:5000/traefik",
		"traefik@sha256:abc":              "traefik",
		"ghcr.io/org/img:1.2@sha256:abcd": "ghcr.io/org/img",
	} {
		if got := imageRepository(ref); got != want {
			t.Errorf("imageRepository(%q) = %q, want %q", ref, got, want)
		}
	}
}
