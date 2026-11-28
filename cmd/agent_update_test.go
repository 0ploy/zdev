package cmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0ploy/zdev/internal/agent"
)

// fakeDocker is a docker stand-in that logs its arguments and answers
// `volume ls`/`volume inspect` from have (volume names); everything else
// succeeds, except `run` of a fill check (test -f), which fails unless the
// volume is in have.
func fakeDocker(t *testing.T, have ...string) (agent.Docker, string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	script := `#!/bin/sh
echo "$*" >> "` + log + `"
have="` + strings.Join(have, " ") + `"
case "$1 $2" in
  "volume ls") for v in $have; do echo "$v"; done; exit 0 ;;
  "volume inspect") for v in $have; do [ "$v" = "${5}" ] && exit 0; done; exit 1 ;;
  "wait "*) exit 1 ;;
esac
case "$*" in
  *"-f /v/.ok"*) for v in $have; do case "$*" in *"src=$v,"*) exit 0 ;; esac; done; exit 1 ;;
esac
exit 0
`
	bin := filepath.Join(dir, "docker")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return agent.Docker{Binary: bin}, log
}

// writeClaudeRelease caches a resolved release, so no network is asked.
func writeClaudeRelease(t *testing.T, agentDir, version string) {
	t.Helper()
	rel := agent.ClaudeRelease{Channel: "stable", Version: version, ResolvedAt: time.Now(),
		Platforms: map[string]agent.ClaudeBuild{"linux-arm64-musl": {SHA256: strings.Repeat("a", 64)}, "linux-arm64": {SHA256: strings.Repeat("b", 64)}}}
	b, _ := json.Marshal(rel)
	_ = os.MkdirAll(filepath.Join(agentDir, "claude"), 0o700)
	if err := os.WriteFile(filepath.Join(agentDir, "claude", "release.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestChooseClaudeNeverWaits(t *testing.T) {
	ctx := context.Background()
	agentDir := t.TempDir()
	writeClaudeRelease(t, agentDir, "2.1.400")

	// Nothing here: the download starts detached, the session is refused.
	d, log := fakeDocker(t)
	_, _, err := chooseClaude(ctx, d, agentDir, "stable", "musl", "arm64", true)
	if err == nil || !strings.Contains(err.Error(), "downloading in the background") || !strings.Contains(err.Error(), "zdev agent setup claude") {
		t.Fatalf("no build: err = %v", err)
	}
	if b, _ := os.ReadFile(log); !strings.Contains(string(b), "run -d --name zdev-fill-zdev-claude-2.1.400-musl-arm64") {
		t.Errorf("no detached download started:\n%s", b)
	}

	// An older build here: the session runs it, the new one downloads detached.
	old := agent.ClaudeVolumeName("2.1.300", "musl", "arm64")
	d, log = fakeDocker(t, old)
	v, vol, err := chooseClaude(ctx, d, agentDir, "stable", "musl", "arm64", true)
	if err != nil || v != "2.1.300" || vol != old {
		t.Fatalf("older build: %s %s %v", v, vol, err)
	}
	if b, _ := os.ReadFile(log); !strings.Contains(string(b), "run -d --name zdev-fill-zdev-claude-2.1.400-musl-arm64") {
		t.Errorf("new release not downloading:\n%s", b)
	}
}
