package project

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/runtime"
	"github.com/0ploy/zdev/internal/secrets"
)

// setupTestEnv creates a temporary ZDEV_HOME and HOME directory with a
// global-config.yaml that has Mutagen disabled. Returns a cleanup function.
func setupTestEnv(t *testing.T) func() {
	t.Helper()

	tmpDir := t.TempDir()

	// Create .zdev dir inside tmp (for state.DefaultManager which uses HOME)
	zdevDir := filepath.Join(tmpDir, ".zdev")
	if err := os.MkdirAll(zdevDir, 0755); err != nil {
		t.Fatalf("failed to create .zdev dir: %v", err)
	}

	// Write global-config.yaml with Mutagen disabled
	globalConfig := []byte("version: 1\ndomain: 0ploy.dev\nruntime: docker\nmutagen:\n  enabled: \"false\"\n")
	if err := os.WriteFile(filepath.Join(zdevDir, "global-config.yaml"), globalConfig, 0644); err != nil {
		t.Fatalf("failed to write global-config.yaml: %v", err)
	}

	// Save and override env vars
	oldHome := os.Getenv("HOME")
	oldZdevHome := os.Getenv("ZDEV_HOME")

	os.Setenv("HOME", tmpDir)
	os.Setenv("ZDEV_HOME", zdevDir)

	return func() {
		os.Setenv("HOME", oldHome)
		os.Setenv("ZDEV_HOME", oldZdevHome)
	}
}

// newTestProject creates a Project with a MockRuntime and basic config for testing.
func newTestProject(mock *runtime.MockRuntime) *Project {
	return &Project{
		Dir: "/tmp/test",
		Config: &config.ProjectConfig{
			Name: "testproject",
			Services: map[string]config.ServiceConfig{
				"app": {Image: "alpine:latest", Command: "sleep infinity", Volumes: []string{"data:/data"}},
			},
			Shared: config.ProjectSharedConfig{
				Router:        false,
				Mail:          false,
				DBUI:          false,
				RedisInsights: false,
			},
		},
		Runtime: mock,
		Secrets: &secrets.Mock{},
	}
}

// =============================================================================
// parseVolumeMount tests
// =============================================================================

func TestParseVolumeMount_NamedVolume(t *testing.T) {
	source, target, isNamed := parseVolumeMount("db_data:/var/lib/data")
	if source != "db_data" {
		t.Errorf("source = %q, want %q", source, "db_data")
	}
	if target != "/var/lib/data" {
		t.Errorf("target = %q, want %q", target, "/var/lib/data")
	}
	if !isNamed {
		t.Error("isNamedVolume = false, want true")
	}
}

func TestParseVolumeMount_AbsoluteBindMount(t *testing.T) {
	source, target, isNamed := parseVolumeMount("/host/path:/container/path")
	if source != "/host/path" {
		t.Errorf("source = %q, want %q", source, "/host/path")
	}
	if target != "/container/path" {
		t.Errorf("target = %q, want %q", target, "/container/path")
	}
	if isNamed {
		t.Error("isNamedVolume = true, want false")
	}
}

func TestParseVolumeMount_RelativeBindMount(t *testing.T) {
	source, target, isNamed := parseVolumeMount("./src:/app")
	if source != "./src" {
		t.Errorf("source = %q, want %q", source, "./src")
	}
	if target != "/app" {
		t.Errorf("target = %q, want %q", target, "/app")
	}
	if isNamed {
		t.Error("isNamedVolume = true, want false")
	}
}

func TestParseVolumeMount_DotPath(t *testing.T) {
	source, target, isNamed := parseVolumeMount(".:/var/www/html")
	if source != "." {
		t.Errorf("source = %q, want %q", source, ".")
	}
	if target != "/var/www/html" {
		t.Errorf("target = %q, want %q", target, "/var/www/html")
	}
	if isNamed {
		t.Error("isNamedVolume = true, want false")
	}
}

func TestParseVolumeMount_NoColon(t *testing.T) {
	source, target, isNamed := parseVolumeMount("just-a-name")
	if source != "just-a-name" {
		t.Errorf("source = %q, want %q", source, "just-a-name")
	}
	if target != "just-a-name" {
		t.Errorf("target = %q, want %q", target, "just-a-name")
	}
	if isNamed {
		t.Error("isNamedVolume = true, want false")
	}
}

