package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/project"
	runtimePkg "github.com/0ploy/zdev/internal/runtime"
	"github.com/0ploy/zdev/internal/services"
	"github.com/0ploy/zdev/internal/state"
	"github.com/0ploy/zdev/internal/ui"
	"github.com/spf13/cobra"
)

// requireDocker checks that Docker is running and returns an error if not.
// Call this at the top of any command that needs Docker.
func requireDocker(ctx context.Context) error {
	docker := runtimePkg.NewDockerCLI()
	return docker.CheckAvailable(ctx)
}

// withProject is the shared bootstrap for commands that need Docker + a
// loaded project. Creates a timeout context, checks Docker availability,
// loads the project from the current directory (or --config override), and
// calls fn. Any error from Docker check or project load is surfaced
// directly - callers don't need to re-wrap with context.
func withProject(timeout time.Duration, fn func(ctx context.Context, proj *project.Project) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if err := requireDocker(ctx); err != nil {
		return err
	}

	proj, err := project.Load()
	if err != nil {
		return err
	}

	return fn(ctx, proj)
}

// withDocker is withProject without the project load, for commands that
// need Docker but operate on state or global resources only (list,
// services, cleanup, etc.).
func withDocker(timeout time.Duration, fn func(ctx context.Context) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if err := requireDocker(ctx); err != nil {
		return err
	}

	return fn(ctx)
}

// schemeFor returns the URL scheme zdev serves under: https when SSL is
// enabled in the global config, http otherwise (including nil config).
func schemeFor(cfg *config.GlobalConfig) string {
	if cfg != nil && cfg.SSL.Enabled {
		return "https"
	}
	return "http"
}

// openSharedServiceURL opens a shared service URL in the browser
// serviceName is used for error messages (e.g., "mail", "db", "router")
// urlPath is the subdomain (e.g., "mail.shared", "db.shared", "docs.shared")
// statusFn returns the service status
func openSharedServiceURL(
	serviceName string,
	urlPath string,
	statusFn func(context.Context, *services.Manager) (*services.ServiceStatus, error),
) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfg, err := config.LoadGlobalConfig()
	if err != nil {
		return fmt.Errorf("failed to load global config: %w", err)
	}

	mgr := services.NewManager(cfg)

	status, err := statusFn(ctx, mgr)
	if err != nil {
		return err
	}

	if !status.Running {
		return fmt.Errorf("%s service is not running\n\nStart it with: zdev services start", serviceName)
	}

	url := fmt.Sprintf("%s://%s.%s", schemeFor(cfg), urlPath, cfg.Domain)

	plainMode := ui.PlainMode(cfg.Terminal.Plain)
	fmt.Printf("Opening %s\n", ui.Hyperlink(url, url, plainMode))

	return openBrowser(url)
}

// volumeRemovalPrompt is the confirmation shown before destroying a
// project's volumes. Shared by `zdev down -v` and `zdev remove -v`.
func volumeRemovalPrompt(projectName string) string {
	return fmt.Sprintf("This will remove all containers, networks, and volumes for project %q.\nData stored in volumes will be permanently deleted. Continue? [y/N]: ", projectName)
}

// confirm prompts the user with a message and returns true if they answer y/yes.
func confirm(msg string) bool {
	fmt.Print(msg)
	reader := bufio.NewReader(os.Stdin)
	response, err := reader.ReadString('\n')
	if err != nil {
		fmt.Printf("failed to read response: %v\n", err)
		return false
	}
	response = strings.TrimSpace(strings.ToLower(response))
	return response == "y" || response == "yes"
}

// completeProjectNames provides shell completion for registered project names.
func completeProjectNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	stateMgr, err := state.DefaultManager()
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	projects, err := stateMgr.ListProjects()
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	var names []string
	for name := range projects {
		names = append(names, name)
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}

// openBrowser opens the given URL in the default browser
func openBrowser(url string) error {
	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}

	return cmd.Start()
}
