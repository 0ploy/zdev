package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSelfUpdateBinaryName(t *testing.T) {
	name := selfUpdateBinaryName()
	if name == "" {
		t.Error("selfUpdateBinaryName() returned empty string")
	}
	if name[:5] != "zdev-" {
		t.Errorf("selfUpdateBinaryName() = %q, want prefix 'zdev-'", name)
	}
}

func TestAtomicSymlinkFresh(t *testing.T) {
	dir := t.TempDir()
	linkPath := filepath.Join(dir, "zdev")
	target := filepath.Join(dir, "real-target")

	// Create the target so the symlink isn't dangling (not required, but
	// realistic for our use).
	if err := os.WriteFile(target, []byte("x"), 0o755); err != nil {
		t.Fatalf("write target: %v", err)
	}

	if err := atomicSymlink(linkPath, target); err != nil {
		t.Fatalf("atomicSymlink: %v", err)
	}

	got, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if got != target {
		t.Errorf("symlink target = %q, want %q", got, target)
	}
}

func TestAtomicSymlinkReplacesExistingFile(t *testing.T) {
	dir := t.TempDir()
	linkPath := filepath.Join(dir, "zdev")
	target := filepath.Join(dir, "real-target")

	// Pre-existing plain file at linkPath - simulates the legacy layout.
	if err := os.WriteFile(linkPath, []byte("legacy"), 0o755); err != nil {
		t.Fatalf("seed legacy binary: %v", err)
	}
	if err := os.WriteFile(target, []byte("new"), 0o755); err != nil {
		t.Fatalf("write target: %v", err)
	}

	if err := atomicSymlink(linkPath, target); err != nil {
		t.Fatalf("atomicSymlink: %v", err)
	}

	info, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatalf("lstat: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("linkPath is not a symlink after replacement")
	}
	if got, _ := os.Readlink(linkPath); got != target {
		t.Errorf("symlink target = %q, want %q", got, target)
	}
	// tmp file should have been cleaned up
	if _, err := os.Stat(linkPath + ".symlink.tmp"); !os.IsNotExist(err) {
		t.Errorf("expected tmp symlink cleaned up, stat err=%v", err)
	}
}

func TestAtomicSymlinkFailsOnUnwritableParent(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root bypasses file-mode permissions")
	}
	parent := t.TempDir()
	// Make parent read-only so we can't create the tmp symlink.
	if err := os.Chmod(parent, 0o555); err != nil {
		t.Fatalf("chmod parent: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })

	linkPath := filepath.Join(parent, "zdev")
	err := atomicSymlink(linkPath, "/some/target")
	if err == nil {
		t.Errorf("expected error on read-only parent, got nil")
	}
}

func TestMigrateToSymlinkIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	linkPath := filepath.Join(dir, "zdev")
	target := filepath.Join(dir, "real-target")

	if err := os.WriteFile(target, []byte("bin"), 0o755); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Symlink(target, linkPath); err != nil {
		t.Fatalf("seed symlink: %v", err)
	}

	// Second migration should be a no-op and not error even if we can't
	// write the parent dir (shouldn't reach atomicSymlink at all).
	if err := migrateToSymlink(linkPath, target); err != nil {
		t.Errorf("migrateToSymlink on already-correct link: %v", err)
	}
}

func TestMigrateToSymlinkConvertsFileWithoutSudo(t *testing.T) {
	dir := t.TempDir()
	linkPath := filepath.Join(dir, "zdev")
	target := filepath.Join(dir, "real-target")

	if err := os.WriteFile(linkPath, []byte("legacy"), 0o755); err != nil {
		t.Fatalf("seed legacy: %v", err)
	}
	if err := os.WriteFile(target, []byte("new"), 0o755); err != nil {
		t.Fatalf("write target: %v", err)
	}

	// Parent is user-writable, so migrateToSymlink should succeed via
	// atomicSymlink and never invoke sudo.
	if err := migrateToSymlink(linkPath, target); err != nil {
		t.Fatalf("migrateToSymlink: %v", err)
	}

	got, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if got != target {
		t.Errorf("symlink target = %q, want %q", got, target)
	}
}

