package cmd

// What differs between the coding agents zdev runs in a sandbox: where the
// build comes from, how its state is prepared in the agent's home, and its
// command line. Everything else (the container, fence, proxy, relay, the
// replacement of a stale sandbox) is the same for all of them (runSandbox).

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/0ploy/zdev/internal/agent"
	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/ui"
)

// codingAgent is one agent zdev can run.
type codingAgent interface {
	name() string  // its command: claude, pi
	title() string // as people call it: Claude Code, pi
	// hosts are its own (API, login), allowed under egress enforce.
	hosts() []string
	// mount is where its build volume sits in the container.
	mount() string
	// sessionMount is where the session's generated files are mounted.
	sessionMount() string
	// install returns the version and volume of the build the image runs,
	// for a session: it never waits for a download or build. A newer
	// release is fetched in the background for the next session; with no
	// build for the image at all, the fetch starts in the background and
	// install returns an error that says so (zdev agent setup waits).
	install(ctx context.Context, d agent.Docker, agentDir string, gcfg *config.GlobalConfig, image string, info *agent.ImageInfo, arch string, plain bool) (version, vol string, err error)
	// ensure gets the newest release's build for libc ("" when it can't be
	// told; an agent that needs it then gets nothing) and arch, waiting for
	// it: zdev agent setup.
	ensure(ctx context.Context, d agent.Docker, agentDir string, gcfg *config.GlobalConfig, libc, arch string, plain bool) error
	// prepare sets up its state in the home volume and the session files,
	// and returns what the container needs.
	prepare(ctx context.Context, d agent.Docker, p agentPrep) (*agentSetup, error)
	// argv is the session's command in the container.
	argv(rest []string, sessionID string) []string
	// interactive reports whether the arguments open the terminal UI.
	interactive(rest []string) bool
	// resume is the arguments for continuing in a replaced sandbox: zdev's
	// session id, and the one the agent reported at its last turn end ("" if
	// none).
	resume(rest []string, sessionID, reported, hostDir, service string) []string
}

// agentPrep is what prepare gets.
type agentPrep struct {
	AgentDir   string // ~/.zdev/agent
	HostHome   string // the developer's home, also the agent's HOME
	HomeVol    string // the per-project volume behind HOME
	ToolsVol   string // the sandbox tools (jq for the preparation scripts)
	Workdir    string // the source in the container
	HostDir    string // the source on this machine
	Owner      string // uid:gid the agent runs as
	SessionDir string // the session's generated files, on the host
	Prompt     string // zdev's system prompt
	TurnHook   bool   // report turn ends (replacing a stale sandbox)
	Connectors bool
	Global     *config.GlobalConfig
}

// agentSetup is what the container needs for the agent.
type agentSetup struct {
	Mounts []string
	Env    map[string]string
}

var (
	claudeAgent codingAgent = claudeKind{}
	piAgent     codingAgent = piKind{}
	agents                  = []codingAgent{claudeAgent, piAgent}
)

// agentNamed returns the agent with that command name.
func agentNamed(name string) (codingAgent, error) {
	for _, a := range agents {
		if a.name() == name {
			return a, nil
		}
	}
	return nil, fmt.Errorf("unknown agent %q (known: %s)", name, strings.Join(agentNames(), ", "))
}

func agentNames() []string {
	var out []string
	for _, a := range agents {
		out = append(out, a.name())
	}
	return out
}

// prepareHome runs a preparation script as root in a one-shot container
// with the home volume at the host's home path and src (when it exists)
// read-only at /src.
func prepareHome(ctx context.Context, d agent.Docker, p agentPrep, src, script string, env []string) error {
	args := []string{"run", "--rm", "-i", "--user", "0", "--network", "none",
		"--mount", "type=volume,src=" + p.HomeVol + ",dst=" + p.HostHome}
	if fi, err := os.Stat(src); err == nil && fi.IsDir() {
		args = append(args, "--mount", "type=bind,src="+src+",dst=/src,readonly")
	}
	if p.ToolsVol != "" {
		args = append(args, "--mount", "type=volume,src="+p.ToolsVol+",dst=/tools,readonly")
	}
	for _, e := range env {
		args = append(args, "-e", e)
	}
	args = append(args, "--entrypoint", "sh", agent.FillImage, "-s")
	if err := d.Stream(ctx, strings.NewReader(script), nil, os.Stderr, args...); err != nil {
		return fmt.Errorf("preparing the agent's home: %w", err)
	}
	return nil
}

