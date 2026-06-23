package services

import (
	"testing"

	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/runtime"
)

func TestLogsContainerConfig_SocketPath(t *testing.T) {
	// Default: empty SocketPath falls back to the conventional path.
	def := LogsContainerConfig(LogsServiceConfig{Image: config.LogsImage, Domain: "0ploy.dev"})
	if got := def.Volumes[0].Source; got != runtime.DefaultDockerSocket {
		t.Errorf("default socket source = %q, want %q", got, runtime.DefaultDockerSocket)
	}
	if got := def.Volumes[0].Target; got != runtime.DefaultDockerSocket {
		t.Errorf("socket target = %q, want %q", got, runtime.DefaultDockerSocket)
	}

	// Explicit engine socket (e.g. Colima) is used as the mount source.
	const colima = "/Users/me/.colima/default/docker.sock"
	cfg := LogsContainerConfig(LogsServiceConfig{Image: config.LogsImage, Domain: "0ploy.dev", SocketPath: colima})
	if got := cfg.Volumes[0].Source; got != colima {
		t.Errorf("socket source = %q, want %q", got, colima)
	}
	if got := cfg.Volumes[0].Target; got != runtime.DefaultDockerSocket {
		t.Errorf("socket target = %q, want %q", got, runtime.DefaultDockerSocket)
	}
}
