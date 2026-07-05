package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/0ploy/zdev/internal/project"
	"github.com/spf13/cobra"
)

var (
	updateBuild   bool
	updateNoBuild bool
)

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update project to match config",
	Long: `Update the project to match the current configuration.

This command detects changes in the config and only recreates
containers that need to be updated. Use this after modifying
your config file instead of running 'zdev down && zdev start'.

If the project hasn't been started yet (no network exists), update
will start it from scratch - equivalent to running 'zdev start'.

Changes detected (via a config hash stamped on each container):
- Image changes
- Environment variable changes
- Volume mount changes
- Command / working directory changes
- Routing configuration (host_port, protocol, port, domain)
- Labels, network aliases, and published ports
- Stale dockerfile: images (Dockerfile contents changed)`,
	RunE: runUpdate,
}

func init() {
	updateCmd.Flags().BoolVar(&updateBuild, "build", false, "Force rebuilding images for services with a dockerfile: config")
	updateCmd.Flags().BoolVar(&updateNoBuild, "no-build", false, "Never build images; fail if a dockerfile: image is missing")
	updateCmd.MarkFlagsMutuallyExclusive("build", "no-build")
	rootCmd.AddCommand(updateCmd)
}

func runUpdate(cmd *cobra.Command, args []string) error {
	return withProject(5*time.Minute, func(ctx context.Context, proj *project.Project) error {
		proj.BuildMode = project.BuildModeFromFlags(updateBuild, updateNoBuild)
		fmt.Printf("Updating project %s...\n", proj.Config.Name)

		updated, err := proj.Update(ctx)
		if err != nil {
			return err
		}

		if updated {
			fmt.Printf("Project %s updated\n", proj.Config.Name)
		} else {
			fmt.Printf("Project %s is up to date\n", proj.Config.Name)
		}
		return nil
	})
}
