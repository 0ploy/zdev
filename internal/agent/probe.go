package agent

// What a service image has, found once per image ID (an ID never changes) in
// a throwaway container without network (PLAN_2 section 1, after Cronyard's
// `cyx tools`): which Claude build fits (libc), whether the image meets the
// requirements (bash, git), its PATH, and the tools the system prompt lists so
// the agent doesn't spend turns finding out.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	LibcGlibc = "glibc"
	LibcMusl  = "musl"
)

// ImageInfo is the probe's result.
type ImageInfo struct {
	ImageID string `json:"image_id"`
	Libc    string `json:"libc"`
	Glibc   string `json:"glibc,omitempty"` // version, glibc images
	OS      string `json:"os,omitempty"`    // PRETTY_NAME
	Path    string `json:"path"`            // the image's PATH
	Bash    bool   `json:"bash"`
	Git     bool   `json:"git"`
	// BusyBox lists core tools that are BusyBox's (options differ from GNU).
	BusyBox []string `json:"busybox,omitempty"`
	// Tools maps a tool to its version line ("" when only present).
	Tools map[string]string `json:"tools,omitempty"`
	// PackageManager is the image's (apk, apt-get, dnf, microdnf, yum,
	// zypper, pacman; "" when there is none), and UID/User who the image runs
	// as: what InstallAdvice needs to say how to add a missing tool.
	PackageManager string `json:"package_manager,omitempty"`
	UID            string `json:"uid,omitempty"`
	User           string `json:"user,omitempty"`
	// ClaudeOK records agent builds verified to start in this image: Claude
	// versions, and "pi <version>".
	ClaudeOK map[string]bool `json:"claude_ok,omitempty"`
	ProbedAt time.Time       `json:"probed_at"`
	// Version is probeVersion at probing time; older entries are probed again.
	Version int `json:"probe_version,omitempty"`
}

// probeVersion changes whenever the probe learns something new, so cached
// results from before are probed again instead of lacking it.
const probeVersion = 2

// probeTools are listed with a version (the command prints it quickly and
// offline) or, without one, only by presence (pnpm and yarn through corepack
// would try to download themselves).
var probeTools = []struct {
	name    string
	version string
}{
	{"php", "php -r 'echo PHP_VERSION;'"},
	{"composer", ""},
	{"node", "node --version"},
	{"npm", ""},
	{"pnpm", ""},
	{"yarn", ""},
	{"bun", "bun --version"},
	{"go", "go version"},
	{"cargo", "cargo --version"},
	{"rustc", "rustc --version"},
	{"python3", "python3 --version"},
	{"ruby", "ruby --version"},
	{"java", ""},
	{"make", ""},
	{"curl", ""},
	{"wget", ""},
	{"jq", ""},
	{"rg", ""},
	{"mysql", ""},
	{"psql", ""},
	{"redis-cli", ""},
	{"zpctl", ""},
	{"supervisorctl", ""},
}

var coreTools = []string{"grep", "sed", "awk", "find", "xargs", "sort", "date", "stat", "diff", "timeout"}

func probeScript() string {
	var b strings.Builder
	b.WriteString(`if [ -e /lib/ld-musl-$(uname -m).so.1 ]; then echo libc=musl; else echo libc=glibc; echo glibc=$(ldd --version 2>&1 | head -1 | grep -o '[0-9]*\.[0-9]*$'); fi
[ -f /etc/os-release ] && . /etc/os-release && echo "os=$PRETTY_NAME"
echo "path=$PATH"
command -v bash >/dev/null 2>&1 && echo bash=1
command -v git >/dev/null 2>&1 && echo git=1
for pm in apk apt-get dnf microdnf yum zypper pacman; do command -v $pm >/dev/null 2>&1 && { echo pm=$pm; break; }; done
echo "uid=$(id -u 2>/dev/null)"
echo "user=$(id -un 2>/dev/null)"
`)
	for _, t := range coreTools {
		fmt.Fprintf(&b, "p=$(command -v %s 2>/dev/null) && case \"$(readlink -f \"$p\" 2>/dev/null)\" in *busybox*) echo busybox=%s;; esac\n", t, t)
	}
	for _, t := range probeTools {
		if t.version == "" {
			fmt.Fprintf(&b, "command -v %s >/dev/null 2>&1 && echo tool.%s=\n", t.name, t.name)
		} else {
			fmt.Fprintf(&b, "command -v %s >/dev/null 2>&1 && echo \"tool.%s=$(%s 2>&1 | head -1)\"\n", t.name, t.name, t.version)
		}
	}
	// The last check fails when its tool is missing; that is not the probe failing.
	b.WriteString("exit 0\n")
	return b.String()
}

// ProbeImage returns what the image has, from the cache in dir or probed now.
func ProbeImage(ctx context.Context, d Docker, dir, image string) (*ImageInfo, error) {
	id, err := d.ImageID(ctx, image)
	if err != nil {
		return nil, err
	}
	cache := loadProbeCache(dir)
	if info, ok := cache[id]; ok {
		return info, nil
	}
	out, err := d.Output(ctx, "run", "--rm", "--network", "none", "--entrypoint", "sh", image, "-c", probeScript())
	if err != nil {
		return nil, fmt.Errorf("probing image %s (it needs /bin/sh): %w", image, err)
	}
	info := parseProbe(out)
	info.ImageID = id
	info.ProbedAt = time.Now()
	cache[id] = info
	saveProbeCache(dir, cache)
	return info, nil
}

