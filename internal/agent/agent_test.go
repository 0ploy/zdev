package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCheckRequest(t *testing.T) {
	off := []string{"docker1"}
	allowed := [][]string{
		{"exec", "app", "ps", "aux"},
		{"exec", "-u", "www-data", "-w", "/app", "app", "php", "-v"},
		{"logs", "app", "--tail", "10"},
		{"logs", "docker1"}, // logs of a socket service stay allowed
		{"status"},
		{"info"},
		{"restart", "app"},
		{"restart", "docker1"}, // a bounce, no exec
		{"restart"},
		{"start", "--build"},
		{"stop"},
		{"update", "--refresh-secrets"},
	}
	for _, args := range allowed {
		if err := CheckRequest(args, off); err != nil {
			t.Errorf("%v refused: %v", args, err)
		}
	}
	refused := []struct {
		code string
		args []string
	}{
		{"not-allowed", []string{"down"}},
		{"not-allowed", []string{"cleanup"}},
		{"not-allowed", []string{"link", "create", "x"}},
		{"other-project", []string{"exec", "--config", "/elsewhere", "app", "ls"}},
		{"run-here", []string{"run", "test"}},
		{"docker-socket", []string{"exec", "-u", "root", "docker1", "docker", "ps"}},
	}
	for _, tc := range refused {
		err := CheckRequest(tc.args, off)
		var r *Refusal
		if !errors.As(err, &r) || r.Code != tc.code {
			t.Errorf("%v: got %v, want refusal %s", tc.args, err, tc.code)
		}
	}
	if err := CheckRequest([]string{"exec", "--config=/x", "app", "ls"}, nil); err == nil {
		t.Error("--config=... must be refused")
	}
}

func TestProjectKey(t *testing.T) {
	cases := map[string]string{
		"/app":                      "-app",
		"/Users/t.lohner/work/zdev": "-Users-t-lohner-work-zdev",
		"/var/www/html":             "-var-www-html",
	}
	for in, want := range cases {
		if got := ProjectKey(in); got != want {
			t.Errorf("ProjectKey(%q) = %q, want %q", in, got, want)
		}
	}
}

// The order of the rules is the security property: socket services and the
// host before the project subnet is accepted, granted host ports before the
// host is rejected, private ranges after the subnet.
func TestFenceScriptOrder(t *testing.T) {
	s := Fence{AllowSubnets: []string{"172.29.0.0/16"}, HostPorts: []int{11434}, BlockIPs: []string{"172.29.0.5"}}.Script()
	pos := func(sub string) int {
		i := strings.Index(s, sub)
		if i < 0 {
			t.Fatalf("script lacks %q:\n%s", sub, s)
		}
		return i
	}
	if !(pos("r -d 172.29.0.5 -j REJECT") < pos("r -d 172.29.0.0/16 -j ACCEPT")) {
		t.Error("a socket service must be rejected before the project subnet is accepted")
	}
	if !(pos("--dport 11434 -j ACCEPT") < pos("r -d $ip -j REJECT")) {
		t.Error("a granted host port must be accepted before the host is rejected")
	}
	if !(pos("r -d 172.29.0.0/16 -j ACCEPT") < pos("r -d 172.16.0.0/12 -j REJECT")) {
		t.Error("the project subnet must be accepted before the private ranges are rejected")
	}
}

func TestSweepable(t *testing.T) {
	now := time.Now()
	day := 24 * time.Hour
	uses := map[string]volumeUse{
		"zdev-claude-2.1.330-musl-arm64":  {First: now.Add(-2 * day), Last: now},               // current
		"zdev-claude-2.1.320-musl-arm64":  {First: now.Add(-9 * day), Last: now.Add(-2 * day)}, // superseded >1 day ago
		"zdev-claude-2.1.320-glibc-arm64": {First: now.Add(-9 * day), Last: now.Add(-1 * day)}, // other variant, recently used
		"zdev-tools-0.14.0-arm64":         {First: now.Add(-30 * day), Last: now.Add(-15 * day)},
	}
	got := sweepable(uses, []string{"zdev-claude-2.1.330-musl-arm64"}, now)
	want := []string{"zdev-claude-2.1.320-musl-arm64", "zdev-tools-0.14.0-arm64"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("sweepable = %v, want %v", got, want)
	}
	// A version first used less than a day ago doesn't supersede yet.
	uses["zdev-claude-2.1.330-musl-arm64"] = volumeUse{First: now.Add(-time.Hour), Last: now}
	if got := sweepable(uses, []string{"zdev-claude-2.1.330-musl-arm64"}, now); len(got) != 1 || got[0] != "zdev-tools-0.14.0-arm64" {
		t.Errorf("within the grace period: %v", got)
	}
}

