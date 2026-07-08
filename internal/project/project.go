package project

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/mutagen"
	"github.com/0ploy/zdev/internal/runtime"
	"github.com/0ploy/zdev/internal/secrets"
	"github.com/0ploy/zdev/internal/services"
	"github.com/0ploy/zdev/internal/state"
)

// Project represents a loaded zdev project
type Project struct {
	Dir     string
	Config  *config.ProjectConfig
	Runtime runtime.Runtime

	// Secrets resolves op:// references in service env at container
	// creation time. Nil falls back to the 1Password CLI resolver.
	Secrets secrets.Resolver

	// BuildMode controls when service images with a `dockerfile:` config
	// are rebuilt. Set from the --build / --no-build flags; zero value is
	// BuildIfStale.
	BuildMode BuildMode

	// RefreshSecrets makes Update re-resolve op:// references and
	// recreate services whose resolved values changed since creation.
	// Set from the --refresh-secrets flag.
	RefreshSecrets bool
}

// ExecOptions contains options for executing a command in a container
type ExecOptions struct {
	User    string // Username or UID to run command as
	Workdir string // Working directory inside the container
}

// Load finds and loads the project from the current directory
func Load() (*Project, error) {
	dir, err := config.FindProjectDir()
	if err != nil {
		return nil, err
	}

	return LoadFromDir(dir)
}

// LoadFromDir loads a project from a specific directory
func LoadFromDir(dir string) (*Project, error) {
	cfg, err := config.LoadProject(dir)
	if err != nil {
		return nil, err
	}

	return &Project{
		Dir:     dir,
		Config:  cfg,
		Runtime: runtime.NewDockerCLI(),
		Secrets: secrets.NewOnePasswordCLI(),
	}, nil
}

// ContainerNameFor returns the full container name for a service in a given project.
// Format: <service>.<project>.zdev (e.g., app.myproject.zdev)
// This standalone function can be used without a loaded Project.
func ContainerNameFor(service, projectName string) string {
	return fmt.Sprintf("%s.%s.zdev", service, projectName)
}

// ContainerName returns the full container name for a service
// Format: <service>.<project>.zdev (e.g., app.myproject.zdev)
func (p *Project) ContainerName(service string) string {
	return ContainerNameFor(service, p.Config.Name)
}

// NetworkName returns the project network name
// Format: <project>.zdev (e.g., myproject.zdev)
func (p *Project) NetworkName() string {
	return fmt.Sprintf("%s.zdev", p.Config.Name)
}

// shellQuote wraps a string in single quotes, escaping any embedded single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// VolumeName returns the full volume name for a project volume
// Format: <volume>.<project>.zdev (e.g., db_data.myproject.zdev)
func (p *Project) VolumeName(volume string) string {
	return fmt.Sprintf("%s.%s.zdev", volume, p.Config.Name)
}

// NamedVolumes returns all named volumes discovered from service volume mounts.
func (p *Project) NamedVolumes() []string {
	seen := make(map[string]bool)
	var names []string
	for _, svc := range p.Config.Services {
		for _, vol := range svc.Volumes {
			source, _, isNamed := parseVolumeMount(vol)
			if isNamed && !seen[source] {
				seen[source] = true
				names = append(names, source)
			}
		}
	}
	return names
}

// ContainerStatus returns the status of a container: "running", "stopped", "not created", or "unknown"
func (p *Project) ContainerStatus(ctx context.Context, containerName string) string {
	exists, err := p.Runtime.ContainerExists(ctx, containerName)
	if err != nil || !exists {
		return "not created"
	}

	running, err := p.Runtime.IsContainerRunning(ctx, containerName)
	if err != nil {
		return "unknown"
	}

	if running {
		return "running"
	}
	return "stopped"
}

// isTLSAvailable checks if TLS is enabled in config and certs exist
func isTLSAvailable() bool {
	globalCfg, err := config.LoadGlobalConfig()
	if err != nil || !globalCfg.SSL.Enabled {
		return false
	}

	// Check if certs exist
	certsDir := config.GetCertsDir()
	certPath := filepath.Join(certsDir, "cert.pem")
	keyPath := filepath.Join(certsDir, "key.pem")

	if _, err := os.Stat(certPath); err != nil {
		return false
	}
	if _, err := os.Stat(keyPath); err != nil {
		return false
	}

	return true
}

// parseVolumeMount parses a volume string like "db_data:/var/lib/data" or "/host/path:/container/path"
// Returns (source, target, isNamedVolume)
func parseVolumeMount(volume string) (source, target string, isNamedVolume bool) {
	parts := strings.SplitN(volume, ":", 2)
	if len(parts) != 2 {
		return volume, volume, false
	}

	source = parts[0]
	target = parts[1]

	// If source starts with / or . it's a bind mount, otherwise it's a named volume
	isNamedVolume = !strings.HasPrefix(source, "/") && !strings.HasPrefix(source, ".")

	return source, target, isNamedVolume
}

