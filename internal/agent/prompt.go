package agent

// The system prompt every session gets, added to Claude Code's own with
// --append-system-prompt-file (PLAN_2 section 5). Generated from the project
// and the image probe, so it is true for this session; the agent then doesn't
// spend turns finding out where it is.

import (
	"fmt"
	"sort"
	"strings"
)

// PromptService is a service of the project, as the prompt names it.
type PromptService struct {
	Name string
	Port int // routing port, 0 when not routed
}

// PromptData is what the system prompt says about a session.
type PromptData struct {
	Project  string
	Service  string // the agent runs in this service's image
	Image    string
	Workdir  string
	URL      string // the project's routed URL, "" when none
	Services []PromptService
	Info     *ImageInfo
	Root     bool // the agent runs as root (with no capabilities)
	Fenced   bool
	// HostAccess lists host ports the fence lets through.
	HostAccess []int
	// Refused services mount the Docker socket (accepted in host config).
	Refused []string
}

// SystemPrompt renders the prompt.
func SystemPrompt(d PromptData) string {
	var b strings.Builder
	w := func(f string, a ...any) { fmt.Fprintf(&b, f, a...) }

	w("# Where you run\n\n")
	w("You are the only Claude Code of this developer: there is none on their machine, and their machine is out of your reach. ")
	w("You run in a sandbox container of the zdev project %q, created from the image of its service `%s` (%s), ", d.Project, d.Service, d.Image)
	w("with the same source, environment and network as the app. The project's source is at %s; ", d.Workdir)
	w("changes there reach the running app and the developer's editor at once. The container is removed after this session; the source, your memory and this conversation stay.\n\n")
	if d.Root {
		w("- You run as root, but with every capability dropped: you can't install system packages or change the system. To add a tool, add it to the service's Dockerfile and ask the developer to rebuild.\n")
	} else {
		w("- You run as the user who owns the project's files, without privileges. To add a tool, add it to the service's Dockerfile and ask the developer to rebuild.\n")
	}
	w("- No Docker here. You manage the project only through `zdev` (below).\n\n")

	w("# What is installed\n\n")
	if d.Info != nil {
		os := d.Info.OS
		if os == "" {
			os = "Linux"
		}
		w("- %s, %s", os, d.Info.Libc)
		if d.Info.Glibc != "" {
			w(" %s", d.Info.Glibc)
		}
		w(". bash and git are available.\n")
		if names := d.Info.ToolNames(); len(names) > 0 {
			parts := make([]string, 0, len(names))
			for _, n := range names {
				if v := d.Info.Tools[n]; v != "" {
					parts = append(parts, fmt.Sprintf("%s (%s)", n, v))
				} else {
					parts = append(parts, n)
				}
			}
			w("- Tools: %s.\n", strings.Join(parts, ", "))
		}
		if len(d.Info.BusyBox) > 0 {
			w("- These are BusyBox's, not GNU's (fewer options, e.g. no `grep -P`): %s.\n", strings.Join(d.Info.BusyBox, ", "))
		}
	}
	w("- `zdev` and `claude` are added at the end of PATH.\n\n")

	w("# The running app\n\n")
	w("The app runs in a separate container, `%s`, so its processes are not visible here (`ps` shows only yours). ", d.Service)
	w("Inspect and manage them through zdev, which forwards to the host:\n\n")
	w("- Processes: `zdev exec %s ps aux`\n", d.Service)
	w("- Logs: `zdev logs %s` (`--tail 200` for recent lines)\n", d.Service)
	w("- Restart the whole service: `zdev restart %s` (your container keeps running)\n", d.Service)
	w("- Signal one process: `zdev exec %s kill -HUP <pid>` (pids from the ps above, not yours)\n", d.Service)
	w("- An interrupted or timed-out `zdev exec` is stopped in the app container as well\n")
	w("- Run something in the app's context (its processes, /tmp, sockets): `zdev exec %s <cmd>`\n", d.Service)
	if d.Info != nil {
		if _, ok := d.Info.Tools["zpctl"]; ok {
			w("- Supervised processes: `zdev exec %s zpctl status`, `zdev exec %s zpctl restart <name>`\n", d.Service, d.Service)
		}
	}
	others := append([]PromptService(nil), d.Services...)
	sort.Slice(others, func(i, j int) bool { return others[i].Name < others[j].Name })
	for _, s := range others {
		if s.Name == d.Service {
			continue
		}
		w("- `%s`: `zdev exec %s <cmd>`, `zdev logs %s`\n", s.Name, s.Name, s.Name)
	}
	w("\nRun builds and tests here; use `zdev exec` only for what must happen in the running app. ")
	w("Don't start a second instance of the app here: this container shares the app's data volumes (databases, caches). ")
	for _, s := range d.Services {
		if s.Name == d.Service && s.Port > 0 {
			w("Reach the app over the network as `http://%s:%d`", d.Service, s.Port)
			if d.URL != "" {
				w(" or %s", d.URL)
			}
			w(". ")
		}
	}
	w("`strace` and `gdb` are not available in either container.\n\n")

	w("# zdev\n\n")
	w("`zdev` commands you run here are forwarded to the host and checked there: `exec`, `logs`, `status`, `info`, `restart <service>` and `run` are allowed for this project. ")
	w("A refusal says why and what to do: relay it to the developer instead of retrying variations. `zdev run` jobs without a service run here, in this container. ")
	w("Changes that reach outside the project (host mounts, new secret references, links) need the developer: ask them to run `zdev update` on their machine.\n")
	if len(d.Refused) > 0 {
		w("- These services hold the Docker socket and are off limits (no `zdev exec`, no network access): %s.\n", strings.Join(d.Refused, ", "))
	}
	w("\n# Git\n\n")
	w("The repository is here, synced with the developer's machine. Run git only in this container, never through `zdev exec`. ")
	w("`.git/hooks` and `.git/config` are read-only: commits, branches and merges work; for pushes, remotes and git config, ask the developer.\n\n")

	w("# Network\n\n")
	if d.Fenced {
		w("The project's services and the internet are reachable. The developer's machine and private networks are not")
		if len(d.HostAccess) > 0 {
			ports := make([]string, len(d.HostAccess))
			for i, p := range d.HostAccess {
				ports[i] = fmt.Sprint(p)
			}
			w(", except ports %s on host.docker.internal", strings.Join(ports, ", "))
		}
		w(".\n")
	} else {
		w("The project's services and the internet are reachable.\n")
	}
	return b.String()
}
