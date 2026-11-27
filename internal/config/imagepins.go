package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// StaleImagePin is a shared-service image that global-config.yaml sets to
// another tag of zdev's default image.
type StaleImagePin struct {
	Key        string // YAML path in global-config.yaml, e.g. "shared.router.image"
	Configured string
	Default    string
}

// StaleImagePins lists the shared-service images global-config.yaml sets to
// another tag of zdev's default image. zdev up to v0.14 wrote every default
// image into the file it generated, which froze them there: a later default
// never took effect. A different repository (a mirror, a fork) is a deliberate
// choice and is not reported.
func (g *GlobalConfig) StaleImagePins() []StaleImagePin {
	d := newDefaultGlobalConfig()
	images := []struct{ key, configured, def string }{
		{"shared.router.image", g.Shared.Router.Image, d.Shared.Router.Image},
		{"shared.mail.image", g.Shared.Mail.Image, d.Shared.Mail.Image},
		{"shared.db.image", g.Shared.DBUI.Image, d.Shared.DBUI.Image},
		{"shared.redis_insights.image", g.Shared.RedisInsights.Image, d.Shared.RedisInsights.Image},
		{"shared.logs.image", g.Shared.Logs.Image, d.Shared.Logs.Image},
		{"dns.image", g.DNS.Image, d.DNS.Image},
	}
	var stale []StaleImagePin
	for _, img := range images {
		if img.configured != img.def && imageRepository(img.configured) == imageRepository(img.def) {
			stale = append(stale, StaleImagePin{Key: img.key, Configured: img.configured, Default: img.def})
		}
	}
	return stale
}

// StaleImagePinsHint is the notice for StaleImagePins, or "" when there are none.
func (g *GlobalConfig) StaleImagePinsHint() string {
	stale := g.StaleImagePins()
	if len(stale) == 0 {
		return ""
	}
	var b strings.Builder
	path := filepath.Join(getZdevHome(), GlobalConfigFilename)
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(path, home+string(filepath.Separator)) {
		path = "~" + strings.TrimPrefix(path, home)
	}
	fmt.Fprintf(&b, "Warning: %s pins old shared-service images:\n", path)
	for _, s := range stale {
		fmt.Fprintf(&b, "  %s: %s (default: %s)\n", s.Key, s.Configured, s.Default)
	}
	b.WriteString("Delete these lines to follow zdev's defaults.\n")
	return b.String()
}

// imageRepository strips the tag and digest from an image reference.
func imageRepository(ref string) string {
	if i := strings.Index(ref, "@"); i >= 0 {
		ref = ref[:i]
	}
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		ref = ref[:i]
	}
	return ref
}
