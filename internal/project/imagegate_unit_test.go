package project

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/runtime"
	"github.com/0ploy/zdev/internal/secrets"
)

// gateTestProject is a project with Mutagen on and one synced service "app"
// without a command:, whose image the mock describes.
func gateTestProject(t *testing.T, image runtime.MockImageConfig) (*Project, *runtime.MockRuntime) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ZDEV_HOME", filepath.Join(home, ".zdev"))
	_ = os.MkdirAll(filepath.Join(home, ".zdev"), 0o755)
	if err := os.WriteFile(filepath.Join(home, ".zdev", config.GlobalConfigFilename),
		[]byte("version: 1\ndomain: 0ploy.dev\nmutagen:\n  enabled: \"true\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	mock := runtime.NewMockRuntime()
	mock.ImageConfigs = map[string]runtime.MockImageConfig{"img:1": image}
	return &Project{
		Dir: dir,
		Config: &config.ProjectConfig{
			Name:     "gatetest",
			Services: map[string]config.ServiceConfig{"app": {Image: "img:1", Volumes: []string{dir + ":/app"}}},
		},
		Runtime: mock,
		Secrets: &secrets.Mock{},
	}, mock
}

func (p *Project) gateTestConfig(t *testing.T) runtime.ContainerConfig {
	t.Helper()
	mounts := NewMutagenMounts(p.GetMutagenSyncMounts())
	p.resolveImageGate(context.Background(), "app", p.Config.Services["app"], true, mounts)
	return p.buildContainerConfig("app", p.Config.Services["app"], true, mounts)
}

func TestImageGateWrapsEntrypointAndCmd(t *testing.T) {
	for _, tc := range []struct {
		name  string
		image runtime.MockImageConfig
		args  []string
	}{
		{"zpinit supervise mode", runtime.MockImageConfig{Entrypoint: []string{"/usr/bin/zpinit"}, ID: "sha256:a"}, []string{"/usr/bin/zpinit"}},
		{"php-fpm", runtime.MockImageConfig{Entrypoint: []string{"docker-php-entrypoint"}, Cmd: []string{"php-fpm"}, ID: "sha256:b"}, []string{"docker-php-entrypoint", "php-fpm"}},
		{"cmd only", runtime.MockImageConfig{Cmd: []string{"nginx", "-g", "daemon off;"}, ID: "sha256:c"}, []string{"nginx", "-g", "daemon off;"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, _ := gateTestProject(t, tc.image)
			cfg := p.gateTestConfig(t)
			if cfg.Entrypoint != "sh" {
				t.Fatalf("Entrypoint = %q, want sh", cfg.Entrypoint)
			}
			want := append([]string{"-c", SyncGateEntrypoint(), syncGateScriptName}, tc.args...)
			if strings.Join(cfg.Command, "\x00") != strings.Join(want, "\x00") {
				t.Errorf("Command = %q\nwant %q", cfg.Command, want)
			}
			if !p.syncGated("app", p.Config.Services["app"]) {
				t.Error("a wrapped service isn't treated as gated, so awaitSyncGate wouldn't order itself against it")
			}
		})
	}
}

func TestImageGateWithoutShellStartsUnwrapped(t *testing.T) {
	p, mock := gateTestProject(t, runtime.MockImageConfig{Entrypoint: []string{"/app/server"}, ID: "sha256:d"})
	mock.Errors = map[string]error{"RunContainer": errors.New("exec: \"sh\": executable file not found")}
	cfg := p.gateTestConfig(t)
	if cfg.Entrypoint != "" || cfg.Command != nil {
		t.Errorf("image without sh wrapped anyway: %q %q", cfg.Entrypoint, cfg.Command)
	}
	if gate := p.imageGates["app"]; gate.Wrap || !strings.Contains(gate.Reason, "no sh") {
		t.Errorf("gate = %+v, want a no-sh reason for the warning", gate)
	}
	if p.syncGated("app", p.Config.Services["app"]) {
		t.Error("an unwrapped service is treated as gated: awaitSyncGate would wait for a flag nobody raises")
	}
}

func TestImageGateNothingToRun(t *testing.T) {
	p, _ := gateTestProject(t, runtime.MockImageConfig{ID: "sha256:e"})
	if cfg := p.gateTestConfig(t); cfg.Entrypoint != "" {
		t.Errorf("image without ENTRYPOINT/CMD wrapped: %q", cfg.Entrypoint)
	}
}

func TestImageGateOnlyForSyncedServicesWithoutCommand(t *testing.T) {
	p, mock := gateTestProject(t, runtime.MockImageConfig{Entrypoint: []string{"x"}, ID: "sha256:f"})
	svc := p.Config.Services["app"]
	svc.Command = "sleep infinity"
	p.Config.Services["app"] = svc
	cfg := p.gateTestConfig(t)
	if cfg.Entrypoint != "" || cfg.Command[0] != "sh" || !strings.Contains(cfg.Command[2], "exec sh -c") {
		t.Errorf("a configured command must keep the command wrapper: %q %q", cfg.Entrypoint, cfg.Command)
	}
	for _, c := range mock.Calls {
		if c.Method == "ImageEntrypoint" {
			t.Error("the image was inspected for a service with a command")
		}
	}
}

// The compare path must build the same config as the create path, or every
// zdev start would report (and zdev update recreate) a wrapped service.
func TestImageGateCompareMatchesCreate(t *testing.T) {
	p, mock := gateTestProject(t, runtime.MockImageConfig{Entrypoint: []string{"/usr/bin/zpinit"}, ID: "sha256:g"})
	created := p.gateTestConfig(t)
	name := p.ContainerName("app")
	mock.ContainersExist = map[string]bool{name: true}
	mock.ContainerLabels = map[string]map[string]string{name: created.Labels}

	fresh := *p // a later zdev run: no gates resolved yet
	fresh.imageGates = nil
	needs, err := fresh.serviceNeedsRecreate(context.Background(), "app", p.Config.Services["app"])
	if err != nil {
		t.Fatal(err)
	}
	if needs {
		t.Error("the compare path sees a different config than the create path")
	}
}

func TestImageShellCheckCached(t *testing.T) {
	p, mock := gateTestProject(t, runtime.MockImageConfig{Entrypoint: []string{"x"}, ID: "sha256:h"})
	p.gateTestConfig(t)
	again := *p
	again.imageGates = nil
	again.gateTestConfig(t)
	runs := 0
	for _, c := range mock.Calls {
		if c.Method == "RunContainer" {
			runs++
		}
	}
	if runs != 1 {
		t.Errorf("sh checked %d times for one image ID, want 1 (cached)", runs)
	}
}
