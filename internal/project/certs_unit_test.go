package project

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/runtime"
	"github.com/0ploy/zdev/internal/ssl"
)

func routedTestProject() *Project {
	p := newTestProject(runtime.NewMockRuntime())
	p.Config.Name = "myproj"
	p.Config.Domain = "myproj.0ploy.dev"
	p.Config.Shared.Router = true
	p.Config.Services = map[string]config.ServiceConfig{
		"web":   {Image: "nginx", Routing: &config.RoutingConfig{Protocol: "http", Port: 80}},
		"api":   {Image: "node", Routing: &config.RoutingConfig{Protocol: "http", Port: 3000, Domain: "api.myproj.0ploy.dev"}},
		"admin": {Image: "node", Routing: &config.RoutingConfig{Protocol: "https", Port: 443, Domain: "admin.example.test"}},
		"db":    {Image: "postgres", Routing: &config.RoutingConfig{Protocol: "tcp", Port: 5432, HostPort: 5432}},
		"redis": {Image: "redis"},
	}
	return p
}

// The cert must cover exactly the hosts the labels route: both derive from
// routesService/routingHost, and this pins that they stay in step.
func TestRoutedHosts_MatchHostRuleLabels(t *testing.T) {
	p := routedTestProject()

	var fromLabels []string
	seen := map[string]bool{}
	for name, svc := range p.Config.Services {
		cfg := p.buildContainerConfig(name, svc, false, nil)
		for k, v := range cfg.Labels {
			if strings.HasPrefix(k, "traefik.http.routers.") && strings.HasSuffix(k, ".rule") {
				host := strings.TrimSuffix(strings.TrimPrefix(v, "Host(`"), "`)")
				if !seen[host] {
					seen[host] = true
					fromLabels = append(fromLabels, host)
				}
			}
		}
	}
	sort.Strings(fromLabels)

	if got := p.routedHosts(); !reflect.DeepEqual(got, fromLabels) {
		t.Errorf("routedHosts() = %v, labels route %v", got, fromLabels)
	}

	p.Config.Shared.Router = false
	if got := p.routedHosts(); len(got) != 0 {
		t.Errorf("router disabled: routedHosts() = %v, want none", got)
	}
}

// recordingSigner stands in for mkcert: it records what it was asked to sign
// and writes placeholder files (never a parseable cert, so every check sees
// the cert as stale).
type recordingSigner struct{ calls [][]string }

func (s *recordingSigner) GetCARoot(context.Context) (string, error) { return "", nil }
func (s *recordingSigner) GenerateCert(_ context.Context, certPath, keyPath string, domains ...string) error {
	s.calls = append(s.calls, domains)
	_ = os.MkdirAll(filepath.Dir(certPath), 0755)
	_ = os.WriteFile(certPath, []byte("cert"), 0644)
	return os.WriteFile(keyPath, []byte("key"), 0600)
}

func enableTLS(t *testing.T) {
	t.Helper()
	zdevHome := os.Getenv("ZDEV_HOME")
	cfg := []byte("version: 1\ndomain: 0ploy.dev\nruntime: docker\nssl:\n  enabled: true\nmutagen:\n  enabled: \"false\"\n")
	if err := os.WriteFile(filepath.Join(zdevHome, "global-config.yaml"), cfg, 0644); err != nil {
		t.Fatal(err)
	}
	certsDir := config.GetCertsDir()
	_ = os.MkdirAll(certsDir, 0755)
	_ = os.WriteFile(filepath.Join(certsDir, "cert.pem"), []byte("x"), 0644)
	_ = os.WriteFile(filepath.Join(certsDir, "key.pem"), []byte("x"), 0600)
}

func stubCertManager(t *testing.T) *recordingSigner {
	t.Helper()
	signer := &recordingSigner{}
	old := newCertManager
	newCertManager = func() (*ssl.CertManager, error) { return ssl.NewCertManagerWithSigner(signer) }
	t.Cleanup(func() { newCertManager = old })
	return signer
}

