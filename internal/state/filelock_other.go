//go:build !darwin && !linux

package state

func (m *Manager) withFileLock(_ bool, fn func() error) error {
	return fn()
}
