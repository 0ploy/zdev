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
	if o := g.AgentProject("other"); o.ConnectorsEnabled() || o.Service != "" || o.Limits.CPUs != "4" {
		t.Errorf("defaults = %+v", o)
	}
}

func TestValidateAgentConfig(t *testing.T) {
	ok := []AgentConfig{
		{},
		{Claude: AgentClaudeConfig{Channel: "latest"}},
		{Claude: AgentClaudeConfig{Channel: "2.1.325"}},
		{Projects: map[string]AgentProjectConfig{"x": {DockerSocket: "accept", HostAccess: []int{9003}}}},
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
	}
	for _, a := range bad {
		if err := validateAgentConfig(&a); err == nil {
			t.Errorf("%+v accepted", a)
		}
	}
}
