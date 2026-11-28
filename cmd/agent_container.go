package cmd

// A sandbox container from create to removal (runSandbox): the relay over
// `docker exec -i`, the network fence and the proxy's NO_PROXY list.

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/0ploy/zdev/internal/agent"
	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/project"
	"github.com/0ploy/zdev/internal/runtime"
	"github.com/0ploy/zdev/internal/ui"
	"golang.org/x/term"
)

// agentNoProxy is what the agent reaches without the proxy: container names
// (*.zdev), the zdev domain (project and shared-service URLs resolve to the
// router alias on the project network), the Docker host names the fence
// handles (agent.host_access), and the project's subnets.
func agentNoProxy(domain string, subnets []string) []string {
	out := []string{".zdev", "host.docker.internal", "gateway.docker.internal", "host.orb.internal", "host.lima.internal"}
	if domain != "" {
		out = append(out, domain, "."+domain)
	}
	return append(out, subnets...)
}

// agentFence builds the network fence of a project's sandbox from what runs
// now: the project's subnets, and the addresses of services holding the
// Docker socket.
func agentFence(ctx context.Context, d agent.Docker, proj *project.Project, hostAccess []int, socketServices []string) agent.Fence {
	fence := agent.Fence{HostPorts: hostAccess, ProxyOnly: true}
	if out, err := d.Output(ctx, "network", "inspect", "--format", "{{range .IPAM.Config}}{{.Subnet}}={{.Gateway}} {{end}}", proj.NetworkName()); err == nil {
		fence.AllowSubnets, fence.HostIPs = subnetsAndGateways(out)
	}
	for _, s := range socketServices {
		if ip, err := d.Output(ctx, "inspect", "--format", "{{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}", proj.ContainerName(s)); err == nil {
			fence.BlockIPs = append(fence.BlockIPs, strings.Fields(ip)...)
		}
	}
	return fence
}

// subnetsAndGateways parses `{{.Subnet}}={{.Gateway}} ...` of a network's
// IPAM config. The gateway is the host's side of the network (on Linux an
// address of the host itself, inside the subnet the fence accepts); when the
// config doesn't name it, Docker took the subnet's first address.
func subnetsAndGateways(out string) (subnets, gateways []string) {
	for _, f := range strings.Fields(out) {
		subnet, gw, _ := strings.Cut(f, "=")
		p, err := netip.ParsePrefix(subnet)
		if err != nil {
			continue
		}
		subnets = append(subnets, p.String())
		if a, err := netip.ParseAddr(gw); err == nil {
			gateways = append(gateways, a.String())
		} else if p.Addr().Is4() {
			gateways = append(gateways, p.Masked().Addr().Next().String())
		}
	}
	return subnets, gateways
}

