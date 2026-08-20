package services

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/runtime"
	"github.com/0ploy/zdev/internal/state"
)

const (
	// SharedNetworkName is the name of the shared Docker network
	SharedNetworkName = "zdev_shared"

	// RouterContainerName is the name of the Traefik router container
	RouterContainerName = "zdev_router"

	// DozzleGroupLabel is the Docker label key Dozzle reads to cluster
	// containers in the UI sidebar (https://dozzle.dev/guide/container-groups).
	DozzleGroupLabel = "dev.dozzle.group"

	// DozzleVisibilityLabel is the zdev-defined label key that gates
	// Dozzle visibility. Dozzle is started with DOZZLE_FILTER set to this
	// label so containers without it are hidden, even though Dozzle has
	// full Docker socket access. Shared services always set it; project
	// containers only set it when shared.logs: true.
	DozzleVisibilityLabel = "zdev.shared.logs"

	// DozzleSharedGroup is the dev.dozzle.group value stamped on every
	// shared service container so they cluster together in the Dozzle UI
	// and nothing falls into Dozzle's default ungrouped bucket. Project
	// containers use the project name as their group instead - see
	// internal/project/project.go buildContainerConfig.
	//
	// "zzz" prefix pins the group to the bottom of Dozzle's sidebar.
	// Dozzle uses locale-aware sort where punctuation (e.g. "~") is
	// primarily ignorable, so symbol prefixes don't actually sort last;
	// a letter-based prefix does.
	DozzleSharedGroup = "zzz shared services"
)

// ServiceStatus represents the status of a shared service
type ServiceStatus struct {
	Running           bool
	ContainerID       string
	ConnectedNetworks []string
}

// Manager handles shared services (router, mail, etc.)
type Manager struct {
	cfg     *config.GlobalConfig
	runtime runtime.Runtime
}

// NewManager creates a new shared services manager
func NewManager(cfg *config.GlobalConfig) *Manager {
	return &Manager{
		cfg:     cfg,
		runtime: runtime.NewDockerCLI(),
	}
}

// EnsureSharedNetwork creates the shared network if it doesn't exist
func (m *Manager) EnsureSharedNetwork(ctx context.Context) error {
	exists, err := m.runtime.NetworkExists(ctx, SharedNetworkName)
	if err != nil {
		return fmt.Errorf("failed to check shared network: %w", err)
	}

	if !exists {
		fmt.Printf("Creating shared network %s...\n", SharedNetworkName)
		if err := m.runtime.CreateNetwork(ctx, SharedNetworkName); err != nil {
			return fmt.Errorf("failed to create shared network: %w", err)
		}
	}

	return nil
}

// =============================================================================
// Generic Service Helpers
// =============================================================================

// getServiceStatus returns the status of a service by container name
func (m *Manager) getServiceStatus(ctx context.Context, containerName, serviceName string) (*ServiceStatus, error) {
	container, err := m.runtime.GetContainer(ctx, containerName)
	if err != nil {
		return nil, fmt.Errorf("failed to check %s container: %w", serviceName, err)
	}

	if container == nil {
		return &ServiceStatus{Running: false}, nil
	}

	return &ServiceStatus{
		Running:     container.Running,
		ContainerID: container.ID,
	}, nil
}

// stopService stops a service container
func (m *Manager) stopService(ctx context.Context, containerName, displayName string) error {
	container, err := m.runtime.GetContainer(ctx, containerName)
	if err != nil {
		return fmt.Errorf("failed to check %s container: %w", displayName, err)
	}

	if container == nil {
		fmt.Printf("%s is not running\n", displayName)
		return nil
	}

	if !container.Running {
		fmt.Printf("%s is already stopped\n", displayName)
		return nil
	}

	fmt.Printf("Stopping %s...\n", strings.ToLower(displayName))
	return m.runtime.StopContainer(ctx, containerName)
}

