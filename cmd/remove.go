package cmd

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/0ploy/zdev/internal/project"
	"github.com/0ploy/zdev/internal/ssl"
	"github.com/0ploy/zdev/internal/state"
	"github.com/spf13/cobra"
)

var (
	removeVolumes bool
	removeForce   bool
)

var removeCmd = &cobra.Command{
	Use:   "remove NAME",
	Short: "Remove a project from the registry",
	Long: `Remove a registered project by name. If the project directory still exists,
containers and networks are cleaned up (like 'zdev down'). If the directory
no longer exists, the stale entry is simply removed from the registry.`,
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeProjectNames,
	RunE:              runRemove,
}

func init() {
	removeCmd.Flags().BoolVarP(&removeVolumes, "volumes", "v", false, "Also remove volumes")
	removeCmd.Flags().BoolVarP(&removeForce, "force", "f", false, "Skip confirmation prompt")
	rootCmd.AddCommand(removeCmd)
}

func runRemove(cmd *cobra.Command, args []string) error {
	return withDocker(2*time.Minute, func(ctx context.Context) error {
		return runRemoveImpl(ctx, args[0])
	})
}

func runRemoveImpl(ctx context.Context, name string) error {
	stateMgr, err := state.DefaultManager()
	if err != nil {
		return fmt.Errorf("failed to load state: %w", err)
	}

	entry, err := stateMgr.GetProject(name)
	if err != nil {
		return fmt.Errorf("failed to read project registry: %w", err)
	}
	if entry == nil {
		return fmt.Errorf("project %q is not registered", name)
	}

	// If the project directory exists, do a full cleanup
	proj, loadErr := project.LoadFromDir(entry.Path)
	if loadErr != nil {
		if _, statErr := os.Stat(entry.Path); statErr == nil && !removeForce {
			// The directory is there but its config doesn't load (syntax
			// error, a field this zdev doesn't know). Its containers and
			// volumes are still out there - dropping the entry would orphan
			// them without asking.
			return fmt.Errorf("cannot load project %q at %s: %w\n"+
				"Fix the config and retry, or use --force to drop the registry entry anyway "+
				"(containers and volumes are kept; 'zdev cleanup' lists them)", name, entry.Path, loadErr)
		}
		if !removeForce && !confirm(fmt.Sprintf("Project directory %s is gone. Remove the registry entry for %q? [y/N]: ", entry.Path, name)) {
			fmt.Println("Aborted.")
			return nil
		}
		fmt.Println("Removing stale registry entry (leftover containers and volumes: 'zdev cleanup')...")
	} else {
		if !removeForce {
			var msg string
			if removeVolumes {
				msg = volumeRemovalPrompt(name)
			} else {
				msg = fmt.Sprintf("This will remove all containers and networks for project %q.\nVolumes will be kept. Use -v to also remove volumes. Continue? [y/N]: ", name)
			}
			if !confirm(msg) {
				fmt.Println("Aborted.")
				return nil
			}
		}

		fmt.Printf("Removing project %s...\n", name)
		if err := proj.Down(ctx, removeVolumes); err != nil {
			if !removeForce {
				return fmt.Errorf("cleanup failed, project kept registered so it can be retried: %w\n"+
					"Use --force to drop the registry entry anyway", err)
			}
			fmt.Printf("Warning: cleanup failed: %v\n", err)
		}
	}

	if err := stateMgr.UnregisterProject(name); err != nil {
		return fmt.Errorf("failed to unregister project: %w", err)
	}
	if err := stateMgr.RemoveProjectFromLinks(name); err != nil {
		fmt.Printf("Warning: failed to remove %s from its links: %v\n", name, err)
	}
	if err := ssl.RemoveProjectCert(name); err != nil {
		fmt.Printf("Warning: failed to remove project certificate: %v\n", err)
	}

	updateDocsWithProjects(ctx)

	fmt.Printf("Project %s removed\n", name)
	return nil
}
