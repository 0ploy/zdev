package cmd

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/project"
)

func TestParseRemote(t *testing.T) {
	for remote, want := range map[string]string{
		"git@github.com:0ploy/zpinit.git":                      "github.com 0ploy/zpinit",
		"https://github.com/0ploy/zpinit":                      "github.com 0ploy/zpinit",
		"https://user@github.com/0ploy/zpinit.git/":            "github.com 0ploy/zpinit",
		"ssh://git@github.com/0ploy/zp.init.git":               "github.com 0ploy/zp.init",
		"https://github.com.evil.com/a/b":                      "github.com.evil.com a/b",
		"git@gitlab.scale.sc:scalecommerce/query-profiler.git": "gitlab.scale.sc scalecommerce/query-profiler",
		"https://gitlab.com/group/sub/project.git":             "gitlab.com group/sub/project",
		"ssh://git@git.example.com:2222/team/app.git":          "git.example.com team/app",
		"https://GitLab.Example.com:8443/team/app":             "gitlab.example.com team/app",
		"/srv/repos/app.git":                                   "",
		"file:///srv/repos/app.git":                            "",
	} {
		h, p, ok := parseRemote(remote)
		got := ""
		if ok {
			got = h + " " + p
		}
		if got != want {
			t.Errorf("%s: got %q, want %q", remote, got, want)
		}
	}
}

func TestCredentialFile(t *testing.T) {
	t.Setenv("ZDEV_HOME", t.TempDir())
	path, err := storeInFile("github-x", "tok")
	if err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(filepath.Dir(path)); fi.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %v", fi.Mode().Perm())
	}
	if v, err := readCredentialFile(path); err != nil || v != "tok" {
		t.Fatalf("read = %q, %v", v, err)
	}
	_ = os.Chmod(path, 0o644)
	if _, err := readCredentialFile(path); err == nil {
		t.Error("a file others can read was accepted")
	}
}

func TestGrantCredentialReplacesSameHost(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ZDEV_HOME", home)
	_ = os.WriteFile(filepath.Join(home, config.GlobalConfigFilename), []byte(`agent:
  credentials:
    github-gh: {hosts: [github.com], command: gh auth token}
    npm: {hosts: [registry.npmjs.org], command: echo x}
  projects:
    app: {credentials: [github-gh, npm]}
`), 0o600)
	if err := grantCredential("app", "github-app", config.AgentCredential{Hosts: githubHosts, File: "~/t"}); err != nil {
		t.Fatal(err)
	}
	g, err := config.LoadGlobalConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got := g.Agent.Projects["app"].Credentials; len(got) != 2 || got[0] != "github-app" || got[1] != "npm" {
		t.Errorf("grants = %v", got)
	}
}

// A project's own list replaces the defaults, so setup granting the first
// project-level credential must carry the defaults over.
func TestGrantCredentialKeepsDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ZDEV_HOME", home)
	_ = os.WriteFile(filepath.Join(home, config.GlobalConfigFilename), []byte(`agent:
  credentials:
    npm: {hosts: [registry.npmjs.org], command: echo x}
  defaults: {credentials: [npm]}
`), 0o600)
	if err := grantCredential("app", "gitlab-app", config.AgentCredential{Hosts: []string{"gitlab.example.com"}, File: "~/t"}); err != nil {
		t.Fatal(err)
	}
	g, err := config.LoadGlobalConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got := g.Agent.Projects["app"].Credentials; len(got) != 2 || got[0] != "gitlab-app" || got[1] != "npm" {
		t.Errorf("grants = %v, want gitlab-app plus npm from the defaults", got)
	}
}

func TestSetupSandboxService(t *testing.T) {
	setup := func(t *testing.T, global string, services map[string]config.ServiceConfig) *project.Project {
		t.Helper()
		home := t.TempDir()
		t.Setenv("ZDEV_HOME", home)
		_ = os.WriteFile(filepath.Join(home, config.GlobalConfigFilename), []byte(global), 0o600)
		dir := t.TempDir()
		for name, svc := range services {
			for i, v := range svc.Volumes {
				svc.Volumes[i] = strings.ReplaceAll(v, "PROJECT", dir)
			}
			services[name] = svc
		}
		return &project.Project{Dir: dir, Config: &config.ProjectConfig{Name: "shop", Services: services}}
	}
	stored := func(t *testing.T) string {
		t.Helper()
		g, err := config.LoadGlobalConfig()
		if err != nil {
			t.Fatal(err)
		}
		return g.Agent.Projects["shop"].Service
	}
	synced := func(image string) config.ServiceConfig {
		return config.ServiceConfig{Image: image, Volumes: []string{"PROJECT:/app"}}
	}
	run := func(t *testing.T, p *project.Project, input string) {
		t.Helper()
		g, err := config.LoadGlobalConfig()
		if err != nil {
			t.Fatal(err)
		}
		if err := setupSandboxService(bufio.NewReader(strings.NewReader(input)), true, g, p); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("app decides", func(t *testing.T) {
		p := setup(t, "version: 1\n", map[string]config.ServiceConfig{"app": synced("a"), "worker": synced("w")})
		run(t, p, "2\n") // must not be read
		if s := stored(t); s != "" {
			t.Errorf("stored %q although app decides", s)
		}
	})
	t.Run("ambiguous asks and stores", func(t *testing.T) {
		p := setup(t, "version: 1\n", map[string]config.ServiceConfig{"web": synced("w"), "api": synced("a"), "db": {Image: "postgres"}})
		run(t, p, "2\n") // candidates sorted: api, web
		if s := stored(t); s != "web" {
			t.Errorf("stored %q, want web", s)
		}
	})
	t.Run("decide each time stores nothing", func(t *testing.T) {
		p := setup(t, "version: 1\n", map[string]config.ServiceConfig{"web": synced("w"), "api": synced("a")})
		run(t, p, "3\n")
		if s := stored(t); s != "" {
			t.Errorf("stored %q", s)
		}
	})
	t.Run("configured is kept", func(t *testing.T) {
		p := setup(t, "version: 1\nagent:\n  projects:\n    shop: {service: api}\n", map[string]config.ServiceConfig{"web": synced("w"), "api": synced("a")})
		run(t, p, "2\n")
		if s := stored(t); s != "api" {
			t.Errorf("stored %q, want the configured api", s)
		}
	})
	t.Run("nothing mounts the project", func(t *testing.T) {
		p := setup(t, "version: 1\n", map[string]config.ServiceConfig{"db": {Image: "postgres"}, "cache": {Image: "redis"}})
		run(t, p, "1\n")
		if s := stored(t); s != "" {
			t.Errorf("stored %q", s)
		}
	})
}
