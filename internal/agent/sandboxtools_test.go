package agent

import (
	"strings"
	"testing"

	"github.com/0ploy/zdev/internal/config"
)

func TestSandboxToolsSpec(t *testing.T) {
	spec, err := sandboxToolsSpec(config.SandboxTools, "arm64")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(spec), "\n")
	if len(lines) != len(config.SandboxTools) {
		t.Fatalf("want one line per tool, got:\n%s", spec)
	}
	for i, line := range lines {
		f := strings.Fields(line)
		if len(f) != 4 {
			t.Fatalf("line %q: want name, sha256, url, path", line)
		}
		tool := config.SandboxTools[i]
		if f[0] != tool.Name || f[1] != tool.Checksum("linux", "arm64") || strings.Contains(line, "{") {
			t.Errorf("line %q for %s", line, tool.Name)
		}
		if (tool.ArchivePath == "") != (f[3] == "-") {
			t.Errorf("line %q: path field", line)
		}
	}
	if !strings.Contains(spec, "gh_"+config.Gh.Version+"_linux_arm64/bin/gh") {
		t.Errorf("gh's archive path not expanded:\n%s", spec)
	}

	if _, err := sandboxToolsSpec(config.SandboxTools, "riscv64"); err == nil {
		t.Error("an architecture without pinned checksums was accepted")
	}
}

func TestSandboxToolsVolumeNameFollowsPins(t *testing.T) {
	a, err := SandboxToolsVolumeName(config.SandboxTools, "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(a, sandboxToolsVolumePrefix) || !strings.HasSuffix(a, "-amd64") {
		t.Errorf("volume name %q", a)
	}
	if b, _ := SandboxToolsVolumeName(config.SandboxTools, "arm64"); b == a {
		t.Error("both architectures share a volume")
	}
	bumped := append([]config.ToolRelease(nil), config.SandboxTools...)
	bumped[0].Version = "9.9.9"
	if c, _ := SandboxToolsVolumeName(bumped, "amd64"); c == a {
		t.Error("a version bump keeps the old volume, which holds the old binaries")
	}
}
