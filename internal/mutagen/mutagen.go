package mutagen

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

// Mutagen wraps mutagen operations for file synchronization
type Mutagen struct {
	binaryPath string
}

// New creates a new Mutagen wrapper with the given binary path
func New(binaryPath string) *Mutagen {
	return &Mutagen{binaryPath: binaryPath}
}

// BinaryPath returns the path to the mutagen binary
func (m *Mutagen) BinaryPath() string {
	return m.binaryPath
}

// Version returns the mutagen version
func (m *Mutagen) Version(ctx context.Context) (string, error) {
	output, err := m.run(ctx, "version")
	if err != nil {
		return "", fmt.Errorf("failed to get mutagen version: %w", err)
	}
	return output, nil
}

// Daemon management

// EnsureDaemon ensures the mutagen daemon is running, starting it if necessary
func (m *Mutagen) EnsureDaemon(ctx context.Context) error {
	if m.IsDaemonRunning(ctx) {
		return nil
	}

	// Start the daemon
	_, err := m.run(ctx, "daemon", "start")
	if err != nil {
		return fmt.Errorf("failed to start mutagen daemon: %w", err)
	}

	return nil
}

// IsDaemonRunning checks if the mutagen daemon is running
func (m *Mutagen) IsDaemonRunning(ctx context.Context) bool {
	// Try to list sessions - this fails if daemon is not running
	cmd := exec.CommandContext(ctx, m.binaryPath, "sync", "list")
	err := cmd.Run()
	return err == nil
}

// Session management

// SessionConfig defines the configuration for creating a sync session
type SessionConfig struct {
	Name    string   // Session name (e.g., sync.app.myproject.zdev)
	Alpha   string   // Local path (host filesystem)
	Beta    string   // Docker endpoint (docker://container/path)
	Ignores []string // Paths to ignore from sync
	// Beta-side defaults stamped on NEW files/dirs only - empty string means
	// "don't pass the flag, let Mutagen use its built-in default". Changing
	// these on an existing session has no effect on already-materialized files.
	DefaultOwnerBeta         string
	DefaultGroupBeta         string
	DefaultFileModeBeta      string
	DefaultDirectoryModeBeta string
}

// Session represents a mutagen sync session
type Session struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Alpha  string `json:"alpha"`
	Beta   string `json:"beta"`
}

