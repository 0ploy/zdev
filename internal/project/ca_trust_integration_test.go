//go:build integration

package project

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/ssl"
)

// CA injection against real Docker: the trust files reach a real container
// at /etc/zdev and over the system CA paths (including ones the image
// doesn't have), and a CA change written by SyncCATrust reaches the RUNNING
// container - through the directory mount and through the single-file
// mounts, which only works because the files are rewritten in place.
//
// Runs in a temp ZDEV_HOME with a stub CA, so the developer's ~/.zdev is
// untouched; the project uses no shared services.
func TestCATrust_MountsAndLiveUpdate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	home := t.TempDir()
	t.Setenv("ZDEV_HOME", home)
	enableTLS(t)

	caRoot := t.TempDir()
	writeCA := func(marker string) {
		t.Helper()
		pem := "-----BEGIN CERTIFICATE-----\n" + marker + "\n-----END CERTIFICATE-----\n"
		if err := os.WriteFile(filepath.Join(caRoot, "rootCA.pem"), []byte(pem), 0644); err != nil {
			t.Fatal(err)
		}
	}
	certMgr, err := ssl.NewCertManagerWithSigner(caSigner{dir: caRoot})
	if err != nil {
		t.Fatal(err)
	}
	old := newCertManager
	newCertManager = func() (*ssl.CertManager, error) { return certMgr, nil }
	t.Cleanup(func() { newCertManager = old })

	writeCA("ZDEV-ITEST-CA-1")
	if err := certMgr.SyncCATrust(ctx); err != nil {
		t.Skipf("no host CA bundle to build the trust bundle from: %v", err)
	}

	cfg := `
name: ca-trust-itest
shared:
  router: false
  mail: false
  db: false
  redis: false
  logs: false
services:
  app:
    image: ` + config.TestImage + `
    command: sleep infinity
`
	projectDir := createTempProject(t, "ca-trust", cfg)
	defer os.RemoveAll(projectDir)
	proj, err := LoadFromDir(projectDir)
	if err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}
	_ = proj.Down(ctx, false)
	defer func() { _ = proj.Down(context.Background(), false) }()

	if err := proj.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	container := proj.ContainerName("app")

	inContainer := func(args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, "docker", append([]string{"exec", container}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker exec %v: %v: %s", args, err, out)
		}
		return string(out)
	}
	bundlePaths := append([]string{containerBundlePath}, systemCABundlePaths...)

	// Mounted where the runtimes look, read-only, with the env pointing at them.
	for _, p := range bundlePaths {
		if got := inContainer("cat", p); !strings.Contains(got, "ZDEV-ITEST-CA-1") || !strings.Contains(got, "BEGIN CERTIFICATE") {
			t.Errorf("%s in the container lacks the zdev CA or the public roots (%d bytes)", p, len(got))
		}
	}
	if got := inContainer("cat", containerCAPath); !strings.Contains(got, "ZDEV-ITEST-CA-1") {
		t.Errorf("%s = %q, want the zdev CA", containerCAPath, got)
	}
	env := inContainer("env")
	for _, want := range []string{"NODE_EXTRA_CA_CERTS=" + containerCAPath, "REQUESTS_CA_BUNDLE=" + containerBundlePath} {
		if !strings.Contains(env, want) {
			t.Errorf("container env lacks %s", want)
		}
	}
	if out, err := exec.CommandContext(ctx, "docker", "exec", container, "sh", "-c", "echo x > /etc/ssl/certs/ca-certificates.crt").CombinedOutput(); err == nil {
		t.Errorf("system CA file is writable from the container, want a read-only mount (%s)", out)
	}

	// A CA change (reinstall) while the container runs: every path follows.
	writeCA("ZDEV-ITEST-CA-2")
	if err := certMgr.SyncCATrust(ctx); err != nil {
		t.Fatalf("SyncCATrust: %v", err)
	}
	for _, p := range append(bundlePaths, containerCAPath) {
		got := inContainer("cat", p)
		if !strings.Contains(got, "ZDEV-ITEST-CA-2") || strings.Contains(got, "ZDEV-ITEST-CA-1") {
			t.Errorf("%s in the running container still has the old CA after SyncCATrust", p)
		}
	}
}
