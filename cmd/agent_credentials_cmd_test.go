package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0ploy/zdev/internal/config"
)

// zdev claude is a pure pass-through: zdev's own commands live under zdev
// agent, so no Claude argument (its own subcommands, --help) is ever taken.
func TestClaudeIsPassThrough(t *testing.T) {
	for _, args := range [][]string{
		{"setup"}, {"credentials", "add"}, {"update"}, {"--help"}, {"-h"},
		{"--model", "opus", "setup"}, {"app", "--continue"}, {"fix the login"},
	} {
		c, rest, err := rootCmd.Find(append([]string{"claude"}, args...))
		if err != nil || c != claudeCmd || strings.Join(rest, " ") != strings.Join(args, " ") {
			t.Errorf("zdev claude %q -> %s %q %v, want claude with the arguments unchanged", args, c.Name(), rest, err)
		}
	}
	c, rest, _ := rootCmd.Find([]string{"agent", "run", "claude", "--model", "opus"})
	if c != agentRunCmd || strings.Join(rest, " ") != "claude --model opus" {
		t.Errorf("zdev agent run claude ... -> %s %q", c.Name(), rest)
	}
}

func TestAgentArgs(t *testing.T) {
	if err := agentArg(nil, nil); err != nil {
		t.Errorf("no agent (every agent): %v", err)
	}
	if err := agentArg(nil, []string{"claude"}); err != nil {
		t.Errorf("claude: %v", err)
	}
	if err := agentArg(nil, []string{"pi"}); err != nil {
		t.Errorf("pi: %v", err)
	}
	if err := agentArg(nil, []string{"codex"}); err == nil || !strings.Contains(err.Error(), "known: claude, pi") {
		t.Errorf("unknown agent: %v", err)
	}
	if err := agentArg(nil, []string{"claude", "claude"}); err == nil {
		t.Error("two agents accepted")
	}
}

// credTestEnv is a zdev home with a global config, and a project "shop" as
// the working directory.
func credTestEnv(t *testing.T, global string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("ZDEV_HOME", home)
	if err := os.WriteFile(filepath.Join(home, config.GlobalConfigFilename), []byte(global), 0o600); err != nil {
		t.Fatal(err)
	}
	proj := t.TempDir()
	_ = os.MkdirAll(filepath.Join(proj, ".zdev"), 0o755)
	if err := os.WriteFile(filepath.Join(proj, ".zdev", "config.yaml"),
		[]byte("version: 1\nname: shop\nservices:\n  app:\n    image: alpine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(proj)
	return home
}

func runCreds(t *testing.T, args ...string) (string, error) {
	t.Helper()
	credAddFlags = struct {
		hosts, env               []string
		command, secret, file    string
		placeholderPfx           string
		preset                   string
		grant, noGrant, grantAll bool
	}{}
	credGrantAll, credRemoveYes = false, false
	var out bytes.Buffer
	rootCmd.SetArgs(append([]string{"agent", "credentials"}, args...))
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	defer func() { rootCmd.SetArgs(nil); rootCmd.SetOut(nil); rootCmd.SetErr(nil) }()
	err := rootCmd.Execute()
	return out.String(), err
}

func loadAgent(t *testing.T) config.AgentConfig {
	t.Helper()
	g, err := config.LoadGlobalConfig()
	if err != nil {
		t.Fatal(err)
	}
	return g.Agent
}

func TestCredentialsAddListTest(t *testing.T) {
	credTestEnv(t, "version: 1\n")
	if _, err := runCreds(t, "add", "sentry", "--host", "sentry.io", "--env", "SENTRY_AUTH_TOKEN",
		"--command", "printf %s%s tok 123", "--grant"); err != nil {
		t.Fatal(err)
	}
	a := loadAgent(t)
	c := a.Credentials["sentry"]
	if strings.Join(c.Hosts, ",") != "sentry.io" || strings.Join(c.Env, ",") != "SENTRY_AUTH_TOKEN" || c.Command != "printf %s%s tok 123" {
		t.Errorf("stored credential %+v", c)
	}
	if got := a.Projects["shop"].Credentials; len(got) != 1 || got[0] != "sentry" {
		t.Errorf("grants = %v", got)
	}

	out, err := runCreds(t, "list")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"sentry  (this project gets it)", "hosts:   sentry.io", "from `printf %s%s tok 123`", "granted: shop"} {
		if !strings.Contains(out, want) {
			t.Errorf("list lacks %q:\n%s", want, out)
		}
	}

	out, err = runCreds(t, "test", "sentry")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "readable: 6 characters") || strings.Contains(out, "tok123") {
		t.Errorf("test must report the length and never the value: %q", out)
	}
}

