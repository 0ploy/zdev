package cmd

// zdev claude: Claude Code in a sandbox container of the project
// (PLAN_2_AGENT_SANDBOX.md). The container is created from a service's image
// with the project's source, network and environment; Claude, zdev's Linux
// build and the login are injected; the container is removed when the session
// ends. This process is the session's host side: no daemon.

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/0ploy/zdev/internal/agent"
	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/project"
	"github.com/0ploy/zdev/internal/runtime"
	"github.com/0ploy/zdev/internal/ui"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var claudeCmd = &cobra.Command{
	Use:   "claude [service] [claude arguments...]",
	Short: "Run Claude Code in a sandbox container of this project",
	Long: `Run Claude Code in a sandbox container of this project.

The sandbox is created from a service's image (default: app), with the same
source, environment and network as the service, and removed when the session
ends. Claude gets no Docker socket and can't reach this machine; it manages the
project through zdev, which checks every request here on the host.

The first argument selects the service only when it names one; everything else
goes to Claude unchanged:

  zdev claude                    Claude in the app service's image
  zdev claude node --continue    in the node service's image, continue the last conversation
  zdev claude "fix the login"    start with a prompt
  zdev claude -- app             pass "app" to Claude (a prompt that equals a service name)

Log in once with /login inside Claude; every sandbox shares that login.`,
	DisableFlagParsing: true,
	RunE:               runClaude,
}

func init() {
	rootCmd.AddCommand(claudeCmd)
}

// splitClaudeArgs separates the optional service from Claude's arguments.
func splitClaudeArgs(args []string, isService func(string) bool) (service string, rest []string) {
	switch {
	case len(args) > 0 && args[0] == "--":
		return "", args[1:]
	case len(args) > 0 && isService(args[0]):
		rest = args[1:]
		if len(rest) > 0 && rest[0] == "--" {
			rest = rest[1:]
		}
		return args[0], rest
	}
	return "", args
}

// claudeSessionArgs adds --session-id unless the arguments already pick a
// conversation (--continue, --resume, --session-id).
func claudeSessionArgs(rest []string, sessionID string) []string {
	for _, a := range rest {
		switch {
		case a == "-c", a == "--continue", a == "-r", a == "--resume",
			strings.HasPrefix(a, "--resume="), a == "--session-id", strings.HasPrefix(a, "--session-id="):
			return rest
		}
	}
	return append([]string{"--session-id", sessionID}, rest...)
}

func newSessionID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

var releaseVersion = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

// zdevLinuxBinary is the zdev build the agent container runs: a local Linux
// build named by ZDEV_AGENT_LINUX_BINARY, else this release's asset.
func zdevLinuxBinary(arch string) (agent.ZdevBinary, error) {
	if p := os.Getenv("ZDEV_AGENT_LINUX_BINARY"); p != "" {
		abs, err := filepath.Abs(p)
		if err != nil {
			return agent.ZdevBinary{}, err
		}
		return agent.ZdevBinary{Path: abs}, nil
	}
	if releaseVersion.MatchString(Version) {
		return agent.ZdevBinary{Version: Version}, nil
	}
	// A dev build installed with its Linux build next to it.
	if self, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(self); err == nil {
			candidate := filepath.Join(filepath.Dir(real), "zdev-linux-"+arch)
			if fi, err := os.Stat(candidate); err == nil && fi.Mode().IsRegular() {
				return agent.ZdevBinary{Path: candidate}, nil
			}
		}
	}
	return agent.ZdevBinary{}, fmt.Errorf("this zdev (%s) is not a release, so there is no Linux build to download for the agent container; build one and point to it:\n\n  make build-linux-%s && export ZDEV_AGENT_LINUX_BINARY=$PWD/zdev-linux-%s", Version, arch, arch)
}

// minAgentAPI is the Docker API version that added --mount volume-subpath
// (Docker Engine 26.0), used for the read-only .git/hooks and .git/config.
const minAgentAPI = "1.45"

