package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/project"
	"github.com/0ploy/zdev/internal/services"
	"github.com/0ploy/zdev/internal/state"
	"github.com/0ploy/zdev/internal/ui"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var infoRaw bool

var infoCmd = &cobra.Command{
	Use:   "info",
	Short: "Show project information",
	Long:  `Display detailed information about the current project including services, volumes, and status.`,
	RunE:  runInfo,
}

func init() {
	infoCmd.Flags().BoolVar(&infoRaw, "raw", false, "disable markdown rendering for info text")
	rootCmd.AddCommand(infoCmd)
}

func runInfo(cmd *cobra.Command, args []string) error {
	// info deliberately does NOT require Docker so users can inspect a
	// project config even when Docker isn't running. Statuses degrade
	// gracefully to "not created" / "unknown".
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	proj, err := project.Load()
	if err != nil {
		return err
	}

	return showProjectInfo(ctx, proj)
}

// showProjectInfo displays project information - can be called from other commands
func showProjectInfo(ctx context.Context, proj *project.Project) error {
	// Load global config for terminal settings
	cfg, _ := config.LoadGlobalConfig()
	plainMode := cfg != nil && ui.PlainMode(cfg.Terminal.Plain)
	globalDomain := config.DefaultDomain
	if cfg != nil && cfg.Domain != "" {
		globalDomain = cfg.Domain
	}
	protocol := schemeFor(cfg)

	// Project header
	fmt.Printf("Project: %s\n", proj.Config.Name)
	fmt.Printf("Path: %s\n", proj.Dir)
	if proj.Config.Domain != "" {
		projectURL := fmt.Sprintf("%s://%s", protocol, proj.Config.Domain)
		hint := ui.HyperlinkKeyHint(plainMode)
		if hint != "" {
			hint = " " + hint
		}
		fmt.Printf("URL:  %s%s\n", ui.Hyperlink(projectURL, projectURL, plainMode), hint)
	}
	// Show additional service-level custom domains
	for _, svc := range proj.Config.Services {
		if svc.Routing != nil && svc.Routing.Domain != "" && svc.Routing.Domain != proj.Config.Domain {
			svcURL := fmt.Sprintf("%s://%s", protocol, svc.Routing.Domain)
			fmt.Printf("      %s\n", ui.Hyperlink(svcURL, svcURL, plainMode))
		}
	}
	fmt.Println()

	// Services
	fmt.Println("Services:")
	for serviceName, svc := range proj.Config.Services {
		containerName := proj.ContainerName(serviceName)
		status := proj.ContainerStatus(ctx, containerName)
		fmt.Printf("  %-15s %-12s %s\n", serviceName, ui.StatusColor(status, plainMode), svc.Image)
	}
	fmt.Println()

	// Volumes
	if len(proj.NamedVolumes()) > 0 {
		fmt.Println("Volumes:")
		volumes, err := proj.Volumes(ctx)
		if err != nil {
			fmt.Printf("  Error loading volumes: %v\n", err)
		} else {
			for _, vol := range volumes {
				existsStr := "not created"
				if vol.Exists {
					existsStr = "created"
				}
				fmt.Printf("  %-15s %s\n", vol.Name, existsStr)
			}
		}
		fmt.Println()
	}

	// Network
	networkName := proj.NetworkName()
	networkExists, _ := proj.Runtime.NetworkExists(ctx, networkName)
	networkStatus := "not created"
	if networkExists {
		networkStatus = "created"
	}
	fmt.Printf("Network: %s (%s)\n", networkName, networkStatus)
	fmt.Println()

	// Shared services
	enabledShared := enabledSharedServices(services.NewManager(cfg), &proj.Config.Shared)
	if len(enabledShared) > 0 {
		fmt.Println("Shared Services:")
		for _, service := range enabledShared {
			for index, link := range sharedServiceLinks(service, protocol, globalDomain) {
				fmt.Printf("  %-14s %s\n", sharedServiceDisplayName(service, index)+":", ui.Hyperlink(link, link, plainMode))
			}
			if service.ContainerName == services.DBUIContainerName {
				printDatabaseHosts(proj, "                 ")
			}
		}
		fmt.Println()
	}

	// Links
	if stateMgr, err := state.DefaultManager(); err == nil {
		if links, err := stateMgr.GetLinksForProject(proj.Config.Name); err == nil && len(links) > 0 {
			fmt.Println("Links:")
			for linkName, entry := range links {
				memberStrs := make([]string, 0, len(entry.Members))
				for _, m := range entry.Members {
					if m.Project != proj.Config.Name {
						memberStrs = append(memberStrs, m.String())
					}
				}
				if len(memberStrs) > 0 {
					fmt.Printf("  %-15s %s\n", linkName, strings.Join(memberStrs, ", "))
				} else {
					fmt.Printf("  %s\n", linkName)
				}
			}
			fmt.Println()
		}
	}

	// Project info (from config)
	if proj.Config.Info != "" {
		fmt.Println("Notes:")
		if err := renderInfo(proj.Config.Info, plainMode); err != nil {
			fmt.Println(proj.Config.Info)
		}
	}

	return nil
}

// renderInfo renders markdown content if terminal supports it and --raw is not set
func renderInfo(content string, plainMode bool) error {
	// Skip markdown rendering if --raw flag is set, plain mode, or not a terminal
	if infoRaw || plainMode || !term.IsTerminal(int(os.Stdout.Fd())) {
		fmt.Println(content)
		return nil
	}

	fmt.Print(ui.RenderMarkdown(content, false))
	return nil
}

// isDBService checks if a service name suggests it's a database service
func isDBService(name string) bool {
	return services.IsDBServiceByName(name)
}