func TestMigrateIfNeededNoOpWhenAlreadySymlinked(t *testing.T) {
	dir := t.TempDir()
	canonical := filepath.Join(dir, "canonical", "zdev")
	if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(canonical, []byte("real"), 0o755); err != nil {
		t.Fatalf("seed canonical: %v", err)
	}
	execPath := filepath.Join(dir, "zdev")
	if err := os.Symlink(canonical, execPath); err != nil {
		t.Fatalf("seed symlink: %v", err)
	}

	// Remove the symlinked canonical target on disk so that any accidental
	// copy+rewrite would be detectable - but migrateIfNeeded should not
	// even touch anything since the symlink already resolves to canonical.
	origBytes, _ := os.ReadFile(canonical)

	if err := migrateIfNeeded(execPath, canonical); err != nil {
		t.Fatalf("migrateIfNeeded: %v", err)
	}

	afterBytes, _ := os.ReadFile(canonical)
	if string(origBytes) != string(afterBytes) {
		t.Errorf("canonical binary content mutated on no-op migration")
	}
}

func TestIsPATHInstall(t *testing.T) {
	dir := t.TempDir()
	installed := filepath.Join(dir, "zdev")
	if err := os.WriteFile(installed, []byte("bin"), 0o755); err != nil {
		t.Fatalf("seed installed binary: %v", err)
	}
	t.Setenv("PATH", dir)

	if !isPATHInstall(installed) {
		t.Errorf("expected true for the PATH-resolved binary")
	}

	// A dev build outside PATH must not count as installed, even though a
	// zdev exists on PATH.
	devDir := t.TempDir()
	devBuild := filepath.Join(devDir, "zdev")
	if err := os.WriteFile(devBuild, []byte("dev"), 0o755); err != nil {
		t.Fatalf("seed dev build: %v", err)
	}
	if isPATHInstall(devBuild) {
		t.Errorf("expected false for a binary outside PATH")
	}

	// A symlink on PATH pointing at the executable still counts.
	linkDir := t.TempDir()
	if err := os.Symlink(installed, filepath.Join(linkDir, "zdev")); err != nil {
		t.Fatalf("seed symlink: %v", err)
	}
	t.Setenv("PATH", linkDir)
	if !isPATHInstall(installed) {
		t.Errorf("expected true when PATH resolves via symlink to the executable")
	}

	t.Setenv("PATH", "")
	if isPATHInstall(installed) {
		t.Errorf("expected false when zdev is not on PATH")
	}
}

func TestMigrateIfNeededConvertsLegacyLayout(t *testing.T) {
	dir := t.TempDir()
	canonical := filepath.Join(dir, "canonical", "zdev")
	execPath := filepath.Join(dir, "bin", "zdev")
	if err := os.MkdirAll(filepath.Dir(execPath), 0o755); err != nil {
		t.Fatalf("mkdir execPath: %v", err)
	}
	// Legacy: plain file at execPath, no canonical yet.
	legacyBytes := []byte("#!/bin/sh\necho legacy zdev\n")
	if err := os.WriteFile(execPath, legacyBytes, 0o755); err != nil {
		t.Fatalf("seed legacy: %v", err)
	}

	if err := migrateIfNeeded(execPath, canonical); err != nil {
		t.Fatalf("migrateIfNeeded: %v", err)
	}

	// Canonical should exist and hold the legacy bytes.
	got, err := os.ReadFile(canonical)
	if err != nil {
		t.Fatalf("read canonical: %v", err)
	}
	if string(got) != string(legacyBytes) {
		t.Errorf("canonical content mismatch")
	}

	// execPath should now be a symlink pointing at canonical.
	info, err := os.Lstat(execPath)
	if err != nil {
		t.Fatalf("lstat execPath: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("execPath is not a symlink after migration")
	}
	if tgt, _ := os.Readlink(execPath); tgt != canonical {
		t.Errorf("symlink target = %q, want %q", tgt, canonical)
	}

	// Running migrateIfNeeded again must be a no-op.
	if err := migrateIfNeeded(execPath, canonical); err != nil {
		t.Fatalf("second migrateIfNeeded: %v", err)
	}
}

