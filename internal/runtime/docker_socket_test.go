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

func TestIsDockerDesktopProxySocket(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		// Docker Desktop per-user sockets (macOS and Linux layouts).
		{"/Users/me/.docker/run/docker.sock", true},
		{"/home/me/.docker/desktop/docker.sock", true},
		// Other engines and the system socket are mountable as-is.
		{"/Users/me/.orbstack/run/docker.sock", false},
		{"/Users/me/.colima/default/docker.sock", false},
		{"/var/run/docker.sock", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isDockerDesktopProxySocket(c.path); got != c.want {
			t.Errorf("isDockerDesktopProxySocket(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestComputeDockerSocketMount_DockerDesktopProxy(t *testing.T) {
	// Docker Desktop's per-user proxy socket, with no /var/run/docker.sock
	// present (default socket disabled): not mountable, with guidance.
	t.Setenv("DOCKER_HOST", "unix:///Users/me/.docker/run/docker.sock")
	path, mountable, reason := computeDockerSocketMount()
	// isSocketFile(/var/run/docker.sock) is almost certainly false in CI/dev
	// (it's not a real unix socket file on the test host), so we expect the
	// not-mountable branch. Guard so the test is meaningful only then.
	if isSocketFile(DefaultDockerSocket) {
		t.Skip("/var/run/docker.sock is a live socket on this host; can't exercise the disabled-socket branch")
	}
	if mountable {
		t.Fatalf("expected proxy socket to be non-mountable, got mountable=true path=%q", path)
	}
	if reason == "" {
		t.Error("expected an actionable reason message, got empty")
	}
}

func TestComputeDockerSocketMount_OtherEngine(t *testing.T) {
	// OrbStack/Colima sockets are mountable exactly as resolved.
	t.Setenv("DOCKER_HOST", "unix:///Users/me/.orbstack/run/docker.sock")
	path, mountable, reason := computeDockerSocketMount()
	if !mountable || path != "/Users/me/.orbstack/run/docker.sock" || reason != "" {
		t.Errorf("computeDockerSocketMount() = (%q, %v, %q), want orbstack path, true, \"\"", path, mountable, reason)
	}
}