// --- Claude Code -----------------------------------------------------------

type claudeKind struct{}

func (claudeKind) name() string    { return "claude" }
func (claudeKind) title() string   { return "Claude Code" }
func (claudeKind) hosts() []string { return agent.ClaudeHosts }
func (claudeKind) mount() string   { return "/opt/zdev/claude" }

func (claudeKind) sessionMount() string { return agent.SessionMount }

func (claudeKind) install(ctx context.Context, d agent.Docker, agentDir string, gcfg *config.GlobalConfig, image string, info *agent.ImageInfo, arch string, plain bool) (string, string, error) {
	version, vol, err := chooseClaude(ctx, d, agentDir, gcfg.Agent.Claude.Channel, info.Libc, arch, plain)
	if err != nil {
		return "", "", err
	}
	if err := agent.CheckClaude(ctx, d, agentDir, image, info, version, vol); err != nil {
		return "", "", err
	}
	return version, vol, nil
}

// ensure downloads the channel's release for libc unless it is here,
// waiting for a download already running.
func (claudeKind) ensure(ctx context.Context, d agent.Docker, agentDir string, gcfg *config.GlobalConfig, libc, arch string, plain bool) error {
	if libc == "" {
		fmt.Println("Claude Code downloads for the sandbox image's C library, which can't be told before the image is built; the first session starts that download.")
		return nil
	}
	channel := gcfg.Agent.Claude.Channel
	if channel == "" {
		channel = "stable"
	}
	rel, err := agent.ResolveClaude(ctx, agentDir, channel, agent.ClaudeCheckEvery)
	if err != nil {
		if v, _ := agent.InstalledClaude(ctx, d, libc, arch); v != "" && !agent.IsPinnedClaude(channel) {
			fmt.Printf("Can't check for a new Claude Code release (%v); %s is here.\n", err, v)
			return nil
		}
		return err
	}
	if _, err := agent.EnsureClaude(ctx, d, rel, libc, arch, func(msg string) { ui.StatusStep(msg, plain) }); err != nil {
		return err
	}
	fmt.Printf("Claude Code %s (%s, %s build) is here.\n", rel.Version, channel, libc)
	return nil
}

func (claudeKind) prepare(ctx context.Context, d agent.Docker, p agentPrep) (*agentSetup, error) {
	hostClaude := filepath.Join(p.HostHome, ".claude")
	if err := os.MkdirAll(hostClaude, 0o700); err != nil {
		return nil, err
	}
	if err := prepareHome(ctx, d, p, hostClaude, agent.PrepareHomeScript, agent.PrepareHomeEnv(p.HostHome, p.Workdir, p.Owner)); err != nil {
		return nil, err
	}
	projectDir, err := agent.PrepareProjectDir(p.AgentDir, hostClaude, p.HostDir, p.Workdir)
	if err != nil {
		return nil, fmt.Errorf("preparing the sandbox's Claude project directory: %w", err)
	}
	mounts, err := agent.ClaudeMounts(hostClaude, projectDir, p.Workdir)
	if err != nil {
		return nil, err
	}
	authDir := agent.AuthDir(p.AgentDir)
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(p.SessionDir, "zdev-system-prompt.md"), []byte(p.Prompt), 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(p.SessionDir, "managed-settings.json"), agent.ManagedSettings(p.Connectors, p.Global.Agent.Claude.DisablePlugins, p.TurnHook), 0o644); err != nil {
		return nil, err
	}
	env := map[string]string{
		"DISABLE_AUTOUPDATER":             "1",
		"CLAUDE_SECURESTORAGE_CONFIG_DIR": agent.AuthMount,
	}
	if strings.HasPrefix(p.Owner, "0:") {
		env["IS_SANDBOX"] = "1" // Claude Code accepts skipping permission prompts as root only with it
	}
	return &agentSetup{
		Mounts: append(mounts,
			"type=bind,src="+authDir+",dst="+agent.AuthMount,
			"type=bind,src="+p.SessionDir+",dst="+agent.SessionMount+",readonly"),
		Env: env,
	}, nil
}

