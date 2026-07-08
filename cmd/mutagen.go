package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/0ploy/zdev/internal/mutagen"
	"github.com/0ploy/zdev/internal/project"
	"github.com/spf13/cobra"
)

// errMutagenDisabled is returned by mutagen subcommands that need file
// sync while it is turned off for this machine.
var errMutagenDisabled = fmt.Errorf("Mutagen file sync is disabled - enable it in ~/.zdev/global-config.yaml")

var mutagenCmd = &cobra.Command{
	Use:   "mutagen",
	Short: "Manage Mutagen file synchronization",
	Long:  `Manage Mutagen file synchronization for the current project. Mutagen provides fast bidirectional file sync between your host filesystem and Docker volumes.`,
}

var mutagenStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show sync status for the project",
	Long:  `Show the status of Mutagen sync sessions for the current project.`,
	RunE:  runMutagenStatus,
}

var mutagenResetCmd = &cobra.Command{
	Use:   "reset",
	Short: "Recreate sync sessions",
	Long:  `Terminate and recreate all Mutagen sync sessions for the current project. Use this if sync gets stuck or has problems.`,
	RunE:  runMutagenReset,
}

var mutagenFlushCmd = &cobra.Command{
	Use:   "flush",
	Short: "Wait for sync completion",
	Long:  `Wait for all pending sync operations to complete. Use this before running commands that depend on file sync being complete.`,
	RunE:  runMutagenFlush,
}

func init() {
	mutagenCmd.AddCommand(mutagenStatusCmd)
	mutagenCmd.AddCommand(mutagenResetCmd)
	mutagenCmd.AddCommand(mutagenFlushCmd)
	rootCmd.AddCommand(mutagenCmd)
}

func runMutagenStatus(cmd *cobra.Command, args []string) error {
	return withProject(30*time.Second, runMutagenStatusImpl)
}

func runMutagenStatusImpl(ctx context.Context, proj *project.Project) error {
	// Check if Mutagen is enabled
	if !proj.IsMutagenEnabled() {
		fmt.Println("Mutagen file sync is disabled")
		fmt.Println()
		fmt.Println("Enable it by setting 'mutagen.enabled: true' in ~/.zdev/global-config.yaml")
		return nil
	}

	// Get Mutagen binary
	m, err := proj.EnsureMutagen(ctx)
	if err != nil {
		return err
	}

	// Get expected sync mounts for this project
	mounts := proj.GetMutagenSyncMounts()

	if len(mounts) == 0 {
		fmt.Println("No directory bind mounts configured - Mutagen sync not needed")
		return nil
	}

	fmt.Printf("Mutagen Sync Status for %s\n", proj.Config.Name)
	fmt.Println("=======================================")
	fmt.Println()

	for _, mount := range mounts {
		exists, _ := m.SessionExists(ctx, mount.SessionName)
		if !exists {
			fmt.Printf("%s: not created\n", mount.SessionName)
			fmt.Printf("  Host:      %s\n", mount.HostPath)
			fmt.Printf("  Container: %s\n", mount.ContainerPath)
			fmt.Println()
			continue
		}

		status, err := m.GetSessionStatus(ctx, mount.SessionName)
		if err != nil {
			status = "unknown"
		}

		fmt.Printf("%s: %s\n", mount.SessionName, status)
		fmt.Printf("  Host:      %s\n", mount.HostPath)
		fmt.Printf("  Container: %s\n", mount.ContainerPath)
		fmt.Println()
	}

	return nil
}

func runMutagenReset(cmd *cobra.Command, args []string) error {
	return withProject(5*time.Minute, runMutagenResetImpl)
}

func runMutagenResetImpl(ctx context.Context, proj *project.Project) error {
	if !proj.IsMutagenEnabled() {
		return errMutagenDisabled
	}

	// Get Mutagen binary
	m, err := proj.EnsureMutagen(ctx)
	if err != nil {
		return err
	}

	// Get expected sync mounts for this project
	mounts := proj.GetMutagenSyncMounts()

	if len(mounts) == 0 {
		fmt.Println("No directory bind mounts configured - nothing to reset")
		return nil
	}

	fmt.Printf("Resetting Mutagen sync for %s...\n", proj.Config.Name)
	fmt.Println()

	// Terminate existing sessions
	for _, mount := range mounts {
		exists, _ := m.SessionExists(ctx, mount.SessionName)
		if exists {
			fmt.Printf("Terminating %s...\n", mount.SessionName)
			if err := m.TerminateSession(ctx, mount.SessionName); err != nil {
				fmt.Printf("Warning: could not terminate %s: %v\n", mount.SessionName, err)
			}
		}
	}

	// Check if containers are running
	containerRunning := false
	for _, mount := range mounts {
		containerName := proj.ContainerName(mount.ServiceName)
		running, _ := proj.Runtime.IsContainerRunning(ctx, containerName)
		if running {
			containerRunning = true
			break
		}
	}

	if !containerRunning {
		fmt.Println()
		fmt.Println("Containers are not running - sessions will be created on next 'zdev start'")
		return nil
	}

	// Recreate sessions
	for _, mount := range mounts {
		containerName := proj.ContainerName(mount.ServiceName)
		beta := fmt.Sprintf("docker://%s%s", containerName, mount.ContainerPath)

		fmt.Printf("Creating %s...\n", mount.SessionName)

		ignores := mutagen.MergeIgnores(proj.Config.Mutagen.Ignore)

		cfg := mutagen.SessionConfig{
			Name:    mount.SessionName,
			Alpha:   mount.HostPath,
			Beta:    beta,
			Ignores: ignores,
		}

		if err := m.CreateSession(ctx, cfg); err != nil {
			return fmt.Errorf("failed to create session %s: %w", mount.SessionName, err)
		}
	}

	fmt.Println()
	fmt.Println("Waiting for initial sync...")

	for _, mount := range mounts {
		if err := m.FlushSession(ctx, mount.SessionName); err != nil {
			fmt.Printf("Warning: could not wait for sync %s: %v\n", mount.SessionName, err)
		}
	}

	fmt.Println("Sync reset complete")
	return nil
}

func runMutagenFlush(cmd *cobra.Command, args []string) error {
	return withProject(5*time.Minute, runMutagenFlushImpl)
}

func runMutagenFlushImpl(ctx context.Context, proj *project.Project) error {
	if !proj.IsMutagenEnabled() {
		return errMutagenDisabled
	}

	// Get Mutagen binary
	m, err := proj.EnsureMutagen(ctx)
	if err != nil {
		return err
	}

	// Get expected sync mounts for this project
	mounts := proj.GetMutagenSyncMounts()

	if len(mounts) == 0 {
		fmt.Println("No directory bind mounts configured - nothing to flush")
		return nil
	}

	fmt.Printf("Waiting for sync to complete for %s...\n", proj.Config.Name)

	for _, mount := range mounts {
		exists, _ := m.SessionExists(ctx, mount.SessionName)
		if !exists {
			fmt.Printf("Session %s does not exist - skipping\n", mount.SessionName)
			continue
		}

		if err := m.FlushSession(ctx, mount.SessionName); err != nil {
			return fmt.Errorf("failed to flush session %s: %w", mount.SessionName, err)
		}
		fmt.Printf("  %s: synced\n", mount.SessionName)
	}

	fmt.Println()
	fmt.Println("All sync operations complete")
	return nil
}
