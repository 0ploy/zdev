package agent

// What the agent may run through the relay (PLAN_2 section 9). Checked on the
// host for every request; the in-container client's own checks are UX only.

import (
	"fmt"
	"strings"
)

// Refusal is a request the host won't run, with a stable code and the next step.
type Refusal struct {
	Code    string
	Message string
	Next    string
}

func (r *Refusal) Error() string {
	s := fmt.Sprintf("zdev: refused (%s): %s", r.Code, r.Message)
	if r.Next != "" {
		s += "\n" + r.Next
	}
	return s
}

// relayCommands are forwarded and run on the host for this project. `run` is
// not among them: jobs run in the sandbox itself, their container steps go
// through `exec`.
var relayCommands = map[string]bool{"exec": true, "logs": true, "status": true, "info": true}

// execValueFlags are `zdev exec` flags that take a value.
var execValueFlags = map[string]bool{"-u": true, "--user": true, "-w": true, "--workdir": true}

// CheckRequest decides whether a forwarded command may run. offLimits are
// services the agent must not exec into (they hold the Docker socket).
func CheckRequest(args []string, offLimits []string) error {
	if len(args) == 0 {
		return &Refusal{Code: "empty", Message: "no command"}
	}
	for _, a := range args {
		if a == "--config" || strings.HasPrefix(a, "--config=") {
			return &Refusal{Code: "other-project", Message: "--config can't point zdev at another project from the sandbox"}
		}
	}
	cmd := args[0]
	if cmd == "run" {
		return &Refusal{Code: "run-here", Message: "zdev run runs inside the sandbox, not on the host", Next: "run it here directly; its container steps are forwarded as zdev exec"}
	}
	if !relayCommands[cmd] {
		return &Refusal{Code: "not-allowed", Message: fmt.Sprintf("`zdev %s` is not allowed in agent mode", cmd),
			Next: "allowed: zdev exec, logs, status, info; ask the developer to run anything else on their machine"}
	}
	if cmd == "exec" {
		service := execService(args[1:])
		for _, s := range offLimits {
			if s == service {
				return &Refusal{Code: "docker-socket", Message: fmt.Sprintf("service %s holds the Docker socket; the agent can't exec into it", service),
					Next: "ask the developer to run the command on their machine"}
			}
		}
	}
	return nil
}

// execService finds the service argument of `zdev exec [flags] <service> ...`.
func execService(args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			continue
		case execValueFlags[a]:
			i++
		case strings.HasPrefix(a, "-"):
		default:
			return a
		}
	}
	return ""
}
