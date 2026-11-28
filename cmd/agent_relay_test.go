package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0ploy/zdev/internal/agent"
	"github.com/0ploy/zdev/internal/project"
	"github.com/0ploy/zdev/internal/runtime"
)

// relayChildEnv makes the test binary act as the host zdev the relay starts
// for a command (relayHandler runs os.Executable(), which in a test is this
// binary): it prints what it was started with and exits with
// relayChildExitEnv.
const (
	relayChildEnv     = "ZDEV_TEST_RELAY_CHILD"
	relayChildExitEnv = "ZDEV_TEST_RELAY_CHILD_EXIT"
)

type relayChildReport struct {
	Args         []string
	Dir          string
	AgentRequest string
	ExecMark     string
}

func TestMain(m *testing.M) {
	if os.Getenv(relayChildEnv) != "" {
		dir, _ := os.Getwd()
		_ = json.NewEncoder(os.Stdout).Encode(relayChildReport{
			Args: os.Args[1:], Dir: dir,
			AgentRequest: os.Getenv(project.AgentRequestEnv),
			ExecMark:     os.Getenv(agent.ExecMarkEnv),
		})
		code := 0
		fmt.Sscan(os.Getenv(relayChildExitEnv), &code)
		os.Exit(code)
	}
	os.Exit(m.Run())
}

const relayTestConfig = `name: shop
services:
  app:
    image: alpine
  docker1:
    image: docker:cli
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
`

// relayTestProject is project "shop" (app, plus docker1 holding the Docker
// socket) in a directory of its own, on a mock runtime.
func relayTestProject(t *testing.T) (*project.Project, *runtime.MockRuntime) {
	t.Helper()
	t.Setenv("ZDEV_HOME", t.TempDir())
	dir := t.TempDir()
	writeRelayConfig(t, dir, relayTestConfig)
	p, err := project.LoadFromDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	mock := runtime.NewMockRuntime()
	p.Runtime = mock
	return p, mock
}

func writeRelayConfig(t *testing.T, dir, cfg string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".zdev"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".zdev", "config.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
}

type relayResult struct {
	code           int
	stdout, stderr string
}

func runRelay(h agent.Handler, req agent.Request, sigs <-chan string) relayResult {
	if sigs == nil {
		sigs = make(chan string)
	}
	var stdout, stderr bytes.Buffer
	code := h(context.Background(), req, strings.NewReader(""), &stdout, &stderr, sigs)
	return relayResult{code, stdout.String(), stderr.String()}
}

func execStreamCalls(mock *runtime.MockRuntime) []runtime.MockCall {
	var out []runtime.MockCall
	for _, c := range mock.Calls {
		if c.Method == "ExecStream" {
			out = append(out, c)
		}
	}
	return out
}

// The runtime operations `zdev run` relays (OpRunning, OpExecStream) reach
// only containers of the session's project, and never one holding the
// Docker socket.
func TestRelayHandlerContainerOps(t *testing.T) {
	p, mock := relayTestProject(t)
	mock.ContainersRunning[p.ContainerName("app")] = true
	h := relayHandler(p, nil, nil, nil)

	tests := []struct {
		name    string
		req     agent.Request
		code    int
		refusal string
	}{
		{"running", agent.Request{Op: agent.OpRunning, Container: p.ContainerName("app")}, 0, ""},
		{"not running", agent.Request{Op: agent.OpRunning, Container: p.ContainerName("docker1")}, 1, ""},
		{"running, other project", agent.Request{Op: agent.OpRunning, Container: "app.blog.zdev"}, 126, "other-project"},
		{"exec, other project", agent.Request{Op: agent.OpExecStream, Container: "app.blog.zdev", Argv: []string{"id"}}, 126, "other-project"},
		{"exec, shared service", agent.Request{Op: agent.OpExecStream, Container: "zdev-router", Argv: []string{"id"}}, 126, "other-project"},
		// The session passed no socket services: the live config finds it.
		{"exec, socket service", agent.Request{Op: agent.OpExecStream, Container: p.ContainerName("docker1"), Argv: []string{"id"}}, 126, "docker-socket"},
		// Only exec is refused there; asking whether it runs is fine.
		{"running, socket service", agent.Request{Op: agent.OpRunning, Container: p.ContainerName("docker1")}, 1, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock.Calls = nil
			r := runRelay(h, tt.req, nil)
			if r.code != tt.code {
				t.Errorf("code = %d, want %d (stderr %q)", r.code, tt.code, r.stderr)
			}
			if tt.refusal != "" && !strings.Contains(r.stderr, "refused ("+tt.refusal+")") {
				t.Errorf("stderr = %q, want refusal %s", r.stderr, tt.refusal)
			}
			if tt.code == 126 && len(execStreamCalls(mock)) > 0 {
				t.Errorf("a refused request reached the runtime: %v", mock.Calls)
			}
		})
	}
}

