//go:build integration

package project

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dockerRuntime "github.com/0ploy/zdev/internal/runtime"
)

// loadMultiMountFixture loads the fixture whose single service declares two
// directory binds, and leaves the project torn down after the test.
func loadMultiMountFixture(t *testing.T, ctx context.Context) *Project {
	t.Helper()

	projectDir, err := filepath.Abs(filepath.Join("..", "..", "testdata", "projects", "mutagen-multi"))
	if err != nil {
		t.Fatalf("failed to get absolute path: %v", err)
	}

	proj, err := LoadFromDir(projectDir)
	if err != nil {
		t.Fatalf("LoadFromDir failed: %v", err)
	}
	if !proj.IsMutagenEnabled() {
		t.Skip("Mutagen is not enabled for this project")
	}

	// Clear anything a previous run left behind, and tear down with a fresh
	// context so cleanup isn't aborted by the test's own cancellation.
	_ = proj.Down(ctx, true)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		_ = proj.Down(cleanupCtx, true)
	})

	return proj
}

// containerVolumeMounts maps each mount destination in a running container to
// the name of the Docker volume behind it. Bind mounts map to "".
func containerVolumeMounts(t *testing.T, ctx context.Context, containerName string) map[string]string {
	t.Helper()

	cmd := exec.CommandContext(ctx, "docker", "inspect", containerName,
		"--format", `{{range .Mounts}}{{.Destination}} {{.Name}}{{"\n"}}{{end}}`)
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("docker inspect %s: %v", containerName, err)
	}

	mounts := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) == 1 {
			mounts[fields[0]] = ""
			continue
		}
		mounts[fields[0]] = fields[1]
	}
	return mounts
}

