package config

import (
	"runtime"
	"strings"
)

// GlobalConfig represents ~/.zdev/config.yaml
type GlobalConfig struct {
	Version  int                 `yaml:"version"`
	Domain   string              `yaml:"domain"`
	Runtime  string              `yaml:"runtime"`
	SSL      SSLConfig           `yaml:"ssl"`
	Shared   SharedConfig        `yaml:"shared"`
	Terminal TerminalConfig      `yaml:"terminal"`
	Mutagen  MutagenGlobalConfig `yaml:"mutagen"`
	DNS      DNSFallbackConfig   `yaml:"dns"`
	Agent    AgentConfig         `yaml:"agent"`
}

// AgentConfig holds the settings of the sandboxed coding agents (zdev agent,
// zdev claude). They live only in the host's global config, never in
// .zdev/config.yaml: the agent can edit project config, so anything there
// could grant it more access. What applies to every agent sits at this level;
// what only one agent understands sits in that agent's section (claude:).
type AgentConfig struct {
	Claude   AgentClaudeConfig             `yaml:"claude"`
	Defaults AgentProjectConfig            `yaml:"defaults"`
	Projects map[string]AgentProjectConfig `yaml:"projects"`
	// Credentials are tokens the egress proxy swaps in for their hosts; a
	// project uses the ones it lists (projects.<p>.credentials).
	Credentials map[string]AgentCredential `yaml:"credentials"`
	// HostHook is a command (run with sh) on this machine when a session
	// starts and ends: SessionStart/SessionEnd in ZDEV_AGENT_EVENT and as
	// Claude-hook JSON on stdin, the zdev claude PID in ZDEV_AGENT_PID.
	// "cmux" is the built-in cmux integration instead of a command.
	HostHook string `yaml:"host_hook"`
	// StatusLine picks the status line in sandboxes: "zdev" (zdev's own),
	// "own" (the developer's from ~/.claude and ~/.pi/agent), or empty for
	// zdev's unless the developer has one of their own.
	StatusLine string `yaml:"status_line"`
}

// AgentCredential is a token bound to hosts. The sandbox only ever sees a
// placeholder in Env; the value is read on the host when a session starts,
// from exactly one of Command's output, 1Password (Secret) or a file only
// the developer can read (File).
type AgentCredential struct {
	Hosts   []string `yaml:"hosts"`             // exact, or *.example.com
	Env     []string `yaml:"env,omitempty"`     // variables holding the placeholder (e.g. GH_TOKEN)
	Command string   `yaml:"command,omitempty"` // e.g. "gh auth token"
	Secret  string   `yaml:"secret,omitempty"`  // op://<vault>/<item>/<field> or op-env://<environment-id>/<VARIABLE>
	File    string   `yaml:"file,omitempty"`    // e.g. ~/.zdev/agent/credentials/github-app (mode 0600)
	// PlaceholderPrefix starts the placeholder the sandbox sees, for tools
	// that judge a token by its shape: pi sends an Anthropic token as a
	// subscription login only when it contains sk-ant-oat. The proxy swaps
	// the whole placeholder, prefix included, for the real value.
	PlaceholderPrefix string `yaml:"placeholder_prefix,omitempty"`
}

// AgentClaudeConfig is what applies to Claude Code in every sandbox.
type AgentClaudeConfig struct {
	// Channel is "stable" (default), "latest" or a version like "2.1.325".
	Channel string `yaml:"channel"`
	// DisablePlugins are Claude plugins switched off in every sandbox:
	// host-only ones whose hooks or tools don't exist there (e.g.
	// codex@openai-codex, whose hooks need node and the Codex CLI).
	DisablePlugins []string `yaml:"disable_plugins"`
}

// AgentProjectClaude is per-project policy only Claude Code understands.
type AgentProjectClaude struct {
	// Connectors enables claude.ai connectors (MCP servers of the login).
	Connectors *bool `yaml:"connectors"`
}

// AgentProjectConfig is per-project agent policy; Defaults supplies what a
// project entry leaves unset (see GlobalConfig.AgentProject).
type AgentProjectConfig struct {
	// Service whose image the agent runs in when the session names none.
	Service string `yaml:"service"`
	// Claude holds what only Claude Code understands.
	Claude AgentProjectClaude `yaml:"claude"`
	// HostAccess lists host ports the agent may reach through the host fence.
	HostAccess []int `yaml:"host_access"`
	// DockerSocket "accept" allows agent mode for a project in which some
	// service mounts the Docker socket; the sandbox is then no boundary.
	DockerSocket string      `yaml:"docker_socket"`
	Limits       AgentLimits `yaml:"limits"`
	// Egress is "log" (default: every public host, logged) or "enforce"
	// (only AllowHosts). A guard against mistakes, not a boundary.
	Egress string `yaml:"egress"`
	// AllowHosts are the hosts the agent may reach under egress: enforce,
	// exact or as *.example.com.
	AllowHosts []string `yaml:"allow_hosts"`
	// Credentials names the agent.credentials this project's sandboxes get.
	Credentials []string `yaml:"credentials"`
}

