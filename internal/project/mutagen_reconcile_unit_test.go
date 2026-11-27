package project

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0ploy/zdev/internal/mutagen"
)

// fakeMutagen is a mutagen binary that succeeds and logs every call's
// subcommand and --sync-mode, one per line.
func fakeMutagen(t *testing.T) (*mutagen.Mutagen, func() []string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	script := `#!/bin/sh
mode=""
prev=""
for a in "$@"; do [ "$prev" = "--sync-mode" ] && mode=" $a"; prev="$a"; done
echo "$1 $2$mode" >> "` + log + `"
`
	bin := filepath.Join(dir, "mutagen")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return mutagen.New(bin), func() []string {
		b, _ := os.ReadFile(log)
		var calls []string
		for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			if l != "sync list" { // EnsureDaemon's liveness probe
				calls = append(calls, l)
			}
		}
		return calls
	}
}

func TestCreateSyncSessionReconcilesTwoWaySafe(t *testing.T) {
	for _, mode := range []string{"", mutagen.DefaultSyncMode} {
		m, calls := fakeMutagen(t)
		if err := createSyncSession(context.Background(), m, mutagen.SessionConfig{Name: "s", Alpha: "/a", Beta: "docker://c/app", SyncMode: mode}); err != nil {
			t.Fatal(err)
		}
		want := []string{
			"sync create two-way-resolved", // the host wins what differs
			"sync flush",
			"sync terminate",
			"sync create two-way-safe", // then the configured mode, from identical trees
		}
		if got := calls(); strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("mode %q: calls\n got  %v\n want %v", mode, got, want)
		}
	}
}

func TestCreateSyncSessionKeepsOtherModes(t *testing.T) {
	for _, mode := range []string{"two-way-resolved", "one-way-safe", "one-way-replica"} {
		m, calls := fakeMutagen(t)
		if err := createSyncSession(context.Background(), m, mutagen.SessionConfig{Name: "s", Alpha: "/a", Beta: "docker://c/app", SyncMode: mode}); err != nil {
			t.Fatal(err)
		}
		if got := calls(); len(got) != 1 || got[0] != "sync create "+mode {
			t.Errorf("mode %s: calls %v, want one create", mode, got)
		}
	}
}

func TestConflictDetail(t *testing.T) {
	d := conflictDetail([]string{"a.php", "b.php"})
	for _, want := range []string{"2 conflict(s)", "a.php, b.php", "zdev mutagen reset"} {
		if !strings.Contains(d, want) {
			t.Errorf("detail %q lacks %q", d, want)
		}
	}
	if d := conflictDetail([]string{"1", "2", "3", "4", "5", "6", "7"}); !strings.Contains(d, "and 2 more") {
		t.Errorf("long list not shortened: %q", d)
	}
}
