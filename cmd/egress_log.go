package cmd

// The egress log of an agent session: every connection the sandbox made
// through the proxy, one line each, in ~/.zdev/agent/logs/<project>/, and a
// summary when the session ends.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/0ploy/zdev/internal/agent"
	"github.com/0ploy/zdev/internal/config"
)

type egressLog struct {
	mu      sync.Mutex
	path    string
	f       *os.File
	hosts   map[string]int    // allowed connections per host
	refused map[string]string // host -> reason code
}

// newEgressLog opens the session's log (best effort: without a file the
// summary still works).
func newEgressLog(project, sessionID string) *egressLog {
	l := &egressLog{hosts: map[string]int{}, refused: map[string]string{}}
	dir := filepath.Join(config.GetZdevHome(), "agent", "logs", project)
	if err := os.MkdirAll(dir, 0o700); err == nil {
		l.path = filepath.Join(dir, time.Now().Format("20060102-150405")+"-"+sessionID+".log")
		l.f, _ = os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	}
	return l
}

func (l *egressLog) record(e agent.EgressEntry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	switch {
	case e.Request != "":
		// one request on an intercepted connection; the connection is counted below
	case e.Refused != "":
		l.refused[e.Host] = e.Refused
	default:
		l.hosts[e.Host]++
	}
	if l.f != nil {
		status := fmt.Sprintf("ok up=%d down=%d", e.Up, e.Down)
		switch {
		case e.Request != "":
			status = "request " + e.Request
		case e.Refused != "":
			status = "refused " + e.Refused + ": " + e.Detail
		}
		if e.Credential != "" {
			status += " [credential " + e.Credential + "]"
		}
		fmt.Fprintf(l.f, "%s %s:%d %s\n", e.Time.Format(time.RFC3339), e.Host, e.Port, status)
	}
}

// summary is one paragraph for the end of the session.
func (l *egressLog) summary() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.hosts) == 0 && len(l.refused) == 0 {
		return ""
	}
	hosts := make([]string, 0, len(l.hosts))
	for h := range l.hosts {
		hosts = append(hosts, h)
	}
	sort.Slice(hosts, func(i, j int) bool { return l.hosts[hosts[i]] > l.hosts[hosts[j]] })
	shown := hosts
	if len(shown) > 8 {
		shown = shown[:8]
	}
	s := fmt.Sprintf("Egress: %d host(s): %s", len(hosts), strings.Join(shown, ", "))
	if len(hosts) > len(shown) {
		s += ", ..."
	}
	if len(l.refused) > 0 {
		var r []string
		for h, why := range l.refused {
			r = append(r, h+" ("+why+")")
		}
		sort.Strings(r)
		s += fmt.Sprintf("\nRefused: %s", strings.Join(r, ", "))
	}
	if l.path != "" {
		s += "\nLog: " + l.path
	}
	return s
}

func (l *egressLog) close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil {
		_ = l.f.Close()
		l.f = nil
	}
}
