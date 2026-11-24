//go:build integration

package services

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/runtime"
	"github.com/0ploy/zdev/internal/state"
)

// Integration tests require Docker to be running
// Run with: go test -tags=integration ./...

// sharedServicesSnapshot records which shared services were running before a test
// so they can be restored afterward.
type sharedServicesSnapshot struct {
	routerRunning bool
	mailRunning   bool
	dbuiRunning   bool
	redisRunning  bool
	logsRunning   bool
	networkExists bool

	// networks maps each shared container to the project networks it was
	// attached to. Restarting a service is not enough: a recreated container
	// comes back on zdev_shared only, so without this the developer's running
	// projects lose their shared services for the rest of the day.
	networks map[string]map[string][]string
}

// snapshotSharedServices checks which shared services are currently running.
func snapshotSharedServices(ctx context.Context, mgr *Manager, docker *runtime.DockerCLI) sharedServicesSnapshot {
	var s sharedServicesSnapshot
	if status, err := mgr.RouterStatus(ctx); err == nil {
		s.routerRunning = status.Running
	}
	if status, err := mgr.MailStatus(ctx); err == nil {
		s.mailRunning = status.Running
	}
	if status, err := mgr.DBUIStatus(ctx); err == nil {
		s.dbuiRunning = status.Running
	}
	if status, err := mgr.RedisInsightsStatus(ctx); err == nil {
		s.redisRunning = status.Running
	}
	if status, err := mgr.LogsStatus(ctx); err == nil {
		s.logsRunning = status.Running
	}
	s.networkExists, _ = docker.NetworkExists(ctx, SharedNetworkName)

	s.networks = make(map[string]map[string][]string)
	for _, svc := range AllSharedServices() {
		s.networks[svc.ContainerName] = mgr.SnapshotProjectNetworks(ctx, svc.ContainerName)
	}
	return s
}

// restoreSharedServices restarts any shared services that were running before the test.
// Uses the real global config so containers are created with correct settings.
func restoreSharedServices(ctx context.Context, snap sharedServicesSnapshot) {
	globalCfg, err := config.LoadGlobalConfig()
	if err != nil {
		return // Best effort
	}
	mgr := NewManager(globalCfg)

	if snap.networkExists {
		_ = mgr.EnsureSharedNetwork(ctx)
	}
	if snap.routerRunning {
		_ = mgr.StartRouter(ctx)
	}
	if snap.mailRunning {
		_ = mgr.StartMail(ctx)
	}
	if snap.dbuiRunning {
		_ = mgr.StartDBUI(ctx)
	}
	if snap.redisRunning {
		_ = mgr.StartRedisInsights(ctx)
	}
	if snap.logsRunning {
		_ = mgr.StartLogs(ctx)
	}

	for container, networks := range snap.networks {
		mgr.RestoreProjectNetworks(ctx, container, networks)
	}
}

