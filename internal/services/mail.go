package services

import (
	"github.com/0ploy/zdev/internal/runtime"
)

// MailContainerName is the name of the Mailpit container
const MailContainerName = "zdev_mail"

// MailServiceConfig holds configuration for the mail container
type MailServiceConfig struct {
	Image      string
	Domain     string
	TLSEnabled bool
}

// MailContainerConfig returns the container configuration for Mailpit.
// SMTP (1025) is reached via the network alias, the web UI (8025) via
// Traefik.
func MailContainerConfig(cfg MailServiceConfig) runtime.ContainerConfig {
	return webUIContainerConfig(webUIConfig{
		ContainerName: MailContainerName,
		Service:       "mail",
		Subdomain:     "mail",
		Alias:         "mail",
		Port:          "8025",
		Image:         cfg.Image,
		Domain:        cfg.Domain,
		TLSEnabled:    cfg.TLSEnabled,
	})
}
