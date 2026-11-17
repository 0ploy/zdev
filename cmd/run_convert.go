package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/0ploy/zdev/internal/justconvert"
	"github.com/0ploy/zdev/internal/run"
	"github.com/0ploy/zdev/internal/tools"
)

// justDeprecationWarning is printed on every run of a .just file.
func justDeprecationWarning(name string) string {
	return fmt.Sprintf("warning: %s.just uses just, which is deprecated and will be removed in a future release. Convert it: zdev run --convert-just %s", name, name)
}

// justShadowWarning is printed when a built-in shadows a .just file, which
// then never runs.
func justShadowWarning(name string) string {
	return fmt.Sprintf("warning: %s.just is shadowed by the built-in `zdev %s` and never runs. Convert it (zdev run --convert-just %s) and run it as `zdev run %s`", name, name, name, name)
}

// warnShadowedJustfile prints the shadow warning if a built-in command name
// also has a .just file in the current project.
func warnShadowedJustfile(name string) {
	if findJustfile(name) != nil {
		fmt.Fprintln(os.Stderr, justShadowWarning(name))
	}
}

// justDumper produces just's JSON dump of a justfile; swapped in tests.
type justDumper func(ctx context.Context, path string) ([]byte, error)

func defaultJustDumper(ctx context.Context) (justDumper, error) {
	toolMgr, err := tools.NewManager()
	if err != nil {
		return nil, fmt.Errorf("failed to initialize tool manager: %w", err)
	}
	justPath, err := toolMgr.EnsureTool(ctx, tools.JustTool())
	if err != nil {
		return nil, fmt.Errorf("the converter needs just to read the files: %w", err)
	}
	just := tools.NewJust(justPath)
	return just.Dump, nil
}

// errNotAllConverted makes the command exit non-zero after reporting.
var errNotAllConverted = errors.New("not every justfile was converted, see above")

// convertJustfiles converts .zdev/commands/<name>.just files to
// .zdev/run/<name>.toml. Without names it converts every .just file.
// Converted files are renamed to .just.converted, never deleted; a file
// that can't be converted safely stays untouched.
func convertJustfiles(ctx context.Context, w io.Writer, dir string, names []string, dump justDumper) error {
	commandsDir := filepath.Join(dir, ".zdev", "commands")
	if len(names) == 0 {
		entries, err := os.ReadDir(commandsDir)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".just") {
				names = append(names, strings.TrimSuffix(e.Name(), ".just"))
			}
		}
		if len(names) == 0 {
			fmt.Fprintln(w, "No .just files in .zdev/commands/, nothing to convert.")
			return nil
		}
	}

	runDir := run.Dir(dir)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return err
	}

	failed, echoed := 0, false
	for _, name := range names {
		name = strings.TrimSuffix(name, ".just")
		src := filepath.Join(commandsDir, name+".just")
		dst := filepath.Join(runDir, name+".toml")
		rel := ".zdev/run/" + name + ".toml"

		fail := func(reasons ...string) {
			failed++
			fmt.Fprintf(w, "✗ %s.just not converted:\n", name)
			for _, r := range reasons {
				fmt.Fprintf(w, "    %s\n", r)
			}
		}

		source, err := os.ReadFile(src)
		if err != nil {
			fail(err.Error())
			continue
		}
		if _, err := os.Stat(dst); err == nil {
			fail(rel + " already exists; remove it or convert by hand")
			continue
		}
		dumpCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		data, err := dump(dumpCtx, src)
		cancel()
		if err != nil {
			fail("just can't read it: " + err.Error())
			continue
		}
		res, err := justconvert.Convert(name, data, source)
		if err != nil {
			fail(err.Error())
			continue
		}
		if len(res.Problems) > 0 {
			fail(res.Problems...)
			continue
		}

		if err := os.WriteFile(dst, []byte(res.TOML), 0o644); err != nil {
			fail(err.Error())
			continue
		}
		// The converter must never write a job file zdev can't load.
		set, err := run.Load(dir, run.Options{})
		if err == nil {
			err = set.Broken[name]
		}
		if err != nil {
			_ = os.Remove(dst)
			fail("converted file doesn't load (a converter bug, please report it): " + err.Error())
			continue
		}
		if err := os.Rename(src, src+".converted"); err != nil {
			_ = os.Remove(dst)
			fail(err.Error())
			continue
		}

		fmt.Fprintf(w, "✓ %s.just -> %s (original kept as %s.just.converted)\n", name, rel, name)
		for _, n := range res.Notes {
			fmt.Fprintf(w, "    review: %s\n", n)
		}
		echoed = echoed || res.Echoed
	}

	if echoed {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Note: just printed each command line before running it (lines without @); jobs don't. Add `set -x` to a job to get that back.")
	}
	if failed > 0 {
		return errNotAllConverted
	}
	return nil
}