func TestEnsureProjectCert_IssuesForNestedAndOutsideHosts(t *testing.T) {
	defer setupTestEnv(t)()
	enableTLS(t)
	signer := stubCertManager(t)

	routedTestProject().ensureProjectCert(context.Background())

	want := [][]string{{"*.myproj.0ploy.dev", "admin.example.test"}}
	if !reflect.DeepEqual(signer.calls, want) {
		t.Errorf("signed %v, want %v", signer.calls, want)
	}
	if _, err := os.Stat(ssl.ProjectTraefikConfigPath("myproj")); err != nil {
		t.Errorf("traefik config not written: %v", err)
	}
}

func TestEnsureProjectCert_NoNestedHostsRemovesCert(t *testing.T) {
	defer setupTestEnv(t)()
	enableTLS(t)
	signer := stubCertManager(t)

	p := routedTestProject()
	p.ensureProjectCert(context.Background())

	// Drop the hosts the main cert can't cover
	delete(p.Config.Services, "api")
	delete(p.Config.Services, "admin")
	p.ensureProjectCert(context.Background())

	if len(signer.calls) != 1 {
		t.Errorf("signed %d times, want 1 (the removal path must not sign)", len(signer.calls))
	}
	certPath, keyPath := ssl.ProjectCertPaths("myproj")
	for _, f := range []string{certPath, keyPath, ssl.ProjectTraefikConfigPath("myproj")} {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Errorf("%s still exists", f)
		}
	}
}

func TestEnsureProjectCert_TLSUnavailableNeedsNoMkcert(t *testing.T) {
	defer setupTestEnv(t)() // no certs: TLS unavailable
	old := newCertManager
	newCertManager = func() (*ssl.CertManager, error) {
		t.Fatal("cert manager constructed without TLS")
		return nil, nil
	}
	t.Cleanup(func() { newCertManager = old })

	routedTestProject().ensureProjectCert(context.Background())
}

// TCP routes match HostSNI(*) and need no cert, but containers still dial
// them by hostname, so the router must carry those names as aliases too.
func TestResolvableHosts_IncludesTCPRoutes(t *testing.T) {
	p := routedTestProject()
	want := []string{"admin.example.test", "api.myproj.0ploy.dev", "myproj.0ploy.dev"}
	if got := p.resolvableHosts(); !reflect.DeepEqual(got, want) {
		t.Errorf("resolvableHosts() = %v, want %v", got, want)
	}

	p.Config.Shared.Router = false
	if got := p.resolvableHosts(); len(got) != 0 {
		t.Errorf("router disabled: resolvableHosts() = %v, want none", got)
	}
}

// caSigner serves a CAROOT directory holding a rootCA.pem.
type caSigner struct{ dir string }

func (s caSigner) GetCARoot(context.Context) (string, error) { return s.dir + "\n", nil }
func (s caSigner) GenerateCert(context.Context, string, string, ...string) error {
	return nil
}

func TestEnsureCATrustFiles_CreatesFromMkcert(t *testing.T) {
	defer setupTestEnv(t)()
	enableTLS(t)
	caRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(caRoot, "rootCA.pem"), []byte("CA-1"), 0644); err != nil {
		t.Fatal(err)
	}
	old := newCertManager
	newCertManager = func() (*ssl.CertManager, error) { return ssl.NewCertManagerWithSigner(caSigner{dir: caRoot}) }
	t.Cleanup(func() { newCertManager = old })

	newTestProject(runtime.NewMockRuntime()).ensureCATrustFiles(context.Background())

	got, err := os.ReadFile(ssl.CAPath())
	if err != nil || string(got) != "CA-1" {
		t.Fatalf("ca.pem = %q, %v; want CA-1", got, err)
	}
}

