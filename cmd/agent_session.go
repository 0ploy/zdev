package cmd

// The host side of a sandbox session before the container runs: which
// project, service and agent version, the checks and approvals that come
// first, and the spec runSandbox gets (agentSession, bootstrapSession for a
// repository without .zdev/).

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/0ploy/zdev/internal/agent"
	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/project"
	"github.com/0ploy/zdev/internal/runtime"
	"github.com/0ploy/zdev/internal/ui"
	"golang.org/x/term"
)

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

// sandboxSpec is what differs between a project session and a bootstrap
// session; runSandbox does the rest.
type sandboxSpec struct {
	Agent     codingAgent
	Name      string // project name (labels, home volume)
	Service   string // service whose image it runs, "" for bootstrap
	Image     string
	Base      runtime.ContainerConfig // starting point: env, mounts, network, working dir
	Workdir   string                  // the source's path in the container
	HostDir   string                  // the source's path on this machine (memory key)
	Owner     string                  // uid:gid the agent runs as
	GitMounts []string                // read-only .git/hooks and .git/config
	Env       map[string]string       // added to the container's environment
	Fence     agent.Fence
	// Handler serves the relay; it gets the sandbox container's name.
	Handler func(container string) agent.Handler
	Prompt  func(info *agent.ImageInfo) string
	// Lacks explains, for the refusal, what to change when the image lacks
	// what agent mode needs; nil gives a generic message.
	Lacks      func(info *agent.ImageInfo, missing []string) string
	Connectors bool
	Limits     config.AgentLimits
	Rest       []string // the agent's arguments
	// SessionID is passed to the agent as --session-id (unless Rest picks a
	// conversation); a replaced sandbox keeps it. Empty: a new one.
	SessionID string
	// TurnHook reports the end of each turn to the relay handler
	// (agent.OpTurnEnd): Claude's Stop hook, pi's agent_settled.
	TurnHook bool
	// Egress is the session's proxy (its Log is set from EgressLog), NoProxy
	// what the agent reaches directly (project services and domains).
	Egress    agent.Egress
	EgressLog *egressLog
	NoProxy   []string
	// Credentials are swapped in by the proxy (their placeholders are in
	// Env); GitConfig is added to the sandbox's git configuration.
	Credentials []agent.Credential
	GitConfig   [][2]string
}

// gitReadOnlyMounts makes .git/hooks and .git/config read-only for the agent
// (PLAN_2 section 6): binds of the Mac's own copies, which the sync never
// carries in agent mode (project.AgentGitGuards), so nothing a container
// writes there reaches the Mac. .git/hooks is created first, so the bind
// has a source. .git/config is a single-file bind: a change on the Mac that
// replaces the file (git writes a new one) shows in the next session.
func gitReadOnlyMounts(hostDir, workdir string) ([]string, error) {
	gitDir := filepath.Join(hostDir, ".git")
	if fi, err := os.Stat(gitDir); err != nil || !fi.IsDir() {
		return nil, nil
	}
	if err := os.MkdirAll(filepath.Join(gitDir, "hooks"), 0o755); err != nil {
		return nil, err
	}
	var mounts []string
	for _, p := range project.AgentGitGuards() {
		src := filepath.Join(hostDir, p)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		if strings.ContainsAny(src, ",\"") {
			return nil, fmt.Errorf("path %q can't be mounted (contains a comma or quote)", src)
		}
		mounts = append(mounts, "type=bind,src="+src+",dst="+workdir+"/"+p+",readonly")
	}
	return mounts, nil
}