func TestParseVolumeMount_MultipleColons(t *testing.T) {
	// SplitN with n=2 splits on the first colon only
	// "C:\path:/container" -> first colon is after "C", so source="C", target="\path:/container"
	source, target, isNamed := parseVolumeMount("C:\\path:/container")
	if source != "C" {
		t.Errorf("source = %q, want %q", source, "C")
	}
	if target != "\\path:/container" {
		t.Errorf("target = %q, want %q", target, "\\path:/container")
	}
	// "C" doesn't start with / or . so it's treated as a named volume
	if !isNamed {
		t.Error("isNamedVolume = false, want true ('C' doesn't start with / or .)")
	}
}

// =============================================================================
// transformVolumesForMutagen tests
// =============================================================================

func TestTransformVolumesForMutagen_NamedVolumePassesThrough(t *testing.T) {
	mock := runtime.NewMockRuntime()
	p := newTestProject(mock)

	mutagenMounts := map[string]MutagenSyncMount{}
	volumes := []string{"db_data:/var/lib/data"}

	result := p.transformVolumesForMutagen("app", volumes, mutagenMounts)

	if len(result) != 1 {
		t.Fatalf("got %d volumes, want 1", len(result))
	}
	// Named volume should be prefixed with project name
	expected := "db_data.testproject.zdev"
	if result[0].Source != expected {
		t.Errorf("source = %q, want %q", result[0].Source, expected)
	}
	if result[0].Target != "/var/lib/data" {
		t.Errorf("target = %q, want %q", result[0].Target, "/var/lib/data")
	}
}

func TestTransformVolumesForMutagen_BindMountWithMutagenMatch(t *testing.T) {
	mock := runtime.NewMockRuntime()
	p := newTestProject(mock)

	mutagenMounts := map[string]MutagenSyncMount{
		"app": {
			ServiceName:   "app",
			HostPath:      "/tmp/test",
			ContainerPath: "/var/www/html",
			VolumeName:    "sync.app.testproject.zdev",
			SessionName:   "zdev-testproject-app",
		},
	}
	volumes := []string{".:/var/www/html"}

	result := p.transformVolumesForMutagen("app", volumes, mutagenMounts)

	if len(result) != 1 {
		t.Fatalf("got %d volumes, want 1", len(result))
	}
	if result[0].Source != "sync.app.testproject.zdev" {
		t.Errorf("source = %q, want %q", result[0].Source, "sync.app.testproject.zdev")
	}
	if result[0].Target != "/var/www/html" {
		t.Errorf("target = %q, want %q", result[0].Target, "/var/www/html")
	}
}

func TestTransformVolumesForMutagen_BindMountWithoutMutagenMatch(t *testing.T) {
	mock := runtime.NewMockRuntime()
	p := newTestProject(mock)

	mutagenMounts := map[string]MutagenSyncMount{}
	volumes := []string{"./config:/etc/app"}

	result := p.transformVolumesForMutagen("app", volumes, mutagenMounts)

	if len(result) != 1 {
		t.Fatalf("got %d volumes, want 1", len(result))
	}
	// Should stay as bind mount
	if result[0].Source != "./config" {
		t.Errorf("source = %q, want %q", result[0].Source, "./config")
	}
	if result[0].Target != "/etc/app" {
		t.Errorf("target = %q, want %q", result[0].Target, "/etc/app")
	}
}

func TestTransformVolumesForMutagen_MixedVolumes(t *testing.T) {
	mock := runtime.NewMockRuntime()
	p := newTestProject(mock)

	mutagenMounts := map[string]MutagenSyncMount{
		"app": {
			ServiceName:   "app",
			HostPath:      "/tmp/test",
			ContainerPath: "/app",
			VolumeName:    "sync.app.testproject.zdev",
			SessionName:   "zdev-testproject-app",
		},
	}
	volumes := []string{
		"db_data:/var/lib/data", // named volume
		".:/app",                // bind mount with Mutagen match
		"./logs:/var/log/app",   // bind mount without Mutagen match
	}

	result := p.transformVolumesForMutagen("app", volumes, mutagenMounts)

	if len(result) != 3 {
		t.Fatalf("got %d volumes, want 3", len(result))
	}

	// Named volume - prefixed
	if result[0].Source != "db_data.testproject.zdev" {
		t.Errorf("vol[0] source = %q, want %q", result[0].Source, "db_data.testproject.zdev")
	}

	// Bind mount replaced with Mutagen volume
	if result[1].Source != "sync.app.testproject.zdev" {
		t.Errorf("vol[1] source = %q, want %q", result[1].Source, "sync.app.testproject.zdev")
	}

	// Bind mount without match stays as-is
	if result[2].Source != "./logs" {
		t.Errorf("vol[2] source = %q, want %q", result[2].Source, "./logs")
	}
}