// detectConfigRename checks whether `.zdev/config.yaml`'s `name:` was edited
// without going through `zdev rename`. The state file is keyed by name and
// stores the project path; if any other entry shares this directory, the user
// renamed via config edit and we need to bail out before the port-ownership
// check produces a misleading "port already used by project X" error.
// Returns the previously registered name, or "" if there's no mismatch.
func (p *Project) detectConfigRename() (string, error) {
	stateMgr, err := state.DefaultManager()
	if err != nil {
		return "", fmt.Errorf("failed to load state: %w", err)
	}
	projects, err := stateMgr.ListProjects()
	if err != nil {
		return "", fmt.Errorf("failed to list projects: %w", err)
	}
	for name, entry := range projects {
		if entry.Path == p.Dir && name != p.Config.Name {
			return name, nil
		}
	}
	return "", nil
}

// errConfigRenameDetected formats a guidance error for the case where the user
// edited the project name in config without running `zdev rename`.
func errConfigRenameDetected(registeredName, configName string) error {
	return fmt.Errorf("project name in .zdev/config.yaml is %q but this directory is registered as %q.\n"+
		"Renaming a project requires migrating containers, volumes, network, and state.\n"+
		"To rename safely: revert the name in .zdev/config.yaml back to %q, then run:\n"+
		"    zdev rename %s",
		configName, registeredName, registeredName, configName)
}

// checkPortAvailability checks if all configured routing ports are available.
// If services is non-empty, only those services' ports are checked.
func (p *Project) checkPortAvailability(ctx context.Context, services map[string]bool) error {
	if !p.Config.Shared.Router {
		return nil // No routing ports to check if not using shared router
	}

	stateMgr, err := state.DefaultManager()
	if err != nil {
		return fmt.Errorf("failed to load state: %w", err)
	}

	for serviceName, svc := range p.Config.Services {
		if len(services) > 0 && !services[serviceName] {
			continue
		}
		if svc.Routing == nil || svc.Routing.HostPort == 0 {
			continue
		}

		port := svc.Routing.HostPort
		protocol := svc.Routing.Protocol

		// Check state file for port ownership
		var owner string
		if protocol == "tcp" {
			owner, err = stateMgr.GetTCPPortOwner(port)
		} else if protocol == "udp" {
			owner, err = stateMgr.GetUDPPortOwner(port)
		}
		if err != nil {
			return fmt.Errorf("failed to check port ownership: %w", err)
		}

		// If owned by current project, it's OK (restart scenario)
		if owner == p.Config.Name {
			continue
		}

		// If owned by another project, give specific error
		if owner != "" {
			return fmt.Errorf("service %s: port %d is already used by project '%s'\nStop that project or choose a different host_port",
				serviceName, port, owner)
		}

		// Not owned by any project - check if port is available on host
		// (could be used by another Docker container or system service)
		hostPort := fmt.Sprintf("0.0.0.0:%d", port)
		if !isPortAvailable(hostPort) {
			return fmt.Errorf("service %s: port %d is already in use on your system\nStop the process using this port or choose a different host_port",
				serviceName, port)
		}
	}
	return nil
}


// isPortAvailable checks if a port is available for binding
func isPortAvailable(hostPort string) bool {
	ln, err := net.Listen("tcp", hostPort)
	if err != nil {
		return false
	}
	ln.Close()
	return true
}

// Start starts all project services
func (p *Project) Start(ctx context.Context) error {
	return p.start(ctx, nil)
}

// StartService starts a single project service. Project-wide setup
// (network, volumes, state registration, shared service connections) runs
// idempotently, so calling this on a never-started project still works.
func (p *Project) StartService(ctx context.Context, name string) error {
	if _, ok := p.Config.Services[name]; !ok {
		return fmt.Errorf("service %q not found in project config (available: %s)", name, strings.Join(p.ServiceNames(), ", "))
	}
	return p.start(ctx, map[string]bool{name: true})
}

