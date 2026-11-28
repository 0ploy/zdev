package config

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The two shapes Renovate's regex managers match in defaults.go. Keep them
// in sync with renovate.json.
//
// renovatePin: a "renovate: datasource=docker" comment directly above
// `Name = "repo:tag"`.
var renovatePin = regexp.MustCompile(`// renovate: datasource=docker\n\s*(\w+Image) = "([^":@]+(?::\d+)?[^":@]*):([^"@]+)"`)

// renovateToolPin: a "renovate:" comment with datasource, depName and
// extractVersion directly above `X = ToolRelease{Name: "x", Version: "v",`.
var renovateToolPin = regexp.MustCompile(`// renovate: datasource=(github-releases|gitlab-releases) depName=\S+ extractVersion=\S+\n\s*(\w+) = ToolRelease\{Name: "[^"]+", Version: "([^"]+)",`)

// fullVersion is a tag Renovate can compare and that never moves: v1.2.3,
// 1.2.3 or 2.93 (dnsmasq has two parts), optionally with -variant suffixes
// (node's -bookworm-slim).
var fullVersion = regexp.MustCompile(`^v?\d+\.\d+(\.\d+)?(-[a-z]+)*$`)

func TestDefaultImagesPinned(t *testing.T) {
	src, err := os.ReadFile("defaults.go")
	if err != nil {
		t.Fatal(err)
	}
	pinned := map[string]string{}
	for _, m := range renovatePin.FindAllStringSubmatch(string(src), -1) {
		pinned[m[1]] = m[3]
	}

	for name, image := range map[string]string{
		"RouterImage":        RouterImage,
		"MailImage":          MailImage,
		"DBUIImage":          DBUIImage,
		"RedisInsightsImage": RedisInsightsImage,
		"LogsImage":          LogsImage,
		"DNSImage":           DNSImage,
		"AlpineImage":        AlpineImage,
		"PiBuildImage":       PiBuildImage,
	} {
		tag, ok := pinned[name]
		if !ok {
			t.Errorf("%s (%s) lacks the \"// renovate: datasource=docker\" line directly above it", name, image)
			continue
		}
		if !fullVersion.MatchString(tag) {
			t.Errorf("%s = %s: pin a full version, not a floating tag (zdev never pulls an image it already has)", name, image)
		}
		if !strings.HasSuffix(image, ":"+tag) {
			t.Errorf("%s = %s: matched tag %q differs", name, image, tag)
		}
	}
}

func TestToolsPinned(t *testing.T) {
	src, err := os.ReadFile("defaults.go")
	if err != nil {
		t.Fatal(err)
	}
	pinned := map[string]string{}
	for _, m := range renovateToolPin.FindAllStringSubmatch(string(src), -1) {
		pinned[m[2]] = m[3]
	}
	plainVersion := regexp.MustCompile(`^\d+\.\d+(\.\d+)?$`)
	for name, r := range map[string]ToolRelease{
		"Mkcert": Mkcert, "Just": Just, "Mutagen": Mutagen, "Jq": Jq, "Gh": Gh, "Glab": Glab, "Fd": Fd, "Rg": Rg, "Bun": Bun,
	} {
		if v, ok := pinned[name]; !ok || v != r.Version {
			t.Errorf("%s: no \"// renovate: datasource=... depName=... extractVersion=...\" line directly above `%s = ToolRelease{Name: %q, Version: %q,`", name, name, r.Name, r.Version)
		}
		if !plainVersion.MatchString(r.Version) {
			t.Errorf("%s: version %q is not a plain version (extractVersion strips the tag's prefix)", name, r.Version)
		}
		if !strings.Contains(r.URL, "{version}") || !strings.Contains(r.URL, "{arch}") {
			t.Errorf("%s: URL must take {version} and {arch}, or a bump leaves it behind: %s", name, r.URL)
		}
		if len(r.SHA256) == 0 {
			t.Errorf("%s: no checksums", name)
		}
		for platform, sum := range r.SHA256 {
			if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(sum) {
				t.Errorf("%s: SHA-256 for %s is not 64 hex digits", name, platform)
			}
		}
	}
	for _, r := range SandboxTools {
		for _, arch := range []string{"amd64", "arm64"} {
			if r.Checksum("linux", arch) == "" {
				t.Errorf("sandbox tool %s: no checksum for linux/%s", r.Name, arch)
			}
		}
	}
}

// Every "renovate:" marker in the repository is in defaults.go and matches
// one of the two shapes; anything else Renovate would silently skip.
func TestRenovateMarkersOnlyInDefaults(t *testing.T) {
	src, err := os.ReadFile("defaults.go")
	if err != nil {
		t.Fatal(err)
	}
	markers := strings.Count(string(src), "// renovate:")
	matched := len(renovatePin.FindAllString(string(src), -1)) + len(renovateToolPin.FindAllString(string(src), -1))
	if markers != matched {
		t.Errorf("defaults.go has %d \"// renovate:\" markers, but only %d match a known shape", markers, matched)
	}

	root := filepath.Join("..", "..")
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "spike", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || filepath.Base(path) == "defaults.go" {
			return nil
		}
		if b, err := os.ReadFile(path); err == nil && strings.Contains(string(b), "// renovate:") {
			t.Errorf("%s has a \"// renovate:\" marker: pins belong in internal/config/defaults.go", path)
		}
		return nil
	})
}