// =============================================================================
// Naming convention tests
// =============================================================================

func TestContainerName(t *testing.T) {
	mock := runtime.NewMockRuntime()
	p := newTestProject(mock)

	got := p.ContainerName("app")
	want := "app.testproject.zdev"
	if got != want {
		t.Errorf("ContainerName = %q, want %q", got, want)
	}

	got = p.ContainerName("db")
	want = "db.testproject.zdev"
	if got != want {
		t.Errorf("ContainerName = %q, want %q", got, want)
	}
}

func TestNetworkName(t *testing.T) {
	mock := runtime.NewMockRuntime()
	p := newTestProject(mock)

	got := p.NetworkName()
	want := "testproject.zdev"
	if got != want {
		t.Errorf("NetworkName = %q, want %q", got, want)
	}
}

func TestVolumeName(t *testing.T) {
	mock := runtime.NewMockRuntime()
	p := newTestProject(mock)

	got := p.VolumeName("data")
	want := "data.testproject.zdev"
	if got != want {
		t.Errorf("VolumeName = %q, want %q", got, want)
	}

	got = p.VolumeName("db_data")
	want = "db_data.testproject.zdev"
	if got != want {
		t.Errorf("VolumeName = %q, want %q", got, want)
	}
}

func TestMutagenSessionName(t *testing.T) {
	mock := runtime.NewMockRuntime()
	p := newTestProject(mock)

	got := p.MutagenSessionName("app")
	want := "zdev-testproject-app"
	if got != want {
		t.Errorf("MutagenSessionName = %q, want %q", got, want)
	}
}

func TestMutagenVolumeName(t *testing.T) {
	mock := runtime.NewMockRuntime()
	p := newTestProject(mock)

	got := p.MutagenVolumeName("app")
	want := "sync.app.testproject.zdev"
	if got != want {
		t.Errorf("MutagenVolumeName = %q, want %q", got, want)
	}
}

// TestGetMutagenSyncMounts_PropagatesPerServiceConfig pins that owner/group/
// mode values from a service's mutagen block end up on the produced
// MutagenSyncMount, so they reach mutagen.SessionConfig downstream.
func TestGetMutagenSyncMounts_PropagatesPerServiceConfig(t *testing.T) {
	tmpDir := t.TempDir()
	srcDir := filepath.Join(tmpDir, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}

	mock := runtime.NewMockRuntime()
	p := &Project{
		Dir: tmpDir,
		Config: &config.ProjectConfig{
			Name: "phpapp",
			Services: map[string]config.ServiceConfig{
				"web": {
					Image:   "my-php-apache:latest",
					Volumes: []string{"./src:/var/www/html"},
					Mutagen: config.ServiceMutagenConfig{
						User:          "www-data",
						Group:         "www-data",
						FileMode:      "0644",
						DirectoryMode: "0755",
					},
				},
			},
		},
		Runtime: mock,
	}

	mounts := p.GetMutagenSyncMounts()
	if len(mounts) != 1 {
		t.Fatalf("expected 1 mount, got %d", len(mounts))
	}
	m := mounts[0]
	if m.Owner != "www-data" {
		t.Errorf("Owner = %q, want www-data", m.Owner)
	}
	if m.Group != "www-data" {
		t.Errorf("Group = %q, want www-data", m.Group)
	}
	if m.FileMode != "0644" {
		t.Errorf("FileMode = %q, want 0644", m.FileMode)
	}
	if m.DirectoryMode != "0755" {
		t.Errorf("DirectoryMode = %q, want 0755", m.DirectoryMode)
	}
}

// =============================================================================
// Lifecycle tests: Start
// =============================================================================

