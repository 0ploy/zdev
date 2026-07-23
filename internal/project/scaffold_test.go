package project

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/runtime"
)

// newScaffoldTestProject builds a project with an "app" service backed by a
// real Dockerfile in a temp dir, and optionally writes a scaffold hook.
func newScaffoldTestProject(t *testing.T, mock *runtime.MockRuntime, services map[string]config.ServiceConfig, hook string) *Project {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".zdev"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".zdev", "Dockerfile"), []byte("FROM alpine:latest\n"), 0644); err != nil {
		t.Fatalf("write Dockerfile: %v", err)
	}
	if hook != "" {
		if err := os.WriteFile(filepath.Join(dir, ".zdev", hook), []byte("#!/bin/sh\necho scaffold\n"), 0644); err != nil {
			t.Fatalf("write hook: %v", err)
		}
	}
	return &Project{
		Dir:     dir,
		Config:  &config.ProjectConfig{Name: "scaffoldproj", Services: services},
		Runtime: mock,
	}
}

func appService() map[string]config.ServiceConfig {
	return map[string]config.ServiceConfig{"app": {Dockerfile: ".zdev/Dockerfile"}}
}

func TestScaffoldHookPath(t *testing.T) {
	t.Run("active hook found", func(t *testing.T) {
		p := newScaffoldTestProject(t, runtime.NewMockRuntime(), appService(), "scaffold.sh")
		if got, err := p.ScaffoldHookPath(); err != nil || got == "" {
			t.Fatal("expected an active scaffold hook path, got empty")
		}
	})
	t.Run("no hook", func(t *testing.T) {
		p := newScaffoldTestProject(t, runtime.NewMockRuntime(), appService(), "")
		if got, err := p.ScaffoldHookPath(); err != nil || got != "" {
			t.Fatalf("expected empty path, got %q", got)
		}
	})
	t.Run("disabled hook is ignored", func(t *testing.T) {
		p := newScaffoldTestProject(t, runtime.NewMockRuntime(), appService(), "scaffold.sh.disabled")
		if got, err := p.ScaffoldHookPath(); err != nil || got != "" {
			t.Fatalf("expected disabled hook to be ignored, got %q", got)
		}
	})
}

func TestDisableScaffoldHook(t *testing.T) {
	t.Run("renames active hook to .disabled", func(t *testing.T) {
		p := newScaffoldTestProject(t, runtime.NewMockRuntime(), appService(), "scaffold.sh")
		disabled, err := p.DisableScaffoldHook()
		if err != nil {
			t.Fatalf("DisableScaffoldHook: %v", err)
		}
		if filepath.Base(disabled) != "scaffold.sh.disabled" {
			t.Errorf("unexpected disabled path %q", disabled)
		}
		if _, err := os.Stat(disabled); err != nil {
			t.Errorf("expected disabled file to exist: %v", err)
		}
		// After disabling, no active hook remains.
		if active, err := p.ScaffoldHookPath(); err != nil || active != "" {
			t.Error("expected no active hook after disabling")
		}
	})
	t.Run("no-op without an active hook", func(t *testing.T) {
		p := newScaffoldTestProject(t, runtime.NewMockRuntime(), appService(), "")
		disabled, err := p.DisableScaffoldHook()
		if err != nil || disabled != "" {
			t.Fatalf("got (%q, %v), want (\"\", nil)", disabled, err)
		}
	})
}

func TestScaffoldService(t *testing.T) {
	t.Run("single service", func(t *testing.T) {
		p := newScaffoldTestProject(t, runtime.NewMockRuntime(),
			map[string]config.ServiceConfig{"web": {Dockerfile: ".zdev/Dockerfile"}}, "")
		name, _, err := p.scaffoldService()
		if err != nil || name != "web" {
			t.Fatalf("got (%q, %v), want (web, nil)", name, err)
		}
	})
	t.Run("prefers app when multiple", func(t *testing.T) {
		p := newScaffoldTestProject(t, runtime.NewMockRuntime(), map[string]config.ServiceConfig{
			"app": {Dockerfile: ".zdev/Dockerfile"},
			"db":  {Image: "mariadb:11.4"},
		}, "")
		name, _, err := p.scaffoldService()
		if err != nil || name != "app" {
			t.Fatalf("got (%q, %v), want (app, nil)", name, err)
		}
	})
	t.Run("errors when ambiguous", func(t *testing.T) {
		p := newScaffoldTestProject(t, runtime.NewMockRuntime(), map[string]config.ServiceConfig{
			"web": {Dockerfile: ".zdev/Dockerfile"},
			"db":  {Image: "mariadb:11.4"},
		}, "")
		if _, _, err := p.scaffoldService(); err == nil {
			t.Fatal("expected an error for ambiguous services")
		}
	})
}

func TestRunScaffold_BuildsAndRunsThrowaway(t *testing.T) {
	mock := runtime.NewMockRuntime()
	p := newScaffoldTestProject(t, mock, appService(), "scaffold.sh")

	if err := p.RunScaffold(context.Background()); err != nil {
		t.Fatalf("RunScaffold: %v", err)
	}

	// A dockerfile: service must be built, then the hook runs in a throwaway.
	if mock.CallCount("BuildImage") != 1 {
		t.Errorf("expected 1 BuildImage call, got %d", mock.CallCount("BuildImage"))
	}
	if mock.CallCount("RunContainer") != 1 {
		t.Fatalf("expected 1 RunContainer call, got %d", mock.CallCount("RunContainer"))
	}

	var cfg runtime.RunConfig
	for _, c := range mock.Calls {
		if c.Method == "RunContainer" {
			cfg = c.Args[1].(runtime.RunConfig)
		}
	}
	if cfg.Entrypoint != "sh" {
		t.Errorf("expected entrypoint override to sh, got %q", cfg.Entrypoint)
	}
	if len(cfg.Volumes) != 1 || cfg.Volumes[0].Source != p.Dir || cfg.Volumes[0].Target != "/app" {
		t.Errorf("expected project bind-mounted at /app, got %+v", cfg.Volumes)
	}
	if cfg.Image != "zdev-scaffoldproj-app:latest" {
		t.Errorf("unexpected image %q", cfg.Image)
	}
}
