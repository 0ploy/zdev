//go:build integration

package project

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/runtime"
	"github.com/0ploy/zdev/internal/services"
	"github.com/0ploy/zdev/internal/state"
)

// createTempProject creates a temporary project directory with the given config
func createTempProject(t *testing.T, name string, cfg string) string {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "zdev-test-"+name)
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	zdevDir := filepath.Join(tmpDir, ".zdev")
	if err := os.MkdirAll(zdevDir, 0755); err != nil {
		t.Fatalf("failed to create .zdev dir: %v", err)
	}

	configPath := filepath.Join(zdevDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(cfg), 0644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	return tmpDir
}

// updateProjectConfig overwrites the project config file
func updateProjectConfig(t *testing.T, projectDir string, cfg string) {
	t.Helper()
	configPath := filepath.Join(projectDir, ".zdev", "config.yaml")
	if err := os.WriteFile(configPath, []byte(cfg), 0644); err != nil {
		t.Fatalf("failed to update config: %v", err)
	}
}

// cleanupRouter stops and removes the router for a clean test slate.
func cleanupRouter(t *testing.T, ctx context.Context) {
	t.Helper()
	docker := runtime.NewDockerCLI()
	_ = docker.StopContainer(ctx, services.RouterContainerName)
	_ = docker.RemoveContainer(ctx, services.RouterContainerName)
}

// snapshotSharedServices records which shared services are running and returns
// a restore function that restarts them. Call at the top of each test that
// tears down shared services.
func snapshotSharedServices(t *testing.T, ctx context.Context) func() {
	t.Helper()
	globalCfg, _ := config.LoadGlobalConfig()
	mgr := services.NewManager(globalCfg)

	routerRunning := false
	dbuiRunning := false
	if status, err := mgr.RouterStatus(ctx); err == nil {
		routerRunning = status.Running
	}
	if status, err := mgr.DBUIStatus(ctx); err == nil {
		dbuiRunning = status.Running
	}

	// Restarting is not enough: these tests remove the containers, and a
	// recreated shared service comes back on zdev_shared only. Without
	// restoring the project networks too, the developer's OTHER running
	// projects silently lose their routing for the rest of the day.
	networks := make(map[string]map[string][]string)
	for _, svc := range services.AllSharedServices() {
		networks[svc.ContainerName] = mgr.SnapshotProjectNetworks(ctx, svc.ContainerName)
	}

	return func() {
		restoreCfg, err := config.LoadGlobalConfig()
		if err != nil {
			return
		}
		restoreMgr := services.NewManager(restoreCfg)
		if routerRunning {
			_ = restoreMgr.StartRouter(ctx)
		}
		if dbuiRunning {
			_ = restoreMgr.StartDBUI(ctx)
		}
		for container, nets := range networks {
			restoreMgr.RestoreProjectNetworks(ctx, container, nets)
		}
	}
}

// cleanupProject runs Down and unregisters from state
func cleanupProject(t *testing.T, ctx context.Context, proj *Project) {
	t.Helper()
	_ = proj.Down(ctx, true)
	// Also unregister from state (project.Down doesn't do this)
	stateMgr, err := state.DefaultManager()
	if err == nil {
		_ = stateMgr.UnregisterProject(proj.Config.Name)
	}
}

// getTestServerBinary returns the absolute path to the test server binary
// for the current OS/architecture (used for running inside Docker on Linux)
func getTestServerBinary(t *testing.T) string {
	t.Helper()

	// Get the repo root by finding the go.mod file
	// We're in internal/project/, so go up two levels
	_, thisFile, _, ok := goruntime.Caller(0)
	if !ok {
		t.Fatal("failed to get current file path")
	}
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")

	// Docker runs Linux, so always use linux binary
	// Detect architecture from host (arm64 Mac runs arm64 Linux containers)
	arch := goruntime.GOARCH
	binaryName := fmt.Sprintf("testserver-linux-%s", arch)
	binaryPath := filepath.Join(repoRoot, "testdata", "bin", binaryName)

	absPath, err := filepath.Abs(binaryPath)
	if err != nil {
		t.Fatalf("failed to get absolute path: %v", err)
	}

	// Verify binary exists
	if _, err := os.Stat(absPath); err != nil {
		t.Fatalf("test server binary not found at %s (run 'make test-server' to build)", absPath)
	}

	return absPath
}

