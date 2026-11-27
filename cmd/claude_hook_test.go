package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAgentHookGetsEventPayloadAndPID(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	cmd := fmt.Sprintf(`{ echo "$ZDEV_AGENT_EVENT $ZDEV_AGENT_PID $ZDEV_PROJECT"; cat; echo; } >> %q`, out)

	end := startAgentHook(cmd, "shop", "/Users/dev/shop", "sid-1", true)
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
	startAgentHook("  ", "shop", "/x", "sid", true)()
}

func TestAgentHookFailureNamesLastStderrLine(t *testing.T) {
	err := runAgentHook(`echo noise >&2; echo "cmux: no socket" >&2; exit 3`, "SessionStart", "shop", agentHookPayload{})
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
	err := runAgentHook(`sleep 30 & wait`, "SessionStart", "shop", agentHookPayload{})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("took %s", d)
	}
}
