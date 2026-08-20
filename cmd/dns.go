package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/resolver"
	"github.com/0ploy/zdev/internal/services"
	"github.com/spf13/cobra"
)

var dnsCmd = &cobra.Command{
	Use:   "dns",
	Short: "Manage the local DNS fallback for rebinding-protected routers",
	Long: `Some routers block DNS answers that point at 127.0.0.1 (DNS rebinding
protection), which breaks zdev's wildcard-record trick for *.<domain>.

The local DNS fallback runs a small dnsmasq container and points your OS at
it for the zdev domain only (via /etc/resolver on macOS, systemd-resolved on
Linux). The router is bypassed for that domain; all other DNS is untouched.`,
}

var dnsEnableCmd = &cobra.Command{
	Use:   "enable",
	Short: "Enable the local DNS fallback (requires sudo for the host resolver config)",
	RunE:  runDNSEnable,
}

var dnsDisableCmd = &cobra.Command{
	Use:   "disable",
	Short: "Disable the local DNS fallback and revert to normal DNS",
	RunE:  runDNSDisable,
}

var dnsStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show local DNS fallback status",
	RunE:  runDNSStatus,
}

func init() {
	dnsCmd.AddCommand(dnsEnableCmd, dnsDisableCmd, dnsStatusCmd)
	rootCmd.AddCommand(dnsCmd)
}

func runDNSEnable(cmd *cobra.Command, args []string) error {
	return withDocker(2*time.Minute, func(ctx context.Context) error {
		cfg, err := config.LoadGlobalConfig()
		if err != nil {
			return fmt.Errorf("failed to load global config: %w", err)
		}

		if ok, reason := resolver.Supported(); !ok {
			fmt.Printf("Automatic setup is unavailable: %s\n\n", reason)
			printManualDNSInstructions(cfg.Domain)
			return fmt.Errorf("cannot configure the DNS fallback automatically on this system")
		}

		fmt.Println("Starting local DNS container...")
		if err := enableDNSFallback(ctx, cfg); err != nil {
			return err
		}

		fmt.Printf("\nLocal DNS fallback enabled for *.%s\n", cfg.Domain)
		fmt.Println("Your router is now bypassed for this domain. Run 'zdev start' as usual.")
		return nil
	})
}

func runDNSDisable(cmd *cobra.Command, args []string) error {
	return withDocker(1*time.Minute, func(ctx context.Context) error {
		cfg, err := config.LoadGlobalConfig()
		if err != nil {
			return fmt.Errorf("failed to load global config: %w", err)
		}

		// Remove ALL zdev-managed resolver config (idempotent, and covers a
		// domain that was changed since enabling), not just the current
		// domain's - otherwise disable could leave an orphaned route behind.
		if err := resolver.Remove(); err != nil {
			return fmt.Errorf("failed to remove host resolver config: %w", err)
		}

		mgr := services.NewManager(cfg)
		if err := mgr.StopDNS(ctx); err != nil {
			return err
		}

		fmt.Printf("\nLocal DNS fallback disabled. *.%s resolves via normal DNS again.\n", cfg.Domain)
		return nil
	})
}

func runDNSStatus(cmd *cobra.Command, args []string) error {
	cfg, err := config.LoadGlobalConfig()
	if err != nil {
		return fmt.Errorf("failed to load global config: %w", err)
	}

	supported, reason := resolver.Supported()
	installed, _ := resolver.IsInstalled(cfg.Domain)

	fmt.Println("Local DNS fallback")
	fmt.Println("==================")
	fmt.Printf("Domain:          *.%s\n", cfg.Domain)
	fmt.Printf("Host resolver:   %s\n", enabledText(installed))
	if !supported {
		fmt.Printf("Platform:        unsupported (%s)\n", reason)
	}

	if err := withDocker(30*time.Second, func(ctx context.Context) error {
		mgr := services.NewManager(cfg)
		status, err := mgr.DNSStatus(ctx)
		if err != nil {
			return err
		}
		state := "stopped"
		if status.Running {
			state = "running"
		}
		fmt.Printf("DNS container:   %s\n", state)
		return nil
	}); err != nil {
		fmt.Printf("DNS container:   unknown (%v)\n", err)
	}

	if installed {
		fmt.Println("\nStatus: enabled")
	} else {
		fmt.Println("\nStatus: disabled (enable with 'zdev dns enable')")
	}
	return nil
}

func enabledText(b bool) string {
	if b {
		return "configured"
	}
	return "not configured"
}

// enableDNSFallback starts the DNS container and points the host resolver
// at it. The container is started BEFORE the resolver is installed so
// *.<domain> resolution never has a window where the OS routes queries to a
// container that isn't answering yet. Callers must confirm resolver.Supported()
// first (runDNSEnable and the systemcheck offer both do).
func enableDNSFallback(ctx context.Context, cfg *config.GlobalConfig) error {
	mgr := services.NewManager(cfg)
	if err := mgr.StartDNS(ctx); err != nil {
		return fmt.Errorf("failed to start DNS container: %w", err)
	}
	if err := resolver.Install(cfg.Domain, config.DNSResolverPort); err != nil {
		return fmt.Errorf("failed to configure host resolver: %w", err)
	}
	return nil
}

// ensureDNSFallbackRunning starts the local DNS fallback container when the
// host is configured to use it. Best-effort and non-fatal: called on
// project/first-run start paths so a crashed or removed container is brought
// back before anything relies on *.<domain> resolving. No-op when the
// fallback isn't enabled.
//
// The shared service registry also starts DNS as part of a project start,
// but that happens too late for callers that verify resolution first (see
// runStartImpl and checkDNS) - hence this explicit, earlier ensure. Both
// paths funnel into the idempotent StartDNS.
func ensureDNSFallbackRunning(ctx context.Context, cfg *config.GlobalConfig) {
	installed, err := resolver.IsInstalled(cfg.Domain)
	if err != nil || !installed {
		return
	}
	mgr := services.NewManager(cfg)
	if err := mgr.StartDNS(ctx); err != nil {
		fmt.Printf("Warning: local DNS fallback container failed to start: %v\n", err)
	}
}

// printManualDNSInstructions tells the user how to point their OS at the
// local DNS container by hand, for systems zdev can't configure itself.
func printManualDNSInstructions(domain string) {
	fmt.Printf("To route *.%s to the local DNS container manually, point your\n", domain)
	fmt.Printf("resolver for that domain at 127.0.0.1:%d.\n", config.DNSResolverPort)
	fmt.Println("On systemd-resolved systems, create /etc/systemd/resolved.conf.d/zdev.conf:")
	fmt.Println("  [Resolve]")
	fmt.Printf("  DNS=127.0.0.1:%d\n", config.DNSResolverPort)
	fmt.Printf("  Domains=~%s\n", domain)
	fmt.Println("then: sudo systemctl restart systemd-resolved")
}
