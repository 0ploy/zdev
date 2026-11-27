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
	"testing"

	"github.com/0ploy/zdev/internal/config"
)

func TestFindInPath(t *testing.T) {
	// Test with a command that should exist on all systems
	t.Run("finds existing command", func(t *testing.T) {
		// 'ls' exists on macOS/Linux, 'cmd' on Windows
		var cmdName string
		if runtime.GOOS == "windows" {
			cmdName = "cmd"
		} else {
			cmdName = "ls"
		}

		path, found := FindInPath(cmdName)
		if !found {
			t.Errorf("expected to find %s in PATH", cmdName)
		}
		if path == "" {
			t.Error("expected non-empty path")
		}
	})

	t.Run("does not find nonexistent command", func(t *testing.T) {
		path, found := FindInPath("zdev-nonexistent-command-12345")
		if found {
			t.Error("expected not to find nonexistent command")
		}
		if path != "" {
			t.Error("expected empty path for nonexistent command")
		}
	})
}

// The URLs the per-tool URL builders produced before the pins moved to
// config.ToolRelease; the shared {version}/{os}/{arch} expansion must keep them.
func TestToolDownloadURLs(t *testing.T) {
	for _, tc := range []struct {
		tool         ToolInfo
		goos, goarch string
		want         string
	}{
		{MkcertTool(), "darwin", "arm64", "https://github.com/FiloSottile/mkcert/releases/download/vVER/mkcert-vVER-darwin-arm64"},
		{MkcertTool(), "linux", "amd64", "https://github.com/FiloSottile/mkcert/releases/download/vVER/mkcert-vVER-linux-amd64"},
		{JustTool(), "darwin", "arm64", "https://github.com/casey/just/releases/download/VER/just-VER-aarch64-apple-darwin.tar.gz"},
		{JustTool(), "darwin", "amd64", "https://github.com/casey/just/releases/download/VER/just-VER-x86_64-apple-darwin.tar.gz"},
		{JustTool(), "linux", "amd64", "https://github.com/casey/just/releases/download/VER/just-VER-x86_64-unknown-linux-musl.tar.gz"},
		{JustTool(), "linux", "arm64", "https://github.com/casey/just/releases/download/VER/just-VER-aarch64-unknown-linux-musl.tar.gz"},
		{MutagenTool(), "darwin", "arm64", "https://github.com/mutagen-io/mutagen/releases/download/vVER/mutagen_darwin_arm64_vVER.tar.gz"},
		{MutagenTool(), "linux", "amd64", "https://github.com/mutagen-io/mutagen/releases/download/vVER/mutagen_linux_amd64_vVER.tar.gz"},
	} {
		want := strings.ReplaceAll(tc.want, "VER", tc.tool.Version)
		if got := tc.tool.DownloadURL(tc.goos, tc.goarch); got != want {
			t.Errorf("%s %s/%s:\n got  %s\n want %s", tc.tool.Name, tc.goos, tc.goarch, got, want)
		}
	}
}

// Every host tool has a checksum for every platform zdev ships for.
func TestToolChecksums(t *testing.T) {
	for _, tool := range []ToolInfo{MkcertTool(), JustTool(), MutagenTool()} {
		for _, p := range []string{"darwin/amd64", "darwin/arm64", "linux/amd64", "linux/arm64"} {
			goos, goarch, _ := strings.Cut(p, "/")
			if sum := tool.Checksum(goos, goarch); len(sum) != 64 {
				t.Errorf("%s: no SHA-256 for %s", tool.Name, p)
			}
		}
	}
}

func TestToolInfo(t *testing.T) {
	for _, tc := range []struct {
		tool    ToolInfo
		name    string
		archive string
	}{
		{MkcertTool(), "mkcert", ""},
		{JustTool(), "just", "tar.gz"},
		{MutagenTool(), "mutagen", "tar.gz"},
	} {
		if tc.tool.Name != tc.name || tc.tool.BinaryName != tc.name || tc.tool.ArchiveType != tc.archive {
			t.Errorf("%s: Name %q, BinaryName %q, ArchiveType %q", tc.name, tc.tool.Name, tc.tool.BinaryName, tc.tool.ArchiveType)
		}
	}
	if extra := MutagenTool().ExtraFiles; len(extra) != 1 || extra[0] != "mutagen-agents.tar.gz" {
		t.Errorf("mutagen ExtraFiles = %v, want the agents bundle", extra)
	}
}

func TestDownloadToolVerifiesChecksumBeforeInstall(t *testing.T) {
	payload := []byte("#!/bin/sh\necho verified\n")
	sum := sha256.Sum256(payload)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	manager := &Manager{binDir: t.TempDir()}
	tool := ToolInfo{
		ToolRelease: config.ToolRelease{Name: "verified-tool", URL: server.URL, SHA256: map[string]string{
			runtime.GOOS + "/" + runtime.GOARCH: hex.EncodeToString(sum[:]),
		}},
		BinaryName: "verified-tool",
	}
	if err := manager.downloadTool(context.Background(), tool); err != nil {
		t.Fatalf("downloadTool() error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(manager.binDir, tool.BinaryName)); err != nil {
		t.Fatalf("verified tool was not installed: %v", err)
	}
}

func TestDownloadToolRejectsChecksumMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("tampered"))
	}))
	defer server.Close()

	manager := &Manager{binDir: t.TempDir()}
	tool := ToolInfo{
		ToolRelease: config.ToolRelease{Name: "bad-tool", URL: server.URL, SHA256: map[string]string{
			runtime.GOOS + "/" + runtime.GOARCH: strings.Repeat("0", 64),
		}},
		BinaryName: "bad-tool",
	}
	if err := manager.downloadTool(context.Background(), tool); err == nil {
		t.Fatal("downloadTool() accepted a checksum mismatch")
	}
	if _, err := os.Stat(filepath.Join(manager.binDir, tool.BinaryName)); !os.IsNotExist(err) {
		t.Fatalf("unverified tool should not be installed, stat error: %v", err)
	}
}
