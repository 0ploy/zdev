package project

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0ploy/zdev/internal/agent"
	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/runtime"
	"github.com/0ploy/zdev/internal/state"
)

// policyTestProject is a project "shop" with Mutagen on and an app service
// syncing the project directory, in a ZDEV_HOME of its own.
func policyTestProject(t *testing.T) (*Project, *runtime.MockRuntime) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("ZDEV_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "global-config.yaml"), []byte("version: 1\ndomain: test.dev\nmutagen:\n  enabled: \"true\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mock := runtime.NewMockRuntime()
	p := newSyncTestProject(t, mock, []string{".git/hooks", "src"}, []string{".:/app"})
	p.Config.Domain = "shop.test.dev"
	return p, mock
}

// runCurrent puts a container of the service into the mock, created from the
// current config (so it isn't outdated).
func runCurrent(p *Project, mock *runtime.MockRuntime, name string) {
	svc := p.Config.Services[name]
	cfg := p.buildContainerConfig(name, svc, true, NewMutagenMounts(p.GetMutagenSyncMounts()))
	mock.Containers[p.ContainerName(name)] = cfg
	mock.ContainersExist[p.ContainerName(name)] = true
}

func violations(p *Project) string {
	var out []string
	for _, v := range p.CheckAgentPolicy("test.dev") {
		out = append(out, v.String())
	}
	return strings.Join(out, "\n")
}

func setApp(p *Project, f func(*config.ServiceConfig)) {
	svc := p.Config.Services["app"]
	f(&svc)
	p.Config.Services["app"] = svc
}

// Inside the project anything goes, except what exposes .git to a
// container: a bind of .git itself, or a native (unsynced) writable bind of
// the project root. The Dockerfile stays inside the project.
func TestAgentPolicyStaticRules(t *testing.T) {
	p, mock := policyTestProject(t)
	runCurrent(p, mock, "app")
	if err := p.RecordAgentBaseline(context.Background()); err != nil {
		t.Fatal(err)
	}
	if v := violations(p); v != "" {
		t.Fatalf("the applied config itself: %s", v)
	}

	allowed := [][]string{
		{".:/app", "./src:/src"},
		{".:/app", "./src:/srv:ro"},
		{".:/app", ".:/ro:ro"}, // read-only native bind of the root: .git readable, not writable
	}
	for _, vols := range allowed {
		setApp(p, func(s *config.ServiceConfig) { s.Volumes = vols })
		if v := violations(p); v != "" {
			t.Errorf("%v refused: %s", vols, v)
		}
	}

	refused := map[string]func(*config.ServiceConfig){
		"outside the project": func(s *config.ServiceConfig) { s.Volumes = []string{".:/app", "/etc:/host-etc:ro"} },
		"repository's .git":   func(s *config.ServiceConfig) { s.Volumes = []string{".:/app", "./.git/hooks:/hooks"} },
		"exposes .git": func(s *config.ServiceConfig) {
			s.Volumes = []string{".:/app"}
			s.Mutagen.NoSync = []string{"/app"}
		},
		"dockerfile": func(s *config.ServiceConfig) { s.Volumes = []string{".:/app"}; s.Dockerfile = "../evil/Dockerfile" },
		"1Password Environment": func(s *config.ServiceConfig) {
			s.Dockerfile = ""
			s.Environment = map[string]string{"DB": "op-env://prod123/DB_PASSWORD"}
		},
		"router label": func(s *config.ServiceConfig) {
			s.Environment = nil
			s.Labels = map[string]string{"traefik.http.routers.x.rule": "Host(`bank.test.dev`)"}
		},
	}
	for want, f := range refused {
		setApp(p, func(s *config.ServiceConfig) { s.Labels = nil })
		setApp(p, f)
		if v := violations(p); !strings.Contains(v, want) {
			t.Errorf("want a violation about %q, got %q", want, v)
		}
	}
}

// What the developer applied stays allowed, per service and with no more
// access: a read-only grant doesn't cover a writable bind, and one service's
// grant doesn't cover another service.
func TestAgentPolicyBaseline(t *testing.T) {
	p, mock := policyTestProject(t)
	ctx := context.Background()
	setApp(p, func(s *config.ServiceConfig) {
		s.Volumes = []string{".:/app", "/var/run/docker.sock:/var/run/docker.sock:ro"}
		s.OpEnv = "dev123"
	})
	runCurrent(p, mock, "app")
	if err := p.RecordAgentBaseline(ctx); err != nil {
		t.Fatal(err)
	}
	if v := violations(p); v != "" {
		t.Fatalf("the applied config itself: %s", v)
	}

	setApp(p, func(s *config.ServiceConfig) {
		s.Volumes = []string{".:/app", "/var/run/docker.sock:/var/run/docker.sock"}
	})
	if v := violations(p); !strings.Contains(v, "writable") {
		t.Errorf("ro -> rw must be refused, got %q", v)
	}

	setApp(p, func(s *config.ServiceConfig) {
		s.Volumes = []string{".:/app", "/var/run/docker.sock:/var/run/docker.sock:ro"}
	})
	p.Config.Services["worker"] = config.ServiceConfig{Image: "alpine", Volumes: []string{"/var/run/docker.sock:/s:ro"}, OpEnv: "dev123"}
	v := violations(p)
	if !strings.Contains(v, "service worker: bind mount of") || !strings.Contains(v, "service worker: 1Password Environment dev123") {
		t.Errorf("another service's grants must not carry over, got %q", v)
	}
}

