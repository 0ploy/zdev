package agent

// Which Claude Code build sandboxes run (PLAN_2 section 2), ported from
// Cronyard's internal/engine/claudecode.go: the channel resolves to a version
// and per-platform SHA-256 from Anthropic's release manifests, at most once a
// day (ClaudeCheckEvery; stable moves about weekly) unless `zdev agent update`
// asks now. zdev runs a new process per command, so the resolved release is
// cached on disk; when the release folder can't be reached, the last one is used.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ClaudeReleaseURL is Anthropic's release folder (claude.ai/install.sh uses it).
const ClaudeReleaseURL = "https://downloads.claude.ai/claude-code-releases"

// ClaudeCheckEvery is how long a resolved release is reused before a session
// start asks the release folder again.
const ClaudeCheckEvery = 24 * time.Hour

var (
	claudeVersionRE = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`)
	sha256HexRE     = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

// ClaudeRelease is one version: per platform the build's SHA-256, and the
// .zst's when there is one.
type ClaudeRelease struct {
	Channel    string                 `json:"channel"`
	Version    string                 `json:"version"`
	Platforms  map[string]ClaudeBuild `json:"platforms"`
	ResolvedAt time.Time              `json:"resolved_at"`
}

// ClaudeBuild is one platform's build of a release.
type ClaudeBuild struct {
	SHA256    string `json:"sha256"`
	ZstSHA256 string `json:"zst_sha256,omitempty"`
}

// IsPinnedClaude reports whether a channel names a fixed version.
func IsPinnedClaude(channel string) bool { return claudeVersionRE.MatchString(channel) }

// CompareClaudeVersions orders two versions (x.y.z, a pre-release suffix
// sorts before the release): -1, 0 or 1.
func CompareClaudeVersions(a, b string) int {
	split := func(v string) ([3]int, string) {
		var n [3]int
		core, pre, _ := strings.Cut(v, "-")
		for i, p := range strings.SplitN(core, ".", 3) {
			fmt.Sscanf(p, "%d", &n[i])
		}
		return n, pre
	}
	an, ap := split(a)
	bn, bp := split(b)
	for i := range an {
		if an[i] != bn[i] {
			if an[i] < bn[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case ap == bp:
		return 0
	case ap == "":
		return 1
	case bp == "":
		return -1
	case ap < bp:
		return -1
	}
	return 1
}

// ClaudePlatform is Anthropic's platform name for a Docker architecture and
// libc: linux-arm64, linux-x64, linux-arm64-musl, linux-x64-musl.
func ClaudePlatform(arch, libc string) string {
	p := "linux-x64"
	if arch == "arm64" {
		p = "linux-arm64"
	}
	if libc == LibcMusl {
		p += "-musl"
	}
	return p
}

// ResolveClaude returns the release for channel (stable, latest or a version),
// from the cache in dir when it is younger than maxAge (0: always ask).
func ResolveClaude(ctx context.Context, dir, channel string, maxAge time.Duration) (*ClaudeRelease, error) {
	if channel == "" {
		channel = "stable"
	}
	// Claude's state lives under <agent dir>/claude/, next to other agents'.
	cachePath := filepath.Join(dir, "claude", "release.json")
	var cached *ClaudeRelease
	if b, err := os.ReadFile(cachePath); err == nil {
		var r ClaudeRelease
		if json.Unmarshal(b, &r) == nil && r.Channel == channel && len(r.Platforms) > 0 {
			cached = &r
		}
	}
	if cached != nil && time.Since(cached.ResolvedAt) < maxAge {
		return cached, nil
	}
	rel, err := resolveClaude(ctx, &http.Client{Timeout: 30 * time.Second}, ClaudeReleaseURL, channel)
	if err != nil {
		if cached != nil {
			return cached, nil
		}
		return nil, fmt.Errorf("which Claude Code to use (%s from %s): %w", channel, ClaudeReleaseURL, err)
	}
	rel.ResolvedAt = time.Now()
	if b, err := json.MarshalIndent(rel, "", "  "); err == nil {
		_ = os.MkdirAll(dir, 0o700)
		_ = os.MkdirAll(filepath.Dir(cachePath), 0o700)
		_ = os.WriteFile(cachePath, b, 0o600)
	}
	return rel, nil
}

func resolveClaude(ctx context.Context, client *http.Client, base, channel string) (*ClaudeRelease, error) {
	get := func(path string, max int64) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/"+path, nil)
		if err != nil {
			return nil, err
		}
		res, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("%s: %s", path, res.Status)
		}
		return io.ReadAll(io.LimitReader(res.Body, max))
	}
	version := channel
	if channel == "stable" || channel == "latest" {
		b, err := get(channel, 64)
		if err != nil {
			return nil, err
		}
		version = strings.TrimSpace(string(b))
	}
	if !claudeVersionRE.MatchString(version) {
		return nil, fmt.Errorf("%q is not a version", version)
	}
	type manifest struct {
		Version   string `json:"version"`
		Platforms map[string]struct {
			Checksum string `json:"checksum"`
		} `json:"platforms"`
	}
	read := func(name string) (*manifest, error) {
		b, err := get(version+"/"+name, 1<<20)
		if err != nil {
			return nil, err
		}
		var m manifest
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, fmt.Errorf("%s: %v", name, err)
		}
		if m.Version != version {
			return nil, fmt.Errorf("%s is for %q, not %s", name, m.Version, version)
		}
		return &m, nil
	}
	m, err := read("manifest.json")
	if err != nil {
		return nil, err
	}
	zst, _ := read("manifest.zst.json") // optional: the plain build otherwise
	rel := &ClaudeRelease{Channel: channel, Version: version, Platforms: map[string]ClaudeBuild{}}
	for _, arch := range []string{"arm64", "amd64"} {
		for _, libc := range []string{LibcGlibc, LibcMusl} {
			p := ClaudePlatform(arch, libc)
			b := ClaudeBuild{SHA256: m.Platforms[p].Checksum}
			if !sha256HexRE.MatchString(b.SHA256) {
				continue
			}
			if zst != nil && sha256HexRE.MatchString(zst.Platforms[p].Checksum) {
				b.ZstSHA256 = zst.Platforms[p].Checksum
			}
			rel.Platforms[p] = b
		}
	}
	if len(rel.Platforms) == 0 {
		return nil, errors.New("manifest.json has no Linux build")
	}
	return rel, nil
}
