package cmd

// Agent releases on the host: Claude Code downloads and pi builds, chosen or
// started without ever making a session wait (`zdev agent update`).

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/0ploy/zdev/internal/agent"
	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/ui"
)

// runClaudeUpdate is `zdev agent update`: asks for the channel's release now
// and downloads it for every libc that already has a build on this engine,
// so the next session doesn't wait and doesn't start with an older version.
func runClaudeUpdate(ctx context.Context) error {
	gcfg, err := config.LoadGlobalConfig()
	if err != nil {
		return err
	}
	plain := ui.PlainMode(gcfg.Terminal.Plain)
	d := agent.NewDocker()
	channel := gcfg.Agent.Claude.Channel
	if channel == "" {
		channel = "stable"
	}
	rel, err := agent.ResolveClaude(ctx, filepath.Join(config.GetZdevHome(), "agent"), channel, 0)
	if err != nil {
		return err
	}
	arch, err := d.ServerArch(ctx)
	if err != nil {
		return err
	}
	var libcs []string
	for _, pair := range agent.InstalledClaudePlatforms(ctx, d) {
		if l, a, _ := strings.Cut(pair, " "); a == arch {
			libcs = append(libcs, l)
		}
	}
	if len(libcs) == 0 {
		fmt.Printf("Claude Code %s is the current %s release. Nothing downloaded yet: `zdev agent setup claude` gets it (in a project, for its image's C library).\n", rel.Version, channel)
		return nil
	}
	for _, libc := range libcs {
		if _, err := agent.EnsureClaude(ctx, d, rel, libc, arch, func(msg string) { ui.StatusStep(msg, plain) }); err != nil {
			return err
		}
	}
	fmt.Printf("Claude Code %s (%s) is ready; new sessions use it, running ones keep their version.\n", rel.Version, channel)
	return nil
}

// runPiUpdate is `zdev agent update [pi]`: asks for pi's newest release now
// and builds it for every architecture that already has a build on this
// engine. Named explicitly (`zdev agent update pi`, which is also what a
// session runs in the background), it builds for the engine's own
// architecture when nothing is built yet; without an agent named it leaves
// a machine that never used pi alone. The background run takes the release
// a session just resolved (it would not get another answer from GitHub, and
// may be rate limited) and records a failure in the build log, where the
// next session finds it.
func runPiUpdate(ctx context.Context, explicit bool) error {
	gcfg, err := config.LoadGlobalConfig()
	if err != nil {
		return err
	}
	plain := ui.PlainMode(gcfg.Terminal.Plain)
	d := agent.NewDocker()
	agentDir := filepath.Join(config.GetZdevHome(), "agent")
	archs := agent.InstalledPiArchs(ctx, d)
	if len(archs) == 0 && !explicit {
		return nil
	}
	maxAge := time.Duration(0)
	if os.Getenv(piBackgroundEnv) != "" {
		maxAge = agent.PiCheckEvery
	}
	rel, err := agent.ResolvePi(ctx, agentDir, maxAge)
	if err != nil {
		recordPiBuildFailure(agentDir, err)
		return err
	}
	if len(archs) == 0 {
		arch, err := d.ServerArch(ctx)
		if err != nil {
			return err
		}
		archs = []string{arch}
	}
	for _, arch := range archs {
		if !agent.PiBuilt(ctx, d, rel.Version, arch) {
			ui.StatusStep(fmt.Sprintf("Building pi %s for linux/%s (a few minutes)", rel.Version, arch), plain)
		}
		if err := buildPiLogged(ctx, d, agentDir, rel, arch, func() { fmt.Println("Waiting for the pi build that's already running...") }); err != nil {
			return err
		}
	}
	fmt.Printf("pi %s is ready; new sessions use it, running ones keep their version.\n", rel.Version)
	return nil
}

// chooseClaude picks the Claude build a session runs. The channel's release
// is checked at most once a day. A session never waits for a download: it
// starts with the newest build here, and a new release downloads in the
// background for the next session. Without a build for this libc (or of a
// pinned version) the download starts in the background and the session is
// refused until it is there (zdev agent setup waits for it).
func chooseClaude(ctx context.Context, d agent.Docker, agentDir, channel, libc, arch string, plain bool) (version, vol string, err error) {
	pinned := agent.IsPinnedClaude(channel)
	haveVersion, haveVol := "", ""
	if !pinned {
		haveVersion, haveVol = agent.InstalledClaude(ctx, d, libc, arch)
	}
	rel, err := agent.ResolveClaude(ctx, agentDir, channel, agent.ClaudeCheckEvery)
	if err != nil {
		if haveVol != "" {
			return haveVersion, haveVol, nil // offline: what's here
		}
		return "", "", err
	}
	if pinned {
		if v := agent.ClaudeVolumeName(rel.Version, libc, arch); agent.VolumeComplete(ctx, d, v) {
			return rel.Version, v, nil
		}
	}
	if haveVol == "" {
		if _, err := agent.StartClaudeDownload(ctx, d, rel, libc, arch); err != nil {
			return "", "", err
		}
		return "", "", fmt.Errorf("Claude Code %s (%s build) isn't here yet: it's downloading in the background now.\nStart the session again in a minute, or run `zdev agent setup claude` to wait for it", rel.Version, libc)
	}
	if agent.CompareVersions(rel.Version, haveVersion) > 0 {
		if started, err := agent.StartClaudeDownload(ctx, d, rel, libc, arch); err == nil && started {
			fmt.Println(ui.Color(fmt.Sprintf("Claude Code %s is downloading in the background; this session runs %s, the next one the new version.", rel.Version, haveVersion), "yellow", plain))
		}
	}
	return haveVersion, haveVol, nil
}
