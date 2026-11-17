package run

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/0ploy/zdev/internal/runtime"
)

type harness struct {
	t      *testing.T
	dir    string
	set    *Set
	rt     *runtime.MockRuntime
	runner *Runner
	out    *bytes.Buffer
	errOut *bytes.Buffer
}

func newHarness(t *testing.T, files map[string]string) *harness {
	t.Helper()
	dir := writeJobs(t, files)
	s, err := Load(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	requireNotBroken(t, s)
	rt := runtime.NewMockRuntime()
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	return &harness{
		t: t, dir: dir, set: s, rt: rt, out: out, errOut: errOut,
		runner: &Runner{
			ProjectDir:   dir,
			ProjectName:  "proj",
			Runtime:      rt,
			HostEnv:      map[string]string{"PROJECTNAME": "proj"},
			ContainerEnv: map[string]string{"PROJECTNAME": "proj", "ZDEV_DOMAIN": "test.dev"},
			Stdin:        strings.NewReader(""),
			Stdout:       out,
			Stderr:       errOut,
		},
	}
}

// run resolves and runs a command line, returning the error.
func (h *harness) run(tokens ...string) error {
	h.t.Helper()
	target, err := h.set.Resolve(tokens)
	if err != nil {
		h.t.Fatalf("resolve %v: %v", tokens, err)
	}
	p, err := h.set.Plan(target, h.dir)
	if err != nil {
		return err
	}
	return h.runner.Run(context.Background(), p)
}

func (h *harness) mustRun(tokens ...string) {
	h.t.Helper()
	if err := h.run(tokens...); err != nil {
		h.t.Fatalf("run %v: %v\nstdout: %s\nstderr: %s", tokens, err, h.out, h.errOut)
	}
}

func requireCode(t *testing.T, err error, code int) *JobError {
	t.Helper()
	var je *JobError
	if !errors.As(err, &je) {
		t.Fatalf("want JobError, got %v", err)
	}
	if je.Code != code {
		t.Fatalf("exit code %d, want %d", je.Code, code)
	}
	return je
}

// --- host jobs (real bash) ---

func TestRun_HostArgsVerbatim(t *testing.T) {
	h := newHarness(t, map[string]string{"args.toml": `run = 'printf "[%s]\n" "$@"'`})
	h.mustRun("args", "cache:clear", "a b", "--help", "")
	if got, want := h.out.String(), "[cache:clear]\n[a b]\n[--help]\n[]\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRun_HostCwdAndWorkdir(t *testing.T) {
	h := newHarness(t, map[string]string{
		"pwd.toml": `
[jobs.root]
run = "pwd -P"
[jobs.sub]
workdir = "packages/{1}"
run = "pwd -P"
`,
	})
	if err := os.MkdirAll(filepath.Join(h.dir, "packages", "api"), 0o755); err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(h.dir)

	h.mustRun("pwd", "root")
	if got := strings.TrimSpace(h.out.String()); got != real {
		t.Errorf("cwd %q, want project root %q", got, real)
	}
	h.out.Reset()
	h.mustRun("pwd", "sub", "api")
	if got := strings.TrimSpace(h.out.String()); got != filepath.Join(real, "packages", "api") {
		t.Errorf("workdir %q", got)
	}
	if err := h.run("pwd", "sub"); err == nil || !strings.Contains(err.Error(), "workdir uses {1}") {
		t.Errorf("missing workdir argument should error, got %v", err)
	}
}

func TestRun_HostEnv(t *testing.T) {
	h := newHarness(t, map[string]string{
		".env.test": "FROM_FILE=file\nOVERRIDDEN=file\n# comment\nexport QUOTED=\"a b\"\n",
		"env.toml": `
env_file = ".zdev/run/.env.test"
env = { OVERRIDDEN = "job", STAGE = "{1}" }
run = 'echo "$PROJECTNAME|$FROM_FILE|$OVERRIDDEN|$QUOTED|$STAGE|${ZDEV_PLAIN:-}"'
`,
	})
	h.runner.Plain = true
	h.mustRun("env", "prod")
	if got, want := strings.TrimSpace(h.out.String()), "proj|file|job|a b|prod|1"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRun_HostFailures(t *testing.T) {
	h := newHarness(t, map[string]string{
		"f.toml": `
[jobs.code]
run = "exit 3"
[jobs.errexit]
run = '''
false
echo after
'''
[jobs.pipefail]
run = '''
false | true
echo after
'''
[jobs.lineno]
run = '''
echo one
zdev_no_such_command_xyz
'''
`,
	})
	requireCode(t, h.run("f", "code"), 3)
	if !strings.Contains(h.errOut.String(), "✗ f code failed (exit 3)") {
		t.Errorf("failure line missing: %q", h.errOut)
	}
	requireCode(t, h.run("f", "errexit"), 1)
	requireCode(t, h.run("f", "pipefail"), 1)
	if strings.Contains(h.out.String(), "after") {
		t.Errorf("a failing line must stop the job: %q", h.out)
	}
	h.errOut.Reset()
	requireCode(t, h.run("f", "lineno"), 127)
	if !strings.Contains(h.errOut.String(), "line 2") {
		t.Errorf("error should point at line 2 of the job body: %q", h.errOut)
	}
}

func TestRun_HostShebangs(t *testing.T) {
	files := map[string]string{
		"sh.toml": "run = '''\n#!/bin/sh\nstep \"from sh\"\necho \"$0 $1\"\n'''",
	}
	if _, err := exec.LookPath("python3"); err == nil {
		files["py.toml"] = "run = '''\n#!/usr/bin/env python3\nimport sys\nprint(sys.argv[1:])\n'''"
	}
	h := newHarness(t, files)
	h.mustRun("sh", "x")
	if got := h.out.String(); !strings.Contains(got, "▶ from sh") || !strings.Contains(got, "sh x") {
		t.Errorf("sh shebang with prelude: %q", got)
	}
	if _, ok := files["py.toml"]; ok {
		h.out.Reset()
		h.mustRun("py", "a b", "c:d")
		if got := strings.TrimSpace(h.out.String()); got != "['a b', 'c:d']" {
			t.Errorf("python: %q", got)
		}
	}
}

func TestRun_HostScript(t *testing.T) {
	h := newHarness(t, map[string]string{
		"s.toml": `
[jobs.ok]
script = "bin/tool.sh --mode {1} {all}"
[jobs.noexec]
script = "bin/plain.sh"
`,
	})
	bin := filepath.Join(h.dir, "bin")
	_ = os.MkdirAll(bin, 0o755)
	_ = os.WriteFile(filepath.Join(bin, "tool.sh"), []byte("#!/bin/sh\nprintf '[%s]' \"$@\"\n"), 0o755)
	_ = os.WriteFile(filepath.Join(bin, "plain.sh"), []byte("#!/bin/sh\n"), 0o644)

	h.mustRun("s", "ok", "prod", "a b")
	// script words, then {all} expanded, then the arguments appended
	if got, want := h.out.String(), "[--mode][prod][prod][a b][prod][a b]"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	err := h.run("s", "noexec")
	if err == nil || !strings.Contains(err.Error(), "not executable") {
		t.Errorf("want not-executable hint, got %v", err)
	}
}

func TestRun_Helpers(t *testing.T) {
	h := newHarness(t, map[string]string{
		"h.toml": `run = '''
step "Installing"
warn "careful"
fail "nope"
echo unreachable
'''`,
	})
	requireCode(t, h.run("h"), 1)
	if !strings.Contains(h.out.String(), "\n\n▶ Installing\n\n") {
		t.Errorf("step: %q", h.out)
	}
	if !strings.Contains(h.errOut.String(), "⚠ careful\n") || !strings.Contains(h.errOut.String(), "✗ nope\n") {
		t.Errorf("warn/fail: %q", h.errOut)
	}
	if strings.Contains(h.out.String(), "unreachable") || strings.Contains(h.out.String(), "\x1b[") {
		t.Errorf("fail must exit; no colors without a terminal: %q", h.out)
	}
}

func TestRun_DependenciesOrderAndDedup(t *testing.T) {
	h := newHarness(t, map[string]string{
		"d.toml": `
[jobs.all]
needs = ["b", "c", "echo {1}", "echo x"]
run = "echo all"
[jobs.b]
needs = ["c"]
run = "echo b"
[jobs.c]
run = "echo c"
[jobs.echo]
run = 'echo "echo $1"'
`,
	})
	h.mustRun("d", "all", "x")
	var lines []string
	for _, l := range strings.Split(h.out.String(), "\n") {
		if l != "" && !strings.HasPrefix(l, "▶") {
			lines = append(lines, l)
		}
	}
	if want := []string{"c", "b", "echo x", "all"}; !reflect.DeepEqual(lines, want) {
		t.Errorf("order %v, want %v (c once, echo x once)", lines, want)
	}
	if !strings.Contains(h.out.String(), "▶ d c") || !strings.Contains(h.out.String(), "▶ d all") {
		t.Errorf("multi-job runs print headers: %q", h.out)
	}
}

func TestRun_OnlyNeedsJob(t *testing.T) {
	h := newHarness(t, map[string]string{
		"d.toml": "[jobs.all]\nneeds = [\"a\", \"b\"]\n[jobs.a]\nrun = \"echo a\"\n[jobs.b]\nrun = \"echo b\"",
	})
	h.mustRun("d", "all")
	out := h.out.String()
	if !strings.Contains(out, "a\n") || !strings.Contains(out, "b\n") || strings.Contains(out, "▶ d all") {
		t.Errorf("dependencies run, the empty job prints nothing: %q", out)
	}
}

func TestRun_FailingDependencyStops(t *testing.T) {
	h := newHarness(t, map[string]string{
		"d.toml": `
[jobs.all]
needs = ["bad"]
run = "echo all"
[jobs.bad]
run = "exit 4"
`,
	})
	requireCode(t, h.run("d", "all"), 4)
	if strings.Contains(h.out.String(), "all\n") && !strings.Contains(h.out.String(), "▶ d all") {
		t.Errorf("all must not run after bad failed: %q", h.out)
	}
	if strings.Contains(h.out.String(), "▶ d all") {
		t.Errorf("all must not start: %q", h.out)
	}
}

func TestRun_SingleJobNoHeader(t *testing.T) {
	h := newHarness(t, map[string]string{"t.toml": `run = "echo hi"`})
	h.mustRun("t")
	if h.out.String() != "hi\n" {
		t.Errorf("single job prints only its output: %q", h.out)
	}
}

func TestRun_Quiet(t *testing.T) {
	h := newHarness(t, map[string]string{
		"q.toml": `
output = "quiet"
[jobs.ok]
run = '''
echo noisy
step "Building"
echo "⚠ plain warning from any language" >&2
'''
[jobs.bad]
run = '''
step "Before failing"
echo noisy
echo boom >&2
exit 2
'''
`,
	})
	h.mustRun("q", "ok")
	out := h.out.String()
	if strings.Contains(out, "noisy") {
		t.Errorf("quiet job output leaked: %q", out)
	}
	if !strings.Contains(out, "▶ Building") || !strings.Contains(out, "✓ q ok (") {
		t.Errorf("want marker passthrough and success line: %q", out)
	}
	if !strings.Contains(h.errOut.String(), "⚠ plain warning from any language") {
		t.Errorf("warning marker: %q", h.errOut)
	}

	h.out.Reset()
	h.errOut.Reset()
	requireCode(t, h.run("q", "bad"), 2)
	if e := h.errOut.String(); !strings.HasSuffix(e, "✗ q bad failed (exit 2)\nnoisy\nboom\n") {
		t.Errorf("failure must dump captured output without the markers already shown: %q", e)
	}
	if strings.Count(h.out.String(), "▶ Before failing") != 1 {
		t.Errorf("marker shown live exactly once: %q", h.out)
	}

	h.out.Reset()
	h.runner.Verbose = true
	h.mustRun("q", "ok")
	if !strings.Contains(h.out.String(), "noisy") || strings.Contains(h.out.String(), "✓") {
		t.Errorf("verbose forces show: %q", h.out)
	}
}

func TestRun_Confirm(t *testing.T) {
	files := map[string]string{
		"c.toml": `
[jobs.all]
needs = ["prep"]
confirm = "Deploy to {1}?"
run = "echo deployed"
[jobs.prep]
run = "echo prep"
`,
	}

	h := newHarness(t, files)
	err := h.run("c", "all", "prod")
	if err == nil || !strings.Contains(err.Error(), `asks "Deploy to prod?"; pass --yes`) {
		t.Errorf("non-terminal without --yes: %v", err)
	}
	if h.out.Len() != 0 {
		t.Errorf("nothing may run before confirmation: %q", h.out)
	}

	h = newHarness(t, files)
	h.runner.Terminal = true
	h.runner.Stdin = strings.NewReader("n\n")
	if err := h.run("c", "all", "prod"); !errors.Is(err, ErrAborted) {
		t.Errorf("no: %v", err)
	}
	if h.out.Len() != 0 || !strings.Contains(h.errOut.String(), "Deploy to prod? [y/N]") {
		t.Errorf("out %q err %q", h.out, h.errOut)
	}

	h = newHarness(t, files)
	h.runner.Terminal = true
	h.runner.Stdin = strings.NewReader("y\n")
	h.mustRun("c", "all", "prod")

	h = newHarness(t, files)
	h.runner.Yes = true
	h.mustRun("c", "all", "prod")
	if !strings.Contains(h.out.String(), "deployed") {
		t.Errorf("--yes: %q", h.out)
	}
}

func TestPlan_Placeholders(t *testing.T) {
	h := newHarness(t, map[string]string{
		"p.toml": `
[jobs.all]
needs = ["build {1} {all}"]
confirm = "Ship {1}?"
env = { WHO = "{2}", ALL = "{all}" }
run = "true"
[jobs.build]
service = "app"
workdir = "/srv/{1}"
script = "bin/build {all}"
`,
	})
	target, _ := h.set.Resolve([]string{"p", "all", "api", "x y"})
	p, err := h.set.Plan(target, h.dir)
	if err != nil {
		t.Fatal(err)
	}
	build, all := p.Steps[0], p.Steps[1]
	if want := []string{"api", "api", "x y"}; !reflect.DeepEqual(build.Args, want) {
		t.Errorf("dependency args %q, want %q", build.Args, want)
	}
	if build.Workdir != "/srv/api" {
		t.Errorf("container workdir %q", build.Workdir)
	}
	if want := []string{"bin/build", "api", "api", "x y"}; !reflect.DeepEqual(build.Script, want) {
		t.Errorf("container script stays relative, {all} splits: %q", build.Script)
	}
	if all.Confirm != "Ship api?" || all.Env["WHO"] != "x y" || all.Env["ALL"] != "api x y" {
		t.Errorf("all: %+v", all)
	}
}

func TestPlan_MissingNeedsArgument(t *testing.T) {
	h := newHarness(t, map[string]string{
		"p.toml": "[jobs.a]\nneeds = [\"b {1}\"]\nrun = \"true\"\n[jobs.b]\nrun = \"true\"",
	})
	err := h.run("p", "a")
	if err == nil || !strings.Contains(err.Error(), `needs "b {1}" uses {1}`) {
		t.Errorf("got %v", err)
	}
}

// --- container jobs (mock runtime) ---

func execCalls(rt *runtime.MockRuntime) []runtime.MockCall {
	var calls []runtime.MockCall
	for _, c := range rt.Calls {
		if c.Method == "ExecStream" {
			calls = append(calls, c)
		}
	}
	return calls
}

func TestRun_ContainerNotRunning(t *testing.T) {
	h := newHarness(t, map[string]string{
		"c.toml": "[jobs.a]\nneeds = [\"b\"]\nservice = \"app\"\nrun = \"x\"\n[jobs.b]\nrun = \"echo b\"",
	})
	err := h.run("c", "a")
	if err == nil || !strings.Contains(err.Error(), "service app, which is not running") {
		t.Errorf("got %v", err)
	}
	if h.out.Len() != 0 {
		t.Errorf("host dependency must not run either: %q", h.out)
	}
}

func TestRun_ContainerDefaultBody(t *testing.T) {
	h := newHarness(t, map[string]string{
		"c.toml": `
service = "app"
user = "www-data"
workdir = "/srv"
env = { STAGE = "{1}" }
run = "bin/console cache:clear"
`,
	})
	h.rt.ContainersRunning["app.proj.zdev"] = true
	h.mustRun("c", "prod", "--force")

	calls := execCalls(h.rt)
	if len(calls) != 1 {
		t.Fatalf("calls: %+v", calls)
	}
	container, argv, opts := calls[0].Args[0].(string), calls[0].Args[1].([]string), calls[0].Args[2].(runtime.ExecStreamOptions)
	if container != "app.proj.zdev" {
		t.Errorf("container %q", container)
	}
	want := []string{"sh", "-c", containerLauncher, "auto", prelude, "bin/console cache:clear", "c", "prod", "--force"}
	if !reflect.DeepEqual(argv, want) {
		t.Errorf("argv\n got %q\nwant %q", argv, want)
	}
	if opts.User != "www-data" || opts.Workdir != "/srv" || !opts.Interactive || opts.TTY {
		t.Errorf("opts: %+v", opts)
	}
	if want := map[string]string{"PROJECTNAME": "proj", "ZDEV_DOMAIN": "test.dev", "STAGE": "prod"}; !reflect.DeepEqual(opts.Env, want) {
		t.Errorf("container env %v (no host env), want %v", opts.Env, want)
	}
}

func TestRun_ContainerQuietAndExitCode(t *testing.T) {
	h := newHarness(t, map[string]string{
		"c.toml": "service = \"app\"\noutput = \"quiet\"\nrun = \"migrate\"",
	})
	h.rt.ContainersRunning["app.proj.zdev"] = true
	h.runner.Terminal = true
	h.rt.ExecStreamFunc = func(_ string, _ []string, opts runtime.ExecStreamOptions) error {
		io.WriteString(opts.Stdout, "▶ Migrating\nlots of output\n")
		return errors.New("docker exec failed")
	}
	requireCode(t, h.run("c"), 1)
	opts := execCalls(h.rt)[0].Args[2].(runtime.ExecStreamOptions)
	if opts.Interactive || opts.TTY || opts.Stdin != nil {
		t.Errorf("quiet jobs get no stdin and no TTY: %+v", opts)
	}
	if !strings.Contains(h.out.String(), "▶ Migrating") || !strings.Contains(h.errOut.String(), "lots of output") {
		t.Errorf("out %q err %q", h.out, h.errOut)
	}
}

func TestRun_ContainerScriptAndShebangs(t *testing.T) {
	h := newHarness(t, map[string]string{
		"c.toml": `
service = "app"
[jobs.script]
script = "bin/run.sh {1}"
[jobs.bash]
run = "#!/bin/bash\necho hi"
[jobs.py]
run = "#!/usr/bin/env python3\nprint(1)"
`,
	})
	h.rt.ContainersRunning["app.proj.zdev"] = true
	h.rt.ExecStreamFunc = func(_ string, argv []string, opts runtime.ExecStreamOptions) error {
		if strings.Contains(strings.Join(argv, " "), "mktemp") {
			body, _ := io.ReadAll(opts.Stdin)
			if string(body) != "#!/usr/bin/env python3\nprint(1)" {
				t.Errorf("script body on stdin: %q", body)
			}
			io.WriteString(opts.Stdout, "/tmp/zdev-run.abc\n")
		}
		return nil
	}

	h.mustRun("c", "script", "x")
	h.mustRun("c", "bash", "y")
	h.mustRun("c", "py", "z")
	calls := execCalls(h.rt)
	if len(calls) != 4 {
		t.Fatalf("want 4 exec calls, got %d", len(calls))
	}
	argv := func(i int) []string { return calls[i].Args[1].([]string) }

	if want := []string{"bin/run.sh", "x", "x"}; !reflect.DeepEqual(argv(0), want) {
		t.Errorf("script: %q", argv(0))
	}
	if want := []string{"sh", "-c", containerLauncher, "/bin/bash", prelude, "#!/bin/bash\necho hi", "c bash", "y"}; !reflect.DeepEqual(argv(1), want) {
		t.Errorf("bash shebang: %q", argv(1))
	}
	if got := argv(3); got[0] != "sh" || got[3] != "/tmp/zdev-run.abc" || got[4] != "z" || !strings.Contains(got[2], `rm -f "$f"`) {
		t.Errorf("python run: %q", got)
	}
}

func TestParseShebang(t *testing.T) {
	cases := []struct {
		body  string
		nil   bool
		shell bool
	}{
		{"echo hi", true, false},
		{"#!/bin/bash\n", false, true},
		{"#!/usr/bin/env bash\n", false, true},
		{"#!/bin/sh -x\n", false, true},
		{"#!/usr/bin/env python3\n", false, false},
		{"#!/usr/bin/env -S bash -x\n", false, false},
		{"#!\n", true, false},
	}
	for _, tc := range cases {
		sb := parseShebang(tc.body)
		if (sb == nil) != tc.nil || (sb != nil && sb.shell != tc.shell) {
			t.Errorf("%q: got %+v", tc.body, sb)
		}
	}
}

func TestParseMarker(t *testing.T) {
	cases := map[string]string{
		"▶ Building":                  "Building",
		"\x1b[1;36m▶ Building\x1b[0m": "Building",
		"  ⚠ careful ":                "careful",
		"✗ broke":                     "broke",
		"plain output":                "",
		"prefix ▶ not at line start":  "",
	}
	for line, want := range cases {
		_, msg, ok := parseMarker(line)
		if ok != (want != "") || msg != want {
			t.Errorf("%q: got %q %v", line, msg, ok)
		}
	}
}

func TestReadDotenv(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	_ = os.WriteFile(path, []byte("A=1\n  export B = two \nC='x # y'\nD=val # comment\n\n# c\nE=\n"), 0o644)
	env, err := readDotenv(path)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"A": "1", "B": "two", "C": "x # y", "D": "val", "E": ""}
	if !reflect.DeepEqual(env, want) {
		t.Errorf("got %v", env)
	}
	_ = os.WriteFile(path, []byte("not a pair\n"), 0o644)
	if _, err := readDotenv(path); err == nil || !strings.Contains(err.Error(), ":1:") {
		t.Errorf("want line error, got %v", err)
	}
}
