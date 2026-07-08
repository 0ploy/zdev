// Package updatecheck implements a once-a-day update check for zdev.
//
// Design:
//   - MaybeAutoUpdate runs on every CLI invocation.
//   - It reads a cached result from ~/.zdev/update-check.json. If a prior
//     install has already landed a newer version at the canonical path, it
//     prints a one-line banner to stderr informing the user that the next
//     invocation will pick it up.
//   - If the cache is older than cacheTTL (or missing), it does a conditional
//     GET (ETag) against the GitHub API synchronously, bounded by apiTimeout.
//     If a newer release exists AND the running binary is in the symlink
//     layout, it downloads the matching asset and atomically replaces
//     ~/.zdev/bin/zdev. No sudo. No re-exec. The current process keeps
//     running its in-memory code; the NEXT zdev invocation transparently
//     uses the new binary via the symlink.
//
// Blocking is deliberate: an earlier fire-and-forget goroutine version got
// killed when main() returned on fast commands (version/list/status), so the
// cache was never written and the install never happened. At most one API hit
// per 24h per machine, and download traffic only when a new release actually
// exists - so the cost is paid at most once a day.
package updatecheck

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/0ploy/zdev/internal/config"
)

const (
	cacheTTL     = 24 * time.Hour
	apiTimeout   = 3 * time.Second  // bound on the synchronous API probe (304 path)
	totalTimeout = 60 * time.Second // ceiling for API + download + verify + install
)

// apiURL is a var (not a const) so tests can point it at an httptest server.
var apiURL = "https://api.github.com/repos/0ploy/zdev/releases/latest"

// canInstallFn is the predicate that decides whether we're in a layout
// where atomic install is safe (symlink layout with canonical target).
// Overridden in tests.
var canInstallFn = defaultCanInstall

type cache struct {
	LastChecked  time.Time `json:"last_checked"`
	ETag         string    `json:"etag,omitempty"`
	LatestTag    string    `json:"latest_tag,omitempty"`
	InstalledTag string    `json:"installed_tag,omitempty"`
}

type release struct {
	TagName string         `json:"tag_name"`
	Assets  []releaseAsset `json:"assets"`
}

type releaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// MaybeAutoUpdate prints a one-line banner to stderr if a prior invocation
// installed a newer version, and performs a synchronous refresh (check +
// download + install) if the cache is stale. Cache-hit path is fast and
// network-free; stale-cache path blocks up to totalTimeout (at most once per
// cacheTTL). All failures are silent.
func MaybeAutoUpdate(currentVersion string) {
	if shouldSkip(currentVersion) {
		return
	}

	path, err := cachePath()
	if err != nil {
		return
	}

	c, _ := loadCache(path) // nil on first run - fine

	if c != nil {
		switch {
		case c.InstalledTag != "" && IsNewer(c.InstalledTag, currentVersion):
			fmt.Fprintf(os.Stderr,
				"\x1b[33m✓ zdev updated to %s; next run will use it.\x1b[0m\n",
				c.InstalledTag)
		case c.LatestTag != "" && IsNewer(c.LatestTag, currentVersion):
			// Seen a newer release but haven't installed it (likely on a
			// legacy layout that needs migration). Fall back to a notify.
			fmt.Fprintf(os.Stderr,
				"\x1b[33m→ zdev %s is available. Run `zdev self-update` to migrate.\x1b[0m\n",
				c.LatestTag)
		}
	}

	if c == nil || time.Since(c.LastChecked) > cacheTTL {
		refreshAndInstall(path, c, currentVersion)
	}
}

// refreshAndInstall performs the conditional GET, writes the cache, and (if
// a newer release exists and the running binary is in canonical layout)
// downloads the asset and installs it. Blocks the caller. The API probe uses
// apiTimeout; if a download is needed, the combined operation is capped at
// totalTimeout.
func refreshAndInstall(path string, prev *cache, currentVersion string) {
	apiCtx, apiCancel := context.WithTimeout(context.Background(), apiTimeout)
	defer apiCancel()

	// Seed next with prev so error and 304 paths preserve ETag/LatestTag/InstalledTag.
	next := cache{LastChecked: time.Now()}
	if prev != nil {
		next.ETag = prev.ETag
		next.LatestTag = prev.LatestTag
		next.InstalledTag = prev.InstalledTag
	}

	rel, etag, err := fetchLatest(apiCtx, prev)
	if err != nil {
		// Rate-limit (403), transient network error, or non-2xx. Still
		// record the attempt so we respect cacheTTL; otherwise every
		// subsequent invocation would retry immediately and, on a shared
		// IP, pile into GitHub's 60-req/hour limit in a death spiral.
		_ = saveCache(path, &next)
		return
	}

	if rel != nil {
		next.ETag = etag
		next.LatestTag = rel.TagName

		if IsNewer(rel.TagName, currentVersion) && canInstallFn() {
			installCtx, installCancel := context.WithTimeout(context.Background(), totalTimeout)
			if err := installAsset(installCtx, rel); err == nil {
				next.InstalledTag = rel.TagName
			}
			installCancel()
		}
	}
	// rel == nil means 304 Not Modified: next already carries prev's ETag/LatestTag.

	_ = saveCache(path, &next)
}