// writeTrustFiles creates the trust files as SyncCATrust would.
func writeTrustFiles(t *testing.T, withBundle bool) {
	t.Helper()
	if err := os.MkdirAll(config.GetTrustDir(), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ssl.CAPath(), []byte("CA"), 0644); err != nil {
		t.Fatal(err)
	}
	if withBundle {
		if err := os.WriteFile(ssl.CABundlePath(), []byte("ROOTS+CA"), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func mountsByTarget(cfg runtime.ContainerConfig) map[string]runtime.VolumeMount {
	m := map[string]runtime.VolumeMount{}
	for _, v := range cfg.Volumes {
		m[v.Target] = v
	}
	return m
}

func TestBuildContainerConfig_TrustsZdevCA(t *testing.T) {
	defer setupTestEnv(t)()
	enableTLS(t)
	p := routedTestProject()

	// No trust files yet: nothing to mount.
	cfg := p.buildContainerConfig("web", p.Config.Services["web"], false, nil)
	if _, ok := cfg.Env["NODE_EXTRA_CA_CERTS"]; ok {
		t.Fatal("NODE_EXTRA_CA_CERTS set without trust files")
	}
	if _, ok := mountsByTarget(cfg)[containerTrustDir]; ok {
		t.Fatal("trust dir mounted without trust files")
	}

	writeTrustFiles(t, true)
	cfg = p.buildContainerConfig("web", p.Config.Services["web"], false, nil)
	mounts := mountsByTarget(cfg)
	want := map[string]string{containerTrustDir: config.GetTrustDir()}
	for _, target := range systemCABundlePaths {
		want[target] = ssl.CABundlePath()
	}
	if len(want) != 4 {
		t.Fatalf("want 4 trust mounts, test lists %d", len(want))
	}
	for target, source := range want {
		v, ok := mounts[target]
		if !ok {
			t.Errorf("no mount at %s", target)
			continue
		}
		if v.Source != source || !v.ReadOnly {
			t.Errorf("mount at %s = %+v, want read-only from %s", target, v, source)
		}
	}
	if cfg.Env["NODE_EXTRA_CA_CERTS"] != containerCAPath {
		t.Errorf("NODE_EXTRA_CA_CERTS = %q, want %q", cfg.Env["NODE_EXTRA_CA_CERTS"], containerCAPath)
	}
	if cfg.Env["REQUESTS_CA_BUNDLE"] != containerBundlePath {
		t.Errorf("REQUESTS_CA_BUNDLE = %q, want %q", cfg.Env["REQUESTS_CA_BUNDLE"], containerBundlePath)
	}
	if _, ok := cfg.Env["SSL_CERT_FILE"]; ok {
		t.Error("SSL_CERT_FILE must not be set")
	}

	// User-set env vars and user mounts on the same paths win.
	svc := p.Config.Services["web"]
	svc.Environment = map[string]string{"NODE_EXTRA_CA_CERTS": "/custom.pem", "REQUESTS_CA_BUNDLE": "/custom-bundle.pem"}
	svc.Volumes = append(svc.Volumes, "./certs.crt:/etc/ssl/certs/ca-certificates.crt:ro")
	cfg = p.buildContainerConfig("web", svc, false, nil)
	if cfg.Env["NODE_EXTRA_CA_CERTS"] != "/custom.pem" || cfg.Env["REQUESTS_CA_BUNDLE"] != "/custom-bundle.pem" {
		t.Errorf("user env overridden: %v", cfg.Env)
	}
	n := 0
	for _, v := range cfg.Volumes {
		if v.Target == "/etc/ssl/certs/ca-certificates.crt" {
			n++
			if v.Source == ssl.CABundlePath() {
				t.Error("zdev bundle mounted over the user's own mount")
			}
		}
	}
	if n != 1 {
		t.Errorf("%d mounts at /etc/ssl/certs/ca-certificates.crt, want 1 (duplicates fail docker run)", n)
	}
}

// Without host roots there is no bundle: the image's system CA files stay
// untouched and only Node gets the zdev CA.
func TestBuildContainerConfig_NoBundleLeavesSystemStore(t *testing.T) {
	defer setupTestEnv(t)()
	enableTLS(t)
	p := routedTestProject()
	writeTrustFiles(t, false)

	cfg := p.buildContainerConfig("web", p.Config.Services["web"], false, nil)
	mounts := mountsByTarget(cfg)
	if _, ok := mounts[containerTrustDir]; !ok {
		t.Error("trust dir not mounted")
	}
	for _, target := range systemCABundlePaths {
		if _, ok := mounts[target]; ok {
			t.Errorf("%s mounted without a bundle", target)
		}
	}
	if _, ok := cfg.Env["REQUESTS_CA_BUNDLE"]; ok {
		t.Error("REQUESTS_CA_BUNDLE set without a bundle")
	}
	if cfg.Env["NODE_EXTRA_CA_CERTS"] != containerCAPath {
		t.Errorf("NODE_EXTRA_CA_CERTS = %q, want %q", cfg.Env["NODE_EXTRA_CA_CERTS"], containerCAPath)
	}
}
