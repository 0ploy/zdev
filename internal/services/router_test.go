package services

import (
	"fmt"
	"testing"

	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/runtime"
)

func TestRouterContainerConfig(t *testing.T) {
	tests := []struct {
		name         string
		image        string
		dashboard    bool
		domain       string
		tcpPorts     []int
		udpPorts     []int
		tlsCertDir   string
		tlsConfigDir string
	}{
		{
			name:      "basic config without dashboard",
			image:     config.RouterImage,
			dashboard: false,
			domain:    "0ploy.dev",
		},
		{
			name:      "config with dashboard enabled",
			image:     config.RouterImage,
			dashboard: true,
			domain:    "example.com",
		},
		{
			name:      "config with TCP ports",
			image:     config.RouterImage,
			dashboard: false,
			domain:    "0ploy.dev",
			tcpPorts:  []int{3306, 5432},
		},
		{
			name:      "config with UDP ports",
			image:     config.RouterImage,
			dashboard: false,
			domain:    "0ploy.dev",
			udpPorts:  []int{514},
		},
		{
			name:         "config with TLS enabled",
			image:        config.RouterImage,
			dashboard:    true,
			domain:       "0ploy.dev",
			tlsCertDir:   "/home/user/.zdev/certs",
			tlsConfigDir: "/home/user/.zdev/traefik",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			routerCfg := RouterConfig{
				Image:        tt.image,
				Dashboard:    tt.dashboard,
				Domain:       tt.domain,
				TCPPorts:     tt.tcpPorts,
				UDPPorts:     tt.udpPorts,
				TLSCertDir:   tt.tlsCertDir,
				TLSConfigDir: tt.tlsConfigDir,
			}
			cfg := RouterContainerConfig(routerCfg)
			tlsEnabled := tt.tlsCertDir != "" && tt.tlsConfigDir != ""

			// Check basic config
			if cfg.Name != RouterContainerName {
				t.Errorf("expected name %q, got %q", RouterContainerName, cfg.Name)
			}

			if cfg.Image != tt.image {
				t.Errorf("expected image %q, got %q", tt.image, cfg.Image)
			}

			if cfg.NetworkName != SharedNetworkName {
				t.Errorf("expected network %q, got %q", SharedNetworkName, cfg.NetworkName)
			}

			// Check ports - base ports
			if !containsString(cfg.Ports, "80:80") {
				t.Error("expected port 80:80")
			}
			if !containsString(cfg.Ports, "443:443") {
				t.Error("expected port 443:443")
			}
				// Check TCP ports
			for _, p := range tt.tcpPorts {
				expected := fmt.Sprintf("%d:%d", p, p)
				if !containsString(cfg.Ports, expected) {
					t.Errorf("expected TCP port %s", expected)
				}
			}
			// Check UDP ports
			for _, p := range tt.udpPorts {
				expected := fmt.Sprintf("%d:%d/udp", p, p)
				if !containsString(cfg.Ports, expected) {
					t.Errorf("expected UDP port %s", expected)
				}
			}

			// Check Docker socket mount (always first volume)
			expectedVolumes := 1
			if tlsEnabled {
				expectedVolumes = 3 // docker socket + certs + traefik config
			}
			if len(cfg.Volumes) != expectedVolumes {
				t.Errorf("expected %d volumes, got %d", expectedVolumes, len(cfg.Volumes))
			}
			if len(cfg.Volumes) >= 1 {
				vol := cfg.Volumes[0]
				if vol.Source != "/var/run/docker.sock" {
					t.Errorf("expected source /var/run/docker.sock, got %q", vol.Source)
				}
				if vol.Target != "/var/run/docker.sock" {
					t.Errorf("expected target /var/run/docker.sock, got %q", vol.Target)
				}
				if !vol.ReadOnly {
					t.Error("expected volume to be read-only")
				}
			}

			// Check TLS volume mounts
			if tlsEnabled {
				if len(cfg.Volumes) >= 2 {
					vol := cfg.Volumes[1]
					if vol.Source != tt.tlsCertDir {
						t.Errorf("expected cert source %q, got %q", tt.tlsCertDir, vol.Source)
					}
					if vol.Target != "/etc/traefik/certs" {
						t.Errorf("expected cert target /etc/traefik/certs, got %q", vol.Target)
					}
				}
				if len(cfg.Volumes) >= 3 {
					vol := cfg.Volumes[2]
					if vol.Source != tt.tlsConfigDir {
						t.Errorf("expected config source %q, got %q", tt.tlsConfigDir, vol.Source)
					}
					if vol.Target != "/etc/traefik/dynamic" {
						t.Errorf("expected config target /etc/traefik/dynamic, got %q", vol.Target)
					}
				}
			}

			// Check labels
			if cfg.Labels["zdev.managed"] != "true" {
				t.Error("expected zdev.managed label to be true")
			}
			if cfg.Labels["zdev.service"] != "router" {
				t.Error("expected zdev.service label to be router")
			}

			// Check command contains required Traefik args
			requiredArgs := []string{
				"--providers.docker=true",
				"--providers.docker.exposedbydefault=false",
				"--entrypoints.http.address=:80",
				"--entrypoints.https.address=:443",
			}
			for _, arg := range requiredArgs {
				if !containsString(cfg.Command, arg) {
					t.Errorf("expected command to contain %q", arg)
				}
			}

			// Check TLS file provider args
			if tlsEnabled {
				if !containsString(cfg.Command, "--providers.file.directory=/etc/traefik/dynamic") {
					t.Error("expected command to contain file provider directory when TLS enabled")
				}
				if cfg.Labels["zdev.tls-enabled"] != "true" {
					t.Error("expected zdev.tls-enabled label to be true when TLS enabled")
				}
			}

			// Check dashboard args and labels
			if tt.dashboard {
				if !containsString(cfg.Command, "--api.dashboard=true") {
					t.Error("expected command to contain --api.dashboard=true when dashboard enabled")
				}
				// Check Traefik labels for dashboard routing
				expectedHost := "router.shared." + tt.domain
				if cfg.Labels["traefik.enable"] != "true" {
					t.Error("expected traefik.enable label to be true when dashboard enabled")
				}
				expectedRule := "Host(`" + expectedHost + "`)"

				// HTTP router is always configured
				if cfg.Labels["traefik.http.routers.traefik-dashboard.rule"] != expectedRule {
					t.Errorf("expected dashboard rule %q, got %q", expectedRule, cfg.Labels["traefik.http.routers.traefik-dashboard.rule"])
				}
				if cfg.Labels["traefik.http.routers.traefik-dashboard.entrypoints"] != "http" {
					t.Error("expected dashboard HTTP entrypoint to be http")
				}
				if cfg.Labels["traefik.http.routers.traefik-dashboard.service"] != "api@internal" {
					t.Error("expected dashboard service to be api@internal")
				}

				// HTTPS router is also configured when TLS enabled
				if tlsEnabled {
					if cfg.Labels["traefik.http.routers.traefik-dashboard-https.rule"] != expectedRule {
						t.Errorf("expected dashboard HTTPS rule %q, got %q", expectedRule, cfg.Labels["traefik.http.routers.traefik-dashboard-https.rule"])
					}
					if cfg.Labels["traefik.http.routers.traefik-dashboard-https.entrypoints"] != "https" {
						t.Error("expected dashboard HTTPS entrypoint to be https when TLS enabled")
					}
					if cfg.Labels["traefik.http.routers.traefik-dashboard-https.tls"] != "true" {
						t.Error("expected dashboard tls label to be true when TLS enabled")
					}
					if cfg.Labels["traefik.http.routers.traefik-dashboard-https.service"] != "api@internal" {
						t.Error("expected dashboard HTTPS service to be api@internal")
					}
				}
			} else {
				if containsString(cfg.Command, "--api.dashboard=true") {
					t.Error("expected command to not contain --api.dashboard=true when dashboard disabled")
				}
				if cfg.Labels["traefik.enable"] == "true" {
					t.Error("expected traefik.enable label to not be set when dashboard disabled")
				}
			}
		})
	}
}

func TestRouterContainerConfig_SocketPath(t *testing.T) {
	// Default: empty SocketPath falls back to the conventional path.
	def := RouterContainerConfig(RouterConfig{Image: config.RouterImage, Domain: "0ploy.dev"})
	if got := def.Volumes[0].Source; got != runtime.DefaultDockerSocket {
		t.Errorf("default socket source = %q, want %q", got, runtime.DefaultDockerSocket)
	}

	// Explicit engine socket (e.g. OrbStack) is used as the mount source,
	// while the in-container target stays the conventional path.
	const orb = "/Users/me/.orbstack/run/docker.sock"
	cfg := RouterContainerConfig(RouterConfig{Image: config.RouterImage, Domain: "0ploy.dev", SocketPath: orb})
	if got := cfg.Volumes[0].Source; got != orb {
		t.Errorf("socket source = %q, want %q", got, orb)
	}
	if got := cfg.Volumes[0].Target; got != runtime.DefaultDockerSocket {
		t.Errorf("socket target = %q, want %q", got, runtime.DefaultDockerSocket)
	}
}

func containsString(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}
