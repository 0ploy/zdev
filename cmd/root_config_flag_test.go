package cmd

import "testing"

func TestGlobalConfigFlag(t *testing.T) {
	tests := []struct {
		name   string
		args   []string
		want   string
		wantOK bool
	}{
		{"before command", []string{"--config", "/p", "start"}, "/p", true},
		{"equals form", []string{"--config=/p", "status"}, "/p", true},
		{"after other flag", []string{"-v", "--config", "/p", "start"}, "/p", true},
		{"inside exec command", []string{"exec", "app", "vitest", "--config", "vitest.config.ts"}, "", false},
		{"inside job args", []string{"run", "test", "--config=x"}, "", false},
		{"after separator", []string{"--", "--config", "/p"}, "", false},
		{"missing value", []string{"--config"}, "", false},
		{"none", []string{"start"}, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := globalConfigFlag(tt.args)
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("globalConfigFlag(%q) = (%q, %v), want (%q, %v)", tt.args, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}
