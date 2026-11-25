package cmd

// The lifecycle side of agent sandboxes (PLAN_2 section 10): a start, update
// or project restart the relay runs for a sandbox is checked here, in the
// process that applies the config; the developer's own runs record what they
// applied, which later requests are checked against.

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/0ploy/zdev/internal/agent"
	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/project"
)

func zdevDomain() string {
	if gcfg, err := config.LoadGlobalConfig(); err == nil && gcfg != nil {
		return gcfg.Domain
	}
	return ""
}

// agentGate runs before a command applies the project's config. For a
// request from a sandbox it refuses (exit 126) what reaches beyond the
// baseline; for the developer in a project with agent mode it lists those
// changes, since running the command approves them.
func agentGate(proj *project.Project) {
	if _, ok := project.AgentRequest(); ok {
		v := proj.CheckAgentPolicy(zdevDomain())
		if len(v) == 0 {
			return
		}
		fmt.Fprintln(os.Stderr, "zdev: refused (policy): this config reaches outside the project, beyond what the developer applied:")
		for _, x := range v {
			fmt.Fprintln(os.Stderr, "  - "+x.String())
		}
		fmt.Fprintln(os.Stderr, "ask the developer to review .zdev/ and run `zdev update` on their machine")
		os.Exit(126)
	}
	if project.AgentSyncService(proj.Config.Name) == "" || !proj.HasAgentBaseline() {
		return
	}
	if v := proj.CheckAgentPolicy(zdevDomain()); len(v) > 0 {
		fmt.Println("Applying config an agent sandbox could not apply itself:")
		for _, x := range v {
			fmt.Println("  - " + x.String())
		}
	}
}

// recordAgentBaseline records what the developer's command applied, in a
// project that uses agent mode.
func recordAgentBaseline(ctx context.Context, proj *project.Project) {
	if project.AgentSyncService(proj.Config.Name) == "" {
		return
	}
	if err := proj.RecordAgentBaseline(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: recording the applied config for agent sandboxes: %v\n", err)
	}
}

// exitOnRefusal ends the process like the relay does for a refusal: the
// message on stderr, exit 126.
func exitOnRefusal(err error) {
	var r *agent.Refusal
	if errors.As(err, &r) {
		fmt.Fprintln(os.Stderr, r.Error())
		os.Exit(126)
	}
}
