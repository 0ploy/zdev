package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"
)

// DockerCLI implements Runtime by shelling out to the docker command
type DockerCLI struct {
	// Binary is the path to the docker binary (default: "docker")
	Binary string
}

// NewDockerCLI creates a new Docker CLI runtime
func NewDockerCLI() *DockerCLI {
	return &DockerCLI{Binary: "docker"}
}

// CheckAvailable verifies that the Docker daemon is reachable.
// Returns nil if Docker is running, or a user-friendly error.
func (d *DockerCLI) CheckAvailable(ctx context.Context) error {
	_, err := exec.LookPath(d.Binary)
	if err != nil {
		return fmt.Errorf("docker not found in PATH - please install Docker Desktop, OrbStack, Colima, or Docker Engine")
	}

	cmd := d.command(ctx, "info", "--format", "{{.ServerVersion}}")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("Docker is not running - please start Docker Desktop, OrbStack, Colima, or the Docker daemon")
	}

	return nil
}

// DefaultDockerSocket is the conventional host path for the Docker daemon
// socket. Used as the fallback when the active context's real socket can't be
// resolved, and as the in-container target every engine expects.
const DefaultDockerSocket = "/var/run/docker.sock"

var (
	socketPathOnce sync.Once
	socketPathVal  string
)

// HostDockerSocketPath returns the host filesystem path of the Docker daemon
// socket for the active docker context. Containers that need the socket (the
// Traefik router, Dozzle) bind-mount this so they work regardless of engine:
// Docker Desktop (~/.docker/run/docker.sock), OrbStack (~/.orbstack/run/...),
// or Colima (~/.colima/default/...). The legacy /var/run/docker.sock symlink is
// not guaranteed to exist (OrbStack only creates it with admin), so we resolve
// the context's actual endpoint and fall back to /var/run/docker.sock.
// The result is cached for the process lifetime - the active context does not
// change mid-run.
func HostDockerSocketPath() string {
	socketPathOnce.Do(func() {
		socketPathVal = resolveHostDockerSocketPath()
	})
	return socketPathVal
}

func resolveHostDockerSocketPath() string {
	// An explicit DOCKER_HOST wins - it's what the docker CLI itself honors.
	if h := os.Getenv("DOCKER_HOST"); h != "" {
		if p, ok := unixSocketPath(h); ok {
			return p
		}
		// tcp:// or npipe:// can't be bind-mounted as a path; fall back.
		return DefaultDockerSocket
	}

	// Otherwise ask the active context for its daemon endpoint.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "context", "inspect",
		"--format", "{{.Endpoints.docker.Host}}").Output()
	if err == nil {
		if p, ok := unixSocketPath(strings.TrimSpace(string(out))); ok {
			return p
		}
	}

	return DefaultDockerSocket
}

// unixSocketPath extracts the filesystem path from a unix:// docker host
// string, reporting whether the host was a non-empty unix socket.
func unixSocketPath(host string) (string, bool) {
	if p := strings.TrimPrefix(host, "unix://"); p != host && p != "" {
		return p, true
	}
	return "", false
}

// command builds an *exec.Cmd for the docker binary with Docker Desktop's
// "What's next" CLI hints suppressed. Routing every shell-out through here
// keeps the user's terminal free of the trailing hint block after exec/logs.
func (d *DockerCLI) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, d.Binary, args...)
	cmd.Env = append(os.Environ(), "DOCKER_CLI_HINTS=false")
	return cmd
}

