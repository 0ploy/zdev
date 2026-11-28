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
	home := t.TempDir()
	sock := filepath.Join(home, ".docker", "run", "docker.sock")
	if err := os.MkdirAll(filepath.Dir(sock), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	orig := dockerSocketPaths
	dockerSocketPaths = func() []string { return []string{"/var/run/docker.sock", "/run/docker.sock", sock} }
	t.Cleanup(func() { dockerSocketPaths = orig })

	p := newSyncTestProject(t, runtime.NewMockRuntime(), nil, []string{".:/app"})
	// A link inside the project to the socket's directory: resolved against
	// the project, not the process's working directory.
	if err := os.Symlink(filepath.Dir(sock), filepath.Join(p.Dir, "engine")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "projects"), 0o755); err != nil {
		t.Fatal(err)
	}
	bind := map[string]string{
		"docker1":  "/var/run/docker.sock:/var/run/docker.sock",
		"orb":      "/Users/me/.orbstack/run/docker.sock:/var/run/docker.sock:ro",
		"varrun":   "/var/run:/host-run",
		"run":      "/run:/host-run",
		"root":     "/:/host:ro",
		"dotdock":  filepath.Join(home, ".docker") + ":/root/.docker",
		"rundir":   filepath.Dir(sock) + ":/x",
		"home":     home + ":/home/me",
		"linked":   "./engine:/x",
		"db":       "db_data:/var/lib/mysql",
		"src":      "./src:/src",
		"projects": filepath.Join(home, "projects") + ":/p",
		"varlib":   "/var/lib:/x",
	}
	for name, vol := range bind {
		p.Config.Services[name] = config.ServiceConfig{Image: "x", Volumes: []string{vol}}
	}
	want := "docker1,dotdock,home,linked,orb,root,run,rundir,varrun"
	if got := strings.Join(p.DockerSocketServices(), ","); got != want {
		t.Errorf("DockerSocketServices = %s, want %s", got, want)
	}
}

// The agent's mount of the project directory syncs the root's .zdev and .git: the
// negations come after the built-in .git and the project's own .zdev ignore,
// and only for the agent's service and only for that mount. The .git guards
// come last in every session of the project, so no negation lifts them.
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
		got := strings.Join(p.sessionIgnores(m, base), ",")
		// Spelled out: every guard is a path git on the Mac reads config,
		// hooks or a git directory from (review 2026-11-28, C2).
		guards := ",.git/hooks,.git/config,.git/commondir,.git/modules,.git/worktrees,.git/config.worktree"
		want := strings.Join(base, ",") + guards
		if m.ContainerPath == "/app" {
			// Anchored, .git as a directory only: nested repositories and a
			// .git file (linked worktree) stay ignored.
			want = strings.Join(base, ",") + ",!/.zdev,!/.git/" + guards
		}
		if got != want {
			t.Errorf("%s: ignores = %s, want %s", m.ContainerPath, got, want)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(os.Getenv("ZDEV_HOME"), "agent", "sync", "shop")); strings.TrimSpace(string(b)) != "app" {
		t.Errorf("marker = %q", b)
	}
}
