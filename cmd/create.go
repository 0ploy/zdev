package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/create"
	"github.com/0ploy/zdev/internal/project"
	"github.com/0ploy/zdev/internal/tools"
	"github.com/spf13/cobra"
)

var (
	createBranch    string
	createTag       string
	createAutoStart bool
	createAutoSetup bool
)

var createCmd = &cobra.Command{
	Use:   "create <template> [project-name]",
	Short: "Create a new project from a template",
	Long: `Create a new project from a template.

Templates can be local directories, GitHub repositories, or built-in zdev templates.

Template resolution:
  Local path     ./my-template, ~/templates/foo, /absolute/path
  GitHub repo    myorg/myrepo
  Built-in       express  ->  0ploy/zdev-template-express

Examples:
  zdev create express my-app
  zdev create myorg/my-template my-app
  zdev create ./local-template my-app
  zdev create express my-app --auto-setup`,
	Args: cobra.RangeArgs(1, 2),
	RunE: runCreate,
}

func init() {
	createCmd.Flags().StringVar(&createBranch, "branch", "", "GitHub branch to use")
	createCmd.Flags().StringVar(&createTag, "tag", "", "GitHub tag to use")
	createCmd.Flags().BoolVar(&createAutoStart, "auto-start", false, "Run zdev start after create")
	createCmd.Flags().BoolVar(&createAutoSetup, "auto-setup", false, "Run zdev setup after create (implies --auto-start)")
	rootCmd.AddCommand(createCmd)
}

func runCreate(cmd *cobra.Command, args []string) error {
	// Resolve template source
	source, err := create.ResolveTemplate(args[0], createBranch, createTag)
	if err != nil {
		return err
	}

	// Get project name from args or prompt
	var name string
	if len(args) >= 2 {
		name = args[1]
	} else {
		name, err = promptProjectName()
		if err != nil {
			return err
		}
	}

	// Validate name
	if err := create.ValidateName(name); err != nil {
		return err
	}

	// Determine target directory
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get working directory: %w", err)
	}
	targetDir := filepath.Join(cwd, name)

	// Check target doesn't exist
	if _, err := os.Stat(targetDir); err == nil {
		return fmt.Errorf("directory %q already exists", name)
	}

	// Create target directory
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// Clean up on error
	success := false
	defer func() {
		if !success {
			os.RemoveAll(targetDir)
		}
	}()

	// Copy/download template
	fmt.Printf("Creating project %s from %s...\n", name, source.DisplayName())

	switch source.Type {
	case "local":
		if err := create.CopyLocal(source.Path, targetDir); err != nil {
			return fmt.Errorf("failed to copy template: %w", err)
		}
	case "github":
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := create.DownloadGitHub(ctx, source, targetDir); err != nil {
			return err
		}
	}

	success = true
	fmt.Printf("Project created: %s\n", name)

	// Auto-setup implies auto-start
	if createAutoSetup {
		createAutoStart = true
	}

	// Auto-start
	if createAutoStart {
		fmt.Println()
		config.SetProjectDirOverride(targetDir)
		if err := runStart(cmd, nil); err != nil {
			return fmt.Errorf("auto-start failed: %w", err)
		}
	}

	// Auto-setup
	if createAutoSetup {
		if err := runAutoSetup(targetDir); err != nil {
			return fmt.Errorf("auto-setup failed: %w", err)
		}
	}

	// Print next steps if not auto-starting/setup
	if !createAutoStart && !createAutoSetup {
		fmt.Println()
		fmt.Println("Next steps:")
		fmt.Printf("  cd %s\n", name)
		fmt.Println("  zdev setup")
	}

	return nil
}

// promptProjectName interactively asks the user for a project name
func promptProjectName() (string, error) {
	reader := bufio.NewReader(os.Stdin)
	fmt.Print("Project name: ")
	name, err := reader.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("failed to read project name: %w", err)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("project name cannot be empty")
	}
	return name, nil
}

// runAutoSetup finds and executes the setup.just justfile for a project
func runAutoSetup(projectDir string) error {
	proj, err := project.LoadFromDir(projectDir)
	if err != nil {
		return fmt.Errorf("failed to load project: %w", err)
	}

	justfileInfo, err := proj.GetJustfile("setup")
	if err != nil {
		return fmt.Errorf("failed to check for setup justfile: %w", err)
	}
	if justfileInfo == nil {
		fmt.Println("No setup.just found, skipping setup step.")
		return nil
	}

	// Ensure just binary is available
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	// setup.just starts containers - fail with the friendly Docker
	// message up front instead of raw errors mid-setup.
	if err := requireDocker(ctx); err != nil {
		return err
	}

	toolMgr, err := tools.NewManager()
	if err != nil {
		return fmt.Errorf("failed to initialize tool manager: %w", err)
	}

	justPath, err := toolMgr.EnsureTool(ctx, tools.JustTool())
	if err != nil {
		return fmt.Errorf("failed to ensure just is installed: %w", err)
	}

	just := tools.NewJust(justPath)
	env := proj.BuildJustEnv()

	fmt.Println()
	fmt.Println("Running setup...")
	return just.Run(ctx, justfileInfo.Path, proj.Dir, nil, env)
}