func TestStart_CreatesNetworkAndVolumesAndContainers(t *testing.T) {
	cleanup := setupTestEnv(t)
	defer cleanup()

	mock := runtime.NewMockRuntime()
	p := newTestProject(mock)

	ctx := context.Background()
	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	// Network should be created
	if !mock.NetworksExist["testproject.zdev"] {
		t.Error("network testproject.zdev was not created")
	}

	// Volume should be created
	if !mock.VolumesExist["data.testproject.zdev"] {
		t.Error("volume data.testproject.zdev was not created")
	}

	// Container should exist and be running
	containerName := "app.testproject.zdev"
	if !mock.ContainersExist[containerName] {
		t.Error("container app.testproject.zdev was not created")
	}
	if !mock.ContainersRunning[containerName] {
		t.Error("container app.testproject.zdev is not running")
	}

	// Verify CreateNetwork was called
	if mock.CallCount("CreateNetwork") != 1 {
		t.Errorf("CreateNetwork called %d times, want 1", mock.CallCount("CreateNetwork"))
	}

	// Verify CreateVolume was called
	if mock.CallCount("CreateVolume") != 1 {
		t.Errorf("CreateVolume called %d times, want 1", mock.CallCount("CreateVolume"))
	}
}

func TestStart_SkipsNetworkCreationIfExists(t *testing.T) {
	cleanup := setupTestEnv(t)
	defer cleanup()

	mock := runtime.NewMockRuntime()
	mock.NetworksExist["testproject.zdev"] = true

	p := newTestProject(mock)

	ctx := context.Background()
	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	// Network should not have been created again
	if mock.CallCount("CreateNetwork") != 0 {
		t.Errorf("CreateNetwork called %d times, want 0 (network already existed)", mock.CallCount("CreateNetwork"))
	}
}

func TestStart_SkipsVolumeCreationIfExists(t *testing.T) {
	cleanup := setupTestEnv(t)
	defer cleanup()

	mock := runtime.NewMockRuntime()
	mock.VolumesExist["data.testproject.zdev"] = true

	p := newTestProject(mock)

	ctx := context.Background()
	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	if mock.CallCount("CreateVolume") != 0 {
		t.Errorf("CreateVolume called %d times, want 0 (volume already existed)", mock.CallCount("CreateVolume"))
	}
}

func TestStart_SkipsAlreadyRunningContainer(t *testing.T) {
	cleanup := setupTestEnv(t)
	defer cleanup()

	mock := runtime.NewMockRuntime()
	mock.ContainersExist["app.testproject.zdev"] = true
	mock.ContainersRunning["app.testproject.zdev"] = true

	p := newTestProject(mock)

	ctx := context.Background()
	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	// Should not create or start a new container
	if mock.CallCount("CreateContainer") != 0 {
		t.Errorf("CreateContainer called %d times, want 0", mock.CallCount("CreateContainer"))
	}
	// StartContainer should not be called since it's already running
	if mock.CallCount("StartContainer") != 0 {
		t.Errorf("StartContainer called %d times, want 0", mock.CallCount("StartContainer"))
	}
}

func TestStart_StartsStoppedContainer(t *testing.T) {
	cleanup := setupTestEnv(t)
	defer cleanup()

	mock := runtime.NewMockRuntime()
	mock.ContainersExist["app.testproject.zdev"] = true
	mock.ContainersRunning["app.testproject.zdev"] = false

	p := newTestProject(mock)

	ctx := context.Background()
	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	// Should start the existing container, not create a new one
	if mock.CallCount("CreateContainer") != 0 {
		t.Errorf("CreateContainer called %d times, want 0", mock.CallCount("CreateContainer"))
	}
	if mock.CallCount("StartContainer") != 1 {
		t.Errorf("StartContainer called %d times, want 1", mock.CallCount("StartContainer"))
	}
}

// =============================================================================
// Lifecycle tests: Stop
// =============================================================================

func TestStop_StopsRunningContainers(t *testing.T) {
	cleanup := setupTestEnv(t)
	defer cleanup()

	mock := runtime.NewMockRuntime()
	mock.ContainersExist["app.testproject.zdev"] = true
	mock.ContainersRunning["app.testproject.zdev"] = true

	p := newTestProject(mock)

	ctx := context.Background()
	if err := p.Stop(ctx); err != nil {
		t.Fatalf("Stop() error: %v", err)
	}

	if mock.CallCount("StopContainer") != 1 {
		t.Errorf("StopContainer called %d times, want 1", mock.CallCount("StopContainer"))
	}
	if !mock.CalledWith("StopContainer", "app.testproject.zdev") {
		t.Error("StopContainer was not called with app.testproject.zdev")
	}
}

