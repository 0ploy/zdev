package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	socketMountOnce   sync.Once
	socketMountPath   string
	socketMountable   bool
	socketMountReason string
)

// dockerDesktopSocketHelp explains why the Docker socket must be mountable and
// how to make it so on Docker Desktop. Surfaced instead of the daemon's cryptic
// "mkdir ... operation not supported" when the per-user socket can't be mounted.
const dockerDesktopSocketHelp = `the Docker socket at ` + DefaultDockerSocket + ` is not available to mount into a container.

zdev's router (Traefik) and log viewer (Dozzle) run as containers that watch the
Docker API, so they bind-mount the Docker socket. On Docker Desktop only ` + DefaultDockerSocket + `
can be mounted into a container - the per-user socket (~/.docker/run/docker.sock)
cannot, because it is a proxy socket the VM's file sharing can't pass through.

Fix: Docker Desktop > Settings > Advanced > enable
"Allow the default Docker socket to be used (requires password)", then Apply &
Restart and run zdev again.`

// HostDockerSocketPath returns the host filesystem path to bind-mount as the
// Docker daemon socket into containers that need the Docker API (the Traefik
// router, Dozzle). This works across engines: Docker Desktop, OrbStack
// (~/.orbstack/run/...), or Colima (~/.colima/default/...).
//
// Note this is the MOUNT source, which is not always the same as the CLI's
// daemon endpoint. On Docker Desktop the active context points at a per-user
// proxy socket (~/.docker/run/docker.sock) that can't be bind-mounted; only the
// special-cased /var/run/docker.sock works, so we return that when present.
// Use CheckDockerSocketMountable to fail early with guidance when it isn't.
// The result is cached for the process lifetime - the active context does not
// change mid-run.
func HostDockerSocketPath() string {
	path, _, _ := dockerSocketMount()
	return path
}

// CheckDockerSocketMountable reports whether the Docker socket can actually be
// bind-mounted into a container on this engine, returning an actionable error
// instead of letting the daemon surface a cryptic mkdir failure. The one case
// it catches: Docker Desktop using its per-user socket with the default
// /var/run/docker.sock disabled. Call it before starting a container that
// mounts the socket (the router, the log viewer).
func CheckDockerSocketMountable() error {
	if _, mountable, reason := dockerSocketMount(); !mountable {
		return errors.New(reason)
	}
	return nil
}

// dockerSocketMount resolves, once per process, the Docker socket bind-mount
// source and whether it is mountable.
func dockerSocketMount() (path string, mountable bool, reason string) {
	socketMountOnce.Do(func() {
		socketMountPath, socketMountable, socketMountReason = computeDockerSocketMount()
	})
	return socketMountPath, socketMountable, socketMountReason
}

func computeDockerSocketMount() (string, bool, string) {
	endpoint := resolveHostDockerSocketPath()

	// On Docker Desktop the active context points at a per-user proxy socket
	// that the VM's file sharing cannot bind-mount into a container. Only the
	// special-cased /var/run/docker.sock works, and only when the user has
	// enabled it.
	if isDockerDesktopProxySocket(endpoint) {
		if isSocketFile(DefaultDockerSocket) {
			return DefaultDockerSocket, true, ""
		}
		return endpoint, false, dockerDesktopSocketHelp
	}

	// OrbStack, Colima, an explicit DOCKER_HOST path, or a real
	// /var/run/docker.sock - all bind-mountable as resolved.
	return endpoint, true, ""
}

// isDockerDesktopProxySocket matches Docker Desktop's per-user socket
// (~/.docker/run/docker.sock on macOS, ~/.docker/desktop/docker.sock on Linux)
// while excluding OrbStack (~/.orbstack/...), Colima (~/.colima/...), and the
// system /var/run/docker.sock.
func isDockerDesktopProxySocket(p string) bool {
	return strings.Contains(p, "/.docker/") && strings.HasSuffix(p, "docker.sock")
}

