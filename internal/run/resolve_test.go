package run

import (
	"reflect"
	"strings"
	"testing"
)

func resolveSet(t *testing.T) *Set {
	t.Helper()
	s := load(t, Options{}, map[string]string{
		"test.toml": "aliases = [\"t\"]\nrun = \"phpunit\"",
		"deploy.toml": `
[jobs.all]
aliases = ["a"]
run = "release"
[jobs.prod]
run = "prod"
[jobs.helper]
hidden = true
run = "help"
`,
		"secret.toml": "hidden = true\nrun = \"x\"",
		"bad.toml":    `nope =`,
	})
	return s
}

func TestResolve(t *testing.T) {
	s := resolveSet(t)
	cases := []struct {
		tokens []string
		job    string // "" = file listing (Job nil)
		args   []string
	}{
		{[]string{"test"}, "test", nil},
		{[]string{"test", "--filter", "cache:clear", "a b"}, "test", []string{"--filter", "cache:clear", "a b"}},
		{[]string{"t", "x"}, "test", []string{"x"}},
		{[]string{"deploy"}, "", nil},
		{[]string{"deploy", "all", "--force"}, "deploy all", []string{"--force"}},
		{[]string{"deploy", "a"}, "deploy all", nil},
		{[]string{"deploy", "prod"}, "deploy prod", nil},
		{[]string{"deploy", "helper"}, "deploy helper", nil}, // hidden is still runnable
		{[]string{"secret"}, "secret", nil},
	}
	for _, tc := range cases {
		target, err := s.Resolve(tc.tokens)
		if err != nil {
			t.Errorf("%v: %v", tc.tokens, err)
			continue
		}
		if tc.job == "" {
			if target.Job != nil || target.File == nil || target.File.Name != "deploy" {
				t.Errorf("%v: want file listing, got %+v", tc.tokens, target)
			}
			continue
		}
		if target.Job == nil || target.Job.ID() != tc.job {
			t.Errorf("%v: got job %v, want %q", tc.tokens, target.Job, tc.job)
			continue
		}
		if len(target.Args) != 0 || len(tc.args) != 0 {
			if !reflect.DeepEqual(target.Args, tc.args) {
				t.Errorf("%v: args %q, want %q", tc.tokens, target.Args, tc.args)
			}
		}
	}
}

func TestResolve_Errors(t *testing.T) {
	s := resolveSet(t)
	cases := []struct {
		tokens []string
		want   []string
		reject []string
	}{
		{nil, []string{"no command"}, nil},
		{[]string{"nope"}, []string{`unknown command "nope"`, "available: deploy, test"}, []string{"secret"}},
		// A multi-job file never treats an unknown token as an argument.
		{[]string{"deploy", "staging"}, []string{`deploy has no job "staging"`, "available: all, prod"}, []string{"helper"}},
		{[]string{"bad"}, []string{"bad.toml"}, nil},
	}
	for _, tc := range cases {
		_, err := s.Resolve(tc.tokens)
		if err == nil {
			t.Errorf("%v: expected error", tc.tokens)
			continue
		}
		for _, w := range tc.want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("%v: %q missing %q", tc.tokens, err, w)
			}
		}
		for _, r := range tc.reject {
			if strings.Contains(err.Error(), r) {
				t.Errorf("%v: %q should not list %q", tc.tokens, err, r)
			}
		}
	}
}
