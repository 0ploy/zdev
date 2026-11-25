package cmd

// Host-side views of the agent sandboxes: zdev status lists them and warns
// about .git sync conflicts, zdev stop / down remove them, and
// `zdev mutagen repair-git` resolves the conflicts.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/0ploy/zdev/internal/agent"
	"github.com/0ploy/zdev/internal/project"
	"github.com/0ploy/zdev/internal/ui"
	"github.com/spf13/cobra"
)

// printSandboxes lists the project's agent sandboxes and the .git conflicts
// of the agent's sync session, if any.
func printSandboxes(ctx context.Context, proj *project.Project, plain bool) {
	d := agent.NewDocker()
	boxes, err := agent.Sandboxes(ctx, d, proj.Config.Name)
	if err == nil && len(boxes) > 0 {
		names := make([]string, 0, len(boxes))
		for n := range boxes {
			names = append(names, n)
		}
		sort.Strings(names)
		fmt.Println("Agent sandboxes:")
		for _, n := range names {
			status := proj.ContainerStatus(ctx, n)
			fmt.Printf("  %-34s %-10s image of %s\n", n, ui.StatusColor(status, plain), boxes[n][agent.LabelService])
		}
		fmt.Println()
	}
	printGitConflicts(ctx, proj, plain)
}

// printGitConflicts warns when the agent's sync session stopped syncing files
// below .git (both sides wrote them, e.g. the index). Silent otherwise.
func printGitConflicts(ctx context.Context, proj *project.Project, plain bool) {
	_, paths, err := proj.AgentGitConflicts(ctx)
	if err != nil || len(paths) == 0 {
		return
	}
	fmt.Println(ui.Color("Git sync conflict: "+strings.Join(paths, ", "), "yellow", plain))
	fmt.Println("  Git wrote these files on this machine and in the agent's sandbox at the same")
	fmt.Println("  time, so they no longer sync. Fix it with: zdev mutagen repair-git")
	fmt.Println()
}

// removeSandboxes removes the project's agent sandboxes (ending their sessions).
func removeSandboxes(ctx context.Context, proj *project.Project) {
	d := agent.NewDocker()
	boxes, err := agent.Sandboxes(ctx, d, proj.Config.Name)
	if err != nil {
		return
	}
	for n := range boxes {
		fmt.Printf("Removing agent sandbox %s...\n", n)
		_, _ = d.Output(ctx, "rm", "-f", n)
	}
}

var mutagenRepairGitCmd = &cobra.Command{
	Use:   "repair-git",
	Short: "Resolve .git sync conflicts between this machine and the agent sandbox",
	Long: `Resolve .git sync conflicts of the agent's sync session.

When git writes the same file on this machine and in an agent sandbox at the
same time (usually .git/index: an editor's git status while the agent
commits), the sync stops carrying it. repair-git copies this machine's version
into the container, and when the index was among them runs "git reset -q" here,
which brings the index to HEAD (including the agent's commits). Staged changes
become unstaged; nothing in the working tree changes.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return withProject(2*time.Minute, func(ctx context.Context, proj *project.Project) error {
			paths, reset, err := proj.RepairAgentGit(ctx)
			if err != nil {
				return err
			}
			if len(paths) == 0 {
				fmt.Println("No .git sync conflicts.")
				return nil
			}
			fmt.Printf("Resolved: %s\n", strings.Join(paths, ", "))
			if reset {
				fmt.Println("Ran git reset -q: the index matches HEAD again (staged changes are now unstaged).")
			}
			return nil
		})
	},
}

func init() {
	mutagenCmd.AddCommand(mutagenRepairGitCmd)
}