func TestCredentialsAddRejects(t *testing.T) {
	credTestEnv(t, "version: 1\nagent:\n  credentials:\n    npm: {hosts: [registry.npmjs.org], command: echo x}\n")
	for _, args := range [][]string{
		{"add", "bad", "--host", "https://x.com/path", "--command", "echo x", "--no-grant"},
		{"add", "Bad_Name", "--host", "x.com", "--command", "echo x", "--no-grant"},
		{"add", "npm", "--host", "x.com", "--command", "echo x", "--no-grant"}, // exists
		{"add", "two", "--host", "x.com", "--command", "echo x", "--file", "~/t", "--no-grant"},
		{"add", "p", "--preset", "nope", "--command", "echo x"},
		{"add", "nosource", "--host", "x.com"}, // no terminal to ask in
	} {
		if _, err := runCreds(t, args...); err == nil {
			t.Errorf("%q accepted", args)
		}
	}
	if a := loadAgent(t); len(a.Credentials) != 1 {
		t.Errorf("a rejected add wrote the config: %v", a.Credentials)
	}
}

func TestCredentialsPreset(t *testing.T) {
	credTestEnv(t, "version: 1\n")
	if _, err := runCreds(t, "add", "npm", "--preset", "npm", "--command", "echo x", "--no-grant"); err != nil {
		t.Fatal(err)
	}
	c := loadAgent(t).Credentials["npm"]
	if strings.Join(c.Hosts, ",") != "registry.npmjs.org" || strings.Join(c.Env, ",") != "NPM_TOKEN" {
		t.Errorf("preset not applied: %+v", c)
	}
}

// A project's own list replaces the defaults: granting to a project that has
// none must carry the defaults over, or it silently loses them.
func TestCredentialsGrantKeepsDefaults(t *testing.T) {
	credTestEnv(t, `version: 1
agent:
  credentials:
    npm: {hosts: [registry.npmjs.org], command: echo x}
    sentry: {hosts: [sentry.io], command: echo y}
  defaults:
    credentials: [npm]
`)
	if _, err := runCreds(t, "grant", "sentry"); err != nil {
		t.Fatal(err)
	}
	if got := loadAgent(t).Projects["shop"].Credentials; strings.Join(got, ",") != "npm,sentry" {
		t.Errorf("project grants = %v, want npm (from defaults) and sentry", got)
	}
	if _, err := runCreds(t, "revoke", "npm"); err != nil {
		t.Fatal(err)
	}
	a := loadAgent(t)
	if got := a.Projects["shop"].Credentials; strings.Join(got, ",") != "sentry" {
		t.Errorf("after revoke: %v", got)
	}
	if got := a.Defaults.Credentials; strings.Join(got, ",") != "npm" {
		t.Errorf("revoking for a project touched the defaults: %v", got)
	}
	if _, err := runCreds(t, "grant", "sentry", "--all"); err != nil {
		t.Fatal(err)
	}
	if got := loadAgent(t).Defaults.Credentials; strings.Join(got, ",") != "npm,sentry" {
		t.Errorf("--all: defaults = %v", got)
	}
	if _, err := runCreds(t, "grant", "nope"); err == nil {
		t.Error("granting an unknown credential was accepted")
	}
}

func TestCredentialsRemove(t *testing.T) {
	home := credTestEnv(t, "version: 1\n")
	file, err := storeInFile("gl", "secret-token")
	if err != nil {
		t.Fatal(err)
	}
	cfg := `version: 1
agent:
  credentials:
    gl: {hosts: [gitlab.example.com], file: ` + file + `}
    other: {hosts: [x.com], command: echo z}
  defaults:
    credentials: [gl, other]
  projects:
    shop: {credentials: [gl]}
    web: {credentials: [other, gl]}
`
	if err := os.WriteFile(filepath.Join(home, config.GlobalConfigFilename), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runCreds(t, "remove", "gl", "--yes"); err != nil {
		t.Fatal(err)
	}
	a := loadAgent(t)
	if _, ok := a.Credentials["gl"]; ok {
		t.Error("credential still defined")
	}
	if strings.Join(a.Defaults.Credentials, ",") != "other" || len(a.Projects["shop"].Credentials) != 0 ||
		strings.Join(a.Projects["web"].Credentials, ",") != "other" {
		t.Errorf("grants left behind: defaults %v, shop %v, web %v", a.Defaults.Credentials, a.Projects["shop"].Credentials, a.Projects["web"].Credentials)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Errorf("the token file zdev stored is still there: %v", err)
	}
}

func TestCredentialsAnthropicPreset(t *testing.T) {
	credTestEnv(t, "version: 1\n")
	if _, err := runCreds(t, "add", "pi-anthropic", "--preset", "anthropic", "--command", "echo x", "--no-grant"); err != nil {
		t.Fatal(err)
	}
	c := loadAgent(t).Credentials["pi-anthropic"]
	if strings.Join(c.Hosts, ",") != "api.anthropic.com" || strings.Join(c.Env, ",") != "ANTHROPIC_OAUTH_TOKEN" || c.PlaceholderPrefix != "sk-ant-oat01-" {
		t.Errorf("anthropic preset: %+v", c)
	}
	out, _ := runCreds(t, "list")
	if !strings.Contains(out, "placeholder starts with: sk-ant-oat01-") {
		t.Errorf("list doesn't show the prefix:\n%s", out)
	}
	if _, err := runCreds(t, "add", "bad-prefix", "--host", "x.com", "--command", "echo x", "--placeholder-prefix", "has space", "--no-grant"); err == nil {
		t.Error("a prefix with a space was accepted")
	}
}
