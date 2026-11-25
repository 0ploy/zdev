package agent

// Sandbox containers and their labels. A sandbox lives exactly as long as the
// `zdev claude` process that created it; the relay ends it when that process
// goes away. Because stdin EOF doesn't reach containers the same way on every
// engine, every start also removes sandboxes whose process is gone (labels
// hold the host PID). Sandboxes do NOT carry zdev.project: the project's
// stale-container cleanup would remove them during `zdev start`.

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

const (
	LabelSandbox = "zdev.sandbox"         // "true"
	LabelProject = "zdev.sandbox.project" // project name
	LabelSession = "zdev.sandbox.session" // session ID
	LabelService = "zdev.sandbox.service" // service whose image it runs
	LabelPID     = "zdev.sandbox.pid"     // PID of `zdev claude` on the host
	LabelHost    = "zdev.sandbox.host"    // host name of that process
)

// SandboxName is the container name for the n-th sandbox of a project.
func SandboxName(n int, project string) string { return fmt.Sprintf("sandbox-%d.%s.zdev", n, project) }

// Sandboxes lists a project's sandbox containers ("" lists all) with their labels.
func Sandboxes(ctx context.Context, d Docker, project string) (map[string]map[string]string, error) {
	filter := LabelSandbox + "=true"
	if project != "" {
		filter = LabelProject + "=" + project
	}
	out, err := d.Output(ctx, "ps", "-a", "--filter", "label="+filter, "--format", "{{.Names}}\t{{.Labels}}")
	if err != nil {
		return nil, err
	}
	res := map[string]map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		name, labels, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		m := map[string]string{}
		for _, kv := range strings.Split(labels, ",") {
			if k, v, ok := strings.Cut(kv, "="); ok {
				m[k] = v
			}
		}
		res[name] = m
	}
	return res, nil
}

// FreeSandboxName returns the lowest unused sandbox name of the project.
func FreeSandboxName(existing map[string]map[string]string, project string) string {
	for n := 1; ; n++ {
		if _, taken := existing[SandboxName(n, project)]; !taken {
			return SandboxName(n, project)
		}
	}
}

// SweepOrphans removes sandboxes created on this host whose `zdev claude`
// process no longer runs.
func SweepOrphans(ctx context.Context, d Docker) {
	all, err := Sandboxes(ctx, d, "")
	if err != nil {
		return
	}
	host, _ := os.Hostname()
	for name, labels := range all {
		if labels[LabelHost] != host {
			continue
		}
		pid, err := strconv.Atoi(labels[LabelPID])
		if err == nil && processAlive(pid) {
			continue
		}
		_, _ = d.Output(ctx, "rm", "-f", name)
	}
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
