package cmd

// zdev claude: Claude Code in a sandbox container of the project
// (PLAN_2_AGENT_SANDBOX.md). The container is created from a service's image
// with the project's source, network and environment; Claude, zdev's Linux
// build and the login are injected; the container is removed when the session
// ends. This process is the session's host side: no daemon.

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/0ploy/zdev/internal/agent"
	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/project"
	"github.com/spf13/cobra"
)

var claudeCmd = &cobra.Command{
	Use:   "claude [service] [claude arguments...]",
	Short: "Run Claude Code in a sandbox of this project (zdev agent run claude)",
	Long: `Run Claude Code in a sandbox container of this project; the shortcut for
'zdev agent run claude'. Everything after it goes to Claude, --help included.

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

Log in once with /login inside Claude; every sandbox shares that login. Setup,
tokens and updates: zdev agent (zdev agent --help).

zdev checks for a new Claude Code release once a day (agent.claude.channel in
~/.zdev/global-config.yaml). A session never waits for it: it starts with the
newest build already here, and the new one downloads in the background for the
next session.`,
	DisableFlagParsing: true,
	RunE:               func(_ *cobra.Command, args []string) error { return runAgent(claudeAgent, args) },
}

var piCmd = &cobra.Command{
	Use:   "pi [service] [pi arguments...]",
	Short: "Run pi in a sandbox of this project (zdev agent run pi)",
	Long: `Run pi (pi.dev) in a sandbox container of this project; the shortcut for
'zdev agent run pi'. Everything after it goes to pi, --help included. The
sandbox is the same as zdev claude's, and so is the optional service as first
argument:

  zdev pi                    pi in the app service's image
  zdev pi node --continue    in the node service's image, continue the last session

Log in once with /login inside pi; every sandbox shares that login. zdev
builds pi itself from its newest release (checked once a day), for glibc and
musl images alike, in the background: a session never waits for it. The
first build comes with zdev agent setup pi (which waits for it); a session
that finds none starts it in the background and asks you to come back.`,
	DisableFlagParsing: true,
	RunE:               func(_ *cobra.Command, args []string) error { return runAgent(piAgent, args) },
}

func init() {
	rootCmd.AddCommand(claudeCmd, piCmd)
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

// runAgent runs a session of an agent: zdev claude, zdev pi, zdev agent run.
func runAgent(ag codingAgent, args []string) error {
	var exitCode int
	run := func(ctx context.Context, proj *project.Project) error {
		code, err := agentSession(ctx, ag, proj, args)
		exitCode = code
		return err
	}
	var err error
	if _, ferr := config.FindProjectDir(); ferr != nil {
		// No .zdev/ here: bootstrap mode, to set the project up.
		err = withDocker(noTimeout, func(ctx context.Context) error {
			code, err := bootstrapSession(ctx, ag, args)
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

// hasPrintFlag reports whether Claude runs non-interactively (-p/--print).
func hasPrintFlag(args []string) bool {
	for _, a := range args {
		if a == "-p" || a == "--print" {
			return true
		}
	}
	return false
}

// resumeArgs are Claude's arguments for continuing a session in a replaced
// sandbox: the conversation (the session zdev started, else the one the
// arguments named, else the newest in this project's transcripts), the model
// and agent the session started with, and a first message saying why.
func resumeArgs(rest []string, sessionID, hostDir, service string) []string {
	id := sessionID
	var keep []string
	for i := 0; i < len(rest); i++ {
		switch a := rest[i]; {
		case a == "--session-id" || a == "-r" || a == "--resume":
			if i+1 < len(rest) && !strings.HasPrefix(rest[i+1], "-") {
				id = rest[i+1]
				i++
			} else if a != "--session-id" {
				id = ""
			}
		case a == "-c" || a == "--continue":
			id = ""
		case strings.HasPrefix(a, "--session-id=") || strings.HasPrefix(a, "--resume="):
			id = a[strings.Index(a, "=")+1:]
		case a == "--model" || a == "--agent":
			if i+1 < len(rest) {
				keep = append(keep, a, rest[i+1])
				i++
			}
		case strings.HasPrefix(a, "--model=") || strings.HasPrefix(a, "--agent="):
			keep = append(keep, a)
		}
	}
	if id == "" {
		id = newestTranscript(hostDir)
	}
	msg := fmt.Sprintf("zdev replaced this sandbox because service %s changed (config, image or secrets); it now runs the new version. Continue where you left off.", service)
	out := append([]string{}, keep...)
	if id != "" {
		out = append(out, "--resume", id)
	} else {
		out = append(out, "--continue")
	}
	return append(out, msg)
}

// newestTranscript is the most recently written sandbox conversation of a
// project (<session-id>.jsonl in the sandboxes' project directory, never
// the host's).
func newestTranscript(hostDir string) string {
	dir := agent.ProjectDir(filepath.Join(config.GetZdevHome(), "agent"), hostDir)
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	newest, best := "", time.Time{}
	for _, f := range files {
		if fi, err := os.Stat(f); err == nil && fi.ModTime().After(best) {
			newest, best = strings.TrimSuffix(filepath.Base(f), ".jsonl"), fi.ModTime()
		}
	}
	return newest
}