func checkAgentDockerAPI(ctx context.Context, d agent.Docker) error {
	api, err := d.Output(ctx, "version", "--format", "{{.Server.APIVersion}}")
	if err != nil {
		return err
	}
	if !apiAtLeast(api, minAgentAPI) {
		engine, _ := d.Output(ctx, "version", "--format", "{{.Server.Version}}")
		return fmt.Errorf("agent mode needs Docker Engine 26 or newer (API %s); this engine is %s (API %s): update Docker Desktop, OrbStack or Colima", minAgentAPI, engine, api)
	}
	return nil
}

func apiAtLeast(have, want string) bool {
	hp, wp := strings.Split(have, "."), strings.Split(want, ".")
	for i := 0; i < len(wp); i++ {
		var h, w int
		if i < len(hp) {
			h, _ = strconv.Atoi(hp[i])
		}
		w, _ = strconv.Atoi(wp[i])
		if h != w {
			return h > w
		}
	}
	return true
}

// hostGitIdentity is the developer's git name and email, for commits made in
// the sandbox.
func hostGitIdentity() (name, email string) {
	get := func(key string) string {
		out, err := exec.Command("git", "config", "--global", key).Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	return get("user.name"), get("user.email")
}

// hostTimeZone is the host's IANA zone name, so the agent's clock reads like
// the developer's.
func hostTimeZone() string {
	if tz := os.Getenv("TZ"); tz != "" {
		return tz
	}
	if target, err := os.Readlink("/etc/localtime"); err == nil {
		if _, zone, ok := strings.Cut(target, "zoneinfo/"); ok {
			return zone
		}
	}
	return ""
}

func runClaude(cmd *cobra.Command, args []string) error {
	var exitCode int
	run := func(ctx context.Context, proj *project.Project) error {
		code, err := claudeSession(ctx, proj, args)
		exitCode = code
		return err
	}
	var err error
	if _, ferr := config.FindProjectDir(); ferr != nil {
		// No .zdev/ here: bootstrap mode, to set the project up.
		err = withDocker(noTimeout, func(ctx context.Context) error {
			code, err := bootstrapSession(ctx, args)
			exitCode = code
			return err
		})
	} else {
		err = withProject(noTimeout, run)
	}
	if err == nil && exitCode != 0 {
		os.Exit(exitCode)
	}
	return err
}

// sandboxSpec is what differs between a project session and a bootstrap
// session; runSandbox does the rest.
type sandboxSpec struct {
	Name       string // project name (labels, home volume)
	Service    string // service whose image it runs, "" for bootstrap
	Image      string
	Base       runtime.ContainerConfig // starting point: env, mounts, network, working dir
	Workdir    string                  // the source's path in the container
	HostDir    string                  // the source's path on this machine (memory key)
	Owner      string                  // uid:gid the agent runs as
	GitMounts  []string                // read-only .git/hooks and .git/config
	Env        map[string]string       // added to the container's environment
	Fence      agent.Fence
	Handler    agent.Handler
	Prompt     func(info *agent.ImageInfo) string
	Connectors bool
	Limits     config.AgentLimits
	Rest       []string // Claude's arguments
}

// gitReadOnlyMounts makes .git/hooks and .git/config read-only for the agent
// (PLAN_2 section 6): from the sync volume with volume-subpath, or nested
// binds of the host paths. .git/hooks is created first: otherwise the agent
// could create it, and the sync would carry its hooks to this machine.
func gitReadOnlyMounts(hostDir, workdir, syncVolume string) ([]string, error) {
	gitDir := filepath.Join(hostDir, ".git")
	if fi, err := os.Stat(gitDir); err != nil || !fi.IsDir() {
		return nil, nil
	}
	if err := os.MkdirAll(filepath.Join(gitDir, "hooks"), 0o755); err != nil {
		return nil, err
	}
	var mounts []string
	for _, p := range []string{".git/hooks", ".git/config"} {
		dst := workdir + "/" + p
		if syncVolume != "" {
			mounts = append(mounts, "type=volume,src="+syncVolume+",dst="+dst+",volume-subpath="+p+",readonly")
		} else {
			mounts = append(mounts, "type=bind,src="+filepath.Join(hostDir, p)+",dst="+dst+",readonly")
		}
	}
	return mounts, nil
}

func claudeSession(ctx context.Context, proj *project.Project, args []string) (int, error) {
	gcfg, err := config.LoadGlobalConfig()
	if err != nil {
		return 0, err
	}
	plain := ui.PlainMode(gcfg.Terminal.Plain)
	name := proj.Config.Name
	acfg := gcfg.AgentProject(name)
	d := agent.NewDocker()

	service, rest := splitClaudeArgs(args, func(s string) bool { _, ok := proj.Config.Services[s]; return ok })
	if service == "" {
		if service, err = proj.AgentDefaultService(acfg.Service); err != nil {
			return 0, err
		}
	}

	// Refusals before anything is created.
	if err := checkAgentPreconditions(ctx, d, gcfg); err != nil {
		return 0, err
	}
	socketServices := proj.DockerSocketServices()
	for _, s := range socketServices {
		if s == service {
			return 0, fmt.Errorf("service %s mounts the Docker socket; an agent in its image would be root on this machine: choose another service", service)
		}
	}
	if len(socketServices) > 0 {
		if acfg.DockerSocket != "accept" {
			subject := "service " + socketServices[0] + " mounts"
			if len(socketServices) > 1 {
				subject = "services " + strings.Join(socketServices, ", ") + " mount"
			}
			return 0, fmt.Errorf("%s the Docker socket: whoever reaches it is root on this machine, so the sandbox can't protect it.\nIf that is acceptable for this project, set in ~/.zdev/global-config.yaml:\n\n  agent:\n    projects:\n      %s:\n        docker_socket: accept", subject, name)
		}
		fmt.Println(ui.Color(fmt.Sprintf("Warning: %s hold the Docker socket; the sandbox is no boundary for this project (accepted in host config).", strings.Join(socketServices, ", ")), "yellow", plain))
	}
	src, err := proj.AgentSource(service)
	if err != nil {
		return 0, err
	}
	gitMounts, err := gitReadOnlyMounts(src.HostPath, src.ContainerPath, src.SyncVolume)
	if err != nil {
		return 0, err
	}

	// The project runs, and the agent's mount syncs .zdev and .git.
	if _, err := project.SetAgentSyncService(name, service); err != nil {
		return 0, err
	}
	ui.StatusStep("Starting "+name, plain)
	if err := proj.Start(ctx); err != nil {
		return 0, err
	}
	printGitConflicts(ctx, proj, plain)

	image := proj.ServiceImage(service)
	imageUser, _ := d.Output(ctx, "image", "inspect", "--format", "{{.Config.User}}", image)
	owner, err := proj.AgentUser(ctx, service, imageUser)
	if err != nil {
		return 0, err
	}
	if strings.HasPrefix(owner, "0:") && proj.IsMutagenEnabled() {
		fmt.Println(ui.Color(fmt.Sprintf("Note: the agent runs as root (with no capabilities), because the files in the sync volume belong to root. Setting services.%s.mutagen.user to the app's user is the cleaner setup.", service), "yellow", plain))
	}
	base, err := proj.AgentBaseConfig(ctx, service)
	if err != nil {
		return 0, err
	}
	if base.WorkingDir == "" {
		base.WorkingDir = src.ContainerPath
	}

	fence := agent.Fence{HostPorts: acfg.HostAccess}
	if subnets, err := d.Output(ctx, "network", "inspect", "--format", "{{range .IPAM.Config}}{{.Subnet}} {{end}}", proj.NetworkName()); err == nil {
		fence.AllowSubnets = strings.Fields(subnets)
	}
	for _, s := range socketServices {
		if ip, err := d.Output(ctx, "inspect", "--format", "{{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}", proj.ContainerName(s)); err == nil {
			fence.BlockIPs = append(fence.BlockIPs, strings.Fields(ip)...)
		}
	}
	var promptServices []agent.PromptService
	for sname, svc := range proj.Config.Services {
		ps := agent.PromptService{Name: sname}
		if svc.Routing != nil {
			ps.Port = svc.Routing.Port
		}
		promptServices = append(promptServices, ps)
	}
	url := proj.URL(schemeFor(gcfg))
	return runSandbox(ctx, gcfg, sandboxSpec{
		Name: name, Service: service, Image: image, Base: base,
		Workdir: src.ContainerPath, HostDir: proj.Dir, Owner: owner, GitMounts: gitMounts,
		Env: map[string]string{
			"ZDEV_PROJECT":         name,
			"ZDEV_RELAY":           agent.RelaySocket,
			"ZDEV_HOST_PROJECTDIR": filepath.Base(proj.Dir),
			"ZDEV_DOMAIN":          gcfg.Domain,
		},
		Fence:   fence,
		Handler: relayHandler(proj, socketServices),
		Prompt: func(info *agent.ImageInfo) string {
			return agent.SystemPrompt(agent.PromptData{
				Project: name, Service: service, Image: image, Workdir: src.ContainerPath,
				URL: url, Services: promptServices, Info: info,
				Root: strings.HasPrefix(owner, "0:"), Fenced: true, HostAccess: acfg.HostAccess, Refused: socketServices,
			})
		},
		Connectors: acfg.ConnectorsEnabled(), Limits: acfg.Limits, Rest: rest,
	})
}

// bootstrapSession is `zdev claude` in a repository without .zdev/: the
// agent writes the setup in zdev's bootstrap image, without project network.
func bootstrapSession(ctx context.Context, args []string) (int, error) {
	gcfg, err := config.LoadGlobalConfig()
	if err != nil {
		return 0, err
	}
	plain := ui.PlainMode(gcfg.Terminal.Plain)
	d := agent.NewDocker()
	if err := checkAgentPreconditions(ctx, d, gcfg); err != nil {
		return 0, err
	}
	dir, err := os.Getwd()
	if err != nil {
		return 0, err
	}
	if out, err := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel").Output(); err == nil {
		dir = strings.TrimSpace(string(out))
	}
	if home, _ := os.UserHomeDir(); dir == home || dir == "/" {
		return 0, errors.New("no .zdev/ here, and this directory is too broad for a bootstrap sandbox: run zdev claude in the repository to set up")
	}
	name := "bootstrap-" + strings.ToLower(nonName.ReplaceAllString(filepath.Base(dir), "-"))
	acfg := gcfg.AgentProject(name)
	ui.StatusStep("No .zdev/ here: bootstrap session to set up "+filepath.Base(dir), plain)
	if err := agent.EnsureBootstrapImage(ctx, d); err != nil {
		return 0, err
	}
	gitMounts, err := gitReadOnlyMounts(dir, dir, "")
	if err != nil {
		return 0, err
	}
	if _, err := d.Output(ctx, "network", "inspect", bootstrapNetwork); err != nil {
		if _, err := d.Output(ctx, "network", "create", "--label", "zdev.agent=bootstrap", bootstrapNetwork); err != nil {
			return 0, err
		}
	}
	_, rest := splitClaudeArgs(args, func(string) bool { return false })
	refusal := &agent.Refusal{Code: "bootstrap", Message: "this repository has no zdev project yet",
		Next: "write .zdev/config.yaml, then ask the developer to run `zdev start` and `zdev claude --continue`"}
	return runSandbox(ctx, gcfg, sandboxSpec{
		Name: name, Image: agent.BootstrapImage(),
		Base: runtime.ContainerConfig{
			WorkingDir: dir, Env: map[string]string{}, NetworkName: bootstrapNetwork,
			Mounts: []string{"type=bind,src=" + dir + ",dst=" + dir},
		},
		Workdir: dir, HostDir: dir, Owner: fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), GitMounts: gitMounts,
		Env:   map[string]string{"ZDEV_RELAY": agent.RelaySocket, "ZDEV_DOMAIN": gcfg.Domain},
		Fence: agent.Fence{HostPorts: acfg.HostAccess},
		Handler: func(_ context.Context, _ agent.Request, _ io.Reader, _, stderr io.Writer, _ <-chan string) int {
			fmt.Fprintln(stderr, refusal.Error())
			return 126
		},
		Prompt:     func(info *agent.ImageInfo) string { return agent.BootstrapPrompt(dir, info) },
		Connectors: acfg.ConnectorsEnabled(), Limits: acfg.Limits, Rest: rest,
	})
}