// AgentLimits bounds the agent container (--cpus, --memory, --pids-limit).
type AgentLimits struct {
	CPUs   string `yaml:"cpus"`
	Memory string `yaml:"memory"`
	Pids   int    `yaml:"pids"`
}

// AgentProject returns the agent settings for a project: its entry in
// agent.projects over agent.defaults over built-in defaults.
func (g *GlobalConfig) AgentProject(name string) AgentProjectConfig {
	out := AgentProjectConfig{Limits: AgentLimits{CPUs: "4", Memory: "8g", Pids: 4096}}
	for _, src := range []AgentProjectConfig{g.Agent.Defaults, g.Agent.Projects[name]} {
		if src.Service != "" {
			out.Service = src.Service
		}
		if src.Claude.Connectors != nil {
			out.Claude.Connectors = src.Claude.Connectors
		}
		if src.HostAccess != nil {
			out.HostAccess = src.HostAccess
		}
		if src.DockerSocket != "" {
			out.DockerSocket = src.DockerSocket
		}
		if src.Limits.CPUs != "" {
			out.Limits.CPUs = src.Limits.CPUs
		}
		if src.Limits.Memory != "" {
			out.Limits.Memory = src.Limits.Memory
		}
		if src.Limits.Pids > 0 {
			out.Limits.Pids = src.Limits.Pids
		}
		if src.Egress != "" {
			out.Egress = src.Egress
		}
		if src.AllowHosts != nil {
			out.AllowHosts = src.AllowHosts
		}
		if src.Credentials != nil {
			out.Credentials = src.Credentials
		}
	}
	if out.Egress == "" {
		out.Egress = "log"
	}
	return out
}

// ConnectorsEnabled reports whether claude.ai connectors are on (default off).
func (a AgentProjectConfig) ConnectorsEnabled() bool {
	return a.Claude.Connectors != nil && *a.Claude.Connectors
}

// DNSFallbackConfig configures the optional local DNS fallback used when
// a router blocks the public wildcard record from resolving to 127.0.0.1
// (DNS rebinding protection). Whether the fallback is ACTIVE is not stored
// here - it is derived from the presence of the host resolver config (see
// internal/resolver), so it can never drift from what actually resolves.
// Only the container image is configurable.
type DNSFallbackConfig struct {
	Image string `yaml:"image"`
}

// MutagenGlobalConfig defines global Mutagen file sync settings
type MutagenGlobalConfig struct {
	Enabled  string `yaml:"enabled"`   // "auto", "true", "false" - auto enables on macOS only
	SyncMode string `yaml:"sync_mode"` // Default sync mode (default: two-way-safe)
}

// IsMutagenEnabled returns whether Mutagen file sync should be used
// "auto" (default): enabled on macOS, disabled on Linux
// "true": always enabled
// "false": always disabled
func (g *GlobalConfig) IsMutagenEnabled() bool {
	switch g.Mutagen.Enabled {
	case "true":
		return true
	case "false":
		return false
	default: // "auto" or empty
		return runtime.GOOS == "darwin"
	}
}

// SSLConfig defines SSL/TLS certificate configuration
type SSLConfig struct {
	Enabled bool `yaml:"enabled"` // Enable HTTPS with mkcert certificates (default: true)
}

// TerminalConfig defines terminal output settings
type TerminalConfig struct {
	Plain bool `yaml:"plain"` // Disable colors, hyperlinks, and markdown rendering
}

// SharedConfig defines shared services configuration
type SharedConfig struct {
	Router        RouterConfig        `yaml:"router"`
	Mail          MailConfig          `yaml:"mail"`
	DBUI          DBUIConfig          `yaml:"db"`
	RedisInsights RedisInsightsConfig `yaml:"redis_insights"`
	Logs          LogsConfig          `yaml:"logs"`
}

// RouterConfig defines Traefik configuration
type RouterConfig struct {
	Image     string `yaml:"image"`
	Dashboard bool   `yaml:"dashboard"`
}

// MailConfig defines Mailpit configuration
type MailConfig struct {
	Image string `yaml:"image"`
}

// DBUIConfig defines Adminer configuration
type DBUIConfig struct {
	Image string `yaml:"image"`
}

// RedisInsightsConfig defines Redis Insights configuration
type RedisInsightsConfig struct {
	Image string `yaml:"image"`
}

// LogsConfig defines Dozzle log viewer configuration
type LogsConfig struct {
	Image string `yaml:"image"`
	// Shell enables Dozzle's "open a shell in the container" feature. Off by
	// default: that shell executes inside any container Dozzle can see, so with
	// the router published on all interfaces it is a remote-shell surface for
	// anyone on the LAN. Opt in per machine in ~/.zdev/global-config.yaml only
	// on a network you trust.
	Shell bool `yaml:"shell"`
}

