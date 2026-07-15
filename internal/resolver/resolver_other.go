//go:build !darwin && !linux

package resolver

// Supported reports that no automated split-DNS mechanism is available.
func Supported() (bool, string) {
	return false, "automatic host DNS configuration is only available on macOS and Linux"
}

// IsInstalled always reports false: there is nothing zdev could have installed.
func IsInstalled(domain string) (bool, error) {
	return false, nil
}

// Install is unsupported on this platform.
func Install(domain string, port int) error {
	return ErrUnsupported
}

// Remove is a no-op: nothing could have been installed here.
func Remove() error {
	return nil
}
