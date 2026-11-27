package agent

// The read-only volumes injected into agent containers (PLAN_2 section 2):
// Claude Code per version, libc variant and architecture, and zdev's own
// Linux binary per version. Each is filled once by a one-shot container that
// downloads, checks the SHA-256 and moves the file into place under a name of
// its own, so two fills at once do no harm (Cronyard's fill.sh).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/0ploy/zdev/internal/config"
)

// FillImage runs the one-shot fill containers (curl, zstd, sha256sum).
const FillImage = config.AlpineImage

const (
	volumeLabel        = "zdev.agent"
	claudeVolumePrefix = "zdev-claude-"
	toolsVolumePrefix  = "zdev-tools-"
	// A superseded Claude version is removed this long after a newer one was
	// first used (running sessions finish with it); any unused volume after
	// volumeUnusedTTL.
	supersededGrace = 24 * time.Hour
	volumeUnusedTTL = 14 * 24 * time.Hour
)

// ClaudeVolumeName is the volume holding one Claude build.
func ClaudeVolumeName(version, libc, arch string) string {
	return fmt.Sprintf("%s%s-%s-%s", claudeVolumePrefix, version, libc, arch)
}

// fillClaude downloads the build into the volume when it isn't complete yet.
const fillClaude = `set -eu
[ -f .ok ] && exit 0
apk add -q --no-cache curl zstd >/dev/null
t=$(mktemp ./claude.XXXXXX); trap 'rm -f "$t" "$t.zst"' EXIT
if [ -n "$ZST_SHA256" ]; then
  curl -fsS --retry 3 -o "$t.zst" "$URL.zst"
  echo "$ZST_SHA256  $t.zst" | sha256sum -c - >/dev/null || { echo "$URL.zst: wrong checksum" >&2; exit 1; }
  zstd -q -d -f -o "$t" "$t.zst"
else
  curl -fsS --retry 3 -o "$t" "$URL"
fi
echo "$SHA256  $t" | sha256sum -c - >/dev/null || { echo "$URL: wrong checksum" >&2; exit 1; }
chmod 755 "$t" && mv "$t" claude && echo "$VERSION" > .ok`

// claudeFill returns the volume for this release, libc and architecture and
// the `docker run` arguments (after "run") of the container that fills it.
func claudeFill(rel *ClaudeRelease, libc, arch string) (vol string, args []string, err error) {
	platform := ClaudePlatform(arch, libc)
	build, ok := rel.Platforms[platform]
	if !ok {
		return "", nil, fmt.Errorf("Claude Code %s has no build for %s", rel.Version, platform)
	}
	vol = ClaudeVolumeName(rel.Version, libc, arch)
	return vol, []string{"--rm", "--user", "0", "--workdir", "/v", "--mount", "type=volume,src=" + vol + ",dst=/v",
		"-e", "VERSION=" + rel.Version, "-e", "SHA256=" + build.SHA256, "-e", "ZST_SHA256=" + build.ZstSHA256,
		"-e", "URL=" + ClaudeReleaseURL + "/" + rel.Version + "/" + platform + "/claude",
		"--entrypoint", "sh", FillImage, "-c", fillClaude}, nil
}

// EnsureClaude makes sure the volume for this release, libc and architecture
// is filled and returns its name. progress is called before a download.
func EnsureClaude(ctx context.Context, d Docker, rel *ClaudeRelease, libc, arch string, progress func(string)) (string, error) {
	vol, args, err := claudeFill(rel, libc, arch)
	if err != nil {
		return "", err
	}
	if complete(ctx, d, vol) {
		return vol, nil
	}
	if progress != nil {
		progress(fmt.Sprintf("Downloading Claude Code %s (%s)", rel.Version, ClaudePlatform(arch, libc)))
	}
	if err := d.CreateVolume(ctx, vol, map[string]string{volumeLabel: "claude", "zdev.agent.claude": rel.Version}); err != nil {
		return "", err
	}
	if err := d.Stream(ctx, nil, nil, os.Stderr, append([]string{"run"}, args...)...); err != nil {
		return "", fmt.Errorf("getting Claude Code %s (%s) failed: %w", rel.Version, ClaudePlatform(arch, libc), err)
	}
	return vol, nil
}

