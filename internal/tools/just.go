package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/0ploy/zdev/internal/config"
)

// JustTool returns the ToolInfo for just
func JustTool() ToolInfo {
	return ToolInfo{
		Name:        "just",
		Version:     config.JustVersion,
		URLTemplate: config.JustURLTemplate,
		BinaryName:  "just",
		ArchiveType: "tar.gz",
		Checksums: map[string]string{
			"darwin/amd64": "036ead9df9bc31dbae9934ae3b4236ed124d0bafcffd33439460ae084d5c9cb7",
			"darwin/arm64": "6574cea774ed4cae12e24b6aa11857de33e80dab4667cf628fd5840df96a2a9d",
			"linux/amd64":  "409044617b6f06517c1035e55ca38c8c856ff8b650e41bc2aa1ef82d5903a23e",
			"linux/arm64":  "23871b1702bfd550fb5f901778fd4290ecf2ee034a45a889fbe96d89b188086d",
		},
		URLBuilder: func(template, version, goos, goarch string) string {
			arch := JustArch(goarch)
			os := JustOS(goos)
			return fmt.Sprintf(template, version, version, arch, os)
		},
	}
}

// Just wraps just operations
type Just struct {
	binaryPath string
}

// NewJust creates a new just wrapper
func NewJust(binaryPath string) *Just {
	return &Just{binaryPath: binaryPath}
}

// Run executes a justfile with the given arguments.
// This runs interactively, attaching stdin/stdout/stderr.
// workingDir is the directory recipes execute in - typically the project
// root, so relative paths in recipes resolve where users author them
// (not the .zdev/commands/ subdir where the justfile lives).
//
// Note: just chdirs to filepath.Dir(justfile) internally when --justfile
// is passed, so cmd.Dir alone is not enough to override the recipe cwd.
// We must pass --working-directory explicitly.
func (j *Just) Run(ctx context.Context, justfile, workingDir string, args []string, env map[string]string) error {
	if workingDir == "" {
		workingDir = filepath.Dir(justfile)
	}

	cmdArgs := []string{"--justfile", justfile, "--working-directory", workingDir}
	cmdArgs = append(cmdArgs, args...)

	cmd := exec.CommandContext(ctx, j.binaryPath, cmdArgs...)
	cmd.Dir = workingDir
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// Build environment: start with current environment, then add/override with provided env
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	return cmd.Run()
}

// Dump returns just's JSON dump of a justfile (`--dump --dump-format json`),
// the input of the zdev run --convert-just converter.
func (j *Just) Dump(ctx context.Context, justfile string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, j.binaryPath, "--justfile", justfile, "--dump", "--dump-format", "json")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("%s", msg)
		}
		return nil, err
	}
	return out, nil
}

// List shows available recipes in a justfile
func (j *Just) List(ctx context.Context, justfile string) (string, error) {
	return RunTool(ctx, j.binaryPath, "--justfile", justfile, "--list")
}

// ListRecipes returns recipe names from a justfile (without descriptions)
func (j *Just) ListRecipes(ctx context.Context, justfile string) ([]string, error) {
	// Use --list with custom formatting to get just the recipe names
	output, err := RunTool(ctx, j.binaryPath, "--justfile", justfile, "--list", "--list-heading=", "--list-prefix=")
	if err != nil {
		return nil, err
	}

	var recipes []string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Recipe names are the first word (before any # comment)
		parts := strings.Fields(line)
		if len(parts) > 0 {
			recipes = append(recipes, parts[0])
		}
	}
	return recipes, nil
}
