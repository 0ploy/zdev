package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPiSessionKey(t *testing.T) {
	for in, want := range map[string]string{
		"/app":                "--app--",
		"/Users/dev/shop":     "--Users-dev-shop--",
		"/srv/a:b/c":          "--srv-a-b-c--",
		"/Users/dev/my.shop/": "--Users-dev-my.shop---",
		"//net/share":         "---net-share--",
	} {
		if got := PiSessionKey(in); got != want {
			t.Errorf("PiSessionKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolvePi(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/" + PiRepo + "/releases/latest":
			_, _ = w.Write([]byte(`{"tag_name":"v1.7.0"}`))
		case "/" + PiRepo + "/releases/download/v1.7.0/SHA256SUMS":
			_, _ = w.Write([]byte(strings.Repeat("a", 64) + "  pi-linux-arm64.tar.gz\n" + strings.Repeat("b", 64) + "  pi-1.7.0-source.tar.gz\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	rel, err := resolvePi(context.Background(), srv.Client(), srv.URL, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if rel.Version != "1.7.0" || rel.SourceSHA256 != strings.Repeat("b", 64) {
		t.Errorf("resolvePi = %+v", rel)
	}
}

func TestPiExtension(t *testing.T) {
	if ext := PiExtension(false); strings.Contains(ext, "agent_settled") || !strings.Contains(ext, "ZDEV_PI_LIB") {
		t.Errorf("without turn hook:\n%s", ext)
	}
	if ext := PiExtension(true); !strings.Contains(ext, `pi.on("agent_settled"`) || !strings.Contains(ext, `"/opt/zdev/bin/zdev", ["sandbox", "turn-end", ctx.sessionManager.getSessionId()]`) {
		t.Errorf("with turn hook:\n%s", ext)
	}
}

func TestPiMounts(t *testing.T) {
	home := t.TempDir()
	hostPi := filepath.Join(home, ".pi", "agent")
	_ = os.MkdirAll(filepath.Join(hostPi, "skills"), 0o700)
	_ = os.MkdirAll(filepath.Join(hostPi, "sessions"), 0o700) // never shared
	_ = os.MkdirAll(filepath.Join(home, ".agents", "skills", "zdev"), 0o700)
	mounts, err := PiMounts(home, "/zdev/agent/pi/projects/-Users-me-shop", "/app")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"type=bind,src=" + filepath.Join(home, ".agents", "skills") + ",dst=" + filepath.Join(home, ".agents", "skills") + ",readonly",
		"type=bind,src=" + hostPi + "/skills,dst=" + hostPi + "/skills,readonly",
		"type=bind,src=/zdev/agent/pi/projects/-Users-me-shop,dst=" + hostPi + "/sessions/--app--",
	}
	if got, w := strings.Join(mounts, "\n"), strings.Join(want, "\n"); got != w {
		// sorted by spec; compare as sets
		for _, x := range want {
			if !strings.Contains(got, x) {
				t.Errorf("PiMounts lacks %s; got\n%s", x, got)
			}
		}
		if len(mounts) != len(want) {
			t.Errorf("PiMounts = %q", mounts)
		}
	}
}

func TestSweepablePiBuilds(t *testing.T) {
	now := time.Now()
	uses := map[string]volumeUse{
		"zdev-pi-1.6.0-glibc-arm64":       {First: now.Add(-72 * time.Hour), Last: now.Add(-48 * time.Hour)},
		"zdev-pi-1.6.0-musl-arm64":        {First: now.Add(-72 * time.Hour), Last: now.Add(-48 * time.Hour)},
		"zdev-pi-1.7.0-glibc-arm64":       {First: now.Add(-30 * time.Hour), Last: now},
		"zdev-claude-2.1.330-glibc-arm64": {First: now.Add(-72 * time.Hour), Last: now.Add(-48 * time.Hour)},
	}
	got := sweepable(uses, []string{"zdev-pi-1.7.0-glibc-arm64"}, now)
	// Only the superseded glibc pi build: musl has no newer build in use,
	// and Claude's builds are another agent's.
	if strings.Join(got, " ") != "zdev-pi-1.6.0-glibc-arm64" {
		t.Errorf("sweepable = %v", got)
	}
}

// The review's case: a newer build nobody used yet (recorded when it was
// created) must never count as replaced by the older one in use.
func TestSweepableKeepsUnusedNewerBuild(t *testing.T) {
	now := time.Now()
	uses := map[string]volumeUse{
		"zdev-pi-1.6.0-glibc-arm64":       {First: now.Add(-72 * time.Hour), Last: now},
		"zdev-pi-1.7.0-glibc-arm64":       {First: now.Add(-time.Minute), Last: now.Add(-time.Minute)},
		"zdev-claude-2.1.400-glibc-arm64": {First: now.Add(-time.Minute), Last: now.Add(-time.Minute)},
		"zdev-claude-2.1.326-glibc-arm64": {First: now.Add(-72 * time.Hour), Last: now},
	}
	if got := sweepable(uses, []string{"zdev-pi-1.6.0-glibc-arm64", "zdev-claude-2.1.326-glibc-arm64"}, now); len(got) != 0 {
		t.Errorf("sweepable = %v, want none", got)
	}
}

func TestPiModels(t *testing.T) {
	dir := t.TempDir()
	if c, s, err := PiModels(dir); c != "" || s != nil || err != nil {
		t.Errorf("no file: %q %v %v", c, s, err)
	}
	_ = os.WriteFile(filepath.Join(dir, "models.json"), []byte(`{
  "providers": {
    "gateway": {"baseUrl": "https://llm.example.com/v1", "apiKey": "sk-literal", "headers": {"X-Team": "$TEAM", "X-Secret": "!op read x"}},
    "env": {"baseUrl": "https://other.example.com", "apiKey": "$OTHER_KEY"},
    "cmd": {"apiKey": "!op read op://v/i/k", "models": [{"id": "m", "headers": {"X-K": "lit"}}]},
    "inurl": {"baseUrl": "https://user:sk-inurl@proxy.example.com/v1", "apiKey": "$INURL_KEY"}
  }
}`), 0o600)
	c, stripped, err := PiModels(dir)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(stripped, ",") != "cmd,gateway,inurl" {
		t.Errorf("stripped = %v", stripped)
	}
	for _, gone := range []string{"sk-literal", "!op read", `"lit"`, "sk-inurl"} {
		if strings.Contains(c, gone) {
			t.Errorf("%s still in:\n%s", gone, c)
		}
	}
	for _, kept := range []string{"$TEAM", "$OTHER_KEY", "https://llm.example.com/v1", "https://proxy.example.com/v1"} {
		if !strings.Contains(c, kept) {
			t.Errorf("%s missing in:\n%s", kept, c)
		}
	}
	_ = os.WriteFile(filepath.Join(dir, "models.json"), []byte(`{oops`), 0o600)
	if _, _, err := PiModels(dir); err == nil {
		t.Error("broken file accepted")
	}
}