func (claudeKind) argv(rest []string, sessionID string) []string {
	return append([]string{"/opt/zdev/claude/claude", "--append-system-prompt-file", agent.SystemPromptFile},
		claudeSessionArgs(rest, sessionID)...)
}

func (claudeKind) interactive(rest []string) bool { return !hasPrintFlag(rest) }

func (claudeKind) resume(rest []string, sessionID, _, hostDir, service string) []string {
	return resumeArgs(rest, sessionID, hostDir, service)
}

// --- pi --------------------------------------------------------------------

type piKind struct{}

func (piKind) name() string    { return "pi" }
func (piKind) title() string   { return "pi" }
func (piKind) hosts() []string { return agent.PiHosts }
func (piKind) mount() string   { return agent.PiMount }

func (piKind) sessionMount() string { return agent.PiSessionMount }

// install picks the pi build like chooseClaude picks Claude's: the newest
// release is checked at most daily and built in the background; a session
// runs what is here.
func (piKind) install(ctx context.Context, d agent.Docker, agentDir string, _ *config.GlobalConfig, image string, info *agent.ImageInfo, arch string, plain bool) (string, string, error) {
	have, haveVol := agent.InstalledPi(ctx, d, info.Libc, arch)
	rel, err := agent.ResolvePi(ctx, agentDir, agent.PiCheckEvery)
	switch {
	case err != nil && haveVol == "":
		return "", "", err
	case haveVol == "":
		if failed := lastPiBuildFailed(agentDir); failed != "" && !agent.PiBuilding(agentDir) {
			return "", "", fmt.Errorf("pi isn't built for this machine yet, and the last build failed (%s; output in %s).\nRun `zdev agent setup pi` to build it and wait for it", failed, piBuildLog(agentDir))
		}
		startPiBuild(agentDir)
		return "", "", fmt.Errorf("pi %s isn't built for this machine yet: it's building in the background now (a few minutes; output in %s).\nStart the session again when it's done, or run `zdev agent setup pi` to wait for it", rel.Version, piBuildLog(agentDir))
	case err == nil && agent.CompareVersions(rel.Version, have) > 0:
		if startPiBuild(agentDir) {
			fmt.Println(ui.Color(fmt.Sprintf("pi %s is building in the background; this session runs %s, the next one the new version.", rel.Version, have), "yellow", plain))
		}
	}
	if err := agent.CheckPi(ctx, d, agentDir, image, info, have, haveVol); err != nil {
		return "", "", err
	}
	return have, haveVol, nil
}

// ensure builds pi's newest release (both libc variants) unless it is here,
// waiting for a build already running.
func (piKind) ensure(ctx context.Context, d agent.Docker, agentDir string, _ *config.GlobalConfig, _, arch string, plain bool) error {
	rel, err := agent.ResolvePi(ctx, agentDir, agent.PiCheckEvery)
	if err != nil {
		if v, _ := agent.InstalledPi(ctx, d, agent.LibcGlibc, arch); v != "" {
			fmt.Printf("Can't check for a new pi release (%v); pi %s is here.\n", err, v)
			return nil
		}
		return err
	}
	if agent.PiBuilt(ctx, d, rel.Version, arch) {
		fmt.Printf("pi %s, the newest release, is built for this machine.\n", rel.Version)
		return nil
	}
	ui.StatusStep(fmt.Sprintf("Building pi %s for this machine (glibc and musl; a few minutes)", rel.Version), plain)
	if err := buildPiLogged(ctx, d, agentDir, rel, arch, func() { fmt.Println("Waiting for the pi build that's already running...") }); err != nil {
		return err
	}
	fmt.Printf("pi %s is ready.\n", rel.Version)
	return nil
}

// piBuildLog is where pi builds write their output.
func piBuildLog(agentDir string) string { return filepath.Join(agentDir, "pi", "build.log") }

// piBuildFailed starts the build log's last line when a build failed.
const piBuildFailed = "FAILED: "

