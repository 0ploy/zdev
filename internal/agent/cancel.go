package agent

// Cancelling a relayed command. A killed `docker exec` client leaves its
// process running in the container, so every relayed exec carries a random
// marker in its environment (inherited by everything it starts), and on
// cancel the host kills the processes that carry it.

import (
	"crypto/rand"
	"encoding/hex"

	"github.com/0ploy/zdev/internal/runtime"
)

// ExecMarkEnv is the environment variable that marks a relayed exec's
// processes. The host's docker CLI passes it on when it is set.
const ExecMarkEnv = runtime.ExecMarkEnv

// NewExecMark returns a fresh marker value.
func NewExecMark() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// KillMarkedScript is run as root with sh in the target container, with the
// marker as $1: SIGTERM to every process carrying it, SIGKILL two seconds
// later to the ones still there. Needs sh, tr and grep (BusyBox has them).
const KillMarkedScript = `m="` + ExecMarkEnv + `=$1"
marked() { for d in /proc/[0-9]*; do p=${d#/proc/}; [ "$p" = "$$" ] && continue; tr '\0' '\n' < "$d/environ" 2>/dev/null | grep -qxF "$m" && echo "$p"; done; }
p=$(marked); [ -n "$p" ] || exit 0
kill -TERM $p 2>/dev/null
for i in 1 2; do sleep 1; p=$(marked); [ -n "$p" ] || exit 0; done
kill -KILL $p 2>/dev/null
exit 0`

// KillMarkedArgv is the command for KillMarkedScript.
func KillMarkedArgv(mark string) []string {
	return []string{"sh", "-c", KillMarkedScript, "sh", mark}
}
