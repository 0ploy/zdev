package config

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// renovatePin is what Renovate's regex manager matches in defaults.go: a
// "renovate: datasource=docker" comment directly above `Name = "repo:tag"`.
// Keep it in sync with renovate.json.
var renovatePin = regexp.MustCompile(`// renovate: datasource=docker\n\s*(\w+Image) = "([^":@]+(?::\d+)?[^":@]*):([^"@]+)"`)

// fullVersion is a tag Renovate can compare and that never moves: v1.2.3,
// 1.2.3 or 2.93 (dnsmasq has two parts), optionally with a -variant suffix.
var fullVersion = regexp.MustCompile(`^v?\d+\.\d+(\.\d+)?(-[a-z]+)?$`)

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