// StartClaudeDownload fills the release's volume in a detached container, so
// a session doesn't wait for it and the download finishes even after zdev
// exits; the next session finds the volume complete. Reports whether a
// download started (false: already there, or already being downloaded).
func StartClaudeDownload(ctx context.Context, d Docker, rel *ClaudeRelease, libc, arch string) (bool, error) {
	vol, args, err := claudeFill(rel, libc, arch)
	if err != nil {
		return false, err
	}
	if complete(ctx, d, vol) {
		return false, nil
	}
	if err := d.CreateVolume(ctx, vol, map[string]string{volumeLabel: "claude", "zdev.agent.claude": rel.Version}); err != nil {
		return false, err
	}
	// One fill per volume: a second `docker run --name` fails while one runs.
	if _, err := d.Output(ctx, append([]string{"run", "-d", "--name", "zdev-fill-" + vol, "--label", volumeLabel + "=fill"}, args...)...); err != nil {
		if strings.Contains(err.Error(), "already in use") {
			return false, nil
		}
		return false, fmt.Errorf("starting the download of Claude Code %s: %w", rel.Version, err)
	}
	return true, nil
}

// InstalledClaude returns the newest complete Claude build for libc and
// architecture on this engine ("" when there is none).
func InstalledClaude(ctx context.Context, d Docker, libc, arch string) (version, vol string) {
	out, err := d.Output(ctx, "volume", "ls", "--filter", "label="+volumeLabel+"=claude", "--format", "{{.Name}}")
	if err != nil {
		return "", ""
	}
	suffix := "-" + libc + "-" + arch
	var versions []string
	for _, name := range strings.Fields(out) {
		v, ok := strings.CutPrefix(name, claudeVolumePrefix)
		if v, ok2 := strings.CutSuffix(v, suffix); ok && ok2 && claudeVersionRE.MatchString(v) {
			versions = append(versions, v)
		}
	}
	sort.Slice(versions, func(i, j int) bool { return CompareClaudeVersions(versions[i], versions[j]) > 0 })
	for _, v := range versions {
		if name := ClaudeVolumeName(v, libc, arch); complete(ctx, d, name) {
			return v, name
		}
	}
	return "", ""
}

// InstalledClaudePlatforms lists the libc/architecture pairs that have a
// Claude build on this engine, as "libc arch".
func InstalledClaudePlatforms(ctx context.Context, d Docker) []string {
	out, err := d.Output(ctx, "volume", "ls", "--filter", "label="+volumeLabel+"=claude", "--format", "{{.Name}}")
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var pairs []string
	for _, name := range strings.Fields(out) {
		parts := strings.Split(strings.TrimPrefix(name, claudeVolumePrefix), "-")
		if len(parts) < 3 {
			continue
		}
		pair := parts[len(parts)-2] + " " + parts[len(parts)-1]
		if !seen[pair] {
			seen[pair] = true
			pairs = append(pairs, pair)
		}
	}
	sort.Strings(pairs)
	return pairs
}

// complete reports whether a fill finished (its .ok marker exists).
func complete(ctx context.Context, d Docker, vol string) bool {
	if !d.VolumeExists(ctx, vol) {
		return false
	}
	_, err := d.Output(ctx, "run", "--rm", "--network", "none", "--mount", "type=volume,src="+vol+",dst=/v,readonly",
		"--entrypoint", "test", FillImage, "-f", "/v/.ok")
	return err == nil
}

// ZdevBinary is where the Linux zdev for agent containers comes from: a local
// file (ZDEV_AGENT_LINUX_BINARY, or a dev build), or a release asset.
type ZdevBinary struct {
	Path    string // local file; empty for a release download
	Version string // release tag (vX.Y.Z) when Path is empty
}

