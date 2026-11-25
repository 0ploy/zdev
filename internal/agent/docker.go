// Package agent runs coding agents (Claude Code) in sandbox containers of a
// zdev project: the Claude binary and zdev's own Linux build injected from
// read-only volumes, the image probe, the generated system prompt and managed
// settings, the host fence. The design is PLAN_2_AGENT_SANDBOX.md.
package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// Docker runs the docker CLI. zdev shells out to the CLI everywhere (keeps
// Podman possible); these are the calls the agent needs beyond
// runtime.Runtime: one-shot containers whose output is read, inspection.
type Docker struct {
	Binary string
}

// NewDocker returns a Docker using the docker binary on PATH.
func NewDocker() Docker { return Docker{Binary: "docker"} }

func (d Docker) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, d.Binary, args...)
	cmd.Env = append(os.Environ(), "DOCKER_CLI_HINTS=false")
	return cmd
}

// Output runs docker and returns its trimmed stdout; stderr goes into the error.
func (d Docker) Output(ctx context.Context, args ...string) (string, error) {
	cmd := d.command(ctx, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("docker %s: %s", args[0], msg)
		}
		return "", fmt.Errorf("docker %s: %w", args[0], err)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// Stream runs docker with the given streams (nil means none).
func (d Docker) Stream(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args ...string) error {
	cmd := d.command(ctx, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	return cmd.Run()
}

// VolumeExists reports whether a named volume exists.
func (d Docker) VolumeExists(ctx context.Context, name string) bool {
	_, err := d.Output(ctx, "volume", "inspect", "--format", "{{.Name}}", name)
	return err == nil
}

// CreateVolume creates a named volume with labels (no-op if it exists).
func (d Docker) CreateVolume(ctx context.Context, name string, labels map[string]string) error {
	args := []string{"volume", "create"}
	for k, v := range labels {
		args = append(args, "--label", k+"="+v)
	}
	_, err := d.Output(ctx, append(args, name)...)
	return err
}

// ServerArch is the Docker daemon's architecture: "arm64" or "amd64".
func (d Docker) ServerArch(ctx context.Context) (string, error) {
	arch, err := d.Output(ctx, "version", "--format", "{{.Server.Arch}}")
	if err != nil {
		return "", err
	}
	switch arch {
	case "arm64", "aarch64":
		return "arm64", nil
	case "amd64", "x86_64":
		return "amd64", nil
	}
	return "", fmt.Errorf("agent mode supports arm64 and amd64 Docker hosts, not %s", arch)
}

// ImageID returns the ID of a local image.
func (d Docker) ImageID(ctx context.Context, image string) (string, error) {
	return d.Output(ctx, "image", "inspect", "--format", "{{.Id}}", image)
}
