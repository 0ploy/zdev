package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHasOwnClaudeStatusLine(t *testing.T) {
	dir := t.TempDir()
	if HasOwnClaudeStatusLine(dir) {
		t.Error("no settings, no status line")
	}
	_ = os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"model": "opus"}`), 0o644)
	if HasOwnClaudeStatusLine(dir) {
		t.Error("settings without statusLine")
	}
	_ = os.WriteFile(filepath.Join(dir, "settings.local.json"), []byte(`{"statusLine": {"type": "command", "command": "x"}}`), 0o644)
	if !HasOwnClaudeStatusLine(dir) {
		t.Error("statusLine in settings.local.json")
	}
}

func TestHasOwnPiStatusLine(t *testing.T) {
	dir := t.TempDir()
	ext := filepath.Join(dir, "extensions")
	_ = os.MkdirAll(filepath.Join(ext, "pkg"), 0o755)
	_ = os.WriteFile(filepath.Join(ext, "other.ts"), []byte(`pi.on("session_start", () => {})`), 0o644)
	if HasOwnPiStatusLine(dir) {
		t.Error("an extension without a footer")
	}
	_ = os.WriteFile(filepath.Join(ext, "pkg", "index.ts"), []byte(`ctx.ui.setFooter(() => ({}))`), 0o644)
	if !HasOwnPiStatusLine(dir) {
		t.Error("a footer in a directory extension")
	}
}

func TestBundledStatusLines(t *testing.T) {
	if !strings.HasPrefix(ClaudeStatusLineScript(), "#!/bin/sh\n") || !strings.Contains(ClaudeStatusLineScript(), "ZDEV_HOST_PROJECTDIR") {
		t.Error("the Claude status line script is missing")
	}
	if !strings.Contains(piStatusLine, "ctx.ui.setFooter(") {
		t.Error("the pi status line is missing")
	}
}