// buildPiLogged builds pi with the output in the build log.
func buildPiLogged(ctx context.Context, d agent.Docker, agentDir string, rel *agent.PiRelease, arch string, waiting func()) error {
	if err := os.MkdirAll(filepath.Join(agentDir, "pi"), 0o700); err != nil {
		return err
	}
	open := func() (*os.File, error) {
		return os.OpenFile(piBuildLog(agentDir), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	}
	// The log is opened once the lock is ours, not to truncate the log of a
	// build that runs.
	var log *os.File
	lw := writerFunc(func(p []byte) (int, error) {
		if log == nil {
			var err error
			if log, err = open(); err != nil {
				return 0, err
			}
			fmt.Fprintf(log, "pi %s for linux/%s, %s\n", rel.Version, arch, time.Now().Format(time.RFC3339))
		}
		return log.Write(p)
	})
	defer func() {
		if log != nil {
			log.Close()
		}
	}()
	if err := agent.BuildPi(ctx, d, agentDir, rel, arch, lw, waiting); err != nil {
		if log == nil {
			if log, _ = open(); log == nil {
				return err
			}
		}
		fmt.Fprintf(log, "\n%s%v\n", piBuildFailed, err)
		return fmt.Errorf("%w (output in %s)", err, piBuildLog(agentDir))
	}
	// Both libc builds are recorded, so the one no session uses is swept
	// like any unused volume.
	agent.MarkUsed(ctx, d, agentDir, agent.PiVolumeName(rel.Version, agent.LibcGlibc, arch),
		agent.PiVolumeName(rel.Version, agent.LibcMusl, arch))
	return nil
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// piBackgroundEnv marks the detached `zdev agent update pi` a session starts.
const piBackgroundEnv = "ZDEV_PI_BACKGROUND_BUILD"

// recordPiBuildFailure writes a failure that happened before a build could
// start (the release lookup) to the build log, so the next session reports
// it instead of announcing another background build.
func recordPiBuildFailure(agentDir string, err error) {
	if os.MkdirAll(filepath.Join(agentDir, "pi"), 0o700) != nil {
		return
	}
	_ = os.WriteFile(piBuildLog(agentDir), []byte(fmt.Sprintf("pi build, %s\n\n%s%v\n", time.Now().Format(time.RFC3339), piBuildFailed, err)), 0o600)
}

// lastPiBuildFailed is why the last build failed, "" when it didn't.
func lastPiBuildFailed(agentDir string) string {
	b, err := os.ReadFile(piBuildLog(agentDir))
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if last := lines[len(lines)-1]; strings.HasPrefix(last, piBuildFailed) {
		return strings.TrimPrefix(last, piBuildFailed)
	}
	return ""
}

// startPiBuild starts `zdev agent update pi` detached from this session, so
// the build finishes even after zdev exits. False when one runs already.
func startPiBuild(agentDir string) bool {
	if agent.PiBuilding(agentDir) {
		return false
	}
	self, err := os.Executable()
	if err != nil {
		return false
	}
	c := exec.Command(self, "agent", "update", "pi")
	c.Env = append(os.Environ(), piBackgroundEnv+"=1")
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if devnull, err := os.Open(os.DevNull); err == nil {
		c.Stdin = devnull
	}
	if err := c.Start(); err != nil {
		return false
	}
	_ = c.Process.Release()
	return true
}

func (piKind) prepare(ctx context.Context, d agent.Docker, p agentPrep) (*agentSetup, error) {
	hostPi := filepath.Join(p.HostHome, ".pi", "agent")
	models, stripped, err := agent.PiModels(hostPi)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: %v; the sandbox gets no models.json\n", err)
	}
	if len(stripped) > 0 {
		fmt.Fprintf(os.Stderr, "Note: ~/.pi/agent/models.json holds keys for %s, which stay out of the sandbox (only \"$NAME\" references come along). Give pi those keys as zdev credentials: zdev agent credentials add (with the provider's host and a variable), then \"apiKey\": \"$THAT_VARIABLE\" in models.json.\n", strings.Join(stripped, ", "))
	}
	if err := prepareHome(ctx, d, p, hostPi, agent.PiPrepareScript, agent.PiPrepareEnv(p.HostHome, p.Workdir, p.Owner, models)); err != nil {
		return nil, err
	}
	sessions := agent.PiProjectDir(p.AgentDir, p.HostDir)
	authDir := agent.PiAuthDir(p.AgentDir)
	for _, dir := range []string{sessions, authDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	}
	mounts, err := agent.PiMounts(p.HostHome, sessions, p.Workdir)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(p.SessionDir, "zdev-system-prompt.md"), []byte(p.Prompt), 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(p.SessionDir, "zdev.js"), []byte(agent.PiExtension(p.TurnHook)), 0o644); err != nil {
		return nil, err
	}
	return &agentSetup{
		Mounts: append(mounts,
			"type=bind,src="+authDir+",dst="+agent.PiAuthMount,
			"type=bind,src="+p.SessionDir+",dst="+agent.PiSessionMount+",readonly"),
		Env: map[string]string{
			"PI_CODING_AGENT_DIR": hostPi,
			// zdev chooses the version and the build carries the model
			// catalog: nothing to fetch from pi.dev at startup.
			"PI_OFFLINE":            "1",
			"PI_SKIP_VERSION_CHECK": "1",
		},
	}, nil
}

