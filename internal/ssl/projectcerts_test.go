package ssl

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/0ploy/zdev/internal/config"
)

// fakeCA is an in-process stand-in for mkcert: it writes rootCA.pem to its
// CAROOT and signs real leaf certs, so cert logic is tested end to end
// without the mkcert binary.
type fakeCA struct {
	t      *testing.T
	root   string
	cert   *x509.Certificate
	key    *ecdsa.PrivateKey
	signed int
}

func newFakeCA(t *testing.T) *fakeCA {
	t.Helper()
	ca := &fakeCA{t: t, root: t.TempDir()}
	ca.rotate()
	return ca
}

// rotate replaces the CA, as a `mkcert -uninstall && mkcert -install` would.
func (ca *fakeCA) rotate() {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		ca.t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: "zdev test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		ca.t.Fatal(err)
	}
	ca.cert, _ = x509.ParseCertificate(der)
	ca.key = key
	writePEM(ca.t, filepath.Join(ca.root, "rootCA.pem"), "CERTIFICATE", der)
}

func (ca *fakeCA) GetCARoot(context.Context) (string, error) { return ca.root + "\n", nil }

func (ca *fakeCA) GenerateCert(_ context.Context, certPath, keyPath string, domains ...string) error {
	ca.signed++
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, d := range domains {
		if ip := net.ParseIP(d); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, d)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(certPath), 0755); err != nil {
		return err
	}
	writePEM(ca.t, certPath, "CERTIFICATE", der)
	keyDER, _ := x509.MarshalECPrivateKey(key)
	writePEM(ca.t, keyPath, "EC PRIVATE KEY", keyDER)
	return nil
}

func writePEM(t *testing.T, path, typ string, der []byte) {
	t.Helper()
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
}

func setupCertEnv(t *testing.T) (*CertManager, *fakeCA) {
	t.Helper()
	t.Setenv("ZDEV_HOME", t.TempDir())
	ca := newFakeCA(t)
	mgr, err := NewCertManagerWithSigner(ca)
	if err != nil {
		t.Fatal(err)
	}
	return mgr, ca
}

