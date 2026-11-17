package firstrun

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/0ploy/zdev/internal/config"
)

func TestIsInitialized(t *testing.T) {
	// Create a temp directory
	tmpDir, err := os.MkdirTemp("", "zdev-firstrun-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	mgr := &Manager{
		zdevHome:   tmpDir,
		domain:     config.DefaultDomain,
		sslEnabled: true,
	}

	t.Run("returns false when not initialized", func(t *testing.T) {
		if mgr.IsInitialized() {
			t.Error("expected IsInitialized to return false when marker file doesn't exist")
		}
	})

	t.Run("returns true when initialized", func(t *testing.T) {
		markerPath := filepath.Join(tmpDir, InitializedFileName)
		if err := os.WriteFile(markerPath, []byte(""), 0644); err != nil {
			t.Fatalf("failed to create marker file: %v", err)
		}

		if !mgr.IsInitialized() {
			t.Error("expected IsInitialized to return true when marker file exists")
		}
	})
}

func TestMarkInitialized(t *testing.T) {
	// Create a temp directory
	tmpDir, err := os.MkdirTemp("", "zdev-firstrun-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Use a subdirectory that doesn't exist yet
	zdevHome := filepath.Join(tmpDir, "subdir", ".zdev")

	mgr := &Manager{
		zdevHome:   zdevHome,
		domain:     config.DefaultDomain,
		sslEnabled: true,
	}

	t.Run("creates marker file and directories", func(t *testing.T) {
		if err := mgr.MarkInitialized(); err != nil {
			t.Fatalf("MarkInitialized failed: %v", err)
		}

		markerPath := filepath.Join(zdevHome, InitializedFileName)
		if _, err := os.Stat(markerPath); err != nil {
			t.Errorf("marker file should exist: %v", err)
		}

		if !mgr.IsInitialized() {
			t.Error("expected IsInitialized to return true after MarkInitialized")
		}
	})
}

func TestNewManager(t *testing.T) {
	cfg := &config.GlobalConfig{
		Domain: "0ploy.dev",
		SSL: config.SSLConfig{
			Enabled: true,
		},
	}

	mgr, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	if mgr.domain != "0ploy.dev" {
		t.Errorf("expected domain '0ploy.dev', got %q", mgr.domain)
	}

	if !mgr.sslEnabled {
		t.Error("expected sslEnabled to be true")
	}

	if mgr.zdevHome == "" {
		t.Error("expected zdevHome to be set")
	}
}

func TestNewManagerSSLDisabled(t *testing.T) {
	cfg := &config.GlobalConfig{
		Domain: config.DefaultDomain,
		SSL: config.SSLConfig{
			Enabled: false,
		},
	}

	mgr, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	if mgr.sslEnabled {
		t.Error("expected sslEnabled to be false")
	}
}
