package cmd

import (
	"fmt"

	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/project"
	"github.com/0ploy/zdev/internal/state"
	"github.com/0ploy/zdev/internal/ui"
	"github.com/spf13/cobra"
)

var openCmd = &cobra.Command{
	Use:   "open [project]",
	Short: "Open the project URL in the browser",
	Long: `Open the project's domain in the default browser.

Without arguments, opens the current project (based on the working directory).
With a project name, opens that registered project's domain.`,
	Args:              cobra.MaximumNArgs(1),
	ValidArgsFunction: completeProjectNames,
	RunE:              runOpen,
}

func init() {
	rootCmd.AddCommand(openCmd)
}

func runOpen(cmd *cobra.Command, args []string) error {
	domain, err := resolveOpenDomain(args)
	if err != nil {
		return err
	}
	if domain == "" {
		return fmt.Errorf("project has no domain configured")
	}

	gcfg, err := config.LoadGlobalConfig()
	if err != nil {
		return fmt.Errorf("failed to load global config: %w", err)
	}

	url := fmt.Sprintf("%s://%s", schemeFor(gcfg), domain)

	plainMode := false
	if gcfg != nil {
		plainMode = ui.PlainMode(gcfg.Terminal.Plain)
	}
	fmt.Printf("Opening %s\n", ui.Hyperlink(url, url, plainMode))

	return openBrowser(url)
}

func resolveOpenDomain(args []string) (string, error) {
	if len(args) == 0 {
		proj, err := project.Load()
		if err != nil {
			return "", err
		}
		return proj.Config.Domain, nil
	}

	stateMgr, err := state.DefaultManager()
	if err != nil {
		return "", fmt.Errorf("failed to load state: %w", err)
	}
	entry, err := stateMgr.GetProject(args[0])
	if err != nil {
		return "", fmt.Errorf("failed to get project: %w", err)
	}
	if entry == nil {
		return "", fmt.Errorf("project %q not found in registry", args[0])
	}
	proj, err := project.LoadFromDir(entry.Path)
	if err != nil {
		return "", fmt.Errorf("failed to load project from %s: %w", entry.Path, err)
	}
	return proj.Config.Domain, nil
}
