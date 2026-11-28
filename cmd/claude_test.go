package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
