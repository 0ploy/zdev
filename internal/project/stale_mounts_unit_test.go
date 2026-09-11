package project

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/runtime"
	"github.com/0ploy/zdev/internal/secrets"
)

// realMountinfoSample is taken from a report of the failure: a single-file
// bind of .env whose host file was replaced by an editor, plus ordinary
// entries that must not be flagged.
const realMountinfoSample = `427 396 0:41 / /app rw,relatime - overlay overlay rw
432 427 0:46 /a.ziethen/scale-shop-dev/etc/shopcockpit/.env//deleted /app/config/.env rw,relatime - ext4 /dev/vda1 rw
433 427 0:46 /a.ziethen/scale-shop-dev/etc/shopcockpit/.env.ai /app/config/.env.ai rw,relatime - ext4 /dev/vda1 rw
435 427 0:47 / /app/data rw,relatime - ext4 /dev/vda1 rw
`

func TestDeletedMountPoints(t *testing.T) {
	got := deletedMountPoints(realMountinfoSample)

	if len(got) != 1 {
		t.Fatalf("got %d deleted mounts (%v), want 1", len(got), got)
	}
	if got[0] != "/app/config/.env" {
		t.Errorf("deleted mount = %q, want %q", got[0], "/app/config/.env")
	}
}

func TestDeletedMountPoints_IgnoresGarbage(t *testing.T) {
	for _, input := range []string{"", "\n\n", "not a mountinfo line", "1 2 0:1 /x"} {
		if got := deletedMountPoints(input); len(got) != 0 {
			t.Errorf("deletedMountPoints(%q) = %v, want none", input, got)
		}
	}
}

// TestFileBindTargets_OnlySingleFiles pins that directory binds and named
// volumes are left out: only a bind of a single FILE is inode-resolved, so
// only those can go stale when the host file is replaced.
func TestFileBindTargets_OnlySingleFiles(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, "src"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "app.env"), []byte("A=1"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	p := &Project{
		Dir:     tmpDir,
		Config:  &config.ProjectConfig{Name: "shop"},
		Runtime: runtime.NewMockRuntime(),
	}

	targets := p.fileBindTargets(config.ServiceConfig{Volumes: []string{
		"./src:/app",
		"./app.env:/app/config/.env",
		"db_data:/var/lib/data",
		"./missing:/app/missing",
	}})

	if len(targets) != 1 || targets["/app/config/.env"] != filepath.Join(tmpDir, "app.env") {
		t.Errorf("fileBindTargets = %v, want only /app/config/.env -> the host file", targets)
	}
}

func TestStaleFileMounts_ReportsReplacedHostFile(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "app.env"), []byte("A=1"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	mock := runtime.NewMockRuntime()
	p := &Project{
		Dir: tmpDir,
		Config: &config.ProjectConfig{
			Name: "scale-shop",
			Services: map[string]config.ServiceConfig{
				"shopcockpit": {
					Image:   "alpine:latest",
					Volumes: []string{"./app.env:/app/config/.env"},
				},
			},
		},
		Runtime: mock,
		Secrets: &secrets.Mock{},
	}

	containerName := p.ContainerName("shopcockpit")
	mock.ContainersRunning[containerName] = true
	// The container no longer has the file at all - the failure as reported.
	mock.ExecOutputFunc = func(_ string, cmd []string) (string, error) {
		switch cmd[0] {
		case "cat":
			return realMountinfoSample, nil
		default:
			return "", errors.New("exit status 1")
		}
	}

	stale := p.StaleFileMounts(context.Background())
	if len(stale) != 1 {
		t.Fatalf("got %d stale mounts (%v), want 1", len(stale), stale)
	}
	if stale[0].ServiceName != "shopcockpit" || stale[0].ContainerPath != "/app/config/.env" {
		t.Errorf("stale mount = %+v, want shopcockpit /app/config/.env", stale[0])
	}
}

// TestStaleFileMounts_SkipsStoppedContainers keeps `zdev status` from paying
// an exec per service on a project that isn't running.
func TestStaleFileMounts_SkipsStoppedContainers(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "app.env"), []byte("A=1"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	mock := runtime.NewMockRuntime()
	p := &Project{
		Dir: tmpDir,
		Config: &config.ProjectConfig{
			Name: "shop",
			Services: map[string]config.ServiceConfig{
				"app": {Image: "alpine:latest", Volumes: []string{"./app.env:/app/config/.env"}},
			},
		},
		Runtime: mock,
		Secrets: &secrets.Mock{},
	}

	if stale := p.StaleFileMounts(context.Background()); len(stale) != 0 {
		t.Errorf("got %v, want no stale mounts for a stopped container", stale)
	}
	if mock.CallCount("ExecOutput") != 0 {
		t.Error("a stopped container was exec'd into")
	}
}