func TestRouting_HTTP(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	defer snapshotSharedServices(t, ctx)()

	testServerBinary := getTestServerBinary(t)
	projectDomain := fmt.Sprintf("routing-http-test.%s", config.DefaultDomain)

	// Create temp project with HTTP routing
	cfg := fmt.Sprintf(`
version: 1
name: routing-http-test
domain: %s
shared:
  router: true
services:
  web:
    image: %s
    volumes:
      - %s:/testserver
    command: /testserver -http=:80
    routing:
      protocol: http
      port: 80
`, projectDomain, config.TestImage, testServerBinary)
	projectDir := createTempProject(t, "http", cfg)
	defer os.RemoveAll(projectDir)

	// Load project
	proj, err := LoadFromDir(projectDir)
	if err != nil {
		t.Fatalf("LoadFromDir failed: %v", err)
	}

	// Cleanup any leftover resources
	defer func() {
		cleanupProject(t, ctx, proj)
		cleanupRouter(t, ctx)
	}()
	cleanupProject(t, ctx, proj)
	cleanupRouter(t, ctx) // Also clean up router to start fresh

	// Start project (this will also start the router)
	t.Log("Starting project with HTTP routing...")
	if err := proj.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Test HTTP routing with retries (Traefik needs time to pick up labels)
	// DefaultDomain (0ploy.dev) resolves to 127.0.0.1
	t.Log("Testing HTTP routing...")
	url := fmt.Sprintf("http://%s", projectDomain)
	client := &http.Client{Timeout: 5 * time.Second}

	var lastErr error
	for i := 0; i < 10; i++ {
		time.Sleep(1 * time.Second)

		resp, err := client.Get(url)
		if err != nil {
			lastErr = fmt.Errorf("HTTP request failed: %w", err)
			continue
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			t.Logf("HTTP routing works: %s", url)
			return
		}

		lastErr = fmt.Errorf("expected status 200, got %d: %s", resp.StatusCode, string(body))
	}

	t.Fatalf("HTTP routing failed after retries: %v", lastErr)
}

func TestRouting_TCP_WithPortChange(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	defer snapshotSharedServices(t, ctx)()

	testServerBinary := getTestServerBinary(t)

	// Create temp project with TCP routing
	initialCfg := fmt.Sprintf(`
version: 1
name: routing-tcp-test
shared:
  router: true
services:
  web:
    image: %s
    volumes:
      - %s:/testserver
    command: /testserver -http=:80
    routing:
      protocol: tcp
      port: 80
      host_port: 19080
`, config.TestImage, testServerBinary)
	projectDir := createTempProject(t, "tcp", initialCfg)
	defer os.RemoveAll(projectDir)

	// Load project
	proj, err := LoadFromDir(projectDir)
	if err != nil {
		t.Fatalf("LoadFromDir failed: %v", err)
	}

	// Cleanup any leftover resources
	defer func() {
		cleanupProject(t, ctx, proj)
		cleanupRouter(t, ctx)
	}()
	cleanupProject(t, ctx, proj)
	cleanupRouter(t, ctx) // Clean up router to start fresh

	// Start project (this will also start the router)
	t.Log("Starting project with TCP routing on port 19080...")
	if err := proj.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Test TCP routing on initial port (with retries for Traefik to pick up labels)
	t.Log("Testing TCP routing on port 19080...")
	if err := testTCPConnectionWithRetry("localhost:19080", 10); err != nil {
		t.Fatalf("TCP connection to port 19080 failed: %v", err)
	}
	t.Log("TCP routing on port 19080 works!")

	// Update config with new port
	updatedCfg := fmt.Sprintf(`
version: 1
name: routing-tcp-test
shared:
  router: true
services:
  web:
    image: %s
    volumes:
      - %s:/testserver
    command: /testserver -http=:80
    routing:
      protocol: tcp
      port: 80
      host_port: 19081
`, config.TestImage, testServerBinary)
	t.Log("Updating config to use port 19081...")
	updateProjectConfig(t, projectDir, updatedCfg)

	// Reload project and run Update
	proj, err = LoadFromDir(projectDir)
	if err != nil {
		t.Fatalf("LoadFromDir after update failed: %v", err)
	}

	updated, err := proj.Update(ctx)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	if !updated {
		t.Fatal("expected Update to report changes")
	}

	// Test new port works (with retries for Traefik to pick up labels)
	t.Log("Testing TCP routing on new port 19081...")
	if err := testTCPConnectionWithRetry("localhost:19081", 10); err != nil {
		t.Fatalf("TCP connection to port 19081 failed: %v", err)
	}

	// Test old port no longer works (should fail immediately)
	t.Log("Verifying old port 19080 no longer works...")
	if err := testTCPConnection("localhost:19080"); err == nil {
		t.Fatal("expected TCP connection to port 19080 to fail after update")
	}

	t.Log("TCP routing port change works!")
}

