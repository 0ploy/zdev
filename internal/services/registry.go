package services

import (
	"context"

	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/resolver"
)

// SharedServiceDef is the single source of truth for a shared service:
// everything needed to start, stop, inspect, and connect/disconnect it
// from project networks. Both the CLI (zdev services start) and the
// per-project connect flow iterate over the same slice so adding a new
// shared service is a single-file change here.
type SharedServiceDef struct {
	// Name is the human-readable display name (e.g., "Router").
	Name string

	// Subdomain is the URL prefix under {Subdomain}.{Domain} (e.g.,
	// "router.shared"). EMPTY means the service has no web UI and is not
	// routed through Traefik - callers must check HasWebUI before printing
	// a URL for it. The DNS fallback is the one such service: the host
	// talks to its published loopback ports directly.
	Subdomain string

	// ContainerName is the Docker container name (e.g., "zdev_router").
	ContainerName string

	// Start, Stop, and Status manage the shared container lifecycle.
	Start  func(context.Context, *Manager) error
	Stop   func(context.Context, *Manager) error
	Status func(context.Context, *Manager) (*ServiceStatus, error)

	// Connect/Disconnect attach or detach the shared container to a
	// project's network. Called by internal/project when a project starts
	// or stops, for every service where ProjectEnabled returns true.
	//
	// Both are NIL for services that never join project networks (the DNS
	// fallback) - check with JoinsProjectNetworks before calling.
	Connect    func(ctx context.Context, m *Manager, projectNetwork string) error
	Disconnect func(ctx context.Context, m *Manager, projectNetwork string) error

	// ProjectEnabled reports whether a project wants this shared service,
	// reading the per-project config flag.
	ProjectEnabled func(*config.ProjectSharedConfig) bool

	// Enabled is a HOST-level gate, orthogonal to ProjectEnabled: nil (the
	// common case) means the service is always available, so every
	// lifecycle command manages it. A non-nil Enabled means the service
	// only exists when the host is configured for it - the DNS fallback,
	// which is gated on the host resolver file actually being installed.
	// Consult it via IsEnabled, never directly.
	Enabled func(*Manager) bool
}

// IsEnabled reports whether this service should be managed on this host.
// Services without an Enabled gate are always managed. A nil Manager (no
// global config loaded) is treated as enabled so display-only callers
// degrade to "show everything" rather than silently hiding services.
func (d SharedServiceDef) IsEnabled(m *Manager) bool {
	if d.Enabled == nil || m == nil {
		return true
	}
	return d.Enabled(m)
}

// HasWebUI reports whether the service is reachable at
// {Subdomain}.{Domain} through the router.
func (d SharedServiceDef) HasWebUI() bool {
	return d.Subdomain != ""
}

// JoinsProjectNetworks reports whether the service attaches to project
// networks (and therefore has Connect/Disconnect closures to call).
func (d SharedServiceDef) JoinsProjectNetworks() bool {
	return d.Connect != nil && d.Disconnect != nil
}

// SharedServiceByContainer looks up a registry entry by container name.
// Prefer this over indexing AllSharedServices - the order is a lifecycle
// concern and changes when services are added.
func SharedServiceByContainer(containerName string) (SharedServiceDef, bool) {
	for _, def := range AllSharedServices() {
		if def.ContainerName == containerName {
			return def, true
		}
	}
	return SharedServiceDef{}, false
}

