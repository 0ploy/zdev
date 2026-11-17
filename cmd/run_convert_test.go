package cmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0ploy/zdev/internal/project"
)

// fixtureDumper serves the converter's testdata dumps instead of running just.
func fixtureDumper(t *testing.T) justDumper {
	return func(_ context.Context, path string) ([]byte, error) {
		name := strings.TrimSuffix(filepath.Base(path), ".just")
		return os.ReadFile(filepath.Join("..", "internal", "justconvert", "testdata", name+".json"))
	}
}

func setupJustProject(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	commands := filepath.Join(dir, ".zdev", "commands")
	if err := os.MkdirAll(commands, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		src, err := os.ReadFile(filepath.Join("..", "internal", "justconvert", "testdata", n+".just"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(commands, n+".just"), src, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestConvertJustfiles(t *testing.T) {
	dir := setupJustProject(t, "console", "deploy", "refuse")
	var out bytes.Buffer
	err := convertJustfiles(context.Background(), &out, dir, nil, fixtureDumper(t))
	if !errors.Is(err, errNotAllConverted) {
		t.Fatalf("refuse.just fails, so the command must fail: %v", err)
	}
	commands := filepath.Join(dir, ".zdev", "commands")
	for _, n := range []string{"console", "deploy"} {
		if !exists(filepath.Join(dir, ".zdev", "run", n+".toml")) || !exists(filepath.Join(commands, n+".just.converted")) || exists(filepath.Join(commands, n+".just")) {
			t.Errorf("%s: want .toml written and .just renamed to .just.converted", n)
		}
	}
	if !exists(filepath.Join(commands, "refuse.just")) || exists(filepath.Join(dir, ".zdev", "run", "refuse.toml")) {
		t.Errorf("a refused file stays untouched and gets no .toml")
	}
	requireContainsCmd(t, out.String(),
		"✓ console.just -> .zdev/run/console.toml",
		"✓ deploy.just -> .zdev/run/deploy.toml",
		"review: deploy all: line 6 `cd`",
		"✗ refuse.just not converted:",
		"uses set export",
		"just printed each command line",
	)
}

func TestConvertJustfiles_NamedAndExisting(t *testing.T) {
	dir := setupJustProject(t, "console", "deploy")
	runDir := filepath.Join(dir, ".zdev", "run")
	_ = os.MkdirAll(runDir, 0o755)
	_ = os.WriteFile(filepath.Join(runDir, "console.toml"), []byte(`run = "mine"`), 0o644)

	var out bytes.Buffer
	err := convertJustfiles(context.Background(), &out, dir, []string{"console.just"}, fixtureDumper(t))
	if !errors.Is(err, errNotAllConverted) {
		t.Fatalf("got %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(runDir, "console.toml")); string(b) != `run = "mine"` {
		t.Errorf("an existing job file must never be overwritten")
	}
	if !exists(filepath.Join(dir, ".zdev", "commands", "deploy.just")) {
		t.Errorf("only the named file is converted")
	}
	requireContainsCmd(t, out.String(), "console.toml already exists")
}

func TestConvertJustfiles_Nothing(t *testing.T) {
	var out bytes.Buffer
	if err := convertJustfiles(context.Background(), &out, t.TempDir(), nil, fixtureDumper(t)); err != nil {
		t.Fatal(err)
	}
	requireContainsCmd(t, out.String(), "nothing to convert")
}

func TestJustWarnings(t *testing.T) {
	requireContainsCmd(t, justDeprecationWarning("db"), "db.just uses just, which is deprecated", "zdev run --convert-just db")
	requireContainsCmd(t, justShadowWarning("logs"), "logs.just is shadowed by the built-in `zdev logs`", "`zdev run logs`")
}

func requireContainsCmd(t *testing.T, haystack string, needles ...string) {
	t.Helper()
	for _, n := range needles {
		if !strings.Contains(haystack, n) {
			t.Errorf("missing %q in:\n%s", n, haystack)
		}
	}
}

func TestShadowedJustfiles(t *testing.T) {
	dir := t.TempDir()
	commands := filepath.Join(dir, ".zdev", "commands")
	_ = os.MkdirAll(commands, 0o755)
	for _, n := range []string{"db.just", "logs.just", "hello.just", "db.just.converted"} {
		_ = os.WriteFile(filepath.Join(commands, n), []byte("x:\n    true\n"), 0o644)
	}
	proj := &project.Project{Dir: dir}
	got := shadowedJustfiles(proj)
	if strings.Join(got, ",") != "db,logs" {
		t.Fatalf("shadowed = %v, want db,logs (hello isn't a built-in, .converted isn't a justfile)", got)
	}

	var out bytes.Buffer
	printShadowedJustfiles(&out, got, true)
	requireContainsCmd(t, out.String(),
		"Shadowed commands:",
		"db.just         never runs: `zdev db` is a built-in",
		"zdev run --convert-just",
	)

	out.Reset()
	printShadowedJustfiles(&out, nil, true)
	if out.Len() != 0 {
		t.Errorf("no section without shadowed files: %q", out.String())
	}
}
