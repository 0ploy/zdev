package config

import "testing"

// validProject returns a minimal ProjectConfig that passes ValidateProjectConfig,
// so individual tests can mutate one field and assert on the result.
func validProject() *ProjectConfig {
	return &ProjectConfig{
		Name: "demo",
		Services: map[string]ServiceConfig{
			"app": {Image: "nginx"},
		},
	}
}

func TestValidateProjectConfig_Domain(t *testing.T) {
	cases := []struct {
		name    string
		domain  string
		wantErr bool
	}{
		{"empty falls back to global", "", false},
		{"plain hostname", "demo.0ploy.dev", false},
		{"backtick Host injection", "evil.local`) || Host(`victim.0ploy.dev", true},
		{"space", "a b.0ploy.dev", true},
		{"quote breaks docs href", `a".0ploy.dev`, true},
		{"single label", "localhost", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validProject()
			cfg.Domain = tc.domain
			err := ValidateProjectConfig(cfg)
			if (err != nil) != tc.wantErr {
				t.Fatalf("domain %q: got err=%v, wantErr=%v", tc.domain, err, tc.wantErr)
			}
		})
	}
}

func TestValidateProjectConfig_RoutingDomainInjection(t *testing.T) {
	cfg := validProject()
	cfg.Services["app"] = ServiceConfig{
		Image: "nginx",
		Routing: &RoutingConfig{
			Protocol: "http",
			Domain:   "x`) || Host(`mail.shared.0ploy.dev",
		},
	}
	if err := ValidateProjectConfig(cfg); err == nil {
		t.Fatal("expected routing.domain with backtick injection to be rejected")
	}
}

func TestValidateGlobalConfig_Domain(t *testing.T) {
	cases := []struct {
		name    string
		domain  string
		wantErr bool
	}{
		{"valid", "0ploy.dev", false},
		{"empty is required", "", true},
		{"backtick", "a`.dev", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &GlobalConfig{Domain: tc.domain}
			err := validateGlobalConfig(cfg)
			if (err != nil) != tc.wantErr {
				t.Fatalf("domain %q: got err=%v, wantErr=%v", tc.domain, err, tc.wantErr)
			}
		})
	}
}
