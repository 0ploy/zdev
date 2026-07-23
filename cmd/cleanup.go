package cmd

import (
	"context"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/project"
	"github.com/0ploy/zdev/internal/runtime"
	"github.com/0ploy/zdev/internal/state"
	"github.com/spf13/cobra"
)

var cleanupForce bool

var cleanupCmd = &cobra.Command{
	Use:   "cleanup",
	Short: "Remove unused containers, volumes and stale project registrations",
	Long: `Prune resources no longer associated with any live project:

  - Orphaned Docker containers (not present in a live project's current config)
  - Orphaned Docker volumes (not referenced by a live project's current config)
  - Stale state entries whose project directory no longer exists on disk

Resources used by the current configuration of registered projects are retained.
Stale resources left behind by removed services are included in the confirmation
list before deletion.

Use --force to skip the confirmation prompt.`,
	RunE: runCleanup,
}

func init() {
	cleanupCmd.Flags().BoolVarP(&cleanupForce, "force", "f", false, "skip confirmation prompt")
	rootCmd.AddCommand(cleanupCmd)
}

func runCleanup(cmd *cobra.Command, args []string) error {
	return withDocker(2*time.Minute, func(ctx context.Context) error {
		stateMgr, err := state.DefaultManager()
		if err != nil {
			return fmt.Errorf("failed to load state: %w", err)
		}

		projects, err := stateMgr.ListProjects()
		if err != nil {
			return fmt.Errorf("failed to list projects: %w", err)
		}

		type staleProject struct {
			name string
			path string
		}
		var staleProjects []staleProject
		knownContainers := make(map[string]bool)
		knownVolumes := make(map[string]bool)

		for name, entry := range projects {
			if _, err := os.Stat(entry.Path); err != nil {
				if os.IsNotExist(err) {
					staleProjects = append(staleProjects, staleProject{name: name, path: entry.Path})
					continue
				}
				return fmt.Errorf("failed to inspect project %s at %s: %w", name, entry.Path, err)
			}

			cfg, err := config.LoadProject(entry.Path)
			if err != nil {
				return fmt.Errorf("failed to load live project %s at %s: %w", name, entry.Path, err)
			}
			loaded := &project.Project{Config: cfg}
			for serviceName := range cfg.Services {
				knownContainers[project.ContainerNameFor(serviceName, cfg.Name)] = true
				knownVolumes[project.MutagenVolumeNameFor(serviceName, cfg.Name)] = true
			}
			for _, volumeName := range loaded.NamedVolumes() {
				knownVolumes[project.VolumeNameFor(volumeName, cfg.Name)] = true
			}
		}

		docker := runtime.NewDockerCLI()

		containers, err := docker.ListContainers(ctx, "label=zdev.project")
		if err != nil {
			return fmt.Errorf("failed to list Docker containers: %w", err)
		}

		var orphanContainers []string
		for _, name := range containers {
			if !knownContainers[name] {
				orphanContainers = append(orphanContainers, name)
			}
		}

		dockerVolumes, err := docker.ListVolumes(ctx, "name=.zdev")
		if err != nil {
			return fmt.Errorf("failed to list Docker volumes: %w", err)
		}

		var orphanVolumes []string
		for _, vol := range dockerVolumes {
			if !knownVolumes[vol.Name] {
				orphanVolumes = append(orphanVolumes, vol.Name)
			}
		}

		sort.Slice(staleProjects, func(i, j int) bool { return staleProjects[i].name < staleProjects[j].name })
		sort.Strings(orphanContainers)
		sort.Strings(orphanVolumes)

		if len(staleProjects) == 0 && len(orphanContainers) == 0 && len(orphanVolumes) == 0 {
			fmt.Println("Nothing to clean up.")
			return nil
		}

		if len(staleProjects) > 0 {
			fmt.Printf("Stale project registrations (%d) - directory missing on disk:\n", len(staleProjects))
			for _, p := range staleProjects {
				fmt.Printf("  - %s (%s)\n", p.name, p.path)
			}
			fmt.Println()
		}

		if len(orphanContainers) > 0 {
			fmt.Printf("Orphaned containers (%d) - absent from live project configs:\n", len(orphanContainers))
			for _, name := range orphanContainers {
				fmt.Printf("  - %s\n", name)
			}
			fmt.Println()
		}

		if len(orphanVolumes) > 0 {
			fmt.Printf("Orphaned volumes (%d) - absent from live project configs:\n", len(orphanVolumes))
			for _, name := range orphanVolumes {
				fmt.Printf("  - %s\n", name)
			}
			fmt.Println()
		}

		if !cleanupForce {
			if !confirm("Delete the items above? [y/N]: ") {
				fmt.Println("Aborted.")
				return nil
			}
		}

		for _, p := range staleProjects {
			fmt.Printf("Unregistering %s... ", p.name)
			if err := stateMgr.UnregisterProject(p.name); err != nil {
				fmt.Printf("failed: %v\n", err)
			} else {
				fmt.Println("done")
			}
		}

		for _, name := range orphanContainers {
			fmt.Printf("Removing container %s... ", name)
			if err := docker.ForceRemoveContainer(ctx, name); err != nil {
				fmt.Printf("failed: %v\n", err)
			} else {
				fmt.Println("done")
			}
		}

		var deleted, failed int
		for _, name := range orphanVolumes {
			fmt.Printf("Removing volume %s... ", name)
			if err := docker.RemoveVolume(ctx, name); err != nil {
				fmt.Printf("failed: %v\n", err)
				failed++
			} else {
				fmt.Println("done")
				deleted++
			}
		}

		if len(orphanVolumes) > 0 {
			fmt.Printf("\nRemoved %d volume(s)", deleted)
			if failed > 0 {
				fmt.Printf(", %d failed", failed)
			}
			fmt.Println()
		}

		return nil
	})
}