func TestManager_RouterLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Create manager with default config (uses DefaultDomain which resolves to 127.0.0.1)
	cfg := &config.GlobalConfig{
		Version: 1,
		Domain:  config.DefaultDomain,
		Runtime: "docker",
		Shared: config.SharedConfig{
			Router: config.RouterConfig{
				Image:     config.RouterImage,
				Dashboard: false,
			},
		},
	}

	mgr := NewManager(cfg)
	docker := runtime.NewDockerCLI()

	// Snapshot running shared services so we can restore them after the test
	snap := snapshotSharedServices(ctx, mgr, docker)
	defer restoreSharedServices(ctx, snap)

	// Cleanup any leftover resources from previous runs
	_ = docker.StopContainer(ctx, RouterContainerName)
	_ = docker.RemoveContainer(ctx, RouterContainerName)
	_ = docker.RemoveNetwork(ctx, SharedNetworkName)

	// Test: Ensure shared network
	t.Run("EnsureSharedNetwork", func(t *testing.T) {
		if err := mgr.EnsureSharedNetwork(ctx); err != nil {
			t.Fatalf("EnsureSharedNetwork failed: %v", err)
		}

		exists, err := docker.NetworkExists(ctx, SharedNetworkName)
		if err != nil {
			t.Fatalf("NetworkExists failed: %v", err)
		}
		if !exists {
			t.Fatal("expected shared network to exist")
		}
	})

	// Test: Start router
	t.Run("StartRouter", func(t *testing.T) {
		if err := mgr.StartRouter(ctx); err != nil {
			t.Fatalf("StartRouter failed: %v", err)
		}

		status, err := mgr.RouterStatus(ctx)
		if err != nil {
			t.Fatalf("RouterStatus failed: %v", err)
		}
		if !status.Running {
			t.Fatal("expected router to be running")
		}
	})

	// Test: Start router again (should be idempotent)
	t.Run("StartRouter_Idempotent", func(t *testing.T) {
		if err := mgr.StartRouter(ctx); err != nil {
			t.Fatalf("StartRouter (second time) failed: %v", err)
		}
	})

	// Router aliases against real Docker: the alias set is accepted, Docker
	// DNS answers a public *.0ploy.dev name (127.0.0.1 in public DNS) with
	// the router, a changed set is applied by reconnecting, and an unchanged
	// set leaves the endpoint alone. Runs on the fresh test router, which is
	// on no real project network, so the developer's aliases are untouched.
	t.Run("RouterAliases_ResolveAndUpdate", func(t *testing.T) {
		// State (registered hosts) from a temp home; reverted after the subtest.
		t.Setenv("ZDEV_HOME", t.TempDir())
		stateMgr, err := state.DefaultManager()
		if err != nil {
			t.Fatal(err)
		}
		alphaNet, betaNet := "zdev_itest_alias_alpha", "zdev_itest_alias_beta"
		alphaHost := "zd-itest-alpha." + config.DefaultDomain
		betaHost := "zd-itest-beta." + config.DefaultDomain
		for _, n := range []string{alphaNet, betaNet} {
			_ = docker.RemoveNetwork(ctx, n)
			if err := docker.CreateNetwork(ctx, n); err != nil {
				t.Fatalf("CreateNetwork %s: %v", n, err)
			}
			n := n
			t.Cleanup(func() {
				_ = docker.NetworkDisconnect(context.Background(), n, RouterContainerName)
				_ = docker.RemoveNetwork(context.Background(), n)
			})
		}

		if err := stateMgr.RegisterProjectWithRouting("zd-itest-alpha", "/tmp/a", nil, nil, []string{alphaHost}); err != nil {
			t.Fatal(err)
		}
		if err := mgr.ConnectRouter(ctx, alphaNet); err != nil {
			t.Fatalf("ConnectRouter(alpha): %v", err)
		}
		requireAliases(t, ctx, docker, alphaNet, routerShortAlias, alphaHost)
		requireResolvesToRouter(t, ctx, alphaNet, alphaHost)
		endpointBefore := routerEndpoint(t, alphaNet)

		// A second project with a new hostname: alpha's network must learn it.
		if err := stateMgr.RegisterProjectWithRouting("zd-itest-beta", "/tmp/b", nil, nil, []string{betaHost}); err != nil {
			t.Fatal(err)
		}
		if err := mgr.ConnectRouter(ctx, betaNet); err != nil {
			t.Fatalf("ConnectRouter(beta): %v", err)
		}
		requireAliases(t, ctx, docker, betaNet, alphaHost, betaHost)
		requireAliases(t, ctx, docker, alphaNet, alphaHost, betaHost)
		requireResolvesToRouter(t, ctx, alphaNet, betaHost)
		if routerEndpoint(t, alphaNet) == endpointBefore {
			t.Error("alpha endpoint unchanged although its alias set changed")
		}

		// Nothing changed: no reconnect anywhere.
		alphaEP, betaEP := routerEndpoint(t, alphaNet), routerEndpoint(t, betaNet)
		if err := mgr.ConnectRouter(ctx, betaNet); err != nil {
			t.Fatalf("ConnectRouter(beta) again: %v", err)
		}
		if routerEndpoint(t, alphaNet) != alphaEP || routerEndpoint(t, betaNet) != betaEP {
			t.Error("router reconnected although the alias set was unchanged")
		}
	})

	// Test: Connect to project network
	t.Run("ConnectRouterToProject", func(t *testing.T) {
		// Create a test network
		testNetwork := "zdev_test_project"
		_ = docker.RemoveNetwork(ctx, testNetwork)
		if err := docker.CreateNetwork(ctx, testNetwork); err != nil {
			t.Fatalf("CreateNetwork failed: %v", err)
		}
		defer docker.RemoveNetwork(ctx, testNetwork)

		router := defFor(t, RouterContainerName)
		if err := router.Connect(ctx, mgr, testNetwork); err != nil {
			t.Fatalf("router Connect failed: %v", err)
		}

		// Connect again should not error
		if err := router.Connect(ctx, mgr, testNetwork); err != nil {
			t.Fatalf("router Connect (second time) failed: %v", err)
		}

		// Disconnect
		if err := router.Disconnect(ctx, mgr, testNetwork); err != nil {
			t.Fatalf("router Disconnect failed: %v", err)
		}
	})

	// Test: Stop router
	t.Run("StopRouter", func(t *testing.T) {
		if err := defFor(t, RouterContainerName).Stop(ctx, mgr); err != nil {
			t.Fatalf("router Stop failed: %v", err)
		}

		status, err := mgr.RouterStatus(ctx)
		if err != nil {
			t.Fatalf("RouterStatus failed: %v", err)
		}
		if status.Running {
			t.Fatal("expected router to be stopped")
		}
	})

}

