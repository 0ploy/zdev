package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/project"
	"github.com/0ploy/zdev/internal/ui"
	"github.com/spf13/cobra"
)

var (
	startQuiet   bool
	startBuild   bool
	startNoBuild bool
)

var startCmd = &cobra.Command{
	Use:   "start [service]",
	Short: "Start the project or a single service",
	Long: `Start project services.

Without arguments, starts every service in the project.
With a service name, starts only that one (project-wide setup like
network and volumes still runs idempotently).`,
	Args: cobra.MaximumNArgs(1),
	RunE: runStart,
}

func init() {
	startCmd.Flags().BoolVarP(&startQuiet, "quiet", "q", false, "Skip project info display after start")
	startCmd.Flags().BoolVar(&startBuild, "build", false, "Force rebuilding images for services with a dockerfile: config")
	startCmd.Flags().BoolVar(&startNoBuild, "no-build", false, "Never build images; fail if a dockerfile: image is missing")
	startCmd.MarkFlagsMutuallyExclusive("build", "no-build")
	rootCmd.AddCommand(startCmd)
}

func runStart(cmd *cobra.Command, args []string) error {
	// Check if first-run setup is needed (before we touch Docker)
	if _, err := RunSystemcheckIfNeeded(); err != nil {
		return err
	}

	return withProject(5*time.Minute, func(ctx context.Context, proj *project.Project) error {
		proj.BuildMode = project.BuildModeFromFlags(startBuild, startNoBuild)
		return runStartImpl(ctx, proj, args)
	})
}

func runStartImpl(ctx context.Context, proj *project.Project, args []string) error {
	plainMode := false
	if gcfg, err := config.LoadGlobalConfig(); err == nil && gcfg != nil {
		plainMode = ui.PlainMode(gcfg.Terminal.Plain)
		// If the local DNS fallback is enabled, make sure its container is
		// up before we verify resolution - system DNS resolves through it.
		ensureDNSFallbackRunning(ctx, gcfg)
	}

	// Verify domain DNS resolves to 127.0.0.1
	if _, err := config.VerifyDomainDNS(proj.Config.Domain); err != nil {
		return fmt.Errorf("DNS verification failed: %w", err)
	}

	if len(args) == 1 {
		service := args[0]
		ui.StatusStep(fmt.Sprintf("Starting service %s", service), plainMode)
		if err := proj.StartService(ctx, service); err != nil {
			return err
		}
		return nil
	}

	ui.StatusStep(fmt.Sprintf("Starting project %s", proj.Config.Name), plainMode)

	if err := proj.Start(ctx); err != nil {
		return err
	}

	updateDocsWithProjects(ctx)

	if startQuiet {
		return nil
	}

	fmt.Println()
	fmt.Println("Project Info:")
	fmt.Println()
	if err := showProjectInfo(ctx, proj); err != nil {
		return err
	}

	if proj.Config.AutoOpenAtStart {
		if globalCfg, err := config.LoadGlobalConfig(); err == nil {
			url := fmt.Sprintf("%s://%s", schemeFor(globalCfg), proj.Config.Domain)
			fmt.Printf("\nOpening %s\n", url)
			_ = openBrowser(url)
		}
	}
	return nil
}
