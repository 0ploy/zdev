package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/0ploy/zdev/internal/updatecheck"
	"github.com/spf13/cobra"
)

// selfUpdateHTTPTimeout bounds every network call in `zdev self-update`.
// Generous enough for a ~40MB binary on a slow link; short enough that a
// hung TCP connection doesn't strand the user.
const selfUpdateHTTPTimeout = 5 * time.Minute

const selfUpdateGithubRepo = "0ploy/zdev"

// githubRelease is a minimal representation of a GitHub release.
type githubRelease struct {
	TagName string        `json:"tag_name"`
	Assets  []githubAsset `json:"assets"`
}

type githubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

var selfUpdateCmd = &cobra.Command{
	Use:   "self-update",
	Short: "Update zdev to the latest version",
	Long:  "Checks GitHub for the latest release, downloads the matching binary, and replaces the current executable.",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), selfUpdateHTTPTimeout)
		defer cancel()
		return runSelfUpdate(ctx)
	},
}

func init() {
	rootCmd.AddCommand(selfUpdateCmd)
}

func runSelfUpdate(ctx context.Context) error {
	canonical, err := updatecheck.CanonicalPath()
	if err != nil {
		return fmt.Errorf("cannot determine install dir: %w", err)
	}
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot determine executable path: %w", err)
	}
	// Silently migrate legacy installs (plain file at /usr/local/bin/zdev)
	// to the symlink layout so subsequent updates don't need sudo. Only
	// applies when the running executable is what PATH resolves to: a
	// binary run from a build or download dir was never installed, and
	// migrating it would copy a dev build over the canonical install (then
	// exit "already up to date", leaving it there) and turn the local file
	// into a symlink.
	if isPATHInstall(execPath) {
		if err := migrateIfNeeded(execPath, canonical); err != nil {
			return err
		}
	}

	currentVersion := strings.TrimPrefix(Version, "v")

	fmt.Printf("Current version: %s\n", Version)
	fmt.Printf("Checking for updates...\n")

	release, err := fetchLatestRelease(ctx)
	if err != nil {
		return fmt.Errorf("failed to check for updates: %w", err)
	}

	latestVersion := strings.TrimPrefix(release.TagName, "v")
	if latestVersion == currentVersion {
		fmt.Printf("Already up to date (%s)\n", Version)
		return nil
	}

	cmp, err := updatecheck.CompareSemver(currentVersion, latestVersion)
	if err == nil && cmp >= 0 {
		fmt.Printf("Already up to date (%s, latest: %s)\n", Version, release.TagName)
		return nil
	}

	fmt.Printf("New version available: %s\n", release.TagName)

	assetName := selfUpdateBinaryName()
	var downloadURL, checksumsURL string
	for _, asset := range release.Assets {
		switch asset.Name {
		case assetName:
			downloadURL = asset.BrowserDownloadURL
		case updatecheck.ChecksumsAssetName:
			checksumsURL = asset.BrowserDownloadURL
		}
	}
	if downloadURL == "" {
		return fmt.Errorf("no binary found for %s/%s (looked for %s)", runtime.GOOS, runtime.GOARCH, assetName)
	}
	if checksumsURL == "" {
		return fmt.Errorf("release %s has no %s - refusing to install without integrity check", release.TagName, updatecheck.ChecksumsAssetName)
	}

	fmt.Printf("Downloading %s...\n", assetName)

	if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
		return fmt.Errorf("cannot create install dir: %w", err)
	}

	// Fetch checksums up-front so a malformed/missing file aborts before
	// we download the much larger binary.
	expectedHex, err := fetchChecksumEntry(ctx, checksumsURL, assetName)
	if err != nil {
		return fmt.Errorf("checksum fetch failed: %w", err)
	}

	tmpPath := canonical + ".update"
	if err := downloadFile(ctx, downloadURL, tmpPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("download failed: %w", err)
	}
	if err := updatecheck.VerifyFile(tmpPath, expectedHex); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("integrity check failed: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("chmod failed: %w", err)
	}

	// Atomic replace of the canonical binary. Dir is user-owned, no sudo.
	if err := os.Rename(tmpPath, canonical); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("install failed: %w", err)
	}

	fmt.Printf("Updated to %s\n", release.TagName)

	// Re-exec into the new binary to verify it starts and show the user
	// the confirmed version. syscall.Exec replaces the current process,
	// so we don't return on success.
	if err := syscall.Exec(canonical, []string{"zdev", "version"}, os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not exec new binary: %v\n", err)
	}
	return nil
}