// AllSharedServices returns the ordered list of shared services. Order
// matters for lifecycle: DNS first (on hosts that need it, nothing
// resolves without it - not even the router's own hostnames), then the
// router (the web UIs route through it), then the rest. Stop and
// disconnect walk the list in reverse.
//
// Stop, Connect, and Disconnect route straight to the Manager's generic
// helpers - the def carries everything they need (container name,
// display name, network alias). Start stays a named Manager method per
// service because it wires up the service-specific container config.
func AllSharedServices() []SharedServiceDef {
	return []SharedServiceDef{
		{
			// The local DNS fallback is infrastructure, not a convenience:
			// on a host whose router enforces DNS rebinding protection it
			// is the ONLY thing making *.<domain> resolve, so it belongs in
			// the same lifecycle as the router. It differs from the others
			// in shape, which is what Enabled / HasWebUI /
			// JoinsProjectNetworks exist for: it has no web UI, never joins
			// project networks, and only exists on hosts where the resolver
			// file is installed (see internal/resolver).
			Name:          "DNS",
			Subdomain:     "",
			ContainerName: DNSContainerName,
			Enabled:       dnsFallbackEnabled,
			Start:         func(ctx context.Context, m *Manager) error { return m.StartDNS(ctx) },
			Stop:          func(ctx context.Context, m *Manager) error { return m.StopDNS(ctx) },
			Status:        func(ctx context.Context, m *Manager) (*ServiceStatus, error) { return m.DNSStatus(ctx) },
			// Every project needs name resolution, so there is no
			// per-project opt-out; Enabled is the only gate.
			ProjectEnabled: func(*config.ProjectSharedConfig) bool { return true },
		},
		{
			Name:          "Router",
			Subdomain:     "router.shared",
			ContainerName: RouterContainerName,
			Start:         func(ctx context.Context, m *Manager) error { return m.StartRouter(ctx) },
			Stop:          stopClosure(RouterContainerName, "Router"),
			Status:        func(ctx context.Context, m *Manager) (*ServiceStatus, error) { return m.RouterStatus(ctx) },
			Connect:       connectClosure(RouterContainerName, "Router", "router"),
			Disconnect:    disconnectClosure(RouterContainerName, "Router"),
			ProjectEnabled: func(s *config.ProjectSharedConfig) bool { return s.Router },
		},
		{
			Name:          "Mail",
			Subdomain:     "mail.shared",
			ContainerName: MailContainerName,
			Start:         func(ctx context.Context, m *Manager) error { return m.StartMail(ctx) },
			Stop:          stopClosure(MailContainerName, "Mail"),
			Status:        func(ctx context.Context, m *Manager) (*ServiceStatus, error) { return m.MailStatus(ctx) },
			Connect:       connectClosure(MailContainerName, "Mail", "mail"),
			Disconnect:    disconnectClosure(MailContainerName, "Mail"),
			ProjectEnabled: func(s *config.ProjectSharedConfig) bool { return s.Mail },
		},
		{
			Name:          "DB",
			Subdomain:     "db.shared",
			ContainerName: DBUIContainerName,
			Start:         func(ctx context.Context, m *Manager) error { return m.StartDBUI(ctx) },
			Stop:          stopClosure(DBUIContainerName, "DBUI"),
			Status:        func(ctx context.Context, m *Manager) (*ServiceStatus, error) { return m.DBUIStatus(ctx) },
			Connect: func(ctx context.Context, m *Manager, net string) error {
				err := connectClosure(DBUIContainerName, "DBUI", "adminer")(ctx, m, net)
				// Refresh the Adminer servers list so the new project's
				// databases show up in the login dropdown.
				_ = UpdateAdminerServers(ctx)
				return err
			},
			Disconnect: func(ctx context.Context, m *Manager, net string) error {
				err := disconnectClosure(DBUIContainerName, "DBUI")(ctx, m, net)
				_ = UpdateAdminerServers(ctx)
				return err
			},
			ProjectEnabled: func(s *config.ProjectSharedConfig) bool { return s.DBUI },
		},
		{
			Name:          "Redis",
			Subdomain:     "redis.shared",
			ContainerName: RedisInsightsContainerName,
			Start:         func(ctx context.Context, m *Manager) error { return m.StartRedisInsights(ctx) },
			Stop:          stopClosure(RedisInsightsContainerName, "RedisInsights"),
			Status:        func(ctx context.Context, m *Manager) (*ServiceStatus, error) { return m.RedisInsightsStatus(ctx) },
			Connect:       connectClosure(RedisInsightsContainerName, "RedisInsights", "redis-insights"),
			Disconnect:    disconnectClosure(RedisInsightsContainerName, "RedisInsights"),
			ProjectEnabled: func(s *config.ProjectSharedConfig) bool { return s.RedisInsights },
		},
		{
			Name:          "Logs",
			Subdomain:     "logs.shared",
			ContainerName: LogsContainerName,
			Start:         func(ctx context.Context, m *Manager) error { return m.StartLogs(ctx) },
			Stop:          stopClosure(LogsContainerName, "Logs"),
			Status:        func(ctx context.Context, m *Manager) (*ServiceStatus, error) { return m.LogsStatus(ctx) },
			Connect:       connectClosure(LogsContainerName, "Logs", "logs"),
			Disconnect:    disconnectClosure(LogsContainerName, "Logs"),
			ProjectEnabled: func(s *config.ProjectSharedConfig) bool { return s.Logs },
		},
	}
}

// stopClosure, connectClosure, and disconnectClosure adapt the Manager's
// generic service helpers to the SharedServiceDef signatures. Connect
// MUST pass the network alias - project containers resolve the shared
// service by that short name.
func stopClosure(containerName, displayName string) func(context.Context, *Manager) error {
	return func(ctx context.Context, m *Manager) error {
		return m.stopService(ctx, containerName, displayName)
	}
}

func connectClosure(containerName, displayName string, aliases ...string) func(context.Context, *Manager, string) error {
	return func(ctx context.Context, m *Manager, projectNetwork string) error {
		statusFn := func(ctx context.Context) (*ServiceStatus, error) {
			return m.getServiceStatus(ctx, containerName, displayName)
		}
		return m.connectServiceToProject(ctx, containerName, displayName, projectNetwork, statusFn, aliases...)
	}
}

func disconnectClosure(containerName, displayName string) func(context.Context, *Manager, string) error {
	return func(ctx context.Context, m *Manager, projectNetwork string) error {
		return m.disconnectServiceFromProject(ctx, containerName, displayName, projectNetwork)
	}
}

// dnsFallbackEnabled reports whether this host routes the zdev domain
// through the local DNS container. Derived from the presence of the host
// resolver file (never a config flag) so it cannot drift from what the OS
// actually resolves - the same rule `zdev dns status` follows.
func dnsFallbackEnabled(m *Manager) bool {
	if m == nil || m.cfg == nil {
		return false
	}
	installed, err := resolver.IsInstalled(m.cfg.Domain)
	return err == nil && installed
}
