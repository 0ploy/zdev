//go:build !darwin && !linux

package state

func (m *Manager) withFileLock(_ bool, fn func() error) error {
	return fn()
}

// WithRouterLock runs fn; no cross-process locking on this platform.
func (m *Manager) WithRouterLock(fn func() error) error {
	return fn()
}
