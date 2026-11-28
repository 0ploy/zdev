package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0ploy/zdev/internal/config"
)

func TestAgentHookGetsEventPayloadAndPID(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	cmd := fmt.Sprintf(`{ echo "$ZDEV_AGENT_EVENT $ZDEV_AGENT_PID $ZDEV_PROJECT"; cat; echo; } >> %q`, out)

	end := startAgentHook(cmd, "claude", "shop", "/Users/dev/shop", "sid-1", true)
	end()

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 4 {
		t.Fatalf("want 2 runs (env line + payload each), got %q", lines)
	}
	for i, event := range []string{"SessionStart", "SessionEnd"} {
		if want := fmt.Sprintf("%s %d shop", event, os.Getpid()); lines[2*i] != want {
			t.Errorf("env line %d = %q, want %q", i, lines[2*i], want)
		}
		var p agentHookPayload
		if err := json.Unmarshal([]byte(lines[2*i+1]), &p); err != nil {
			t.Fatalf("payload %q: %v", lines[2*i+1], err)
		}
		if p != (agentHookPayload{SessionID: "sid-1", Cwd: "/Users/dev/shop", HookEventName: event}) {
			t.Errorf("payload %d = %+v", i, p)
		}
	}
}

func TestAgentHookEmptyCommandIsNoop(t *testing.T) {
	startAgentHook("  ", "claude", "shop", "/x", "sid", true)()
}

func TestAgentHookFailureNamesLastStderrLine(t *testing.T) {
	err := runAgentHook(`echo noise >&2; echo "cmux: no socket" >&2; exit 3`, "SessionStart", "claude", "shop", agentHookPayload{})
	if err == nil || !strings.Contains(err.Error(), "exit status 3: cmux: no socket") {
		t.Fatalf("err = %v", err)
	}
}

func TestAgentHookTimeoutKillsProcessGroup(t *testing.T) {
	old := agentHookTimeout
	agentHookTimeout = 200 * time.Millisecond
	defer func() { agentHookTimeout = old }()

	start := time.Now()
	// The grandchild sleep holds stderr open: only killing the group ends it.
	err := runAgentHook(`sleep 30 & wait`, "SessionStart", "claude", "shop", agentHookPayload{})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("took %s", d)
	}
}

// fakeCmux puts a cmux CLI in place that records its arguments, the PID
// variables, the inherited launch variable and stdin, and returns the file.
func fakeCmux(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	out := filepath.Join(dir, "out")
	bin := filepath.Join(dir, "cmux")
	script := fmt.Sprintf("#!/bin/sh\n{ echo \"$* claude=$CMUX_CLAUDE_PID pi=$CMUX_PI_PID launch=$CMUX_AGENT_LAUNCH_KIND\"; cat; echo; } >> %q\n", out)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CMUX_BUNDLED_CLI_PATH", bin)
	t.Setenv("CMUX_SURFACE_ID", "surface-1")
	return out
}

func TestCmuxHostHookReportsPiThroughItsCmuxHook(t *testing.T) {
	out := fakeCmux(t)
	// Inherited from an agent cmux started: must not describe this launch.
	t.Setenv("CMUX_AGENT_LAUNCH_KIND", "claude")
	t.Setenv("CMUX_PI_PID", "1")

	startAgentHook(" cmux ", "pi", "shop", "/Users/dev/shop", "sid-2", true)()

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	pid := os.Getpid()
	want := []string{
		fmt.Sprintf("hooks pi session-start claude= pi=%d launch=", pid),
		fmt.Sprintf("hooks pi session-end claude= pi=%d launch=", pid),
	}
	if len(lines) != 2*len(want) {
		t.Fatalf("want %d runs, got %q", len(want), lines)
	}
	for i, w := range want {
		if lines[2*i] != w {
			t.Errorf("run %d = %q, want %q", i, lines[2*i], w)
		}
		var p agentHookPayload
		if err := json.Unmarshal([]byte(lines[2*i+1]), &p); err != nil || p.Cwd != "/Users/dev/shop" {
			t.Errorf("payload %d = %q (%v)", i, lines[2*i+1], err)
		}
	}
}

// fakeCmuxSocket listens where CMUX_SOCKET_PATH points, answers every
// command line with reply and returns the lines it got.
func fakeCmuxSocket(t *testing.T, reply string) <-chan string {
	t.Helper()
	dir, err := os.MkdirTemp("", "cmux") // short: a socket path is limited to ~100 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	got := make(chan string, 8)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			line, _ := bufio.NewReader(conn).ReadString('\n')
			got <- strings.TrimSpace(line)
			fmt.Fprintln(conn, reply)
			conn.Close()
		}
	}()
	t.Setenv("CMUX_SOCKET_PATH", sock)
	t.Setenv("CMUX_WORKSPACE_ID", "ws-1")
	t.Setenv("CMUX_SURFACE_ID", "surface-1")
	return got
}