func parseProbe(out string) *ImageInfo {
	info := &ImageInfo{Tools: map[string]string{}, Version: probeVersion}
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		switch {
		case k == "libc":
			info.Libc = v
		case k == "glibc":
			info.Glibc = v
		case k == "os":
			info.OS = v
		case k == "path":
			info.Path = v
		case k == "bash":
			info.Bash = true
		case k == "git":
			info.Git = true
		case k == "busybox":
			info.BusyBox = append(info.BusyBox, v)
		case k == "pm":
			info.PackageManager = v
		case k == "uid":
			info.UID = strings.TrimSpace(v)
		case k == "user":
			info.User = strings.TrimSpace(v)
		case strings.HasPrefix(k, "tool."):
			info.Tools[strings.TrimPrefix(k, "tool.")] = strings.TrimSpace(v)
		}
	}
	if info.Path == "" {
		info.Path = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	}
	return info
}

// Missing names what the image lacks for agent mode.
func (i *ImageInfo) Missing() []string {
	var m []string
	if !i.Bash {
		m = append(m, "bash")
	}
	if !i.Git {
		m = append(m, "git")
	}
	return m
}

// InstallAdvice returns the Dockerfile lines that add packages to the
// image, for its package manager and as root (an image running as another
// user switches to root for the install and back). ok is false when the
// image has no package manager (distroless) and nothing can be added.
func (i *ImageInfo) InstallAdvice(packages []string) (lines []string, ok bool) {
	pkgs := strings.Join(packages, " ")
	var run string
	switch i.PackageManager {
	case "apk":
		run = "RUN apk add --no-cache " + pkgs
	case "apt-get":
		run = "RUN apt-get update && apt-get install -y --no-install-recommends " + pkgs + " && rm -rf /var/lib/apt/lists/*"
	case "dnf", "microdnf", "yum":
		run = "RUN " + i.PackageManager + " install -y " + pkgs + " && " + i.PackageManager + " clean all"
	case "zypper":
		run = "RUN zypper --non-interactive install " + pkgs + " && zypper clean --all"
	case "pacman":
		run = "RUN pacman -Sy --noconfirm " + pkgs + " && pacman -Scc --noconfirm"
	default:
		return nil, false
	}
	if i.UID != "" && i.UID != "0" {
		back := i.User
		if back == "" {
			back = i.UID
		}
		return []string{"USER root", run, "USER " + back}, true
	}
	return []string{run}, true
}

// ToolNames lists the found tools, sorted.
func (i *ImageInfo) ToolNames() []string {
	names := make([]string, 0, len(i.Tools))
	for n := range i.Tools {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// CheckClaude runs `claude --version` once per image and Claude version, so an
// incompatible build fails here with a clear message, not mid-session.
func CheckClaude(ctx context.Context, d Docker, dir, image string, info *ImageInfo, version, claudeVolume string) error {
	return checkBuild(ctx, d, dir, image, info, version, func() error {
		_, err := d.Output(ctx, "run", "--rm", "--network", "none", "--user", "65534:65534", "--cap-drop", "ALL",
			"-e", "HOME=/tmp", "-e", "DISABLE_AUTOUPDATER=1",
			"--mount", "type=volume,src="+claudeVolume+",dst=/opt/zdev/claude,readonly",
			"--entrypoint", "/opt/zdev/claude/claude", image, "--version")
		if err != nil {
			return fmt.Errorf("Claude Code %s (%s build) doesn't run in image %s: %w", version, info.Libc, image, err)
		}
		return nil
	})
}

// checkBuild runs check once per image and key (an agent build), recording
// success in the probe cache.
func checkBuild(ctx context.Context, d Docker, dir, image string, info *ImageInfo, key string, check func() error) error {
	if info.ClaudeOK[key] {
		return nil
	}
	if err := check(); err != nil {
		return err
	}
	cache := loadProbeCache(dir)
	if c, ok := cache[info.ImageID]; ok {
		info = c
	}
	if info.ClaudeOK == nil {
		info.ClaudeOK = map[string]bool{}
	}
	info.ClaudeOK[key] = true
	cache[info.ImageID] = info
	saveProbeCache(dir, cache)
	return nil
}

func loadProbeCache(dir string) map[string]*ImageInfo {
	cache := map[string]*ImageInfo{}
	if b, err := os.ReadFile(filepath.Join(dir, "images.json")); err == nil {
		_ = json.Unmarshal(b, &cache)
	}
	// Forget images not probed for 30 days (a probe is cheap, the file isn't)
	// and results of an older probe, which lack what it learned since.
	for id, info := range cache {
		if time.Since(info.ProbedAt) > 30*24*time.Hour || info.Version < probeVersion {
			delete(cache, id)
		}
	}
	return cache
}

func saveProbeCache(dir string, cache map[string]*ImageInfo) {
	if b, err := json.MarshalIndent(cache, "", "  "); err == nil {
		_ = os.MkdirAll(dir, 0o700)
		_ = os.WriteFile(filepath.Join(dir, "images.json"), b, 0o600)
	}
}
