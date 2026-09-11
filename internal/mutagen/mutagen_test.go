package mutagen

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestNew(t *testing.T) {
	m := New("/usr/local/bin/mutagen")

	if m == nil {
		t.Fatal("expected non-nil Mutagen")
	}

	if m.binaryPath != "/usr/local/bin/mutagen" {
		t.Errorf("expected binaryPath '/usr/local/bin/mutagen', got %q", m.binaryPath)
	}
}

func TestMergeIgnores(t *testing.T) {
	tests := []struct {
		name        string
		userIgnores []string
		wantLen     int
		wantHas     []string
	}{
		{
			name:        "empty user ignores",
			userIgnores: nil,
			wantLen:     len(BuiltinIgnores),
			wantHas:     BuiltinIgnores,
		},
		{
			name:        "user ignores added",
			userIgnores: []string{"vendor", "node_modules"},
			wantLen:     len(BuiltinIgnores) + 2,
			wantHas:     append(BuiltinIgnores, "vendor", "node_modules"),
		},
		{
			name:        "duplicate builtin ignored",
			userIgnores: []string{".git", "custom"},
			wantLen:     len(BuiltinIgnores) + 1, // .git is duplicate, only custom added
			wantHas:     append(BuiltinIgnores, "custom"),
		},
		{
			name:        "user duplicates deduplicated",
			userIgnores: []string{"vendor", "vendor", "node_modules"},
			wantLen:     len(BuiltinIgnores) + 2, // vendor only counted once
			wantHas:     append(BuiltinIgnores, "vendor", "node_modules"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := MergeIgnores(tt.userIgnores)

			if len(result) != tt.wantLen {
				t.Errorf("MergeIgnores() returned %d items, want %d", len(result), tt.wantLen)
			}

			// Check that all expected items are present
			resultSet := make(map[string]bool)
			for _, r := range result {
				resultSet[r] = true
			}

			for _, want := range tt.wantHas {
				if !resultSet[want] {
					t.Errorf("MergeIgnores() missing expected item %q", want)
				}
			}
		})
	}
}

func TestMergeIgnores_OrderPreserved(t *testing.T) {
	result := MergeIgnores([]string{"custom1", "custom2"})

	// Built-in ignores should come first
	for i, builtin := range BuiltinIgnores {
		if i >= len(result) {
			t.Fatalf("result too short, expected at least %d items", len(BuiltinIgnores))
		}
		if result[i] != builtin {
			t.Errorf("expected builtin %q at index %d, got %q", builtin, i, result[i])
		}
	}

	// User ignores should follow
	if result[len(BuiltinIgnores)] != "custom1" {
		t.Errorf("expected 'custom1' after builtins, got %q", result[len(BuiltinIgnores)])
	}
	if result[len(BuiltinIgnores)+1] != "custom2" {
		t.Errorf("expected 'custom2' after custom1, got %q", result[len(BuiltinIgnores)+1])
	}
}

func TestBuiltinIgnores(t *testing.T) {
	// Verify expected built-in ignores are present
	expected := []string{".git", ".DS_Store"}

	if len(BuiltinIgnores) != len(expected) {
		t.Errorf("expected %d builtin ignores, got %d", len(expected), len(BuiltinIgnores))
	}

	for i, want := range expected {
		if BuiltinIgnores[i] != want {
			t.Errorf("expected BuiltinIgnores[%d] = %q, got %q", i, want, BuiltinIgnores[i])
		}
	}
}

func TestSessionConfig(t *testing.T) {
	cfg := SessionConfig{
		Name:    "zdev-myproject-app",
		Alpha:   "/Users/test/myproject",
		Beta:    "docker://app.myproject.zdev/app",
		Ignores: []string{".git", "vendor"},
	}

	if cfg.Name != "zdev-myproject-app" {
		t.Errorf("expected Name 'zdev-myproject-app', got %q", cfg.Name)
	}
	if cfg.Alpha != "/Users/test/myproject" {
		t.Errorf("expected Alpha '/Users/test/myproject', got %q", cfg.Alpha)
	}
	if cfg.Beta != "docker://app.myproject.zdev/app" {
		t.Errorf("expected Beta 'docker://app.myproject.zdev/app', got %q", cfg.Beta)
	}
	if len(cfg.Ignores) != 2 {
		t.Errorf("expected 2 ignores, got %d", len(cfg.Ignores))
	}
}

