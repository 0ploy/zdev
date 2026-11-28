package cmd

// The host's handler for what the in-container zdev relays (PLAN_2 section
// 8). Every check runs here: the container side is untrusted.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/0ploy/zdev/internal/agent"
	"github.com/0ploy/zdev/internal/project"
	"github.com/0ploy/zdev/internal/runtime"
)

// reportedSessionID is what a session id from the sandbox (untrusted) may
// look like before it becomes an argument of the next sandbox's agent.
var reportedSessionID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// lifecycleCommands change what runs; after them the relay refreshes the
// fence and checks whether the agent's own service changed.
var lifecycleCommands = map[string]bool{"start": true, "stop": true, "restart": true, "update": true}

// relayHandler checks and runs the sandbox's requests on the host. Every
// check is here: the container side is untrusted. after runs once a
// lifecycle command finished, turnEnd when the agent finished a turn, with
// the session id it reported ("" when none; nil:
// nothing to do).
func relayHandler(proj *project.Project, offLimits []string, after func(ctx context.Context, stderr io.Writer), turnEnd func(ctx context.Context, sessionID string)) agent.Handler {
	self, _ := os.Executable()
	// live is the project as its config reads now (the agent may have added
	// a service), under the session's name: a renamed config never names
	// another project's containers here. Services holding the Docker socket
	// stay off limits whichever config they come from.
	live := func() (*project.Project, []string) {
		p, err := project.LoadFromDir(proj.Dir)
		if err != nil || p.Config.Name != proj.Config.Name {
			return proj, offLimits
		}
		off := append([]string(nil), offLimits...)
		for _, s := range p.DockerSocketServices() {
			if !slices.Contains(off, s) {
				off = append(off, s)
			}
		}
		return p, off
	}
	refuse := func(stderr io.Writer, r *agent.Refusal) int {
		fmt.Fprintln(stderr, r.Error())
		return 126
	}
	return func(ctx context.Context, req agent.Request, stdin io.Reader, stdout, stderr io.Writer, sigs <-chan string) int {
		if req.Op == agent.OpTurnEnd {
			if turnEnd != nil {
				id := ""
				if len(req.Args) == 1 && reportedSessionID.MatchString(req.Args[0]) {
					id = req.Args[0]
				}
				turnEnd(ctx, id)
			}
			return 0
		}
		cur, offLimits := live()
		switch req.Op {
		case agent.OpRunning, agent.OpExecStream:
			svc, ok := "", false
			for name := range cur.Config.Services {
				if cur.ContainerName(name) == req.Container {
					svc, ok = name, true
				}
			}
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
			env := map[string]string{}
			for k, v := range req.Env {
				env[k] = v
			}
			// After the request's own: it can't pick its marker (or another
			// exec's, to have that one killed with it).
			env[agent.ExecMarkEnv] = mark
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
		// These read .zdev/ on the host: let what the agent just wrote there
		// arrive first. (exec doesn't need it: the app container mounts the
		// same sync volume.)
		if lifecycleCommands[req.Args[0]] || req.Args[0] == "status" || req.Args[0] == "info" {
			if err := cur.FlushAgentSync(ctx); err != nil && lifecycleCommands[req.Args[0]] && req.Args[0] != "stop" {
				if strings.Contains(err.Error(), "paused") {
					fmt.Fprintln(stderr, "zdev: the file sync is paused while the project is stopped, so the host applies .zdev/ as it was at the stop; after the start, run `zdev update` for changes made since")
				} else {
					fmt.Fprintf(stderr, "zdev: waiting for the file sync failed (%v); the host may not see your latest .zdev/ changes yet\n", err)
				}
			}
		}
		mark := agent.NewExecMark()
		c := exec.Command(self, req.Args...)
		c.Dir = proj.Dir
		c.Stdin, c.Stdout, c.Stderr = stdin, stdout, stderr
		c.Env = append(os.Environ(), agent.ExecMarkEnv+"="+mark, project.AgentRequestEnv+"="+proj.Config.Name)
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
					killMarked(cur, runningServices(cur, offLimits), mark)
					if code == 0 {
						code = 130 // the docker CLI exits 0 on SIGINT
					}
				}
				if after != nil && lifecycleCommands[req.Args[0]] && code != 126 {
					actx, cancel := context.WithTimeout(context.Background(), time.Minute)
					after(actx, stderr)
					cancel()
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
