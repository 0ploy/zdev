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

func TestSplitClaudeArgs(t *testing.T) {
	isService := func(s string) bool { return s == "app" || s == "node" }
	cases := []struct {
		args    []string
		service string
		rest    string
	}{
		{nil, "", ""},
		{[]string{"--continue"}, "", "--continue"},
		{[]string{"node", "--continue"}, "node", "--continue"},
		{[]string{"app", "--", "-p", "x"}, "app", "-p x"},
		{[]string{"fix the login"}, "", "fix the login"},
		{[]string{"--", "app"}, "", "app"},
	}
	for _, c := range cases {
		s, rest := splitClaudeArgs(c.args, isService)
		if s != c.service || strings.Join(rest, " ") != c.rest {
			t.Errorf("splitClaudeArgs(%q) = %q, %q; want %q, %q", c.args, s, rest, c.service, c.rest)
		}
	}
}

func TestClaudeSessionArgs(t *testing.T) {
	if got := claudeSessionArgs([]string{"-p", "hi"}, "id"); strings.Join(got, " ") != "--session-id id -p hi" {
		t.Errorf("got %v", got)
	}
	for _, a := range [][]string{{"--continue"}, {"-c"}, {"--resume", "x"}, {"-r"}, {"--session-id", "y"}, {"--resume=x"}} {
		if got := claudeSessionArgs(a, "id"); strings.Join(got, " ") != strings.Join(a, " ") {
			t.Errorf("%v: got %v, want unchanged", a, got)
		}
	}
}

// A replaced sandbox resumes the same conversation with the session's model.
func TestResumeArgs(t *testing.T) {
	cases := []struct {
		rest []string
		want string // the arguments before the message
	}{
		{nil, "--resume sid"},
		{[]string{"fix the bug"}, "--resume sid"},
		{[]string{"--model", "opus", "fix it"}, "--model opus --resume sid"},
		{[]string{"--resume", "abc"}, "--resume abc"},
		{[]string{"--session-id=xyz"}, "--resume xyz"},
	}
	for _, c := range cases {
		got := resumeArgs(c.rest, "sid", t.TempDir(), "app")
		if strings.Join(got[:len(got)-1], " ") != c.want || !strings.Contains(got[len(got)-1], "service app changed") {
			t.Errorf("resumeArgs(%v) = %q, want %q + message", c.rest, got, c.want)
		}
	}
	// --continue names no session: the newest transcript, else --continue.
	if got := resumeArgs([]string{"--continue"}, "sid", "/no/such/project", "app"); got[0] != "--continue" {
		t.Errorf("--continue without transcripts: %q", got)
	}
}

func TestPiInteractive(t *testing.T) {
	for args, want := range map[string]bool{"": true, "fix it": true, "-p hi": false, "--print": false, "--mode json": false, "--mode=rpc": false, "--mode text": true} {
		if got := piAgent.interactive(strings.Fields(args)); got != want {
			t.Errorf("interactive(%q) = %v", args, got)
		}
	}
}

func TestPiResume(t *testing.T) {
	cases := []struct {
		rest     []string
		reported string
		want     string
	}{
		{nil, "pi-1", "--session-id pi-1"},
		{[]string{"--model", "opus", "fix it"}, "pi-1", "--model opus --session-id pi-1"},
		{[]string{"--session-id=other", "--thinking=high"}, "", "--thinking=high --session-id other"},
		{[]string{"--session", "/x/s.jsonl"}, "", "--session /x/s.jsonl"},
		{[]string{"--session", "/x/s.jsonl"}, "pi-2", "--session-id pi-2"},
		{[]string{"--continue"}, "", "--continue"},
	}
	for _, c := range cases {
		got := piAgent.resume(c.rest, "zdev-id", c.reported, "/no/such/project", "app")
		if strings.Join(got[:len(got)-1], " ") != c.want || !strings.Contains(got[len(got)-1], "service app changed") {
			t.Errorf("resume(%q, %q) = %q, want %q + message", c.rest, c.reported, got, c.want)
		}
	}
	if got := piAgent.argv([]string{"-p", "hi"}, "zdev-id"); strings.Contains(strings.Join(got, " "), "zdev-id") {
		t.Errorf("argv passes zdev's session id to pi: %q", got)
	}
}

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

func TestLastPiBuildFailed(t *testing.T) {
	dir := t.TempDir()
	if got := lastPiBuildFailed(dir); got != "" {
		t.Errorf("no log: %q", got)
	}
	_ = os.MkdirAll(filepath.Join(dir, "pi"), 0o700)
	_ = os.WriteFile(piBuildLog(dir), []byte("pi 1.7.0\nnpm ...\n\n"+piBuildFailed+"building pi 1.7.0: exit status 1\n"), 0o600)
	if got := lastPiBuildFailed(dir); got != "building pi 1.7.0: exit status 1" {
		t.Errorf("failed build: %q", got)
	}
	_ = os.WriteFile(piBuildLog(dir), []byte("pi 1.7.0\nok\n"), 0o600)
	if got := lastPiBuildFailed(dir); got != "" {
		t.Errorf("good build: %q", got)
	}
}
