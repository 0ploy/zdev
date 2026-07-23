package state

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestManager_RegisterAndList(t *testing.T) {
	// Create temp directory for test
	tmpDir, err := os.MkdirTemp("", "zdev-state-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	statePath := filepath.Join(tmpDir, "state.yaml")
	mgr := NewManager(statePath)

	// Initially empty
	projects, err := mgr.ListProjects()
	if err != nil {
		t.Fatalf("ListProjects failed: %v", err)
	}
	if len(projects) != 0 {
		t.Errorf("expected 0 projects, got %d", len(projects))
	}

	// Register a project
	err = mgr.RegisterProject("myproject", "/home/user/myproject")
	if err != nil {
		t.Fatalf("RegisterProject failed: %v", err)
	}

	// Should have one project now
	projects, err = mgr.ListProjects()
	if err != nil {
		t.Fatalf("ListProjects failed: %v", err)
	}
	if len(projects) != 1 {
		t.Errorf("expected 1 project, got %d", len(projects))
	}

	entry, ok := projects["myproject"]
	if !ok {
		t.Fatal("expected 'myproject' in projects")
	}
	if entry.Path != "/home/user/myproject" {
		t.Errorf("expected path '/home/user/myproject', got %q", entry.Path)
	}
	if entry.LastStarted.IsZero() {
		t.Error("expected LastStarted to be set")
	}

	// Register another project
	err = mgr.RegisterProject("other", "/home/user/other")
	if err != nil {
		t.Fatalf("RegisterProject failed: %v", err)
	}

	projects, err = mgr.ListProjects()
	if err != nil {
		t.Fatalf("ListProjects failed: %v", err)
	}
	if len(projects) != 2 {
		t.Errorf("expected 2 projects, got %d", len(projects))
	}
}

func TestManager_GetProject(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "zdev-state-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	statePath := filepath.Join(tmpDir, "state.yaml")
	mgr := NewManager(statePath)

	// Get non-existent project
	entry, err := mgr.GetProject("nonexistent")
	if err != nil {
		t.Fatalf("GetProject failed: %v", err)
	}
	if entry != nil {
		t.Error("expected nil for non-existent project")
	}

	// Register and get
	err = mgr.RegisterProject("myproject", "/some/path")
	if err != nil {
		t.Fatalf("RegisterProject failed: %v", err)
	}

	entry, err = mgr.GetProject("myproject")
	if err != nil {
		t.Fatalf("GetProject failed: %v", err)
	}
	if entry == nil {
		t.Fatal("expected non-nil entry")
	}
	if entry.Path != "/some/path" {
		t.Errorf("expected path '/some/path', got %q", entry.Path)
	}
}

func TestManager_UnregisterProject(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "zdev-state-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	statePath := filepath.Join(tmpDir, "state.yaml")
	mgr := NewManager(statePath)

	// Register two projects
	_ = mgr.RegisterProject("proj1", "/path/1")
	_ = mgr.RegisterProject("proj2", "/path/2")

	// Unregister one
	err = mgr.UnregisterProject("proj1")
	if err != nil {
		t.Fatalf("UnregisterProject failed: %v", err)
	}

	projects, err := mgr.ListProjects()
	if err != nil {
		t.Fatalf("ListProjects failed: %v", err)
	}
	if len(projects) != 1 {
		t.Errorf("expected 1 project, got %d", len(projects))
	}
	if _, ok := projects["proj1"]; ok {
		t.Error("proj1 should have been removed")
	}
	if _, ok := projects["proj2"]; !ok {
		t.Error("proj2 should still exist")
	}
}

func TestManager_UpdateExistingProject(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "zdev-state-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	statePath := filepath.Join(tmpDir, "state.yaml")
	mgr := NewManager(statePath)

	// Register project
	err = mgr.RegisterProject("myproject", "/old/path")
	if err != nil {
		t.Fatalf("RegisterProject failed: %v", err)
	}

	entry1, _ := mgr.GetProject("myproject")
	firstTime := entry1.LastStarted

	// Re-register with new path (simulates project move)
	err = mgr.RegisterProject("myproject", "/new/path")
	if err != nil {
		t.Fatalf("RegisterProject failed: %v", err)
	}

	entry2, _ := mgr.GetProject("myproject")
	if entry2.Path != "/new/path" {
		t.Errorf("expected path to be updated to '/new/path', got %q", entry2.Path)
	}
	if !entry2.LastStarted.After(firstTime) && entry2.LastStarted != firstTime {
		t.Error("expected LastStarted to be updated")
	}
}

func TestManager_MultipleInstancesDoNotLoseConcurrentUpdates(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.yaml")

	const count = 40
	var wg sync.WaitGroup
	errs := make(chan error, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			manager := NewManager(statePath)
			name := fmt.Sprintf("project-%d", index)
			errs <- manager.RegisterProject(name, "/tmp/"+name)
		}(i)
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("RegisterProject() error: %v", err)
		}
	}

	projects, err := NewManager(statePath).ListProjects()
	if err != nil {
		t.Fatalf("ListProjects() error: %v", err)
	}
	if len(projects) != count {
		t.Fatalf("got %d projects, want %d", len(projects), count)
	}
}
