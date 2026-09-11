package runtime

import "testing"

func TestMountSlug(t *testing.T) {
	tests := []struct {
		name          string
		containerPath string
		want          string
	}{
		{"simple path", "/app", "app"},
		{"nested path", "/opt/etc/shopcockpit", "opt-etc-shopcockpit"},
		{"trailing slash", "/app/", "app"},
		{"dots and underscores", "/var/www/html/.env_dir", "var-www-html-env-dir"},
		{"already hyphenated", "/app/wall-art-gift-card", "app-wall-art-gift-card"},
		{"root only", "/", "root"},
		{"no alphanumerics", "/_/-/", "root"},
		{"uppercase folded", "/App/SRC", "app-src"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MountSlug(tt.containerPath); got != tt.want {
				t.Errorf("MountSlug(%q) = %q, want %q", tt.containerPath, got, tt.want)
			}
		})
	}
}

// TestMountSlug_LongPathTruncatesWithHash pins that deep paths stay short
// enough to read while remaining distinct - the plugin directories that
// exposed this bug nest six levels deep.
func TestMountSlug_LongPathTruncatesWithHash(t *testing.T) {
	a := MountSlug("/var/www/html/custom/plugins/wall-art-gift-card-test")
	b := MountSlug("/var/www/html/custom/plugins/wall-art-gift-card-prod")

	if a == b {
		t.Fatalf("distinct paths produced the same slug: %q", a)
	}
	for _, slug := range []string{a, b} {
		if len(slug) > mountSlugMaxLen {
			t.Errorf("slug %q is %d chars, want at most %d", slug, len(slug), mountSlugMaxLen)
		}
		for _, r := range slug {
			isAlnum := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
			if !isAlnum && r != '-' {
				t.Errorf("slug %q contains %q, which Mutagen session names disallow", slug, r)
			}
		}
	}
}

func TestMutagenSyncVolumeNames(t *testing.T) {
	if got, want := MutagenSyncVolumeName("app", "shop"), "sync.app.shop.zdev"; got != want {
		t.Errorf("MutagenSyncVolumeName = %q, want %q", got, want)
	}
	if got, want := MutagenSyncVolumeNameForMount("app", "opt-etc", "shop"), "sync.app-opt-etc.shop.zdev"; got != want {
		t.Errorf("MutagenSyncVolumeNameForMount = %q, want %q", got, want)
	}
}
