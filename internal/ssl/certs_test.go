package ssl

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildDomainList(t *testing.T) {
	domain := "0ploy.dev"
	domains := buildDomainList(domain)

	expected := []string{
		"*.0ploy.dev",
		"*.shared.0ploy.dev",
		"0ploy.dev",
		"localhost",
		"127.0.0.1",
	}

	if len(domains) != len(expected) {
		t.Errorf("expected %d domains, got %d", len(expected), len(domains))
	}

	for i, exp := range expected {
		if i >= len(domains) {
			break
		}
		if domains[i] != exp {
			t.Errorf("domain[%d]: expected %q, got %q", i, exp, domains[i])
		}
	}
}

func TestBuildDomainListCustomDomain(t *testing.T) {
	domain := "example.local"
	domains := buildDomainList(domain)

	// Check wildcard
	if domains[0] != "*.example.local" {
		t.Errorf("expected wildcard '*.example.local', got %q", domains[0])
	}

	// Check shared wildcard
	if domains[1] != "*.shared.example.local" {
		t.Errorf("expected shared wildcard '*.shared.example.local', got %q", domains[1])
	}

	// Check bare domain
	if domains[2] != "example.local" {
		t.Errorf("expected bare domain 'example.local', got %q", domains[2])
	}
}

func TestGetCertPaths(t *testing.T) {
	// Create a temp directory to simulate ~/.zdev/certs
	tmpDir, err := os.MkdirTemp("", "zdev-ssl-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	certsDir := filepath.Join(tmpDir, "certs")
	if err := os.MkdirAll(certsDir, 0755); err != nil {
		t.Fatalf("failed to create certs dir: %v", err)
	}

	// Create a CertManager with custom certsDir (for testing)
	cm := &CertManager{
		certsDir: certsDir,
		mkcert:   nil, // Not needed for this test
	}

	certPath, keyPath := cm.GetCertPaths()

	expectedCertPath := filepath.Join(certsDir, "cert.pem")
	expectedKeyPath := filepath.Join(certsDir, "key.pem")

	if certPath != expectedCertPath {
		t.Errorf("expected cert path %q, got %q", expectedCertPath, certPath)
	}

	if keyPath != expectedKeyPath {
		t.Errorf("expected key path %q, got %q", expectedKeyPath, keyPath)
	}
}

func TestCertsExist(t *testing.T) {
	// Create a temp directory
	tmpDir, err := os.MkdirTemp("", "zdev-ssl-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	certsDir := filepath.Join(tmpDir, "certs")
	if err := os.MkdirAll(certsDir, 0755); err != nil {
		t.Fatalf("failed to create certs dir: %v", err)
	}

	cm := &CertManager{
		certsDir: certsDir,
		mkcert:   nil,
	}

	t.Run("returns false when no certs exist", func(t *testing.T) {
		if cm.CertsExist() {
			t.Error("expected CertsExist to return false when no certs exist")
		}
	})

	t.Run("returns false when only cert exists", func(t *testing.T) {
		certPath := filepath.Join(certsDir, "cert.pem")
		if err := os.WriteFile(certPath, []byte("fake cert"), 0644); err != nil {
			t.Fatalf("failed to write cert: %v", err)
		}
		defer os.Remove(certPath)

		if cm.CertsExist() {
			t.Error("expected CertsExist to return false when only cert exists")
		}
	})

	t.Run("returns true when both cert and key exist", func(t *testing.T) {
		certPath := filepath.Join(certsDir, "cert.pem")
		keyPath := filepath.Join(certsDir, "key.pem")

		if err := os.WriteFile(certPath, []byte("fake cert"), 0644); err != nil {
			t.Fatalf("failed to write cert: %v", err)
		}
		if err := os.WriteFile(keyPath, []byte("fake key"), 0600); err != nil {
			t.Fatalf("failed to write key: %v", err)
		}

		if !cm.CertsExist() {
			t.Error("expected CertsExist to return true when both files exist")
		}
	})
}