func TestStop_SkipsNonExistentContainers(t *testing.T) {
	cleanup := setupTestEnv(t)
	defer cleanup()

	mock := runtime.NewMockRuntime()
	// Container does not exist
	p := newTestProject(mock)

	ctx := context.Background()
	if err := p.Stop(ctx); err != nil {
		t.Fatalf("Stop() error: %v", err)
	}

	if mock.CallCount("StopContainer") != 0 {
		t.Errorf("StopContainer called %d times, want 0", mock.CallCount("StopContainer"))
	}
}

func TestStop_SkipsAlreadyStoppedContainers(t *testing.T) {
	cleanup := setupTestEnv(t)
	defer cleanup()

	mock := runtime.NewMockRuntime()
	mock.ContainersExist["app.testproject.zdev"] = true
	mock.ContainersRunning["app.testproject.zdev"] = false

	p := newTestProject(mock)

	ctx := context.Background()
	if err := p.Stop(ctx); err != nil {
		t.Fatalf("Stop() error: %v", err)
	}

	if mock.CallCount("StopContainer") != 0 {
		t.Errorf("StopContainer called %d times, want 0 (container already stopped)", mock.CallCount("StopContainer"))
	}
}

// =============================================================================
// Lifecycle tests: StartService
// =============================================================================

// newMultiServiceTestProject builds a project with two services for filter tests.
func newMultiServiceTestProject(mock *runtime.MockRuntime) *Project {
	return &Project{
		Dir: "/tmp/test",
		Config: &config.ProjectConfig{
			Name: "testproject",
			Services: map[string]config.ServiceConfig{
				"app": {Image: "alpine:latest", Command: "sleep infinity"},
				"db":  {Image: "postgres:16", Command: "postgres"},
			},
		},
		Runtime: mock,
	}
}

func TestStartService_StartsOnlyNamedService(t *testing.T) {
	cleanup := setupTestEnv(t)
	defer cleanup()

	mock := runtime.NewMockRuntime()
	p := newMultiServiceTestProject(mock)

	ctx := context.Background()
	if err := p.StartService(ctx, "app"); err != nil {
		t.Fatalf("StartService() error: %v", err)
	}

	if !mock.ContainersRunning["app.testproject.zdev"] {
		t.Error("app container should be running")
	}
	if mock.ContainersExist["db.testproject.zdev"] {
		t.Error("db container should NOT have been created")
	}
}

func TestStartService_FailsForUnknownService(t *testing.T) {
	cleanup := setupTestEnv(t)
	defer cleanup()

	mock := runtime.NewMockRuntime()
	p := newMultiServiceTestProject(mock)

	ctx := context.Background()
	err := p.StartService(ctx, "doesnotexist")
	if err == nil {
		t.Fatal("StartService() expected error for unknown service, got nil")
	}
	if mock.CallCount("CreateContainer") != 0 {
		t.Error("Expected no container operations for unknown service")
	}
}

func TestStartService_RunsProjectSetupIdempotently(t *testing.T) {
	cleanup := setupTestEnv(t)
	defer cleanup()

	mock := runtime.NewMockRuntime()
	p := newMultiServiceTestProject(mock)

	ctx := context.Background()
	if err := p.StartService(ctx, "app"); err != nil {
		t.Fatalf("StartService() error: %v", err)
	}

	// Network should be created on first single-service start
	if !mock.NetworksExist["testproject.zdev"] {
		t.Error("network was not created")
	}
}

// =============================================================================
// Lifecycle tests: StopService
// =============================================================================

func TestStopService_StopsOnlyNamedService(t *testing.T) {
	cleanup := setupTestEnv(t)
	defer cleanup()

	mock := runtime.NewMockRuntime()
	mock.ContainersExist["app.testproject.zdev"] = true
	mock.ContainersRunning["app.testproject.zdev"] = true
	mock.ContainersExist["db.testproject.zdev"] = true
	mock.ContainersRunning["db.testproject.zdev"] = true

	p := newMultiServiceTestProject(mock)

	ctx := context.Background()
	if err := p.StopService(ctx, "app"); err != nil {
		t.Fatalf("StopService() error: %v", err)
	}

	if mock.CallCount("StopContainer") != 1 {
		t.Errorf("StopContainer called %d times, want 1", mock.CallCount("StopContainer"))
	}
	if !mock.CalledWith("StopContainer", "app.testproject.zdev") {
		t.Error("StopContainer was not called with app.testproject.zdev")
	}
	if mock.CalledWith("StopContainer", "db.testproject.zdev") {
		t.Error("StopContainer should NOT have been called for db")
	}
}