func TestClaudePlatform(t *testing.T) {
	cases := [][3]string{{"arm64", LibcGlibc, "linux-arm64"}, {"arm64", LibcMusl, "linux-arm64-musl"}, {"amd64", LibcGlibc, "linux-x64"}, {"amd64", LibcMusl, "linux-x64-musl"}}
	for _, c := range cases {
		if got := ClaudePlatform(c[0], c[1]); got != c[2] {
			t.Errorf("ClaudePlatform(%s, %s) = %s, want %s", c[0], c[1], got, c[2])
		}
	}
}

func TestParseProbe(t *testing.T) {
	info := parseProbe("libc=musl\nos=Alpine Linux v3.23\npath=/usr/bin:/bin\nbash=1\ngit=1\nbusybox=grep\nbusybox=sed\ntool.php=8.4.19\ntool.composer=\n")
	if info.Libc != LibcMusl || info.Path != "/usr/bin:/bin" || !info.Bash || !info.Git {
		t.Errorf("parsed %+v", info)
	}
	if len(info.Missing()) != 0 {
		t.Errorf("Missing = %v", info.Missing())
	}
	if info.Tools["php"] != "8.4.19" || len(info.BusyBox) != 2 {
		t.Errorf("tools %v busybox %v", info.Tools, info.BusyBox)
	}
	if m := parseProbe("libc=glibc\n").Missing(); strings.Join(m, ",") != "bash,git" {
		t.Errorf("Missing = %v", m)
	}
}

func TestManagedSettings(t *testing.T) {
	var s map[string]any
	if err := json.Unmarshal(ManagedSettings(false, nil, false), &s); err != nil {
		t.Fatal(err)
	}
	if s["disableClaudeAiConnectors"] != true {
		t.Error("connectors must be off by default")
	}
	if p, _ := s["permissions"].(map[string]any); p["defaultMode"] != "bypassPermissions" {
		t.Errorf("permissions = %v", s["permissions"])
	}
	if s["hooks"] != nil {
		t.Errorf("no turn hook unless asked: %v", s["hooks"])
	}
	s = nil
	_ = json.Unmarshal(ManagedSettings(true, []string{"codex@openai-codex"}, true), &s)
	if !strings.Contains(fmt.Sprint(s["hooks"]), TurnEndHook) {
		t.Errorf("hooks = %v, want the Stop hook %s", s["hooks"], TurnEndHook)
	}
	if s["disableClaudeAiConnectors"] != false {
		t.Error("connectors on when the project allows them")
	}
	if ep, _ := s["enabledPlugins"].(map[string]any); ep["codex@openai-codex"] != false {
		t.Errorf("enabledPlugins = %v", s["enabledPlugins"])
	}
}

