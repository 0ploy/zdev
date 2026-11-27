package agent

// Static third-party CLIs added to every agent container, for images that
// lack them: jq (status line and hook scripts in the developer's Claude
// settings read their JSON input with it) and gh/glab (the GitHub and GitLab
// credentials the egress proxy swaps in are for them). They sit in their own
// read-only volume at /opt/zdev/tools, last on PATH, so an image's own copy
// wins. The volume is named by a hash of the pins, so a dev build of zdev
// reuses it and a changed pin gets a fresh one.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
)

// SandboxToolsMount is where the volume is mounted in agent containers.
const SandboxToolsMount = "/opt/zdev/tools"

const sandboxToolsVolumePrefix = "zdev-sandbox-tools-"

// SandboxTool is one pinned binary. URL and Path take {version} and {arch}
// (amd64 or arm64, the names all three projects use).
type SandboxTool struct {
	Name    string
	Version string
	URL     string
	Path    string            // the binary inside a .tar.gz download; "" for a plain binary
	SHA256  map[string]string // arch -> sha256 of the download
}

// SandboxTools are the pins. Each "renovate:" comment sits on the line
// directly above its entry, whose first line holds Name and Version, for
// Renovate's regex manager (TestSandboxToolsPinned keeps that shape). A
// version bump needs new SHA256 values from the release's checksum file.
var SandboxTools = []SandboxTool{
	// renovate: datasource=github-releases depName=jqlang/jq extractVersion=^jq-(?<version>.+)$
	{Name: "jq", Version: "1.8.2",
		URL: "https://github.com/jqlang/jq/releases/download/jq-{version}/jq-linux-{arch}",
		SHA256: map[string]string{
			"amd64": "b1c22172dd303f3be49e935aa56aa48a8b7a46e0bc838b4997d3bb451495870f",
			"arm64": "8b85c817833814ddca00a144c33705546355afccf0cf39b188f3cdb48b852309",
		}},
	// renovate: datasource=github-releases depName=cli/cli extractVersion=^v(?<version>.+)$
	{Name: "gh", Version: "2.106.0",
		URL:  "https://github.com/cli/cli/releases/download/v{version}/gh_{version}_linux_{arch}.tar.gz",
		Path: "gh_{version}_linux_{arch}/bin/gh",
		SHA256: map[string]string{
			"amd64": "a476449fa9625413c54a3734eded62e9e480e49f421ff08e7f3b3879c059a033",
			"arm64": "5e8a0c51e1a8345f36bd0b8bb88604b6a5388df16f3ab3e3b9b6d794fa223a67",
		}},
	// renovate: datasource=gitlab-releases depName=gitlab-org/cli extractVersion=^v(?<version>.+)$
	{Name: "glab", Version: "1.133.0",
		URL:  "https://gitlab.com/gitlab-org/cli/-/releases/v{version}/downloads/glab_{version}_linux_{arch}.tar.gz",
		Path: "bin/glab",
		SHA256: map[string]string{
			"amd64": "b17f858b8d700f1607f8ec02fe092a455da6a8dd18dac3eb6ccb09db1befd4bf",
			"arm64": "a529fd254824b0f3f8d2350080c9b46633b67ff3e2b11455836d7a680c498b09",
		}},
}

func (t SandboxTool) expand(s, arch string) string {
	return strings.NewReplacer("{version}", t.Version, "{arch}", arch).Replace(s)
}

// sandboxToolsSpec is one line per tool for fillSandboxTools:
// name, sha256, URL, path in the archive ("-" for a plain binary).
func sandboxToolsSpec(tools []SandboxTool, arch string) (string, error) {
	var b strings.Builder
	for _, t := range tools {
		sum := t.SHA256[arch]
		if sum == "" {
			return "", fmt.Errorf("%s %s has no pinned checksum for %s", t.Name, t.Version, arch)
		}
		path := "-"
		if t.Path != "" {
			path = t.expand(t.Path, arch)
		}
		fmt.Fprintf(&b, "%s %s %s %s\n", t.Name, sum, t.expand(t.URL, arch), path)
	}
	return b.String(), nil
}

// SandboxToolsVolumeName names the volume by a hash of what it holds.
func SandboxToolsVolumeName(tools []SandboxTool, arch string) (string, error) {
	spec, err := sandboxToolsSpec(tools, arch)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256([]byte(spec))
	return fmt.Sprintf("%s%s-%s", sandboxToolsVolumePrefix, hex.EncodeToString(h[:])[:12], arch), nil
}

// fillSandboxTools downloads each tool, checks its SHA-256, unpacks it under
// a temp name that can't collide with an archive path, runs its --version
// (the binaries are static, so Alpine runs them) and moves it into place
// (two fills at once do no harm).
const fillSandboxTools = `set -eu
[ -f .ok ] && exit 0
apk add -q --no-cache curl >/dev/null
while read -r name sum url path; do
  [ -n "$name" ] || continue
  t=$(mktemp ./dl.XXXXXX); x=$(mktemp -d ./x.XXXXXX)
  curl -fsSL --retry 3 -o "$t" "$url"
  echo "$sum  $t" | sha256sum -c - >/dev/null || { echo "$url: wrong checksum" >&2; rm -rf "$t" "$x"; exit 1; }
  if [ "$path" = "-" ]; then mv "$t" "$x/.tool"; else tar xzf "$t" -C "$x" "$path" && mv "$x/$path" "$x/.tool"; fi
  chown 0:0 "$x/.tool" && chmod 755 "$x/.tool"
  [ -f "$x/.tool" ] && "$x/.tool" --version >/dev/null || { echo "$name: the unpacked binary doesn't run" >&2; rm -rf "$t" "$x"; exit 1; }
  mv "$x/.tool" "$name"
  rm -rf "$t" "$x"
done <<EOF
$TOOLS
EOF
touch .ok`

// EnsureSandboxTools fills the volume with SandboxTools for arch when it
// isn't complete yet and returns its name.
func EnsureSandboxTools(ctx context.Context, d Docker, arch string) (string, error) {
	vol, err := SandboxToolsVolumeName(SandboxTools, arch)
	if err != nil {
		return "", err
	}
	if complete(ctx, d, vol) {
		return vol, nil
	}
	spec, err := sandboxToolsSpec(SandboxTools, arch)
	if err != nil {
		return "", err
	}
	if err := d.CreateVolume(ctx, vol, map[string]string{volumeLabel: "sandbox-tools"}); err != nil {
		return "", err
	}
	if err := d.Stream(ctx, nil, nil, os.Stderr, "run", "--rm", "--user", "0", "--workdir", "/v",
		"--mount", "type=volume,src="+vol+",dst=/v", "-e", "TOOLS="+spec,
		"--entrypoint", "sh", FillImage, "-c", fillSandboxTools); err != nil {
		return "", fmt.Errorf("getting jq, gh and glab for the agent container failed: %w", err)
	}
	return vol, nil
}
