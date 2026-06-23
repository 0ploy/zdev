package runtime

import "testing"

func TestUnixSocketPath(t *testing.T) {
	cases := []struct {
		host     string
		wantPath string
		wantOK   bool
	}{
		{"unix:///var/run/docker.sock", "/var/run/docker.sock", true},
		{"unix:///Users/me/.orbstack/run/docker.sock", "/Users/me/.orbstack/run/docker.sock", true},
		{"unix:///Users/me/.colima/default/docker.sock", "/Users/me/.colima/default/docker.sock", true},
		{"tcp://127.0.0.1:2375", "", false},
		{"npipe:////./pipe/docker_engine", "", false},
		{"unix://", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		got, ok := unixSocketPath(c.host)
		if got != c.wantPath || ok != c.wantOK {
			t.Errorf("unixSocketPath(%q) = (%q, %v), want (%q, %v)",
				c.host, got, ok, c.wantPath, c.wantOK)
		}
	}
}

func TestResolveHostDockerSocketPath_DockerHost(t *testing.T) {
	// An explicit unix:// DOCKER_HOST is honored verbatim.
	t.Setenv("DOCKER_HOST", "unix:///Users/me/.orbstack/run/docker.sock")
	if got := resolveHostDockerSocketPath(); got != "/Users/me/.orbstack/run/docker.sock" {
		t.Errorf("resolveHostDockerSocketPath() = %q, want orbstack socket path", got)
	}

	// A non-unix DOCKER_HOST (tcp/npipe) can't be bind-mounted; fall back.
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:2375")
	if got := resolveHostDockerSocketPath(); got != DefaultDockerSocket {
		t.Errorf("resolveHostDockerSocketPath() = %q, want %q", got, DefaultDockerSocket)
	}
}