func TestStopService_NoOpForAlreadyStoppedContainer(t *testing.T) {
	cleanup := setupTestEnv(t)
	defer cleanup()

	mock := runtime.NewMockRuntime()
	mock.ContainersExist["app.testproject.zdev"] = true
	mock.ContainersRunning["app.testproject.zdev"] = false

	p := newMultiServiceTestProject(mock)

	ctx := context.Background()
	if err := p.StopService(ctx, "app"); err != nil {
		t.Fatalf("StopService() error: %v", err)
	}

	if mock.CallCount("StopContainer") != 0 {
		t.Errorf("StopContainer called %d times, want 0 (already stopped)", mock.CallCount("StopContainer"))
	}
}

func TestStopService_NoOpForMissingContainer(t *testing.T) {
	cleanup := setupTestEnv(t)
	defer cleanup()

	mock := runtime.NewMockRuntime()
	p := newMultiServiceTestProject(mock)

	ctx := context.Background()
	if err := p.StopService(ctx, "app"); err != nil {
		t.Fatalf("StopService() error: %v", err)
	}

	if mock.CallCount("StopContainer") != 0 {
		t.Errorf("StopContainer called %d times, want 0 (container missing)", mock.CallCount("StopContainer"))
	}
}

func TestStopService_FailsForUnknownService(t *testing.T) {
	cleanup := setupTestEnv(t)
	defer cleanup()

	mock := runtime.NewMockRuntime()
	p := newMultiServiceTestProject(mock)

	ctx := context.Background()
	err := p.StopService(ctx, "doesnotexist")
	if err == nil {
		t.Fatal("StopService() expected error for unknown service, got nil")
	}
	if mock.CallCount("StopContainer") != 0 {
		t.Error("Expected no container operations for unknown service")
	}
}

// =============================================================================
// Lifecycle tests: RestartService
// =============================================================================

func TestRestartService_StopsAndStartsRunningContainer(t *testing.T) {
	cleanup := setupTestEnv(t)
	defer cleanup()

	mock := runtime.NewMockRuntime()
	mock.ContainersExist["app.testproject.zdev"] = true
	mock.ContainersRunning["app.testproject.zdev"] = true

	p := newTestProject(mock)

	ctx := context.Background()
	if err := p.RestartService(ctx, "app"); err != nil {
		t.Fatalf("RestartService() error: %v", err)
	}

	if mock.CallCount("StopContainer") != 1 {
		t.Errorf("StopContainer called %d times, want 1", mock.CallCount("StopContainer"))
	}
	if mock.CallCount("StartContainer") != 1 {
		t.Errorf("StartContainer called %d times, want 1", mock.CallCount("StartContainer"))
	}
	if !mock.CalledWith("StartContainer", "app.testproject.zdev") {
		t.Error("StartContainer was not called with app.testproject.zdev")
	}
}

func TestRestartService_StartsAlreadyStoppedContainer(t *testing.T) {
	cleanup := setupTestEnv(t)
	defer cleanup()

	mock := runtime.NewMockRuntime()
	mock.ContainersExist["app.testproject.zdev"] = true
	mock.ContainersRunning["app.testproject.zdev"] = false

	p := newTestProject(mock)

	ctx := context.Background()
	if err := p.RestartService(ctx, "app"); err != nil {
		t.Fatalf("RestartService() error: %v", err)
	}

	if mock.CallCount("StopContainer") != 0 {
		t.Errorf("StopContainer called %d times, want 0 (already stopped)", mock.CallCount("StopContainer"))
	}
	if mock.CallCount("StartContainer") != 1 {
		t.Errorf("StartContainer called %d times, want 1", mock.CallCount("StartContainer"))
	}
}

func TestRestartService_FailsForUnknownService(t *testing.T) {
	cleanup := setupTestEnv(t)
	defer cleanup()

	mock := runtime.NewMockRuntime()
	p := newTestProject(mock)

	ctx := context.Background()
	err := p.RestartService(ctx, "doesnotexist")
	if err == nil {
		t.Fatal("RestartService() expected error for unknown service, got nil")
	}
	if mock.CallCount("StopContainer") != 0 || mock.CallCount("StartContainer") != 0 {
		t.Error("Expected no container operations for unknown service")
	}
}

