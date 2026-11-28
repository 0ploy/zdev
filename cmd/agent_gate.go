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
	refuseEscapingBinds(proj)
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

// escapingBindsError explains binds that leave the project through a
// symlink (Project.EscapingBinds), nil when there are none.
func escapingBindsError(proj *project.Project) error {
	v := proj.EscapingBinds()
	if len(v) == 0 {
		return nil
	}
	msg := "in a project you use an agent sandbox with, a bind mount written inside the project can't lead outside it through a symlink:"
	for _, x := range v {
		msg += "\n  - " + x.String()
	}
	msg += "\na container may have placed that link: remove it, or if it is yours, bind its target path itself in .zdev/config.yaml"
	return errors.New(msg)
}

// refuseEscapingBinds stops a command that would start containers with
// such a bind, for the developer too (their start would mount the link's
// target and record it as approved): exit 126 for a sandbox's request, 1
// otherwise.
func refuseEscapingBinds(proj *project.Project) {
	err := escapingBindsError(proj)
	if err == nil {
		return
	}
	if _, ok := project.AgentRequest(); ok {
		fmt.Fprintln(os.Stderr, "zdev: refused (symlink): "+err.Error())
		os.Exit(126)
	}
	fmt.Fprintln(os.Stderr, "Error: "+err.Error())
	os.Exit(1)
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