// ProjectConfig represents .zdev/config.yaml
type ProjectConfig struct {
	Version         int                      `yaml:"version"`
	Name            string                   `yaml:"name"`
	Domain          string                   `yaml:"domain"`
	Info            string                   `yaml:"info"`
	AutoOpenAtStart bool                     `yaml:"auto_open_at_start"`
	Variables       map[string]string        `yaml:"variables"`
	Shared          ProjectSharedConfig      `yaml:"shared"`
	Environment     map[string]string        `yaml:"environment"`
	Services        map[string]ServiceConfig `yaml:"services"`
	Mutagen         ProjectMutagenConfig     `yaml:"mutagen"`

	// HostEnv lists the host environment variables the config reads through
	// ${VAR} (set by LoadProject). An agent request substitutes only those the
	// agent baseline approved.
	HostEnv []string `yaml:"-"`
}

// ProjectMutagenConfig defines project-level Mutagen settings
type ProjectMutagenConfig struct {
	Ignore []string `yaml:"ignore"` // Paths to exclude from sync (not synced in either direction)
}

// ProjectSharedConfig defines which shared services a project uses.
// Defaults are populated by defaultProjectShared() in loader.go before
// yaml.Decode, so the user's config only overrides fields they specify.
// router, mail, logs default to true; db, redis default to false.
type ProjectSharedConfig struct {
	Router        bool `yaml:"router"`
	Mail          bool `yaml:"mail"`
	DBUI          bool `yaml:"db"`
	RedisInsights bool `yaml:"redis"`
	Logs          bool `yaml:"logs"`
}

// defaultProjectShared returns the default ProjectSharedConfig used for
// projects whose .zdev/config.yaml omits the shared block (or omits
// individual fields). Connect-by-default services are enabled here;
// niche services (db, redis) stay opt-in.
func defaultProjectShared() ProjectSharedConfig {
	return ProjectSharedConfig{
		Router: true,
		Mail:   true,
		Logs:   true,
	}
}

// ServiceConfig defines a container service
type ServiceConfig struct {
	Image          string               `yaml:"image"`
	Dockerfile     string               `yaml:"dockerfile"` // Build a local dev image from this Dockerfile (resolved against the project root, which is also the build context) instead of pulling image; image then names the tag
	Routing        *RoutingConfig       `yaml:"routing"`    // Traefik routing config (requires shared.router: true)
	WorkingDir     string               `yaml:"working_dir"`
	Volumes        []string             `yaml:"volumes"`
	Environment    map[string]string    `yaml:"environment"`
	OpEnv          string               `yaml:"op-env"` // 1Password Environment ID (not secret, safe to commit): every variable in it is injected into the container env, explicit environment: entries win; single variables use op-env://<environment-id>/<VARIABLE> values in environment: instead
	Command        string               `yaml:"command"`
	Labels         map[string]string    `yaml:"labels"`
	RegisterToDBUI bool                 `yaml:"register_to_dbui"` // Register this service in the shared DB UI (Adminer)
	Mutagen        ServiceMutagenConfig `yaml:"mutagen"`          // Per-service Mutagen sync defaults (ownership/permissions of files synced into the container)
}

// ServiceMutagenConfig defines per-service Mutagen sync defaults applied to
// files materialized inside the container (the beta side of the sync). These
// map to Mutagen's `--default-*-beta` flags. Useful when the container's
// runtime user (e.g. www-data) differs from the host user, so synced files
// land with ownership the in-container process can read/write.
//
// Only stamps NEW files - changing values requires the session to be recreated
// (zdev does this automatically when it detects drift).
type ServiceMutagenConfig struct {
	User          string `yaml:"user"`           // owner for new files/dirs in the container (name or numeric UID); maps to --default-owner-beta
	Group         string `yaml:"group"`          // group for new files/dirs in the container (name or numeric GID); maps to --default-group-beta
	FileMode      string `yaml:"file_mode"`      // octal mode for new files (e.g. "0644"); maps to --default-file-mode-beta
	DirectoryMode string `yaml:"directory_mode"` // octal mode for new directories (e.g. "0755"); maps to --default-directory-mode-beta

	// NoSync lists container paths that must stay plain Docker bind mounts
	// instead of being synced. Every directory bind is synced by default,
	// which is right for source trees but wrong for a directory the container
	// reads ONCE, early: containers start before the sync sessions are
	// resumed, so an entrypoint that reads /docker-entrypoint-initdb.d, nginx
	// conf.d, or mounted certificates can find them missing. A plain bind is
	// present the moment the container starts.
	NoSync []string `yaml:"no_sync"`
}

// VolumeMountTarget returns the container path of a volume entry, ignoring the
// optional mode suffix: "./src:/app:ro" -> "/app". Returns "" for an entry
// that declares no target.
func VolumeMountTarget(volume string) string {
	parts := strings.Split(volume, ":")
	if len(parts) < 2 {
		return ""
	}
	return parts[1]
}

// RoutingConfig defines how a service is exposed via the shared router
type RoutingConfig struct {
	Protocol string `yaml:"protocol"`  // http, https, tcp, udp
	Port     int    `yaml:"port"`      // Container port (defaults: http=80, https=443, tcp/udp=required)
	HostPort int    `yaml:"host_port"` // Host port for tcp/udp (required for tcp/udp, ignored for http/https)
	Domain   string `yaml:"domain"`    // Custom domain for http/https (defaults to project domain)
}
