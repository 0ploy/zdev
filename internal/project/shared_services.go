package project

import (
	"context"
	"fmt"

	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/runtime"
	"github.com/0ploy/zdev/internal/services"
)

// connectEnabledSharedServices connects every shared service whose
// per-project flag is set to this project's network. The registry is the
// single source of truth (internal/services/registry.go), shared with the
// CLI so enabling a new shared service is a single-file change.
func (p *Project) connectEnabledSharedServices(ctx context.Context) {
	mgr, err := p.sharedManager()
	if err != nil {
		fmt.Printf("Warning: failed to load shared service config; shared services not connected: %v\n", err)
		return
	}
	for _, svc := range services.AllSharedServices() {
		if !svc.ProjectEnabled(&p.Config.Shared) || !svc.IsEnabled(mgr) {
			continue
		}
		fmt.Printf("Ensuring shared %s is running...\n", svc.Name)
		if err := svc.Start(ctx, mgr); err != nil {
			fmt.Printf("Warning: failed to start %s: %v\n", svc.Name, err)
			continue
		}
		if !svc.JoinsProjectNetworks() {
			continue
		}
		if err := svc.Connect(ctx, mgr, p.NetworkName()); err != nil {
			fmt.Printf("Warning: %v\n", err)
		}
	}
}

// disconnectEnabledSharedServices detaches every enabled shared service
// from the project's network. Iterates in reverse order so the router
// (which the others may route through) is removed last.
func (p *Project) disconnectEnabledSharedServices(ctx context.Context) {
	mgr, err := p.sharedManager()
	if err != nil {
		fmt.Printf("Warning: failed to load shared service config during disconnect: %v\n", err)
		return
	}
	registry := services.AllSharedServices()
	for i := len(registry) - 1; i >= 0; i-- {
		svc := registry[i]
		if !svc.ProjectEnabled(&p.Config.Shared) || !svc.JoinsProjectNetworks() {
			continue
		}
		if err := svc.Disconnect(ctx, mgr, p.NetworkName()); err != nil {
			fmt.Printf("Warning: failed to disconnect shared %s: %v\n", svc.Name, err)
		}
	}
}

// sharedManager constructs a services.Manager from the current global
// config. Errors swallowed by callers so a missing/broken global config
// doesn't stop the project lifecycle.
func (p *Project) sharedManager() (*services.Manager, error) {
	cfg, err := config.LoadGlobalConfig()
	if err != nil {
		return nil, err
	}
	return services.NewManager(cfg), nil
}

// configureRouting adds Traefik labels for routing based on the routing config
func (p *Project) configureRouting(cfg *runtime.ContainerConfig, serviceName string, routing *config.RoutingConfig, tlsEnabled bool) {
	traefikName := fmt.Sprintf("%s-%s", p.Config.Name, serviceName)

	cfg.Labels["traefik.enable"] = "true"
	cfg.Labels["traefik.docker.network"] = p.NetworkName()

	// Use service-level domain if set, otherwise fall back to project domain
	routingDomain := p.Config.Domain
	if routing.Domain != "" {
		routingDomain = routing.Domain
	}

	switch routing.Protocol {
	case "http":
		port := routing.Port
		if port == 0 {
			port = 80
		}
		// Always configure HTTP router
		cfg.Labels[fmt.Sprintf("traefik.http.routers.%s.rule", traefikName)] = fmt.Sprintf("Host(`%s`)", routingDomain)
		cfg.Labels[fmt.Sprintf("traefik.http.routers.%s.entrypoints", traefikName)] = "http"
		cfg.Labels[fmt.Sprintf("traefik.http.routers.%s.service", traefikName)] = traefikName
		cfg.Labels[fmt.Sprintf("traefik.http.services.%s.loadbalancer.server.port", traefikName)] = fmt.Sprintf("%d", port)
		cfg.Labels[fmt.Sprintf("traefik.http.services.%s.loadbalancer.passHostHeader", traefikName)] = "true"

		// Also configure HTTPS router when TLS is enabled
		if tlsEnabled {
			httpsName := traefikName + "-https"
			cfg.Labels[fmt.Sprintf("traefik.http.routers.%s.rule", httpsName)] = fmt.Sprintf("Host(`%s`)", routingDomain)
			cfg.Labels[fmt.Sprintf("traefik.http.routers.%s.entrypoints", httpsName)] = "https"
			cfg.Labels[fmt.Sprintf("traefik.http.routers.%s.tls", httpsName)] = "true"
			cfg.Labels[fmt.Sprintf("traefik.http.routers.%s.service", httpsName)] = traefikName // Use same service
		}

	case "https":
		port := routing.Port
		if port == 0 {
			port = 443
		}
		cfg.Labels[fmt.Sprintf("traefik.http.routers.%s.rule", traefikName)] = fmt.Sprintf("Host(`%s`)", routingDomain)
		cfg.Labels[fmt.Sprintf("traefik.http.routers.%s.entrypoints", traefikName)] = "https"
		cfg.Labels[fmt.Sprintf("traefik.http.routers.%s.tls", traefikName)] = "true"
		cfg.Labels[fmt.Sprintf("traefik.http.routers.%s.service", traefikName)] = traefikName
		cfg.Labels[fmt.Sprintf("traefik.http.services.%s.loadbalancer.server.port", traefikName)] = fmt.Sprintf("%d", port)
		cfg.Labels[fmt.Sprintf("traefik.http.services.%s.loadbalancer.passHostHeader", traefikName)] = "true"

	case "tcp":
		if routing.Port == 0 || routing.HostPort == 0 {
			return // TCP requires both port and host_port
		}
		entrypoint := fmt.Sprintf("tcp-%d", routing.HostPort)
		cfg.Labels[fmt.Sprintf("traefik.tcp.routers.%s.rule", traefikName)] = "HostSNI(`*`)"
		cfg.Labels[fmt.Sprintf("traefik.tcp.routers.%s.entrypoints", traefikName)] = entrypoint
		cfg.Labels[fmt.Sprintf("traefik.tcp.routers.%s.service", traefikName)] = traefikName
		cfg.Labels[fmt.Sprintf("traefik.tcp.services.%s.loadbalancer.server.port", traefikName)] = fmt.Sprintf("%d", routing.Port)

	case "udp":
		if routing.Port == 0 || routing.HostPort == 0 {
			return // UDP requires both port and host_port
		}
		entrypoint := fmt.Sprintf("udp-%d", routing.HostPort)
		cfg.Labels[fmt.Sprintf("traefik.udp.routers.%s.entrypoints", traefikName)] = entrypoint
		cfg.Labels[fmt.Sprintf("traefik.udp.routers.%s.service", traefikName)] = traefikName
		cfg.Labels[fmt.Sprintf("traefik.udp.services.%s.loadbalancer.server.port", traefikName)] = fmt.Sprintf("%d", routing.Port)
	}
}

// GetRequiredPorts returns all TCP/UDP host ports required by this project's routing config
func (p *Project) GetRequiredPorts() (tcpPorts, udpPorts []int) {
	for _, svc := range p.Config.Services {
		if svc.Routing == nil || svc.Routing.HostPort == 0 {
			continue
		}
		switch svc.Routing.Protocol {
		case "tcp":
			tcpPorts = append(tcpPorts, svc.Routing.HostPort)
		case "udp":
			udpPorts = append(udpPorts, svc.Routing.HostPort)
		}
	}
	return
}