func TestSessionConfigHash(t *testing.T) {
	base := SessionConfig{
		Name:                     "zdev-app-myproject",
		Alpha:                    "/host/path",
		Beta:                     "docker://app.myproject.zdev/app",
		Ignores:                  []string{".git", "vendor"},
		DefaultOwnerBeta:         "www-data",
		DefaultGroupBeta:         "www-data",
		DefaultFileModeBeta:      "0644",
		DefaultDirectoryModeBeta: "0755",
	}

	// Identity: hash is deterministic across calls.
	if base.Hash() != base.Hash() {
		t.Fatal("Hash() not deterministic")
	}

	// Identity-only fields (Name/Alpha/Beta) must NOT shift the hash -
	// they're session identity, not config drift.
	identityShift := base
	identityShift.Name = "different-name"
	identityShift.Alpha = "/elsewhere"
	identityShift.Beta = "docker://other/app"
	if identityShift.Hash() != base.Hash() {
		t.Error("Hash should ignore Name/Alpha/Beta")
	}

	// Each tracked field shifts the hash.
	cases := []struct {
		name   string
		mutate func(*SessionConfig)
	}{
		{"owner", func(c *SessionConfig) { c.DefaultOwnerBeta = "root" }},
		{"group", func(c *SessionConfig) { c.DefaultGroupBeta = "root" }},
		{"file_mode", func(c *SessionConfig) { c.DefaultFileModeBeta = "0600" }},
		{"directory_mode", func(c *SessionConfig) { c.DefaultDirectoryModeBeta = "0700" }},
		{"ignores", func(c *SessionConfig) { c.Ignores = []string{".git"} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mutated := base
			tc.mutate(&mutated)
			if mutated.Hash() == base.Hash() {
				t.Errorf("Hash should change when %s changes", tc.name)
			}
		})
	}

	// Ignore order must not affect the hash (sorted internally).
	reordered := base
	reordered.Ignores = []string{"vendor", ".git"}
	if reordered.Hash() != base.Hash() {
		t.Error("Hash should be invariant to Ignores order")
	}
}

// stubMutagen returns a Mutagen whose binary is a script printing fixed output,
// so the health probe's parsing can be exercised without a daemon.
func stubMutagen(t *testing.T, output string, exitCode int) *Mutagen {
	t.Helper()

	dir := t.TempDir()
	outFile := filepath.Join(dir, "output")
	if err := os.WriteFile(outFile, []byte(output), 0o644); err != nil {
		t.Fatalf("write stub output: %v", err)
	}

	binary := filepath.Join(dir, "mutagen")
	script := fmt.Sprintf("#!/bin/sh\ncat %q\nexit %d\n", outFile, exitCode)
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatalf("write stub binary: %v", err)
	}

	return New(binary)
}

func TestSessionHealthy(t *testing.T) {
	tests := []struct {
		name        string
		output      string
		exitCode    int
		wantHealthy bool
		wantKnown   bool
		wantDetail  string
	}{
		{
			name:        "synchronizing cleanly",
			output:      "true|true|0|0|0|0|0|",
			wantHealthy: true,
			wantKnown:   true,
		},
		{
			// Measured on nginx-unprivileged: both ends connected, status
			// "Watching for changes", flush succeeds, and not one file made it
			// into the container.
			name:       "connected but unable to write",
			output:     "true|true|0|0|0|1|0|container marker: unable to create file: permission denied; ",
			wantKnown:  true,
			wantDetail: "container marker: unable to create file: permission denied",
		},
		{
			name:       "container endpoint gone",
			output:     "true|false|0|0|0|0|0|",
			wantKnown:  true,
			wantDetail: "endpoint not connected",
		},
		{
			name:       "conflicts count against health",
			output:     "true|true|0|0|0|0|2|",
			wantKnown:  true,
			wantDetail: "",
		},
		{
			name:      "a future mutagen changes the model",
			output:    "some other shape entirely",
			wantKnown: false,
		},
		{
			name:      "no such session",
			output:    "",
			exitCode:  1,
			wantKnown: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := stubMutagen(t, tt.output, tt.exitCode)

			healthy, detail, known := m.SessionHealthy(context.Background(), "zdev-p-app")

			if known != tt.wantKnown {
				t.Fatalf("known = %v, want %v", known, tt.wantKnown)
			}
			if healthy != tt.wantHealthy {
				t.Errorf("healthy = %v, want %v", healthy, tt.wantHealthy)
			}
			if detail != tt.wantDetail {
				t.Errorf("detail = %q, want %q", detail, tt.wantDetail)
			}
		})
	}
}