// start is the shared implementation behind Start and StartService.
// When filter is nil all services run; otherwise only services whose names
// are keys with a true value are started. Port checks, Mutagen finalization,
// and state/shared/link wiring all respect the same filter so single-service
// starts don't touch unrelated state.
func (p *Project) start(ctx context.Context, filter map[string]bool) error {
	if registered, err := p.detectConfigRename(); err != nil {
		return err
	} else if registered != "" {
		return errConfigRenameDetected(registered, p.Config.Name)
	}

	// Check port availability before starting anything
	if err := p.checkPortAvailability(ctx, filter); err != nil {
		return err
	}

	m, mutagenMounts, mutagenMountMap, err := p.prepareMutagen(ctx)
	if err != nil {
		return err
	}
	mutagenEnabled := m != nil

	// Create project network if it doesn't exist
	networkName := p.NetworkName()
	networkExists, err := p.Runtime.NetworkExists(ctx, networkName)
	if err != nil {
		return fmt.Errorf("failed to check network: %w", err)
	}

	if !networkExists {
		fmt.Printf("Creating network %s...\n", networkName)
		if err := p.Runtime.CreateNetwork(ctx, networkName); err != nil {
			return fmt.Errorf("failed to create network: %w", err)
		}
	}

	// Create project volumes if they don't exist
	for _, volumeName := range p.NamedVolumes() {
		fullName := p.VolumeName(volumeName)
		exists, err := p.Runtime.VolumeExists(ctx, fullName)
		if err != nil {
			return fmt.Errorf("failed to check volume %s: %w", volumeName, err)
		}
		if !exists {
			fmt.Printf("Creating volume %s...\n", fullName)
			if err := p.Runtime.CreateVolume(ctx, fullName); err != nil {
				return fmt.Errorf("failed to create volume %s: %w", volumeName, err)
			}
		}
	}

	// Start services (filtered)
	for serviceName, serviceCfg := range p.Config.Services {
		if len(filter) > 0 && !filter[serviceName] {
			continue
		}
		if err := p.startServiceWithMutagen(ctx, serviceName, serviceCfg, mutagenEnabled, mutagenMountMap); err != nil {
			return fmt.Errorf("failed to start service %s: %w", serviceName, err)
		}
	}

	// Only finalize Mutagen sessions for the services that actually started,
	// so single-service starts don't touch sessions tied to other services.
	finalizedMounts := mutagenMounts
	if len(filter) > 0 {
		finalizedMounts = finalizedMounts[:0]
		for _, mount := range mutagenMounts {
			if filter[mount.ServiceName] {
				finalizedMounts = append(finalizedMounts, mount)
			}
		}
	}
	if err := p.finalizeMutagen(ctx, m, finalizedMounts); err != nil {
		return err
	}

	// Register project with routing info in state
	tcpPorts, udpPorts := p.GetRequiredPorts()
	stateMgr, err := state.DefaultManager()
	if err != nil {
		return fmt.Errorf("failed to load state: %w", err)
	}
	if err := stateMgr.RegisterProjectWithRouting(p.Config.Name, p.Dir, tcpPorts, udpPorts); err != nil {
		return fmt.Errorf("failed to register project: %w", err)
	}

	// Connect shared services
	p.connectEnabledSharedServices(ctx)

	// Connect to link networks
	p.connectLinks(ctx)

	return nil
}

// startServiceWithMutagen starts a service with optional Mutagen volume transformation
func (p *Project) startServiceWithMutagen(ctx context.Context, name string, svc config.ServiceConfig, mutagenEnabled bool, mutagenMounts map[string]MutagenSyncMount) error {
	containerName := p.ContainerName(name)

	// Build the image first when a `dockerfile:` config is present, so an
	// existing container can be recreated to pick up a rebuilt image.
	rebuilt := false
	if svc.Dockerfile != "" {
		var err error
		rebuilt, err = p.ensureBuiltImage(ctx, name, svc)
		if err != nil {
			return err
		}
	}

	// Check if container already exists
	exists, err := p.Runtime.ContainerExists(ctx, containerName)
	if err != nil {
		return err
	}

	if exists && rebuilt {
		// The image was rebuilt under the container - recreate it so the
		// running service actually uses the new image.
		fmt.Printf("Recreating service %s with rebuilt image...\n", name)
		running, err := p.Runtime.IsContainerRunning(ctx, containerName)
		if err != nil {
			return err
		}
		if running {
			if err := p.Runtime.StopContainer(ctx, containerName); err != nil {
				return err
			}
		}
		if err := p.Runtime.RemoveContainer(ctx, containerName); err != nil {
			return err
		}
	} else if exists {
		running, err := p.Runtime.IsContainerRunning(ctx, containerName)
		if err != nil {
			return err
		}

		if running {
			fmt.Printf("Service %s is already running\n", name)
			return nil
		}

		fmt.Printf("Starting service %s...\n", name)
		return p.Runtime.StartContainer(ctx, containerName)
	}

	// Pull image if needed (built images are guaranteed present here)
	if svc.Dockerfile == "" {
		imageExists, err := p.Runtime.ImageExists(ctx, svc.Image)
		if err != nil {
			return err
		}

		if !imageExists {
			fmt.Printf("Pulling image %s...\n", svc.Image)
			if err := p.Runtime.PullImage(ctx, svc.Image); err != nil {
				return err
			}
		}
	}

	// Build container config (single source of truth)
	cfg := p.buildContainerConfig(name, svc, mutagenEnabled, mutagenMounts)

	// Resolve op:// secret references AFTER the config hash is stamped
	// (hash covers the unresolved refs, so the update compare path never
	// needs 1Password) and before the container is created.
	if err := p.resolveSecretEnv(ctx, name, &cfg); err != nil {
		return err
	}

	// Create and start
	fmt.Printf("Creating service %s...\n", name)
	if _, err := p.Runtime.CreateContainer(ctx, cfg); err != nil {
		return err
	}

	fmt.Printf("Starting service %s...\n", name)
	return p.Runtime.StartContainer(ctx, containerName)
}

