package services

import (
	"context"

	"github.com/0ploy/zdev/internal/config"
)

// SharedServiceDef is the single source of truth for a shared service:
// everything needed to start, stop, inspect, and connect/disconnect it
// from project networks. Both the CLI (zdev services start) and the
// per-project connect flow iterate over the same slice so adding a new
// shared service is a single-file change here.
type SharedServiceDef struct {
	// Name is the human-readable display name (e.g., "Router").
	Name string

	// Subdomain is the URL prefix under {Subdomain}.{Domain} (e.g., "router.shared").
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
	Connect    func(ctx context.Context, m *Manager, projectNetwork string) error
	Disconnect func(ctx context.Context, m *Manager, projectNetwork string) error

	// ProjectEnabled reports whether a project wants this shared service,
	// reading the per-project config flag.
	ProjectEnabled func(*config.ProjectSharedConfig) bool
}

// AllSharedServices returns the ordered list of shared services. Order
// matters for lifecycle: start router first (others route through it),
// stop/disconnect router last.
//
// Stop, Connect, and Disconnect route straight to the Manager's generic
// helpers - the def carries everything they need (container name,
// display name, network alias). Start stays a named Manager method per
// service because it wires up the service-specific container config.
func AllSharedServices() []SharedServiceDef {
	return []SharedServiceDef{
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
