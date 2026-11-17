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
