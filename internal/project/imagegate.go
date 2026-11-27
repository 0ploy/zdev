package project

// The sync-ready gate for services without a command: in config. Their image
// starts its own ENTRYPOINT/CMD (php-fpm, nginx, zpinit in supervise mode),
// which zdev can't prefix with the gate the way it wraps a configured
// command. Instead the container's entrypoint becomes `sh -c <gate>; exec
// "$@"` with the image's ENTRYPOINT and CMD as the arguments: the gate holds
// until the initial sync is done, then the image's own process takes over PID
// 1 with the arguments it always had. Needs sh in the image; images without
// one start unwrapped, as before, with the warning.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/runtime"
)

// imageGate is what buildContainerConfig needs to know about a service's
// image to gate it: whether to wrap, and the image's ENTRYPOINT then CMD.
type imageGate struct {
	Wrap   bool
	Args   []string
	Reason string // why not, for the warning
}

// syncGateScriptName is $0 of the wrapper script (shows up in ps).
const syncGateScriptName = "zdev-sync-gate"

// syncGatePrelude re-arms the gate, announces the wait and waits for the
// ready flag (see SyncGateCommand for why every start re-arms it).
func syncGatePrelude() string {
	return "rm -f " + syncReadyFlag + " " + legacySyncReadyFlag + " 2>/dev/null" +
		"; touch " + syncWaitingFlag +
		"; while [ ! -f " + syncReadyFlag + " ]; do sleep 0.2; done" +
		"; rm -f " + syncWaitingFlag
}

// SyncGateEntrypoint is the wrapper around an image's own entrypoint: the
// gate, then the image's ENTRYPOINT and CMD (passed as arguments) exec'd in
// its place.
func SyncGateEntrypoint() string {
	return syncGatePrelude() + `; exec "$@"`
}

// gatedEntrypoint returns the --entrypoint and arguments that run args behind
// the gate.
func gatedEntrypoint(args []string) (string, []string) {
	return "sh", append([]string{"-c", SyncGateEntrypoint(), syncGateScriptName}, args...)
}

// resolveImageGate records in p.imageGates how buildContainerConfig gates
// the service, for a synced service without a command: (the only kind that
// needs its image looked at). Every path that builds a container config for
// such a service calls it first - create, the update compare, the sandbox base
// and the git-guard compare - so all of them see the same wrapper and hash.
// Without a recorded gate, buildContainerConfig leaves the image's entrypoint
// alone, which is the behavior before this existed.
func (p *Project) resolveImageGate(ctx context.Context, name string, svc config.ServiceConfig, mutagenEnabled bool, mounts MutagenMounts) {
	if !mutagenEnabled || svc.Command != "" || !mounts.Has(name) {
		return
	}
	if p.imageGates == nil {
		p.imageGates = map[string]imageGate{}
	}
	if _, done := p.imageGates[name]; done {
		return
	}
	image := p.serviceImage(name, svc)
	entrypoint, cmd, id, err := p.Runtime.ImageEntrypoint(ctx, image)
	switch {
	case err != nil:
		p.imageGates[name] = imageGate{Reason: "its image " + image + " can't be inspected"}
		return
	case len(entrypoint)+len(cmd) == 0:
		p.imageGates[name] = imageGate{Reason: "its image has neither ENTRYPOINT nor CMD"}
		return
	}
	if !imageHasShell(ctx, p.Runtime, image, id) {
		p.imageGates[name] = imageGate{Reason: "its image has no sh to wait in"}
		return
	}
	p.imageGates[name] = imageGate{Wrap: true, Args: append(append([]string(nil), entrypoint...), cmd...)}
}

// syncGated reports whether the service waits at the sync-ready gate: a
// configured command is wrapped, and so is an image whose entrypoint is.
func (p *Project) syncGated(name string, svc config.ServiceConfig) bool {
	return svc.Command != "" || p.imageGates[name].Wrap
}

var shellCacheMu sync.Mutex

// imageHasShell reports whether image has an sh, checked once per image ID
// by running it. Only positive answers are cached: a failed check may be a
// Docker hiccup, so it is repeated on the next start rather than remembered.
func imageHasShell(ctx context.Context, rt runtime.Runtime, image, id string) bool {
	shellCacheMu.Lock()
	defer shellCacheMu.Unlock()
	path := filepath.Join(config.GetZdevHome(), "cache", "image-shell.json")
	known := map[string]bool{}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &known)
	}
	if id != "" && known[id] {
		return true
	}
	err := rt.RunContainer(ctx, runtime.RunConfig{Image: image, Entrypoint: "sh", Command: []string{"-c", "true"}, Quiet: true})
	if err != nil {
		return false
	}
	if id != "" {
		known[id] = true
		if b, err := json.Marshal(known); err == nil {
			_ = os.MkdirAll(filepath.Dir(path), 0o755)
			_ = os.WriteFile(path, b, 0o644)
		}
	}
	return true
}