// startService starts a service container with the given config.
//
// If a container already exists, its stamped runtime.ConfigHashLabel is
// compared to the hash of the freshly built expected config. A mismatch
// means the baked config has drifted from what the current code would
// produce (e.g. SSL flipped on, image bumped, domain changed), so the
// container is removed and recreated. Matching hash + running = no-op;
// matching hash + stopped = plain start.
func (m *Manager) startService(ctx context.Context, containerName, displayName, image string, configFn func() runtime.ContainerConfig) error {
	// Ensure shared network exists
	if err := m.EnsureSharedNetwork(ctx); err != nil {
		return err
	}

	// Check if container already exists
	container, err := m.runtime.GetContainer(ctx, containerName)
	if err != nil {
		return fmt.Errorf("failed to check %s container: %w", displayName, err)
	}

	expectedCfg := configFn()

	if container != nil {
		currentLabels, err := m.runtime.GetContainerLabels(ctx, containerName)
		if err != nil {
			return fmt.Errorf("failed to read %s labels: %w", displayName, err)
		}
		if currentLabels[runtime.ConfigHashLabel] != expectedCfg.Labels[runtime.ConfigHashLabel] {
			fmt.Printf("%s config drift detected, recreating...\n", displayName)
			_ = m.runtime.StopContainer(ctx, containerName)
			if err := m.runtime.RemoveContainer(ctx, containerName); err != nil {
				return fmt.Errorf("failed to remove %s container: %w", displayName, err)
			}
		} else if container.Running {
			fmt.Printf("%s is already running\n", displayName)
			return nil
		} else {
			// Config matches, just start the existing container
			fmt.Printf("Starting %s...\n", strings.ToLower(displayName))
			return m.runtime.StartContainer(ctx, containerName)
		}
	}

	// Pull image if needed
	imageExists, err := m.runtime.ImageExists(ctx, image)
	if err != nil {
		return fmt.Errorf("failed to check %s image: %w", displayName, err)
	}

	if !imageExists {
		fmt.Printf("Pulling image %s...\n", image)
		if err := m.runtime.PullImage(ctx, image); err != nil {
			return fmt.Errorf("failed to pull %s image: %w", displayName, err)
		}
	}

	fmt.Printf("Creating %s container...\n", strings.ToLower(displayName))
	if _, err := m.runtime.CreateContainer(ctx, expectedCfg); err != nil {
		return fmt.Errorf("failed to create %s container: %w", displayName, err)
	}

	fmt.Printf("Starting %s...\n", strings.ToLower(displayName))
	if err := m.runtime.StartContainer(ctx, containerName); err != nil {
		return fmt.Errorf("failed to start %s: %w", displayName, err)
	}

	return nil
}

// connectServiceToProject connects a service to a project network with optional aliases
func (m *Manager) connectServiceToProject(ctx context.Context, containerName, displayName, projectNetwork string, statusFn func(context.Context) (*ServiceStatus, error), aliases ...string) error {
	status, err := statusFn(ctx)
	if err != nil {
		return err
	}

	if !status.Running {
		return fmt.Errorf("%s is not running (start with: zdev services start)", strings.ToLower(displayName))
	}

	if err := m.runtime.NetworkConnect(ctx, projectNetwork, containerName, aliases...); err != nil {
		// Ignore "already connected" errors
		errStr := err.Error()
		if strings.Contains(strings.ToLower(errStr), "already exists") || strings.Contains(strings.ToLower(errStr), "already connected") {
			return nil
		}
		return fmt.Errorf("failed to connect %s to network %s: %w", strings.ToLower(displayName), projectNetwork, err)
	}

	return nil
}

// disconnectServiceFromProject disconnects a service from a project network
func (m *Manager) disconnectServiceFromProject(ctx context.Context, containerName, displayName, projectNetwork string) error {
	container, err := m.runtime.GetContainer(ctx, containerName)
	if err != nil {
		return fmt.Errorf("failed to check %s container: %w", displayName, err)
	}

	if container == nil {
		return nil // No container, nothing to disconnect
	}

	return m.runtime.NetworkDisconnect(ctx, projectNetwork, containerName)
}