func TestManager_NetworkConnect(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	docker := runtime.NewDockerCLI()
	testNetwork := "zdev_network_test"
	testContainer := "zdev_network_test_container"

	// Cleanup
	defer func() {
		_ = docker.StopContainer(ctx, testContainer)
		_ = docker.RemoveContainer(ctx, testContainer)
		_ = docker.RemoveNetwork(ctx, testNetwork)
	}()

	// Create network
	_ = docker.RemoveNetwork(ctx, testNetwork)
	if err := docker.CreateNetwork(ctx, testNetwork); err != nil {
		t.Fatalf("CreateNetwork failed: %v", err)
	}

	// Create and start a container on default network
	cfg := runtime.ContainerConfig{
		Name:    testContainer,
		Image:   config.TestImage,
		Command: []string{"sleep", "infinity"},
	}

	// Pull image if needed
	imageExists, _ := docker.ImageExists(ctx, cfg.Image)
	if !imageExists {
		if err := docker.PullImage(ctx, cfg.Image); err != nil {
			t.Fatalf("PullImage failed: %v", err)
		}
	}

	if _, err := docker.CreateContainer(ctx, cfg); err != nil {
		t.Fatalf("CreateContainer failed: %v", err)
	}
	if err := docker.StartContainer(ctx, testContainer); err != nil {
		t.Fatalf("StartContainer failed: %v", err)
	}

	// Test: Connect container to network
	if err := docker.NetworkConnect(ctx, testNetwork, testContainer); err != nil {
		t.Fatalf("NetworkConnect failed: %v", err)
	}

	// Test: Disconnect container from network
	if err := docker.NetworkDisconnect(ctx, testNetwork, testContainer); err != nil {
		t.Fatalf("NetworkDisconnect failed: %v", err)
	}

	// Test: Disconnect again should not error
	if err := docker.NetworkDisconnect(ctx, testNetwork, testContainer); err != nil {
		t.Fatalf("NetworkDisconnect (second time) should not error: %v", err)
	}

	// Test: Connect with aliases
	if err := docker.NetworkConnect(ctx, testNetwork, testContainer, "myalias", "otheralias"); err != nil {
		t.Fatalf("NetworkConnect with aliases failed: %v", err)
	}
}

func TestManager_DocsRoutes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Use the real global config because docs.yaml is shared global state
	// (~/.zdev/traefik/docs.yaml). Concurrent integration tests in
	// internal/project also tear down/restore the shared router using
	// LoadGlobalConfig, so a synthetic cfg here can race against their
	// restore and end up asserting against a docs.yaml that was rewritten
	// with a different SSL setting between StartRouter and the HTTP check.
	cfg, err := config.LoadGlobalConfig()
	if err != nil {
		t.Fatalf("LoadGlobalConfig: %v", err)
	}

	mgr := NewManager(cfg)
	docker := runtime.NewDockerCLI()

	// Snapshot running shared services so we can restore them after the test
	snap := snapshotSharedServices(ctx, mgr, docker)
	defer restoreSharedServices(ctx, snap)

	// Cleanup any leftover resources from previous runs
	_ = docker.StopContainer(ctx, RouterContainerName)
	_ = docker.RemoveContainer(ctx, RouterContainerName)
	_ = docker.RemoveNetwork(ctx, SharedNetworkName)

	// Ensure shared network exists
	if err := mgr.EnsureSharedNetwork(ctx); err != nil {
		t.Fatalf("EnsureSharedNetwork failed: %v", err)
	}

	// Start router (this will set up docs config via EnsureDocsConfig)
	if err := mgr.StartRouter(ctx); err != nil {
		t.Fatalf("StartRouter failed: %v", err)
	}

	// Use a client that doesn't follow redirects to check 302 status
	noRedirectClient := &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	normalClient := &http.Client{Timeout: 5 * time.Second}

	protocol := "http"
	if cfg.SSL.Enabled {
		protocol = "https"
	}

	// Test: docs.shared.<domain> returns 200
	t.Run("DocsReturns200", func(t *testing.T) {
		url := fmt.Sprintf("http://docs.shared.%s", cfg.Domain)

		var lastErr error
		for i := 0; i < 15; i++ {
			time.Sleep(1 * time.Second)

			resp, err := normalClient.Get(url)
			if err != nil {
				lastErr = fmt.Errorf("HTTP request to docs failed: %w", err)
				continue
			}
			resp.Body.Close()

			if resp.StatusCode == http.StatusOK {
				t.Logf("Docs route returns 200: %s", url)
				return
			}

			lastErr = fmt.Errorf("expected HTTP 200, got %d", resp.StatusCode)
		}

		t.Fatalf("Docs route check failed after retries: %v", lastErr)
	})

	// Test: nonexisting.<domain> returns 302 redirect to docs
	t.Run("NonExistingReturns302", func(t *testing.T) {
		url := fmt.Sprintf("http://nonexisting.%s", cfg.Domain)

		var lastErr error
		for i := 0; i < 15; i++ {
			time.Sleep(1 * time.Second)

			resp, err := noRedirectClient.Get(url)
			if err != nil {
				lastErr = fmt.Errorf("HTTP request to nonexisting domain failed: %w", err)
				continue
			}
			resp.Body.Close()

			if resp.StatusCode == http.StatusFound {
				// Verify redirect location points to docs (protocol depends on SSL state)
				location := resp.Header.Get("Location")
				expectedLocation := fmt.Sprintf("%s://docs.shared.%s/", protocol, cfg.Domain)
				if location != expectedLocation {
					t.Fatalf("expected redirect to %s, got %s", expectedLocation, location)
				}
				t.Logf("Non-existing route returns 302 redirect to docs: %s -> %s", url, location)
				return
			}

			lastErr = fmt.Errorf("expected HTTP 302, got %d", resp.StatusCode)
		}

		t.Fatalf("Non-existing route check failed after retries: %v", lastErr)
	})

}

