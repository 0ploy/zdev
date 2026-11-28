package agent

// Static third-party CLIs added to every agent container, for images that
// lack them: jq (status line and hook scripts in the developer's Claude
// settings read their JSON input with it) and gh/glab (the GitHub and GitLab
// credentials the egress proxy swaps in are for them). They sit in their own
// read-only volume at /opt/zdev/tools, last on PATH, so an image's own copy
// wins. The volume is named by a hash of the pins, so a dev build of zdev
// reuses it and a changed pin gets a fresh one. The pins are
// config.SandboxTools in internal/config/defaults.go.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"github.com/0ploy/zdev/internal/config"
)

// SandboxToolsMount is where the volume is mounted in agent containers.
const SandboxToolsMount = "/opt/zdev/tools"

const sandboxToolsVolumePrefix = "zdev-sandbox-tools-"

// sandboxToolsSpec is one line per tool for fillSandboxTools:
// name, sha256, URL, path in the archive ("-" for a plain binary).
func sandboxToolsSpec(tools []config.ToolRelease, arch string) (string, error) {
	var b strings.Builder
	for _, t := range tools {
		sum := t.Checksum("linux", arch)
		if sum == "" {
			return "", fmt.Errorf("%s %s has no pinned checksum for linux/%s", t.Name, t.Version, arch)
		}
		path := "-"
		if t.ArchivePath != "" {
			path = t.BinaryPath("linux", arch)
		}
		fmt.Fprintf(&b, "%s %s %s %s\n", t.Name, sum, t.DownloadURL("linux", arch), path)
	}
	return b.String(), nil
}

// SandboxToolsVolumeName names the volume by a hash of what it holds.
func SandboxToolsVolumeName(tools []config.ToolRelease, arch string) (string, error) {
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
	vol, err := SandboxToolsVolumeName(config.SandboxTools, arch)
	if err != nil {
		return "", err
	}
	if complete(ctx, d, vol) {
		return vol, nil
	}
	spec, err := sandboxToolsSpec(config.SandboxTools, arch)
	if err != nil {
		return "", err
	}
	if err := d.CreateVolume(ctx, vol, map[string]string{volumeLabel: "sandbox-tools"}); err != nil {
		return "", err
	}
	if err := d.Stream(ctx, nil, nil, os.Stderr, "run", "--rm", "--user", "0", "--workdir", "/v",
		"--mount", "type=volume,src="+vol+",dst=/v", "-e", "TOOLS="+spec,
		"--entrypoint", "sh", FillImage, "-c", fillSandboxTools); err != nil {
		return "", fmt.Errorf("getting the sandbox tools (jq, gh, glab, fd, rg) failed: %w", err)
	}
	return vol, nil
}