func TestRestartService_FailsWhenContainerMissing(t *testing.T) {
	cleanup := setupTestEnv(t)
	defer cleanup()

	mock := runtime.NewMockRuntime()
	// Container intentionally not created
	p := newTestProject(mock)

	ctx := context.Background()
	err := p.RestartService(ctx, "app")
	if err == nil {
		t.Fatal("RestartService() expected error when container missing, got nil")
	}
	if mock.CallCount("StartContainer") != 0 {
		t.Error("StartContainer should not be called when container is missing")
	}
}

// =============================================================================
// Lifecycle tests: Down
// =============================================================================

func TestDown_StopsAndRemovesContainersAndNetwork(t *testing.T) {
	cleanup := setupTestEnv(t)
	defer cleanup()

	mock := runtime.NewMockRuntime()
	mock.ContainersExist["app.testproject.zdev"] = true
	mock.ContainersRunning["app.testproject.zdev"] = true
	mock.NetworksExist["testproject.zdev"] = true

	p := newTestProject(mock)

	ctx := context.Background()
	if err := p.Down(ctx, false); err != nil {
		t.Fatalf("Down() error: %v", err)
	}

	// Container should be stopped
	if mock.CallCount("StopContainer") != 1 {
		t.Errorf("StopContainer called %d times, want 1", mock.CallCount("StopContainer"))
	}

	// Container should be removed
	if mock.CallCount("RemoveContainer") != 1 {
		t.Errorf("RemoveContainer called %d times, want 1", mock.CallCount("RemoveContainer"))
	}

	// Network should be removed
	if mock.CallCount("RemoveNetwork") != 1 {
		t.Errorf("RemoveNetwork called %d times, want 1", mock.CallCount("RemoveNetwork"))
	}
}

func TestDown_SkipsStopForNonRunningContainers(t *testing.T) {
	cleanup := setupTestEnv(t)
	defer cleanup()

	mock := runtime.NewMockRuntime()
	mock.ContainersExist["app.testproject.zdev"] = true
	mock.ContainersRunning["app.testproject.zdev"] = false
	mock.NetworksExist["testproject.zdev"] = true

	p := newTestProject(mock)

	ctx := context.Background()
	if err := p.Down(ctx, false); err != nil {
		t.Fatalf("Down() error: %v", err)
	}

	// Should not stop (already stopped), but should remove
	if mock.CallCount("StopContainer") != 0 {
		t.Errorf("StopContainer called %d times, want 0", mock.CallCount("StopContainer"))
	}
	if mock.CallCount("RemoveContainer") != 1 {
		t.Errorf("RemoveContainer called %d times, want 1", mock.CallCount("RemoveContainer"))
	}
}

func TestDown_SkipsNonExistentContainers(t *testing.T) {
	cleanup := setupTestEnv(t)
	defer cleanup()

	mock := runtime.NewMockRuntime()
	mock.NetworksExist["testproject.zdev"] = true

	p := newTestProject(mock)

	ctx := context.Background()
	if err := p.Down(ctx, false); err != nil {
		t.Fatalf("Down() error: %v", err)
	}

	if mock.CallCount("StopContainer") != 0 {
		t.Errorf("StopContainer called %d times, want 0", mock.CallCount("StopContainer"))
	}
	if mock.CallCount("RemoveContainer") != 0 {
		t.Errorf("RemoveContainer called %d times, want 0", mock.CallCount("RemoveContainer"))
	}
	// Network should still be removed
	if mock.CallCount("RemoveNetwork") != 1 {
		t.Errorf("RemoveNetwork called %d times, want 1", mock.CallCount("RemoveNetwork"))
	}
}

func TestDown_WithRemoveVolumes(t *testing.T) {
	cleanup := setupTestEnv(t)
	defer cleanup()

	mock := runtime.NewMockRuntime()
	mock.ContainersExist["app.testproject.zdev"] = true
	mock.ContainersRunning["app.testproject.zdev"] = true
	mock.NetworksExist["testproject.zdev"] = true
	mock.VolumesExist["data.testproject.zdev"] = true

	p := newTestProject(mock)

	ctx := context.Background()
	if err := p.Down(ctx, true); err != nil {
		t.Fatalf("Down(removeVolumes=true) error: %v", err)
	}

	// Volume should be removed
	if mock.CallCount("RemoveVolume") != 1 {
		t.Errorf("RemoveVolume called %d times, want 1", mock.CallCount("RemoveVolume"))
	}
	if !mock.CalledWith("RemoveVolume", "data.testproject.zdev") {
		t.Error("RemoveVolume was not called with data.testproject.zdev")
	}
}

