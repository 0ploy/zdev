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
	Long:  `Start shared services (router, mail, db). This creates the shared network and starts Traefik, Mailpit, and Adminer.`,
	RunE:  runServicesStart,
}

var servicesStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop shared services",
	Long:  `Stop the shared services (router, mail, db).`,
	RunE:  runServicesStop,
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

		// Stop in reverse order (router last)
		registry := sharedServiceRegistry()
		for i := len(registry) - 1; i >= 0; i-- {
			if err := registry[i].Stop(ctx, mgr); err != nil {
				return err
			}
		}

		fmt.Println()
		fmt.Println("Shared services stopped")
		return nil
	})
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
	routerStatus, err := registry[0].Status(ctx, mgr)
	if err != nil {
		return err
	}
	if routerStatus.Running {
		fmt.Printf("Docs:   running (%s://docs.shared.%s)\n", protocol, cfg.Domain)
	} else {
		fmt.Println("Docs:   stopped (requires router)")
	}

	for _, svc := range registry {
		status, err := svc.Status(ctx, mgr)
		if err != nil {
			return err
		}
		if status.Running {
			fmt.Printf("%-7s running (%s://%s.%s)\n", svc.Name+":", protocol, svc.Subdomain, cfg.Domain)
		} else {
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
		_ = registry[i].Stop(ctx, mgr)
	}

	// Remove containers (reverse order)
	fmt.Println("Removing containers...")
	for i := len(registry) - 1; i >= 0; i-- {
		_ = docker.RemoveContainer(ctx, registry[i].ContainerName)
	}

	// Start fresh
	fmt.Println("Starting services...")
	for _, svc := range registry {
		if err := svc.Start(ctx, mgr); err != nil {
			return err
		}
	}

	printSharedServiceURLs(cfg, "Shared services recreated:")
	return nil
}
