package ssl

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

type caRootSigner struct{ dir string }

func (s caRootSigner) GetCARoot(context.Context) (string, error) { return s.dir, nil }
func (s caRootSigner) GenerateCert(context.Context, string, string, ...string) error {
	return nil
}

func inode(t *testing.T, path string) uint64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Sys().(*syscall.Stat_t).Ino
}

const (
	publicPEM = "-----BEGIN CERTIFICATE-----\nPUBLIC\n-----END CERTIFICATE-----\n"
	caPEM1    = "-----BEGIN CERTIFICATE-----\nZDEV-1\n-----END CERTIFICATE-----\n"
	caPEM2    = "-----BEGIN CERTIFICATE-----\nZDEV-2\n-----END CERTIFICATE-----\n"
)

// trustFixture points ZDEV_HOME, the mkcert CAROOT and the host bundle at
// temp dirs. hostBundle "" means the host has no CA bundle.
func trustFixture(t *testing.T, hostBundle string) (mgr *CertManager, writeCA func(string)) {
	t.Helper()
	t.Setenv("ZDEV_HOME", t.TempDir())
	caRoot := t.TempDir()
	writeCA = func(s string) {
		if err := os.WriteFile(filepath.Join(caRoot, "rootCA.pem"), []byte(s), 0644); err != nil {
			t.Fatal(err)
		}
	}
	hostPath := filepath.Join(t.TempDir(), "cert.pem")
	if hostBundle != "" {
		if err := os.WriteFile(hostPath, []byte(hostBundle), 0644); err != nil {
			t.Fatal(err)
		}
	}
	old := hostCABundlePaths
	hostCABundlePaths = []string{hostPath}
	t.Cleanup(func() { hostCABundlePaths = old })

	mgr, err := NewCertManagerWithSigner(caRootSigner{dir: caRoot})
	if err != nil {
		t.Fatal(err)
	}
	return mgr, writeCA
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSyncCATrust_WritesCAAndBundle(t *testing.T) {
	mgr, writeCA := trustFixture(t, publicPEM)
	writeCA(caPEM1)
	if err := mgr.SyncCATrust(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, CAPath()); got != caPEM1 {
		t.Errorf("ca.pem = %q, want the mkcert root", got)
	}
	bundle := readFile(t, CABundlePath())
	if !strings.HasPrefix(bundle, publicPEM) || !strings.Contains(bundle, strings.TrimSpace(caPEM1)) {
		t.Errorf("bundle = %q, want host roots followed by the zdev CA", bundle)
	}
}

// The bundle is bind-mounted as single files (inode pinned): a CA change
// must rewrite both files in place or running containers keep the old CA.
func TestSyncCATrust_UpdatesInPlace(t *testing.T) {
	mgr, writeCA := trustFixture(t, publicPEM)
	writeCA(caPEM1)
	if err := mgr.SyncCATrust(context.Background()); err != nil {
		t.Fatal(err)
	}
	caBefore, bundleBefore := inode(t, CAPath()), inode(t, CABundlePath())

	writeCA(caPEM2)
	if err := mgr.SyncCATrust(context.Background()); err != nil {
		t.Fatal(err)
	}
	if bundle := readFile(t, CABundlePath()); !strings.Contains(bundle, "ZDEV-2") || strings.Contains(bundle, "ZDEV-1") {
		t.Errorf("bundle not updated to the new CA: %q", bundle)
	}
	if inode(t, CAPath()) != caBefore || inode(t, CABundlePath()) != bundleBefore {
		t.Error("trust files were replaced, want in-place writes")
	}
}

// On Linux, mkcert -install adds the root to the system store: no duplicate.
func TestSyncCATrust_HostAlreadyTrustsCA(t *testing.T) {
	mgr, writeCA := trustFixture(t, publicPEM+caPEM1)
	writeCA(caPEM1)
	if err := mgr.SyncCATrust(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(readFile(t, CABundlePath()), "ZDEV-1"); n != 1 {
		t.Errorf("zdev CA appears %d times in the bundle, want 1", n)
	}
}

// A bundle holding only the zdev CA would break public HTTPS once mounted
// over the system CA files - without host roots, write no bundle.
func TestSyncCATrust_NoHostBundle(t *testing.T) {
	mgr, writeCA := trustFixture(t, "")
	writeCA(caPEM1)
	if err := mgr.SyncCATrust(context.Background()); err == nil {
		t.Error("want an error when the host has no CA bundle")
	}
	if got := readFile(t, CAPath()); got != caPEM1 {
		t.Errorf("ca.pem = %q, want it written anyway", got)
	}
	if _, err := os.Stat(CABundlePath()); !os.IsNotExist(err) {
		t.Errorf("bundle exists without host roots (stat err %v)", err)
	}
}