func TestManager_DBUILifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Create manager with default config (uses DefaultDomain which resolves to 127.0.0.1)
	cfg := &config.GlobalConfig{
		Version: 1,
		Domain:  config.DefaultDomain,
		Runtime: "docker",
		Shared: config.SharedConfig{
			Router: config.RouterConfig{
				Image:     config.RouterImage,
				Dashboard: false,
			},
			DBUI: config.DBUIConfig{
				Image: config.DBUIImage,
			},
		},
	}

	mgr := NewManager(cfg)
	docker := runtime.NewDockerCLI()

	// Snapshot running shared services so we can restore them after the test
	snap := snapshotSharedServices(ctx, mgr, docker)
	defer restoreSharedServices(ctx, snap)

	// Cleanup any leftover resources from previous runs
	_ = docker.StopContainer(ctx, DBUIContainerName)
	_ = docker.RemoveContainer(ctx, DBUIContainerName)
	_ = docker.StopContainer(ctx, RouterContainerName)
	_ = docker.RemoveContainer(ctx, RouterContainerName)
	_ = docker.RemoveNetwork(ctx, SharedNetworkName)

	// Ensure shared network exists
	if err := mgr.EnsureSharedNetwork(ctx); err != nil {
		t.Fatalf("EnsureSharedNetwork failed: %v", err)
	}

	// Start router first (needed for HTTP routing to DBUI)
	if err := mgr.StartRouter(ctx); err != nil {
		t.Fatalf("StartRouter failed: %v", err)
	}

	// Test: Start DBUI
	t.Run("StartDBUI", func(t *testing.T) {
		if err := mgr.StartDBUI(ctx); err != nil {
			t.Fatalf("StartDBUI failed: %v", err)
		}

		status, err := mgr.DBUIStatus(ctx)
		if err != nil {
			t.Fatalf("DBUIStatus failed: %v", err)
		}
		if !status.Running {
			t.Fatal("expected DBUI to be running")
		}
	})

	// Test: Start DBUI again (should be idempotent)
	t.Run("StartDBUI_Idempotent", func(t *testing.T) {
		if err := mgr.StartDBUI(ctx); err != nil {
			t.Fatalf("StartDBUI (second time) failed: %v", err)
		}
	})

	// Test: HTTP health check - verifies Adminer loads without PHP errors
	t.Run("HTTPHealthCheck", func(t *testing.T) {
		// Test HTTP routing via Traefik with retries (Traefik needs time to pick up labels)
		// DefaultDomain (0ploy.dev) is a wildcard that resolves to 127.0.0.1
		url := fmt.Sprintf("http://db.shared.%s", cfg.Domain)
		client := &http.Client{Timeout: 5 * time.Second}

		var lastErr error
		for i := 0; i < 10; i++ {
			time.Sleep(1 * time.Second)

			resp, err := client.Get(url)
			if err != nil {
				lastErr = fmt.Errorf("HTTP request to Adminer failed: %w", err)
				continue
			}
			resp.Body.Close()

			// Check for 200 OK - a PHP fatal error would result in 500
			if resp.StatusCode == http.StatusOK {
				t.Logf("Adminer HTTP health check passed: %s", url)
				return
			}

			lastErr = fmt.Errorf("expected HTTP 200, got %d (possible PHP error in login-servers.php)", resp.StatusCode)
		}

		t.Fatalf("Adminer HTTP health check failed after retries: %v", lastErr)
	})

	// Test: Connect to project network
	t.Run("ConnectDBUIToProject", func(t *testing.T) {
		// Create a test network
		testNetwork := "zdev_dbui_test_project"
		_ = docker.RemoveNetwork(ctx, testNetwork)
		if err := docker.CreateNetwork(ctx, testNetwork); err != nil {
			t.Fatalf("CreateNetwork failed: %v", err)
		}
		defer docker.RemoveNetwork(ctx, testNetwork)

		dbui := defFor(t, DBUIContainerName)
		if err := dbui.Connect(ctx, mgr, testNetwork); err != nil {
			t.Fatalf("DBUI Connect failed: %v", err)
		}

		// Connect again should not error
		if err := dbui.Connect(ctx, mgr, testNetwork); err != nil {
			t.Fatalf("DBUI Connect (second time) failed: %v", err)
		}

		// Disconnect
		if err := dbui.Disconnect(ctx, mgr, testNetwork); err != nil {
			t.Fatalf("DBUI Disconnect failed: %v", err)
		}
	})

	// Test: Stop DBUI
	t.Run("StopDBUI", func(t *testing.T) {
		if err := defFor(t, DBUIContainerName).Stop(ctx, mgr); err != nil {
			t.Fatalf("DBUI Stop failed: %v", err)
		}

		status, err := mgr.DBUIStatus(ctx)
		if err != nil {
			t.Fatalf("DBUIStatus failed: %v", err)
		}
		if status.Running {
			t.Fatal("expected DBUI to be stopped")
		}
	})

}