// A relayed exec carries the host's marker, whatever the request's env says
// (it could otherwise name another exec's marker and get that one killed).
func TestRelayHandlerExecMarker(t *testing.T) {
	p, mock := relayTestProject(t)
	h := relayHandler(p, nil, nil, nil)
	mock.ExecStreamFunc = func(string, []string, runtime.ExecStreamOptions) error {
		return exec.Command("sh", "-c", "exit 3").Run()
	}
	r := runRelay(h, agent.Request{
		Op: agent.OpExecStream, Container: p.ContainerName("app"), Argv: []string{"false"},
		User: "www-data", Workdir: "/app",
		Env: map[string]string{"FOO": "bar", agent.ExecMarkEnv: "chosen-by-agent"},
	}, nil)
	if r.code != 3 {
		t.Errorf("code = %d, want the exec's 3", r.code)
	}
	calls := execStreamCalls(mock)
	if len(calls) != 1 {
		t.Fatalf("ExecStream calls = %v", calls)
	}
	opts := calls[0].Args[2].(runtime.ExecStreamOptions)
	if opts.Env["FOO"] != "bar" || opts.User != "www-data" || opts.Workdir != "/app" {
		t.Errorf("request not passed on: %+v", opts)
	}
	if m := opts.Env[agent.ExecMarkEnv]; m == "" || m == "chosen-by-agent" {
		t.Errorf("marker = %q, want the host's own", m)
	}
}

// An interrupted exec is killed by its marker (a killed `docker exec` client
// leaves the process running in the container) and reports 130.
func TestRelayHandlerExecInterrupt(t *testing.T) {
	p, mock := relayTestProject(t)
	h := relayHandler(p, nil, nil, nil)
	started := make(chan struct{})
	mock.ExecStreamContextFunc = func(ctx context.Context, _ string, cmd []string, _ runtime.ExecStreamOptions) error {
		if cmd[0] == "sh" {
			return nil // the kill
		}
		close(started)
		<-ctx.Done() // like a docker exec, it ends when cancelled
		return ctx.Err()
	}
	sigs := make(chan string)
	go func() { <-started; sigs <- "INT" }()
	r := runRelay(h, agent.Request{Op: agent.OpExecStream, Container: p.ContainerName("app"), Argv: []string{"sleep", "1000"}}, sigs)
	if r.code != 130 {
		t.Errorf("code = %d, want 130", r.code)
	}
	calls := execStreamCalls(mock)
	if len(calls) != 2 {
		t.Fatalf("ExecStream calls = %v, want the exec and its kill", calls)
	}
	mark := calls[0].Args[2].(runtime.ExecStreamOptions).Env[agent.ExecMarkEnv]
	kill := calls[1]
	if kill.Args[0] != p.ContainerName("app") || strings.Join(kill.Args[1].([]string), " ") != strings.Join(agent.KillMarkedArgv(mark), " ") {
		t.Errorf("kill = %v, want KillMarkedArgv(%q) in the exec's container", kill.Args, mark)
	}
	if kill.Args[2].(runtime.ExecStreamOptions).User != "0" {
		t.Errorf("the kill must run as root to reach the exec's processes")
	}
}

// The session's project stays the one the relay acts on: a config the agent
// renamed is ignored (its containers would be another project's).
func TestRelayHandlerRenamedConfig(t *testing.T) {
	p, mock := relayTestProject(t)
	mock.ContainersRunning[p.ContainerName("app")] = true
	writeRelayConfig(t, p.Dir, "name: blog\nservices:\n  app:\n    image: alpine\n")
	h := relayHandler(p, []string{"docker1"}, nil, nil)
	if r := runRelay(h, agent.Request{Op: agent.OpExecStream, Container: "app.blog.zdev", Argv: []string{"id"}}, nil); r.code != 126 || !strings.Contains(r.stderr, "other-project") {
		t.Errorf("exec into the renamed config's container: %+v", r)
	}
	if r := runRelay(h, agent.Request{Op: agent.OpRunning, Container: p.ContainerName("app")}, nil); r.code != 0 {
		t.Errorf("the session's own container: %+v", r)
	}
	// docker1 is gone from the renamed config, but the session's socket
	// services stay off limits.
	if r := runRelay(h, agent.Request{Op: agent.OpExecStream, Container: p.ContainerName("docker1"), Argv: []string{"id"}}, nil); r.code != 126 || !strings.Contains(r.stderr, "docker-socket") {
		t.Errorf("exec into the session's socket service: %+v", r)
	}
}

