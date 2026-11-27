package agent

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// renovateToolPin is what Renovate's regex manager matches in
// sandboxtools.go: the "renovate:" comment directly above an entry whose
// first line holds Name and Version. Keep it in sync with renovate.json.
var renovateToolPin = regexp.MustCompile(`// renovate: datasource=(github-releases|gitlab-releases) depName=\S+ extractVersion=\S+\n\s*\{Name: "(\w+)", Version: "([^"]+)",`)

var semver = regexp.MustCompile(`^\d+\.\d+(\.\d+)?$`)
var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

func TestSandboxToolsPinned(t *testing.T) {
	src, err := os.ReadFile("sandboxtools.go")
	if err != nil {
		t.Fatal(err)
	}
	pinned := map[string]string{}
	for _, m := range renovateToolPin.FindAllStringSubmatch(string(src), -1) {
		pinned[m[2]] = m[3]
	}
	for _, tool := range SandboxTools {
		if v, ok := pinned[tool.Name]; !ok || v != tool.Version {
			t.Errorf("%s: no \"// renovate: datasource=... depName=... extractVersion=...\" line directly above `{Name: %q, Version: %q,`", tool.Name, tool.Name, tool.Version)
		}
		if !semver.MatchString(tool.Version) {
			t.Errorf("%s: version %q is not a plain version (Renovate's extractVersion strips the tag prefix)", tool.Name, tool.Version)
		}
		if !strings.Contains(tool.URL, "{version}") || !strings.Contains(tool.URL, "{arch}") {
			t.Errorf("%s: URL must take {version} and {arch}, or a version bump leaves it behind: %s", tool.Name, tool.URL)
		}
		for _, arch := range []string{"amd64", "arm64"} {
			if !sha256Hex.MatchString(tool.SHA256[arch]) {
				t.Errorf("%s: no SHA-256 for %s", tool.Name, arch)
			}
		}
	}
}

func TestSandboxToolsSpec(t *testing.T) {
	spec, err := sandboxToolsSpec(SandboxTools, "arm64")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(spec), "\n")
	if len(lines) != len(SandboxTools) {
		t.Fatalf("want one line per tool, got:\n%s", spec)
	}
	for i, line := range lines {
		f := strings.Fields(line)
		if len(f) != 4 {
			t.Fatalf("line %q: want name, sha256, url, path", line)
		}
		tool := SandboxTools[i]
		if f[0] != tool.Name || f[1] != tool.SHA256["arm64"] || strings.Contains(line, "{") {
			t.Errorf("line %q for %s", line, tool.Name)
		}
		if (tool.Path == "") != (f[3] == "-") {
			t.Errorf("line %q: path field", line)
		}
	}
	if !strings.Contains(spec, "gh_"+SandboxTools[1].Version+"_linux_arm64/bin/gh") {
		t.Errorf("gh's archive path not expanded:\n%s", spec)
	}

	if _, err := sandboxToolsSpec(SandboxTools, "riscv64"); err == nil {
		t.Error("an architecture without pinned checksums was accepted")
	}
}

func TestSandboxToolsVolumeNameFollowsPins(t *testing.T) {
	a, err := SandboxToolsVolumeName(SandboxTools, "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(a, sandboxToolsVolumePrefix) || !strings.HasSuffix(a, "-amd64") {
		t.Errorf("volume name %q", a)
	}
	if b, _ := SandboxToolsVolumeName(SandboxTools, "arm64"); b == a {
		t.Error("both architectures share a volume")
	}
	bumped := append([]SandboxTool(nil), SandboxTools...)
	bumped[0].Version = "9.9.9"
	if c, _ := SandboxToolsVolumeName(bumped, "amd64"); c == a {
		t.Error("a version bump keeps the old volume, which holds the old binaries")
	}
}
