package agent

// The agent's home and Claude's configuration (PLAN_2 sections 3 and 4).
//
// HOME in the agent container is the host's home path (e.g. /Users/me), backed
// by a per-project volume, so absolute ~/.claude paths in plugin metadata,
// settings and symlinked skills keep working. From the host's ~/.claude:
// skills, agents, commands and plugins read-only, this project's memory and
// transcripts read-write, CLAUDE.md, settings and scripts copied at start.
// zdev never parses Claude's files; it seeds a fresh .claude.json once.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// AuthMount is where the shared login directory sits in the container;
// CLAUDE_SECURESTORAGE_CONFIG_DIR points Claude's credential store there.
const AuthMount = "/zdev/claude-auth"

// SessionMount holds the session's generated files (managed settings, system
// prompt). Claude reads managed settings from /etc/claude-code.
const SessionMount = "/etc/claude-code"

// SystemPromptFile is the generated prompt inside SessionMount.
const SystemPromptFile = SessionMount + "/zdev-system-prompt.md"

// HomeVolumeName is the per-project volume behind the agent's HOME.
func HomeVolumeName(project string) string { return "zdev-agent-home-" + project }

// AuthDir is the shared login directory on the host.
func AuthDir(agentDir string) string { return filepath.Join(agentDir, "claude-auth") }

var nonKeyChars = regexp.MustCompile(`[^A-Za-z0-9]`)

// ProjectKey is Claude's per-project directory name for a working directory:
// every character that is not a letter or digit becomes "-".
func ProjectKey(path string) string { return nonKeyChars.ReplaceAllString(path, "-") }

// claudeShared are the ~/.claude directories every session reads.
var claudeShared = []string{"skills", "agents", "commands", "plugins"}

// ClaudeMounts returns the --mount specs for the host's ~/.claude: the shared
// directories and the targets of symlinks inside them read-only (at their
// own paths, so the links resolve), and this project's memory directory
// read-write at the key of the container's working directory. The memory
// directory is created on the host when missing.
func ClaudeMounts(hostClaude, hostProjectDir, containerWorkdir string) ([]string, error) {
	var mounts []string
	seen := map[string]bool{}
	add := func(src, dst string, readOnly bool) error {
		if strings.ContainsAny(src+dst, ",\"") {
			return fmt.Errorf("path %q can't be mounted (contains a comma or quote)", src)
		}
		if seen[dst] {
			return nil
		}
		seen[dst] = true
		m := "type=bind,src=" + src + ",dst=" + dst
		if readOnly {
			m += ",readonly"
		}
		mounts = append(mounts, m)
		return nil
	}
	for _, name := range claudeShared {
		dir := filepath.Join(hostClaude, name)
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			continue
		}
		if err := add(dir, dir, true); err != nil {
			return nil, err
		}
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if e.Type()&os.ModeSymlink == 0 {
				continue
			}
			target, err := filepath.EvalSymlinks(filepath.Join(dir, e.Name()))
			if err != nil || strings.HasPrefix(target, hostClaude+string(os.PathSeparator)) {
				continue
			}
			if err := add(target, target, true); err != nil {
				return nil, err
			}
		}
	}
	memory := filepath.Join(hostClaude, "projects", ProjectKey(hostProjectDir))
	if err := os.MkdirAll(memory, 0o700); err != nil {
		return nil, fmt.Errorf("creating %s: %w", memory, err)
	}
	if err := add(memory, filepath.Join(hostClaude, "projects", ProjectKey(containerWorkdir)), false); err != nil {
		return nil, err
	}
	sort.Strings(mounts)
	return mounts, nil
}

// firstStart answers Claude's first-start questions (onboarding, theme, trust
// in the working directory, the bypass-permissions warning); written once,
// when the home has no .claude.json yet (Cronyard does the same).
func firstStart(workdir string) string {
	b, _ := json.Marshal(map[string]any{
		"hasCompletedOnboarding":        true,
		"theme":                         "dark",
		"bypassPermissionsModeAccepted": true,
		"projects": map[string]any{
			workdir: map[string]bool{"hasTrustDialogAccepted": true, "hasCompletedProjectOnboarding": true},
		},
	})
	return string(b)
}

// PrepareHomeScript prepares the home volume as root, in a one-shot container
// with the volume at $H and the host's ~/.claude read-only at /src: copies
// CLAUDE.md, the settings and top-level scripts, seeds .claude.json once,
// creates the memory mount point and hands everything to the agent's user.
// The volume never stays empty (an empty volume gets its root's owner and
// mode copied from the image at every mount).
const PrepareHomeScript = `set -eu
mkdir -p "$H/.claude/projects/$KEY"
for f in CLAUDE.md settings.json settings.local.json; do
  if [ -f "/src/$f" ]; then cp "/src/$f" "$H/.claude/$f"; else rm -f "$H/.claude/$f"; fi
done
for f in /src/*.sh; do [ -f "$f" ] && cp "$f" "$H/.claude/"; done
[ -f "$H/.claude.json" ] || printf '%s' "$SEED" > "$H/.claude.json"
if [ "$(stat -c %u:%g "$H")" != "$OWNER" ]; then chown -R "$OWNER" "$H"; else chown "$OWNER" "$H/.claude" "$H/.claude/projects" "$H/.claude/projects/$KEY" "$H/.claude"/* "$H/.claude.json" 2>/dev/null || true; fi`

// PrepareHomeEnv returns the variables for PrepareHomeScript.
func PrepareHomeEnv(home, workdir, owner string) []string {
	return []string{"H=" + home, "KEY=" + ProjectKey(workdir), "OWNER=" + owner, "SEED=" + firstStart(workdir)}
}

// ManagedSettings is Claude's managed-settings.json for a session: tools run
// without prompts (the container is the boundary), the updater is off,
// claude.ai connectors only when the project allows them, and the host-only
// plugins of agent.disable_plugins switched off.
func ManagedSettings(connectors bool, disablePlugins []string) []byte {
	settings := map[string]any{
		"permissions":                       map[string]string{"defaultMode": "bypassPermissions"},
		"skipDangerousModePermissionPrompt": true,
		"disableClaudeAiConnectors":         !connectors,
		"env":                               map[string]string{"DISABLE_AUTOUPDATER": "1"},
	}
	if len(disablePlugins) > 0 {
		off := map[string]bool{}
		for _, p := range disablePlugins {
			off[p] = false
		}
		settings["enabledPlugins"] = off
	}
	b, _ := json.MarshalIndent(settings, "", "  ")
	return append(b, '\n')
}
