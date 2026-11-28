package cmd

// zdev agent: everything about the sandboxed coding agents. `zdev agent run
// <agent>` starts a session; `zdev claude` and `zdev pi` are its shortcuts
// and pure pass-throughs, so zdev's own commands never share an agent's
// argument space (Claude has subcommands of its own: update, auth, mcp...;
// pi too: install, update, auth...).

import (
	"context"
	"errors"

	"github.com/spf13/cobra"
)

var agentCmd = &cobra.Command{
	Use:   "agent",
	Short: "Manage agent sandboxes: setup, credentials, update, run",
	Long: `Coding agents in sandboxes of this project (see docs/agents.md).

  zdev agent run claude [service] [args]   a session (shortcut: zdev claude)
  zdev agent run pi [service] [args]       a pi session (shortcut: zdev pi)
  zdev agent setup                         check the login, give the project a GitHub or GitLab token
  zdev agent credentials                   the tokens sandboxes get, swapped in by the egress proxy
  zdev agent update                        download the newest agent release now

Agents: claude, pi.`,
}

var agentRunCmd = &cobra.Command{
	Use:   "run <agent> [service] [agent arguments...]",
	Short: "Run a coding agent in a sandbox of this project",
	Long: `Run a coding agent in a sandbox of this project. Everything after the agent
goes to it (a service name first selects the image); zdev claude is the
shortcut for zdev agent run claude, zdev pi for zdev agent run pi.

Agents: claude, pi.`,
	DisableFlagParsing: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
			return cmd.Help()
		}
		ag, err := agentNamed(args[0])
		if err != nil {
			return err
		}
		return runAgent(ag, args[1:])
	},
}

var agentSetupCmd = &cobra.Command{
	Use:   "setup [agent]",
	Short: "Check the agent login, give this project's sandbox a GitHub or GitLab token",
	Args:  agentArg,
	RunE: func(cmd *cobra.Command, args []string) error {
		name := ""
		if len(args) == 1 {
			name = args[0]
		}
		return runAgentSetup(cmd.Context(), name)
	},
}

var agentUpdateCmd = &cobra.Command{
	Use:   "update [agent]",
	Short: "Download the newest agent release now (default: every agent)",
	Args:  agentArg,
	RunE: func(_ *cobra.Command, args []string) error {
		return withDocker(noTimeout, func(ctx context.Context) error {
			var errs []error
			if len(args) == 0 || args[0] == "claude" {
				errs = append(errs, runClaudeUpdate(ctx))
			}
			if len(args) == 0 || args[0] == "pi" {
				errs = append(errs, runPiUpdate(ctx, len(args) == 1))
			}
			return errors.Join(errs...)
		})
	},
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
		_, err := agentNamed(args[0])
		return err
	}
	return nil
}
