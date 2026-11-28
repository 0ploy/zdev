package agent

// The agent's home and Claude's configuration (PLAN_2 sections 3 and 4).
//
// HOME in the agent container is the host's home path (e.g. /Users/me), backed
// by a per-project volume, so absolute ~/.claude paths in plugin metadata,
// settings and symlinked skills keep working. From the host's ~/.claude:
// skills, agents, commands and plugins read-only, CLAUDE.md, settings and
// scripts copied at start. Memory and transcripts are the sandbox's own
// (ProjectDir), never the host's: a host transcript in the sandbox would leak
// the host's conversations and resume them there, and one the sandbox wrote
// would be resumed by Claude on the host as genuine history.
// zdev seeds a fresh .claude.json once.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
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
func AuthDir(agentDir string) string { return filepath.Join(agentDir, "claude", "auth") }

var nonKeyChars = regexp.MustCompile(`[^A-Za-z0-9]`)

// ProjectKey is Claude's per-project directory name for a working directory:
// every character that is not a letter or digit becomes "-".
func ProjectKey(path string) string { return nonKeyChars.ReplaceAllString(path, "-") }

// ProjectDir is the sandboxes' Claude project directory (memory and
// transcripts) for a host project directory, keyed like Claude's own so it
// follows the directory, not the project name.
func ProjectDir(agentDir, hostProjectDir string) string {
	return filepath.Join(agentDir, "claude", "projects", ProjectKey(hostProjectDir))
}

// PrepareProjectDir creates the sandboxes' project directory on first use:
// the host's memory of the project is copied in, and the conversations
// earlier sandboxes left in the host's directory (whose cwd is the
// container's working directory) are moved over. Afterwards nothing is
// shared: each side keeps its own memory and conversations.
func PrepareProjectDir(agentDir, hostClaude, hostProjectDir, containerWorkdir string) (string, error) {
	dir := ProjectDir(agentDir, hostProjectDir)
	if _, err := os.Stat(dir); err == nil {
		return dir, nil
	}
	tmp := dir + ".tmp"
	if err := os.RemoveAll(tmp); err != nil {
		return "", err
	}
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return "", err
	}
	hostDir := filepath.Join(hostClaude, "projects", ProjectKey(hostProjectDir))
	if err := copyTree(filepath.Join(hostDir, "memory"), filepath.Join(tmp, "memory")); err != nil {
		return "", fmt.Errorf("copying the project's memory: %w", err)
	}
	if err := os.Rename(tmp, dir); err != nil {
		_ = os.RemoveAll(tmp)
		if _, statErr := os.Stat(dir); statErr == nil {
			return dir, nil // a concurrent session created it first
		}
		return "", err
	}
	if containerWorkdir != hostProjectDir {
		moveSandboxTranscripts(hostDir, dir, containerWorkdir)
	}
	return dir, nil
}

// moveSandboxTranscripts moves the conversations recorded with cwd
// containerWorkdir (and their side directories) from hostDir to dir. Best
// effort: a conversation that can't be read or moved stays where it is.
func moveSandboxTranscripts(hostDir, dir, containerWorkdir string) {
	files, _ := filepath.Glob(filepath.Join(hostDir, "*.jsonl"))
	for _, f := range files {
		if transcriptCwd(f) != containerWorkdir {
			continue
		}
		if err := os.Rename(f, filepath.Join(dir, filepath.Base(f))); err != nil {
			continue
		}
		side := strings.TrimSuffix(f, ".jsonl")
		if fi, err := os.Stat(side); err == nil && fi.IsDir() {
			_ = os.Rename(side, filepath.Join(dir, filepath.Base(side)))
		}
	}
}

// transcriptCwd is the first cwd recorded in a Claude transcript.
func transcriptCwd(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<20)
	for i := 0; i < 50; i++ {
		line, err := r.ReadBytes('\n')
		var entry struct {
			Cwd string `json:"cwd"`
		}
		if json.Unmarshal(line, &entry) == nil && entry.Cwd != "" {
			return entry.Cwd
		}
		if err != nil {
			return ""
		}
	}
	return ""
}