// fetchLatest performs a conditional GET against the GitHub API. Returns
// (nil, "", nil) on 304 Not Modified - caller must fall back to prev values.
func fetchLatest(ctx context.Context, prev *cache) (*release, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if prev != nil && prev.ETag != "" {
		req.Header.Set("If-None-Match", prev.ETag)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusNotModified:
		return nil, "", nil
	case http.StatusOK:
		var rel release
		if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
			return nil, "", err
		}
		return &rel, resp.Header.Get("ETag"), nil
	default:
		return nil, "", fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
}

// installAsset finds the asset matching this OS/arch, downloads it to
// canonical+".update", verifies it against the release's checksums.txt,
// chmods, and atomically renames into place. Overwrites the currently-
// running binary's canonical target; on Unix this is safe because the
// running process has the file open and keeps executing its in-memory copy.
//
// If the release lacks checksums.txt (older releases) the binary is
// rejected - silent installs without integrity checks are not worth the
// supply-chain risk.
func installAsset(ctx context.Context, rel *release) error {
	assetName := "zdev-" + runtime.GOOS + "-" + runtime.GOARCH
	var downloadURL, checksumsURL string
	for _, a := range rel.Assets {
		switch a.Name {
		case assetName:
			downloadURL = a.BrowserDownloadURL
		case ChecksumsAssetName:
			checksumsURL = a.BrowserDownloadURL
		}
	}
	if downloadURL == "" {
		return fmt.Errorf("no asset for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	if checksumsURL == "" {
		return fmt.Errorf("release %s has no %s - refusing to install without integrity check", rel.TagName, ChecksumsAssetName)
	}

	canonical, err := CanonicalPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
		return err
	}

	// Fetch checksums first so we can fail fast if they're missing/malformed
	// before paying the cost of downloading a large binary.
	expectedHex, err := fetchExpectedChecksum(ctx, checksumsURL, assetName)
	if err != nil {
		return err
	}

	tmpPath := canonical + ".update"
	if err := downloadTo(ctx, downloadURL, tmpPath); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := VerifyFile(tmpPath, expectedHex); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, canonical)
}

// fetchExpectedChecksum downloads the release's checksums file and returns
// the sha256 hex string for the given asset name.
func fetchExpectedChecksum(ctx context.Context, checksumsURL, assetName string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, checksumsURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch checksums: status %d", resp.StatusCode)
	}

	sums := ParseChecksums(resp.Body)
	expected, ok := sums[assetName]
	if !ok {
		return "", fmt.Errorf("no checksum for %s in %s", assetName, ChecksumsAssetName)
	}
	return expected, nil
}

func downloadTo(ctx context.Context, url, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned %d", resp.StatusCode)
	}

	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// defaultCanInstall returns true iff the running binary resolves (via
// symlinks) to CanonicalPath. When false, the user is on a legacy plain-file
// install and atomic replacement of canonical would have no effect on
// subsequent invocations - so we skip the install and let the banner prompt
// them to migrate.
func defaultCanInstall() bool {
	canonical, err := CanonicalPath()
	if err != nil {
		return false
	}
	execPath, err := os.Executable()
	if err != nil {
		return false
	}
	realPath, err := filepath.EvalSymlinks(execPath)
	if err != nil {
		return false
	}
	realCanonical, err := filepath.EvalSymlinks(canonical)
	if err != nil {
		return false
	}
	return realPath == realCanonical
}

// CanonicalPath returns the user-owned location where the real zdev binary
// should live. Callers typically pair this with a symlink in a PATH dir.
func CanonicalPath() (string, error) {
	return filepath.Join(config.GetZdevHome(), "bin", "zdev"), nil
}

// shouldSkip returns true for cases where an update check is unwanted:
// dev builds, CI environments, or when the user opts out.
func shouldSkip(v string) bool {
	if v == "" || v == "dev" {
		return true
	}
	if os.Getenv("CI") != "" || os.Getenv("ZDEV_NO_UPDATE_CHECK") != "" {
		return true
	}
	return false
}

func cachePath() (string, error) {
	return filepath.Join(config.GetZdevHome(), "update-check.json"), nil
}

func loadCache(path string) (*cache, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c cache
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func saveCache(path string, c *cache) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
