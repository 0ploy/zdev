package services

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/runtime"
)

// RouterConfig holds configuration for the router container
type RouterConfig struct {
	Image        string
	Dashboard    bool
	Domain       string
	TCPPorts     []int  // Additional TCP ports to expose
	UDPPorts     []int  // Additional UDP ports to expose
	TLSCertDir   string // Path to certificate directory (empty = no TLS)
	TLSConfigDir string // Path to Traefik dynamic config directory
	DocsDir      string // Path to docs directory for Statiq plugin (empty = no docs)
	SocketPath   string // Host Docker socket path to mount (empty = /var/run/docker.sock)
}

// RouterContainerConfig returns the container configuration for the Traefik router
func RouterContainerConfig(cfg RouterConfig) runtime.ContainerConfig {
	command := []string{
		"--providers.docker=true",
		"--providers.docker.exposedbydefault=false",
		// No network restriction - containers specify their network via traefik.docker.network label
		"--entrypoints.http.address=:80",
		"--entrypoints.https.address=:443",
	}

	ports := []string{"80:80", "443:443"}

	// Add file provider for TLS/docs configuration if config directory is set
	tlsEnabled := cfg.TLSCertDir != "" && cfg.TLSConfigDir != ""
	if tlsEnabled || cfg.DocsDir != "" {
		command = append(command,
			"--providers.file.directory=/etc/traefik/dynamic",
			"--providers.file.watch=true",
		)
	}

	// Add Statiq plugin for serving docs if docs directory is set
	docsEnabled := cfg.DocsDir != ""
	if docsEnabled {
		command = append(command,
			fmt.Sprintf("--experimental.plugins.statiq.moduleName=%s", config.StatiqPluginModule),
			fmt.Sprintf("--experimental.plugins.statiq.version=%s", config.StatiqPluginVersion),
		)
	}

	// Add TCP entrypoints and ports
	for _, port := range cfg.TCPPorts {
		entrypoint := fmt.Sprintf("tcp-%d", port)
		command = append(command, fmt.Sprintf("--entrypoints.%s.address=:%d", entrypoint, port))
		ports = append(ports, fmt.Sprintf("%d:%d", port, port))
	}

	// Add UDP entrypoints and ports
	for _, port := range cfg.UDPPorts {
		entrypoint := fmt.Sprintf("udp-%d", port)
		command = append(command, fmt.Sprintf("--entrypoints.%s.address=:%d/udp", entrypoint, port))
		ports = append(ports, fmt.Sprintf("%d:%d/udp", port, port))
	}

	labels := map[string]string{
		"zdev.managed":       "true",
		"zdev.service":       "router",
		DozzleVisibilityLabel: "true",
		DozzleGroupLabel:      DozzleSharedGroup,
	}

	// Store configured ports in labels for later comparison
	labels["zdev.tcp-ports"] = intsToString(cfg.TCPPorts)
	labels["zdev.udp-ports"] = intsToString(cfg.UDPPorts)

	// Store TLS status in labels
	if tlsEnabled {
		labels["zdev.tls-enabled"] = "true"
	}

	// Store docs status in labels
	if docsEnabled {
		labels["zdev.docs-enabled"] = "true"
	}

	// Enable dashboard if configured (accessible via router.shared.<domain>)
	if cfg.Dashboard {
		dashboardHost := fmt.Sprintf("router.shared.%s", cfg.Domain)

		command = append(command,
			"--api=true",
			"--api.dashboard=true",
		)

		labels["traefik.enable"] = "true"

		// Always configure HTTP router for dashboard
		labels["traefik.http.routers.traefik-dashboard.rule"] = fmt.Sprintf("Host(`%s`)", dashboardHost)
		labels["traefik.http.routers.traefik-dashboard.entrypoints"] = "http"
		labels["traefik.http.routers.traefik-dashboard.service"] = "api@internal"

		// Also configure HTTPS router when TLS is enabled
		if tlsEnabled {
			labels["traefik.http.routers.traefik-dashboard-https.rule"] = fmt.Sprintf("Host(`%s`)", dashboardHost)
			labels["traefik.http.routers.traefik-dashboard-https.entrypoints"] = "https"
			labels["traefik.http.routers.traefik-dashboard-https.tls"] = "true"
			labels["traefik.http.routers.traefik-dashboard-https.service"] = "api@internal"
		}
	}

	// Build volumes list. The host socket path varies by engine (Docker
	// Desktop, OrbStack, Colima); the in-container target is always the
	// conventional path Traefik expects.
	socketSource := cfg.SocketPath
	if socketSource == "" {
		socketSource = runtime.DefaultDockerSocket
	}
	volumes := []runtime.VolumeMount{
		{
			Source:   socketSource,
			Target:   runtime.DefaultDockerSocket,
			ReadOnly: true,
		},
	}

	// Add TLS volume mounts if enabled
	if tlsEnabled {
		volumes = append(volumes,
			runtime.VolumeMount{
				Source:   cfg.TLSCertDir,
				Target:   "/etc/traefik/certs",
				ReadOnly: true,
			},
		)
	}

	// Add dynamic config directory mount (for TLS and/or docs config)
	if cfg.TLSConfigDir != "" {
		volumes = append(volumes,
			runtime.VolumeMount{
				Source:   cfg.TLSConfigDir,
				Target:   "/etc/traefik/dynamic",
				ReadOnly: true,
			},
		)
	}

	// Add docs volume mount if enabled
	if docsEnabled {
		volumes = append(volumes,
			runtime.VolumeMount{
				Source:   cfg.DocsDir,
				Target:   "/docs",
				ReadOnly: true,
			},
		)
	}

	out := runtime.ContainerConfig{
		Name:        RouterContainerName,
		Image:       cfg.Image,
		Command:     command,
		Ports:       ports,
		NetworkName: SharedNetworkName,
		Aliases:     []string{"router"},
		Volumes:     volumes,
		Labels:      labels,
	}
	runtime.StampConfigHash(&out)
	return out
}

// intsToString converts a slice of ints to a sorted comma-separated string.
// Sorted so the output is deterministic across runs - both the router's
// zdev.tcp-ports / zdev.udp-ports labels and the config hash derived
// from them are stable.
func intsToString(ports []int) string {
	if len(ports) == 0 {
		return ""
	}
	sorted := make([]int, len(ports))
	copy(sorted, ports)
	sort.Ints(sorted)

	parts := make([]string, len(sorted))
	for i, p := range sorted {
		parts[i] = strconv.Itoa(p)
	}
	return strings.Join(parts, ",")
}
