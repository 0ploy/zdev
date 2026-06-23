package services

import (
	"fmt"

	"github.com/0ploy/zdev/internal/runtime"
)

// LogsContainerName is the name of the Dozzle log viewer container
const LogsContainerName = "zdev_logs"

// LogsDataVolumeName is the named volume backing Dozzle's /data directory
// (notification settings, saved searches, user state).
const LogsDataVolumeName = "zdev_logs_data"

// LogsServiceConfig holds configuration for the Dozzle container
type LogsServiceConfig struct {
	Image      string
	Domain     string
	TLSEnabled bool
	SocketPath string // Host Docker socket path to mount (empty = /var/run/docker.sock)
}

// LogsContainerConfig returns the container configuration for Dozzle.
// Dozzle reads container info from the Docker socket; project containers
// stamp dev.dozzle.group=<project> in buildContainerConfig so they cluster
// per-project in the UI.
func LogsContainerConfig(cfg LogsServiceConfig) runtime.ContainerConfig {
	logsHost := fmt.Sprintf("logs.shared.%s", cfg.Domain)

	// Host socket path varies by engine (Docker Desktop, OrbStack, Colima);
	// the in-container target is the conventional path Dozzle expects.
	socketSource := cfg.SocketPath
	if socketSource == "" {
		socketSource = runtime.DefaultDockerSocket
	}

	labels := map[string]string{
		"zdev.managed":       "true",
		"zdev.service":       "logs",
		DozzleVisibilityLabel: "true",
		DozzleGroupLabel:      DozzleSharedGroup,

		// Enable Traefik routing for web UI
		"traefik.enable":         "true",
		"traefik.docker.network": SharedNetworkName,

		// HTTP router for web UI
		"traefik.http.routers.zdev-logs.rule":        fmt.Sprintf("Host(`%s`)", logsHost),
		"traefik.http.routers.zdev-logs.entrypoints": "http",
		"traefik.http.routers.zdev-logs.service":     "zdev-logs",

		// Service pointing to Dozzle web UI port
		"traefik.http.services.zdev-logs.loadbalancer.server.port": "8080",
	}

	// Add HTTPS router if TLS is enabled
	if cfg.TLSEnabled {
		labels["traefik.http.routers.zdev-logs-https.rule"] = fmt.Sprintf("Host(`%s`)", logsHost)
		labels["traefik.http.routers.zdev-logs-https.entrypoints"] = "https"
		labels["traefik.http.routers.zdev-logs-https.tls"] = "true"
		labels["traefik.http.routers.zdev-logs-https.service"] = "zdev-logs"
	}

	out := runtime.ContainerConfig{
		Name:        LogsContainerName,
		Image:       cfg.Image,
		NetworkName: SharedNetworkName,
		Aliases:     []string{"logs"},
		Labels:      labels,
		Env: map[string]string{
			"DOZZLE_NO_ANALYTICS": "true",
			// Restrict Dozzle to opted-in containers. Shared services always
			// stamp zdev.shared.logs=true; project containers only get the
			// label when their config sets shared.logs: true (see
			// internal/project/project.go buildContainerConfig). Other
			// zdev-managed containers and unrelated containers are hidden.
			"DOZZLE_FILTER": "label=" + DozzleVisibilityLabel + "=true",
			// Allow opening a shell into containers from the Dozzle UI.
			"DOZZLE_ENABLE_SHELL": "true",
		},
		Volumes: []runtime.VolumeMount{
			{
				Source:   socketSource,
				Target:   runtime.DefaultDockerSocket,
				ReadOnly: true,
			},
			// Persist Dozzle's own state (notification settings, user data,
			// saved searches) across container recreates. Named volume; Docker
			// auto-creates it on first start.
			{
				Source: LogsDataVolumeName,
				Target: "/data",
			},
		},
	}
	runtime.StampConfigHash(&out)
	return out
}
