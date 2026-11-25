package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0ploy/zdev/internal/agent"
	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/runtime"
	"github.com/0ploy/zdev/internal/ssl"
)

// SSH remotes go over HTTPS, and git asks the helper for the placeholder on
// credential hosts only.
func TestSandboxGitConfig(t *testing.T) {
	creds := []agent.Credential{{Name: "gh-work", Hosts: []string{"github.com", "*.github.com"}, Placeholder: "ph"}}
	var lines []string
	for _, kv := range sandboxGitConfig(creds) {
		lines = append(lines, kv[0]+"="+kv[1])
	}
	all := strings.Join(lines, "\n")
	for _, want := range []string{
		"url.https://github.com/.insteadOf=git@github.com:",
		"url.https://github.com/.insteadOf=ssh://git@github.com/",
		"url.https://gitlab.com/.insteadOf=git@gitlab.com:",
		`credential.https://github.com.helper=!f() { test "$1" = get && printf 'username=x-access-token\npassword=%s\n' "$ZDEV_CREDENTIAL_GH_WORK"; }; f`,
	} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %s in\n%s", want, all)
		}
	}
	if strings.Contains(all, "*.github.com") {
		t.Error("a wildcard host can't be a git URL")
	}
	if strings.Contains(strings.Join(func() []string {
		var o []string
		for _, kv := range sandboxGitConfig(nil) {
			o = append(o, kv[0])
		}
		return o
	}(), " "), "credential.") {
		t.Error("no helper without credentials")
	}
}

func TestCredentialEnv(t *testing.T) {
	gcfg := &config.GlobalConfig{Agent: config.AgentConfig{Credentials: map[string]config.AgentCredential{
		"github": {Env: []string{"GH_TOKEN", "GITHUB_TOKEN"}},
	}}}
	env := credentialEnv(gcfg, []agent.Credential{{Name: "github", Placeholder: "ph", Value: "secret"}})
	if env["GH_TOKEN"] != "ph" || env["GITHUB_TOKEN"] != "ph" || env["ZDEV_CREDENTIAL_GITHUB"] != "ph" {
		t.Errorf("env = %v", env)
	}
	for _, v := range env {
		if v == "secret" {
			t.Fatal("the token reached the sandbox's environment")
		}
	}
}

// The session CA joins zdev's bundle where the config mounts it; without
// that bundle nothing is swapped.
func TestTrustSessionCA(t *testing.T) {
	t.Setenv("ZDEV_HOME", t.TempDir())
	ca, err := agent.NewSessionCA()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cfg := runtime.ContainerConfig{Volumes: []runtime.VolumeMount{{Source: ssl.CABundlePath(), Target: "/etc/ssl/certs/ca-certificates.crt", ReadOnly: true}}}
	if ok, _ := trustSessionCA(&cfg, ca, dir, "/etc/claude-code"); ok {
		t.Fatal("no bundle on the host: must not claim trust")
	}
	if err := os.MkdirAll(filepath.Dir(ssl.CABundlePath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ssl.CABundlePath(), []byte("ROOTS\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ok, err := trustSessionCA(&cfg, ca, dir, "/etc/claude-code")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if cfg.Volumes[0].Source != dir+"/ca-bundle.pem" {
		t.Errorf("bundle mount = %s", cfg.Volumes[0].Source)
	}
	b, _ := os.ReadFile(dir + "/ca-bundle.pem")
	if !strings.HasPrefix(string(b), "ROOTS\n") || !strings.Contains(string(b), string(ca.PEM())) {
		t.Errorf("bundle = %q", b)
	}
	if cfg.Env["NODE_EXTRA_CA_CERTS"] != "/etc/claude-code/extra-ca.pem" {
		t.Errorf("env = %v", cfg.Env)
	}
}