// isPATHInstall reports whether execPath is the zdev that PATH resolves to.
// False for a binary run from a build tree or via an explicit path that
// isn't the installed one, and when zdev isn't on PATH at all.
func isPATHInstall(execPath string) bool {
	pathBin, err := exec.LookPath("zdev")
	if err != nil {
		return false
	}
	realExec, err := filepath.EvalSymlinks(execPath)
	if err != nil {
		realExec = execPath
	}
	realPathBin, err := filepath.EvalSymlinks(pathBin)
	if err != nil {
		realPathBin = pathBin
	}
	return realExec == realPathBin
}

// migrateIfNeeded ensures execPath resolves to canonical. If not, it copies
// the current binary to canonical and replaces execPath with a symlink.
// Emits no output on the happy path; a sudo password prompt may appear when
// execPath lives in a root-owned dir.
func migrateIfNeeded(execPath, canonical string) error {
	realPath, err := filepath.EvalSymlinks(execPath)
	if err != nil {
		realPath = execPath
	}
	// Also evaluate canonical so /tmp vs /private/tmp (macOS) doesn't
	// cause a spurious mismatch.
	realCanonical, err := filepath.EvalSymlinks(canonical)
	if err != nil {
		realCanonical = canonical
	}
	if realPath == realCanonical {
		return nil // already migrated
	}

	if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
		return err
	}
	if err := copyFile(realPath, canonical); err != nil {
		return err
	}
	if err := os.Chmod(canonical, 0o755); err != nil {
		return err
	}
	return migrateToSymlink(execPath, canonical)
}

// migrateToSymlink replaces linkPath with a symlink to target. Tries without
// sudo first; falls back to `sudo ln -sfn` if the parent dir isn't writable.
func migrateToSymlink(linkPath, target string) error {
	if existing, err := os.Readlink(linkPath); err == nil && existing == target {
		return nil // already the right symlink
	}

	if err := atomicSymlink(linkPath, target); err == nil {
		return nil
	}

	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "One-time migration: converting zdev to a symlinked layout.")
	fmt.Fprintln(os.Stderr, "Future updates will not require sudo.")
	fmt.Fprintln(os.Stderr)

	cmd := exec.Command("sudo", "ln", "-sfn", target, linkPath)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// copyFile copies src to dst. dst is truncated if it exists.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// atomicSymlink creates or replaces linkPath as a symlink to target using
// create-tmp + rename so there's no window where linkPath is missing.
// Fails if the parent directory isn't writable by the current user.
func atomicSymlink(linkPath, target string) error {
	tmp := linkPath + ".symlink.tmp"
	_ = os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, linkPath); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func fetchLatestRelease(ctx context.Context) (*githubRelease, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", selfUpdateGithubRepo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned %d", resp.StatusCode)
	}

	var release githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}
	return &release, nil
}

func downloadFile(ctx context.Context, url, dest string) error {
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
	defer f.Close()

	_, err = io.Copy(f, resp.Body)
	return err
}

// fetchChecksumEntry downloads the release's checksums.txt and returns the
// sha256 hex for the given asset name.
func fetchChecksumEntry(ctx context.Context, checksumsURL, assetName string) (string, error) {
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

	sums := updatecheck.ParseChecksums(resp.Body)
	expected, ok := sums[assetName]
	if !ok {
		return "", fmt.Errorf("no checksum for %s", assetName)
	}
	return expected, nil
}

func selfUpdateBinaryName() string {
	return "zdev-" + runtime.GOOS + "-" + runtime.GOARCH
}