func agentSession(ctx context.Context, ag codingAgent, proj *project.Project, args []string) (int, error) {
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

	// The project runs, and the agent's mount syncs .zdev and .git.
	if _, err := project.SetAgentSyncService(name, service); err != nil {
		return 0, err
	}
	if err := approveAgentChanges(proj, gcfg.Domain); err != nil {
		return 0, err
	}
	ui.StatusStep("Starting "+name, plain)
	if err := proj.Start(ctx); err != nil {
		return 0, err
	}
	if err := proj.ApplyAgentGitGuards(ctx); err != nil {
		return 0, err
	}
	if err := proj.RecordAgentBaseline(ctx); err != nil {
		return 0, fmt.Errorf("recording what the project applies: %w", err)
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
	url := proj.URL(schemeFor(gcfg))
	interactive := ag.interactive(rest)

	// A session can outlive its container: when the agent's own service
	// changed (config, image, secrets), the sandbox is replaced at the end of
	// the agent's turn and the conversation resumes.
	var (
		mu       sync.Mutex // relay requests run concurrently
		current  string     // the service as the running sandbox was built from it
		notified string     // the version the agent was last told about
		respawn  bool
		reported string             // the session id the agent last reported (pi)
		elog     *egressLog         // spans replaced sandboxes
		creds    []agent.Credential // resolved once, kept across replaced sandboxes
	)
	stale := func(ctx context.Context) bool {
		now := serviceSnapshot(ctx, d, proj.ContainerName(service))
		return now != "" && now != current
	}
	spec := func(ctx context.Context) (sandboxSpec, error) {
		cur, err := project.LoadFromDir(proj.Dir)
		if err != nil {
			return sandboxSpec{}, err
		}
		if cur.Config.Name != name {
			return sandboxSpec{}, fmt.Errorf(".zdev/config.yaml now names project %q; start `zdev %s` again", cur.Config.Name, ag.name())
		}
		base, err := cur.AgentBaseConfig(ctx, service)
		if err != nil {
			return sandboxSpec{}, err
		}
		if base.WorkingDir == "" {
			base.WorkingDir = src.ContainerPath
		}
		gitMounts, err := gitReadOnlyMounts(src.HostPath, src.ContainerPath)
		if err != nil {
			return sandboxSpec{}, err
		}
		// Without a sync volume the service config already has the git
		// guards (project.gitGuardMounts); Docker refuses a second mount.
		gitMounts = withoutTargets(gitMounts, base.Volumes)
		image := cur.ServiceImage(service)
		var promptServices []agent.PromptService
		for sname, svc := range cur.Config.Services {
			ps := agent.PromptService{Name: sname}
			if svc.Routing != nil {
				ps.Port = svc.Routing.Port
			}
			promptServices = append(promptServices, ps)
		}
		fence := agentFence(ctx, d, proj, acfg.HostAccess, socketServices)
		noProxy := agentNoProxy(gcfg.Domain, fence.AllowSubnets)
		for sname := range cur.Config.Services {
			noProxy = append(noProxy, sname)
		}
		mu.Lock()
		current = serviceSnapshot(ctx, d, proj.ContainerName(service))
		notified = current
		mu.Unlock()
		return sandboxSpec{
			Agent: ag, Name: name, Service: service, Image: image, Base: base,
			Workdir: src.ContainerPath, HostDir: proj.Dir, Owner: owner, GitMounts: gitMounts,
			Env: mergeEnv(map[string]string{
				"ZDEV_PROJECT":         name,
				"ZDEV_RELAY":           agent.RelaySocket,
				"ZDEV_HOST_PROJECTDIR": filepath.Base(proj.Dir),
				"ZDEV_DOMAIN":          gcfg.Domain,
			}, credentialEnv(gcfg, creds)),
			Credentials: creds,
			GitConfig:   sandboxGitConfig(creds),
			Fence:       fence,
			Egress:      agent.Egress{Domain: gcfg.Domain, Mode: acfg.Egress, Allow: acfg.AllowHosts, AgentHosts: ag.hosts()},
			EgressLog:   elog,
			NoProxy:     noProxy,
			Handler: func(container string) agent.Handler {
				// After a lifecycle command: socket services may have new
				// addresses, and the agent's own service may have changed.
				after := func(ctx context.Context, stderr io.Writer) {
					mu.Lock()
					defer mu.Unlock()
					if err := agentFence(ctx, d, proj, acfg.HostAccess, socketServices).Apply(ctx, d, container); err != nil {
						fmt.Fprintf(stderr, "zdev: refreshing the sandbox's network fence failed: %v\n", err)
					}
					if now := serviceSnapshot(ctx, d, proj.ContainerName(service)); now != notified && now != "" {
						notified = now
						if interactive {
							fmt.Fprintf(stderr, "zdev: service %s changed (config, image or secrets). When your turn ends, zdev replaces this sandbox with its new version and resumes this conversation; until then use `zdev exec %s` for anything that needs the new one.\n", service, service)
						} else {
							fmt.Fprintf(stderr, "zdev: service %s changed (config, image or secrets); this sandbox still runs its previous version. Use `zdev exec %s` for anything that needs the new one.\n", service, service)
						}
					}
				}
				// At the end of a turn: replace a stale sandbox. Removing the
				// container ends the agent's exec; runSandbox then returns and
				// the loop below starts the next one.
				turnEnd := func(ctx context.Context, id string) {
					if !interactive {
						return
					}
					mu.Lock()
					defer mu.Unlock()
					if id != "" {
						reported = id
					}
					if respawn || !stale(ctx) {
						return
					}
					respawn = true
					go func() {
						time.Sleep(time.Second) // let the agent finish writing the turn
						_, _ = d.Output(context.Background(), "rm", "-f", container)
					}()
				}
				return relayHandler(proj, socketServices, after, turnEnd)
			},
			Prompt: func(info *agent.ImageInfo) string {
				return agent.SystemPrompt(agent.PromptData{
					Agent: ag.title(), AgentCmd: ag.name(), Project: name, Service: service, Image: image, Workdir: src.ContainerPath,
					URL: url, Services: promptServices, Info: info,
					Root: strings.HasPrefix(owner, "0:"), Fenced: true, HostAccess: acfg.HostAccess, Refused: socketServices,
					EgressEnforce: acfg.Egress == agent.EgressEnforce, AllowHosts: acfg.AllowHosts,
					Credentials: describeCredentials(gcfg, creds),
				})
			},
			Lacks: func(info *agent.ImageInfo, missing []string) string {
				advice, fix := sandboxImageAdvice(proj, service, info, missing)
				if fix != nil {
					advice += "\nzdev agent setup can make this change for you.\n"
				}
				return advice
			},
			Connectors: acfg.ConnectorsEnabled(), Limits: acfg.Limits, Rest: rest, TurnHook: interactive,
		}, nil
	}

	creds = resolveCredentials(ctx, gcfg, acfg.Credentials)
	sessionID := newSessionID()
	elog = newEgressLog(name, sessionID)
	defer func() {
		elog.close()
		if sum := elog.summary(); sum != "" {
			fmt.Println(sum)
		}
	}()
	s, err := spec(ctx)
	if err != nil {
		return 0, err
	}
	s.SessionID = sessionID
	defer startAgentHook(gcfg.Agent.HostHook, ag.name(), name, proj.Dir, sessionID, plain)()
	for {
		code, err := runSandbox(ctx, gcfg, s)
		mu.Lock()
		again := respawn
		respawn = false
		mu.Unlock()
		if err != nil || !again {
			return code, err
		}
		ui.StatusStep(fmt.Sprintf("Service %s changed: replacing the sandbox with its new version and resuming the conversation", service), plain)
		sessionID := s.SessionID
		next, err := spec(ctx)
		if err != nil {
			return 0, err
		}
		next.SessionID = sessionID
		mu.Lock()
		last := reported
		mu.Unlock()
		next.Rest = ag.resume(rest, sessionID, last, proj.Dir, service)
		s = next
	}
}

func mergeEnv(a, b map[string]string) map[string]string {
	for k, v := range b {
		a[k] = v
	}
	return a
}

// withoutTargets drops the --mount specs whose destination one of vols
// already mounts.
func withoutTargets(mounts []string, vols []runtime.VolumeMount) []string {
	taken := map[string]bool{}
	for _, v := range vols {
		taken[v.Target] = true
	}
	var out []string
	for _, m := range mounts {
		dst := ""
		for _, part := range strings.Split(m, ",") {
			if v, ok := strings.CutPrefix(part, "dst="); ok {
				dst = v
			}
		}
		if !taken[dst] {
			out = append(out, m)
		}
	}
	return out
}

// serviceSnapshot identifies what a service container was created from: its
// config hash, secrets hash and image. Empty when it doesn't exist.
func serviceSnapshot(ctx context.Context, d agent.Docker, container string) string {
	out, err := d.Output(ctx, "inspect", "--format",
		`{{index .Config.Labels "`+runtime.ConfigHashLabel+`"}} {{index .Config.Labels "`+runtime.SecretsHashLabel+`"}} {{.Image}}`, container)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// approveAgentChanges shows the developer what starting the project would
// apply beyond what was applied before (changes an agent may have written to
// .zdev/ in an earlier session) and asks before `zdev claude` applies them.
func approveAgentChanges(proj *project.Project, domain string) error {
	if !proj.HasAgentBaseline() {
		return nil
	}
	v := proj.CheckAgentPolicy(domain)
	if len(v) == 0 {
		return nil
	}
	fmt.Println("The project's config reaches further than what it applied so far:")
	for _, x := range v {
		fmt.Println("  - " + x.String())
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return errors.New("review the config, then apply it with `zdev update` before starting a session")
	}
	fmt.Print("Apply it? [y/N] ")
	var answer string
	_, _ = fmt.Scanln(&answer)
	if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
		return errors.New("not applied; review .zdev/ (an agent may have changed it), then run `zdev update`")
	}
	return nil
}

// bootstrapSession is `zdev claude` (or pi) in a repository without .zdev/:
// the agent writes the setup in zdev's bootstrap image, without project
// network.
func bootstrapSession(ctx context.Context, ag codingAgent, args []string) (int, error) {
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
		return 0, fmt.Errorf("no .zdev/ here, and this directory is too broad for a bootstrap sandbox: run zdev %s in the repository to set up", ag.name())
	}
	name := "bootstrap-" + strings.ToLower(nonName.ReplaceAllString(filepath.Base(dir), "-"))
	acfg := gcfg.AgentProject(name)
	ui.StatusStep("No .zdev/ here: bootstrap session to set up "+filepath.Base(dir), plain)
	if err := agent.EnsureBootstrapImage(ctx, d); err != nil {
		return 0, err
	}
	gitMounts, err := gitReadOnlyMounts(dir, dir)
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
		Next: "write .zdev/config.yaml, then ask the developer to run `zdev start` and `zdev " + ag.name() + " --continue`"}
	sessionID := newSessionID()
	elog := newEgressLog(name, sessionID)
	defer func() {
		elog.close()
		if sum := elog.summary(); sum != "" {
			fmt.Println(sum)
		}
	}()
	defer startAgentHook(gcfg.Agent.HostHook, ag.name(), name, dir, sessionID, plain)()
	return runSandbox(ctx, gcfg, sandboxSpec{
		Agent:     ag,
		SessionID: sessionID,
		Egress:    agent.Egress{Domain: gcfg.Domain, Mode: acfg.Egress, Allow: acfg.AllowHosts, AgentHosts: ag.hosts()},
		EgressLog: elog,
		NoProxy:   agentNoProxy(gcfg.Domain, nil),
		Name:      name, Image: agent.BootstrapImage(),
		Base: runtime.ContainerConfig{
			WorkingDir: dir, Env: map[string]string{}, NetworkName: bootstrapNetwork,
			Mounts: []string{"type=bind,src=" + dir + ",dst=" + dir},
		},
		Workdir: dir, HostDir: dir, Owner: fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), GitMounts: gitMounts,
		Env:       map[string]string{"ZDEV_RELAY": agent.RelaySocket, "ZDEV_DOMAIN": gcfg.Domain},
		GitConfig: sandboxGitConfig(nil),
		Fence:     agent.Fence{HostPorts: acfg.HostAccess, ProxyOnly: true},
		Handler: func(string) agent.Handler {
			return func(_ context.Context, _ agent.Request, _ io.Reader, _, stderr io.Writer, _ <-chan string) int {
				fmt.Fprintln(stderr, refusal.Error())
				return 126
			}
		},
		Prompt:     func(info *agent.ImageInfo) string { return agent.BootstrapPrompt(ag.title(), ag.name(), dir, info) },
		Connectors: acfg.ConnectorsEnabled(), Limits: acfg.Limits, Rest: rest,
	})
}

var nonName = regexp.MustCompile(`[^A-Za-z0-9-]+`)

// checkAgentPreconditions refuses agent mode where it can't be safe.
func checkAgentPreconditions(ctx context.Context, d agent.Docker, gcfg *config.GlobalConfig) error {
	if gcfg.Shared.Logs.Shell {
		return errors.New("agent mode is off while the log viewer's shell is on (shared.logs.shell in ~/.zdev/global-config.yaml): it opens a shell in any container, including the router, which holds the Docker socket")
	}
	return nil
}

// bootstrapNetwork is the network bootstrap sessions use: a user-defined
// network, so DNS is Docker's embedded resolver (127.0.0.11) instead of the
// engine's upstream on a private address, which the host fence blocks.
const bootstrapNetwork = "zdev-agent-bootstrap"
