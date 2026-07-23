package cmd

import (
	"context"
	"fmt"
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
