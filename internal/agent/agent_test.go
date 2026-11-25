package agent

import (
	"encoding/json"
	"errors"
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
	}
	for _, args := range allowed {
		if err := CheckRequest(args, off); err != nil {
			t.Errorf("%v refused: %v", args, err)
		}
	}
	refused := map[string][]string{
		"not-allowed":   {"down"},
		"other-project": {"exec", "--config", "/elsewhere", "app", "ls"},
		"run-here":      {"run", "test"},
		"docker-socket": {"exec", "-u", "root", "docker1", "docker", "ps"},
	}
	for code, args := range refused {
		err := CheckRequest(args, off)
		var r *Refusal
		if !errors.As(err, &r) || r.Code != code {
			t.Errorf("%v: got %v, want refusal %s", args, err, code)
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
	if err := json.Unmarshal(ManagedSettings(false, nil), &s); err != nil {
		t.Fatal(err)
	}
	if s["disableClaudeAiConnectors"] != true {
		t.Error("connectors must be off by default")
	}
	if p, _ := s["permissions"].(map[string]any); p["defaultMode"] != "bypassPermissions" {
		t.Errorf("permissions = %v", s["permissions"])
	}
	_ = json.Unmarshal(ManagedSettings(true, []string{"codex@openai-codex"}), &s)
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
