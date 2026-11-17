package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0ploy/zdev/internal/run"
)

func loadRunSet(t *testing.T, files map[string]string) (string, *run.Set) {
	t.Helper()
	dir := t.TempDir()
	runDir := run.Dir(dir)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(runDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	set, err := run.Load(dir, run.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return dir, set
}

var runFixture = map[string]string{
	"test.toml": "description = \"Run the tests\"\naliases = [\"t\"]\nrun = \"phpunit\"",
	"deploy.toml": `
description = "Deployment"
[jobs.all]
description = "Everything"
needs = ["build {1}"]
confirm = "Ship {1}?"
env = { STAGE = "{1}" }
run = '''
echo one
echo two
'''
[jobs.build]
service = "app"
user = "www-data"
output = "quiet"
script = "bin/build {all}"
[jobs.helper]
hidden = true
run = "true"
`,
	"secret.toml": "hidden = true\nrun = \"x\"",
	"bad.toml":    "output = \"loud\"\nrun = \"x\"",
}

func TestPrintRunList(t *testing.T) {
	_, set := loadRunSet(t, runFixture)
	var buf bytes.Buffer
	printRunList(&buf, set)
	out := buf.String()
	for _, want := range []string{"deploy", "Deployment", "all", "Everything", "build", "test, t", "Run the tests", "Broken", `bad.toml: output must be`} {
		if !strings.Contains(out, want) {
			t.Errorf("list missing %q:\n%s", want, out)
		}
	}
	for _, reject := range []string{"helper", "secret"} {
		if strings.Contains(out, reject) {
			t.Errorf("hidden %q listed:\n%s", reject, out)
		}
	}
}

func TestPrintRunList_Empty(t *testing.T) {
	_, set := loadRunSet(t, nil)
	var buf bytes.Buffer
	printRunList(&buf, set)
	if !strings.Contains(buf.String(), "No project commands") {
		t.Errorf("got %q", buf.String())
	}
}

func TestPrintRunFile(t *testing.T) {
	_, set := loadRunSet(t, runFixture)
	target, err := set.Resolve([]string{"deploy"})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	printRunFile(&buf, target.File)
	out := buf.String()
	if !strings.Contains(out, "zdev run deploy <job>") || !strings.Contains(out, "all") || strings.Contains(out, "helper") {
		t.Errorf("got:\n%s", out)
	}
}

func TestPrintRunPlan(t *testing.T) {
	dir, set := loadRunSet(t, runFixture)
	args := []string{"deploy", "all", "prod env"}
	target, err := set.Resolve(args)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := set.Plan(target, dir)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	printRunPlan(&buf, args, plan)
	out := buf.String()
	for _, want := range []string{
		"zdev run deploy all 'prod env'",
		"1. deploy build  (app, user www-data, output quiet)",
		"workdir  (service default)",
		"script   bin/build 'prod env' 'prod env'",
		"2. deploy all  (host, output show)",
		"env      STAGE",
		`confirm  "Ship prod env?"`,
		"     | echo one\n     | echo two\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("plan missing %q:\n%s", want, out)
		}
	}
}

func TestPrintRunJSON(t *testing.T) {
	_, set := loadRunSet(t, runFixture)
	var buf bytes.Buffer
	if err := printRunJSON(&buf, set); err != nil {
		t.Fatal(err)
	}
	var out runJSONOutput
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, buf.String())
	}
	if len(out.Files) != 3 || len(out.Broken) != 1 || out.Broken[0].File != "bad.toml" {
		t.Fatalf("files %d broken %v", len(out.Files), out.Broken)
	}
	var deploy runJSONFile
	for _, f := range out.Files {
		if f.Name == "deploy" {
			deploy = f
		}
	}
	if len(deploy.Jobs) != 3 || deploy.Jobs[0].ID != "deploy all" || deploy.Jobs[2].Hidden != true {
		t.Errorf("deploy: %+v", deploy)
	}
}

func TestShellJoin(t *testing.T) {
	got := shellJoin([]string{"plain", "a b", "it's", "", "cache:clear"})
	if want := `plain 'a b' 'it'\''s' '' cache:clear`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}
