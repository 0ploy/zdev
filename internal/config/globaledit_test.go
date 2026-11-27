package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestEditGlobalConfigKeepsComments(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ZDEV_HOME", home)
	path := filepath.Join(home, GlobalConfigFilename)
	orig := "# my settings\ndomain: example.test # the domain\nagent:\n  disable_plugins: [codex@openai-codex]\n"
	if err := os.WriteFile(path, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	err := EditGlobalConfig(func(root *yaml.Node) error {
		creds, err := YAMLMapping(root, "agent", "credentials")
		if err != nil {
			return err
		}
		if err := SetYAML(creds, "github-x", AgentCredential{Hosts: []string{"github.com"}, File: "~/t"}); err != nil {
			return err
		}
		p, err := YAMLMapping(root, "agent", "projects", "x")
		if err != nil {
			return err
		}
		return SetYAML(p, "credentials", []string{"github-x"})
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	out := string(b)
	for _, want := range []string{"# my settings", "# the domain", "disable_plugins", "github-x:", "file: ~/t"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode changed to %v", fi.Mode().Perm())
	}
	cfg, err := LoadGlobalConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Agent.Projects["x"].Credentials; len(got) != 1 || got[0] != "github-x" {
		t.Errorf("project credentials = %v", got)
	}
}

func TestEditGlobalConfigRefusesInvalid(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ZDEV_HOME", home)
	err := EditGlobalConfig(func(root *yaml.Node) error {
		p, _ := YAMLMapping(root, "agent", "projects", "x")
		return SetYAML(p, "credentials", []string{"undefined"})
	})
	if err == nil {
		t.Fatal("an undefined credential was written")
	}
	if _, err := os.Stat(filepath.Join(home, GlobalConfigFilename)); !os.IsNotExist(err) {
		t.Error("file written despite the error")
	}
}
