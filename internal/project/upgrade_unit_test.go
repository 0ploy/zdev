package project

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0ploy/zdev/internal/config"
	"github.com/0ploy/zdev/internal/state"
)

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()
	done := make(chan string)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()
	fn()
	_ = w.Close()
	return <-done
}

// A project registered by a zdev that didn't record hosts must become
// reachable by URL as soon as ANY project starts, without being restarted.
func TestBackfillRoutedHosts_FromConfigOnDisk(t *testing.T) {
	defer setupTestEnv(t)()
	stateMgr, err := state.DefaultManager()
	if err != nil {
		t.Fatal(err)
	}

	legacyDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(legacyDir, ".zdev"), 0755); err != nil {
		t.Fatal(err)
	}
	cfg := "name: legacy\nservices:\n  app:\n    image: nginx\n    routing:\n      port: 80\n" +
		"  api:\n    image: node\n    routing:\n      port: 3000\n      domain: api.legacy.0ploy.dev\n"
	if err := os.WriteFile(filepath.Join(legacyDir, ".zdev", "config.yaml"), []byte(cfg), 0644); err != nil {
		t.Fatal(err)
	}
	_ = stateMgr.RegisterProject("legacy", legacyDir)
	_ = stateMgr.RegisterProject("gone", filepath.Join(t.TempDir(), "missing"))

	backfillRoutedHosts(stateMgr)

	entry, err := stateMgr.GetProject("legacy")
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := config.LoadProject(legacyDir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"api.legacy.0ploy.dev", loaded.Domain}
	if !entry.HostsRecorded || len(entry.Hosts) != 2 || entry.Hosts[0] != want[0] || entry.Hosts[1] != want[1] {
		t.Errorf("legacy entry = %+v, want recorded hosts %v", entry, want)
	}

	// An unloadable project stays pending and is retried next time.
	pending, _ := stateMgr.ProjectsWithoutHosts()
	if _, ok := pending["gone"]; !ok || len(pending) != 1 {
		t.Errorf("pending = %v, want only gone", pending)
	}
}

func TestWarnOutdatedServices(t *testing.T) {
	p, _ := seedRunningService(t, nil)
	ctx := context.Background()

	if out := captureStdout(t, func() { p.warnOutdatedServices(ctx, nil) }); out != "" {
		t.Errorf("up-to-date container warned: %q", out)
	}

	svc := p.Config.Services["app"]
	svc.Environment = map[string]string{"NEW": "1"}
	p.Config.Services["app"] = svc

	out := captureStdout(t, func() { p.warnOutdatedServices(ctx, nil) })
	if !strings.Contains(out, "Service app runs with an outdated config") || !strings.Contains(out, "zdev update") {
		t.Errorf("drifted container: output %q, want an update hint", out)
	}
	if out := captureStdout(t, func() { p.warnOutdatedServices(ctx, map[string]bool{"other": true}) }); out != "" {
		t.Errorf("filtered-out service warned: %q", out)
	}
}

// zdev start keeps existing containers, so it must say when one is outdated.
func TestStart_HintsAtOutdatedRunningContainer(t *testing.T) {
	p, _ := seedRunningService(t, nil)
	svc := p.Config.Services["app"]
	svc.Environment = map[string]string{"NEW": "1"}
	p.Config.Services["app"] = svc

	var startErr error
	out := captureStdout(t, func() { startErr = p.Start(context.Background()) })
	if startErr != nil {
		t.Fatalf("Start: %v", startErr)
	}
	if !strings.Contains(out, "Service app is already running") || !strings.Contains(out, "outdated config") {
		t.Errorf("output %q, want the running service kept and an update hint", out)
	}
}
