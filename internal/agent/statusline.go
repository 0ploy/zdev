package agent

// zdev's own status line for the sandboxes: the same segments in Claude Code
// (a command, set in the session's managed settings, which win over the
// developer's) and in pi (a footer, set by zdev's extension).

import (
	_ "embed"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

//go:embed statusline/claude.sh
var claudeStatusLine string

//go:embed statusline/pi.js
var piStatusLine string

// ClaudeStatusLineFile is where the session's Claude status line script sits
// in the sandbox (in the session directory, next to managed-settings.json).
const ClaudeStatusLineFile = SessionMount + "/zdev-statusline.sh"

// ClaudeStatusLineScript is the script ClaudeStatusLineFile holds.
func ClaudeStatusLineScript() string { return claudeStatusLine }

// HasOwnClaudeStatusLine reports whether the developer's Claude settings in
// hostClaude (~/.claude) set a status line of their own.
func HasOwnClaudeStatusLine(hostClaude string) bool {
	for _, f := range []string{"settings.json", "settings.local.json"} {
		data, err := os.ReadFile(filepath.Join(hostClaude, f))
		if err != nil {
			continue
		}
		var s map[string]json.RawMessage
		if json.Unmarshal(data, &s) == nil && s["statusLine"] != nil {
			return true
		}
	}
	return false
}

// HasOwnPiStatusLine reports whether one of the developer's pi extensions in
// hostPi (~/.pi/agent) replaces the footer.
func HasOwnPiStatusLine(hostPi string) bool {
	entries, err := os.ReadDir(filepath.Join(hostPi, "extensions"))
	if err != nil {
		return false
	}
	for _, e := range entries {
		var data []byte
		if e.IsDir() {
			data, err = os.ReadFile(filepath.Join(hostPi, "extensions", e.Name(), "index.ts"))
			if err != nil {
				data, err = os.ReadFile(filepath.Join(hostPi, "extensions", e.Name(), "index.js"))
			}
		} else {
			data, err = os.ReadFile(filepath.Join(hostPi, "extensions", e.Name()))
		}
		if err == nil && strings.Contains(string(data), "setFooter") {
			return true
		}
	}
	return false
}