// TestProject_MultiMountGivesEachBindItsOwnVolumeAndSession is the end-to-end
// regression test for the reported defect. With one sync volume and one
// session name per SERVICE, a service with two directory binds ended up with
// the session on one bind and the volume on the other: the volume was created
// and mounted but never filled by anything, and the bind that did have a
// session was a raw bind of the host directory syncing against itself.
func TestProject_MultiMountGivesEachBindItsOwnVolumeAndSession(t *testing.T) {
	mutagenPath := skipIfMutagenNotAvailable(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	proj := loadMultiMountFixture(t, ctx)
	docker := dockerRuntime.NewDockerCLI()
	containerName := proj.ContainerName("app")

	if err := proj.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// The service command needs content from BOTH binds, so a container that
	// is still running proves the gate opened only after both were filled.
	running, err := docker.IsContainerRunning(ctx, containerName)
	if err != nil {
		t.Fatalf("IsContainerRunning failed: %v", err)
	}
	if !running {
		logs, _ := exec.CommandContext(ctx, "docker", "logs", containerName).CombinedOutput()
		t.Fatalf("container exited instead of starting against synced content; logs: %s", logs)
	}

	mounts := proj.GetMutagenSyncMounts()
	if len(mounts) != 2 {
		t.Fatalf("got %d sync mounts, want 2 (one per directory bind)", len(mounts))
	}

	volumeMounts := containerVolumeMounts(t, ctx, containerName)
	seenVolumes := make(map[string]string)

	for _, mount := range mounts {
		// Each declared mount must have its own session...
		check := exec.CommandContext(ctx, mutagenPath, "sync", "list", mount.SessionName)
		if output, err := check.CombinedOutput(); err != nil {
			t.Errorf("no Mutagen session %s for mount %s: %v (%s)",
				mount.SessionName, mount.ContainerPath, err, output)
		}

		// ...and the sync volume has to be mounted at that same path, or the
		// session is filling a volume nothing reads.
		got := volumeMounts[mount.ContainerPath]
		if got != mount.VolumeName {
			t.Errorf("container path %s is backed by %q, want the sync volume %q",
				mount.ContainerPath, got, mount.VolumeName)
		}
		if prev, dup := seenVolumes[got]; dup {
			t.Errorf("volume %s backs both %s and %s", got, prev, mount.ContainerPath)
		}
		seenVolumes[got] = mount.ContainerPath
	}

	// Both host directories must actually be readable inside the container.
	for path, want := range map[string]string{
		"/app/marker":         "src marker",
		"/opt/etc/app/marker": "etc marker",
	} {
		out, err := exec.CommandContext(ctx, "docker", "exec", containerName, "cat", path).CombinedOutput()
		if err != nil {
			t.Errorf("reading %s in container: %v (%s)", path, err, out)
			continue
		}
		if strings.TrimSpace(string(out)) != want {
			t.Errorf("%s = %q, want %q", path, strings.TrimSpace(string(out)), want)
		}
	}
}

// TestProject_SyncGateReArmsOnPlainDockerStart pins the recovery half of the
// fix. The sync-ready flag lives in the container's writable layer, so before
// the wrapper cleared it on startup a container that had been let through once
// would skip the gate on every later start - racing straight into a directory
// nothing had filled yet, dying, and staying dead until someone thought to
// `docker rm` it. `zdev start` must be able to recover such a container.
func TestProject_SyncGateReArmsOnPlainDockerStart(t *testing.T) {
	_ = skipIfMutagenNotAvailable(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	proj := loadMultiMountFixture(t, ctx)
	containerName := proj.ContainerName("app")

	if err := proj.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Bounce the container behind zdev's back, the way a Docker Desktop
	// restart or a manual `docker start` would.
	if out, err := exec.CommandContext(ctx, "docker", "stop", containerName).CombinedOutput(); err != nil {
		t.Fatalf("docker stop: %v (%s)", err, out)
	}
	if out, err := exec.CommandContext(ctx, "docker", "start", containerName).CombinedOutput(); err != nil {
		t.Fatalf("docker start: %v (%s)", err, out)
	}

	// The wrapper must have cleared the stale flag and be holding at the gate.
	if err := waitForContainerFile(ctx, containerName, syncWaitingFlag, 30*time.Second); err != nil {
		t.Fatalf("container did not reach the sync-ready gate after a plain docker start: %v", err)
	}
	if containerFileExists(ctx, containerName, syncReadyFlag) {
		t.Error("the stale sync-ready flag survived the restart - the gate was skipped")
	}

	// And `zdev start` alone has to get it running again, with no docker rm.
	if err := proj.Start(ctx); err != nil {
		t.Fatalf("Start after restart failed: %v", err)
	}
	if err := waitForContainerFile(ctx, containerName, syncReadyFlag, 60*time.Second); err != nil {
		t.Fatalf("zdev start did not reopen the sync-ready gate: %v", err)
	}

	running, err := dockerRuntime.NewDockerCLI().IsContainerRunning(ctx, containerName)
	if err != nil {
		t.Fatalf("IsContainerRunning failed: %v", err)
	}
	if !running {
		logs, _ := exec.CommandContext(ctx, "docker", "logs", containerName).CombinedOutput()
		t.Fatalf("container is not running after recovery; logs: %s", logs)
	}
}

func containerFileExists(ctx context.Context, containerName, path string) bool {
	return exec.CommandContext(ctx, "docker", "exec", containerName, "test", "-f", path).Run() == nil
}

func waitForContainerFile(ctx context.Context, containerName, path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if containerFileExists(ctx, containerName, path) {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return context.DeadlineExceeded
}

// TestProject_StartRecoversContainerPinnedToRemovedNetwork covers a failure
// that is easy to walk into and impossible to read: a project network removed
// while its containers are merely stopped (a cleanup, a prune, another
// project's teardown) leaves those containers pinned to a network ID that no
// longer exists. Docker then refuses to start them, naming only that ID, and
// `zdev start` could never fix it because it reused the existing container.
func TestProject_StartRecoversContainerPinnedToRemovedNetwork(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	projectDir, err := filepath.Abs(filepath.Join("..", "..", "testdata", "projects", "minimal"))
	if err != nil {
		t.Fatalf("failed to get absolute path: %v", err)
	}
	proj, err := LoadFromDir(projectDir)
	if err != nil {
		t.Fatalf("LoadFromDir failed: %v", err)
	}

	_ = proj.Down(ctx, true)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cleanupCancel()
		_ = proj.Down(cleanupCtx, true)
	})

	if err := proj.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Stop the containers, then pull the network out from under them. Docker
	// allows this precisely because no container is running.
	if err := proj.Stop(ctx); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
	networkName := proj.NetworkName()
	// The shared services keep endpoints on the project network; detach them
	// the way a teardown would, so the network can actually be removed.
	detachSharedEndpoints(t, ctx, networkName)
	if out, err := exec.CommandContext(ctx, "docker", "network", "rm", networkName).CombinedOutput(); err != nil {
		t.Fatalf("docker network rm %s: %v (%s)", networkName, err, out)
	}

	// Plain docker cannot recover from this - confirm the trap is real before
	// asserting that zdev gets out of it.
	if out, err := exec.CommandContext(ctx, "docker", "network", "create", networkName).CombinedOutput(); err != nil {
		t.Fatalf("docker network create: %v (%s)", err, out)
	}
	containerName := proj.ContainerName("app")
	if out, err := exec.CommandContext(ctx, "docker", "start", containerName).CombinedOutput(); err == nil {
		t.Fatalf("expected a plain docker start to fail on the stale network, but it worked: %s", out)
	}

	if err := proj.Start(ctx); err != nil {
		t.Fatalf("Start did not recover the container: %v", err)
	}

	running, err := dockerRuntime.NewDockerCLI().IsContainerRunning(ctx, containerName)
	if err != nil {
		t.Fatalf("IsContainerRunning failed: %v", err)
	}
	if !running {
		t.Error("container is not running after recovery")
	}
}

// detachSharedEndpoints disconnects every container still attached to a
// network so it can be removed.
func detachSharedEndpoints(t *testing.T, ctx context.Context, networkName string) {
	t.Helper()

	out, err := exec.CommandContext(ctx, "docker", "network", "inspect", networkName,
		"--format", `{{range .Containers}}{{.Name}} {{end}}`).Output()
	if err != nil {
		return
	}
	for _, name := range strings.Fields(string(out)) {
		_ = exec.CommandContext(ctx, "docker", "network", "disconnect", "-f", networkName, name).Run()
	}
}

// TestProject_NoSyncKeepsABindNative pins the escape hatch for directories a
// container reads during startup. Everything else about sync is an
// optimisation; this is a correctness requirement, because a sync volume is
// empty until the session has been resumed and flushed, which happens after
// the container is already running. A plain bind is there from the first
// instant, so no_sync has to keep the mount native end to end.
func TestProject_NoSyncKeepsABindNative(t *testing.T) {
	_ = skipIfMutagenNotAvailable(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	projectDir, err := filepath.Abs(filepath.Join("..", "..", "testdata", "projects", "mutagen-nosync"))
	if err != nil {
		t.Fatalf("failed to get absolute path: %v", err)
	}
	proj, err := LoadFromDir(projectDir)
	if err != nil {
		t.Fatalf("LoadFromDir failed: %v", err)
	}
	if !proj.IsMutagenEnabled() {
		t.Skip("Mutagen is not enabled for this project")
	}

	_ = proj.Down(ctx, true)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cleanupCancel()
		_ = proj.Down(cleanupCtx, true)
	})

	if err := proj.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	mounts := proj.GetMutagenSyncMounts()
	if len(mounts) != 1 || mounts[0].ContainerPath != "/app" {
		t.Fatalf("sync mounts = %v, want only /app", mounts)
	}

	containerName := proj.ContainerName("app")
	types := containerMountTypes(t, ctx, containerName)
	if got := types["/docker-entrypoint-initdb.d"]; got != "bind" {
		t.Errorf("/docker-entrypoint-initdb.d is a %q mount, want a native bind", got)
	}
	if got := types["/app"]; got != "volume" {
		t.Errorf("/app is a %q mount, want the sync volume", got)
	}

	// Both are readable; no_sync changes the mechanism, not the content.
	for path, want := range map[string]string{
		"/docker-entrypoint-initdb.d/01-schema.sql": "01-schema",
		"/app/marker": "src marker",
	} {
		out, err := exec.CommandContext(ctx, "docker", "exec", containerName, "cat", path).CombinedOutput()
		if err != nil {
			t.Errorf("reading %s: %v (%s)", path, err, out)
			continue
		}
		if strings.TrimSpace(string(out)) != want {
			t.Errorf("%s = %q, want %q", path, strings.TrimSpace(string(out)), want)
		}
	}
}

// containerMountTypes maps each mount destination to its Docker mount type.
func containerMountTypes(t *testing.T, ctx context.Context, containerName string) map[string]string {
	t.Helper()

	cmd := exec.CommandContext(ctx, "docker", "inspect", containerName,
		"--format", `{{range .Mounts}}{{.Destination}} {{.Type}}{{"\n"}}{{end}}`)
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("docker inspect %s: %v", containerName, err)
	}

	types := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if fields := strings.Fields(line); len(fields) == 2 {
			types[fields[0]] = fields[1]
		}
	}
	return types
}
