package cmd

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/project"
	"github.com/0ploy/zdev/internal/services"
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
	cfg, _ := config.LoadGlobalConfig()
	plainMode := cfg != nil && ui.PlainMode(cfg.Terminal.Plain)
	domain := config.DefaultDomain
	if cfg != nil && cfg.Domain != "" {
		domain = cfg.Domain
	}
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

	// Shared services
	fmt.Println("Shared Services:")
	mgr := services.NewManager(cfg)

	// Router status (includes docs)
	routerStatus := getSharedServiceStatus(ctx, mgr, mgr.RouterStatus)
	if proj.Config.Shared.Router {
		fmt.Printf("  %-15s %s\n", "router", ui.StatusColor(routerStatus, plainMode))
		if routerStatus == "running" {
			docsURL := fmt.Sprintf("%s://docs.shared.%s", protocol, domain)
			fmt.Printf("                  %s\n", ui.Hyperlink(docsURL, docsURL, plainMode))
			routerURL := fmt.Sprintf("%s://router.shared.%s", protocol, domain)
			fmt.Printf("                  %s\n", ui.Hyperlink(routerURL, routerURL, plainMode))
		}
	}

	// Mail status
	if proj.Config.Shared.Mail {
		mailStatus := getSharedServiceStatus(ctx, mgr, mgr.MailStatus)
		fmt.Printf("  %-15s %s\n", "mail", ui.StatusColor(mailStatus, plainMode))
		if mailStatus == "running" {
			url := fmt.Sprintf("%s://mail.shared.%s", protocol, domain)
			fmt.Printf("                  %s\n", ui.Hyperlink(url, url, plainMode))
		}
	}

	// DB status
	if proj.Config.Shared.DBUI {
		dbStatus := getSharedServiceStatus(ctx, mgr, mgr.DBUIStatus)
		fmt.Printf("  %-15s %s\n", "db", ui.StatusColor(dbStatus, plainMode))
		if dbStatus == "running" {
			url := fmt.Sprintf("%s://db.shared.%s", protocol, domain)
			fmt.Printf("                  %s\n", ui.Hyperlink(url, url, plainMode))
			// Show database hostnames for this project
			for serviceName, svc := range proj.Config.Services {
				if svc.RegisterToDBUI || isDBService(serviceName) {
					hostname := proj.ContainerName(serviceName)
					fmt.Printf("                  └ %s\n", hostname)
				}
			}
		}
	}

	// Redis Insights status
	if proj.Config.Shared.RedisInsights {
		redisStatus := getSharedServiceStatus(ctx, mgr, mgr.RedisInsightsStatus)
		fmt.Printf("  %-15s %s\n", "redis", ui.StatusColor(redisStatus, plainMode))
		if redisStatus == "running" {
			url := fmt.Sprintf("%s://redis.shared.%s", protocol, domain)
			fmt.Printf("                  %s\n", ui.Hyperlink(url, url, plainMode))
		}
	}

	// Logs (Dozzle) status
	if proj.Config.Shared.Logs {
		logsStatus := getSharedServiceStatus(ctx, mgr, mgr.LogsStatus)
		fmt.Printf("  %-15s %s\n", "logs", ui.StatusColor(logsStatus, plainMode))
		if logsStatus == "running" {
			url := fmt.Sprintf("%s://logs.shared.%s", protocol, domain)
			fmt.Printf("                  %s\n", ui.Hyperlink(url, url, plainMode))
		}
	}

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

func getSharedServiceStatus(ctx context.Context, mgr *services.Manager, statusFn func(context.Context) (*services.ServiceStatus, error)) string {
	status, err := statusFn(ctx)
	if err != nil {
		return "unknown"
	}
	if status.Running {
		return "running"
	}
	return "stopped"
}

