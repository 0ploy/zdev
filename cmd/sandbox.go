package cmd

// zdev inside an agent sandbox container (PLAN_2_AGENT_SANDBOX.md, section 8).
// The container runs `zdev sandbox idle` under Docker's init and `zdev sandbox
// relay` on the stdio of a `docker exec -i` held by `zdev claude` on the host.
// Every other zdev command in the sandbox is sent to the host through the
// relay, which checks and runs it there; `zdev run` runs here, with its
// container steps going through the relay.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/0ploy/zdev/internal/agent"
	"github.com/spf13/cobra"
)

// inSandbox reports whether this zdev runs inside an agent sandbox.
func inSandbox() bool { return os.Getenv("ZDEV_SANDBOX") != "" }

// sandboxLocal are the commands that run in the sandbox itself.
var sandboxLocal = map[string]bool{"sandbox": true, "run": true, "version": true, "help": true, "--help": true, "-h": true, "completion": true}

func sandboxExecute() error {
	if len(os.Args) < 2 || sandboxLocal[os.Args[1]] {
		return rootCmd.Execute()
	}
	cwd, _ := os.Getwd()
	code, err := agent.Call(os.Getenv("ZDEV_RELAY"), agent.Request{Args: os.Args[1:], Cwd: cwd}, os.Stdin, os.Stdout, os.Stderr)
	if err != nil {
		if errors.Is(err, agent.ErrNotConnected) {
			fmt.Fprintln(os.Stderr, "zdev: "+err.Error())
			os.Exit(1)
		}
		return err
	}
	os.Exit(code)
	return nil
}

var sandboxCmd = &cobra.Command{
	Use:    "sandbox",
	Short:  "Internal commands of the agent sandbox container",
	Hidden: true,
}

var sandboxIdleCmd = &cobra.Command{
	Use:   "idle",
	Short: "Keep the sandbox container running until it is stopped",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
		<-sigs
		return nil
	},
}

var sandboxRelayCmd = &cobra.Command{
	Use:   "relay",
	Short: "Carry zdev requests to the host over stdin/stdout",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		socket := os.Getenv("ZDEV_RELAY")
		if socket == "" {
			socket = agent.RelaySocket
		}
		return agent.RunRelay(socket)
	},
}

// sandboxTurnEndCmd is Claude's Stop hook (managed settings) and what zdev's
// pi extension runs when pi settled (with pi's session id, which a replaced
// sandbox resumes): it tells the host a turn ended, the moment a sandbox can
// be replaced without cutting into the agent's work. Always quiet and exit
// 0: a hook error shows in Claude.
var sandboxTurnEndCmd = &cobra.Command{
	Use:   "turn-end [session-id]",
	Short: "Tell the host that the agent finished a turn",
	Args:  cobra.ArbitraryArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		req := agent.Request{Op: agent.OpTurnEnd}
		if len(args) == 1 {
			req.Args = args
		}
		_, _ = agent.Call(os.Getenv("ZDEV_RELAY"), req, strings.NewReader(""), io.Discard, io.Discard)
		return nil
	},
}

func init() {
	sandboxCmd.AddCommand(sandboxIdleCmd, sandboxRelayCmd, sandboxTurnEndCmd)
	rootCmd.AddCommand(sandboxCmd)
}
