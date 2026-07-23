package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

// A template without a scaffold hook must keep `zdev create` a pure copy:
// runScaffoldHook must NOT parse config.yaml (so a config this zdev version
// can't parse still creates) and must report scaffolded=false.
func TestRunScaffoldHook_NoHookDoesNotLoadConfig(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".zdev"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Unknown top-level field: config.LoadProject (KnownFields strict) would
	// reject this if it were ever loaded.
	if err := os.WriteFile(filepath.Join(dir, ".zdev", "config.yaml"),
		[]byte("this_field_does_not_exist: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	scaffolded, err := runScaffoldHook(dir)
	if err != nil {
		t.Fatalf("expected no error for a hook-less project, got %v", err)
	}
	if scaffolded {
		t.Fatal("expected scaffolded=false when no scaffold.sh is present")
	}
}

// A disabled hook (scaffold.sh.disabled) must be ignored — no scaffold runs.
func TestRunScaffoldHook_DisabledHookIgnored(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".zdev"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".zdev", "scaffold.sh.disabled"),
		[]byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	scaffolded, err := runScaffoldHook(dir)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if scaffolded {
		t.Fatal("expected scaffolded=false for a .disabled hook")
	}
}
