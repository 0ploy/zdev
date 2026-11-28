package cmd

// agent.host_hook: a command of the developer's that runs on the host when a
// `zdev claude` session starts and ends, e.g. to tell the terminal that Claude
// runs in this tab, or "cmux" for the built-in cmux integration. Only zdev's
// own facts reach it, never anything from the sandbox: the payload is built
// here, in Claude's hook shape.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/0ploy/zdev/internal/ui"
)

var agentHookTimeout = 5 * time.Second // var for tests

// agentHookPayload is what the hook reads on stdin: the fields of Claude
// Code's own hook input that zdev knows on the host.
type agentHookPayload struct {
	SessionID     string `json:"session_id"`
	Cwd           string `json:"cwd"`
	HookEventName string `json:"hook_event_name"`
}

// startAgentHook runs the hook for SessionStart and returns the function that
// runs it for SessionEnd. It spans the whole `zdev claude`, not each sandbox:
// a replaced sandbox resumes the same session. A crash skips SessionEnd, so
// the hook also gets this process's PID (ZDEV_AGENT_PID) to watch.
func startAgentHook(command, agentName, project, dir, sessionID string, plain bool) (end func()) {
	if strings.TrimSpace(command) == "" {
		return func() {}
	}
	run := func(event string) {
		if err := runAgentHook(command, event, agentName, project, agentHookPayload{SessionID: sessionID, Cwd: dir, HookEventName: event}); err != nil {
			fmt.Fprintln(os.Stderr, ui.Color(fmt.Sprintf("zdev: agent.host_hook (%s) failed: %v", event, err), "yellow", plain))
		}
	}
	run("SessionStart")
	return func() { run("SessionEnd") }
}

// cmuxHostHook is the host_hook value that selects the built-in cmux
// integration instead of a shell command.
const cmuxHostHook = "cmux"

// runAgentHook runs the command with sh, the payload on stdin and the event in
// the environment. It never holds up a session for long: after
// agentHookTimeout its whole process group is killed.
func runAgentHook(command, event, agentName, project string, p agentHookPayload) error {
	payload, err := json.Marshal(p)
	if err != nil {
		return err
	}
	argv := []string{"/bin/sh", "-c", command}
	env := os.Environ()
	if strings.TrimSpace(command) == cmuxHostHook {
		argv, env, err = cmuxHookCommand(event, agentName, env)
		if err != nil || argv == nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), agentHookTimeout)
	defer cancel()
	c := exec.CommandContext(ctx, argv[0], argv[1:]...)
	c.Stdin = bytes.NewReader(payload)
	var stderr bytes.Buffer
	c.Stderr = &stderr // stdout is discarded
	c.Env = append(env,
		"ZDEV_AGENT_EVENT="+event,
		"ZDEV_AGENT="+agentName,
		fmt.Sprintf("ZDEV_AGENT_PID=%d", os.Getpid()),
		"ZDEV_PROJECT="+project,
	)
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error { return syscall.Kill(-c.Process.Pid, syscall.SIGKILL) }
	c.WaitDelay = time.Second
	err = c.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("timed out after %s", agentHookTimeout)
	}
	if err != nil {
		if msg := lastLine(stderr.String()); msg != "" {
			return fmt.Errorf("%v: %s", err, msg)
		}
		return err
	}
	return nil
}

// cmuxHookCommand is host_hook: cmux, the hook cmux's own wrappers install,
// reporting this zdev process as the agent so cmux marks the tab while it
// runs. Claude goes through `cmux claude-hook`, pi through `cmux hooks pi`.
// cmux keeps one Claude mark per workspace (the newest session's), but one
// per tab for pi. A nil argv means nothing to do: outside a cmux terminal,
// or an event or agent cmux has no hook for.
//
// The CMUX_AGENT_* variables are dropped on purpose: inherited from an agent
// cmux started (zdev run from Claude's shell on the Mac), they would describe
// that agent's launch, cmux would trust it and record `claude --resume` to
// restore this tab, which after a cmux restart starts Claude outside the
// sandbox. Without them cmux sees the zdev process, doesn't take it for an
// agent launch and records nothing to restore.
func cmuxHookCommand(event, agentName string, env []string) (argv, outEnv []string, err error) {
	if os.Getenv("CMUX_SURFACE_ID") == "" {
		return nil, nil, nil
	}
	var sub string
	switch event {
	case "SessionStart":
		sub = "session-start"
	case "SessionEnd":
		sub = "session-end"
	default:
		return nil, nil, nil
	}
	var args []string
	var pidVar string
	switch agentName {
	case "claude":
		args, pidVar = []string{"claude-hook", sub}, "CMUX_CLAUDE_PID"
	case "pi":
		args, pidVar = []string{"hooks", "pi", sub}, "CMUX_PI_PID"
	default:
		return nil, nil, nil
	}
	bin := os.Getenv("CMUX_BUNDLED_CLI_PATH")
	if bin == "" {
		if bin, err = exec.LookPath("cmux"); err != nil {
			return nil, nil, errors.New("the cmux command is not on PATH")
		}
	}
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "CMUX_AGENT_") || k == "CMUX_CLAUDE_PID" || k == "CMUX_PI_PID" {
			continue
		}
		outEnv = append(outEnv, kv)
	}
	outEnv = append(outEnv, fmt.Sprintf("%s=%d", pidVar, os.Getpid()))
	return append([]string{bin}, args...), outEnv, nil
}

// inCmux reports whether zdev runs in a cmux terminal with its CLI at hand,
// where host_hook: cmux has something to talk to.
func inCmux() bool {
	if os.Getenv("CMUX_SURFACE_ID") == "" {
		return false
	}
	if os.Getenv("CMUX_BUNDLED_CLI_PATH") != "" {
		return true
	}
	_, err := exec.LookPath("cmux")
	return err == nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
