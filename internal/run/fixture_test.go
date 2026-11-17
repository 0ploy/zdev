package run

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/runtime"
)

// The full fixture project (testdata/projects/full) ships job files that use
// every feature; these tests keep them loadable and runnable.

func loadFullFixture(t *testing.T) (string, *Set) {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "testdata", "projects", "full"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadProject(dir)
	if err != nil {
		t.Fatalf("fixture config: %v", err)
	}
	var services []string
	for name := range cfg.Services {
		services = append(services, name)
	}
	set, err := Load(dir, Options{Services: services})
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Broken) > 0 || len(set.Warnings) > 0 {
		t.Fatalf("fixture job files must load cleanly: broken %v, warnings %v", set.Broken, set.Warnings)
	}
	return dir, set
}

func runFixture(t *testing.T, dir string, set *Set, tokens ...string) string {
	t.Helper()
	target, err := set.Resolve(tokens)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := set.Plan(target, dir)
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	r := &Runner{
		ProjectDir: dir, ProjectName: "full", Runtime: runtime.NewMockRuntime(),
		HostEnv: map[string]string{"PROJECTNAME": "full", "PROJECTPATH": dir, "PROJECTDIR": "full", "ZDEV_DOMAIN": "test.dev"},
		Yes:     true,
		Stdin:   strings.NewReader(""), Stdout: &out, Stderr: &errOut,
	}
	if err := r.Run(context.Background(), plan); err != nil {
		t.Fatalf("zdev run %v: %v\nstdout: %s\nstderr: %s", tokens, err, out.String(), errOut.String())
	}
	return out.String()
}

func TestFullFixture_Loads(t *testing.T) {
	_, set := loadFullFixture(t)
	var names []string
	for _, f := range set.Files {
		names = append(names, f.Name)
	}
	if got := strings.Join(names, ","); got != "console,deploy,setup,test" {
		t.Errorf("files %s", got)
	}
}

func TestFullFixture_SetupAndTest(t *testing.T) {
	dir, set := loadFullFixture(t)

	out := runFixture(t, dir, set, "setup", "all")
	if !strings.Contains(out, "▶ Installing dependencies") || !strings.Contains(out, "Setup complete for full") ||
		strings.Index(out, "Installing") > strings.Index(out, "Setup complete") {
		t.Errorf("setup all: %q", out)
	}

	out = runFixture(t, dir, set, "setup", "env")
	for _, want := range []string{"PROJECTNAME: full", "PROJECTPATH: " + dir, "PROJECTDIR: full", "ZDEV_DOMAIN: test.dev"} {
		if !strings.Contains(out, want) {
			t.Errorf("setup env missing %q: %q", want, out)
		}
	}

	out = runFixture(t, dir, set, "test", "all")
	if !strings.Contains(out, "Running unit tests...") || !strings.Contains(out, "▶ Linting") ||
		!strings.Contains(out, "✓ test lint (") || strings.Contains(out, "lint details") {
		t.Errorf("test all (needs-only job, quiet lint): %q", out)
	}

	out = runFixture(t, dir, set, "test", "run", "cache:clear", "a b")
	if !strings.Contains(out, "Running tests with arg: cache:clear\nRunning tests with arg: a b\n") {
		t.Errorf("test run args: %q", out)
	}

	if out := runFixture(t, dir, set, "test", "u"); !strings.Contains(out, "Running unit tests...") {
		t.Errorf("alias u: %q", out)
	}
}

func TestFullFixture_DeployRelease(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	dir, set := loadFullFixture(t)
	out := runFixture(t, dir, set, "deploy", "release", "prod")
	for _, want := range []string{
		"Running unit tests...",                       // cross-file dependency
		"building in scripts with: --stage prod prod", // script, workdir, placeholder + appended args
		"notified prod",                               // hidden python job, env placeholder
		"▶ Releasing to prod",
		"region=eu-central stage=prod", // env_file, overridden by env
	} {
		if !strings.Contains(out, want) {
			t.Errorf("deploy release missing %q:\n%s", want, out)
		}
	}
}

func TestFullFixture_ContainerJob(t *testing.T) {
	dir, set := loadFullFixture(t)
	target, err := set.Resolve([]string{"c", "cache:clear"})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := set.Plan(target, dir)
	if err != nil {
		t.Fatal(err)
	}
	rt := runtime.NewMockRuntime()
	rt.ContainersRunning["app.full.zdev"] = true
	r := &Runner{ProjectDir: dir, ProjectName: "full", Runtime: rt,
		Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
	if err := r.Run(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	var call runtime.MockCall
	for _, c := range rt.Calls {
		if c.Method == "ExecStream" {
			call = c
		}
	}
	opts, ok := call.Args[2].(runtime.ExecStreamOptions)
	if !ok || call.Args[0] != "app.full.zdev" || opts.Workdir != "/app" || opts.Env["APP_DEBUG"] != "1" {
		t.Errorf("container exec: %+v", call)
	}
}
