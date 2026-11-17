package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/project"
	"github.com/0ploy/zdev/internal/state"
	"github.com/0ploy/zdev/internal/ui"
	"github.com/spf13/cobra"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show project and services status",
	Long:  `Display the status of project services and shared infrastructure.`,
	RunE:  runStatus,
}

func init() {
	rootCmd.AddCommand(statusCmd)
}

func runStatus(cmd *cobra.Command, args []string) error {
	return withProject(30*time.Second, func(ctx context.Context, proj *project.Project) error {
		return runStatusImpl(ctx, proj)
	})
}

func runStatusImpl(ctx context.Context, proj *project.Project) error {
	// Load global config for terminal settings
	cfg, err := config.LoadGlobalConfig()
	if err != nil {
		return err
	}
	plainMode := ui.PlainMode(cfg.Terminal.Plain)
	protocol := schemeFor(cfg)

	// Project header
	fmt.Printf("Project: %s\n", proj.Config.Name)
	if proj.Config.Domain != "" {
		projectURL := fmt.Sprintf("%s://%s", protocol, proj.Config.Domain)
		hint := ui.HyperlinkKeyHint(plainMode)
		if hint != "" {
			hint = " " + hint
		}
		fmt.Printf("URL:     %s%s\n", ui.Hyperlink(projectURL, projectURL, plainMode), hint)
	}
	fmt.Println()

	// Project services
	fmt.Println("Services:")
	for serviceName := range proj.Config.Services {
		containerName := proj.ContainerName(serviceName)
		status := proj.ContainerStatus(ctx, containerName)
		fmt.Printf("  %-15s %s\n", serviceName, ui.StatusColor(status, plainMode))
	}
	fmt.Println()

	printStaleFileMounts(ctx, proj, plainMode)
	printShadowedJustfiles(os.Stdout, shadowedJustfiles(proj), plainMode)

	printSharedServiceStatus(ctx, proj, cfg, plainMode)

	// Links
	if stateMgr, err := state.DefaultManager(); err == nil {
		if links, err := stateMgr.GetLinksForProject(proj.Config.Name); err == nil && len(links) > 0 {
			fmt.Println()
			fmt.Println("Links:")
			for linkName, entry := range links {
				// Show other members (not this project)
				var others []string
				for _, m := range entry.Members {
					if m.Project != proj.Config.Name {
						others = append(others, m.String())
					}
				}
				networkExists, _ := proj.Runtime.NetworkExists(ctx, entry.Network)
				netStatus := "active"
				if !networkExists {
					netStatus = "network missing"
				}
				if len(others) > 0 {
					fmt.Printf("  %-15s %-12s linked to: %s\n", linkName, ui.StatusColor(netStatus, plainMode), strings.Join(others, ", "))
				} else {
					fmt.Printf("  %-15s %s\n", linkName, ui.StatusColor(netStatus, plainMode))
				}
			}
		}
	}

	return nil
}

// printStaleFileMounts warns about single-file bind mounts whose host file was
// replaced since the container started. The container keeps the old, unlinked
// inode, so the file reads stale or missing there while looking perfectly fine
// on the host - a failure that is otherwise indistinguishable from a bug in
// the user's own code.
func printStaleFileMounts(ctx context.Context, proj *project.Project, plainMode bool) {
	stale := proj.StaleFileMounts(ctx)
	if len(stale) == 0 {
		return
	}

	// StatusColor only knows container states, so colour the note directly.
	note := "host file was replaced"
	if !plainMode && ui.SupportsColors() {
		note = ui.Color(note, "yellow", false)
	}

	fmt.Println("Stale file mounts:")
	for _, mount := range stale {
		fmt.Printf("  %-15s %-30s %s\n", mount.ServiceName, mount.ContainerPath, note)
	}
	fmt.Println()
	fmt.Println("  These containers hold a deleted copy of the file - the host version is")
	fmt.Println("  fine. Restart the affected services to reattach them.")
	fmt.Println()
}

// shadowedJustfiles returns the names of .zdev/commands/*.just files that a
// built-in command shadows: `zdev <name>` runs the built-in, so the file
// never runs and nothing else says so.
func shadowedJustfiles(proj *project.Project) []string {
	justfiles, err := proj.DiscoverJustfiles()
	if err != nil {
		return nil
	}
	var names []string
	for _, jf := range justfiles {
		if isBuiltinCommand(jf.Name) {
			names = append(names, jf.Name)
		}
	}
	return names
}

func printShadowedJustfiles(w io.Writer, names []string, plainMode bool) {
	if len(names) == 0 {
		return
	}
	note := "never runs"
	if !plainMode && ui.SupportsColors() {
		note = ui.Color(note, "yellow", false)
	}
	fmt.Fprintln(w, "Shadowed commands:")
	for _, name := range names {
		fmt.Fprintf(w, "  %-15s %s: `zdev %s` is a built-in\n", name+".just", note, name)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  Convert them with `zdev run --convert-just` and run them as `zdev run <name>`.")
	fmt.Fprintln(w)
}
