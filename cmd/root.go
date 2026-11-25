package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/project"
	"github.com/0ploy/zdev/internal/tools"
	"github.com/0ploy/zdev/internal/ui"
	"github.com/0ploy/zdev/internal/updatecheck"
	"github.com/spf13/cobra"
)

var (
	// configDir holds the --config flag value
	configDir string

	rootCmd = &cobra.Command{
		Use:          "zdev",
		Short:        "Local development environment framework",
		Long:         `zdev is a local development environment framework for web applications.`,
		SilenceUsage: true, // Don't show usage on runtime errors (only on invalid args)
		RunE:         runRoot,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			// Set the project directory override if --config was specified
			if configDir != "" {
				config.SetProjectDirOverride(configDir)
			}
			return nil
		},
	}
)

func init() {
	// Global flags
	rootCmd.PersistentFlags().StringVar(&configDir, "config", "", "Path to project directory containing .zdev/ (overrides auto-discovery)")

	// Set custom help template to include project commands before flags
	rootCmd.SetHelpTemplate(`{{with (or .Long .Short)}}{{. | trimTrailingWhitespaces}}

{{end}}{{if or .Runnable .HasSubCommands}}{{.UsageString}}{{end}}`)

	// Custom usage template that includes project commands
	rootCmd.SetUsageTemplate(`Usage:{{if .Runnable}}
  {{.UseLine}}{{end}}{{if .HasAvailableSubCommands}}
  {{.CommandPath}} [command]{{end}}{{if gt (len .Aliases) 0}}

Aliases:
  {{.NameAndAliases}}{{end}}{{if .HasExample}}

Examples:
{{.Example}}{{end}}{{if .HasAvailableSubCommands}}

Available Commands:{{range .Commands}}{{if (or .IsAvailableCommand (eq .Name "help"))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{end}}{{if .HasAvailableLocalFlags}}
{{projectCommands}}
Flags:
{{.LocalFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{if .HasAvailableInheritedFlags}}

Global Flags:
{{.InheritedFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{if .HasHelpSubCommands}}

Additional help topics:{{range .Commands}}{{if .IsAdditionalHelpTopicCommand}}
  {{rpad .CommandPath .CommandPathPadding}} {{.Short}}{{end}}{{end}}{{end}}{{if .HasAvailableSubCommands}}

Use "{{.CommandPath}} [command] --help" for more information about a command.{{end}}
`)

	// Add custom template function for project commands
	cobra.AddTemplateFunc("projectCommands", getProjectCommandsString)
}

func runRoot(cmd *cobra.Command, args []string) error {
	// If no command given, check if we need first-run setup
	ran, err := RunSystemcheckIfNeeded()
	if err != nil {
		return err
	}

	if ran {
		// First-run setup was performed, we're done
		return nil
	}

	// Already initialized, show help
	fmt.Println()
	return cmd.Help()
}

// Execute runs the root command
// It intercepts unknown commands and tries to route them to justfiles
func Execute() error {
	// Inside an agent sandbox zdev is a client of the host (see sandbox.go):
	// no auto-update, and most commands go through the relay.
	if inSandbox() {
		return sandboxExecute()
	}

	// Non-blocking: prints a banner if a prior background install landed
	// a newer binary, and kicks off a new background refresh + install
	// when the cache is stale.
	// Not for a request an agent sandbox sent through the relay.
	if _, ok := project.AgentRequest(); !ok {
		updatecheck.MaybeAutoUpdate(Version)
	}

	// Parse --config flag early so it applies to justfile commands too
	parseConfigFlag()

	// Check if first arg might be a justfile command
	if len(os.Args) > 1 {
		cmdName := os.Args[1]

		// Skip if it looks like a flag
		if strings.HasPrefix(cmdName, "-") {
			return rootCmd.Execute()
		}

		// Skip if it's a built-in command
		if isBuiltinCommand(cmdName) {
			warnShadowedJustfile(cmdName)
			return rootCmd.Execute()
		}

		// Try to find a matching justfile
		if justfile := findJustfile(cmdName); justfile != nil {
			fmt.Fprintln(os.Stderr, justDeprecationWarning(justfile.Name))
			if err := runJustfile(justfile, os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "       in %s\n", justfile.Path)
				return err
			}
			return nil
		}
	}

	return rootCmd.Execute()
}