// ToolsVolumeName names the volume for a zdev binary: per release version,
// or per content hash of a local build.
func ToolsVolumeName(src ZdevBinary, arch string) (string, error) {
	if src.Path == "" {
		return fmt.Sprintf("%s%s-%s", toolsVolumePrefix, strings.TrimPrefix(src.Version, "v"), arch), nil
	}
	f, err := os.Open(src.Path)
	if err != nil {
		return "", fmt.Errorf("the Linux zdev binary for agent containers: %w", err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%sdev-%s-%s", toolsVolumePrefix, hex.EncodeToString(h.Sum(nil))[:12], arch), nil
}

const fillToolsRelease = `set -eu
[ -f .ok ] && exit 0
apk add -q --no-cache curl >/dev/null
t=$(mktemp ./zdev.XXXXXX); trap 'rm -f "$t" sums' EXIT
curl -fsSL --retry 3 -o sums "$BASE/checksums.txt"
curl -fsSL --retry 3 -o "$t" "$BASE/$ASSET"
want=$(awk -v a="$ASSET" '$2==a{print $1}' sums)
[ -n "$want" ] || { echo "checksums.txt has no $ASSET" >&2; exit 1; }
echo "$want  $t" | sha256sum -c - >/dev/null || { echo "$ASSET: wrong checksum" >&2; exit 1; }
chmod 755 "$t" && mv "$t" zdev && touch .ok`

const fillToolsLocal = `set -eu
[ -f .ok ] && exit 0
cp /src/zdev ./zdev.tmp && chmod 755 ./zdev.tmp && mv ./zdev.tmp zdev && touch .ok`

// EnsureTools fills the tools volume with zdev's Linux binary and returns its name.
func EnsureTools(ctx context.Context, d Docker, src ZdevBinary, arch string) (string, error) {
	vol, err := ToolsVolumeName(src, arch)
	if err != nil {
		return "", err
	}
	if complete(ctx, d, vol) {
		return vol, nil
	}
	if err := d.CreateVolume(ctx, vol, map[string]string{volumeLabel: "tools"}); err != nil {
		return "", err
	}
	args := []string{"run", "--rm", "-i", "--user", "0", "--workdir", "/v",
		"--mount", "type=volume,src=" + vol + ",dst=/v"}
	script := fillToolsLocal
	if src.Path == "" {
		script = fillToolsRelease
		args = append(args, "-e", "BASE=https://github.com/0ploy/zdev/releases/download/"+src.Version,
			"-e", "ASSET=zdev-linux-"+arch)
	} else {
		args = append(args, "--mount", "type=bind,src="+src.Path+",dst=/src/zdev,readonly")
	}
	args = append(args, "--entrypoint", "sh", FillImage, "-s")
	if err := d.Stream(ctx, strings.NewReader(script), nil, os.Stderr, args...); err != nil {
		return "", fmt.Errorf("preparing zdev for the agent container failed: %w", err)
	}
	return vol, nil
}

// volumeUse is when a volume was first and last used, kept in
// ~/.zdev/agent/volumes.json: Docker records neither.
type volumeUse struct {
	First time.Time `json:"first"`
	Last  time.Time `json:"last"`
}

// MarkUsed records the use of volumes and removes superseded or long unused
// agent volumes (best effort: Docker refuses to remove a volume a container
// still mounts, so running sessions keep theirs).
func MarkUsed(ctx context.Context, d Docker, dir string, used ...string) {
	path := filepath.Join(dir, "volumes.json")
	uses := map[string]volumeUse{}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &uses)
	}
	now := time.Now()
	for _, v := range used {
		u := uses[v]
		if u.First.IsZero() {
			u.First = now
		}
		u.Last = now
		uses[v] = u
	}
	for _, v := range sweepable(uses, used, now) {
		if _, err := d.Output(ctx, "volume", "rm", v); err == nil {
			delete(uses, v)
		}
	}
	if b, err := json.MarshalIndent(uses, "", "  "); err == nil {
		_ = os.MkdirAll(dir, 0o700)
		_ = os.WriteFile(path, b, 0o600)
	}
}

// sweepable lists recorded volumes to remove: unused for volumeUnusedTTL, or
// a Claude volume of the same libc and architecture as one in use whose
// version was superseded more than supersededGrace ago.
func sweepable(uses map[string]volumeUse, inUse []string, now time.Time) []string {
	current := map[string]bool{}
	supersededBy := map[string]time.Time{} // "-libc-arch" suffix -> first use of the current version
	for _, v := range inUse {
		current[v] = true
		if strings.HasPrefix(v, claudeVolumePrefix) {
			supersededBy[claudeSuffix(v)] = uses[v].First
		}
	}
	var out []string
	for v, u := range uses {
		if current[v] {
			continue
		}
		if now.Sub(u.Last) > volumeUnusedTTL {
			out = append(out, v)
			continue
		}
		if first, ok := supersededBy[claudeSuffix(v)]; ok && strings.HasPrefix(v, claudeVolumePrefix) && now.Sub(first) > supersededGrace {
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

// claudeSuffix is "-<libc>-<arch>" of a Claude volume name.
func claudeSuffix(v string) string {
	parts := strings.Split(v, "-")
	if len(parts) < 3 {
		return ""
	}
	return "-" + parts[len(parts)-2] + "-" + parts[len(parts)-1]
}