// StopService stops a single service container. Pauses only the Mutagen
// sessions tied to that service. Other services in the project remain
// running. To start it back up, use `zdev start <service>` (or `zdev
// restart <service>` for an in-place bounce of an existing container).
func (p *Project) StopService(ctx context.Context, name string) error {
	if _, ok := p.Config.Services[name]; !ok {
		return fmt.Errorf("service %q not found in project config (available: %s)", name, strings.Join(p.ServiceNames(), ", "))
	}

	containerName := p.ContainerName(name)
	exists, err := p.Runtime.ContainerExists(ctx, containerName)
	if err != nil {
		return err
	}
	if !exists {
		fmt.Printf("Service %s is not running\n", name)
		return nil
	}

	if p.IsMutagenEnabled() {
		if m, err := p.EnsureMutagen(ctx); err == nil {
			for _, mount := range p.GetMutagenSyncMounts() {
				if mount.ServiceName != name {
					continue
				}
				if existsSess, _ := m.SessionExists(ctx, mount.SessionName); existsSess {
					fmt.Printf("Pausing sync session %s...\n", mount.SessionName)
					if err := m.PauseSession(ctx, mount.SessionName); err != nil {
						fmt.Printf("Warning: could not pause session %s: %v\n", mount.SessionName, err)
					}
				}
			}
		}
	}

	running, err := p.Runtime.IsContainerRunning(ctx, containerName)
	if err != nil {
		return err
	}
	if !running {
		fmt.Printf("Service %s is not running\n", name)
		return nil
	}

	fmt.Printf("Stopping service %s...\n", name)
	if err := p.Runtime.StopContainer(ctx, containerName); err != nil {
		return fmt.Errorf("failed to stop service %s: %w", name, err)
	}
	return nil
}

// Stop stops all project services
func (p *Project) Stop(ctx context.Context) error {
	// Pause Mutagen sync sessions first (before stopping containers)
	if p.IsMutagenEnabled() {
		p.pauseMutagenSessions(ctx)
	}

	for serviceName := range p.Config.Services {
		containerName := p.ContainerName(serviceName)

		exists, err := p.Runtime.ContainerExists(ctx, containerName)
		if err != nil {
			return err
		}

		if !exists {
			continue
		}

		running, err := p.Runtime.IsContainerRunning(ctx, containerName)
		if err != nil {
			return err
		}

		if !running {
			fmt.Printf("Service %s is not running\n", serviceName)
			continue
		}

		fmt.Printf("Stopping service %s...\n", serviceName)
		if err := p.Runtime.StopContainer(ctx, containerName); err != nil {
			return fmt.Errorf("failed to stop service %s: %w", serviceName, err)
		}
	}
	return nil
}

// teardownContainers disconnects Mutagen, links, and shared services, then
// stops and removes every service container for this project. Both `Down`
// and `Rename` use this as their common first phase; their post-teardown
// steps (remove network/volumes/state vs migrate volumes) differ and live
// in the caller.
func (p *Project) teardownContainers(ctx context.Context) error {
	// Terminate Mutagen sync sessions first (before removing containers)
	if p.IsMutagenEnabled() {
		p.terminateMutagenSessions(ctx)
	}

	// Disconnect from link networks (before removing containers)
	p.disconnectLinks(ctx)

	// Disconnect shared services (do this first, before removing network)
	p.disconnectEnabledSharedServices(ctx)

	for serviceName := range p.Config.Services {
		containerName := p.ContainerName(serviceName)

		exists, err := p.Runtime.ContainerExists(ctx, containerName)
		if err != nil {
			return err
		}
		if !exists {
			continue
		}

		running, err := p.Runtime.IsContainerRunning(ctx, containerName)
		if err != nil {
			return err
		}
		if running {
			fmt.Printf("Stopping service %s...\n", serviceName)
			if err := p.Runtime.StopContainer(ctx, containerName); err != nil {
				return fmt.Errorf("failed to stop service %s: %w", serviceName, err)
			}
		}

		fmt.Printf("Removing service %s...\n", serviceName)
		if err := p.Runtime.RemoveContainer(ctx, containerName); err != nil {
			return fmt.Errorf("failed to remove service %s: %w", serviceName, err)
		}
	}
	return nil
}

