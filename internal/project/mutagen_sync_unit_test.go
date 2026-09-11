package project

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/runtime"
	"github.com/0ploy/zdev/internal/secrets"
)

// newSyncTestProject builds a project whose service declares the given volume
// entries, creating every relative directory source on disk so the mount
// discovery (which stats the host path) sees them as directories.
func newSyncTestProject(t *testing.T, mock *runtime.MockRuntime, dirs []string, volumes []string) *Project {
	t.Helper()

	tmpDir := t.TempDir()
	for _, dir := range dirs {
		if err := os.MkdirAll(filepath.Join(tmpDir, dir), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	return &Project{
		Dir: tmpDir,
		Config: &config.ProjectConfig{
			Name: "shop",
			Services: map[string]config.ServiceConfig{
				"app": {
					Image:   "alpine:latest",
					Command: "sleep infinity",
					Volumes: volumes,
				},
			},
		},
		Runtime: mock,
		Secrets: &secrets.Mock{},
	}
}

// TestGetMutagenSyncMounts_SingleBindKeepsLegacyNames pins the compatibility
// half of the naming rule: a service with one directory bind must keep the
// names it had before sync became per-mount, so upgrading doesn't strand its
// sync volume (and everything at an ignored path inside it) behind a new name.
func TestGetMutagenSyncMounts_SingleBindKeepsLegacyNames(t *testing.T) {
	mock := runtime.NewMockRuntime()
	p := newSyncTestProject(t, mock,
		[]string{"src"},
		[]string{"./src:/app", "db_data:/var/lib/data"})

	mounts := p.GetMutagenSyncMounts()
	if len(mounts) != 1 {
		t.Fatalf("got %d mounts, want 1", len(mounts))
	}
	if got, want := mounts[0].VolumeName, "sync.app.shop.zdev"; got != want {
		t.Errorf("VolumeName = %q, want %q", got, want)
	}
	if got, want := mounts[0].SessionName, "zdev-shop-app"; got != want {
		t.Errorf("SessionName = %q, want %q", got, want)
	}
}

// TestGetMutagenSyncMounts_MultipleBindsGetOwnNames is the regression test for
// the reported defect: a service with two directory binds used to produce two
// mounts sharing one session name and one volume name. The shared session name
// made the second mount's session silently skipped (the existence check found
// its sibling), and the shared volume name meant only one bind could be backed
// by the sync volume while the other degraded to a raw bind mount.
func TestGetMutagenSyncMounts_MultipleBindsGetOwnNames(t *testing.T) {
	mock := runtime.NewMockRuntime()
	p := newSyncTestProject(t, mock,
		[]string{"src", "etc/app"},
		[]string{"./etc/app:/opt/etc/app", "./src:/app"})

	mounts := p.GetMutagenSyncMounts()
	if len(mounts) != 2 {
		t.Fatalf("got %d mounts, want 2", len(mounts))
	}

	byPath := make(map[string]MutagenSyncMount, len(mounts))
	for _, mount := range mounts {
		byPath[mount.ContainerPath] = mount
	}

	app, ok := byPath["/app"]
	if !ok {
		t.Fatal("no sync mount produced for /app")
	}
	etc, ok := byPath["/opt/etc/app"]
	if !ok {
		t.Fatal("no sync mount produced for /opt/etc/app")
	}

	if app.SessionName == etc.SessionName {
		t.Errorf("both mounts share session name %q - the second one would never be created", app.SessionName)
	}
	if app.VolumeName == etc.VolumeName {
		t.Errorf("both mounts share volume name %q - only one bind could be backed by it", app.VolumeName)
	}
	if got, want := app.VolumeName, "sync.app-app.shop.zdev"; got != want {
		t.Errorf("/app VolumeName = %q, want %q", got, want)
	}
	if got, want := app.SessionName, "zdev-shop-app-app"; got != want {
		t.Errorf("/app SessionName = %q, want %q", got, want)
	}
	if got, want := etc.VolumeName, "sync.app-opt-etc-app.shop.zdev"; got != want {
		t.Errorf("/opt/etc/app VolumeName = %q, want %q", got, want)
	}
}

// TestTransformVolumesForMutagen_EveryDirBindGetsItsOwnVolume pins the other
// half of the defect: the container config used to look the sync mount up by
// service name alone, so whichever mount happened to be last won the volume and
// every other directory bind fell through to a raw bind of the host directory.
func TestTransformVolumesForMutagen_EveryDirBindGetsItsOwnVolume(t *testing.T) {
	mock := runtime.NewMockRuntime()
	p := newSyncTestProject(t, mock,
		[]string{"src", "etc/app"},
		[]string{"./etc/app:/opt/etc/app", "./src:/app"})

	volumes := []string{"./etc/app:/opt/etc/app", "./src:/app"}
	result := p.transformVolumesForMutagen("app", volumes, NewMutagenMounts(p.GetMutagenSyncMounts()))

	if len(result) != 2 {
		t.Fatalf("got %d volumes, want 2", len(result))
	}
	for _, vol := range result {
		if !strings.HasPrefix(vol.Source, "sync.") {
			t.Errorf("mount at %s uses source %q - it degraded to a raw bind mount", vol.Target, vol.Source)
		}
	}
	if result[0].Source == result[1].Source {
		t.Errorf("both mounts got the same volume %q", result[0].Source)
	}
}

// TestValidateMountNames_DetectsCollision covers the paths that slug to the
// same token. Letting them through would silently reintroduce the shared-name
// bug for those two mounts.
func TestValidateMountNames_DetectsCollision(t *testing.T) {
	mock := runtime.NewMockRuntime()
	p := newSyncTestProject(t, mock,
		[]string{"one", "two"},
		[]string{"./one:/app/x", "./two:/app-x"})

	mounts := p.GetMutagenSyncMounts()
	if len(mounts) != 2 {
		t.Fatalf("got %d mounts, want 2", len(mounts))
	}
	err := validateMountNames(mounts)
	if err == nil {
		t.Fatal("expected a collision error for /app/x and /app-x")
	}
	if !strings.Contains(err.Error(), "/app/x") || !strings.Contains(err.Error(), "/app-x") {
		t.Errorf("error should name both mount paths, got: %v", err)
	}
}

func TestValidateMountNames_AcceptsDistinctMounts(t *testing.T) {
	mock := runtime.NewMockRuntime()
	p := newSyncTestProject(t, mock,
		[]string{"src", "etc/app"},
		[]string{"./etc/app:/opt/etc/app", "./src:/app"})

	if err := validateMountNames(p.GetMutagenSyncMounts()); err != nil {
		t.Fatalf("validateMountNames returned %v, want nil", err)
	}
}

// TestSyncGateCommand_ReArmsGate pins that the wrapper clears the ready flag
// before waiting on it. The flag lives in the container's writable layer, so a
// container that was let through once would otherwise skip the gate on every
// later start and keep racing straight into a possibly-empty directory.
func TestSyncGateCommand_ReArmsGate(t *testing.T) {
	got := SyncGateCommand("npm run dev")

	clear := strings.Index(got, "rm -f "+syncReadyFlag)
	wait := strings.Index(got, "while [ ! -f "+syncReadyFlag)
	if clear == -1 {
		t.Fatalf("wrapper never clears the ready flag: %s", got)
	}
	if wait == -1 {
		t.Fatalf("wrapper never waits for the ready flag: %s", got)
	}
	if clear > wait {
		t.Errorf("wrapper waits before clearing the flag: %s", got)
	}
	if !strings.Contains(got, "touch "+syncWaitingFlag) {
		t.Errorf("wrapper never announces that it reached the gate: %s", got)
	}
	if !strings.Contains(got, "exec sh -c 'npm run dev'") {
		t.Errorf("wrapper does not exec the service command: %s", got)
	}
}

// TestSignalSyncReady_HoldsGateWhenAMountIsUnsynced is the second half of the
// reported failure: the gate used to be opened unconditionally, so a service
// whose sync never completed started against an empty directory, died in
// milliseconds, and took down the very endpoint its session needed to recover.
func TestSignalSyncReady_HoldsGateWhenAMountIsUnsynced(t *testing.T) {
	mock := runtime.NewMockRuntime()
	p := newSyncTestProject(t, mock,
		[]string{"src", "etc/app"},
		[]string{"./etc/app:/opt/etc/app", "./src:/app"})

	mounts := p.GetMutagenSyncMounts()
	failed := map[string]string{}
	for _, mount := range mounts {
		// Only the /opt/etc/app session made it - exactly the shape the
		// report measured, where the source tree at /app stays unsynced.
		if mount.ContainerPath != "/opt/etc/app" {
			failed[mount.SessionName] = "endpoint not connected"
		}
	}

	err := p.signalSyncReady(context.Background(), mounts, failed)
	if err == nil {
		t.Fatal("expected an error naming the unsynced mount")
	}
	if !strings.Contains(err.Error(), "/app") {
		t.Errorf("error should name the unsynced mount path, got: %v", err)
	}
	if execTouchedReadyFlag(mock) {
		t.Error("the sync-ready gate was opened even though a mount never synced")
	}
}

func TestSignalSyncReady_OpensGateWhenEverythingSynced(t *testing.T) {
	mock := runtime.NewMockRuntime()
	p := newSyncTestProject(t, mock,
		[]string{"src", "etc/app"},
		[]string{"./etc/app:/opt/etc/app", "./src:/app"})

	mounts := p.GetMutagenSyncMounts()

	if err := p.signalSyncReady(context.Background(), mounts, map[string]string{}); err != nil {
		t.Fatalf("signalSyncReady returned %v, want nil", err)
	}
	if !execTouchedReadyFlag(mock) {
		t.Error("the sync-ready gate was never opened")
	}
}

// execTouchedReadyFlag reports whether any recorded Exec call opened the gate.
func execTouchedReadyFlag(mock *runtime.MockRuntime) bool {
	for _, call := range mock.Calls {
		if call.Method != "Exec" || len(call.Args) < 2 {
			continue
		}
		cmd, ok := call.Args[1].([]string)
		if !ok {
			continue
		}
		if strings.Contains(strings.Join(cmd, " "), "touch "+syncReadyFlag) {
			return true
		}
	}
	return false
}

// TestSyncGateCommand_UsesAUserWritablePath is the non-root guard. The wrapper
// runs as the image's default user, which on percona, postgres, node and
// plenty of others cannot write to / - so a gate anchored there could neither
// be raised by zdev nor cleared by the container, leaving those services
// deadlocked or silently ungated. Measured: `touch /.zdev-sync-ready` as uid
// 1001 is "Permission denied", /tmp is 1777 everywhere a shell exists.
func TestSyncGateCommand_UsesAUserWritablePath(t *testing.T) {
	if !strings.HasPrefix(syncReadyFlag, "/tmp/") {
		t.Errorf("syncReadyFlag = %q, want a path writable by any container user", syncReadyFlag)
	}
	if !strings.HasPrefix(syncWaitingFlag, "/tmp/") {
		t.Errorf("syncWaitingFlag = %q, want a path writable by any container user", syncWaitingFlag)
	}

	got := SyncGateCommand("npm run dev")
	// The legacy marker is cleared too, so a root image behaves identically
	// and no stale copy is left behind for entrypoints still watching it.
	if !strings.Contains(got, legacySyncReadyFlag) {
		t.Errorf("wrapper does not clear the legacy marker: %s", got)
	}
}

// TestSignalSyncReady_TouchesBothMarkers pins the compatibility touch: the
// pre-/tmp path is still raised, as root, because template entrypoints were
// documented to wait on it themselves.
func TestSignalSyncReady_TouchesBothMarkers(t *testing.T) {
	mock := runtime.NewMockRuntime()
	p := newSyncTestProject(t, mock, []string{"src"}, []string{"./src:/app"})

	mounts := p.GetMutagenSyncMounts()

	if err := p.signalSyncReady(context.Background(), mounts, map[string]string{}); err != nil {
		t.Fatalf("signalSyncReady returned %v", err)
	}

	var touchedLegacyAsRoot bool
	for _, call := range mock.Calls {
		if call.Method != "Exec" || len(call.Args) < 4 {
			continue
		}
		cmd, _ := call.Args[1].([]string)
		opts, _ := call.Args[3].(runtime.ExecOptions)
		if strings.Contains(strings.Join(cmd, " "), "touch "+legacySyncReadyFlag) && opts.User == "root" {
			touchedLegacyAsRoot = true
		}
	}
	if !touchedLegacyAsRoot {
		t.Error("the legacy sync-ready marker was not raised as root")
	}
}