// copyTree copies the regular files and directories under src to dst
// (symlinks are skipped: the copy must not reach outside src). A missing src
// copies nothing.
func copyTree(src, dst string) error {
	if _, err := os.Stat(src); os.IsNotExist(err) {
		return nil
	}
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(target, 0o700)
		case !d.Type().IsRegular():
			return nil
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
}

// claudeShared are the ~/.claude directories every session reads.
var claudeShared = []string{"skills", "agents", "commands", "plugins"}

// ClaudeMounts returns the --mount specs for the host's ~/.claude: the shared
// directories and the targets of symlinks inside them read-only (at their
// own paths, so the links resolve), and the sandboxes' project directory
// (PrepareProjectDir) read-write at the key of the container's working
// directory.
func ClaudeMounts(hostClaude, projectDir, containerWorkdir string) ([]string, error) {
	m := newMountSet()
	if err := m.sharedDirs(hostClaude, claudeShared); err != nil {
		return nil, err
	}
	if err := m.add(projectDir, filepath.Join(hostClaude, "projects", ProjectKey(containerWorkdir)), false); err != nil {
		return nil, err
	}
	return m.sorted(), nil
}

// mountSet collects --mount specs, one per destination.
type mountSet struct {
	mounts []string
	seen   map[string]bool
}

func newMountSet() *mountSet { return &mountSet{seen: map[string]bool{}} }

func (m *mountSet) add(src, dst string, readOnly bool) error {
	if strings.ContainsAny(src+dst, ",\"") {
		return fmt.Errorf("path %q can't be mounted (contains a comma or quote)", src)
	}
	if m.seen[dst] {
		return nil
	}
	m.seen[dst] = true
	spec := "type=bind,src=" + src + ",dst=" + dst
	if readOnly {
		spec += ",readonly"
	}
	m.mounts = append(m.mounts, spec)
	return nil
}

// sharedDirs adds the directories names under hostDir and the targets of
// symlinks inside them read-only, at their own paths, so the links resolve.
func (m *mountSet) sharedDirs(hostDir string, names []string) error {
	for _, name := range names {
		dir := filepath.Join(hostDir, name)
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			continue
		}
		if err := m.add(dir, dir, true); err != nil {
			return err
		}
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if e.Type()&os.ModeSymlink == 0 {
				continue
			}
			target, err := filepath.EvalSymlinks(filepath.Join(dir, e.Name()))
			if err != nil || strings.HasPrefix(target, hostDir+string(os.PathSeparator)) {
				continue
			}
			if err := m.add(target, target, true); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *mountSet) sorted() []string {
	sort.Strings(m.mounts)
	return m.mounts
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

// TurnEndHook is the Stop hook that tells the host a turn ended.
const TurnEndHook = "/opt/zdev/bin/zdev sandbox turn-end"

// ManagedSettings is Claude's managed-settings.json for a session: tools run
// without prompts (the container is the boundary), the updater is off,
// claude.ai connectors only when the project allows them, the host-only
// plugins of agent.claude.disable_plugins switched off, with turnHook the Stop
// hook that lets the host replace the sandbox between turns, and with
// statusLine zdev's status line (ClaudeStatusLineFile) over the developer's.
func ManagedSettings(connectors bool, disablePlugins []string, turnHook, statusLine bool) []byte {
	settings := map[string]any{
		"permissions":                       map[string]string{"defaultMode": "bypassPermissions"},
		"skipDangerousModePermissionPrompt": true,
		"disableClaudeAiConnectors":         !connectors,
		"env":                               map[string]string{"DISABLE_AUTOUPDATER": "1"},
	}
	if turnHook {
		settings["hooks"] = map[string]any{
			"Stop": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": TurnEndHook, "timeout": 10}}}},
		}
	}
	if statusLine {
		settings["statusLine"] = map[string]any{"type": "command", "command": "sh " + ClaudeStatusLineFile}
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
