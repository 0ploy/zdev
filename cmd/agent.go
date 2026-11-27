package cmd

// zdev agent: everything about the sandboxed coding agents. `zdev agent run
// <agent>` starts a session; `zdev claude` is its shortcut and a pure
// pass-through to Claude, so zdev's own commands never share Claude's
// argument space (Claude has subcommands of its own: update, auth, mcp...).

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// knownAgents are the agents zdev agent run can start.
var knownAgents = []string{"claude"}

var agentCmd = &cobra.Command{
	Use:   "agent",
	Short: "Manage agent sandboxes: setup, credentials, update, run",
	Long: `Coding agents in sandboxes of this project (see docs/agents.md).

  zdev agent run claude [service] [args]   a session (shortcut: zdev claude)
  zdev agent setup                         check the login, give the project a GitHub or GitLab token
  zdev agent credentials                   the tokens sandboxes get, swapped in by the egress proxy
  zdev agent update                        download the newest agent release now

Agents: ` + strings.Join(knownAgents, ", ") + `.`,
}

var agentRunCmd = &cobra.Command{
	Use:   "run <agent> [service] [agent arguments...]",
	Short: "Run a coding agent in a sandbox of this project",
	Long: `Run a coding agent in a sandbox of this project. Everything after the agent
goes to it (a service name first selects the image); zdev claude is the
shortcut for zdev agent run claude.

Agents: ` + strings.Join(knownAgents, ", ") + `.`,
	DisableFlagParsing: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
			return cmd.Help()
		}
		if err := checkAgent(args[0]); err != nil {
			return err
		}
		return runClaude(cmd, args[1:])
	},
}

var agentSetupCmd = &cobra.Command{
	Use:   "setup [agent]",
	Short: "Check the agent login, give this project's sandbox a GitHub or GitLab token",
	Args:  agentArg,
	RunE:  func(cmd *cobra.Command, _ []string) error { return runClaudeSetup(cmd.Context()) },
}

var agentUpdateCmd = &cobra.Command{
	Use:   "update [agent]",
	Short: "Download the newest agent release now (default: every agent)",
	Args:  agentArg,
	RunE:  func(*cobra.Command, []string) error { return withDocker(noTimeout, runClaudeUpdate) },
}

func init() {
	agentCmd.AddCommand(agentRunCmd, agentSetupCmd, agentCredentialsCmd, agentUpdateCmd)
	rootCmd.AddCommand(agentCmd)
}

// agentArg accepts no argument (every agent) or one known agent.
func agentArg(_ *cobra.Command, args []string) error {
	if len(args) > 1 {
		return errors.New("takes at most one agent")
	}
	if len(args) == 1 {
		return checkAgent(args[0])
	}
	return nil
}

func checkAgent(name string) error {
	for _, a := range knownAgents {
		if a == name {
			return nil
		}
	}
	return fmt.Errorf("unknown agent %q (known: %s)", name, strings.Join(knownAgents, ", "))
}
