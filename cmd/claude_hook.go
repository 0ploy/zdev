package cmd

// agent.host_hook: a command of the developer's that runs on the host when a
// `zdev claude` session starts and ends, e.g. to tell the terminal that Claude
// runs in this tab (cmux). Only zdev's own facts reach it, never anything from
// the sandbox: the payload is built here, in Claude's hook shape.

import (
	"bytes"
	"context"
	"encoding/json"
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
func startAgentHook(command, project, dir, sessionID string, plain bool) (end func()) {
	if strings.TrimSpace(command) == "" {
		return func() {}
	}
	run := func(event string) {
		if err := runAgentHook(command, event, project, agentHookPayload{SessionID: sessionID, Cwd: dir, HookEventName: event}); err != nil {
			fmt.Fprintln(os.Stderr, ui.Color(fmt.Sprintf("zdev: agent.host_hook (%s) failed: %v", event, err), "yellow", plain))
		}
	}
	run("SessionStart")
	return func() { run("SessionEnd") }
}

// runAgentHook runs the command with sh, the payload on stdin and the event in
// the environment. It never holds up a session for long: after
// agentHookTimeout its whole process group is killed.
func runAgentHook(command, event, project string, p agentHookPayload) error {
	payload, err := json.Marshal(p)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), agentHookTimeout)
	defer cancel()
	c := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	c.Stdin = bytes.NewReader(payload)
	var stderr bytes.Buffer
	c.Stderr = &stderr // stdout is discarded
	c.Env = append(os.Environ(),
		"ZDEV_AGENT_EVENT="+event,
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

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
