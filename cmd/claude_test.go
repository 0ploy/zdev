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