func TestIsStaleNetworkError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"docker's actual message", errors.New("Error response from daemon: failed to set up container networking: network 5fcd986172091087 not found"), true},
		{"by name", errors.New("network sw-test10.zdev not found"), true},
		{"unrelated failure", errors.New("Error response from daemon: no such image"), false},
		{"missing container", errors.New("Error response from daemon: No such container: app.shop.zdev"), false},
		{"nil", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isStaleNetworkError(tt.err); got != tt.want {
				t.Errorf("isStaleNetworkError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// TestStartService_RecreatesContainerPinnedToRemovedNetwork covers the failure
// a removed-and-recreated project network leaves behind: the container pins
// the old network ID, so Docker refuses to start it with a message naming an
// ID the user has never seen, and no amount of `zdev start` helps. zdev must
// recreate the container instead of surfacing that error.
func TestStartService_RecreatesContainerPinnedToRemovedNetwork(t *testing.T) {
	mock := runtime.NewMockRuntime()
	p := newTestProject(mock)

	containerName := p.ContainerName("app")
	mock.ContainersExist[containerName] = true
	mock.ContainersRunning[containerName] = false
	mock.ImagesExist["alpine:latest"] = true
	mock.Errors["StartContainer"] = errors.New(
		"Error response from daemon: failed to set up container networking: network 5fcd98617209 not found")

	// The error is injected for every StartContainer call, so the recreated
	// container fails to start too; what matters is that zdev got as far as
	// recreating it instead of giving up on the first failure.
	_ = p.startServiceWithMutagen(context.Background(), "app", p.Config.Services["app"], false, nil)

	var sequence []string
	for _, call := range mock.Calls {
		switch call.Method {
		case "StartContainer", "RemoveContainer", "CreateContainer":
			sequence = append(sequence, call.Method)
		}
	}

	want := []string{"StartContainer", "RemoveContainer", "CreateContainer", "StartContainer"}
	if len(sequence) != len(want) {
		t.Fatalf("call sequence = %v, want %v", sequence, want)
	}
	for i := range want {
		if sequence[i] != want[i] {
			t.Fatalf("call sequence = %v, want %v", sequence, want)
		}
	}
}

// TestStartService_KeepsUnrelatedStartErrors makes sure the recovery path is
// narrow: any other start failure must surface as-is, not trigger a recreate.
func TestStartService_KeepsUnrelatedStartErrors(t *testing.T) {
	mock := runtime.NewMockRuntime()
	p := newTestProject(mock)

	containerName := p.ContainerName("app")
	mock.ContainersExist[containerName] = true
	mock.ContainersRunning[containerName] = false
	mock.Errors["StartContainer"] = errors.New("Error response from daemon: driver failed programming external connectivity")

	err := p.startServiceWithMutagen(context.Background(), "app", p.Config.Services["app"], false, nil)
	if err == nil {
		t.Fatal("expected the start error to surface")
	}
	if mock.CallCount("RemoveContainer") != 0 {
		t.Error("the container was recreated for an unrelated start failure")
	}
}

// TestStaleFileMounts_IgnoresDeletedMarkerWhenTheMountStillWorks is the
// false-positive guard. On Docker Desktop's virtiofs a replaced host file
// leaves the "//deleted" marker in mountinfo while the container keeps reading
// the current content - measured, not assumed. Warning on the marker alone
// would fire on every editor save.
func TestStaleFileMounts_IgnoresDeletedMarkerWhenTheMountStillWorks(t *testing.T) {
	tmpDir := t.TempDir()
	hostFile := filepath.Join(tmpDir, "app.env")
	if err := os.WriteFile(hostFile, []byte("A=1"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	info, err := os.Stat(hostFile)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	mock := runtime.NewMockRuntime()
	p := &Project{
		Dir: tmpDir,
		Config: &config.ProjectConfig{
			Name: "scale-shop",
			Services: map[string]config.ServiceConfig{
				"shopcockpit": {Image: "alpine:latest", Volumes: []string{"./app.env:/app/config/.env"}},
			},
		},
		Runtime: mock,
		Secrets: &secrets.Mock{},
	}

	containerName := p.ContainerName("shopcockpit")
	mock.ContainersRunning[containerName] = true
	mock.ExecOutputFunc = func(_ string, cmd []string) (string, error) {
		switch cmd[0] {
		case "cat":
			return realMountinfoSample, nil
		case "stat":
			// The container sees exactly what the host has.
			return fmt.Sprintf("%d:%d\n", info.Size(), info.ModTime().Unix()), nil
		default:
			return "", nil
		}
	}

	if stale := p.StaleFileMounts(context.Background()); len(stale) != 0 {
		t.Errorf("got %v, want no warning for a mount that still reads correctly", stale)
	}
}

// TestStaleFileMounts_ReportsDivergedContent covers the other half: the file is
// still there inside the container, but it is the old inode's content.
func TestStaleFileMounts_ReportsDivergedContent(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "app.env"), []byte("A=1"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	mock := runtime.NewMockRuntime()
	p := &Project{
		Dir: tmpDir,
		Config: &config.ProjectConfig{
			Name: "scale-shop",
			Services: map[string]config.ServiceConfig{
				"shopcockpit": {Image: "alpine:latest", Volumes: []string{"./app.env:/app/config/.env"}},
			},
		},
		Runtime: mock,
		Secrets: &secrets.Mock{},
	}

	containerName := p.ContainerName("shopcockpit")
	mock.ContainersRunning[containerName] = true
	mock.ExecOutputFunc = func(_ string, cmd []string) (string, error) {
		switch cmd[0] {
		case "cat":
			return realMountinfoSample, nil
		case "stat":
			return "9999:1", nil // an older, larger file
		default:
			return "", nil
		}
	}

	stale := p.StaleFileMounts(context.Background())
	if len(stale) != 1 {
		t.Fatalf("got %d stale mounts (%v), want 1", len(stale), stale)
	}
}
