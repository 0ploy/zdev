package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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

func TestBuildDownloadURL(t *testing.T) {
	tool := ToolInfo{
		Name:        "mkcert",
		Version:     "v1.4.4",
		URLTemplate: "https://github.com/FiloSottile/mkcert/releases/download/%s/mkcert-%s-%s-%s",
		BinaryName:  "mkcert",
	}

	url := buildDownloadURL(tool)

	// URL should contain version, OS, and arch
	expectedOS := runtime.GOOS
	expectedArch := runtime.GOARCH

	if url == "" {
		t.Error("expected non-empty URL")
	}

	// Check that URL contains expected components
	if !contains(url, "v1.4.4") {
		t.Errorf("URL should contain version, got: %s", url)
	}
	if !contains(url, expectedOS) {
		t.Errorf("URL should contain OS %s, got: %s", expectedOS, url)
	}
	if !contains(url, expectedArch) {
		t.Errorf("URL should contain arch %s, got: %s", expectedArch, url)
	}
}

func TestMkcertTool(t *testing.T) {
	tool := MkcertTool()

	if tool.Name != "mkcert" {
		t.Errorf("expected Name 'mkcert', got %q", tool.Name)
	}

	if tool.Version != config.MkcertVersion {
		t.Errorf("expected Version %q, got %q", config.MkcertVersion, tool.Version)
	}

	if tool.BinaryName != "mkcert" {
		t.Errorf("expected BinaryName 'mkcert', got %q", tool.BinaryName)
	}

	if tool.URLTemplate != config.MkcertURLTemplate {
		t.Errorf("expected URLTemplate from config, got %q", tool.URLTemplate)
	}
}

func TestJustArch(t *testing.T) {
	tests := []struct {
		goarch   string
		expected string
	}{
		{"amd64", "x86_64"},
		{"arm64", "aarch64"},
		{"386", "386"},
		{"unknown", "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.goarch, func(t *testing.T) {
			result := JustArch(tt.goarch)
			if result != tt.expected {
				t.Errorf("JustArch(%q) = %q, want %q", tt.goarch, result, tt.expected)
			}
		})
	}
}

func TestJustOS(t *testing.T) {
	tests := []struct {
		goos     string
		expected string
	}{
		{"darwin", "apple-darwin"},
		{"linux", "unknown-linux-musl"},
		{"windows", "windows"},
		{"freebsd", "freebsd"},
	}

	for _, tt := range tests {
		t.Run(tt.goos, func(t *testing.T) {
			result := JustOS(tt.goos)
			if result != tt.expected {
				t.Errorf("JustOS(%q) = %q, want %q", tt.goos, result, tt.expected)
			}
		})
	}
}

func TestJustTool(t *testing.T) {
	tool := JustTool()

	if tool.Name != "just" {
		t.Errorf("expected Name 'just', got %q", tool.Name)
	}

	if tool.Version != config.JustVersion {
		t.Errorf("expected Version %q, got %q", config.JustVersion, tool.Version)
	}

	if tool.BinaryName != "just" {
		t.Errorf("expected BinaryName 'just', got %q", tool.BinaryName)
	}

	if tool.ArchiveType != "tar.gz" {
		t.Errorf("expected ArchiveType 'tar.gz', got %q", tool.ArchiveType)
	}

	if tool.URLBuilder == nil {
		t.Error("expected URLBuilder to be set")
	}
}

func TestBuildDownloadURLWithCustomBuilder(t *testing.T) {
	tool := JustTool()
	url := buildDownloadURL(tool)

	expectedArch := JustArch(runtime.GOARCH)
	expectedOS := JustOS(runtime.GOOS)

	// just release filenames embed the version: just-<version>-<arch>-<os>.tar.gz
	expected := fmt.Sprintf(
		"https://github.com/casey/just/releases/download/%s/just-%s-%s-%s.tar.gz",
		config.JustVersion, config.JustVersion, expectedArch, expectedOS,
	)
	if url != expected {
		t.Errorf("URL mismatch\n  got:  %s\n  want: %s", url, expected)
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
		Name:       "verified-tool",
		BinaryName: "verified-tool",
		URLBuilder: func(_, _, _, _ string) string { return server.URL },
		Checksums: map[string]string{
			runtime.GOOS + "/" + runtime.GOARCH: hex.EncodeToString(sum[:]),
		},
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
		Name:       "bad-tool",
		BinaryName: "bad-tool",
		URLBuilder: func(_, _, _, _ string) string { return server.URL },
		Checksums: map[string]string{
			runtime.GOOS + "/" + runtime.GOARCH: strings.Repeat("0", 64),
		},
	}
	if err := manager.downloadTool(context.Background(), tool); err == nil {
		t.Fatal("downloadTool() accepted a checksum mismatch")
	}
	if _, err := os.Stat(filepath.Join(manager.binDir, tool.BinaryName)); !os.IsNotExist(err) {
		t.Fatalf("unverified tool should not be installed, stat error: %v", err)
	}
}

// contains checks if s contains substr
func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
