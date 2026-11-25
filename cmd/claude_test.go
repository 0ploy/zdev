package cmd

import (
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
