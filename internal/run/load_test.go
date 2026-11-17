package run

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// writeJobs creates a project dir with the given .zdev/run files.
func writeJobs(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	runDir := Dir(dir)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		path := filepath.Join(runDir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func load(t *testing.T, opts Options, files map[string]string) *Set {
	t.Helper()
	s, err := Load(writeJobs(t, files), opts)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return s
}

// brokenMsg returns the error for a broken file, failing if it isn't broken.
func brokenMsg(t *testing.T, s *Set, name string) string {
	t.Helper()
	err, ok := s.Broken[name]
	if !ok {
		t.Fatalf("expected %s to be broken, broken: %v", name, s.Broken)
	}
	return err.Error()
}

func requireNotBroken(t *testing.T, s *Set) {
	t.Helper()
	if len(s.Broken) > 0 {
		t.Fatalf("unexpected broken files: %v", s.Broken)
	}
}

func TestLoad_MissingDir(t *testing.T) {
	s, err := Load(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Files) != 0 || len(s.Broken) != 0 {
		t.Fatalf("expected empty set, got %+v", s)
	}
}

func TestLoad_SingleJobFile(t *testing.T) {
	s := load(t, Options{Services: []string{"app"}}, map[string]string{
		"test.toml": `
description = "Run the test suite"
aliases = ["t"]
service = "app"
user = "www-data"
output = "quiet"
env = { APP_ENV = "test" }
run = '''
vendor/bin/phpunit "$@"
'''
`,
	})
	requireNotBroken(t, s)
	if len(s.Files) != 1 {
		t.Fatalf("want 1 file, got %d", len(s.Files))
	}
	f := s.Files[0]
	if !f.Single || f.Name != "test" || f.Description != "Run the test suite" {
		t.Fatalf("unexpected file: %+v", f)
	}
	if !reflect.DeepEqual(f.Aliases, []string{"t"}) {
		t.Errorf("file aliases = %v", f.Aliases)
	}
	j := f.Jobs[0]
	if j.Name != "" || j.ID() != "test" {
		t.Errorf("single job name %q, id %q", j.Name, j.ID())
	}
	if j.Run != "vendor/bin/phpunit \"$@\"\n" {
		t.Errorf("run body not verbatim: %q", j.Run)
	}
	if j.Service != "app" || j.User != "www-data" || j.Output != OutputQuiet || j.Env["APP_ENV"] != "test" {
		t.Errorf("unexpected job: %+v", j)
	}
	if j.Aliases != nil {
		t.Errorf("single-job aliases belong to the file, job has %v", j.Aliases)
	}
}

func TestLoad_MultiJobDefaultsAndOrder(t *testing.T) {
	s := load(t, Options{Services: []string{"app"}}, map[string]string{
		"deploy.toml": `
description = "Deployment"
service = "host"
output = "quiet"
env = { STAGE = "staging", REGION = "eu" }

[jobs.migrate]
service = "app"
user = "www-data"
run = "bin/console doctrine:migrations:migrate -n"

[jobs.all]
description = "Build, migrate, release"
needs = ["build", "migrate"]
output = "show"
env = { STAGE = "production" }
run = "./scripts/release.sh"

[jobs.build]
script = "scripts/build.sh {all}"
`,
	})
	requireNotBroken(t, s)
	f := s.Files[0]
	if f.Single || f.Description != "Deployment" {
		t.Fatalf("unexpected file: %+v", f)
	}
	var names []string
	for _, j := range f.Jobs {
		names = append(names, j.Name)
	}
	if !reflect.DeepEqual(names, []string{"migrate", "all", "build"}) {
		t.Errorf("jobs not in file order: %v", names)
	}

	migrate, all, build := f.Job("migrate"), f.Job("all"), f.Job("build")
	if migrate.Service != "app" || migrate.Output != OutputQuiet || migrate.Description != "" {
		t.Errorf("migrate should override service, inherit output, not inherit description: %+v", migrate)
	}
	if all.Output != OutputShow || all.Service != ServiceHost {
		t.Errorf("all: %+v", all)
	}
	if want := map[string]string{"STAGE": "production", "REGION": "eu"}; !reflect.DeepEqual(all.Env, want) {
		t.Errorf("env should merge per variable, got %v", all.Env)
	}
	if build.Env["STAGE"] != "staging" {
		t.Errorf("build should inherit file env, got %v", build.Env)
	}
	if all.ID() != "deploy all" {
		t.Errorf("ID = %q", all.ID())
	}
}

func TestLoad_Defaults(t *testing.T) {
	s := load(t, Options{}, map[string]string{"x.toml": `run = "true"`})
	requireNotBroken(t, s)
	j := s.Files[0].Jobs[0]
	if j.Service != ServiceHost || j.Output != OutputShow || j.Env != nil {
		t.Errorf("unexpected defaults: %+v", j)
	}
}

func TestLoad_FileErrors(t *testing.T) {
	cases := []struct {
		name, content, want string
	}{
		{"both", "run = \"a\"\n[jobs.x]\nrun = \"b\"", "both a top-level run/script and [jobs.*]"},
		{"nojob", `description = "nothing"`, "defines no job"},
		{"emptyjobs", "[jobs]", "defines no job"},
		{"runandscript", "[jobs.x]\nrun = \"a\"\nscript = \"b.sh\"", "jobs.x: has both run and script"},
		{"emptyrun", `run = "  "`, "needs a run or script"},
		{"emptyjob", "[jobs.a]\ndescription = \"x\"", "needs a run or script"},
		{"output", "output = \"loud\"\nrun = \"a\"", `output must be "show" or "quiet"`},
		{"hostuser", "user = \"root\"\nrun = \"a\"", "user only applies to container jobs"},
		{"inheriteduser", "service = \"app\"\nuser = \"www-data\"\n[jobs.x]\nservice = \"host\"\nrun = \"a\"", "jobs.x: user only applies"},
		{"service", "service = \"db\"\nrun = \"a\"", `service "db" is not defined`},
		{"envkey", "env = { \"MY-VAR\" = \"x\" }\nrun = \"a\"", `invalid variable name "MY-VAR"`},
		{"envtype", "env = { PORT = 8080 }\nrun = \"a\"", "PORT"},
		{"placeholder0", "workdir = \"x/{0}\"\nrun = \"a\"", "invalid placeholder {0}"},
		{"placeholder01", "confirm = \"{01}?\"\nrun = \"a\"", "invalid placeholder {01}"},
		{"fileneeds", "needs = [\"x\"]\n[jobs.x]\nrun = \"a\"", "needs is only allowed on a job"},
		{"filehidden", "hidden = true\n[jobs.x]\nrun = \"a\"", "hidden is only allowed on a job"},
		{"jobname", "[jobs.Build]\nrun = \"a\"", "jobs.Build: invalid job name"},
		{"aliasname", "aliases = [\"-x\"]\nrun = \"a\"", `invalid name "-x"`},
		{"jobalias", "[jobs.a]\nrun = \"a\"\n[jobs.b]\naliases = [\"a\"]\nrun = \"b\"", `alias "a" collides with job "a"`},
		{"syntax", "run = '''unterminated", "x"},
		{"emptyneed", "[jobs.a]\nneeds = [\" \"]\nrun = \"a\"", "needs: empty entry"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := load(t, Options{Services: []string{"app"}}, map[string]string{tc.name + ".toml": tc.content})
			if msg := brokenMsg(t, s, tc.name); !strings.Contains(msg, tc.want) {
				t.Errorf("error %q does not contain %q", msg, tc.want)
			}
			if len(s.Files) != 0 {
				t.Errorf("broken file must not be in Files")
			}
		})
	}
}

func TestLoad_CollectsAllProblems(t *testing.T) {
	s := load(t, Options{}, map[string]string{"x.toml": `
[jobs.a]
output = "loud"
run = "a"
[jobs.b]
user = "root"
run = "b"
`})
	msg := brokenMsg(t, s, "x")
	for _, want := range []string{"jobs.a: output", "jobs.b: user"} {
		if !strings.Contains(msg, want) {
			t.Errorf("%q missing %q", msg, want)
		}
	}
}

func TestLoad_ServiceCheckSkippedWithoutServices(t *testing.T) {
	s := load(t, Options{}, map[string]string{"x.toml": "service = \"whatever\"\nrun = \"a\""})
	requireNotBroken(t, s)
}

func TestLoad_InvalidFileNameAndIgnoredEntries(t *testing.T) {
	s := load(t, Options{}, map[string]string{
		"Deploy.toml":         `run = "a"`,
		".#deploy.toml":       `garbage`,
		"notes.md":            `# not a job`,
		"scripts/deploy.toml": `garbage`, // subdirectories are ignored
		"ok.toml":             `run = "a"`,
	})
	if msg := brokenMsg(t, s, "Deploy"); !strings.Contains(msg, "invalid file name") {
		t.Errorf("got %q", msg)
	}
	if len(s.Broken) != 1 || len(s.Files) != 1 || s.Files[0].Name != "ok" {
		t.Errorf("files %v, broken %v", s.Files, s.Broken)
	}
}

func TestLoad_UnknownKeysWarn(t *testing.T) {
	s := load(t, Options{}, map[string]string{"x.toml": `
timeout = 5
[jobs.a]
run = "a"
retries = 3
`})
	requireNotBroken(t, s)
	got := strings.Join(s.Warnings, "\n")
	for _, want := range []string{`"timeout"`, `"jobs.a.retries"`} {
		if !strings.Contains(got, want) {
			t.Errorf("warnings %q missing %s", got, want)
		}
	}
}

func TestLoad_OneBrokenFileKeepsOthers(t *testing.T) {
	s := load(t, Options{}, map[string]string{
		"bad.toml":  `output = "loud"` + "\nrun = \"a\"",
		"good.toml": `run = "a"`,
	})
	if len(s.Files) != 1 || s.Files[0].Name != "good" {
		t.Fatalf("files: %v", s.Files)
	}
	brokenMsg(t, s, "bad")
}

func TestLoad_FileAliasCollisions(t *testing.T) {
	s := load(t, Options{}, map[string]string{
		"a.toml":      "aliases = [\"x\"]\nrun = \"a\"",
		"b.toml":      "aliases = [\"x\"]\nrun = \"b\"",
		"c.toml":      "aliases = [\"a\"]\nrun = \"c\"",
		"d.toml":      "aliases = [\"broken\"]\nrun = \"d\"",
		"broken.toml": `nope = `,
	})
	if _, ok := s.Broken["a"]; ok {
		t.Errorf("a sorts first and keeps alias x")
	}
	if msg := brokenMsg(t, s, "b"); !strings.Contains(msg, "an alias of a.toml") {
		t.Errorf("b: %q", msg)
	}
	if msg := brokenMsg(t, s, "c"); !strings.Contains(msg, "file a.toml") {
		t.Errorf("c: %q", msg)
	}
	if msg := brokenMsg(t, s, "d"); !strings.Contains(msg, "file broken.toml") {
		t.Errorf("d: a broken file still owns its name, got %q", msg)
	}
}

func TestLoad_Needs(t *testing.T) {
	s := load(t, Options{}, map[string]string{
		"build.toml": `run = "make"`,
		"db.toml": `
[jobs.reset]
run = "reset"
[jobs.seed]
run = "seed"
`,
		"deploy.toml": `
[jobs.all]
needs = ["prep", "build {1}", "db reset", "test"]
run = "release"
[jobs.prep]
hidden = true
run = "prep"
[jobs.test]
run = "same-file test wins over test.toml"
`,
		"test.toml": `run = "phpunit"`,
	})
	requireNotBroken(t, s)
	all := s.byName["deploy"].Job("all")
	want := []struct {
		id   string
		args []string
	}{
		{"deploy prep", []string{}},
		{"build", []string{"{1}"}},
		{"db reset", []string{}},
		{"deploy test", []string{}},
	}
	for i, n := range all.Needs {
		dep, args, err := s.resolveNeed(all, n)
		if err != nil {
			t.Fatalf("%q: %v", n, err)
		}
		if dep.ID() != want[i].id || len(args) != len(want[i].args) || (len(args) > 0 && args[0] != want[i].args[0]) {
			t.Errorf("%q resolved to %q %v, want %q %v", n, dep.ID(), args, want[i].id, want[i].args)
		}
	}
}

func TestLoad_NeedsErrors(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"unknown", map[string]string{"a.toml": "needs = [\"nope\"]\nrun = \"a\""}, `unknown command "nope"`},
		{"multinojob", map[string]string{
			"a.toml":  "needs = [\"db\"]\nrun = \"a\"",
			"db.toml": "[jobs.reset]\nrun = \"r\"",
		}, "db.toml has several jobs"},
		{"unknownjob", map[string]string{
			"a.toml":  "needs = [\"db nuke\"]\nrun = \"a\"",
			"db.toml": "[jobs.reset]\nrun = \"r\"",
		}, `db has no job "nuke"`},
		{"self", map[string]string{"a.toml": "[jobs.x]\nneeds = [\"x\"]\nrun = \"a\""}, "can't depend on itself"},
		{"selfsingle", map[string]string{"a.toml": "needs = [\"a\"]\nrun = \"a\""}, "can't depend on itself"},
		{"placeholderfile", map[string]string{"a.toml": "needs = [\"{1}\"]\nrun = \"a\""}, "can't be a placeholder"},
		{"placeholderjob", map[string]string{
			"a.toml":  "needs = [\"db {1}\"]\nrun = \"a\"",
			"db.toml": "[jobs.reset]\nrun = \"r\"",
		}, "job name can't be a placeholder"},
		{"brokendep", map[string]string{
			"a.toml": "needs = [\"b\"]\nrun = \"a\"",
			"b.toml": `output = "loud"` + "\nrun = \"b\"",
		}, "output must be"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := load(t, Options{}, tc.files)
			if msg := brokenMsg(t, s, "a"); !strings.Contains(msg, tc.want) {
				t.Errorf("error %q does not contain %q", msg, tc.want)
			}
		})
	}
}