// CreateContainer creates a new container but does not start it
func (d *DockerCLI) CreateContainer(ctx context.Context, cfg ContainerConfig) (string, error) {
	args := []string{"create", "--name", cfg.Name}

	// Add labels
	for k, v := range cfg.Labels {
		args = append(args, "--label", fmt.Sprintf("%s=%s", k, v))
	}

	// Add environment variables
	for k, v := range cfg.Env {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
	}

	// Add working directory
	if cfg.WorkingDir != "" {
		args = append(args, "-w", cfg.WorkingDir)
	}

	// Add volume mounts
	for _, vol := range cfg.Volumes {
		mount := fmt.Sprintf("%s:%s", vol.Source, vol.Target)
		if vol.ReadOnly {
			mount += ":ro"
		}
		args = append(args, "-v", mount)
	}

	// Add port mappings
	for _, port := range cfg.Ports {
		args = append(args, "-p", port)
	}

	// Add restart policy
	if cfg.RestartPolicy != "" {
		args = append(args, "--restart", cfg.RestartPolicy)
	}

	// Add network
	if cfg.NetworkName != "" {
		args = append(args, "--network", cfg.NetworkName)
	}

	// Add network aliases
	for _, alias := range cfg.Aliases {
		args = append(args, "--network-alias", alias)
	}

	// Add image
	args = append(args, cfg.Image)

	// Add command if specified
	args = append(args, cfg.Command...)

	out, err := d.run(ctx, args...)
	if err != nil {
		return "", err
	}

	// Output is the container ID
	return strings.TrimSpace(out), nil
}