// =============================================================================
// Router (special handling for ports)
// =============================================================================

// StartRouter starts the Traefik router container.
//
// Ports are aggregated from every registered project's state. The running
// container's config-hash is compared against a freshly built expected
// config; any drift (image, SSL, dashboard, docs, domain, new ports)
// triggers a recreate. To avoid a restart storm whenever one project
// happens to not need a port another already configured, the expected
// port set is the UNION of what the container currently has and what
// state now requires: extra ports in the running router don't force a
// recreate on their own. Intentional port shrinking happens via
// RefreshRouter, which is called when a project is removed.
func (m *Manager) StartRouter(ctx context.Context) error {
	// The router bind-mounts the Docker socket; fail early with guidance if
	// it can't be mounted (e.g. Docker Desktop's default socket is disabled)
	// rather than letting the daemon surface a cryptic mkdir error.
	if err := runtime.CheckDockerSocketMountable(); err != nil {
		return err
	}

	stateMgr, err := state.DefaultManager()
	if err != nil {
		return fmt.Errorf("failed to load state: %w", err)
	}

	tcpPorts, udpPorts, err := stateMgr.GetAllRoutingPorts()
	if err != nil {
		return fmt.Errorf("failed to get routing ports from state: %w", err)
	}

	if err := m.EnsureSharedNetwork(ctx); err != nil {
		return err
	}

	container, err := m.runtime.GetContainer(ctx, RouterContainerName)
	if err != nil {
		return fmt.Errorf("failed to check router container: %w", err)
	}

	if container != nil {
		currentLabels, err := m.runtime.GetContainerLabels(ctx, RouterContainerName)
		if err != nil {
			return fmt.Errorf("failed to read router labels: %w", err)
		}

		// Union current + required so a smaller required set doesn't trigger
		// a recreate - preserves "extra ports are fine" behavior.
		effectiveTCP := unionPortSets(parsePortCSV(currentLabels["zdev.tcp-ports"]), tcpPorts)
		effectiveUDP := unionPortSets(parsePortCSV(currentLabels["zdev.udp-ports"]), udpPorts)
		expectedCfg := m.buildRouterContainerConfig(effectiveTCP, effectiveUDP)

		if currentLabels[runtime.ConfigHashLabel] != expectedCfg.Labels[runtime.ConfigHashLabel] {
			fmt.Println("Router config drift detected, recreating...")
			_ = m.runtime.StopContainer(ctx, RouterContainerName)
			if err := m.runtime.RemoveContainer(ctx, RouterContainerName); err != nil {
				return fmt.Errorf("failed to remove router container: %w", err)
			}
		} else if container.Running {
			fmt.Println("Router is already running")
			return nil
		} else {
			fmt.Println("Starting router...")
			return m.runtime.StartContainer(ctx, RouterContainerName)
		}
	}

	image := m.cfg.Shared.Router.Image
	imageExists, err := m.runtime.ImageExists(ctx, image)
	if err != nil {
		return fmt.Errorf("failed to check router image: %w", err)
	}

	if !imageExists {
		fmt.Printf("Pulling image %s...\n", image)
		if err := m.runtime.PullImage(ctx, image); err != nil {
			return fmt.Errorf("failed to pull router image: %w", err)
		}
	}

	// Recreate with the required (narrower) port set - if we're recreating
	// anyway there's no reason to keep orphaned ports around.
	cfg := m.buildRouterContainerConfig(tcpPorts, udpPorts)

	fmt.Println("Creating router container...")
	if _, err := m.runtime.CreateContainer(ctx, cfg); err != nil {
		return fmt.Errorf("failed to create router container: %w", err)
	}

	fmt.Println("Starting router...")
	if err := m.runtime.StartContainer(ctx, RouterContainerName); err != nil {
		return fmt.Errorf("failed to start router: %w", err)
	}

	return nil
}