// argv leaves the session to pi (a --session-id of its own would make pi
// warn about every new session): its extension reports the id instead.
func (piKind) argv(rest []string, _ string) []string {
	return append([]string{agent.PiCommand,
		"--append-system-prompt", agent.PiSessionMount + "/zdev-system-prompt.md",
		"-e", agent.PiSessionMount + "/zdev.js"}, rest...)
}

func (piKind) interactive(rest []string) bool {
	for i, a := range rest {
		switch {
		case a == "-p" || a == "--print", a == "--mode=json" || a == "--mode=rpc":
			return false
		case a == "--mode" && i+1 < len(rest) && (rest[i+1] == "json" || rest[i+1] == "rpc"):
			return false
		}
	}
	return true
}

// resume continues the session pi reported (else the one the arguments
// named, else the newest of this project), with the model the session
// started with and a first message saying why.
func (piKind) resume(rest []string, _, reported, hostDir, service string) []string {
	id, session := "", ""
	var keep []string
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		val := func() (string, bool) {
			if k, v, ok := strings.Cut(a, "="); ok && strings.HasPrefix(k, "--") {
				return v, true
			}
			if i+1 < len(rest) {
				i++
				return rest[i], true
			}
			return "", false
		}
		switch flag, _, _ := strings.Cut(a, "="); flag {
		case "--session-id":
			if v, ok := val(); ok {
				id = v
			}
		case "--session":
			if v, ok := val(); ok {
				session = v
			}
		case "--model", "--provider", "--thinking", "--models":
			if strings.Contains(a, "=") {
				keep = append(keep, a)
			} else if v, ok := val(); ok {
				keep = append(keep, flag, v)
			}
		}
	}
	if reported != "" {
		id, session = reported, ""
	}
	if id == "" && session == "" {
		id = newestPiSession(hostDir)
	}
	msg := fmt.Sprintf("zdev replaced this sandbox because service %s changed (config, image or secrets); it now runs the new version. Continue where you left off.", service)
	out := append([]string{}, keep...)
	switch {
	case session != "":
		out = append(out, "--session", session)
	case id != "":
		out = append(out, "--session-id", id)
	default:
		out = append(out, "--continue")
	}
	return append(out, msg)
}

// newestPiSession is the id of the most recently written pi session of a
// project (<timestamp>_<id>.jsonl in the sandboxes' session directory).
func newestPiSession(hostDir string) string {
	dir := agent.PiProjectDir(filepath.Join(config.GetZdevHome(), "agent"), hostDir)
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	newest, best := "", time.Time{}
	for _, f := range files {
		if fi, err := os.Stat(f); err == nil && fi.ModTime().After(best) {
			if _, id, ok := strings.Cut(strings.TrimSuffix(filepath.Base(f), ".jsonl"), "_"); ok {
				newest, best = id, fi.ModTime()
			}
		}
	}
	return newest
}