// Commands run as host zdev in the project directory, marked as the session
// project's agent request (what the policy and the env filter key on) and
// with an exec marker; refusals never start one. after runs following a
// lifecycle command, unless the policy refused it (126).
func TestRelayHandlerCommands(t *testing.T) {
	p, _ := relayTestProject(t)
	t.Setenv(relayChildEnv, "1")
	afterRuns := 0
	h := relayHandler(p, nil, func(context.Context, io.Writer) { afterRuns++ }, nil)

	tests := []struct {
		name    string
		args    []string
		exit    string
		code    int
		refusal string
		after   bool
	}{
		{"status", []string{"status"}, "0", 0, "", false},
		{"update", []string{"update"}, "0", 0, "", true},
		{"failed update", []string{"update"}, "2", 2, "", true},
		{"update the policy refused", []string{"update"}, "126", 126, "", false},
		{"down", []string{"down"}, "0", 126, "not-allowed", false},
		{"empty", nil, "0", 126, "empty", false},
		// The live config's socket service, though the session passed none.
		{"exec into the socket service", []string{"exec", "docker1", "sh"}, "0", 126, "docker-socket", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(relayChildExitEnv, tt.exit)
			afterRuns = 0
			r := runRelay(h, agent.Request{Args: tt.args}, nil)
			if r.code != tt.code {
				t.Fatalf("code = %d, want %d (stderr %q)", r.code, tt.code, r.stderr)
			}
			if (afterRuns > 0) != tt.after {
				t.Errorf("after ran %d times, want %v", afterRuns, tt.after)
			}
			if tt.refusal != "" {
				if !strings.Contains(r.stderr, "refused ("+tt.refusal+")") {
					t.Errorf("stderr = %q, want refusal %s", r.stderr, tt.refusal)
				}
				if r.stdout != "" {
					t.Errorf("a refused command started zdev: %q", r.stdout)
				}
				return
			}
			var rep relayChildReport
			if err := json.Unmarshal([]byte(r.stdout), &rep); err != nil {
				t.Fatalf("child output %q: %v", r.stdout, err)
			}
			if strings.Join(rep.Args, " ") != strings.Join(tt.args, " ") {
				t.Errorf("args = %v", rep.Args)
			}
			if d, _ := filepath.EvalSymlinks(p.Dir); rep.Dir != d && rep.Dir != p.Dir {
				t.Errorf("dir = %q, want %q", rep.Dir, p.Dir)
			}
			if rep.AgentRequest != "shop" {
				t.Errorf("%s = %q, want the session's project", project.AgentRequestEnv, rep.AgentRequest)
			}
			if rep.ExecMark == "" {
				t.Errorf("no exec marker")
			}
		})
	}
}

// The turn-end hook hands on only a session id that looks like one.
func TestRelayHandlerTurnEnd(t *testing.T) {
	p, _ := relayTestProject(t)
	var got []string
	h := relayHandler(p, nil, nil, func(_ context.Context, id string) { got = append(got, id) })
	for _, args := range [][]string{{"0b6e7f2a-1c3d-4e5f-8a9b-c0d1e2f3a4b5"}, {"../../etc"}, {"a", "b"}, nil} {
		if r := runRelay(h, agent.Request{Op: agent.OpTurnEnd, Args: args}, nil); r.code != 0 {
			t.Errorf("%v: code %d", args, r.code)
		}
	}
	if strings.Join(got, ",") != "0b6e7f2a-1c3d-4e5f-8a9b-c0d1e2f3a4b5,,," {
		t.Errorf("turnEnd got %q", got)
	}
}

// A request from a sandbox never opens a browser on the developer's machine
// (zdev logs --open, or any later caller).
func TestOpenBrowserRefusedForAgentRequests(t *testing.T) {
	t.Setenv(project.AgentRequestEnv, "shop")
	if err := openBrowser("https://logs.shared.0ploy.dev"); err == nil || !strings.Contains(err.Error(), "logs.shared.0ploy.dev") {
		t.Errorf("err = %v, want a refusal naming the URL", err)
	}
}

// `zdev claude` refuses to start a project whose bind leaves it through a
// symlink, before anything starts (review 2026-11-28, H1).
func TestApproveAgentChangesRefusesEscapingBinds(t *testing.T) {
	p, _ := relayTestProject(t)
	if err := os.Symlink(t.TempDir(), filepath.Join(p.Dir, "storage")); err != nil {
		t.Fatal(err)
	}
	svc := p.Config.Services["app"]
	svc.Volumes = []string{"./storage:/app/storage"}
	p.Config.Services["app"] = svc
	if err := approveAgentChanges(p, "0ploy.dev"); err != nil {
		t.Fatalf("not in agent mode yet: %v", err)
	}
	if _, err := project.SetAgentSyncService("shop", "app"); err != nil {
		t.Fatal(err)
	}
	err := approveAgentChanges(p, "0ploy.dev")
	if err == nil || !strings.Contains(err.Error(), "./storage leads to") || !strings.Contains(err.Error(), "bind its target path itself") {
		t.Errorf("err = %v", err)
	}
}
