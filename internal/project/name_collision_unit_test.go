package project

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/runtime"
	"github.com/0ploy/zdev/internal/state"
)

func writeNamedProject(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".zdev"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "version: 1\nname: " + name + "\nservices:\n  app:\n    image: alpine:latest\n"
	if err := os.WriteFile(filepath.Join(dir, ".zdev", "config.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestNameCollision_SecondCheckoutRefused: a second checkout with the same
// name (a git worktree) used to overwrite the state path and adopt the first
// checkout's containers and volumes - `zdev down -v` there deleted them.
func TestNameCollision_SecondCheckoutRefused(t *testing.T) {
	cleanup := setupTestEnv(t)
	defer cleanup()

	first, second := t.TempDir(), t.TempDir()
	writeNamedProject(t, first, "shop")
	writeNamedProject(t, second, "shop")
	stateMgr, err := state.DefaultManager()
	if err != nil {
		t.Fatal(err)
	}
	if err := stateMgr.RegisterProject("shop", first); err != nil {
		t.Fatal(err)
	}

	mock := runtime.NewMockRuntime()
	p := &Project{
		Dir:     second,
		Config:  &config.ProjectConfig{Name: "shop", Services: map[string]config.ServiceConfig{"app": {Image: "alpine:latest"}}},
		Runtime: mock,
	}

	err = p.Down(context.Background(), true)
	if err == nil || !strings.Contains(err.Error(), first) {
		t.Fatalf("Down() error = %v, want a collision naming %s", err, first)
	}
	if n := mock.CallCount("RemoveVolume") + mock.CallCount("RemoveContainer"); n != 0 {
		t.Errorf("%d removals happened despite the collision", n)
	}
	if err := p.Start(context.Background()); err == nil {
		t.Error("Start() succeeded despite the collision")
	}
}

// TestNameCollision_MovedCheckoutTakesOver: a registered path that no longer
// holds the project is a moved directory, not a collision.
func TestNameCollision_MovedCheckoutTakesOver(t *testing.T) {
	cleanup := setupTestEnv(t)
	defer cleanup()

	moved := t.TempDir()
	writeNamedProject(t, moved, "shop")
	stateMgr, err := state.DefaultManager()
	if err != nil {
		t.Fatal(err)
	}
	if err := stateMgr.RegisterProject("shop", filepath.Join(t.TempDir(), "gone")); err != nil {
		t.Fatal(err)
	}

	p := &Project{Dir: moved, Config: &config.ProjectConfig{Name: "shop"}}
	if otherDir, err := p.detectNameCollision(); err != nil || otherDir != "" {
		t.Errorf("detectNameCollision() = (%q, %v), want no collision", otherDir, err)
	}
}
