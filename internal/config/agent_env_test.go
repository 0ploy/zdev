package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeAgentEnvProject(t *testing.T, yaml string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".zdev"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".zdev", "config.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

const agentEnvConfig = `name: shop
info: "token=${FAKE_HOST_TOKEN} db=${DB_HOST} own=${OWN} dir=${PROJECTDIR}"
variables:
  OWN: mine
services:
  app:
    image: alpine
    environment:
      T: ${FAKE_HOST_TOKEN}
`

// An agent sandbox edits .zdev/ and `zdev info` prints the substituted
// config: a request from it must not read the host's environment beyond what
// the developer's own runs read (review C1).
func TestAgentRequestSubstitutesOnlyApprovedHostEnv(t *testing.T) {
	t.Setenv("FAKE_HOST_TOKEN", "ghp_REALSECRET")
	t.Setenv("DB_HOST", "db.local")
	dir := writeAgentEnvProject(t, agentEnvConfig)
	defer SetAgentHostEnv(nil)

	cfg, err := LoadProject(dir)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"DB_HOST", "FAKE_HOST_TOKEN"}; !reflect.DeepEqual(cfg.HostEnv, want) {
		t.Errorf("HostEnv = %v, want %v", cfg.HostEnv, want)
	}
	if cfg.Info != "token=ghp_REALSECRET db=db.local own=mine dir="+filepath.Base(dir) {
		t.Errorf("developer run: info = %q", cfg.Info)
	}

	t.Setenv(AgentRequestEnv, "shop")
	SetAgentHostEnv(nil) // nothing registered: fail closed
	cfg, err = LoadProject(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Info != "token=${FAKE_HOST_TOKEN} db=${DB_HOST} own=mine dir="+filepath.Base(dir) {
		t.Errorf("agent request without a lookup: info = %q", cfg.Info)
	}
	if want := []string{"DB_HOST", "FAKE_HOST_TOKEN"}; !reflect.DeepEqual(cfg.HostEnv, want) {
		t.Errorf("agent request: HostEnv = %v, want %v (independent of the filter)", cfg.HostEnv, want)
	}

	var asked string
	SetAgentHostEnv(func(project string) map[string]bool {
		asked = project
		return map[string]bool{"DB_HOST": true}
	})
	cfg, err = LoadProject(dir)
	if err != nil {
		t.Fatal(err)
	}
	if asked != "shop" {
		t.Errorf("lookup asked for %q, want the request's project", asked)
	}
	if cfg.Info != "token=${FAKE_HOST_TOKEN} db=db.local own=mine dir="+filepath.Base(dir) {
		t.Errorf("agent request: info = %q", cfg.Info)
	}
	if got := cfg.Services["app"].Environment["T"]; got != "${FAKE_HOST_TOKEN}" {
		t.Errorf("agent request: service env T = %q", got)
	}
}

// A variable the config defines itself reads the host only when the host sets
// it (the environment wins); unset names count too, so HostEnv doesn't depend
// on what the host's environment holds.
func TestHostEnvRefs(t *testing.T) {
	t.Setenv("OWN_BUT_SET", "x")
	os.Unsetenv("UNSET_ONE")
	got := hostEnvRefs("${PROJECTNAME} ${HOME} ${OWN} ${OWN_BUT_SET} ${UNSET_ONE} ${UNSET_ONE}",
		map[string]string{"OWN": "a", "OWN_BUT_SET": "b"})
	if want := []string{"OWN_BUT_SET", "UNSET_ONE"}; !reflect.DeepEqual(got, want) {
		t.Errorf("hostEnvRefs = %v, want %v", got, want)
	}
}
