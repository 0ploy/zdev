package project

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
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

// Inside the project anything goes, except a writable bind rooted at the
// host's git hooks or config. The Dockerfile stays inside the project.
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
		{".:/app", ".:/ro:ro"},         // read-only native bind of the root
		{".:/app", "./.git:/repo.git"}, // contains the hooks: guard mounts go over them
		{".:/app", "./.git/hooks:/hooks:ro"},
	}
	for _, vols := range allowed {
		setApp(p, func(s *config.ServiceConfig) { s.Volumes = vols })
		if v := violations(p); v != "" {
			t.Errorf("%v refused: %s", vols, v)
		}
	}

	refused := map[string]func(*config.ServiceConfig){
		"outside the project": func(s *config.ServiceConfig) { s.Volumes = []string{".:/app", "/etc:/host-etc:ro"} },
		"git hooks or config": func(s *config.ServiceConfig) { s.Volumes = []string{".:/app", "./.git/hooks:/hooks"} },
		"/.git/config":        func(s *config.ServiceConfig) { s.Volumes = []string{".:/app", "./.git/config:/gitconfig"} },
		"dockerfile":          func(s *config.ServiceConfig) { s.Volumes = []string{".:/app"}; s.Dockerfile = "../evil/Dockerfile" },
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

// guardTestProject is policyTestProject with Mutagen on or off and the
// project in agent mode, with a .git/config.
func guardTestProject(t *testing.T, mutagenOn bool) (*Project, *runtime.MockRuntime) {
	t.Helper()
	p, mock := policyTestProject(t)
	enabled := "false"
	if mutagenOn {
		enabled = "true"
	}
	if err := os.WriteFile(filepath.Join(os.Getenv("ZDEV_HOME"), "global-config.yaml"), []byte("version: 1\ndomain: test.dev\nmutagen:\n  enabled: \""+enabled+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.Dir, ".git", "config"), []byte("[core]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := SetAgentSyncService("shop", "app"); err != nil {
		t.Fatal(err)
	}
	return p, mock
}

func guardTargets(cfg runtime.ContainerConfig) []string {
	var out []string
	for _, v := range cfg.Volumes {
		if strings.HasSuffix(v.Target, "/hooks") || strings.HasSuffix(v.Target, "/config") {
			out = append(out, v.Target+map[bool]string{true: ":ro", false: ":rw"}[v.ReadOnly])
		}
	}
	sort.Strings(out)
	return out
}

// Without Mutagen a writable bind containing the hooks is the host directory
// itself: every such bind gets read-only guards over the hooks and config.
func TestGitGuardMountsNativeBinds(t *testing.T) {
	p, _ := guardTestProject(t, false)
	setApp(p, func(s *config.ServiceConfig) {
		s.Volumes = []string{".:/app", "./.git:/repo.git", "./src:/src", ".:/ro:ro"}
	})
	cfg := p.buildContainerConfig("app", p.Config.Services["app"], false, nil)
	got := strings.Join(guardTargets(cfg), " ")
	want := "/app/.git/config:ro /app/.git/hooks:ro /repo.git/config:ro /repo.git/hooks:ro"
	if got != want {
		t.Errorf("guards = %q, want %q", got, want)
	}

	if err := ForgetAgentState("shop"); err != nil {
		t.Fatal(err)
	}
	if got := guardTargets(p.buildContainerConfig("app", p.Config.Services["app"], false, nil)); len(got) != 0 {
		t.Errorf("without agent mode: %v", got)
	}
}

// A synced mount needs no guard mounts: the sync ignores the paths.
func TestGitGuardMountsSkipSynced(t *testing.T) {
	p, _ := guardTestProject(t, true)
	cfg := p.buildContainerConfig("app", p.Config.Services["app"], true, NewMutagenMounts(p.GetMutagenSyncMounts()))
	if got := guardTargets(cfg); len(got) != 0 {
		t.Errorf("synced mount got guards: %v", got)
	}
}

// Entering agent mode recreates a container that lacks only the guards,
// and leaves one with other drift for the developer's update.
func TestApplyAgentGitGuards(t *testing.T) {
	p, mock := guardTestProject(t, false)
	p.Config.Services["worker"] = config.ServiceConfig{Image: "alpine", Command: "sleep 1", Volumes: []string{".:/w"}}
	for _, name := range []string{"app", "worker"} {
		p.skipGitGuards = true
		cfg := p.buildContainerConfig(name, p.Config.Services[name], false, nil)
		p.skipGitGuards = false
		mock.Containers[p.ContainerName(name)] = cfg
		mock.ContainersExist[p.ContainerName(name)] = true
		mock.ContainersRunning[p.ContainerName(name)] = true
	}
	// worker drifted otherwise too: not this function's business.
	w := p.Config.Services["worker"]
	w.Environment = map[string]string{"NEW": "1"}
	p.Config.Services["worker"] = w

	if err := p.ApplyAgentGitGuards(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(guardTargets(mock.Containers[p.ContainerName("app")]), " "); got != "/app/.git/config:ro /app/.git/hooks:ro" {
		t.Errorf("app not recreated with guards: %q", got)
	}
	if got := guardTargets(mock.Containers[p.ContainerName("worker")]); len(got) != 0 {
		t.Errorf("worker with other drift was recreated: %v", got)
	}
}

// A config that newly reads the host's environment is beyond the baseline;
// the developer's own run approves the names it reads (review C1).
func TestAgentPolicyHostEnv(t *testing.T) {
	p, mock := policyTestProject(t)
	runCurrent(p, mock, "app")
	p.Config.HostEnv = []string{"DB_HOST"}
	if err := p.RecordAgentBaseline(context.Background()); err != nil {
		t.Fatal(err)
	}
	if v := violations(p); v != "" {
		t.Fatalf("the applied config itself: %s", v)
	}
	p.Config.HostEnv = []string{"DB_HOST", "GH_TOKEN"}
	if v := violations(p); v != "${GH_TOKEN} reads this machine's environment" {
		t.Errorf("violations = %q", v)
	}
}

// End to end: a request loads the config with only the approved names.
func TestAgentRequestLoadsApprovedHostEnvOnly(t *testing.T) {
	t.Setenv("ZDEV_HOME", t.TempDir())
	t.Setenv("DB_HOST", "db.local")
	t.Setenv("FAKE_HOST_TOKEN", "ghp_REALSECRET")
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".zdev"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".zdev", "config.yaml"),
		[]byte("name: shop\ninfo: \"${DB_HOST} ${FAKE_HOST_TOKEN}\"\nservices:\n  app:\n    image: alpine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	baseline := filepath.Join(config.GetZdevHome(), "agent", "approved", "shop.json")
	if err := os.MkdirAll(filepath.Dir(baseline), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(baseline, []byte(`{"services":{},"env_names":["DB_HOST"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(AgentRequestEnv, "shop")
	p, err := LoadFromDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if p.Config.Info != "db.local ${FAKE_HOST_TOKEN}" {
		t.Errorf("info = %q", p.Config.Info)
	}
	if v := violations(p); v != "${FAKE_HOST_TOKEN} reads this machine's environment" {
		t.Errorf("violations = %q", v)
	}
}

// A bind written inside the project must stay inside it: a container may
// have replaced the directory with a link to the home directory, which the
// next start would mount, by whoever runs it (review 2026-11-28, H1).
func TestEscapingBinds(t *testing.T) {
	p, _ := policyTestProject(t)
	outside := t.TempDir()
	for _, l := range []struct{ name, target string }{
		{"storage", outside},                     // escapes
		{"current", filepath.Join(p.Dir, "src")}, // stays inside
	} {
		if err := os.Symlink(l.target, filepath.Join(p.Dir, l.name)); err != nil {
			t.Fatal(err)
		}
	}
	setApp(p, func(s *config.ServiceConfig) {
		s.Volumes = []string{".:/app", "./storage:/app/storage", "./current:/srv", filepath.Join(p.Dir, "storage") + ":/abs", outside + ":/data"}
	})
	if v := p.EscapingBinds(); len(v) != 0 {
		t.Fatalf("not in agent mode: %v", v)
	}
	if _, err := SetAgentSyncService("shop", "app"); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, v := range p.EscapingBinds() {
		got = append(got, v.String())
	}
	want := []string{
		"service app: ./storage leads to " + resolveHostPath(outside, ".") + " through a symlink",
		"service app: " + filepath.Join(p.Dir, "storage") + " leads to " + resolveHostPath(outside, ".") + " through a symlink",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("EscapingBinds =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
