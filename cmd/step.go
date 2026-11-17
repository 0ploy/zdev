package cmd

import (
	"strings"

	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/ui"
	"github.com/spf13/cobra"
)

var stepCmd = &cobra.Command{
	Use:   "step <message>",
	Short: "Print a visually distinct status step",
	Long: `Print a visually distinct status step (two blank lines, colored prefix, bold text).

For legacy .zdev/commands/*.just recipes (just is deprecated). Jobs in
.zdev/run/*.toml get the same output from the built-in step helper:

    run = '''
    step "Installing PHP extensions"
    zdev exec app sh -c "apk add ..."
    '''

Convert justfiles with: zdev run --convert-just`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		message := strings.Join(args, " ")

		plainMode := false
		if cfg, err := config.LoadGlobalConfig(); err == nil && cfg != nil {
			plainMode = ui.PlainMode(cfg.Terminal.Plain)
		}

		ui.StatusStep(message, plainMode)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(stepCmd)
}
