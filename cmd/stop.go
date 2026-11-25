package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/0ploy/zdev/internal/project"
	"github.com/spf13/cobra"
)

var stopCmd = &cobra.Command{
	Use:   "stop [service]",
	Short: "Stop the project or a single service",
	Long: `Stop project containers (kept around for quick restart).

Without arguments, stops every running service.
With a service name, stops only that one container; other services keep
running.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runStop,
}

func init() {
	rootCmd.AddCommand(stopCmd)
}

func runStop(cmd *cobra.Command, args []string) error {
	return withProject(2*time.Minute, func(ctx context.Context, proj *project.Project) error {
		if len(args) == 1 {
			service := args[0]
			fmt.Printf("Stopping service %s...\n", service)
			return proj.StopService(ctx, service)
		}

		fmt.Printf("Stopping project %s...\n", proj.Config.Name)

		// A sandbox stopping its own project keeps running (and its
		// session with it); the developer's stop ends the sessions.
		if _, ok := project.AgentRequest(); !ok {
			removeSandboxes(ctx, proj)
		}
		if err := proj.Stop(ctx); err != nil {
			return err
		}

		updateDocsWithProjects(ctx)

		fmt.Printf("Project %s stopped\n", proj.Config.Name)
		return nil
	})
}
