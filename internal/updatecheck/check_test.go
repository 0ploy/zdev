package updatecheck

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestShouldSkip(t *testing.T) {
	// Isolate env so we don't inherit CI=true from the test runner.
	t.Setenv("CI", "")
	t.Setenv("ZDEV_NO_UPDATE_CHECK", "")

	tests := []struct {
		name    string
		version string
		envKey  string
		envVal  string
		want    bool
	}{
		{"empty version", "", "", "", true},
		{"dev build", "dev", "", "", true},
		{"normal version", "v0.5.6", "", "", false},
		{"CI set", "v0.5.6", "CI", "true", true},
		{"opt-out set", "v0.5.6", "ZDEV_NO_UPDATE_CHECK", "1", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envKey != "" {
				t.Setenv(tt.envKey, tt.envVal)
			}
			if got := shouldSkip(tt.version); got != tt.want {
				t.Errorf("shouldSkip(%q) = %v, want %v", tt.version, got, tt.want)
			}
		})
	}
}

func TestCacheLoadSave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.json")

	if c, err := loadCache(path); err == nil || c != nil {
		t.Fatalf("expected error for missing cache, got cache=%v err=%v", c, err)
	}

	want := &cache{
		LastChecked:  time.Now().UTC().Truncate(time.Second),
		ETag:         `W/"abc123"`,
		LatestTag:    "v0.5.7",
		InstalledTag: "v0.5.7",
	}
	if err := saveCache(path, want); err != nil {
		t.Fatalf("saveCache: %v", err)
	}

	got, err := loadCache(path)
	if err != nil {
		t.Fatalf("loadCache: %v", err)
	}
	if got.ETag != want.ETag || got.LatestTag != want.LatestTag || got.InstalledTag != want.InstalledTag {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, want)
	}
	if !got.LastChecked.Equal(want.LastChecked) {
		t.Errorf("LastChecked mismatch: got %v, want %v", got.LastChecked, want.LastChecked)
	}
}

func TestSaveCacheCreatesParentDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "deeper", "cache.json")
	if err := saveCache(path, &cache{LatestTag: "v1.0.0"}); err != nil {
		t.Fatalf("saveCache with missing parents: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected file to exist, stat err: %v", err)
	}
}

func TestCanonicalPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	got, err := CanonicalPath()
	if err != nil {
		t.Fatalf("CanonicalPath: %v", err)
	}
	want := filepath.Join(home, ".zdev", "bin", "zdev")
	if got != want {
		t.Errorf("CanonicalPath() = %q, want %q", got, want)
	}
}

// testEnv points apiURL, HOME, and canInstallFn at a scratch setup and
// restores the originals on cleanup.
type testEnv struct {
	home    string
	apiSrv  *httptest.Server
	dlSrv   *httptest.Server
	binBody []byte
}

func newTestEnv(t *testing.T, tag string, installable bool) *testEnv {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)

	binBody := []byte("#!/bin/sh\necho " + tag + "\n")
	sum := sha256.Sum256(binBody)
	checksumsBody := []byte(fmt.Sprintf("%s  zdev-%s-%s\n", hex.EncodeToString(sum[:]), runtime.GOOS, runtime.GOARCH))

	dlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Checksum file or binary are served from the same server, by path suffix.
		if filepath.Base(r.URL.Path) == ChecksumsAssetName {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(checksumsBody)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(binBody)
	}))
	t.Cleanup(dlSrv.Close)

	assetName := "zdev-" + runtime.GOOS + "-" + runtime.GOARCH
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `W/"`+tag+`"`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w,
			`{"tag_name":%q,"assets":[{"name":%q,"browser_download_url":%q},{"name":%q,"browser_download_url":%q}]}`,
			tag,
			assetName, dlSrv.URL+"/"+assetName,
			ChecksumsAssetName, dlSrv.URL+"/"+ChecksumsAssetName,
		)
	}))
	t.Cleanup(apiSrv.Close)

	prevAPI := apiURL
	apiURL = apiSrv.URL
	t.Cleanup(func() { apiURL = prevAPI })

	prevCan := canInstallFn
	canInstallFn = func() bool { return installable }
	t.Cleanup(func() { canInstallFn = prevCan })

	return &testEnv{home: home, apiSrv: apiSrv, dlSrv: dlSrv, binBody: binBody}
}

// captureStderr redirects os.Stderr for the duration of fn and returns
// everything written to it.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	prev := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = prev }()

	fn()

	w.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read stderr: %v", err)
	}
	return string(out)
}