// buildRouterContainerConfig assembles the full router container config
// for a given set of TCP/UDP ports, including TLS and docs wiring. It
// has idempotent side effects (EnsureTraefikConfig, EnsureDocsConfig
// create directories on disk if missing) but that's fine - they're safe
// to call on every check.
func (m *Manager) buildRouterContainerConfig(tcpPorts, udpPorts []int) runtime.ContainerConfig {
	routerCfg := RouterConfig{
		Image:      m.cfg.Shared.Router.Image,
		Dashboard:  m.cfg.Shared.Router.Dashboard,
		Domain:     m.cfg.Domain,
		TCPPorts:   tcpPorts,
		UDPPorts:   udpPorts,
		SocketPath: runtime.HostDockerSocketPath(),
	}

	if m.cfg.SSL.Enabled {
		traefikDir, err := config.EnsureTraefikConfig()
		if err != nil {
			fmt.Printf("Warning: failed to configure TLS: %v\n", err)
		} else if traefikDir != "" {
			routerCfg.TLSCertDir = config.GetCertsDir()
			routerCfg.TLSConfigDir = traefikDir
		}
	}

	docsDir, err := config.EnsureDocsConfig(m.cfg.Domain, m.cfg.SSL.Enabled)
	if err != nil {
		fmt.Printf("Warning: failed to configure docs: %v\n", err)
	} else {
		routerCfg.DocsDir = docsDir
		if routerCfg.TLSConfigDir == "" {
			routerCfg.TLSConfigDir = config.GetTraefikConfigDir()
		}
	}

	return RouterContainerConfig(routerCfg)
}

// RefreshRouter rebuilds the router if the ports from state don't match current router
func (m *Manager) RefreshRouter(ctx context.Context) error {
	container, err := m.runtime.GetContainer(ctx, RouterContainerName)
	if err != nil {
		return fmt.Errorf("failed to check router container: %w", err)
	}

	if container == nil || !container.Running {
		return nil // Router not running, nothing to refresh
	}

	stateMgr, err := state.DefaultManager()
	if err != nil {
		return fmt.Errorf("failed to load state: %w", err)
	}

	tcpPorts, udpPorts, err := stateMgr.GetAllRoutingPorts()
	if err != nil {
		return fmt.Errorf("failed to get routing ports from state: %w", err)
	}

	labels, err := m.runtime.GetContainerLabels(ctx, RouterContainerName)
	if err != nil {
		return fmt.Errorf("failed to get router labels: %w", err)
	}

	currentTCP := labels["zdev.tcp-ports"]
	currentUDP := labels["zdev.udp-ports"]
	requiredTCP := intsToString(tcpPorts)
	requiredUDP := intsToString(udpPorts)

	if currentTCP == requiredTCP && currentUDP == requiredUDP {
		return nil
	}

	fmt.Println("Updating router ports...")
	_ = m.runtime.StopContainer(ctx, RouterContainerName)
	if err := m.runtime.RemoveContainer(ctx, RouterContainerName); err != nil {
		return fmt.Errorf("failed to remove router container: %w", err)
	}

	return m.StartRouter(ctx)
}

// parsePortCSV parses the comma-separated port list stored in
// zdev.tcp-ports / zdev.udp-ports labels back into []int. Empty or
// malformed entries are skipped rather than erroring - a drifted label
// just leads to a recreate, which is the fallback behavior we want.
func parsePortCSV(s string) []int {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	ports := make([]int, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		var n int
		if _, err := fmt.Sscanf(p, "%d", &n); err == nil {
			ports = append(ports, n)
		}
	}
	return ports
}

