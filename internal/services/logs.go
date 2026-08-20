package services

import (
	"strconv"

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
	Shell      bool   // Enable Dozzle's in-container shell feature (off by default; see config.LogsConfig.Shell)
	SocketPath string // Host Docker socket path to mount (empty = /var/run/docker.sock)
}

// LogsContainerConfig returns the container configuration for Dozzle.
// Dozzle reads container info from the Docker socket; project containers
// stamp dev.dozzle.group=<project> in buildContainerConfig so they cluster
// per-project in the UI.
func LogsContainerConfig(cfg LogsServiceConfig) runtime.ContainerConfig {
	// Host socket path varies by engine (Docker Desktop, OrbStack, Colima);
	// the in-container target is the conventional path Dozzle expects.
	socketSource := cfg.SocketPath
	if socketSource == "" {
		socketSource = runtime.DefaultDockerSocket
	}

	return webUIContainerConfig(webUIConfig{
		ContainerName: LogsContainerName,
		Service:       "logs",
		Subdomain:     "logs",
		Alias:         "logs",
		Port:          "8080",
		Image:         cfg.Image,
		Domain:        cfg.Domain,
		TLSEnabled:    cfg.TLSEnabled,
		Env: map[string]string{
			"DOZZLE_NO_ANALYTICS": "true",
			// Restrict Dozzle to opted-in containers. Shared services always
			// stamp zdev.shared.logs=true; project containers only get the
			// label when their config sets shared.logs: true (see
			// internal/project/project.go buildContainerConfig). Other
			// zdev-managed containers and unrelated containers are hidden.
			"DOZZLE_FILTER": "label=" + DozzleVisibilityLabel + "=true",
			// Opening a shell into containers from the Dozzle UI. Off by
			// default because the router is published on all interfaces;
			// opt in via shared.logs.shell in ~/.zdev/global-config.yaml.
			"DOZZLE_ENABLE_SHELL": strconv.FormatBool(cfg.Shell),
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
	})
}