func TestRouting_UDP_WithPortChange(t *testing.T) {
	// Skip on macOS: UDP response routing through Traefik doesn't work in
	// Docker-on-Mac due to how macOS handles UDP NAT through the Docker VM.
	// Packets reach the container (confirmed via tcpdump) but responses don't
	// make it back. This test passes on Linux.
	//
	// To force run on Mac (for debugging): RUN_UDP_TEST=1 go test ...
	if goruntime.GOOS == "darwin" && os.Getenv("RUN_UDP_TEST") == "" {
		t.Skip("UDP routing doesn't work on Docker-on-Mac; skipping (runs on Linux)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	defer snapshotSharedServices(t, ctx)()

	testServerBinary := getTestServerBinary(t)

	// Create temp project with UDP routing using our test server
	initialCfg := fmt.Sprintf(`
version: 1
name: routing-udp-test
shared:
  router: true
services:
  echo:
    image: %s
    volumes:
      - %s:/testserver
    command: /testserver -http= -udp=:5353
    routing:
      protocol: udp
      port: 5353
      host_port: 19053
`, config.TestImage, testServerBinary)
	projectDir := createTempProject(t, "udp", initialCfg)
	defer os.RemoveAll(projectDir)

	// Load project
	proj, err := LoadFromDir(projectDir)
	if err != nil {
		t.Fatalf("LoadFromDir failed: %v", err)
	}

	// Cleanup any leftover resources
	defer func() {
		cleanupProject(t, ctx, proj)
		cleanupRouter(t, ctx)
	}()
	cleanupProject(t, ctx, proj)
	cleanupRouter(t, ctx) // Clean up router to start fresh

	// Start project (this will also start the router)
	t.Log("Starting project with UDP routing on port 19053...")
	if err := proj.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Wait for test server to be ready
	t.Log("Waiting for test server to start...")
	time.Sleep(3 * time.Second)

	// Debug: check container and router status
	docker := runtime.NewDockerCLI()
	containerName := proj.ContainerName("echo")
	running, _ := docker.IsContainerRunning(ctx, containerName)
	t.Logf("Container %s running: %v", containerName, running)

	// Check router networks
	routerContainer, _ := docker.GetContainer(ctx, services.RouterContainerName)
	if routerContainer != nil {
		t.Logf("Router running: %v", routerContainer.Running)
	}

	// Test using nc-style approach (send and receive separately)
	t.Log("Testing UDP routing on port 19053...")
	conn, err := net.DialTimeout("udp", "127.0.0.1:19053", 5*time.Second)
	if err != nil {
		t.Fatalf("UDP dial failed: %v", err)
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(10 * time.Second))

	msg := []byte("test-udp-ping")
	t.Logf("Sending %d bytes...", len(msg))
	n, err := conn.Write(msg)
	if err != nil {
		t.Fatalf("UDP write failed: %v", err)
	}
	t.Logf("Sent %d bytes", n)

	buf := make([]byte, 64)
	t.Log("Reading response...")
	n, err = conn.Read(buf)
	if err != nil {
		// Check container logs for debugging
		t.Logf("UDP read error: %v", err)
		t.Logf("Checking container logs...")
		_ = docker.Exec(ctx, containerName, []string{"cat", "/proc/1/fd/2"}, true, runtime.ExecOptions{})
		t.Fatalf("UDP read failed: %v", err)
	}
	t.Logf("Received %d bytes: %s", n, string(buf[:n]))
	t.Log("UDP routing on port 19053 works!")

	// Update config with new port
	updatedCfg := fmt.Sprintf(`
version: 1
name: routing-udp-test
shared:
  router: true
services:
  echo:
    image: %s
    volumes:
      - %s:/testserver
    command: /testserver -http= -udp=:5353
    routing:
      protocol: udp
      port: 5353
      host_port: 19054
`, config.TestImage, testServerBinary)
	t.Log("Updating config to use port 19054...")
	updateProjectConfig(t, projectDir, updatedCfg)

	// Reload project and run Update
	proj, err = LoadFromDir(projectDir)
	if err != nil {
		t.Fatalf("LoadFromDir after update failed: %v", err)
	}

	updated, err := proj.Update(ctx)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	if !updated {
		t.Fatal("expected Update to report changes")
	}

	// Wait for new container to be ready
	t.Log("Waiting for test server to start...")
	time.Sleep(3 * time.Second)

	// Test new port works
	t.Log("Testing UDP routing on new port 19054...")
	if err := testUDPConnection("127.0.0.1:19054"); err != nil {
		t.Fatalf("UDP connection to port 19054 failed: %v", err)
	}

	// Test old port no longer works (should fail immediately)
	t.Log("Verifying old port 19053 no longer works...")
	if err := testUDPConnection("127.0.0.1:19053"); err == nil {
		t.Fatal("expected UDP connection to port 19053 to fail after update")
	}

	t.Log("UDP routing port change works!")
}

func TestRouting_PortConflict(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	testServerBinary := getTestServerBinary(t)

	// Register a fake project with a TCP port in state
	stateMgr, err := state.DefaultManager()
	if err != nil {
		t.Fatalf("failed to get state manager: %v", err)
	}

	// Register fake project with port 19999
	if err := stateMgr.RegisterProjectWithRouting("fake-conflict-project", "/tmp/fake", []int{19999}, nil); err != nil {
		t.Fatalf("failed to register fake project: %v", err)
	}
	defer stateMgr.UnregisterProject("fake-conflict-project")

	// Create a project that tries to use the same port
	cfg := fmt.Sprintf(`
version: 1
name: routing-conflict-test
shared:
  router: true
services:
  web:
    image: %s
    volumes:
      - %s:/testserver
    command: /testserver -http=:80
    routing:
      protocol: tcp
      port: 80
      host_port: 19999
`, config.TestImage, testServerBinary)
	projectDir := createTempProject(t, "conflict", cfg)
	defer os.RemoveAll(projectDir)

	// Load project
	proj, err := LoadFromDir(projectDir)
	if err != nil {
		t.Fatalf("LoadFromDir failed: %v", err)
	}
	defer proj.Down(ctx, true)

	// Try to start - should fail with port conflict error
	t.Log("Starting project with conflicting port 19999...")
	err = proj.Start(ctx)
	if err == nil {
		t.Fatal("expected Start to fail due to port conflict")
	}

	if !strings.Contains(err.Error(), "fake-conflict-project") {
		t.Fatalf("expected error to mention 'fake-conflict-project', got: %v", err)
	}

	t.Logf("Got expected port conflict error: %v", err)
}

// testTCPConnectionWithRetry attempts TCP connection with retries
func testTCPConnectionWithRetry(addr string, maxRetries int) error {
	var lastErr error
	for i := 0; i < maxRetries; i++ {
		if i > 0 {
			time.Sleep(1 * time.Second)
		}
		if err := testTCPConnection(addr); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	return lastErr
}

// testUDPConnectionWithRetry attempts UDP connection with retries
func testUDPConnectionWithRetry(addr string, maxRetries int) error {
	var lastErr error
	for i := 0; i < maxRetries; i++ {
		if i > 0 {
			time.Sleep(1 * time.Second)
		}
		if err := testUDPConnection(addr); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	return lastErr
}

// testTCPConnection attempts a TCP connection and sends an HTTP request
func testTCPConnection(addr string) error {
	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		return fmt.Errorf("dial failed: %w", err)
	}
	defer conn.Close()

	// Set deadline for the whole operation
	conn.SetDeadline(time.Now().Add(5 * time.Second))

	// Send a simple HTTP request
	request := "GET / HTTP/1.1\r\nHost: test\r\nConnection: close\r\n\r\n"
	if _, err := conn.Write([]byte(request)); err != nil {
		return fmt.Errorf("write failed: %w", err)
	}

	// Read response
	buf := make([]byte, 1024)
	n, err := conn.Read(buf)
	if err != nil && err != io.EOF {
		return fmt.Errorf("read failed: %w", err)
	}

	response := string(buf[:n])
	if !strings.Contains(response, "HTTP/1.1 200") && !strings.Contains(response, "HTTP/1.0 200") {
		return fmt.Errorf("unexpected response: %s", response)
	}

	return nil
}

// testUDPConnection attempts a UDP connection and expects an echo response
func testUDPConnection(addr string) error {
	conn, err := net.DialTimeout("udp", addr, 3*time.Second)
	if err != nil {
		return fmt.Errorf("dial failed: %w", err)
	}
	defer conn.Close()

	// Set deadline
	conn.SetDeadline(time.Now().Add(3 * time.Second))

	// Send a test packet
	if _, err := conn.Write([]byte("ping")); err != nil {
		return fmt.Errorf("write failed: %w", err)
	}

	// Read response
	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	if err != nil {
		return fmt.Errorf("read failed (no response): %w", err)
	}

	if n == 0 {
		return fmt.Errorf("empty response")
	}

	return nil
}

func TestRouting_HTTPS(t *testing.T) {
	// Check if TLS certs exist - skip if not
	certsDir := config.GetCertsDir()
	certPath := filepath.Join(certsDir, "cert.pem")
	keyPath := filepath.Join(certsDir, "key.pem")

	if _, err := os.Stat(certPath); err != nil {
		t.Skip("TLS certs not found - run 'zdev systemcheck' first to generate certificates")
	}
	if _, err := os.Stat(keyPath); err != nil {
		t.Skip("TLS key not found - run 'zdev systemcheck' first to generate certificates")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	defer snapshotSharedServices(t, ctx)()

	testServerBinary := getTestServerBinary(t)
	projectDomain := fmt.Sprintf("routing-https-test.%s", config.DefaultDomain)

	// Create temp project with HTTP routing (which should also get HTTPS when TLS is available)
	cfg := fmt.Sprintf(`
version: 1
name: routing-https-test
domain: %s
shared:
  router: true
services:
  web:
    image: %s
    volumes:
      - %s:/testserver
    command: /testserver -http=:80
    routing:
      protocol: http
      port: 80
`, projectDomain, config.TestImage, testServerBinary)
	projectDir := createTempProject(t, "https", cfg)
	defer os.RemoveAll(projectDir)

	// Load project
	proj, err := LoadFromDir(projectDir)
	if err != nil {
		t.Fatalf("LoadFromDir failed: %v", err)
	}

	// Cleanup any leftover resources
	defer func() {
		cleanupProject(t, ctx, proj)
		cleanupRouter(t, ctx)
	}()
	cleanupProject(t, ctx, proj)
	cleanupRouter(t, ctx)

	// Start project (this will also start the router with TLS)
	t.Log("Starting project with HTTP routing (TLS enabled)...")
	if err := proj.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Create HTTP client (no TLS)
	httpClient := &http.Client{Timeout: 5 * time.Second}

	// Create HTTPS client (skip cert verification for self-signed certs)
	httpsClient := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
		},
	}

	// Test HTTP routing with retries
	// DefaultDomain (0ploy.dev) resolves to 127.0.0.1
	httpURL := fmt.Sprintf("http://%s", projectDomain)
	t.Logf("Testing HTTP routing: %s", httpURL)
	var lastErr error
	httpSuccess := false
	for i := 0; i < 10; i++ {
		time.Sleep(1 * time.Second)

		resp, err := httpClient.Get(httpURL)
		if err != nil {
			lastErr = fmt.Errorf("HTTP request failed: %w", err)
			continue
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			t.Log("HTTP routing works!")
			httpSuccess = true
			break
		}

		lastErr = fmt.Errorf("HTTP expected status 200, got %d: %s", resp.StatusCode, string(body))
	}

	if !httpSuccess {
		t.Fatalf("HTTP routing failed after retries: %v", lastErr)
	}

	// Test HTTPS routing with retries
	httpsURL := fmt.Sprintf("https://%s", projectDomain)
	t.Logf("Testing HTTPS routing: %s", httpsURL)
	httpsSuccess := false
	for i := 0; i < 10; i++ {
		time.Sleep(1 * time.Second)

		resp, err := httpsClient.Get(httpsURL)
		if err != nil {
			lastErr = fmt.Errorf("HTTPS request failed: %w", err)
			continue
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			t.Log("HTTPS routing works!")
			httpsSuccess = true
			break
		}

		lastErr = fmt.Errorf("HTTPS expected status 200, got %d: %s", resp.StatusCode, string(body))
	}

	if !httpsSuccess {
		t.Fatalf("HTTPS routing failed after retries: %v", lastErr)
	}

	t.Log("Both HTTP and HTTPS routing work!")
}