// Down stops and removes all project containers and the network.
// If removeVolumes is true, also removes volumes.
func (p *Project) Down(ctx context.Context, removeVolumes bool) error {
	if err := p.teardownContainers(ctx); err != nil {
		return err
	}

	networkName := p.NetworkName()
	networkExists, err := p.Runtime.NetworkExists(ctx, networkName)
	if err != nil {
		return fmt.Errorf("failed to check network: %w", err)
	}

	if networkExists {
		fmt.Printf("Removing network %s...\n", networkName)
		if err := p.Runtime.RemoveNetwork(ctx, networkName); err != nil {
			return fmt.Errorf("failed to remove network: %w", err)
		}
	}

	// Remove volumes if requested
	if removeVolumes {
		// Remove project volumes
		for _, volumeName := range p.NamedVolumes() {
			fullName := p.VolumeName(volumeName)
			exists, err := p.Runtime.VolumeExists(ctx, fullName)
			if err != nil {
				return fmt.Errorf("failed to check volume %s: %w", volumeName, err)
			}

			if exists {
				fmt.Printf("Removing volume %s...\n", fullName)
				if err := p.Runtime.RemoveVolume(ctx, fullName); err != nil {
					return fmt.Errorf("failed to remove volume %s: %w", volumeName, err)
				}
			}
		}

		// Remove Mutagen sync volumes
		if p.IsMutagenEnabled() {
			p.removeMutagenVolumes(ctx)
		}
	}

	// Unregister from global state
	stateMgr, err := state.DefaultManager()
	if err == nil {
		_ = stateMgr.UnregisterProject(p.Config.Name)
	}

	// Refresh router to release any TCP/UDP ports this project was using
	if p.Config.Shared.Router {
		globalCfg, err := config.LoadGlobalConfig()
		if err == nil {
			mgr := services.NewManager(globalCfg)
			_ = mgr.RefreshRouter(ctx)
		}
	}

	return nil
}

