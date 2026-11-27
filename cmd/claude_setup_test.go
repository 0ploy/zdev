package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/0ploy/zdev/internal/config"
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