func TestSystemPrompt(t *testing.T) {
	p := SystemPrompt(PromptData{
		Project: "shop", Service: "app", Image: "shop-app", Workdir: "/app", URL: "https://shop.0ploy.dev",
		Services: []PromptService{{Name: "app", Port: 8000}, {Name: "db"}},
		Info:     &ImageInfo{Libc: LibcMusl, Tools: map[string]string{"zpctl": ""}},
		Root:     true, Fenced: true, HostAccess: []int{11434}, Refused: []string{"docker1"},
	})
	for _, want := range []string{
		"only Claude Code", "zdev exec app ps aux", "zdev exec app zpctl status", "`db`: `zdev exec db <cmd>`",
		"http://app:8000", "https://shop.0ploy.dev", "Don't start a second instance", "every capability dropped",
		"off limits", "docker1", "except ports 11434", ".git/hooks",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	if strings.Contains(p, "\u2014") {
		t.Error("no em dashes")
	}
}

func TestCompareClaudeVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"2.1.326", "2.1.326", 0},
		{"2.1.330", "2.1.326", 1},
		{"2.1.9", "2.1.10", -1},
		{"3.0.0", "2.9.99", 1},
		{"2.1.0-beta.1", "2.1.0", -1},
		{"2.1.0-beta.2", "2.1.0-beta.1", 1},
	}
	for _, c := range cases {
		if got := CompareClaudeVersions(c.a, c.b); got != c.want {
			t.Errorf("CompareClaudeVersions(%s, %s) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
	if !IsPinnedClaude("2.1.325") || IsPinnedClaude("stable") || IsPinnedClaude("latest") {
		t.Error("IsPinnedClaude")
	}
}

// A resolved release is reused until it is maxAge old: a session start asks
// the release folder at most once a day.
func TestResolveClaudeUsesCache(t *testing.T) {
	dir := t.TempDir()
	cached := ClaudeRelease{Channel: "stable", Version: "2.1.300", ResolvedAt: time.Now().Add(-20 * time.Hour),
		Platforms: map[string]ClaudeBuild{"linux-arm64": {SHA256: strings.Repeat("a", 64)}}}
	b, _ := json.Marshal(cached)
	_ = os.MkdirAll(filepath.Join(dir, "claude"), 0o700)
	if err := os.WriteFile(filepath.Join(dir, "claude", "release.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	rel, err := ResolveClaude(context.Background(), dir, "stable", ClaudeCheckEvery)
	if err != nil || rel.Version != "2.1.300" {
		t.Fatalf("got %v, %v; want the cached 2.1.300", rel, err)
	}
}

func TestPrepareProjectDir(t *testing.T) {
	root := t.TempDir()
	agentDir, hostClaude := filepath.Join(root, "agent"), filepath.Join(root, "claude")
	hostProject := "/Users/me/work/shop"
	hostDir := filepath.Join(hostClaude, "projects", ProjectKey(hostProject))
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(hostDir, "memory", "MEMORY.md"), "- [x](x.md)\n")
	write(filepath.Join(hostDir, "host.jsonl"), `{"type":"summary"}`+"\n"+`{"cwd":"/Users/me/work/shop"}`+"\n")
	write(filepath.Join(hostDir, "sandbox.jsonl"), `{"cwd":"/app"}`+"\n")
	write(filepath.Join(hostDir, "sandbox", "subagents", "a.jsonl"), "{}\n")

	dir, err := PrepareProjectDir(agentDir, hostClaude, hostProject, "/app")
	if err != nil {
		t.Fatal(err)
	}
	if dir != ProjectDir(agentDir, hostProject) {
		t.Errorf("dir = %s", dir)
	}
	exists := func(path string) bool { _, err := os.Stat(path); return err == nil }
	for _, want := range []string{"memory/MEMORY.md", "sandbox.jsonl", "sandbox/subagents/a.jsonl"} {
		if !exists(filepath.Join(dir, want)) {
			t.Errorf("%s missing in the sandboxes' directory", want)
		}
	}
	if exists(filepath.Join(dir, "host.jsonl")) || !exists(filepath.Join(hostDir, "host.jsonl")) {
		t.Error("a host conversation must stay on the host")
	}
	if exists(filepath.Join(hostDir, "sandbox.jsonl")) || exists(filepath.Join(hostDir, "sandbox")) {
		t.Error("sandbox conversations must leave the host's directory")
	}
	if !exists(filepath.Join(hostDir, "memory", "MEMORY.md")) {
		t.Error("the host's memory must stay")
	}

	// Only once: later host memory and transcripts are not copied over.
	write(filepath.Join(hostDir, "memory", "new.md"), "x")
	write(filepath.Join(hostDir, "later.jsonl"), `{"cwd":"/app"}`+"\n")
	if _, err := PrepareProjectDir(agentDir, hostClaude, hostProject, "/app"); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(dir, "memory", "new.md")) || exists(filepath.Join(dir, "later.jsonl")) {
		t.Error("the directory is seeded once, then the two sides stay apart")
	}
}

func TestClaudeMountsProjectDir(t *testing.T) {
	hostClaude := filepath.Join(t.TempDir(), "claude")
	mounts, err := ClaudeMounts(hostClaude, "/zdev/agent/projects/-Users-me-shop", "/app")
	if err != nil {
		t.Fatal(err)
	}
	want := "type=bind,src=/zdev/agent/projects/-Users-me-shop,dst=" + filepath.Join(hostClaude, "projects", "-app")
	if len(mounts) != 1 || mounts[0] != want {
		t.Errorf("mounts = %v, want [%s]", mounts, want)
	}
}

func TestInstallAdvice(t *testing.T) {
	for _, tc := range []struct {
		pm, uid, user string
		want          []string
	}{
		{"apk", "0", "root", []string{"RUN apk add --no-cache bash git"}},
		{"apt-get", "0", "root", []string{"RUN apt-get update && apt-get install -y --no-install-recommends bash git && rm -rf /var/lib/apt/lists/*"}},
		{"dnf", "", "", []string{"RUN dnf install -y bash git && dnf clean all"}},
		{"apk", "82", "www-data", []string{"USER root", "RUN apk add --no-cache bash git", "USER www-data"}},
		{"apt-get", "1000", "", []string{"USER root", "RUN apt-get update && apt-get install -y --no-install-recommends bash git && rm -rf /var/lib/apt/lists/*", "USER 1000"}},
	} {
		info := &ImageInfo{PackageManager: tc.pm, UID: tc.uid, User: tc.user}
		got, ok := info.InstallAdvice([]string{"bash", "git"})
		if !ok || strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
			t.Errorf("%s as %s/%s: %q", tc.pm, tc.uid, tc.user, got)
		}
	}
	if _, ok := (&ImageInfo{}).InstallAdvice([]string{"bash"}); ok {
		t.Error("an image without a package manager got advice")
	}
}

func TestParseProbePackageManagerAndUser(t *testing.T) {
	info := parseProbe("libc=musl\nos=Alpine Linux v3.24\npath=/usr/bin\ngit=1\npm=apk\nuid=82\nuser=www-data\n")
	if info.PackageManager != "apk" || info.UID != "82" || info.User != "www-data" || info.Version != probeVersion {
		t.Errorf("parsed %+v", info)
	}
	if m := info.Missing(); strings.Join(m, ",") != "bash" {
		t.Errorf("missing = %v", m)
	}
}