// runSandbox prepares what every session needs (Claude and zdev volumes, the
// agent's home, the session files), creates the container, applies the fence,
// serves the relay and runs Claude in the terminal; the container is removed
// when Claude exits. Returns Claude's exit code.
func runSandbox(ctx context.Context, gcfg *config.GlobalConfig, s sandboxSpec) (int, error) {
	plain := ui.PlainMode(gcfg.Terminal.Plain)
	d := agent.NewDocker()
	agentDir := filepath.Join(config.GetZdevHome(), "agent")
	agent.SweepOrphans(ctx, d)
	agent.SweepSessionDirs(ctx, d, agentDir)

	info, err := agent.ProbeImage(ctx, d, agentDir, s.Image)
	if err != nil {
		return 0, err
	}
	if missing := info.Missing(); len(missing) > 0 {
		if s.Lacks != nil {
			return 0, errors.New(strings.TrimRight(s.Lacks(info, missing), "\n"))
		}
		return 0, fmt.Errorf("image %s lacks %s, which agent mode needs", s.Image, strings.Join(missing, " and "))
	}
	arch, err := d.ServerArch(ctx)
	if err != nil {
		return 0, err
	}
	ag := s.Agent
	agentVersion, agentVol, err := ag.install(ctx, d, agentDir, gcfg, s.Image, info, arch, plain)
	if err != nil {
		return 0, err
	}
	zbin, err := zdevLinuxBinary(arch)
	if err != nil {
		return 0, err
	}
	toolsVol, err := agent.EnsureTools(ctx, d, zbin, arch)
	if err != nil {
		return 0, err
	}
	sandboxToolsVol, err := agent.EnsureSandboxTools(ctx, d, arch)
	if err != nil {
		return 0, err
	}
	agent.MarkUsed(ctx, d, agentDir, agentVol, toolsVol, sandboxToolsVol)

	// The agent's home (at the host's home path), its state and the
	// session's generated files.
	hostHome, err := os.UserHomeDir()
	if err != nil {
		return 0, err
	}
	homeVol := agent.HomeVolumeName(s.Name)
	if err := d.CreateVolume(ctx, homeVol, map[string]string{"zdev.agent": "home", agent.LabelProject: s.Name}); err != nil {
		return 0, err
	}
	sessionID := s.SessionID
	if sessionID == "" {
		sessionID = newSessionID()
	}
	sessionDir := filepath.Join(agentDir, "sessions", sessionID)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		return 0, err
	}
	defer os.RemoveAll(sessionDir)
	setup, err := ag.prepare(ctx, d, agentPrep{
		AgentDir: agentDir, HostHome: hostHome, HomeVol: homeVol, ToolsVol: sandboxToolsVol, Workdir: s.Workdir, HostDir: s.HostDir,
		Owner: s.Owner, SessionDir: sessionDir, Prompt: s.Prompt(info), TurnHook: s.TurnHook,
		Connectors: s.Connectors, Global: gcfg,
	})
	if err != nil {
		return 0, err
	}

	// The container.
	existing, err := agent.Sandboxes(ctx, d, s.Name)
	if err != nil {
		return 0, err
	}
	container := agent.FreeSandboxName(existing, s.Name)
	hostname, _ := os.Hostname()
	cfg := s.Base
	cfg.Volumes = append([]runtime.VolumeMount(nil), cfg.Volumes...)
	// Credentials: the proxy presents certificates from a CA of this
	// session for their hosts, so the sandbox has to trust it.
	var ca *agent.SessionCA
	if len(s.Credentials) > 0 {
		if ca, err = agent.NewSessionCA(); err != nil {
			return 0, err
		}
		ok, err := trustSessionCA(&cfg, ca, sessionDir, ag.sessionMount())
		if err != nil {
			return 0, err
		}
		if !ok {
			fmt.Fprintln(os.Stderr, "Warning: credentials need zdev's CA bundle in the containers (ssl enabled, `zdev systemcheck`); the sandbox runs without them")
			ca, s.Credentials = nil, nil
		}
	}
	cfg.Name = container
	cfg.Image = s.Image
	cfg.User = s.Owner
	cfg.Entrypoint = "/opt/zdev/bin/zdev"
	cfg.Command = []string{"sandbox", "idle"}
	cfg.Init = true
	cfg.AutoRemove = true // the relay stops it when this process is gone
	cfg.CapDrop = []string{"ALL"}
	cfg.SecurityOpt = []string{"no-new-privileges"}
	cfg.CPUs, cfg.Memory, cfg.PidsLimit = s.Limits.CPUs, s.Limits.Memory, s.Limits.Pids
	cfg.Labels = map[string]string{
		agent.LabelSandbox: "true", agent.LabelProject: s.Name, agent.LabelSession: sessionID,
		agent.LabelService: s.Service, agent.LabelPID: strconv.Itoa(os.Getpid()), agent.LabelHost: hostname,
	}
	env := map[string]string{
		"HOME":         hostHome,
		"PATH":         info.Path + ":/opt/zdev/bin:" + filepath.Dir(ag.argv(nil, "")[0]) + ":" + agent.SandboxToolsMount,
		"SHELL":        "/bin/bash",
		"ZDEV_SANDBOX": container,
	}
	for k, v := range setup.Env {
		env[k] = v
	}
	gitConfig := [][2]string{{"safe.directory", "*"}}
	if gitName, gitEmail := hostGitIdentity(); gitName != "" || gitEmail != "" {
		gitConfig = append(gitConfig, [2]string{"user.name", gitName}, [2]string{"user.email", gitEmail})
	}
	gitConfig = append(gitConfig, s.GitConfig...)
	env["GIT_CONFIG_COUNT"] = strconv.Itoa(len(gitConfig))
	for i, kv := range gitConfig {
		env[fmt.Sprintf("GIT_CONFIG_KEY_%d", i)], env[fmt.Sprintf("GIT_CONFIG_VALUE_%d", i)] = kv[0], kv[1]
	}
	if tz := hostTimeZone(); tz != "" {
		env["TZ"] = tz
	}
	// The internet only through the egress proxy in the relay (the fence
	// rejects the rest); project services and domains directly.
	noProxy := strings.Join(append([]string{"localhost", "127.0.0.1", "::1"}, s.NoProxy...), ",")
	for _, k := range []string{"HTTPS_PROXY", "HTTP_PROXY", "https_proxy", "http_proxy"} {
		env[k] = agent.ProxyURL
	}
	env["NO_PROXY"], env["no_proxy"] = noProxy, noProxy
	env["NODE_USE_ENV_PROXY"] = "1" // Node's own fetch ignores the variables above otherwise
	for k, v := range s.Env {
		env[k] = v
	}
	if cfg.Env == nil {
		cfg.Env = map[string]string{}
	}
	for k, v := range env {
		cfg.Env[k] = v
	}
	cfg.Mounts = append(append([]string(nil), cfg.Mounts...),
		"type=volume,src="+toolsVol+",dst=/opt/zdev/bin,readonly",
		"type=volume,src="+agentVol+",dst="+ag.mount()+",readonly",
		"type=volume,src="+sandboxToolsVol+",dst="+agent.SandboxToolsMount+",readonly",
		"type=volume,src="+homeVol+",dst="+hostHome,
	)
	cfg.Mounts = append(cfg.Mounts, setup.Mounts...)
	cfg.Mounts = append(cfg.Mounts, s.GitMounts...)

	what := "image of service " + s.Service
	if s.Service == "" {
		what = "bootstrap image"
	}
	ui.StatusStep(fmt.Sprintf("Starting sandbox %s (%s, %s %s)", container, what, ag.title(), agentVersion), plain)
	rt := runtime.NewDockerCLI()
	if _, err := rt.CreateContainer(ctx, cfg); err != nil {
		return 0, fmt.Errorf("creating the sandbox: %w", err)
	}
	defer func() { _, _ = d.Output(context.Background(), "rm", "-f", container) }()
	if err := rt.StartContainer(ctx, container); err != nil {
		return 0, fmt.Errorf("starting the sandbox: %w", err)
	}
	if err := s.Fence.Apply(ctx, d, container); err != nil {
		return 0, err
	}

	// The relay: SSH over the stdio of a docker exec; this process is the server.
	relayCtx, stopRelay := context.WithCancel(ctx)
	defer stopRelay()
	egress := s.Egress
	egress.Credentials, egress.CA = s.Credentials, ca
	if s.EgressLog != nil {
		egress.Log = s.EgressLog.record
	}
	if err := startRelay(relayCtx, container, s.Handler(container), &egress); err != nil {
		return 0, err
	}

	// The agent, in the terminal. Docker handles the TTY. Stdin only when it
	// is a terminal or carries input: with -p and an attached but empty
	// stdin, Claude waits for piped input first.
	execArgs := []string{"exec"}
	if stdinCarriesInput() {
		execArgs = append(execArgs, "-i")
	}
	if term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd())) {
		execArgs = append(execArgs, "-t")
	}
	for _, k := range []string{"TERM", "COLORTERM", "TERM_PROGRAM"} {
		if os.Getenv(k) != "" {
			execArgs = append(execArgs, "-e", k)
		}
	}
	execArgs = append(execArgs, "-w", cfg.WorkingDir, container)
	execArgs = append(execArgs, ag.argv(s.Rest, sessionID)...)

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigs)
	c := exec.CommandContext(ctx, "docker", execArgs...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := c.Start(); err != nil {
		return 0, err
	}
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	select {
	case err = <-done:
	case <-sigs:
		_ = c.Process.Signal(syscall.SIGTERM)
		err = <-done
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	return 0, err
}

// startRelay runs `zdev sandbox relay` in the container and serves it until
// ctx ends; it returns once the relay's socket is up.
func startRelay(ctx context.Context, container string, handler agent.Handler, egress *agent.Egress) error {
	relay := exec.CommandContext(ctx, "docker", "exec", "-i", container, "/opt/zdev/bin/zdev", "sandbox", "relay")
	stdin, err := relay.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := relay.StdoutPipe()
	if err != nil {
		return err
	}
	relay.Stderr = os.Stderr
	if err := relay.Start(); err != nil {
		return fmt.Errorf("starting the relay: %w", err)
	}
	go func() { _ = relay.Wait() }()
	go func() { _ = agent.Serve(ctx, agent.PipeConn(stdout, stdin), handler, egress) }()
	for i := 0; i < 50; i++ {
		if err := exec.CommandContext(ctx, "docker", "exec", container, "test", "-S", agent.RelaySocket).Run(); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return errors.New("the relay in the sandbox didn't come up")
}

// stdinCarriesInput reports whether stdin is a terminal, a pipe or a file,
// not /dev/null or another device.
func stdinCarriesInput() bool {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		return true
	}
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeNamedPipe != 0 || fi.Mode().IsRegular()
}
