package config

import "testing"

func TestAgentProjectMerge(t *testing.T) {
	yes := true
	g := &GlobalConfig{Agent: AgentConfig{
		Defaults: AgentProjectConfig{HostAccess: []int{1}, Limits: AgentLimits{Memory: "2g"}},
		Projects: map[string]AgentProjectConfig{"shop": {Service: "web", Connectors: &yes, Limits: AgentLimits{CPUs: "2"}}},
	}}
	p := g.AgentProject("shop")
	if p.Service != "web" || !p.ConnectorsEnabled() || p.Limits.CPUs != "2" || p.Limits.Memory != "2g" || p.Limits.Pids != 4096 || len(p.HostAccess) != 1 {
		t.Errorf("merged = %+v", p)
	}
	if o := g.AgentProject("other"); o.ConnectorsEnabled() || o.Service != "" || o.Limits.CPUs != "4" || o.Egress != "log" {
		t.Errorf("defaults = %+v", o)
	}
}

func TestValidateAgentConfig(t *testing.T) {
	ok := []AgentConfig{
		{},
		{Claude: AgentClaudeConfig{Channel: "latest"}},
		{Claude: AgentClaudeConfig{Channel: "2.1.325"}},
		{Projects: map[string]AgentProjectConfig{"x": {DockerSocket: "accept", HostAccess: []int{9003}}}},
		{Projects: map[string]AgentProjectConfig{"x": {Egress: "enforce", AllowHosts: []string{"*.github.com", "registry.npmjs.org"}}}},
		{Credentials: map[string]AgentCredential{"github": {Hosts: []string{"github.com", "*.github.com"}, Env: []string{"GH_TOKEN"}, Command: "gh auth token"}},
			Projects: map[string]AgentProjectConfig{"x": {Credentials: []string{"github"}}}},
		{Credentials: map[string]AgentCredential{"npm": {Hosts: []string{"registry.npmjs.org"}, Secret: "op-env://abc/NPM_TOKEN"}}},
		{Credentials: map[string]AgentCredential{"github": {Hosts: []string{"github.com"}, Secret: "op://vault/item/credential"}}},
		{Credentials: map[string]AgentCredential{"github": {Hosts: []string{"github.com"}, File: "~/.zdev/agent/credentials/github"}}},
	}
	for _, a := range ok {
		if err := validateAgentConfig(&a); err != nil {
			t.Errorf("%+v: %v", a, err)
		}
	}
	bad := []AgentConfig{
		{Claude: AgentClaudeConfig{Channel: "nightly"}},
		{Defaults: AgentProjectConfig{DockerSocket: "yes"}},
		{Projects: map[string]AgentProjectConfig{"x": {HostAccess: []int{70000}}}},
		{Defaults: AgentProjectConfig{Egress: "block"}},
		{Projects: map[string]AgentProjectConfig{"x": {AllowHosts: []string{"https://github.com"}}}},
		{Projects: map[string]AgentProjectConfig{"x": {AllowHosts: []string{"git*.com"}}}},
		{Projects: map[string]AgentProjectConfig{"x": {Credentials: []string{"undefined"}}}},
		{Credentials: map[string]AgentCredential{"github": {Command: "gh auth token"}}},                                            // no hosts
		{Credentials: map[string]AgentCredential{"github": {Hosts: []string{"github.com"}}}},                                       // no source
		{Credentials: map[string]AgentCredential{"github": {Hosts: []string{"github.com"}, Command: "x", Secret: "op-env://a/B"}}}, // both
		{Credentials: map[string]AgentCredential{"github": {Hosts: []string{"github.com"}, Secret: "op://vault/item"}}},
		{Credentials: map[string]AgentCredential{"github": {Hosts: []string{"github.com"}, Secret: "op://v/i/f", File: "/x"}}},
		{Credentials: map[string]AgentCredential{"github": {Hosts: []string{"github.com"}, File: "relative/token"}}},
		{Credentials: map[string]AgentCredential{"github": {Hosts: []string{"github.com"}, Command: "x", Env: []string{"GH-TOKEN"}}}},
	}
	for _, a := range bad {
		if err := validateAgentConfig(&a); err == nil {
			t.Errorf("%+v accepted", a)
		}
	}
}