func TestRefreshAndInstallDownloadsAndInstalls(t *testing.T) {
	env := newTestEnv(t, "v9.9.9", true)

	path := filepath.Join(env.home, "cache.json")
	stderr := captureStderr(t, func() {
		refreshAndInstall(path, nil, "v0.1.0")
	})
	if !strings.Contains(stderr, "v9.9.9 is available") {
		t.Errorf("stderr missing install announcement: %q", stderr)
	}
	if !strings.Contains(stderr, "updated to v9.9.9") {
		t.Errorf("stderr missing success message: %q", stderr)
	}

	c, err := loadCache(path)
	if err != nil {
		t.Fatalf("cache not written: %v", err)
	}
	if c.LatestTag != "v9.9.9" {
		t.Errorf("LatestTag = %q, want v9.9.9", c.LatestTag)
	}
	if c.InstalledTag != "v9.9.9" {
		t.Errorf("InstalledTag = %q, want v9.9.9", c.InstalledTag)
	}

	canonical, _ := CanonicalPath()
	got, err := os.ReadFile(canonical)
	if err != nil {
		t.Fatalf("canonical binary not installed: %v", err)
	}
	if string(got) != string(env.binBody) {
		t.Errorf("installed binary content mismatch")
	}
	info, err := os.Stat(canonical)
	if err != nil {
		t.Fatalf("stat canonical: %v", err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Errorf("installed binary not executable: mode=%v", info.Mode())
	}
}

func TestRefreshAndInstallSkipsWhenNotNewer(t *testing.T) {
	env := newTestEnv(t, "v0.1.0", true)

	path := filepath.Join(env.home, "cache.json")
	stderr := captureStderr(t, func() {
		refreshAndInstall(path, nil, "v0.1.0")
	})
	if stderr != "" {
		t.Errorf("expected silence when not newer, got %q", stderr)
	}

	c, err := loadCache(path)
	if err != nil {
		t.Fatalf("cache not written: %v", err)
	}
	if c.LatestTag != "v0.1.0" {
		t.Errorf("LatestTag = %q", c.LatestTag)
	}
	if c.InstalledTag != "" {
		t.Errorf("InstalledTag = %q, want empty (not newer)", c.InstalledTag)
	}

	canonical, _ := CanonicalPath()
	if _, err := os.Stat(canonical); !os.IsNotExist(err) {
		t.Errorf("canonical binary should not exist when not newer, err=%v", err)
	}
}

func TestRefreshAndInstallSkipsInstallWhenNotInCanonicalLayout(t *testing.T) {
	env := newTestEnv(t, "v9.9.9", false) // canInstallFn returns false

	path := filepath.Join(env.home, "cache.json")
	refreshAndInstall(path, nil, "v0.1.0")

	c, err := loadCache(path)
	if err != nil {
		t.Fatalf("cache not written: %v", err)
	}
	if c.LatestTag != "v9.9.9" {
		t.Errorf("LatestTag = %q, want v9.9.9", c.LatestTag)
	}
	if c.InstalledTag != "" {
		t.Errorf("InstalledTag = %q, want empty (legacy layout)", c.InstalledTag)
	}

	canonical, _ := CanonicalPath()
	if _, err := os.Stat(canonical); !os.IsNotExist(err) {
		t.Errorf("canonical binary should not exist when canInstallFn false")
	}
}

func TestRefreshAndInstall304PreservesPrev(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	var sawIfNoneMatch string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawIfNoneMatch = r.Header.Get("If-None-Match")
		w.WriteHeader(http.StatusNotModified)
	}))
	defer srv.Close()

	prev := apiURL
	apiURL = srv.URL
	defer func() { apiURL = prev }()

	path := filepath.Join(home, "cache.json")
	old := &cache{
		LastChecked:  time.Now().Add(-48 * time.Hour),
		ETag:         `W/"cached"`,
		LatestTag:    "v0.5.7",
		InstalledTag: "v0.5.7",
	}
	refreshAndInstall(path, old, "v0.5.6")

	if sawIfNoneMatch != `W/"cached"` {
		t.Errorf("If-None-Match header = %q, want cached", sawIfNoneMatch)
	}
	got, err := loadCache(path)
	if err != nil {
		t.Fatalf("cache not written: %v", err)
	}
	if got.LatestTag != "v0.5.7" || got.ETag != `W/"cached"` || got.InstalledTag != "v0.5.7" {
		t.Errorf("304 should preserve prev values, got %+v", got)
	}
	if time.Since(got.LastChecked) > time.Minute {
		t.Errorf("LastChecked not refreshed: %v", got.LastChecked)
	}
}

func TestRefreshAndInstallServerErrorStillWritesCache(t *testing.T) {
	// API failures (rate limit, 5xx, transient network) must still bump
	// LastChecked so we respect cacheTTL instead of retrying on every
	// invocation and driving a rate-limit death spiral on shared IPs.
	home := t.TempDir()
	t.Setenv("HOME", home)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden) // mimic GitHub "rate limit exceeded"
	}))
	defer srv.Close()

	prev := apiURL
	apiURL = srv.URL
	defer func() { apiURL = prev }()

	path := filepath.Join(home, "cache.json")
	refreshAndInstall(path, nil, "v0.1.0")

	c, err := loadCache(path)
	if err != nil {
		t.Fatalf("cache not written on API error: %v", err)
	}
	if time.Since(c.LastChecked) > time.Minute {
		t.Errorf("LastChecked not recent: %v", c.LastChecked)
	}
}

func TestRefreshAndInstallErrorPreservesPrev(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	prev := apiURL
	apiURL = srv.URL
	defer func() { apiURL = prev }()

	path := filepath.Join(home, "cache.json")
	old := &cache{
		LastChecked:  time.Now().Add(-48 * time.Hour),
		ETag:         `W/"cached"`,
		LatestTag:    "v0.5.7",
		InstalledTag: "v0.5.7",
	}
	refreshAndInstall(path, old, "v0.5.6")

	got, err := loadCache(path)
	if err != nil {
		t.Fatalf("cache not written: %v", err)
	}
	if got.ETag != `W/"cached"` || got.LatestTag != "v0.5.7" || got.InstalledTag != "v0.5.7" {
		t.Errorf("error path should preserve prev values, got %+v", got)
	}
	if time.Since(got.LastChecked) > time.Minute {
		t.Errorf("LastChecked not refreshed on error: %v", got.LastChecked)
	}
}