func TestLoad_NeedsBrokenCascades(t *testing.T) {
	s := load(t, Options{}, map[string]string{
		"a.toml": "needs = [\"b\"]\nrun = \"a\"",
		"b.toml": "needs = [\"c\"]\nrun = \"b\"",
		"c.toml": "needs = [\"missing\"]\nrun = \"c\"",
		"d.toml": `run = "independent"`,
	})
	for _, n := range []string{"a", "b", "c"} {
		brokenMsg(t, s, n)
	}
	if len(s.Files) != 1 || s.Files[0].Name != "d" {
		t.Errorf("only d should survive, got %v", s.Files)
	}
}

func TestLoad_Cycle(t *testing.T) {
	s := load(t, Options{}, map[string]string{
		"a.toml": `
[jobs.x]
needs = ["y"]
run = "x"
[jobs.y]
needs = ["b"]
run = "y"
`,
		"b.toml":    "needs = [\"a x\"]\nrun = \"b\"",
		"user.toml": "needs = [\"b\"]\nrun = \"depends on the cycle\"",
		"ok.toml":   `run = "fine"`,
	})
	msg := brokenMsg(t, s, "a")
	if !strings.Contains(msg, "dependency cycle: ") || !strings.Contains(msg, "a x") || !strings.Contains(msg, "b") {
		t.Errorf("a: %q", msg)
	}
	brokenMsg(t, s, "b")
	brokenMsg(t, s, "user")
	if len(s.Files) != 1 || s.Files[0].Name != "ok" {
		t.Errorf("only ok should survive, got %v", s.Files)
	}
}
