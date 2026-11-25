package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/runtime"
)

func TestAgentSourceAndDefaultService(t *testing.T) {
	p := newSyncTestProject(t, runtime.NewMockRuntime(), nil, []string{".:/app", "cache:/root/.cache"})
	src, err := p.AgentSource("app")
	if err != nil || src.ContainerPath != "/app" || src.HostPath != p.Dir {
		t.Fatalf("AgentSource = %+v, %v", src, err)
	}
	if s, err := p.AgentDefaultService(""); err != nil || s != "app" {
		t.Errorf("default service = %q, %v", s, err)
	}
	if _, err := p.AgentDefaultService("nope"); err == nil {
		t.Error("an unknown configured service must be an error")
	}
	p.Config.Services["worker"] = config.ServiceConfig{Image: "x", Volumes: []string{"data:/data"}}
	if _, err := p.AgentSource("worker"); err == nil {
		t.Error("a service without the project mount can't host the agent")
	}
}

func TestDockerSocketServices(t *testing.T) {
	p := newSyncTestProject(t, runtime.NewMockRuntime(), nil, []string{".:/app"})
	p.Config.Services["docker1"] = config.ServiceConfig{Image: "x", Volumes: []string{"/var/run/docker.sock:/var/run/docker.sock"}}
	p.Config.Services["orb"] = config.ServiceConfig{Image: "x", Volumes: []string{"/Users/me/.orbstack/run/docker.sock:/var/run/docker.sock:ro"}}
	p.Config.Services["db"] = config.ServiceConfig{Image: "x", Volumes: []string{"db_data:/var/lib/mysql"}}
	if got := strings.Join(p.DockerSocketServices(), ","); got != "docker1,orb" {
		t.Errorf("DockerSocketServices = %s", got)
	}
}

// The agent's mount of the project directory syncs .zdev and .git: the
// negations come LAST, after the built-in .git and the project's own .zdev
// ignore, and only for the agent's service and only for that mount.
func TestSessionIgnoresAgentNegations(t *testing.T) {
	t.Setenv("ZDEV_HOME", t.TempDir())
	p := newSyncTestProject(t, runtime.NewMockRuntime(), []string{"logs"}, []string{".:/app", "./logs:/var/log/app"})
	p.Config.Mutagen.Ignore = []string{"vendor", ".zdev"}
	base := []string{".git", ".DS_Store", "vendor", ".zdev"}
	mounts := p.serviceSyncMounts("app", p.Config.Services["app"])
	if got := p.sessionIgnores(mounts[0], base); strings.Join(got, ",") != strings.Join(base, ",") {
		t.Errorf("without agent mode: %v", got)
	}
	if _, err := SetAgentSyncService("shop", "app"); err != nil {
		t.Fatal(err)
	}
	for _, m := range mounts {
		got := p.sessionIgnores(m, base)
		if m.ContainerPath == "/app" {
			if strings.Join(got[len(got)-2:], ",") != "!.zdev,!.git" {
				t.Errorf("agent mount ignores = %v", got)
			}
		} else if len(got) != len(base) {
			t.Errorf("other mount %s got negations: %v", m.ContainerPath, got)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(os.Getenv("ZDEV_HOME"), "agent", "sync", "shop")); strings.TrimSpace(string(b)) != "app" {
		t.Errorf("marker = %q", b)
	}
}