// An outdated container is recorded from what it runs (its binds), not from
// a config it was never created from.
func TestAgentPolicyOutdatedServiceFromContainer(t *testing.T) {
	p, mock := policyTestProject(t)
	setApp(p, func(s *config.ServiceConfig) { s.Volumes = []string{".:/app", "/tmp/share:/share"} })
	runCurrent(p, mock, "app")
	// The config changed after the container was created.
	setApp(p, func(s *config.ServiceConfig) {
		s.Volumes = []string{".:/app", "/tmp/share:/share"}
		s.Environment = map[string]string{"NEW": "1"}
	})
	if err := p.RecordAgentBaseline(context.Background()); err != nil {
		t.Fatal(err)
	}
	if v := violations(p); v != "" {
		t.Errorf("binds of the running container must count as applied: %s", v)
	}
	// A synced directory is applied by the start itself (its sync session
	// follows the config), also for an outdated container.
	shared := t.TempDir()
	setApp(p, func(s *config.ServiceConfig) {
		s.Volumes = []string{".:/app", "/tmp/share:/share", shared + ":/shared"}
	})
	if err := p.RecordAgentBaseline(context.Background()); err != nil {
		t.Fatal(err)
	}
	if v := violations(p); v != "" {
		t.Errorf("a synced directory the start applied: %s", v)
	}
	setApp(p, func(s *config.ServiceConfig) { s.Volumes = []string{".:/app", "/tmp/other:/share"} })
	if v := violations(p); !strings.Contains(v, "outside the project") {
		t.Errorf("a new source must be refused, got %q", v)
	}
}

// Routing: new hostnames under the zdev domain are fine unless another
// project has them; others and new host ports need the developer.
func TestAgentPolicyRouting(t *testing.T) {
	p, mock := policyTestProject(t)
	p.Config.Shared.Router = true
	setApp(p, func(s *config.ServiceConfig) { s.Routing = &config.RoutingConfig{Protocol: "http", Port: 80} })
	runCurrent(p, mock, "app")
	if err := p.RecordAgentBaseline(context.Background()); err != nil {
		t.Fatal(err)
	}
	mgr, err := state.DefaultManager()
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.RegisterProjectWithRouting("bank", "/elsewhere", nil, nil, []string{"bank.test.dev"}); err != nil {
		t.Fatal(err)
	}

	p.Config.Services["api"] = config.ServiceConfig{Image: "x", Routing: &config.RoutingConfig{Protocol: "http", Port: 80, Domain: "api.shop.test.dev"}}
	if v := violations(p); v != "" {
		t.Errorf("a new host under the zdev domain: %s", v)
	}
	cases := map[string]config.RoutingConfig{
		"belongs to project bank": {Protocol: "http", Port: 80, Domain: "bank.test.dev"},
		"outside test.dev":        {Protocol: "http", Port: 80, Domain: "github.com"},
		"host port tcp/5432":      {Protocol: "tcp", Port: 5432, HostPort: 5432},
	}
	for want, r := range cases {
		r := r
		p.Config.Services["api"] = config.ServiceConfig{Image: "x", Routing: &r}
		if v := violations(p); !strings.Contains(v, want) {
			t.Errorf("want %q, got %q", want, v)
		}
	}
}

// A request from a sandbox can't load a config that names another project.
func TestAgentRequestPinsProject(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".zdev"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".zdev", "config.yaml"), []byte("name: other\nservices:\n  app:\n    image: alpine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ZDEV_HOME", t.TempDir())
	if _, err := LoadFromDir(dir); err != nil {
		t.Fatalf("without a request: %v", err)
	}
	t.Setenv(AgentRequestEnv, "shop")
	_, err := LoadFromDir(dir)
	var r *agent.Refusal
	if !errors.As(err, &r) || r.Code != "project-changed" {
		t.Fatalf("got %v, want a project-changed refusal", err)
	}
	t.Setenv(AgentRequestEnv, "other")
	if _, err := LoadFromDir(dir); err != nil {
		t.Fatalf("matching name: %v", err)
	}
}

// RecordAgentBaseline never runs for a request from a sandbox.
func TestAgentBaselineNotFromSandbox(t *testing.T) {
	p, mock := policyTestProject(t)
	runCurrent(p, mock, "app")
	t.Setenv(AgentRequestEnv, "shop")
	if err := p.RecordAgentBaseline(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p.HasAgentBaseline() {
		t.Error("a sandbox request recorded a baseline")
	}
}