// Update checks for config changes and recreates containers as needed
// Returns true if any changes were made
func (p *Project) Update(ctx context.Context) (bool, error) {
	if registered, err := p.detectConfigRename(); err != nil {
		return false, err
	} else if registered != "" {
		return false, errConfigRenameDetected(registered, p.Config.Name)
	}

	// Check port availability for new ports
	if err := p.checkPortAvailability(ctx, nil); err != nil {
		return false, err
	}

	// Ensure network exists
	networkName := p.NetworkName()
	networkExists, err := p.Runtime.NetworkExists(ctx, networkName)
	if err != nil {
		return false, fmt.Errorf("failed to check network: %w", err)
	}

	if !networkExists {
		// Project not started yet, just run Start
		return true, p.Start(ctx)
	}

	// Ensure volumes exist
	for _, volumeName := range p.NamedVolumes() {
		fullName := p.VolumeName(volumeName)
		exists, err := p.Runtime.VolumeExists(ctx, fullName)
		if err != nil {
			return false, fmt.Errorf("failed to check volume %s: %w", volumeName, err)
		}
		if !exists {
			fmt.Printf("Creating volume %s...\n", fullName)
			if err := p.Runtime.CreateVolume(ctx, fullName); err != nil {
				return false, fmt.Errorf("failed to create volume %s: %w", volumeName, err)
			}
		}
	}

	// Mutagen state is prepared lazily on first need: a no-op `zdev update`
	// (no service drifted) shouldn't pay the daemon-startup + volume-create
	// cost. `serviceNeedsRecreate` does its own lightweight mount discovery
	// (no daemon) for the hash compare, so the diff stays cheap.
	//
	// Once prepared, the same context is reused for every recreated service
	// in this run. Without this discipline, a recreated container is built
	// with `mutagenEnabled=false` and silently swaps the named sync volume
	// for a raw bind mount, dropping anything in the Mutagen ignore list
	// (vendor/, .setup-complete) that lived only inside the volume.
	var (
		mutagenPrepared bool
		mDaemon         *mutagen.Mutagen
		mutagenMounts   []MutagenSyncMount
		mutagenMountMap map[string]MutagenSyncMount
	)
	prepare := func() error {
		if mutagenPrepared {
			return nil
		}
		daemon, mounts, mountMap, err := p.prepareMutagen(ctx)
		if err != nil {
			return err
		}
		mDaemon = daemon
		mutagenMounts = mounts
		mutagenMountMap = mountMap
		mutagenPrepared = true
		return nil
	}
	startService := func(serviceName string, svc config.ServiceConfig) error {
		if err := prepare(); err != nil {
			return err
		}
		return p.startServiceWithMutagen(ctx, serviceName, svc, mDaemon != nil, mutagenMountMap)
	}

	updated := false

	// Check each service for changes
	for serviceName, svc := range p.Config.Services {
		containerName := p.ContainerName(serviceName)

		exists, err := p.Runtime.ContainerExists(ctx, containerName)
		if err != nil {
			return false, err
		}

		if !exists {
			// Container doesn't exist, create it
			fmt.Printf("Creating service %s...\n", serviceName)
			if err := startService(serviceName, svc); err != nil {
				return false, fmt.Errorf("failed to start service %s: %w", serviceName, err)
			}
			updated = true
			continue
		}

		// Check if container needs recreation
		needsRecreate, err := p.serviceNeedsRecreate(ctx, serviceName, svc)
		if err != nil {
			return false, err
		}

		// A stale `dockerfile:` image counts as a change too - the tag stays
		// the same so the config hash can't see it. The rebuild itself
		// happens on the recreate path (startService -> ensureBuiltImage).
		if !needsRecreate {
			stale, err := p.serviceBuildStale(ctx, serviceName, svc)
			if err != nil {
				return false, err
			}
			needsRecreate = stale
		}

		// --refresh-secrets: resolve op:// refs fresh and recreate only
		// when the values actually rotated (compared via the stamped
		// secrets hash). Without the flag secrets stay untouched.
		if !needsRecreate && p.RefreshSecrets {
			stale, err := p.serviceSecretsStale(ctx, serviceName, svc)
			if err != nil {
				return false, err
			}
			if !stale && serviceUsesSecretRefs(p.Config.Environment, svc) {
				fmt.Printf("Secrets unchanged for service %s\n", serviceName)
			}
			needsRecreate = stale
		}

		if needsRecreate {
			fmt.Printf("Recreating service %s...\n", serviceName)

			// Stop and remove old container
			running, _ := p.Runtime.IsContainerRunning(ctx, containerName)
			if running {
				if err := p.Runtime.StopContainer(ctx, containerName); err != nil {
					return false, fmt.Errorf("failed to stop service %s: %w", serviceName, err)
				}
			}
			if err := p.Runtime.RemoveContainer(ctx, containerName); err != nil {
				return false, fmt.Errorf("failed to remove service %s: %w", serviceName, err)
			}

			// Create new container
			if err := startService(serviceName, svc); err != nil {
				return false, fmt.Errorf("failed to start service %s: %w", serviceName, err)
			}
			updated = true
		} else {
			// Ensure container is running
			running, _ := p.Runtime.IsContainerRunning(ctx, containerName)
			if !running {
				fmt.Printf("Starting service %s...\n", serviceName)
				if err := p.Runtime.StartContainer(ctx, containerName); err != nil {
					return false, fmt.Errorf("failed to start service %s: %w", serviceName, err)
				}
			}
		}
	}

	// Always run prepare+finalize so Mutagen-only changes (per-service
	// owner/group/mode tweaks) are picked up even on otherwise-no-op updates.
	// Both calls are no-ops when Mutagen is disabled globally or the project
	// has no sync mounts, so the cost on irrelevant projects is negligible.
	if err := prepare(); err != nil {
		return updated, err
	}
	if mutagenPrepared {
		if err := p.finalizeMutagen(ctx, mDaemon, mutagenMounts); err != nil {
			return updated, err
		}
	}

	// Update state with current routing info
	tcpPorts, udpPorts := p.GetRequiredPorts()
	stateMgr, err := state.DefaultManager()
	if err != nil {
		return updated, fmt.Errorf("failed to load state: %w", err)
	}
	if err := stateMgr.RegisterProjectWithRouting(p.Config.Name, p.Dir, tcpPorts, udpPorts); err != nil {
		return updated, fmt.Errorf("failed to register project: %w", err)
	}

	// Connect shared services
	p.connectEnabledSharedServices(ctx)

	// Connect to link networks
	p.connectLinks(ctx)

	return updated, nil
}

// serviceNeedsRecreate checks if a service container needs to be recreated.
// It compares the configHashLabel stamped at creation time against the hash
// of the freshly built expected config. Any drift in image, env, volumes,
// command, working dir, routing labels, or ports triggers recreation.
// Containers created before the hash label existed have no label and will
// be recreated on the next update - an intentional one-time migration.
func (p *Project) serviceNeedsRecreate(ctx context.Context, serviceName string, svc config.ServiceConfig) (bool, error) {
	containerName := p.ContainerName(serviceName)

	currentLabels, err := p.Runtime.GetContainerLabels(ctx, containerName)
	if err != nil {
		return true, nil // If we can't read labels, recreate to be safe
	}

	mutagenEnabled := p.IsMutagenEnabled()
	var mutagenMountMap map[string]MutagenSyncMount
	if mutagenEnabled {
		mutagenMountMap = make(map[string]MutagenSyncMount)
		for _, mount := range p.GetMutagenSyncMounts() {
			mutagenMountMap[mount.ServiceName] = mount
		}
	}
	expectedCfg := p.buildContainerConfig(serviceName, svc, mutagenEnabled, mutagenMountMap)

	return currentLabels[runtime.ConfigHashLabel] != expectedCfg.Labels[runtime.ConfigHashLabel], nil
}