// defFor returns the registry definition for a shared service by
// container name, so lifecycle tests exercise the exact closures the
// CLI and per-project connect flow use.
func defFor(t *testing.T, containerName string) SharedServiceDef {
	t.Helper()
	for _, def := range AllSharedServices() {
		if def.ContainerName == containerName {
			return def
		}
	}
	t.Fatalf("no shared service definition for %s", containerName)
	return SharedServiceDef{}
}

// requireAliases fails unless the router's endpoint on network carries every
// wanted alias.
func requireAliases(t *testing.T, ctx context.Context, docker *runtime.DockerCLI, network string, want ...string) {
	t.Helper()
	networks, err := docker.GetContainerNetworks(ctx, RouterContainerName)
	if err != nil {
		t.Fatalf("GetContainerNetworks: %v", err)
	}
	got, ok := networks[network]
	if !ok {
		t.Fatalf("router not attached to %s", network)
	}
	have := map[string]bool{}
	for _, a := range got {
		have[a] = true
	}
	for _, w := range want {
		if !have[w] {
			t.Errorf("router aliases on %s = %v, missing %s", network, got, w)
		}
	}
}

// routerEndpoint returns the router's endpoint ID on network; it changes
// exactly when the router is disconnected and reconnected.
func routerEndpoint(t *testing.T, network string) string {
	t.Helper()
	return dockerInspect(t, fmt.Sprintf(`{{(index .NetworkSettings.Networks %q).EndpointID}}`, network))
}

func dockerInspect(t *testing.T, format string) string {
	t.Helper()
	out, err := exec.Command("docker", "inspect", "-f", format, RouterContainerName).CombinedOutput()
	if err != nil {
		t.Fatalf("docker inspect: %v: %s", err, out)
	}
	return strings.TrimSpace(string(out))
}

// requireResolvesToRouter resolves host from a throwaway container on
// network and expects the router's IP there - not the public 127.0.0.1.
func requireResolvesToRouter(t *testing.T, ctx context.Context, network, host string) {
	t.Helper()
	routerIP := dockerInspect(t, fmt.Sprintf(`{{(index .NetworkSettings.Networks %q).IPAddress}}`, network))
	out, err := exec.CommandContext(ctx, "docker", "run", "--rm", "--network", network, config.TestImage, "getent", "hosts", host).CombinedOutput()
	if err != nil {
		t.Fatalf("resolving %s on %s: %v: %s", host, network, err, out)
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 || fields[0] != routerIP {
		t.Errorf("%s on %s resolves to %q, want the router at %s", host, network, strings.TrimSpace(string(out)), routerIP)
	}
}