// Claude gets a mark of its own per zdev process: cmux's `claude-hook` key is
// one per workspace, and the next session would take the tab's icon away.
func TestCmuxHostHookMarksClaudeWithAKeyOfItsOwn(t *testing.T) {
	out := fakeCmux(t)
	got := fakeCmuxSocket(t, "OK")

	startAgentHook("cmux", "claude", "shop", "/Users/dev/shop", "sid-1", true)()

	pid := os.Getpid()
	want := []string{
		fmt.Sprintf("set_agent_pid claude_code.zdev-%d %d --tab=ws-1 --panel=surface-1", pid, pid),
		fmt.Sprintf("clear_agent_pid claude_code.zdev-%d --tab=ws-1 --panel=surface-1", pid),
	}
	for _, w := range want {
		if line := <-got; line != w {
			t.Errorf("sent %q, want %q", line, w)
		}
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("the cmux CLI ran for Claude (%v)", err)
	}
}

func TestCmuxHostHookReportsCmuxRefusal(t *testing.T) {
	fakeCmuxSocket(t, "ERROR: Tab not found")
	err := runAgentHook("cmux", "SessionStart", "claude", "shop", agentHookPayload{})
	if err == nil || !strings.Contains(err.Error(), "Tab not found") {
		t.Fatalf("err = %v", err)
	}
}

func TestCmuxHostHookDoesNothingOutsideCmux(t *testing.T) {
	out := fakeCmux(t)
	t.Setenv("CMUX_SURFACE_ID", "")
	if err := runAgentHook("cmux", "SessionStart", "claude", "shop", agentHookPayload{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("cmux ran outside a cmux terminal (%v)", err)
	}
}

func TestSetupCmuxHookStoresTheBuiltInOnlyWhenAsked(t *testing.T) {
	fakeCmux(t)
	for _, tc := range []struct {
		name, current, answer, want string
	}{
		{"empty, default yes", "", "\n", "cmux"},
		{"empty, declined", "", "n\n", ""},
		{"own hook, default keeps it", "~/bin/hook", "\n", "~/bin/hook"},
		{"own hook, replaced", "~/bin/hook", "y\n", "cmux"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("ZDEV_HOME", home)
			if tc.current != "" {
				_ = os.WriteFile(filepath.Join(home, config.GlobalConfigFilename), []byte("agent:\n  host_hook: "+tc.current+"\n"), 0o600)
			}
			gcfg, err := config.LoadGlobalConfig()
			if err != nil {
				t.Fatal(err)
			}
			if err := setupCmuxHook(bufio.NewReader(strings.NewReader(tc.answer)), true, gcfg); err != nil {
				t.Fatal(err)
			}
			g, err := config.LoadGlobalConfig()
			if err != nil {
				t.Fatal(err)
			}
			if g.Agent.HostHook != tc.want {
				t.Errorf("host_hook = %q, want %q", g.Agent.HostHook, tc.want)
			}
		})
	}
}

func TestUseZdevStatusLine(t *testing.T) {
	own := func() bool { return true }
	none := func() bool { return false }
	for _, tc := range []struct {
		setting string
		hasOwn  func() bool
		want    bool
	}{
		{"", none, true}, {"", own, false}, {"zdev", own, true}, {"own", none, false},
	} {
		if got := useZdevStatusLine(tc.setting, tc.hasOwn); got != tc.want {
			t.Errorf("useZdevStatusLine(%q, own=%v) = %v", tc.setting, tc.hasOwn(), got)
		}
	}
}

func TestSetupStatusLineAsksOnlyWithOneOfTheirOwn(t *testing.T) {
	for _, tc := range []struct {
		name, settings, answer, want string
	}{
		{"none of their own: no question", `{}`, "y\n", ""},
		{"their own, default keeps it", `{"statusLine": {"type": "command", "command": "x"}}`, "\n", "own"},
		{"their own, zdev's chosen", `{"statusLine": {"type": "command", "command": "x"}}`, "y\n", "zdev"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			zhome, home := t.TempDir(), t.TempDir()
			t.Setenv("ZDEV_HOME", zhome)
			_ = os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
			_ = os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(tc.settings), 0o644)
			gcfg, err := config.LoadGlobalConfig()
			if err != nil {
				t.Fatal(err)
			}
			if err := setupStatusLine(bufio.NewReader(strings.NewReader(tc.answer)), true, gcfg, home); err != nil {
				t.Fatal(err)
			}
			g, err := config.LoadGlobalConfig()
			if err != nil {
				t.Fatal(err)
			}
			if g.Agent.StatusLine != tc.want {
				t.Errorf("status_line = %q, want %q", g.Agent.StatusLine, tc.want)
			}
		})
	}
}
