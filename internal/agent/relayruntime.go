package agent

import (
	"context"
	"fmt"
	"os"

	"github.com/0ploy/zdev/internal/runtime"
)

// RelayRuntime serves `zdev run`'s container steps inside a sandbox: the
// runtime calls go through the relay to the host, which checks and runs them.
type RelayRuntime struct {
	Socket string
}

// IsContainerRunning asks the host whether a project container runs.
func (r RelayRuntime) IsContainerRunning(ctx context.Context, name string) (bool, error) {
	code, err := Call(r.Socket, Request{Op: OpRunning, Container: name}, nil, os.Stdout, os.Stderr)
	if err != nil {
		return false, err
	}
	return code == 0, nil
}

// ExecStream runs a command in a project container through the host.
func (r RelayRuntime) ExecStream(ctx context.Context, container string, cmd []string, opts runtime.ExecStreamOptions) error {
	var stdin = opts.Stdin
	if !opts.Interactive {
		stdin = nil
	}
	code, err := Call(r.Socket, Request{Op: OpExecStream, Container: container, Argv: cmd, User: opts.User, Workdir: opts.Workdir, Env: opts.Env},
		stdin, opts.Stdout, opts.Stderr)
	if err != nil {
		return err
	}
	if code != 0 {
		return &ExitError{Code: code}
	}
	return nil
}

// ExitError is a non-zero exit of a command run through the relay.
type ExitError struct{ Code int }

func (e *ExitError) Error() string { return fmt.Sprintf("exit status %d", e.Code) }
func (e *ExitError) ExitCode() int { return e.Code }
