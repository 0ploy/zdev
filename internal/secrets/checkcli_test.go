package secrets

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeOp puts an op on a PATH of its own that exits with code for
// `op environment secret list --help`.
func fakeOp(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "op"), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func TestCheckCLI(t *testing.T) {
	t.Run("current", func(t *testing.T) {
		fakeOp(t, `[ "$1 $2 $3" = "environment secret list" ] && exit 0; exit 1`)
		if err := CheckCLI(context.Background()); err != nil {
			t.Errorf("op with secret list reported: %v", err)
		}
	})
	t.Run("too old", func(t *testing.T) {
		fakeOp(t, `echo '[ERROR] unknown command "secret" for "op environment"' >&2; exit 1`)
		err := CheckCLI(context.Background())
		if err == nil || !strings.Contains(err.Error(), "too old") || !strings.Contains(err.Error(), "brew install 1password-cli") {
			t.Errorf("op without secret list: %v", err)
		}
	})
	t.Run("missing", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		err := CheckCLI(context.Background())
		if err == nil || !strings.Contains(err.Error(), "not found") {
			t.Errorf("missing op: %v", err)
		}
	})
}