// Hash returns a deterministic sha256 over the parts of SessionConfig that,
// if changed, require terminating and recreating the session for the change
// to take effect. Name, Alpha, Beta are intentionally excluded - those are
// session identity, not config drift.
func (cfg SessionConfig) Hash() string {
	ignores := append([]string(nil), cfg.Ignores...)
	sort.Strings(ignores)
	payload := struct {
		Ignores                  []string
		DefaultOwnerBeta         string
		DefaultGroupBeta         string
		DefaultFileModeBeta      string
		DefaultDirectoryModeBeta string
	}{
		Ignores:                  ignores,
		DefaultOwnerBeta:         cfg.DefaultOwnerBeta,
		DefaultGroupBeta:         cfg.DefaultGroupBeta,
		DefaultFileModeBeta:      cfg.DefaultFileModeBeta,
		DefaultDirectoryModeBeta: cfg.DefaultDirectoryModeBeta,
	}
	b, _ := json.Marshal(payload)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// CreateSession creates a new sync session
func (m *Mutagen) CreateSession(ctx context.Context, cfg SessionConfig) error {
	// Ensure daemon is running
	if err := m.EnsureDaemon(ctx); err != nil {
		return err
	}

	args := []string{
		"sync", "create",
		cfg.Alpha,
		cfg.Beta,
		"--name", cfg.Name,
		"--sync-mode", "two-way-safe",
	}

	if cfg.DefaultOwnerBeta != "" {
		args = append(args, "--default-owner-beta", cfg.DefaultOwnerBeta)
	}
	if cfg.DefaultGroupBeta != "" {
		args = append(args, "--default-group-beta", cfg.DefaultGroupBeta)
	}
	if cfg.DefaultFileModeBeta != "" {
		args = append(args, "--default-file-mode-beta", cfg.DefaultFileModeBeta)
	}
	if cfg.DefaultDirectoryModeBeta != "" {
		args = append(args, "--default-directory-mode-beta", cfg.DefaultDirectoryModeBeta)
	}

	// Add ignores
	for _, ignore := range cfg.Ignores {
		args = append(args, "--ignore", ignore)
	}

	_, err := m.run(ctx, args...)
	if err != nil {
		return fmt.Errorf("failed to create sync session: %w", err)
	}

	return nil
}

// ResumeSession resumes a paused sync session
func (m *Mutagen) ResumeSession(ctx context.Context, name string) error {
	_, err := m.run(ctx, "sync", "resume", name)
	if err != nil {
		return fmt.Errorf("failed to resume session %s: %w", name, err)
	}
	return nil
}

// PauseSession pauses an active sync session
func (m *Mutagen) PauseSession(ctx context.Context, name string) error {
	_, err := m.run(ctx, "sync", "pause", name)
	if err != nil {
		return fmt.Errorf("failed to pause session %s: %w", name, err)
	}
	return nil
}

// TerminateSession terminates and removes a sync session
func (m *Mutagen) TerminateSession(ctx context.Context, name string) error {
	_, err := m.run(ctx, "sync", "terminate", name)
	if err != nil {
		return fmt.Errorf("failed to terminate session %s: %w", name, err)
	}
	return nil
}

// FlushSession waits for all pending sync operations to complete
func (m *Mutagen) FlushSession(ctx context.Context, name string) error {
	_, err := m.run(ctx, "sync", "flush", name)
	if err != nil {
		return fmt.Errorf("failed to flush session %s: %w", name, err)
	}
	return nil
}

// SessionExists checks if a session with the given name exists
func (m *Mutagen) SessionExists(ctx context.Context, name string) (bool, error) {
	// List sessions and check if name is present
	cmd := exec.CommandContext(ctx, m.binaryPath, "sync", "list", "--template", "{{range .}}{{.Name}}\n{{end}}")
	output, err := cmd.CombinedOutput()
	if err != nil {
		// If daemon isn't running, no sessions exist
		return false, nil
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, line := range lines {
		if strings.TrimSpace(line) == name {
			return true, nil
		}
	}

	return false, nil
}

// GetSessionStatus returns the status of a session
func (m *Mutagen) GetSessionStatus(ctx context.Context, name string) (string, error) {
	// Use template to get just the status
	cmd := exec.CommandContext(ctx, m.binaryPath, "sync", "list", name, "--template", "{{range .}}{{.Status}}{{end}}")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to get session status: %w", err)
	}

	return strings.TrimSpace(string(output)), nil
}

// ListSessions returns all sync sessions
func (m *Mutagen) ListSessions(ctx context.Context) ([]Session, error) {
	cmd := exec.CommandContext(ctx, m.binaryPath, "sync", "list", "--output", "json")
	output, err := cmd.CombinedOutput()
	if err != nil {
		// No sessions or daemon not running
		return nil, nil
	}

	// Parse JSON output
	var sessions []Session
	if err := json.Unmarshal(output, &sessions); err != nil {
		// Mutagen might return empty output for no sessions
		if len(output) == 0 || strings.TrimSpace(string(output)) == "" {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to parse sessions: %w", err)
	}

	return sessions, nil
}

// ListSessionsByPrefix returns all sessions with names starting with the given prefix
func (m *Mutagen) ListSessionsByPrefix(ctx context.Context, prefix string) ([]string, error) {
	cmd := exec.CommandContext(ctx, m.binaryPath, "sync", "list", "--template", "{{range .}}{{.Name}}\n{{end}}")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, nil
	}

	var matching []string
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, line := range lines {
		name := strings.TrimSpace(line)
		if name != "" && strings.HasPrefix(name, prefix) {
			matching = append(matching, name)
		}
	}

	return matching, nil
}

// run executes a mutagen command and returns the output
func (m *Mutagen) run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, m.binaryPath, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if len(output) > 0 {
			return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
		}
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

// Built-in ignores that are always applied
var BuiltinIgnores = []string{
	".git",
	".DS_Store",
}

// MergeIgnores combines built-in ignores with user-provided ignores
func MergeIgnores(userIgnores []string) []string {
	seen := make(map[string]bool)
	var result []string

	// Add built-in ignores first
	for _, ignore := range BuiltinIgnores {
		if !seen[ignore] {
			seen[ignore] = true
			result = append(result, ignore)
		}
	}

	// Add user ignores
	for _, ignore := range userIgnores {
		if !seen[ignore] {
			seen[ignore] = true
			result = append(result, ignore)
		}
	}

	return result
}
