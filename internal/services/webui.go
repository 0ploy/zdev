package services

import (
	"fmt"

	"github.com/0ploy/zdev/internal/runtime"
)

// webUIConfig describes a shared service exposed through Traefik as a
// web UI at <subdomain>.shared.<domain>. All shared services except the
// router itself (which needs port publishing) are built this way.
type webUIConfig struct {
	ContainerName string
	Service       string // zdev.service label value
	Subdomain     string // <subdomain>.shared.<domain>; also names the Traefik router ("zdev-<subdomain>")
	Alias         string // network alias project containers resolve
	Port          string // container port Traefik load-balances to
	Image         string
	Domain        string
	TLSEnabled    bool
	Env           map[string]string
	Volumes       []runtime.VolumeMount
}

// webUIContainerConfig builds the Traefik-routed container config shared
// by all web-UI services and stamps the config hash. No ports are
// published directly - the web UI is reached via Traefik, and any
// service-internal ports (e.g. SMTP 1025) via the network alias.
func webUIContainerConfig(cfg webUIConfig) runtime.ContainerConfig {
	host := fmt.Sprintf("%s.shared.%s", cfg.Subdomain, cfg.Domain)
	router := "zdev-" + cfg.Subdomain

	labels := map[string]string{
		"zdev.managed":        "true",
		"zdev.service":        cfg.Service,
		DozzleVisibilityLabel: "true",
		DozzleGroupLabel:      DozzleSharedGroup,

		// Enable Traefik routing for the web UI
		"traefik.enable":         "true",
		"traefik.docker.network": SharedNetworkName,

		// HTTP router
		"traefik.http.routers." + router + ".rule":        fmt.Sprintf("Host(`%s`)", host),
		"traefik.http.routers." + router + ".entrypoints": "http",
		"traefik.http.routers." + router + ".service":     router,

		// Service pointing to the web UI port
		"traefik.http.services." + router + ".loadbalancer.server.port": cfg.Port,
	}

	// Add HTTPS router if TLS is enabled
	if cfg.TLSEnabled {
		labels["traefik.http.routers."+router+"-https.rule"] = fmt.Sprintf("Host(`%s`)", host)
		labels["traefik.http.routers."+router+"-https.entrypoints"] = "https"
		labels["traefik.http.routers."+router+"-https.tls"] = "true"
		labels["traefik.http.routers."+router+"-https.service"] = router
	}

	out := runtime.ContainerConfig{
		Name:        cfg.ContainerName,
		Image:       cfg.Image,
		NetworkName: SharedNetworkName,
		Aliases:     []string{cfg.Alias},
		Labels:      labels,
		Env:         cfg.Env,
		Volumes:     cfg.Volumes,
	}
	runtime.StampConfigHash(&out)
	return out
}