var nonName = regexp.MustCompile(`[^A-Za-z0-9-]+`)

// checkAgentPreconditions refuses agent mode where it can't be safe.
func checkAgentPreconditions(ctx context.Context, d agent.Docker, gcfg *config.GlobalConfig) error {
	if err := checkAgentDockerAPI(ctx, d); err != nil {
		return err
	}
	if gcfg.Shared.Logs.Shell {
		return errors.New("agent mode is off while the log viewer's shell is on (shared.logs.shell in ~/.zdev/global-config.yaml): it opens a shell in any container, including the router, which holds the Docker socket")
	}
	return nil
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

	info, err := agent.ProbeImage(ctx, d, agentDir, s.Image)
	if err != nil {
		return 0, err
	}
	if missing := info.Missing(); len(missing) > 0 {
		return 0, fmt.Errorf("image %s lacks %s, which agent mode needs; add it to the service's Dockerfile (Alpine: apk add %s, Debian/Ubuntu: apt-get install %s)",
			s.Image, strings.Join(missing, " and "), strings.Join(missing, " "), strings.Join(missing, " "))
	}
	arch, err := d.ServerArch(ctx)
	if err != nil {
		return 0, err
	}
	rel, err := agent.ResolveClaude(ctx, agentDir, gcfg.Agent.Claude.Channel)
	if err != nil {
		return 0, err
	}
	claudeVol, err := agent.EnsureClaude(ctx, d, rel, info.Libc, arch, func(msg string) { ui.StatusStep(msg, plain) })
	if err != nil {
		return 0, err
	}
	if err := agent.CheckClaude(ctx, d, agentDir, s.Image, info, rel.Version, claudeVol); err != nil {
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
	agent.MarkUsed(ctx, d, agentDir, claudeVol, toolsVol)

	// The agent's home (at the host's home path) and Claude's configuration.
	hostHome, err := os.UserHomeDir()
	if err != nil {
		return 0, err
	}
	hostClaude := filepath.Join(hostHome, ".claude")
	if err := os.MkdirAll(hostClaude, 0o700); err != nil {
		return 0, err
	}
	homeVol := agent.HomeVolumeName(s.Name)
	if err := d.CreateVolume(ctx, homeVol, map[string]string{"zdev.agent": "home", agent.LabelProject: s.Name}); err != nil {
		return 0, err
	}
	prepArgs := []string{"run", "--rm", "-i", "--user", "0", "--network", "none",
		"--mount", "type=volume,src=" + homeVol + ",dst=" + hostHome,
		"--mount", "type=bind,src=" + hostClaude + ",dst=/src,readonly"}
	for _, e := range agent.PrepareHomeEnv(hostHome, s.Workdir, s.Owner) {
		prepArgs = append(prepArgs, "-e", e)
	}
	prepArgs = append(prepArgs, "--entrypoint", "sh", agent.FillImage, "-s")
	if err := d.Stream(ctx, strings.NewReader(agent.PrepareHomeScript), nil, os.Stderr, prepArgs...); err != nil {
		return 0, fmt.Errorf("preparing the agent's home: %w", err)
	}
	claudeMounts, err := agent.ClaudeMounts(hostClaude, s.HostDir, s.Workdir)
	if err != nil {
		return 0, err
	}
	authDir := agent.AuthDir(agentDir)
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		return 0, err
	}

	// The session's generated files.
	sessionID := newSessionID()
	sessionDir := filepath.Join(agentDir, "sessions", sessionID)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		return 0, err
	}
	defer os.RemoveAll(sessionDir)
	if err := os.WriteFile(filepath.Join(sessionDir, "zdev-system-prompt.md"), []byte(s.Prompt(info)), 0o644); err != nil {
		return 0, err
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "managed-settings.json"), agent.ManagedSettings(s.Connectors, gcfg.Agent.DisablePlugins), 0o644); err != nil {
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
		"HOME":                            hostHome,
		"PATH":                            info.Path + ":/opt/zdev/bin:/opt/zdev/claude",
		"SHELL":                           "/bin/bash",
		"DISABLE_AUTOUPDATER":             "1",
		"CLAUDE_SECURESTORAGE_CONFIG_DIR": agent.AuthMount,
		"ZDEV_SANDBOX":                    container,
		"GIT_CONFIG_COUNT":                "1",
		"GIT_CONFIG_KEY_0":                "safe.directory",
		"GIT_CONFIG_VALUE_0":              "*",
	}
	if gitName, gitEmail := hostGitIdentity(); gitName != "" || gitEmail != "" {
		env["GIT_CONFIG_COUNT"] = "3"
		env["GIT_CONFIG_KEY_1"], env["GIT_CONFIG_VALUE_1"] = "user.name", gitName
		env["GIT_CONFIG_KEY_2"], env["GIT_CONFIG_VALUE_2"] = "user.email", gitEmail
	}
	if tz := hostTimeZone(); tz != "" {
		env["TZ"] = tz
	}
	if strings.HasPrefix(s.Owner, "0:") {
		env["IS_SANDBOX"] = "1" // Claude Code accepts skipping permission prompts as root only with it
	}
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
		"type=volume,src="+claudeVol+",dst=/opt/zdev/claude,readonly",
		"type=volume,src="+homeVol+",dst="+hostHome,
		"type=bind,src="+authDir+",dst="+agent.AuthMount,
		"type=bind,src="+sessionDir+",dst="+agent.SessionMount+",readonly",
	)
	cfg.Mounts = append(cfg.Mounts, claudeMounts...)
	cfg.Mounts = append(cfg.Mounts, s.GitMounts...)

	what := "image of service " + s.Service
	if s.Service == "" {
		what = "bootstrap image"
	}
	ui.StatusStep(fmt.Sprintf("Starting sandbox %s (%s, Claude Code %s)", container, what, rel.Version), plain)
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
	if err := startRelay(relayCtx, container, s.Handler); err != nil {
		return 0, err
	}

	// Claude, in the terminal. Docker handles the TTY. Stdin only when it is
	// a terminal or carries input: with -p and an attached but empty stdin,
	// Claude waits for piped input first.
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
	execArgs = append(execArgs, "-w", cfg.WorkingDir, container, "/opt/zdev/claude/claude",
		"--append-system-prompt-file", agent.SystemPromptFile)
	execArgs = append(execArgs, claudeSessionArgs(s.Rest, sessionID)...)

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
func startRelay(ctx context.Context, container string, handler agent.Handler) error {
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
	go func() { _ = agent.Serve(ctx, agent.PipeConn(stdout, stdin), handler) }()
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

// relayHandler checks and runs the sandbox's requests on the host. Every
// check is here: the container side is untrusted.
func relayHandler(proj *project.Project, offLimits []string) agent.Handler {
	self, _ := os.Executable()
	projectContainer := func(name string) (string, bool) {
		for svc := range proj.Config.Services {
			if proj.ContainerName(svc) == name {
				return svc, true
			}
		}
		return "", false
	}
	refuse := func(stderr io.Writer, r *agent.Refusal) int {
		fmt.Fprintln(stderr, r.Error())
		return 126
	}
	return func(ctx context.Context, req agent.Request, stdin io.Reader, stdout, stderr io.Writer, sigs <-chan string) int {
		switch req.Op {
		case agent.OpRunning, agent.OpExecStream:
			svc, ok := projectContainer(req.Container)
			if !ok {
				return refuse(stderr, &agent.Refusal{Code: "other-project", Message: req.Container + " is not a container of this project"})
			}
			for _, s := range offLimits {
				if s == svc && req.Op == agent.OpExecStream {
					return refuse(stderr, &agent.Refusal{Code: "docker-socket", Message: "service " + svc + " holds the Docker socket; the agent can't exec into it"})
				}
			}
			if req.Op == agent.OpRunning {
				if running, err := proj.Runtime.IsContainerRunning(ctx, req.Container); err != nil || !running {
					return 1
				}
				return 0
			}
			mark := agent.NewExecMark()
			env := map[string]string{agent.ExecMarkEnv: mark}
			for k, v := range req.Env {
				env[k] = v
			}
			cctx, cancel := context.WithCancel(ctx)
			defer cancel()
			interrupted := make(chan struct{})
			go func() {
				select {
				case <-sigs:
					close(interrupted)
					cancel()
				case <-cctx.Done():
				}
			}()
			err := proj.Runtime.ExecStream(cctx, req.Container, req.Argv, runtime.ExecStreamOptions{
				User: req.User, Workdir: req.Workdir, Env: env, Interactive: true,
				Stdin: stdin, Stdout: stdout, Stderr: stderr,
			})
			select {
			case <-interrupted:
			case <-ctx.Done():
			default:
				return exitCodeOf(err)
			}
			killMarked(proj, []string{req.Container}, mark)
			return 130
		}

		if err := agent.CheckRequest(req.Args, offLimits); err != nil {
			var r *agent.Refusal
			if errors.As(err, &r) {
				return refuse(stderr, r)
			}
			fmt.Fprintln(stderr, err)
			return 1
		}
		mark := agent.NewExecMark()
		c := exec.Command(self, req.Args...)
		c.Dir = proj.Dir
		c.Stdin, c.Stdout, c.Stderr = stdin, stdout, stderr
		c.Env = append(os.Environ(), agent.ExecMarkEnv+"="+mark)
		c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		c.WaitDelay = 2 * time.Second
		if err := c.Start(); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		done := make(chan error, 1)
		go func() { done <- c.Wait() }()
		interrupted := false
		gone := ctx.Done()
		for {
			select {
			case sig := <-sigs:
				s := syscall.SIGTERM
				if sig == "INT" {
					s = syscall.SIGINT
				}
				interrupted = true
				_ = syscall.Kill(-c.Process.Pid, s)
			case <-gone:
				gone = nil
				interrupted = true
				_ = syscall.Kill(-c.Process.Pid, syscall.SIGTERM)
			case err := <-done:
				code := exitCodeOf(err)
				if interrupted {
					killMarked(proj, runningServices(proj, offLimits), mark)
					if code == 0 {
						code = 130 // the docker CLI exits 0 on SIGINT
					}
				}
				return code
			}
		}
	}
}

// killMarked stops what a cancelled relayed exec left behind: a killed
// `docker exec` client doesn't stop its process in the container.
func killMarked(proj *project.Project, containers []string, mark string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for _, c := range containers {
		_ = proj.Runtime.ExecStream(ctx, c, agent.KillMarkedArgv(mark), runtime.ExecStreamOptions{
			User: "0", Stdout: io.Discard, Stderr: io.Discard,
		})
	}
}

// runningServices are the containers of the project's running services the
// agent may exec into.
func runningServices(proj *project.Project, offLimits []string) []string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var out []string
	for svc := range proj.Config.Services {
		if slices.Contains(offLimits, svc) {
			continue
		}
		name := proj.ContainerName(svc)
		if ok, err := proj.Runtime.IsContainerRunning(ctx, name); err == nil && ok {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() >= 0 {
		return exitErr.ExitCode()
	}
	return 1
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

// bootstrapNetwork is the network bootstrap sessions use: a user-defined
// network, so DNS is Docker's embedded resolver (127.0.0.11) instead of the
// engine's upstream on a private address, which the host fence blocks.
const bootstrapNetwork = "zdev-agent-bootstrap"
