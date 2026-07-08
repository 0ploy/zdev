//go:build integration

package mutagen

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// findMutagenBinary looks for mutagen in common locations
func findMutagenBinary() string {
	// Check zdev's bin directory first
	homeDir, err := os.UserHomeDir()
	if err == nil {
		zdevPath := filepath.Join(homeDir, ".zdev", "bin", "mutagen")
		if _, err := os.Stat(zdevPath); err == nil {
			return zdevPath
		}
	}

	// Check PATH
	path, err := exec.LookPath("mutagen")
	if err == nil {
		return path
	}

	return ""
}

func TestMutagen_Daemon(t *testing.T) {
	binaryPath := findMutagenBinary()
	if binaryPath == "" {
		t.Skip("mutagen binary not found, skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	m := New(binaryPath)

	// Test EnsureDaemon (should start if not running)
	err := m.EnsureDaemon(ctx)
	if err != nil {
		t.Fatalf("EnsureDaemon() failed: %v", err)
	}

	// Daemon should now be running
	if !m.IsDaemonRunning(ctx) {
		t.Error("expected daemon to be running after EnsureDaemon")
	}

	// Calling EnsureDaemon again should be a no-op
	err = m.EnsureDaemon(ctx)
	if err != nil {
		t.Fatalf("EnsureDaemon() (second call) failed: %v", err)
	}
}

func TestMutagen_SessionLifecycle(t *testing.T) {
	binaryPath := findMutagenBinary()
	if binaryPath == "" {
		t.Skip("mutagen binary not found, skipping integration test")
	}

	// Check if Docker is available
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not found, skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	m := New(binaryPath)

	// Create a unique session name for this test
	sessionName := "zdev-test-integration"

	// Clean up any existing session from previous test runs
	_ = m.TerminateSession(ctx, sessionName)

	// Create a temporary directory for the alpha (host) side
	tmpDir, err := os.MkdirTemp("", "mutagen-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Start a test container
	containerName := "zdev-mutagen-integration-test"

	// Remove any existing container
	_ = exec.CommandContext(ctx, "docker", "rm", "-f", containerName).Run()

	// Start container
	err = exec.CommandContext(ctx, "docker", "run", "-d",
		"--name", containerName,
		"alpine:latest",
		"sleep", "infinity",
	).Run()
	if err != nil {
		t.Fatalf("failed to start test container: %v", err)
	}
	defer exec.CommandContext(ctx, "docker", "rm", "-f", containerName).Run()

	// Create sync session
	cfg := SessionConfig{
		Name:    sessionName,
		Alpha:   tmpDir,
		Beta:    "docker://" + containerName + "/app",
		Ignores: []string{".git"},
	}

	t.Log("Creating sync session...")
	err = m.CreateSession(ctx, cfg)
	if err != nil {
		t.Fatalf("CreateSession() failed: %v", err)
	}

	// Verify session exists
	exists, err := m.SessionExists(ctx, sessionName)
	if err != nil {
		t.Fatalf("SessionExists() failed: %v", err)
	}
	if !exists {
		t.Error("expected session to exist after creation")
	}

	// Get session status
	status, err := m.GetSessionStatus(ctx, sessionName)
	if err != nil {
		t.Fatalf("GetSessionStatus() failed: %v", err)
	}
	t.Logf("Session status: %s", status)

	// Create a test file and verify sync
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("hello mutagen"), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	// Flush to ensure sync completes
	t.Log("Flushing session...")
	err = m.FlushSession(ctx, sessionName)
	if err != nil {
		t.Fatalf("FlushSession() failed: %v", err)
	}

	// Verify file exists in container
	verifyCmd := exec.CommandContext(ctx, "docker", "exec", containerName, "cat", "/app/test.txt")
	output, err := verifyCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("failed to verify file in container: %v (output: %s)", err, output)
	}
	if string(output) != "hello mutagen" {
		t.Errorf("expected file content 'hello mutagen', got %q", string(output))
	}

	// Test pause
	t.Log("Pausing session...")
	err = m.PauseSession(ctx, sessionName)
	if err != nil {
		t.Fatalf("PauseSession() failed: %v", err)
	}

	// Session should still exist but be paused
	exists, err = m.SessionExists(ctx, sessionName)
	if err != nil {
		t.Fatalf("SessionExists() after pause failed: %v", err)
	}
	if !exists {
		t.Error("expected session to still exist after pause")
	}

	// Test resume
	t.Log("Resuming session...")
	err = m.ResumeSession(ctx, sessionName)
	if err != nil {
		t.Fatalf("ResumeSession() failed: %v", err)
	}

	// Test terminate
	t.Log("Terminating session...")
	err = m.TerminateSession(ctx, sessionName)
	if err != nil {
		t.Fatalf("TerminateSession() failed: %v", err)
	}

	// Session should no longer exist
	exists, err = m.SessionExists(ctx, sessionName)
	if err != nil {
		t.Fatalf("SessionExists() after terminate failed: %v", err)
	}
	if exists {
		t.Error("expected session to not exist after terminate")
	}

	t.Log("Session lifecycle test completed successfully")
}
