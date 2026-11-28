package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/0ploy/zdev/internal/project"
	"github.com/spf13/cobra"
)

var restartCmd = &cobra.Command{
	Use:   "restart [service]",
	Short: "Restart the project or a single service",
	Long: `Stop and start project services.

Without arguments, restarts every service in the project.
With a service name, restarts only that container in-place. Use this for
quick bounces; to pick up config changes use 'zdev update'.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runRestart,
}

func init() {
	rootCmd.AddCommand(restartCmd)
}

func runRestart(cmd *cobra.Command, args []string) error {
	// Same budget as start - a restart is a stop (fast) plus a start.
	return withProject(5*time.Minute, func(ctx context.Context, proj *project.Project) error {
		if len(args) == 1 {
			// A bounce applies no config, but Docker resolves bind sources
			// again on every start: from a sandbox that writes the host
			// directory (Linux), a source swapped for a symlink since the
			// last check would mount whatever it points at. The policy
			// resolves symlinks, so check it here too (review 2026-11-28, H1).
			if _, ok := project.AgentRequest(); ok {
				agentGate(proj)
			}
			service := args[0]
			fmt.Printf("Restarting service %s...\n", service)
			return proj.RestartService(ctx, service)
		}

		agentGate(proj)
		fmt.Printf("Restarting project %s...\n", proj.Config.Name)

		if err := proj.Restart(ctx); err != nil {
			return err
		}
		recordAgentBaseline(ctx, proj)

		updateDocsWithProjects(ctx)

		fmt.Println()
		fmt.Println("Project Info:")
		fmt.Println()
		return showProjectInfo(ctx, proj)
	})
}
