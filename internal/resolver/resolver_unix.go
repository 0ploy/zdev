//go:build darwin || linux

package resolver

import (
	"fmt"
	"os"
	"os/exec"
)

// runSudoScript prints a short explanation, then runs the given shell
// script under sudo with the user's terminal wired through so the password
// prompt is visible and interactive. The script is authored from validated,
// zdev-controlled inputs only (a validated domain and a temp path from
// os.CreateTemp) - never raw user text.
func runSudoScript(explain, script string) error {
	if explain != "" {
		fmt.Println(explain)
	}
	cmd := exec.Command("sudo", "sh", "-c", script)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("privileged command failed: %w", err)
	}
	return nil
}

// writeTempConfig writes content to a temp file and returns its path plus a
// cleanup func. The file is created readable so the subsequent sudo copy
// can read it.
func writeTempConfig(content string) (string, func(), error) {
	f, err := os.CreateTemp("", "zdev-resolver-*.conf")
	if err != nil {
		return "", func() {}, fmt.Errorf("failed to create temp file: %w", err)
	}
	name := f.Name()
	cleanup := func() { _ = os.Remove(name) }
	if _, err := f.WriteString(content); err != nil {
		_ = f.Close()
		cleanup()
		return "", func() {}, fmt.Errorf("failed to write temp file: %w", err)
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("failed to close temp file: %w", err)
	}
	return name, cleanup, nil
}