// parseConfigFlag extracts a global --config flag before Cobra processes the
// args. This is needed because justfile commands bypass Cobra's flag parsing.
//
// Only the global position is scanned (before the command name, i.e. the
// first non-flag argument, or `--`). Anything after it belongs to the command:
// `zdev exec app vitest --config vitest.config.ts` and job arguments must not
// be read as the project directory. `--config` placed after a built-in
// command name is still honored, by Cobra itself (PersistentPreRunE).
func parseConfigFlag() {
	if dir, ok := globalConfigFlag(os.Args[1:]); ok {
		configDir = dir
		config.SetProjectDirOverride(configDir)
	}
}

// globalConfigFlag returns the --config value given before the command name.
func globalConfigFlag(args []string) (string, bool) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			return "", false
		case arg == "--config":
			if i+1 < len(args) {
				return args[i+1], true
			}
			return "", false
		case strings.HasPrefix(arg, "--config="):
			return strings.TrimPrefix(arg, "--config="), true
		case strings.HasPrefix(arg, "-"):
			continue
		default:
			return "", false // command name reached
		}
	}
	return "", false
}

// isBuiltinCommand checks if the name is a registered Cobra command
func isBuiltinCommand(name string) bool {
	for _, cmd := range rootCmd.Commands() {
		if cmd.Name() == name {
			return true
		}
		// Check aliases
		for _, alias := range cmd.Aliases {
			if alias == name {
				return true
			}
		}
	}
	return false
}

// getProjectCommandsString returns the project commands section as a string for the help template
func getProjectCommandsString() string {
	// Try to find project directory
	dir, err := config.FindProjectDir()
	if err != nil {
		return "" // Not in a project, skip
	}

	// Load project to discover justfiles
	proj, err := project.LoadFromDir(dir)
	if err != nil {
		return ""
	}

	justfiles, err := proj.DiscoverJustfiles()
	if err != nil || len(justfiles) == 0 {
		return "" // No justfiles found
	}

	var sb strings.Builder
	sb.WriteString("\nProject Commands (just, deprecated - convert with: zdev run --convert-just):\n")

	// Try to get just binary for listing recipes
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var just *tools.Just
	if toolMgr, err := tools.NewManager(); err == nil {
		if justPath, err := toolMgr.EnsureTool(ctx, tools.JustTool()); err == nil {
			just = tools.NewJust(justPath)
		}
	}

	for _, jf := range justfiles {
		sb.WriteString(fmt.Sprintf("  %s\n", jf.Name))

		// Try to get recipes with descriptions for this justfile
		if just != nil {
			listOutput, err := just.List(ctx, jf.Path)
			if err != nil {
				// Show syntax error in red
				errMsg := fmt.Sprintf("Syntax error in .zdev/commands/%s.just", jf.Name)
				sb.WriteString(fmt.Sprintf("      %s\n", ui.Color(errMsg, "red", false)))
			} else {
				// Parse the recipes, skipping the "Available recipes:" header
				var recipes []string
				var defaultIdx = -1
				for _, line := range strings.Split(listOutput, "\n") {
					line = strings.TrimSpace(line)
					if line == "" || line == "Available recipes:" {
						continue
					}
					// Check if this is the "default" recipe
					recipeName := strings.Fields(line)[0]
					if recipeName == "default" {
						defaultIdx = len(recipes)
					}
					recipes = append(recipes, line)
				}

				// If there's a recipe named "default", move it to the top
				if defaultIdx > 0 {
					defaultLine := recipes[defaultIdx]
					recipes = append([]string{defaultLine}, append(recipes[:defaultIdx], recipes[defaultIdx+1:]...)...)
					defaultIdx = 0
				}

				// Output recipes, marking "default" with *
				for i, line := range recipes {
					if i == defaultIdx && defaultIdx >= 0 {
						sb.WriteString(fmt.Sprintf("    * %s\n", line))
					} else {
						sb.WriteString(fmt.Sprintf("      %s\n", line))
					}
				}
			}
		}
	}

	return sb.String()
}
