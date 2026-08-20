package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/project"
	"github.com/0ploy/zdev/internal/services"
	"github.com/0ploy/zdev/internal/ui"
)

// enabledSharedServices returns the shared services this project uses:
// those its config opts into AND that the host actually provides (see
// SharedServiceDef.IsEnabled - the DNS fallback only exists on hosts where
// the resolver file is installed).
func enabledSharedServices(manager *services.Manager, shared *config.ProjectSharedConfig) []services.SharedServiceDef {
	var enabled []services.SharedServiceDef
	for _, service := range services.AllSharedServices() {
		if service.ProjectEnabled(shared) && service.IsEnabled(manager) {
			enabled = append(enabled, service)
		}
	}
	return enabled
}

func sharedServiceLinks(service services.SharedServiceDef, scheme, domain string) []string {
	// Services without a web UI (the DNS fallback) have no URLs at all.
	if !service.HasWebUI() {
		return nil
	}

	var subdomains []string
	if service.ContainerName == services.RouterContainerName {
		subdomains = append(subdomains, "docs.shared")
	}
	subdomains = append(subdomains, service.Subdomain)

	links := make([]string, 0, len(subdomains))
	for _, subdomain := range subdomains {
		links = append(links, fmt.Sprintf("%s://%s.%s", scheme, subdomain, domain))
	}
	return links
}

func sharedServiceDisplayName(service services.SharedServiceDef, linkIndex int) string {
	if service.ContainerName == services.RouterContainerName && linkIndex == 0 {
		return "docs"
	}
	return strings.ToLower(service.Name)
}

func printDatabaseHosts(proj *project.Project, indent string) {
	for serviceName, service := range proj.Config.Services {
		if service.RegisterToDBUI || isDBService(serviceName) {
			fmt.Printf("%s└ %s\n", indent, proj.ContainerName(serviceName))
		}
	}
}

func sharedServiceStatus(ctx context.Context, manager *services.Manager, service services.SharedServiceDef) string {
	status, err := service.Status(ctx, manager)
	if err != nil {
		return "unknown"
	}
	if status.Running {
		return "running"
	}
	return "stopped"
}

func printSharedServiceStatus(ctx context.Context, proj *project.Project, cfg *config.GlobalConfig, plainMode bool) {
	fmt.Println("Shared Services:")
	manager := services.NewManager(cfg)
	scheme := schemeFor(cfg)

	for _, service := range enabledSharedServices(manager, &proj.Config.Shared) {
		status := sharedServiceStatus(ctx, manager, service)
		fmt.Printf("  %-15s %s\n", strings.ToLower(service.Name), ui.StatusColor(status, plainMode))
		if status != "running" {
			continue
		}
		for _, link := range sharedServiceLinks(service, scheme, cfg.Domain) {
			fmt.Printf("                  %s\n", ui.Hyperlink(link, link, plainMode))
		}
		if service.ContainerName == services.DBUIContainerName {
			printDatabaseHosts(proj, "                  ")
		}
	}
}
