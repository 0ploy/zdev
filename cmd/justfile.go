package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/project"
	"github.com/0ploy/zdev/internal/tools"
)

// findJustfile attempts to locate a justfile for the given command name
// Returns nil if not in a project directory or justfile doesn't exist
func findJustfile(name string) *project.JustfileInfo {
	// Try to find project directory
	dir, err := config.FindProjectDir()
	if err != nil {
		return nil // Not in a project, let Cobra handle it
	}

	// Check if justfile exists
	justfile, err := project.GetJustfileFromDir(dir, name)
	if err != nil || justfile == nil {
		return nil
	}

	return justfile
}

// runJustfile executes a justfile command.
//
// If the justfile declares a recipe literally named after the file
// (e.g. console.just with a `console *args:` recipe), zdev prepends
// that recipe name so args containing colons (cache:clear, db:migrate)
// flow through as recipe arguments instead of being interpreted as
// just's module-path separator. If no such recipe exists, args pass
// through unchanged (legacy behavior - first arg is the recipe name).
func runJustfile(justfile *project.JustfileInfo, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	// Recipes almost always shell into containers via `zdev exec` - fail
	// with the friendly Docker message up front instead of raw errors
	// mid-recipe.
	if err := requireDocker(ctx); err != nil {
		return err
	}

	// Ensure just is available
	toolMgr, err := tools.NewManager()
	if err != nil {
		return fmt.Errorf("failed to initialize tool manager: %w", err)
	}

	justPath, err := toolMgr.EnsureTool(ctx, tools.JustTool())
	if err != nil {
		return fmt.Errorf("failed to ensure just is installed: %w", err)
	}

	// Load project for environment variables
	proj, err := project.Load()
	if err != nil {
		return fmt.Errorf("failed to load project: %w", err)
	}

	just := tools.NewJust(justPath)

	// Build environment
	env := proj.BuildJustEnv()

	// Decide whether to auto-prepend the filename as recipe name
	finalArgs := buildJustArgs(ctx, just, justfile, args)

	// Run justfile from the project root so relative paths in recipes
	// resolve as users expect (not .zdev/commands/ where the file lives).
	return just.Run(ctx, justfile.Path, proj.Dir, finalArgs, env)
}

// buildJustArgs returns the args to pass to just. When the justfile
// declares a recipe literally named after the file, the filename is
// prepended so args containing colons flow through to the recipe body.
// Falls back to the raw args if listing recipes fails (e.g. justfile
// has a syntax error) - the user still sees just's real error when we
// run it.
func buildJustArgs(ctx context.Context, just *tools.Just, justfile *project.JustfileInfo, args []string) []string {
	listCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	recipes, err := just.ListRecipes(listCtx, justfile.Path)
	if err != nil {
		return args
	}
	for _, r := range recipes {
		if r == justfile.Name {
			return append([]string{justfile.Name}, args...)
		}
	}
	return args
}
