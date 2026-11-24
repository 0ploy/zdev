//go:build darwin || linux

package state

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func (m *Manager) withFileLock(exclusive bool, fn func() error) error {
	return flockPath(m.path+".lock", exclusive, fn)
}

// WithRouterLock runs fn while holding an exclusive, cross-process lock
// that serializes router alias updates (see services.ConnectRouter).
// Separate from the state lock: fn reads state, and the state lock isn't
// reentrant.
func (m *Manager) WithRouterLock(fn func() error) error {
	return flockPath(m.path+".router.lock", true, fn)
}

func flockPath(path string, exclusive bool, fn func() error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("failed to create state directory: %w", err)
	}

	lockFile, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return fmt.Errorf("failed to open state lock: %w", err)
	}
	defer lockFile.Close()

	operation := unix.LOCK_SH
	if exclusive {
		operation = unix.LOCK_EX
	}
	if err := unix.Flock(int(lockFile.Fd()), operation); err != nil {
		return fmt.Errorf("failed to lock state file: %w", err)
	}
	defer unix.Flock(int(lockFile.Fd()), unix.LOCK_UN)

	return fn()
}
