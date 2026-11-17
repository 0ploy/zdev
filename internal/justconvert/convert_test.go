package justconvert

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0ploy/zdev/internal/run"
	"github.com/0ploy/zdev/internal/runtime"
)

// The testdata/*.json dumps were generated with just 1.49.0 (config.JustVersion):
//
//	just --justfile testdata/<name>.just --dump --dump-format json > testdata/<name>.json
func convertFixture(t *testing.T, name string) *Result {
	t.Helper()
	source, err := os.ReadFile(filepath.Join("testdata", name+".just"))
	if err != nil {
		t.Fatal(err)
	}
	dump, err := os.ReadFile(filepath.Join("testdata", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := Convert(name, dump, source)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// loadConverted writes the converted file into a fresh project and loads it,
// failing if zdev can't use it.
func loadConverted(t *testing.T, name, toml string) (string, *run.Set) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(run.Dir(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(run.Dir(dir), name+".toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	set, err := run.Load(dir, run.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := set.Broken[name]; err != nil {
		t.Fatalf("converted file doesn't load: %v\n%s", err, toml)
	}
	return dir, set
}

func requireContains(t *testing.T, haystack string, needles ...string) {
	t.Helper()
	for _, n := range needles {
		if !strings.Contains(haystack, n) {
			t.Errorf("missing %q in:\n%s", n, haystack)
		}
	}
}

func TestConvert_MultiRecipe(t *testing.T) {
	res := convertFixture(t, "deploy")
	if len(res.Problems) > 0 {
		t.Fatalf("problems: %v", res.Problems)
	}
	_, set := loadConverted(t, "deploy", res.TOML)

	requireContains(t, res.TOML,
		`[jobs.all]`,
		`description = "Ship it everywhere"`,
		`needs = ["build", "notify {1}"]`,
		`confirm = "Ship it?"`,
		// required-argument check sits on the first body line, keeping line numbers
		`if [ $# -lt 1 ]; then fail "usage: zdev run deploy all <target> [rest...]"; fi; step "Deploying ${1}"`,
		// in quotes: raw text like just; standalone word: "${@:2}"
		`echo "rest: ${*:2}" "${@:2}"`,
		"false || true\n",
		"echo continued \\\n  line\n",
		`aliases = ["b"]`,
		"[jobs.notify]\nhidden = true\n",
		`echo "notify ${1:-ops}" glued=${1:-ops}x`,
		"[jobs.summary]\nneeds = [\"build\"]\n\n",
		"#!/usr/bin/env python3\nprint(\"hi\")\n",
	)
	if strings.Contains(res.TOML, "@zdev") || strings.Contains(res.TOML, "-false") {
		t.Errorf("@ and - prefixes must be gone:\n%s", res.TOML)
	}
	if !res.Echoed {
		t.Error("lines without @ were echoed by just")
	}
	notes := strings.Join(res.Notes, "\n")
	requireContains(t, notes,
		"line 6 `cd` now affects the following lines",
		"bare `zdev deploy` ran recipe all; there is no default job any more: zdev run deploy all",
	)

	// file order is kept
	target, _ := set.Resolve([]string{"deploy"})
	var names []string
	for _, j := range target.File.Jobs {
		names = append(names, j.Name)
	}
	if got := strings.Join(names, ","); got != "all,build,notify,summary,py" {
		t.Errorf("job order %s", got)
	}
}

func TestConvert_SingleRecipeNamedAfterFile(t *testing.T) {
	res := convertFixture(t, "console")
	if len(res.Problems) > 0 {
		t.Fatalf("problems: %v", res.Problems)
	}
	loadConverted(t, "console", res.TOML)
	requireContains(t, res.TOML, `description = "Symfony console"`, "zdev exec app php bin/console \"$@\"\n")
	if strings.Contains(res.TOML, "[jobs") {
		t.Errorf("a single recipe becomes a single-job file:\n%s", res.TOML)
	}
	if len(res.Notes) != 0 {
		t.Errorf("nothing to review: %v", res.Notes)
	}
}

func TestConvert_VariablesAndFunctions(t *testing.T) {
	res := convertFixture(t, "db")
	if len(res.Problems) > 0 {
		t.Fatalf("problems: %v", res.Problems)
	}
	loadConverted(t, "db", res.TOML)
	requireContains(t, res.TOML,
		"#!/bin/sh\necho \"resetting mysql://dev@db/app\" from \"${ZDEV_CALLER_DIR}\"\n",
	)
	requireContains(t, strings.Join(res.Notes, "\n"), "`zdev db reset` becomes `zdev run db`")
}

func TestConvert_Refuses(t *testing.T) {
	res := convertFixture(t, "refuse")
	if res.TOML != "" {
		t.Errorf("a refused file must not produce output:\n%s", res.TOML)
	}
	requireContains(t, strings.Join(res.Problems, "\n"),
		"uses set export",
		"variable up is an expression",
		"recipe a: attribute [no-cd] has no zdev equivalent",
		"uses OS attributes ([linux])",
		"recipe a: dependency b runs after the recipe (&&)",
		"recipe b: interpolates {{...}} into a /usr/bin/env python3 script",
	)
}

func TestTomlStrings(t *testing.T) {
	if got := tomlString("a \"b\" \\ \x1b\n"); got != `"a \"b\" \\ \u001B\n"` {
		t.Errorf("tomlString: %s", got)
	}
	if got := tomlMultiline("x '''y''' \\z\n"); got != "\"\"\"\nx '''y''' \\\\z\n\"\"\"" {
		t.Errorf("tomlMultiline fallback: %q", got)
	}
}

// TestConvert_SameOutputAsJust runs the original recipes with just and the
// converted jobs with zdev's runner and compares what they print. Arguments
// without spaces: with spaces, quoting standalone parameters is a deliberate
// improvement over just's word splitting.
func TestConvert_SameOutputAsJust(t *testing.T) {
	justBin, err := exec.LookPath("just")
	if err != nil {
		t.Skip("just not installed")
	}
	res := convertFixture(t, "eq")
	if len(res.Problems) > 0 {
		t.Fatalf("problems: %v", res.Problems)
	}
	dir, set := loadConverted(t, "eq", res.TOML)

	cases := [][]string{
		{"greet"},
		{"greet", "bob"},
		{"greet", "bob", "x", "cache:clear"},
		{"pair", "one"},
		{"pair", "one", "two"},
	}
	for _, args := range cases {
		cmd := exec.Command(justBin, append([]string{"--justfile", filepath.Join("testdata", "eq.just")}, args...)...)
		want, err := cmd.Output()
		if err != nil {
			t.Fatalf("just %v: %v", args, err)
		}

		target, err := set.Resolve(append([]string{"eq"}, args...))
		if err != nil {
			t.Fatal(err)
		}
		plan, err := set.Plan(target, dir)
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		r := &run.Runner{ProjectDir: dir, ProjectName: "p", Runtime: runtime.NewMockRuntime(),
			Stdin: strings.NewReader(""), Stdout: &out, Stderr: &bytes.Buffer{}}
		if err := r.Run(context.Background(), plan); err != nil {
			t.Fatalf("zdev run eq %v: %v", args, err)
		}
		// multi-job runs add a header per job; compare the jobs' own output
		var got []string
		for _, line := range strings.Split(out.String(), "\n") {
			if line != "" && !strings.HasPrefix(line, "▶ ") {
				got = append(got, line)
			}
		}
		if g, w := strings.Join(got, "\n"), strings.TrimSpace(string(want)); g != w {
			t.Errorf("%v\njust:\n%s\nzdev:\n%s", args, w, g)
		}
	}
}