// unionPortSets returns the sorted deduplicated union of two port lists.
func unionPortSets(a, b []int) []int {
	seen := make(map[int]bool, len(a)+len(b))
	out := make([]int, 0, len(a)+len(b))
	for _, p := range a {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, p := range b {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Ints(out)
	return out
}

func (m *Manager) RouterStatus(ctx context.Context) (*ServiceStatus, error) {
	return m.getServiceStatus(ctx, RouterContainerName, "Router")
}

// =============================================================================
// Mail
// =============================================================================

func (m *Manager) StartMail(ctx context.Context) error {
	return m.startService(ctx, MailContainerName, "Mail", m.cfg.Shared.Mail.Image, func() runtime.ContainerConfig {
		return MailContainerConfig(MailServiceConfig{
			Image:      m.cfg.Shared.Mail.Image,
			Domain:     m.cfg.Domain,
			TLSEnabled: m.cfg.SSL.Enabled,
		})
	})
}

func (m *Manager) MailStatus(ctx context.Context) (*ServiceStatus, error) {
	return m.getServiceStatus(ctx, MailContainerName, "Mail")
}

// =============================================================================
// DBUI (Adminer) - special handling for config
// =============================================================================

func (m *Manager) StartDBUI(ctx context.Context) error {
	// Ensure Adminer config (servers.php) exists before starting
	adminerCfgDir, err := EnsureAdminerConfig(ctx)
	if err != nil {
		fmt.Printf("Warning: could not configure Adminer servers: %v\n", err)
		adminerCfgDir = ""
	}

	return m.startService(ctx, DBUIContainerName, "DBUI", m.cfg.Shared.DBUI.Image, func() runtime.ContainerConfig {
		return DBUIContainerConfig(DBUIServiceConfig{
			Image:         m.cfg.Shared.DBUI.Image,
			Domain:        m.cfg.Domain,
			TLSEnabled:    m.cfg.SSL.Enabled,
			AdminerCfgDir: adminerCfgDir,
		})
	})
}

func (m *Manager) DBUIStatus(ctx context.Context) (*ServiceStatus, error) {
	return m.getServiceStatus(ctx, DBUIContainerName, "DBUI")
}

// =============================================================================
// Redis Insights
// =============================================================================

func (m *Manager) StartRedisInsights(ctx context.Context) error {
	return m.startService(ctx, RedisInsightsContainerName, "RedisInsights", m.cfg.Shared.RedisInsights.Image, func() runtime.ContainerConfig {
		return RedisInsightsContainerConfig(RedisInsightsServiceConfig{
			Image:      m.cfg.Shared.RedisInsights.Image,
			Domain:     m.cfg.Domain,
			TLSEnabled: m.cfg.SSL.Enabled,
		})
	})
}

func (m *Manager) RedisInsightsStatus(ctx context.Context) (*ServiceStatus, error) {
	return m.getServiceStatus(ctx, RedisInsightsContainerName, "RedisInsights")
}

// =============================================================================
// Logs (Dozzle)
// =============================================================================

func (m *Manager) StartLogs(ctx context.Context) error {
	// Dozzle bind-mounts the Docker socket; fail early with guidance if it
	// can't be mounted (e.g. Docker Desktop's default socket is disabled).
	if err := runtime.CheckDockerSocketMountable(); err != nil {
		return err
	}
	return m.startService(ctx, LogsContainerName, "Logs", m.cfg.Shared.Logs.Image, func() runtime.ContainerConfig {
		return LogsContainerConfig(LogsServiceConfig{
			Image:      m.cfg.Shared.Logs.Image,
			Domain:     m.cfg.Domain,
			TLSEnabled: m.cfg.SSL.Enabled,
			Shell:      m.cfg.Shared.Logs.Shell,
			SocketPath: runtime.HostDockerSocketPath(),
		})
	})
}

func (m *Manager) LogsStatus(ctx context.Context) (*ServiceStatus, error) {
	return m.getServiceStatus(ctx, LogsContainerName, "Logs")
}
