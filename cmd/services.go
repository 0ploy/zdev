package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/runtime"
	"github.com/0ploy/zdev/internal/services"
	"github.com/spf13/cobra"
)

// sharedServiceRegistry proxies to services.AllSharedServices so commands
// stay on the local package's existing helper name without duplicating
// the definitions.
func sharedServiceRegistry() []services.SharedServiceDef {
	return services.AllSharedServices()
}

var servicesCmd = &cobra.Command{
	Use:   "services",
	Short: "Manage shared services",
	Long:  `Manage shared infrastructure services like Traefik router, Mailpit, and Adminer.`,
}

var servicesStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start shared services",
	Long: `Start the shared services: the Traefik router, Mailpit, Adminer,
RedisInsight, Dozzle, and - on hosts where it is enabled - the local DNS
fallback. This also creates the shared Docker network.`,
	RunE: runServicesStart,
}

var servicesStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop shared services",
	Long: `Stop the shared services.

If the local DNS fallback is enabled, this stops it too, so *.<domain> will
not resolve until 'zdev services start' (or 'zdev start') brings it back.
To turn the fallback off for good, use 'zdev dns disable' - that reverts the
host resolver config first, so normal DNS takes over.`,
	RunE: runServicesStop,
}

var servicesStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show shared services status",
	Long:  `Show the status of shared services (router, mail, etc.).`,
	RunE:  runServicesStatus,
}

var servicesRecreateCmd = &cobra.Command{
	Use:   "recreate",
	Short: "Recreate shared services",
	Long:  `Stop, remove, and recreate all shared service containers. Use this after updating zdev or when containers need to be rebuilt with new configuration.`,
	RunE:  runServicesRecreate,
}

func init() {
	servicesCmd.AddCommand(servicesStartCmd)
	servicesCmd.AddCommand(servicesStopCmd)
	servicesCmd.AddCommand(servicesStatusCmd)
	servicesCmd.AddCommand(servicesRecreateCmd)
	rootCmd.AddCommand(servicesCmd)
}

func printSharedServiceURLs(cfg *config.GlobalConfig, header string) {
	protocol := schemeFor(cfg)
	fmt.Println()
	fmt.Println(header)
	fmt.Printf("  Docs:   %s://docs.shared.%s\n", protocol, cfg.Domain)
	for _, svc := range sharedServiceRegistry() {
		if !svc.HasWebUI() {
			continue
		}
		fmt.Printf("  %-7s %s://%s.%s\n", svc.Name+":", protocol, svc.Subdomain, cfg.Domain)
	}
}

func runServicesStart(cmd *cobra.Command, args []string) error {
	return withDocker(5*time.Minute, func(ctx context.Context) error {
		cfg, err := config.LoadGlobalConfig()
		if err != nil {
			return fmt.Errorf("failed to load global config: %w", err)
		}

		mgr := services.NewManager(cfg)

		for _, svc := range sharedServiceRegistry() {
			if !svc.IsEnabled(mgr) {
				continue
			}
			if err := svc.Start(ctx, mgr); err != nil {
				return err
			}
		}

		printSharedServiceURLs(cfg, "Shared services started:")
		return nil
	})
}

func runServicesStop(cmd *cobra.Command, args []string) error {
	return withDocker(2*time.Minute, func(ctx context.Context) error {
		cfg, err := config.LoadGlobalConfig()
		if err != nil {
			return fmt.Errorf("failed to load global config: %w", err)
		}

		mgr := services.NewManager(cfg)

		// Stop in reverse order (router, then DNS, last).
		registry := sharedServiceRegistry()
		for i := len(registry) - 1; i >= 0; i-- {
			if err := stopSharedService(ctx, mgr, registry[i]); err != nil {
				return err
			}
		}

		fmt.Println()
		fmt.Println("Shared services stopped")
		return nil
	})
}

// stopSharedService stops one shared service. A service the host doesn't
// provide (the DNS fallback, where the resolver file isn't installed) is
// skipped UNLESS its container is actually running - so a leftover from a
// since-disabled service still gets shut down, without printing "not
// running" noise on the machines that never enabled it.
func stopSharedService(ctx context.Context, mgr *services.Manager, svc services.SharedServiceDef) error {
	if !svc.IsEnabled(mgr) {
		status, err := svc.Status(ctx, mgr)
		if err != nil || !status.Running {
			return nil
		}
	}
	return svc.Stop(ctx, mgr)
}

func runServicesStatus(cmd *cobra.Command, args []string) error {
	return withDocker(30*time.Second, runServicesStatusImpl)
}

func runServicesStatusImpl(ctx context.Context) error {
	cfg, err := config.LoadGlobalConfig()
	if err != nil {
		return fmt.Errorf("failed to load global config: %w", err)
	}

	mgr := services.NewManager(cfg)

	protocol := schemeFor(cfg)

	fmt.Println("Shared Services Status")
	fmt.Println("======================")
	fmt.Println()

	// Docs status depends on router
	registry := sharedServiceRegistry()
	router, ok := services.SharedServiceByContainer(services.RouterContainerName)
	if !ok {
		return fmt.Errorf("router missing from the shared service registry")
	}
	routerStatus, err := router.Status(ctx, mgr)
	if err != nil {
		return err
	}
	if routerStatus.Running {
		fmt.Printf("Docs:   running (%s://docs.shared.%s)\n", protocol, cfg.Domain)
	} else {
		fmt.Println("Docs:   stopped (requires router)")
	}

	for _, svc := range registry {
		if !svc.IsEnabled(mgr) {
			continue
		}
		status, err := svc.Status(ctx, mgr)
		if err != nil {
			return err
		}
		switch {
		case status.Running && svc.HasWebUI():
			fmt.Printf("%-7s running (%s://%s.%s)\n", svc.Name+":", protocol, svc.Subdomain, cfg.Domain)
		case status.Running:
			fmt.Printf("%-7s running\n", svc.Name+":")
		default:
			fmt.Printf("%-7s stopped\n", svc.Name+":")
		}
	}

	return nil
}

func runServicesRecreate(cmd *cobra.Command, args []string) error {
	return withDocker(5*time.Minute, runServicesRecreateImpl)
}

func runServicesRecreateImpl(ctx context.Context) error {
	cfg, err := config.LoadGlobalConfig()
	if err != nil {
		return fmt.Errorf("failed to load global config: %w", err)
	}

	mgr := services.NewManager(cfg)
	docker := runtime.NewDockerCLI()
	registry := sharedServiceRegistry()

	fmt.Println("Recreating shared services...")
	fmt.Println()

	// Stop all services (reverse order)
	fmt.Println("Stopping services...")
	for i := len(registry) - 1; i >= 0; i-- {
		_ = stopSharedService(ctx, mgr, registry[i])
	}

	// Remove containers (reverse order)
	fmt.Println("Removing containers...")
	for i := len(registry) - 1; i >= 0; i-- {
		_ = docker.RemoveContainer(ctx, registry[i].ContainerName)
	}

	// Start fresh
	fmt.Println("Starting services...")
	for _, svc := range registry {
		if !svc.IsEnabled(mgr) {
			continue
		}
		if err := svc.Start(ctx, mgr); err != nil {
			return err
		}
	}

	printSharedServiceURLs(cfg, "Shared services recreated:")
	return nil
}