func TestDown_WithRemoveVolumesRemovesStaleProjectVolumes(t *testing.T) {
	cleanup := setupTestEnv(t)
	defer cleanup()

	mock := runtime.NewMockRuntime()
	mock.VolumesExist["removed.testproject.zdev"] = true
	mock.VolumesExist["data.other.zdev"] = true

	p := newTestProject(mock)
	if err := p.Down(context.Background(), true); err != nil {
		t.Fatalf("Down(removeVolumes=true) error: %v", err)
	}

	if !mock.CalledWith("RemoveVolume", "removed.testproject.zdev") {
		t.Error("stale project volume was not removed")
	}
	if mock.CalledWith("RemoveVolume", "data.other.zdev") {
		t.Error("volume from another project must not be removed")
	}
}

func TestUpdateRemovesContainerForDeletedService(t *testing.T) {
	cleanup := setupTestEnv(t)
	defer cleanup()

	mock := runtime.NewMockRuntime()
	mock.NetworksExist["testproject.zdev"] = true
	mock.ContainersExist["old.testproject.zdev"] = true
	mock.ContainersRunning["old.testproject.zdev"] = true
	mock.ContainerLabels["old.testproject.zdev"] = map[string]string{
		"zdev.project": "testproject",
		"zdev.service": "old",
	}

	p := newTestProject(mock)
	if _, err := p.Update(context.Background()); err != nil {
		t.Fatalf("Update() error: %v", err)
	}

	if !mock.CalledWith("RemoveContainer", "old.testproject.zdev") {
		t.Error("container for deleted service was not removed")
	}
}

func TestDown_WithoutRemoveVolumes(t *testing.T) {
	cleanup := setupTestEnv(t)
	defer cleanup()

	mock := runtime.NewMockRuntime()
	mock.ContainersExist["app.testproject.zdev"] = true
	mock.ContainersRunning["app.testproject.zdev"] = true
	mock.NetworksExist["testproject.zdev"] = true
	mock.VolumesExist["data.testproject.zdev"] = true

	p := newTestProject(mock)

	ctx := context.Background()
	if err := p.Down(ctx, false); err != nil {
		t.Fatalf("Down(removeVolumes=false) error: %v", err)
	}

	// Volume should NOT be removed
	if mock.CallCount("RemoveVolume") != 0 {
		t.Errorf("RemoveVolume called %d times, want 0 (removeVolumes=false)", mock.CallCount("RemoveVolume"))
	}

	// Volume should still exist in mock state
	if !mock.VolumesExist["data.testproject.zdev"] {
		t.Error("volume data.testproject.zdev should still exist")
	}
}

func TestDown_RemovesNetworkEvenIfNoContainers(t *testing.T) {
	cleanup := setupTestEnv(t)
	defer cleanup()

	mock := runtime.NewMockRuntime()
	mock.NetworksExist["testproject.zdev"] = true

	p := newTestProject(mock)

	ctx := context.Background()
	if err := p.Down(ctx, false); err != nil {
		t.Fatalf("Down() error: %v", err)
	}

	if !mock.CalledWith("RemoveNetwork", "testproject.zdev") {
		t.Error("RemoveNetwork was not called with testproject.zdev")
	}
	if mock.NetworksExist["testproject.zdev"] {
		t.Error("network testproject.zdev should have been removed")
	}
}

func TestDown_SkipsNetworkRemovalIfNotExists(t *testing.T) {
	cleanup := setupTestEnv(t)
	defer cleanup()

	mock := runtime.NewMockRuntime()
	// No network exists

	p := newTestProject(mock)

	ctx := context.Background()
	if err := p.Down(ctx, false); err != nil {
		t.Fatalf("Down() error: %v", err)
	}

	if mock.CallCount("RemoveNetwork") != 0 {
		t.Errorf("RemoveNetwork called %d times, want 0 (network doesn't exist)", mock.CallCount("RemoveNetwork"))
	}
}