// buildContainerConfig builds the full container configuration for a service.
// This is the single source of truth for container config - used by both
// startServiceWithMutagen (for creating containers) and serviceNeedsRecreate
// (for comparing against running containers).
func (p *Project) buildContainerConfig(name string, svc config.ServiceConfig, mutagenEnabled bool, mutagenMounts map[string]MutagenSyncMount) runtime.ContainerConfig {
	containerName := p.ContainerName(name)

	cfg := runtime.ContainerConfig{
		Name:        containerName,
		Image:       p.serviceImage(name, svc),
		WorkingDir:  svc.WorkingDir,
		NetworkName: p.NetworkName(),
		Aliases:     []string{name},
		Env:         make(map[string]string),
		Labels: map[string]string{
			"zdev.managed": "true",
			"zdev.project": p.Config.Name,
			"zdev.service": name,
		},
	}

	// Merge global environment first
	for k, v := range p.Config.Environment {
		cfg.Env[k] = v
	}

	// Then service-specific environment (overrides global)
	for k, v := range svc.Environment {
		cfg.Env[k] = v
	}

	// Add USER_ID and GROUP_ID for bind mount permission handling
	if _, exists := cfg.Env["USER_ID"]; !exists {
		cfg.Env["USER_ID"] = fmt.Sprintf("%d", os.Getuid())
	}
	if _, exists := cfg.Env["GROUP_ID"]; !exists {
		cfg.Env["GROUP_ID"] = fmt.Sprintf("%d", os.Getgid())
	}

	// Opt-in to the shared Dozzle log viewer: stamp the visibility-filter
	// label and per-project group only when shared.logs is enabled in the
	// project config. Without these labels, Dozzle's DOZZLE_FILTER hides
	// the container entirely - so projects that don't opt in stay out of
	// Dozzle's UI even though they share the host's Docker daemon.
	if p.Config.Shared.Logs {
		cfg.Labels[services.DozzleVisibilityLabel] = "true"
		cfg.Labels[services.DozzleGroupLabel] = p.Config.Name
	}

	// Add any explicit labels from config (before routing, so routing labels take precedence)
	for k, v := range svc.Labels {
		cfg.Labels[k] = v
	}

	// Configure routing if specified (after user labels, so routing wins on conflict)
	if svc.Routing != nil && p.Config.Shared.Router {
		p.configureRouting(&cfg, name, svc.Routing, isTLSAvailable())
	}

	// Parse and add volume mounts, transforming for Mutagen if enabled
	if mutagenEnabled && mutagenMounts != nil {
		cfg.Volumes = p.transformVolumesForMutagen(name, svc.Volumes, mutagenMounts)
	} else {
		for _, vol := range svc.Volumes {
			source, target, isNamedVolume := parseVolumeMount(vol)
			if isNamedVolume {
				source = p.VolumeName(source)
			}
			cfg.Volumes = append(cfg.Volumes, runtime.VolumeMount{
				Source: source,
				Target: target,
			})
		}
	}

	// Parse command
	if svc.Command != "" {
		// When Mutagen is enabled for this service, wrap with sync-ready gate
		_, hasMutagenMount := mutagenMounts[name]
		if mutagenEnabled && hasMutagenMount {
			cfg.Command = []string{"sh", "-c",
				"while [ ! -f /.zdev-sync-ready ]; do sleep 0.2; done; exec sh -c " + shellQuote(svc.Command),
			}
		} else {
			cfg.Command = []string{"sh", "-c", svc.Command}
		}
	}

	// Stamp a deterministic hash of the final config so serviceNeedsRecreate
	// can detect any drift (image, env, volumes, command, routing, etc.) with
	// a single label compare. Computed last so it covers everything above.
	runtime.StampConfigHash(&cfg)

	return cfg
}

// Exec runs a command in a service container
func (p *Project) Exec(ctx context.Context, service string, command []string, interactive bool, opts ExecOptions) error {
	// Verify service exists
	if _, ok := p.Config.Services[service]; !ok {
		return fmt.Errorf("unknown service: %s", service)
	}

	containerName := p.ContainerName(service)

	// Check if running
	running, err := p.Runtime.IsContainerRunning(ctx, containerName)
	if err != nil {
		return err
	}

	if !running {
		return fmt.Errorf("service %s is not running", service)
	}

	runtimeOpts := runtime.ExecOptions{
		User:    opts.User,
		Workdir: opts.Workdir,
	}

	return p.Runtime.Exec(ctx, containerName, command, interactive, runtimeOpts)
}