func TestProjectCertNames(t *testing.T) {
	tests := []struct {
		name  string
		hosts []string
		want  []string
	}{
		{"none", nil, nil},
		{"covered by main cert", []string{"myproj.0ploy.dev", "0ploy.dev", "mail.shared.0ploy.dev", "localhost"}, nil},
		{"nested", []string{"api.myproj.0ploy.dev"}, []string{"*.myproj.0ploy.dev"}},
		{"siblings share a wildcard", []string{"api.myproj.0ploy.dev", "admin.myproj.0ploy.dev"}, []string{"*.myproj.0ploy.dev"}},
		{"deeper", []string{"v1.api.myproj.0ploy.dev"}, []string{"*.api.myproj.0ploy.dev"}},
		{"nested under shared", []string{"a.b.shared.0ploy.dev"}, []string{"*.b.shared.0ploy.dev"}},
		{"outside base domain", []string{"myapp.test", "api.myapp.test"}, []string{"api.myapp.test", "myapp.test"}},
		{"suffix but not subdomain", []string{"foo0ploy.dev"}, []string{"foo0ploy.dev"}},
		{"case and trailing dot", []string{"API.MyProj.0ploy.dev."}, []string{"*.myproj.0ploy.dev"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ProjectCertNames("0ploy.dev", tt.hosts)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestProjectCert_IssueThenNoop(t *testing.T) {
	mgr, ca := setupCertEnv(t)
	ctx := context.Background()
	names := []string{"*.myproj.0ploy.dev"}

	ok, err := mgr.ProjectCertUpToDate(ctx, "myproj", names)
	if err != nil || ok {
		t.Fatalf("fresh env: upToDate=%v err=%v, want false/nil", ok, err)
	}
	if err := mgr.WriteProjectCert(ctx, "myproj", names); err != nil {
		t.Fatal(err)
	}

	yaml, err := os.ReadFile(ProjectTraefikConfigPath("myproj"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"/etc/traefik/certs/projects/myproj.pem", "/etc/traefik/certs/projects/myproj.key", "cert-sha256: "} {
		if !strings.Contains(string(yaml), want) {
			t.Errorf("traefik config missing %q:\n%s", want, yaml)
		}
	}

	ok, err = mgr.ProjectCertUpToDate(ctx, "myproj", names)
	if err != nil || !ok {
		t.Fatalf("after write: upToDate=%v err=%v, want true/nil", ok, err)
	}
	if ca.signed != 1 {
		t.Errorf("signed %d certs, want 1", ca.signed)
	}
}

func TestProjectCert_StaleWhen(t *testing.T) {
	ctx := context.Background()
	names := []string{"*.myproj.0ploy.dev"}

	t.Run("names change", func(t *testing.T) {
		mgr, _ := setupCertEnv(t)
		_ = mgr.WriteProjectCert(ctx, "myproj", names)
		if ok, _ := mgr.ProjectCertUpToDate(ctx, "myproj", append(names, "myapp.test")); ok {
			t.Error("cert with fewer names reported up to date")
		}
	})

	t.Run("CA rotated", func(t *testing.T) {
		mgr, ca := setupCertEnv(t)
		_ = mgr.WriteProjectCert(ctx, "myproj", names)
		ca.rotate()
		fresh, _ := NewCertManagerWithSigner(ca) // the CA pool is cached per manager
		if ok, _ := fresh.ProjectCertUpToDate(ctx, "myproj", names); ok {
			t.Error("cert from old CA reported up to date")
		}
	})

	t.Run("traefik config missing", func(t *testing.T) {
		mgr, _ := setupCertEnv(t)
		_ = mgr.WriteProjectCert(ctx, "myproj", names)
		_ = os.Remove(ProjectTraefikConfigPath("myproj"))
		if ok, _ := mgr.ProjectCertUpToDate(ctx, "myproj", names); ok {
			t.Error("cert without traefik config reported up to date")
		}
	})
}

func TestProjectCert_RegenerationChangesTraefikConfig(t *testing.T) {
	// Traefik only watches the dynamic dir: a regenerated cert must change
	// the yaml, or the new cert is never loaded.
	mgr, _ := setupCertEnv(t)
	ctx := context.Background()
	names := []string{"*.myproj.0ploy.dev"}

	_ = mgr.WriteProjectCert(ctx, "myproj", names)
	first, _ := os.ReadFile(ProjectTraefikConfigPath("myproj"))
	_ = mgr.WriteProjectCert(ctx, "myproj", names)
	second, _ := os.ReadFile(ProjectTraefikConfigPath("myproj"))
	if string(first) == string(second) {
		t.Error("traefik config unchanged after cert regeneration")
	}
}

func TestRemoveProjectCert(t *testing.T) {
	mgr, _ := setupCertEnv(t)
	ctx := context.Background()

	if err := RemoveProjectCert("missing"); err != nil {
		t.Errorf("removing absent cert: %v", err)
	}

	_ = mgr.WriteProjectCert(ctx, "myproj", []string{"*.myproj.0ploy.dev"})
	if err := RemoveProjectCert("myproj"); err != nil {
		t.Fatal(err)
	}
	certPath, keyPath := ProjectCertPaths("myproj")
	for _, p := range []string{certPath, keyPath, ProjectTraefikConfigPath("myproj")} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s still exists", p)
		}
	}
}

func TestSweepOrphanProjectCerts(t *testing.T) {
	mgr, _ := setupCertEnv(t)
	ctx := context.Background()

	// "foo" and "foo-key" both exist: neither's files may be mistaken for the other's
	for _, p := range []string{"foo", "foo-key", "gone"} {
		if err := mgr.WriteProjectCert(ctx, p, []string{"*." + p + ".0ploy.dev"}); err != nil {
			t.Fatal(err)
		}
	}
	// Non-project files in the dynamic dir must survive
	dynamic := filepath.Join(config.GetTraefikConfigDir(), "dynamic.yaml")
	_ = os.WriteFile(dynamic, []byte("tls: {}\n"), 0644)

	if err := SweepOrphanProjectCerts(map[string]bool{"foo": true, "foo-key": true}); err != nil {
		t.Fatal(err)
	}

	for _, p := range []string{"foo", "foo-key"} {
		certPath, keyPath := ProjectCertPaths(p)
		for _, f := range []string{certPath, keyPath, ProjectTraefikConfigPath(p)} {
			if _, err := os.Stat(f); err != nil {
				t.Errorf("kept project %s lost %s", p, f)
			}
		}
	}
	certPath, keyPath := ProjectCertPaths("gone")
	for _, f := range []string{certPath, keyPath, ProjectTraefikConfigPath("gone")} {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Errorf("orphan file %s survived", f)
		}
	}
	if _, err := os.Stat(dynamic); err != nil {
		t.Error("sweep removed dynamic.yaml")
	}
}

func TestRefreshForCA(t *testing.T) {
	mgr, ca := setupCertEnv(t)
	ctx := context.Background()

	if _, _, _, err := mgr.EnsureCerts(ctx, "0ploy.dev"); err != nil {
		t.Fatal(err)
	}
	names := []string{"*.myproj.0ploy.dev", "myapp.test"}
	_ = mgr.WriteProjectCert(ctx, "myproj", names)

	// Same CA: nothing to do
	mainRegen, regen, err := mgr.RefreshForCA(ctx, "0ploy.dev", []string{"myproj", "nocert"})
	if err != nil || mainRegen || len(regen) != 0 {
		t.Fatalf("unchanged CA: main=%v regen=%v err=%v", mainRegen, regen, err)
	}

	ca.rotate()
	fresh, _ := NewCertManagerWithSigner(ca)
	mainRegen, regen, err = fresh.RefreshForCA(ctx, "0ploy.dev", []string{"myproj", "nocert"})
	if err != nil {
		t.Fatal(err)
	}
	if !mainRegen || !reflect.DeepEqual(regen, []string{"myproj"}) {
		t.Fatalf("rotated CA: main=%v regen=%v", mainRegen, regen)
	}
	// Regenerated with the same names, now valid for the new CA
	if ok, _ := fresh.ProjectCertUpToDate(ctx, "myproj", names); !ok {
		t.Error("regenerated project cert not up to date")
	}
	if ok, _ := fresh.VerifyCertCA(ctx); !ok {
		t.Error("main cert not valid for new CA")
	}
}
