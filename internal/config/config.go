package config

import "runtime"

// GlobalConfig represents ~/.zdev/config.yaml
type GlobalConfig struct {
	Version  int                  `yaml:"version"`
	Domain   string               `yaml:"domain"`
	Runtime  string               `yaml:"runtime"`
	SSL      SSLConfig            `yaml:"ssl"`
	Shared   SharedConfig         `yaml:"shared"`
	Terminal TerminalConfig       `yaml:"terminal"`
	Mutagen  MutagenGlobalConfig  `yaml:"mutagen"`
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
	Secrets         ProjectSecretsConfig     `yaml:"secrets"`
	Services map[string]ServiceConfig `yaml:"services"`
	Mutagen  ProjectMutagenConfig   `yaml:"mutagen"`
}

// ProjectSecretsConfig wires the project to a 1Password Environment.
// OpEnv is the Environment's ID (1Password app > Developer >
// Environments > Manage environment > Copy environment ID). The ID is
// not secret and safe to commit; env values reference variables in the
// Environment as `op-env://<NAME>`.
type ProjectSecretsConfig struct {
	OpEnv string `yaml:"op-env"`
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
	Image          string                `yaml:"image"`
	Dockerfile     string                `yaml:"dockerfile"` // Build a local dev image from this Dockerfile (resolved against the project root, which is also the build context) instead of pulling image; image then names the tag
	Routing        *RoutingConfig        `yaml:"routing"` // Traefik routing config (requires shared.router: true)
	WorkingDir     string                `yaml:"working_dir"`
	Volumes        []string              `yaml:"volumes"`
	Environment    map[string]string     `yaml:"environment"`
	Command        string                `yaml:"command"`
	Labels         map[string]string     `yaml:"labels"`
	RegisterToDBUI bool                  `yaml:"register_to_dbui"` // Register this service in the shared DB UI (Adminer)
	Mutagen        ServiceMutagenConfig  `yaml:"mutagen"`          // Per-service Mutagen sync defaults (ownership/permissions of files synced into the container)
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
}

// RoutingConfig defines how a service is exposed via the shared router
type RoutingConfig struct {
	Protocol string `yaml:"protocol"`  // http, https, tcp, udp
	Port     int    `yaml:"port"`      // Container port (defaults: http=80, https=443, tcp/udp=required)
	HostPort int    `yaml:"host_port"` // Host port for tcp/udp (required for tcp/udp, ignored for http/https)
	Domain   string `yaml:"domain"`    // Custom domain for http/https (defaults to project domain)
}

