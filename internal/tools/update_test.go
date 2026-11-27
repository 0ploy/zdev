package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/0ploy/zdev/internal/config"
)

// updateFixture serves a tool release printing version, and counts downloads.
type updateFixture struct {
	manager   *Manager
	tool      ToolInfo
	downloads *atomic.Int32
	updated   *string // path BeforeUpdate saw
}

func newUpdateFixture(t *testing.T, version string, serverFails bool) updateFixture {
	t.Helper()
	payload := []byte("#!/bin/sh\necho 'zdevtesttool " + version + "'\n")
	sum := sha256.Sum256(payload)
	var downloads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		downloads.Add(1)
		if serverFails {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		_, _ = w.Write(payload)
	}))
	t.Cleanup(server.Close)
	updated := ""
	return updateFixture{
		manager:   &Manager{binDir: t.TempDir()},
		downloads: &downloads,
		updated:   &updated,
		tool: ToolInfo{
			ToolRelease: config.ToolRelease{Name: "zdevtesttool", Version: version, URL: server.URL,
				SHA256: map[string]string{runtime.GOOS + "/" + runtime.GOARCH: hex.EncodeToString(sum[:])}},
			BinaryName:   "zdevtesttool-update",
			VersionArgs:  []string{"--version"},
			BeforeUpdate: func(_ context.Context, old string) { updated = old },
		},
	}
}

// install puts a copy printing version into binDir, as an older zdev did.
func (f updateFixture) install(t *testing.T, version string) {
	t.Helper()
	if err := os.WriteFile(f.manager.GetToolPath(f.tool), []byte("#!/bin/sh\necho 'zdevtesttool "+version+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func (f updateFixture) installedVersion(t *testing.T) string {
	out, err := RunTool(context.Background(), f.manager.GetToolPath(f.tool), "--version")
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimPrefix(out, "zdevtesttool ")
}

func TestEnsureToolKeepsCurrentPin(t *testing.T) {
	f := newUpdateFixture(t, "2.0.0", false)
	if _, err := f.manager.EnsureTool(context.Background(), f.tool); err != nil {
		t.Fatal(err)
	}
	if _, err := f.manager.EnsureTool(context.Background(), f.tool); err != nil {
		t.Fatal(err)
	}
	if n := f.downloads.Load(); n != 1 {
		t.Errorf("downloads = %d, want 1 (the second call finds the pinned release)", n)
	}
	if *f.updated != "" {
		t.Error("BeforeUpdate ran for a fresh install")
	}
}

func TestEnsureToolReplacesChangedPin(t *testing.T) {
	old := newUpdateFixture(t, "1.0.0", false)
	if _, err := old.manager.EnsureTool(context.Background(), old.tool); err != nil {
		t.Fatal(err)
	}
	f := newUpdateFixture(t, "2.0.0", false)
	f.manager = old.manager // same bin dir, new pin

	path, err := f.manager.EnsureTool(context.Background(), f.tool)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.installedVersion(t); got != "2.0.0" {
		t.Errorf("installed version %q after a pin change, want 2.0.0", got)
	}
	if *f.updated != path {
		t.Errorf("BeforeUpdate saw %q, want the installed binary %q", *f.updated, path)
	}
	if b, _ := os.ReadFile(f.manager.pinPath(f.tool)); !strings.HasPrefix(string(b), "2.0.0 ") {
		t.Errorf("pin file %q not updated", b)
	}
}

func TestEnsureToolAdoptsMatchingCopyWithoutPin(t *testing.T) {
	f := newUpdateFixture(t, "2.0.0", false)
	f.install(t, "2.0.0") // installed by a zdev without pin files

	if _, err := f.manager.EnsureTool(context.Background(), f.tool); err != nil {
		t.Fatal(err)
	}
	if n := f.downloads.Load(); n != 0 {
		t.Errorf("downloads = %d, want 0: the copy is the pinned version", n)
	}
	if _, err := os.Stat(f.manager.pinPath(f.tool)); err != nil {
		t.Errorf("pin file not written for the adopted copy: %v", err)
	}
}

func TestEnsureToolReplacesOlderCopyWithoutPin(t *testing.T) {
	f := newUpdateFixture(t, "2.0.0", false)
	f.install(t, "1.0.0")

	if _, err := f.manager.EnsureTool(context.Background(), f.tool); err != nil {
		t.Fatal(err)
	}
	if got := f.installedVersion(t); got != "2.0.0" {
		t.Errorf("installed version %q, want 2.0.0", got)
	}
	if *f.updated == "" {
		t.Error("BeforeUpdate didn't run before replacing the older copy")
	}
}

func TestEnsureToolKeepsOldCopyWhenUpdateFails(t *testing.T) {
	f := newUpdateFixture(t, "2.0.0", true)
	f.install(t, "1.0.0")

	path, err := f.manager.EnsureTool(context.Background(), f.tool)
	if err != nil {
		t.Fatalf("a failed update must keep the installed copy usable: %v", err)
	}
	if path != filepath.Join(f.manager.binDir, f.tool.BinaryName) || f.installedVersion(t) != "1.0.0" {
		t.Errorf("path %q, version %q: want the old copy", path, f.installedVersion(t))
	}
	if _, err := os.Stat(f.manager.pinPath(f.tool)); !os.IsNotExist(err) {
		t.Error("pin file written although the update failed, so it would never be retried")
	}
}