// isSocketFile reports whether p exists and is a unix socket, following
// symlinks so a /var/run/docker.sock symlink to the real socket still counts.
func isSocketFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode()&os.ModeSocket != 0
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
		args = append(args, "-v", vol.Arg())
	}

	for _, m := range cfg.Mounts {
		args = append(args, "--mount", m)
	}

	// Add port mappings
	for _, port := range cfg.Ports {
		args = append(args, "-p", port)
	}

	// Agent sandbox options (see ContainerConfig)
	if cfg.User != "" {
		args = append(args, "--user", cfg.User)
	}
	if cfg.Entrypoint != "" {
		args = append(args, "--entrypoint", cfg.Entrypoint)
	}
	if cfg.Init {
		args = append(args, "--init")
	}
	if cfg.AutoRemove {
		args = append(args, "--rm")
	}
	for _, c := range cfg.CapDrop {
		args = append(args, "--cap-drop", c)
	}
	for _, c := range cfg.CapAdd {
		args = append(args, "--cap-add", c)
	}
	for _, o := range cfg.SecurityOpt {
		args = append(args, "--security-opt", o)
	}
	if cfg.PidsLimit > 0 {
		args = append(args, "--pids-limit", fmt.Sprintf("%d", cfg.PidsLimit))
	}
	if cfg.Memory != "" {
		args = append(args, "--memory", cfg.Memory)
	}
	if cfg.CPUs != "" {
		args = append(args, "--cpus", cfg.CPUs)
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

	// Attach an interactive TTY only when the caller opts in AND stdin is a real
	// terminal - otherwise `docker run -it` fails with "the input device is not a
	// TTY" in CI/piped runs. Mirrors Exec's TTY detection.
	if cfg.Interactive && term.IsTerminal(int(os.Stdin.Fd())) {
		args = append(args, "-it")
	}

	if cfg.Entrypoint != "" {
		args = append(args, "--entrypoint", cfg.Entrypoint)
	}

	for k, v := range cfg.Env {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
	}

	for _, vol := range cfg.Volumes {
		args = append(args, "-v", vol.Arg())
	}

	if cfg.WorkingDir != "" {
		args = append(args, "-w", cfg.WorkingDir)
	}

	args = append(args, cfg.Image)
	args = append(args, cfg.Command...)

	cmd := d.command(ctx, args...)
	if cfg.Quiet {
		return cmd.Run() // no stdio: output discarded
	}
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

// ExecOutput runs a command in a running container and returns its stdout.
func (d *DockerCLI) ExecOutput(ctx context.Context, container string, command []string) (string, error) {
	args := append([]string{"exec", container}, command...)
	return d.run(ctx, args...)
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

	// Interactive callers always get stdin (-i) so piped input reaches the
	// command (`cat dump.sql | zdev exec db mysql`); a TTY (-t) only when stdin
	// is a real terminal, otherwise docker fails with "the input device is not
	// a TTY".
	if interactive {
		args = append(args, "-i")
		if term.IsTerminal(int(os.Stdin.Fd())) {
			args = append(args, "-t")
		}
	}

	// Add user option if specified
	if opts.User != "" {
		args = append(args, "--user", opts.User)
	}

	// Add workdir option if specified
	if opts.Workdir != "" {
		args = append(args, "--workdir", opts.Workdir)
	}

	// A relayed exec from an agent sandbox: pass the marker on (the value
	// comes from the CLI's environment), so a cancel can find the processes.
	if os.Getenv(ExecMarkEnv) != "" {
		args = append(args, "-e", ExecMarkEnv)
	}

	args = append(args, container)
	args = append(args, command...)

	cmd := d.command(ctx, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

// ExecStream runs a command in a running container with the caller's streams.
func (d *DockerCLI) ExecStream(ctx context.Context, container string, command []string, opts ExecStreamOptions) error {
	args := []string{"exec"}
	if opts.Interactive {
		args = append(args, "-i")
	}
	if opts.TTY {
		args = append(args, "-t")
	}
	if opts.User != "" {
		args = append(args, "--user", opts.User)
	}
	if opts.Workdir != "" {
		args = append(args, "--workdir", opts.Workdir)
	}
	keys := make([]string, 0, len(opts.Env))
	for k := range opts.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	cmd := d.command(ctx)
	for _, k := range keys {
		args = append(args, "-e", k)
		cmd.Env = append(cmd.Env, k+"="+opts.Env[k])
	}
	args = append(args, container)
	args = append(args, command...)
	cmd.Args = append([]string{d.Binary}, args...)
	cmd.Stdin = opts.Stdin
	cmd.Stdout = opts.Stdout
	cmd.Stderr = opts.Stderr
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

// ContainerBinds returns the container's bind mounts.
func (d *DockerCLI) ContainerBinds(ctx context.Context, name string) ([]Bind, error) {
	out, err := d.run(ctx, "inspect", "--type=container", "--format={{json .Mounts}}", name)
	if err != nil {
		return nil, err
	}
	var mounts []struct {
		Type        string
		Source      string
		Destination string
		RW          bool
	}
	if err := json.Unmarshal([]byte(out), &mounts); err != nil {
		return nil, fmt.Errorf("failed to parse container mounts: %w", err)
	}
	var binds []Bind
	for _, m := range mounts {
		if m.Type == "bind" {
			binds = append(binds, Bind{Source: hostBindSource(m.Source), Target: m.Destination, ReadOnly: !m.RW})
		}
	}
	return binds, nil
}

// hostBindSource maps a bind source as the engine reports it back to the
// host path. Docker Desktop reports some binds under its VM's view of the
// host's file sharing (/host_mnt/Users/...), others as given (seen on the
// same daemon for two containers of one project).
func hostBindSource(src string) string {
	if rest, ok := strings.CutPrefix(src, "/host_mnt/"); ok {
		if _, err := os.Stat("/" + rest); err == nil {
			return "/" + rest
		}
	}
	return src
}

// GetContainerNetworks returns the networks a container is attached to,
// mapped to the user-defined aliases on each endpoint.
//
// Reads the endpoint's Aliases field, not DNSNames: DNSNames also carries
// the container name and short ID, which Docker assigns itself and would be
// rejected/duplicated on a reconnect.
func (d *DockerCLI) GetContainerNetworks(ctx context.Context, name string) (map[string][]string, error) {
	out, err := d.run(ctx, "inspect", "--type=container", "--format={{json .NetworkSettings.Networks}}", name)
	if err != nil {
		if isNotFoundErr(err) {
			return nil, nil
		}
		return nil, err
	}

	var networks map[string]struct {
		Aliases []string `json:"Aliases"`
	}
	if err := json.Unmarshal([]byte(out), &networks); err != nil {
		return nil, fmt.Errorf("failed to parse container networks: %w", err)
	}

	result := make(map[string][]string, len(networks))
	for network, endpoint := range networks {
		result[network] = endpoint.Aliases
	}
	return result, nil
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

// ImageEntrypoint returns a local image's ENTRYPOINT, CMD and ID. They are
// read from a container created (never started) from the image and removed
// again: with the containerd image store, inspecting a multi-platform image
// by tag can return an empty Config (seen with nginx:alpine on Docker 29)
// although containers get the right values. What a container gets is what
// matters here anyway.
func (d *DockerCLI) ImageEntrypoint(ctx context.Context, image string) ([]string, []string, string, error) {
	id, err := d.run(ctx, "image", "inspect", "--format={{.Id}}", image)
	if err != nil {
		return nil, nil, "", err
	}
	cid, err := d.run(ctx, "create", "--label", "zdev.probe=true", image)
	if err != nil {
		return nil, nil, "", err
	}
	cid = strings.TrimSpace(cid)
	defer func() { _, _ = d.run(context.WithoutCancel(ctx), "rm", "-f", cid) }()
	out, err := d.run(ctx, "inspect", "--type=container",
		`--format={{json .Config.Entrypoint}}{{"\n"}}{{json .Config.Cmd}}`, cid)
	if err != nil {
		return nil, nil, "", err
	}
	return parseImageEntrypoint(strings.TrimSpace(out) + "\n" + strings.TrimSpace(id))
}

func parseImageEntrypoint(out string) ([]string, []string, string, error) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		return nil, nil, "", fmt.Errorf("unexpected image inspect output %q", out)
	}
	var entrypoint, cmd []string
	if err := json.Unmarshal([]byte(lines[0]), &entrypoint); err != nil {
		return nil, nil, "", fmt.Errorf("image ENTRYPOINT: %w", err)
	}
	if err := json.Unmarshal([]byte(lines[1]), &cmd); err != nil {
		return nil, nil, "", fmt.Errorf("image CMD: %w", err)
	}
	return entrypoint, cmd, strings.TrimSpace(lines[2]), nil
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

// PruneDanglingImages removes untagged images with the label that no
// container (running or not) uses.
func (d *DockerCLI) PruneDanglingImages(ctx context.Context, label string) error {
	_, err := d.run(ctx, "image", "prune", "--force", "--filter", "label="+label)
	return err
}

// RemoveImage removes an image reference (never forced: an image a
// container uses stays).
func (d *DockerCLI) RemoveImage(ctx context.Context, image string) error {
	_, err := d.run(ctx, "image", "rm", image)
	return err
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

// ListNetworks returns the names of networks matching a docker filter
// (e.g. "name=.zdev"). An empty filter lists every network.
func (d *DockerCLI) ListNetworks(ctx context.Context, filter string) ([]string, error) {
	args := []string{"network", "ls", "--format", "{{.Name}}"}
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
// --entrypoint sh bypasses the image's own init (zpinit, PHP entrypoints),
// which might start services or ignore the arguments instead of copying.
func (d *DockerCLI) CopyVolume(ctx context.Context, srcVolume, dstVolume, image string) error {
	_, err := d.run(ctx, "run", "--rm", "--entrypoint", "sh",
		"-v", srcVolume+":/from:ro",
		"-v", dstVolume+":/to",
		image, "-c", "cp -a /from/. /to/")
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