// RunContainer runs a one-shot container in the foreground (docker run --rm),
// streaming stdout/stderr to the caller's terminal and returning when it exits.
// A non-zero exit surfaces as an error. --rm removes the container afterward.
func (d *DockerCLI) RunContainer(ctx context.Context, cfg RunConfig) error {
	args := []string{"run", "--rm"}

	if cfg.Entrypoint != "" {
		args = append(args, "--entrypoint", cfg.Entrypoint)
	}

	for k, v := range cfg.Env {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
	}

	for _, vol := range cfg.Volumes {
		mount := fmt.Sprintf("%s:%s", vol.Source, vol.Target)
		if vol.ReadOnly {
			mount += ":ro"
		}
		args = append(args, "-v", mount)
	}

	if cfg.WorkingDir != "" {
		args = append(args, "-w", cfg.WorkingDir)
	}

	args = append(args, cfg.Image)
	args = append(args, cfg.Command...)

	cmd := d.command(ctx, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

// StartContainer starts an existing container
func (d *DockerCLI) StartContainer(ctx context.Context, nameOrID string) error {
	_, err := d.run(ctx, "start", nameOrID)
	return err
}

// StopContainer stops a running container
func (d *DockerCLI) StopContainer(ctx context.Context, nameOrID string) error {
	_, err := d.run(ctx, "stop", nameOrID)
	return err
}

// RemoveContainer removes a container (must be stopped first)
func (d *DockerCLI) RemoveContainer(ctx context.Context, nameOrID string) error {
	_, err := d.run(ctx, "rm", nameOrID)
	return err
}

// ForceRemoveContainer stops (if running) and removes a container in one call.
func (d *DockerCLI) ForceRemoveContainer(ctx context.Context, nameOrID string) error {
	_, err := d.run(ctx, "rm", "-f", nameOrID)
	return err
}

// ListContainers returns the names of containers matching the given filter
// (e.g. "label=zdev.project"). Includes stopped containers.
func (d *DockerCLI) ListContainers(ctx context.Context, filter string) ([]string, error) {
	args := []string{"ps", "-a", "--format", "{{.Names}}"}
	if filter != "" {
		args = append(args, "--filter", filter)
	}

	out, err := d.run(ctx, args...)
	if err != nil {
		return nil, err
	}

	var names []string
	for _, name := range strings.Split(strings.TrimSpace(out), "\n") {
		if name == "" {
			continue
		}
		names = append(names, name)
	}
	return names, nil
}

// LogsOptions configures log streaming behavior
type LogsOptions struct {
	Follow bool // Stream logs in real-time (-f)
	Tail   int  // Number of lines to show from end (0 = all)
}

// Logs streams container logs to stdout/stderr
func (d *DockerCLI) Logs(ctx context.Context, container string, opts LogsOptions) error {
	args := []string{"logs"}

	if opts.Follow {
		args = append(args, "-f")
	}

	if opts.Tail > 0 {
		args = append(args, "--tail", fmt.Sprintf("%d", opts.Tail))
	}

	args = append(args, container)

	cmd := d.command(ctx, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

// Exec runs a command in a running container
func (d *DockerCLI) Exec(ctx context.Context, container string, command []string, interactive bool, opts ExecOptions) error {
	args := []string{"exec"}

	// Check if stdin is a real TTY (not just a character device)
	isTTY := interactive && term.IsTerminal(int(os.Stdin.Fd()))

	if isTTY {
		args = append(args, "-it")
	}

	// Add user option if specified
	if opts.User != "" {
		args = append(args, "--user", opts.User)
	}

	// Add workdir option if specified
	if opts.Workdir != "" {
		args = append(args, "--workdir", opts.Workdir)
	}

	args = append(args, container)
	args = append(args, command...)

	cmd := d.command(ctx, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

// ContainerExists checks if a container with the given name exists
func (d *DockerCLI) ContainerExists(ctx context.Context, name string) (bool, error) {
	// Use docker inspect - returns error if container doesn't exist
	_, err := d.run(ctx, "inspect", "--type=container", name)
	if err != nil {
		if isNotFoundErr(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// isNotFoundErr reports whether a docker CLI error means the inspected
// object (container, image, network, volume) does not exist - callers
// treat that as a clean "no" rather than a failure.
func isNotFoundErr(err error) bool {
	errLower := strings.ToLower(err.Error())
	return strings.Contains(errLower, "no such") || strings.Contains(errLower, "not found")
}

// IsContainerRunning checks if a container is currently running
func (d *DockerCLI) IsContainerRunning(ctx context.Context, name string) (bool, error) {
	out, err := d.run(ctx, "inspect", "--format={{.State.Running}}", name)
	if err != nil {
		if isNotFoundErr(err) {
			return false, nil
		}
		return false, err
	}
	return strings.TrimSpace(out) == "true", nil
}

// GetContainer returns information about a container
func (d *DockerCLI) GetContainer(ctx context.Context, name string) (*Container, error) {
	out, err := d.run(ctx, "inspect", "--type=container", name)
	if err != nil {
		if isNotFoundErr(err) {
			return nil, nil
		}
		return nil, err
	}

	// Parse JSON output
	var inspectResult []struct {
		ID    string `json:"Id"`
		Name  string `json:"Name"`
		State struct {
			Status  string `json:"Status"`
			Running bool   `json:"Running"`
		} `json:"State"`
		Config struct {
			Image string `json:"Image"`
		} `json:"Config"`
	}

	if err := json.Unmarshal([]byte(out), &inspectResult); err != nil {
		return nil, fmt.Errorf("failed to parse docker inspect output: %w", err)
	}

	if len(inspectResult) == 0 {
		return nil, nil
	}

	result := inspectResult[0]
	return &Container{
		ID:      result.ID,
		Name:    strings.TrimPrefix(result.Name, "/"),
		Image:   result.Config.Image,
		Status:  result.State.Status,
		Running: result.State.Running,
	}, nil
}

// GetContainerLabels returns the labels of a container
func (d *DockerCLI) GetContainerLabels(ctx context.Context, name string) (map[string]string, error) {
	out, err := d.run(ctx, "inspect", "--type=container", "--format={{json .Config.Labels}}", name)
	if err != nil {
		return nil, err
	}

	var labels map[string]string
	if err := json.Unmarshal([]byte(out), &labels); err != nil {
		return nil, fmt.Errorf("failed to parse container labels: %w", err)
	}

	return labels, nil
}

// PullImage pulls an image from a registry
func (d *DockerCLI) PullImage(ctx context.Context, image string) error {
	_, err := d.run(ctx, "pull", image)
	return err
}

// ImageExists checks if an image exists locally
func (d *DockerCLI) ImageExists(ctx context.Context, image string) (bool, error) {
	_, err := d.run(ctx, "inspect", "--type=image", image)
	if err != nil {
		if isNotFoundErr(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// GetImageLabels returns the labels of a local image
func (d *DockerCLI) GetImageLabels(ctx context.Context, image string) (map[string]string, error) {
	out, err := d.run(ctx, "inspect", "--type=image", "--format={{json .Config.Labels}}", image)
	if err != nil {
		return nil, err
	}

	var labels map[string]string
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &labels); err != nil {
		return nil, fmt.Errorf("failed to parse image labels: %w", err)
	}
	if labels == nil {
		labels = map[string]string{}
	}
	return labels, nil
}

// BuildImage builds a local image from a Dockerfile, streaming build output
// to the terminal. Labels are passed in sorted order so the invocation is
// deterministic.
func (d *DockerCLI) BuildImage(ctx context.Context, cfg ImageBuildConfig) error {
	args := []string{"build", "-f", cfg.Dockerfile, "-t", cfg.Tag}

	for _, k := range sortedKeys(cfg.Labels) {
		args = append(args, "--label", fmt.Sprintf("%s=%s", k, cfg.Labels[k]))
	}

	args = append(args, cfg.Context)

	cmd := d.command(ctx, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// CreateNetwork creates a Docker network
func (d *DockerCLI) CreateNetwork(ctx context.Context, name string) error {
	_, err := d.run(ctx, "network", "create", name)
	return err
}

// RemoveNetwork removes a Docker network
func (d *DockerCLI) RemoveNetwork(ctx context.Context, name string) error {
	_, err := d.run(ctx, "network", "rm", name)
	return err
}

// NetworkExists checks if a network exists
func (d *DockerCLI) NetworkExists(ctx context.Context, name string) (bool, error) {
	_, err := d.run(ctx, "network", "inspect", name)
	if err != nil {
		if isNotFoundErr(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// NetworkConnect connects a container to a network with optional aliases
func (d *DockerCLI) NetworkConnect(ctx context.Context, networkName, containerName string, aliases ...string) error {
	args := []string{"network", "connect"}
	for _, alias := range aliases {
		args = append(args, "--alias", alias)
	}
	args = append(args, networkName, containerName)
	_, err := d.run(ctx, args...)
	return err
}

// NetworkDisconnect disconnects a container from a network.
// "is not connected" is not an error - the desired end state is already reached.
func (d *DockerCLI) NetworkDisconnect(ctx context.Context, networkName, containerName string) error {
	_, err := d.run(ctx, "network", "disconnect", networkName, containerName)
	if err == nil {
		return nil
	}
	if strings.Contains(strings.ToLower(err.Error()), "is not connected") {
		return nil
	}
	return err
}

// CreateVolume creates a Docker volume
func (d *DockerCLI) CreateVolume(ctx context.Context, name string) error {
	_, err := d.run(ctx, "volume", "create", name)
	return err
}

// RemoveVolume removes a Docker volume
func (d *DockerCLI) RemoveVolume(ctx context.Context, name string) error {
	_, err := d.run(ctx, "volume", "rm", name)
	return err
}

// VolumeExists checks if a volume exists
func (d *DockerCLI) VolumeExists(ctx context.Context, name string) (bool, error) {
	_, err := d.run(ctx, "volume", "inspect", name)
	if err != nil {
		if isNotFoundErr(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// CopyVolume copies all data from one volume to another using a temporary container.
// The image must be locally available and must contain sh and cp.
func (d *DockerCLI) CopyVolume(ctx context.Context, srcVolume, dstVolume, image string) error {
	_, err := d.run(ctx, "run", "--rm",
		"-v", srcVolume+":/from:ro",
		"-v", dstVolume+":/to",
		image, "sh", "-c", "cp -a /from/. /to/")
	return err
}

// ListVolumes lists volumes matching the filter (e.g., "name=.zdev")
func (d *DockerCLI) ListVolumes(ctx context.Context, filter string) ([]Volume, error) {
	args := []string{"volume", "ls", "--format", "{{.Name}}"}
	if filter != "" {
		args = append(args, "--filter", filter)
	}

	out, err := d.run(ctx, args...)
	if err != nil {
		return nil, err
	}

	var volumes []Volume
	for _, name := range strings.Split(strings.TrimSpace(out), "\n") {
		if name == "" {
			continue
		}
		volumes = append(volumes, Volume{Name: name})
	}

	return volumes, nil
}

// run executes a docker command and returns stdout
func (d *DockerCLI) run(ctx context.Context, args ...string) (string, error) {
	cmd := d.command(ctx, args...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		// Include stderr in error message for better debugging
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg != "" {
			return "", fmt.Errorf("%s: %s", err, errMsg)
		}
		return "", err
	}

	return stdout.String(), nil
}
