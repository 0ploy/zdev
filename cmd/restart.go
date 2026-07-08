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
			service := args[0]
			fmt.Printf("Restarting service %s...\n", service)
			return proj.RestartService(ctx, service)
		}

		fmt.Printf("Restarting project %s...\n", proj.Config.Name)

		if err := proj.Restart(ctx); err != nil {
			return err
		}

		updateDocsWithProjects(ctx)

		fmt.Println()
		fmt.Println("Project Info:")
		fmt.Println()
		return showProjectInfo(ctx, proj)
	})
}