// LogsOptions configures log output behavior
type LogsOptions struct {
	Follow bool // Stream logs in real-time
	Tail   int  // Number of lines to show from end (0 = all)
}

// Logs streams logs from a service container
func (p *Project) Logs(ctx context.Context, service string, opts LogsOptions) error {
	// Verify service exists
	if _, ok := p.Config.Services[service]; !ok {
		return fmt.Errorf("unknown service: %s", service)
	}

	containerName := p.ContainerName(service)

	// Check if container exists
	exists, err := p.Runtime.ContainerExists(ctx, containerName)
	if err != nil {
		return err
	}

	if !exists {
		return fmt.Errorf("service %s container does not exist - run 'zdev start' first", service)
	}

	runtimeOpts := runtime.LogsOptions{
		Follow: opts.Follow,
		Tail:   opts.Tail,
	}

	return p.Runtime.Logs(ctx, containerName, runtimeOpts)
}

// Restart stops and starts the project
func (p *Project) Restart(ctx context.Context) error {
	if err := p.Stop(ctx); err != nil {
		return fmt.Errorf("failed to stop: %w", err)
	}

	if err := p.Start(ctx); err != nil {
		return fmt.Errorf("failed to start: %w", err)
	}

	return nil
}

// RestartService bounces a single service container in-place. Skips the
// project-wide setup (network, volumes, state, shared services) since
// those are assumed to already be in place from a prior `zdev start`.
// Pauses and resumes only the Mutagen sync sessions tied to this service.
// To pick up config changes, use `zdev update` instead.
func (p *Project) RestartService(ctx context.Context, name string) error {
	if _, ok := p.Config.Services[name]; !ok {
		return fmt.Errorf("service %q not found in project config (available: %s)", name, strings.Join(p.ServiceNames(), ", "))
	}

	containerName := p.ContainerName(name)
	exists, err := p.Runtime.ContainerExists(ctx, containerName)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("service %s container does not exist - run 'zdev start' first", name)
	}

	var serviceMounts []MutagenSyncMount
	if p.IsMutagenEnabled() {
		for _, m := range p.GetMutagenSyncMounts() {
			if m.ServiceName == name {
				serviceMounts = append(serviceMounts, m)
			}
		}
	}

	if len(serviceMounts) > 0 {
		if m, err := p.EnsureMutagen(ctx); err == nil {
			for _, mount := range serviceMounts {
				if existsSess, _ := m.SessionExists(ctx, mount.SessionName); existsSess {
					fmt.Printf("Pausing sync session %s...\n", mount.SessionName)
					if err := m.PauseSession(ctx, mount.SessionName); err != nil {
						fmt.Printf("Warning: could not pause session %s: %v\n", mount.SessionName, err)
					}
				}
			}
		}
	}

	running, err := p.Runtime.IsContainerRunning(ctx, containerName)
	if err != nil {
		return err
	}
	if running {
		fmt.Printf("Stopping service %s...\n", name)
		if err := p.Runtime.StopContainer(ctx, containerName); err != nil {
			return fmt.Errorf("failed to stop service %s: %w", name, err)
		}
	}

	fmt.Printf("Starting service %s...\n", name)
	if err := p.Runtime.StartContainer(ctx, containerName); err != nil {
		return fmt.Errorf("failed to start service %s: %w", name, err)
	}

	if len(serviceMounts) > 0 {
		if m, err := p.EnsureMutagen(ctx); err == nil {
			if err := p.finalizeMutagen(ctx, m, serviceMounts); err != nil {
				return err
			}
		}
	}

	return nil
}

// ServiceNames returns a list of all service names
func (p *Project) ServiceNames() []string {
	names := make([]string, 0, len(p.Config.Services))
	for name := range p.Config.Services {
		names = append(names, name)
	}
	return names
}

// VolumeInfo contains information about a project volume
type VolumeInfo struct {
	Name            string
	FullName        string
	Exists   bool
}

// Volumes returns information about all project volumes
func (p *Project) Volumes(ctx context.Context) ([]VolumeInfo, error) {
	var volumes []VolumeInfo

	for _, volumeName := range p.NamedVolumes() {
		fullName := p.VolumeName(volumeName)
		exists, err := p.Runtime.VolumeExists(ctx, fullName)
		if err != nil {
			return nil, fmt.Errorf("failed to check volume %s: %w", volumeName, err)
		}

		volumes = append(volumes, VolumeInfo{
			Name:     volumeName,
			FullName: fullName,
			Exists:   exists,
		})
	}

	return volumes, nil
}
