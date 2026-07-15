package resolver

import "testing"

func TestValidateDomain(t *testing.T) {
	valid := []string{"0ploy.dev", "example.com", "my-app.local", "a.b.c.dev"}
	for _, d := range valid {
		if err := validateDomain(d); err != nil {
			t.Errorf("validateDomain(%q) = %v, want nil", d, err)
		}
	}

	// Anything that could break a file path or a sudo shell command must
	// be rejected - these feed into both.
	invalid := []string{
		"",
		"nodot",
		"has space.dev",
		"semi;rm -rf.dev",
		"../../etc/passwd",
		"dev/../etc",
		"quote\".dev",
		"$(whoami).dev",
		"-leadinghyphen.dev",
		"trailinghyphen-.dev",
	}
	for _, d := range invalid {
		if err := validateDomain(d); err == nil {
			t.Errorf("validateDomain(%q) = nil, want error", d)
		}
	}
}
