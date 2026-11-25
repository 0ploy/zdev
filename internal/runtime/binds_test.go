package runtime

import "testing"

// Docker Desktop reports some bind sources under /host_mnt; they map back to
// the host path when that exists, anything else stays as reported.
func TestHostBindSource(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		"/host_mnt" + dir:        dir,
		dir:                      dir,
		"/host_mnt/no/such/path": "/host_mnt/no/such/path",
		"/var/run/docker.sock":   "/var/run/docker.sock",
	}
	for in, want := range cases {
		if got := hostBindSource(in); got != want {
			t.Errorf("hostBindSource(%q) = %q, want %q", in, got, want)
		}
	}
}
