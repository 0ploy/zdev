package config

import (
	"fmt"
	"regexp"
	"strconv"
)

var (
	projectNameRegex = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	serviceNameRegex = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)
	// hostnameRegex matches a plain DNS hostname (dot-separated labels). It is
	// intentionally the same shape as resolver.domainPattern. Domains flow
	// unquoted into Traefik Host(`...`) rules (internal/project/shared_services.go)
	// and into the docs HTML, so a value containing a backtick, quote, space, or
	// slash could break out and attach extra matchers/markup. Reject anything
	// that isn't a bare hostname.
	hostnameRegex = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?)+$`)
)

// validateHostname rejects anything that isn't a plain dot-separated hostname.
// An empty value is allowed (callers fall back to a higher-level default, e.g.
// routing.domain falls back to the project domain); pass required=true where an
// empty value is itself invalid.
func validateHostname(field, value string, required bool) error {
	if value == "" {
		if required {
			return fmt.Errorf("%s is required", field)
		}
		return nil
	}
	if !hostnameRegex.MatchString(value) {
		return fmt.Errorf("%s %q is not a valid hostname", field, value)
	}
	return nil
}

// ValidateProjectName checks that a project name is safe for DNS and Docker
// resource names.
func ValidateProjectName(name string) error {
	if name == "" {
		return fmt.Errorf("project name cannot be empty")
	}
	if !projectNameRegex.MatchString(name) {
		return fmt.Errorf("project name %q is invalid: must contain only lowercase letters, numbers, and hyphens (no leading/trailing hyphens, max 63 chars)", name)
	}
	return nil
}

// ValidateProjectConfig checks semantic constraints that YAML decoding cannot
// express.
func ValidateProjectConfig(cfg *ProjectConfig) error {
	if err := ValidateProjectName(cfg.Name); err != nil {
		return err
	}
	if err := validateHostname("domain", cfg.Domain, false); err != nil {
		return err
	}
	if len(cfg.Services) == 0 {
		return fmt.Errorf("at least one service is required")
	}

	claimedPorts := make(map[string]string)
	for name, service := range cfg.Services {
		if !serviceNameRegex.MatchString(name) {
			return fmt.Errorf("service name %q is invalid: use letters, numbers, dots, hyphens, or underscores", name)
		}
		if service.Image == "" && service.Dockerfile == "" {
			return fmt.Errorf("service %s: either image or dockerfile is required", name)
		}
		if err := validateMode(name, "file_mode", service.Mutagen.FileMode); err != nil {
			return err
		}
		if err := validateMode(name, "directory_mode", service.Mutagen.DirectoryMode); err != nil {
			return err
		}
		if service.Routing == nil {
			continue
		}

		routing := service.Routing
		switch routing.Protocol {
		case "http", "https":
			if routing.Port < 0 || routing.Port > 65535 {
				return fmt.Errorf("service %s: routing port must be between 1 and 65535", name)
			}
			if routing.HostPort != 0 {
				return fmt.Errorf("service %s: host_port is only valid for tcp or udp routing", name)
			}
		case "tcp", "udp":
			if routing.Port < 1 || routing.Port > 65535 {
				return fmt.Errorf("service %s: %s routing requires port between 1 and 65535", name, routing.Protocol)
			}
			if routing.HostPort < 1 || routing.HostPort > 65535 {
				return fmt.Errorf("service %s: %s routing requires host_port between 1 and 65535", name, routing.Protocol)
			}
			key := fmt.Sprintf("%s/%d", routing.Protocol, routing.HostPort)
			if owner := claimedPorts[key]; owner != "" {
				return fmt.Errorf("service %s: host port %d/%s is already used by service %s", name, routing.HostPort, routing.Protocol, owner)
			}
			claimedPorts[key] = name
		default:
			return fmt.Errorf("service %s: unsupported routing protocol %q (use http, https, tcp, or udp)", name, routing.Protocol)
		}
		if err := validateHostname(fmt.Sprintf("service %s: routing domain", name), routing.Domain, false); err != nil {
			return err
		}
	}
	return nil
}

func validateMode(serviceName, field, value string) error {
	if value == "" {
		return nil
	}
	mode, err := strconv.ParseUint(value, 8, 12)
	if err != nil || mode > 0777 {
		return fmt.Errorf("service %s: mutagen %s %q is invalid: use an octal mode from 0000 to 0777", serviceName, field, value)
	}
	return nil
}

func validateGlobalConfig(cfg *GlobalConfig) error {
	switch cfg.Mutagen.Enabled {
	case "", "auto", "true", "false":
	default:
		return fmt.Errorf("mutagen.enabled must be auto, true, or false")
	}
	switch cfg.Mutagen.SyncMode {
	case "", "two-way-safe", "two-way-resolved", "one-way-safe", "one-way-replica":
	default:
		return fmt.Errorf("unsupported mutagen.sync_mode %q", cfg.Mutagen.SyncMode)
	}
	if err := validateHostname("domain", cfg.Domain, true); err != nil {
		return err
	}
	return nil
}
