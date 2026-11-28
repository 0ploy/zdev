package agent

// pi (github.com/earendil-works/pi), the second agent zdev runs. Its official
// Linux builds exist for glibc only, so zdev builds both libc variants itself,
// from the release's source archive (which carries the model catalog, so pi
// needs no network for it) with Bun, the way pi's own release workflow does:
// one build per version and architecture fills both volumes, so glibc and musl
// images always run the same version. The build runs on the developer's
// machine in two one-shot containers (Alpine fetches and checks the
// downloads, Node builds) and takes a few minutes, once per pi release.
// Nothing of it stays but the two build volumes: the downloads and npm's
// cache live in a volume of that build alone, and the Node image goes again
// when the build pulled it.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/0ploy/zdev/internal/config"
)

const (
	// PiRepo is pi's GitHub repository.
	PiRepo = "earendil-works/pi"
	// PiMount is where a pi build sits in the sandbox.
	PiMount = "/opt/zdev/pi"
	// PiCommand is the launcher the sandbox runs (on PATH as pi).
	PiCommand = PiMount + "/bin/pi"
	// PiAuthMount holds the shared login (auth.json) in the container.
	PiAuthMount = "/zdev/pi-auth"
	// PiSessionMount holds a pi session's generated files.
	PiSessionMount = "/etc/zdev-agent"
	// PiCheckEvery is how long a resolved release is reused.
	PiCheckEvery = 24 * time.Hour

	piVolumePrefix = "zdev-pi-"
)

var piVersionRE = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

// PiRelease is one pi version and its source archive's checksum.
type PiRelease struct {
	Version      string    `json:"version"`
	SourceSHA256 string    `json:"source_sha256"`
	ResolvedAt   time.Time `json:"resolved_at"`
}

// PiVolumeName is the volume holding one pi build.
func PiVolumeName(version, libc, arch string) string {
	return fmt.Sprintf("%s%s-%s-%s", piVolumePrefix, version, libc, arch)
}

// PiAuthDir is the shared login directory on the host.
func PiAuthDir(agentDir string) string { return filepath.Join(agentDir, "pi", "auth") }

// PiProjectDir is the sandboxes' pi session directory for a host project
// directory.
func PiProjectDir(agentDir, hostProjectDir string) string {
	return filepath.Join(agentDir, "pi", "projects", ProjectKey(hostProjectDir))
}

// PiSessionKey is pi's directory name for a working directory's sessions.
// Like pi, it drops one leading separator only.
func PiSessionKey(cwd string) string {
	if strings.HasPrefix(cwd, "/") || strings.HasPrefix(cwd, "\\") {
		cwd = cwd[1:]
	}
	return "--" + strings.NewReplacer("/", "-", "\\", "-", ":", "-").Replace(cwd) + "--"
}

// ResolvePi returns pi's newest release, from the cache in dir when it is
// younger than maxAge (0: always ask).
func ResolvePi(ctx context.Context, dir string, maxAge time.Duration) (*PiRelease, error) {
	cachePath := filepath.Join(dir, "pi", "release.json")
	var cached *PiRelease
	if b, err := os.ReadFile(cachePath); err == nil {
		var r PiRelease
		if json.Unmarshal(b, &r) == nil && piVersionRE.MatchString(r.Version) && sha256HexRE.MatchString(r.SourceSHA256) {
			cached = &r
		}
	}
	if cached != nil && time.Since(cached.ResolvedAt) < maxAge {
		return cached, nil
	}
	rel, err := resolvePi(ctx, &http.Client{Timeout: 30 * time.Second}, "https://api.github.com", "https://github.com")
	if err != nil {
		if cached != nil {
			return cached, nil
		}
		return nil, fmt.Errorf("which pi to use (github.com/%s): %w", PiRepo, err)
	}
	rel.ResolvedAt = time.Now()
	if b, err := json.MarshalIndent(rel, "", "  "); err == nil {
		_ = os.MkdirAll(filepath.Dir(cachePath), 0o700)
		_ = os.WriteFile(cachePath, b, 0o600)
	}
	return rel, nil
}

func resolvePi(ctx context.Context, client *http.Client, api, web string) (*PiRelease, error) {
	get := func(url string, max int64) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		res, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("%s: %s", url, res.Status)
		}
		return io.ReadAll(io.LimitReader(res.Body, max))
	}
	b, err := get(api+"/repos/"+PiRepo+"/releases/latest", 1<<20)
	if err != nil {
		return nil, err
	}
	var latest struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(b, &latest); err != nil {
		return nil, err
	}
	version := strings.TrimPrefix(latest.TagName, "v")
	if !piVersionRE.MatchString(version) {
		return nil, fmt.Errorf("latest release %q is not a version", latest.TagName)
	}
	sums, err := get(web+"/"+PiRepo+"/releases/download/v"+version+"/SHA256SUMS", 64<<10)
	if err != nil {
		return nil, err
	}
	asset := "pi-" + version + "-source.tar.gz"
	for _, line := range strings.Split(string(sums), "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[1] == asset && sha256HexRE.MatchString(f[0]) {
			return &PiRelease{Version: version, SourceSHA256: f[0]}, nil
		}
	}
	return nil, fmt.Errorf("SHA256SUMS of v%s has no %s", version, asset)
}

// piFetch runs in Alpine: the source archive and Bun into the cache, checked
// against their SHA-256, and (when the musl build is made) the C++ runtime
// it needs into its volume (lib/, used only when the image has none).
const piFetch = `set -eu
apk add -q --no-cache curl unzip libstdc++ libgcc >/dev/null
cd /cache
src="pi-$PI_VERSION-source.tar.gz"
if [ ! -f "$src.ok" ]; then
  t=$(mktemp ./src.XXXXXX)
  curl -fsSL --retry 3 -o "$t" "https://github.com/` + PiRepo + `/releases/download/v$PI_VERSION/$src"
  echo "$PI_SHA256  $t" | sha256sum -c - >/dev/null || { rm -f "$t"; echo "$src: wrong checksum" >&2; exit 1; }
  mv "$t" "$src" && touch "$src.ok"
fi
if [ ! -x "bun-$BUN_VERSION/bun" ]; then
  t=$(mktemp ./bun.XXXXXX)
  curl -fsSL --retry 3 -o "$t" "$BUN_URL"
  echo "$BUN_SHA256  $t" | sha256sum -c - >/dev/null || { rm -f "$t"; echo "Bun $BUN_VERSION: wrong checksum" >&2; exit 1; }
  rm -rf "bun-$BUN_VERSION.tmp" && mkdir "bun-$BUN_VERSION.tmp"
  unzip -q -j -d "bun-$BUN_VERSION.tmp" "$t" '*/bun' && rm -f "$t"
  rm -rf "bun-$BUN_VERSION" && mv "bun-$BUN_VERSION.tmp" "bun-$BUN_VERSION"
fi
if [ -d /musl ]; then
  rm -f /musl/.ok && mkdir -p /musl/lib
  cp -L /usr/lib/libstdc++.so.6 /usr/lib/libgcc_s.so.1 /musl/lib/
fi`

// piBuild runs in Node: builds pi like its release workflow (bundled model
// catalog) and compiles it for the libc variants in $LIBCS into /<libc>,
// with the files pi expects next to its binary and the bin/pi launcher.
const piBuild = `set -eu
export PATH="/cache/bun-$BUN_VERSION:$PATH" npm_config_cache=/cache/npm
w=$(mktemp -d) && trap 'rm -rf "$w"' EXIT && cd "$w"
log="$w/build.log"
run() { "$@" >>"$log" 2>&1 || { tail -40 "$log" >&2; exit 1; }; }
tar -xzf "/cache/pi-$PI_VERSION-source.tar.gz"
cd "$(find . -mindepth 1 -maxdepth 1 -type d | head -1)"
run npm ci --ignore-scripts --no-audit --no-fund
run npm run build:offline
cd packages/coding-agent
for libc in $LIBCS; do
  out=/$libc
  rm -f "$out/.ok"
  target="bun-linux-$BUN_ARCH"
  [ "$libc" = musl ] && target="$target-musl"
  [ "$BUN_ARCH" = x64 ] && target="$target-baseline"
  run bun build --compile --no-compile-autoload-bunfig --no-compile-autoload-dotenv --compile-autoload-package-json \
    --target="$target" ./dist/bun/cli.js ./src/utils/image-resize-worker.ts ./src/extensions/codemode/worker.ts \
    --outfile "$out/pi.tmp"
  rm -rf "$out/theme" "$out/assets" "$out/export-html" "$out/docs" "$out/examples" "$out/native" "$out/bin"
  cp package.json README.md CHANGELOG.md ../../node_modules/@silvia-odwyer/photon-node/photon_rs_bg.wasm "$out/"
  mkdir -p "$out/theme" "$out/assets" "$out/native/linux/prebuilds" "$out/bin"
  cp dist/modes/interactive/theme/*.json "$out/theme/"
  cp dist/modes/interactive/assets/* "$out/assets/"
  cp -r dist/core/export-html docs examples "$out/"
  cp -R "../tui/native/linux/prebuilds/linux-$BUN_ARCH" "$out/native/linux/prebuilds/"
  mv "$out/pi.tmp" "$out/pi"
  if [ "$libc" = musl ]; then
    # The C++ runtime from lib/ only when the image has none: given to pi
    # alone, and taken out of its environment again by zdev's extension.
    cat >"$out/bin/pi" <<'SH'
#!/bin/sh
for d in /usr/lib /lib /usr/local/lib; do
  [ -e "$d/libstdc++.so.6" ] && exec ` + PiMount + `/pi "$@"
done
export ZDEV_PI_LIB=` + PiMount + `/lib
export LD_LIBRARY_PATH="$ZDEV_PI_LIB${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"
exec ` + PiMount + `/pi "$@"
SH
  else
    printf '#!/bin/sh\nexec ` + PiMount + `/pi "$@"\n' >"$out/bin/pi"
  fi
  chmod 755 "$out/bin/pi"
done
[ -d /glibc ] && /glibc/pi --version >/dev/null
for libc in $LIBCS; do echo "$PI_VERSION" >"/$libc/.ok"; done`

// bunArch is Bun's name for a Docker architecture.
func bunArch(arch string) string {
	if arch == "arm64" {
		return "arm64"
	}
	return "x64"
}

// PiBuilt reports whether this version's builds for arch are complete.
func PiBuilt(ctx context.Context, d Docker, version, arch string) bool {
	return complete(ctx, d, PiVolumeName(version, LibcGlibc, arch)) && complete(ctx, d, PiVolumeName(version, LibcMusl, arch))
}

// BuildPi builds the release for arch into its glibc and musl volumes unless
// they are complete (only the missing ones). One build at a time per machine (a file lock in dir):
// a second caller waits for the first (waiting is called first) and then
// finds the volumes complete. log gets the steps' output. Whatever the
// outcome, only complete build volumes stay.
func BuildPi(ctx context.Context, d Docker, dir string, rel *PiRelease, arch string, log io.Writer, waiting func()) (err error) {
	if PiBuilt(ctx, d, rel.Version, arch) {
		return nil
	}
	if PiBuilding(dir) && waiting != nil {
		waiting()
	}
	unlock, err := lockFile(filepath.Join(dir, "pi", "build.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	if PiBuilt(ctx, d, rel.Version, arch) {
		return nil
	}
	// Cleanup runs even when ctx was cancelled (Ctrl-C during the build).
	bg := context.WithoutCancel(ctx)
	// Only what is missing is built: a complete variant may be mounted by
	// running sessions and stays untouched (the other one can be missing
	// after the sweep removed it as unused).
	var libcs, vols []string
	for _, libc := range []string{LibcGlibc, LibcMusl} {
		if v := PiVolumeName(rel.Version, libc, arch); !complete(ctx, d, v) {
			libcs, vols = append(libcs, libc), append(vols, v)
		}
	}
	// Created (with their label) right before each step that mounts them:
	// mounting a volume that is gone would make Docker create it without
	// the label, invisible to InstalledPi and to the sweep.
	createVols := func() error {
		for _, v := range vols {
			if err := d.CreateVolume(ctx, v, map[string]string{volumeLabel: "pi", "zdev.agent.pi": rel.Version}); err != nil {
				return err
			}
		}
		return nil
	}
	defer func() {
		if err != nil {
			_, _ = d.Output(bg, append([]string{"volume", "rm", "-f"}, vols...)...)
		}
	}()
	cacheVol := "zdev-pi-build-" + rel.Version + "-" + arch
	if err := d.CreateVolume(ctx, cacheVol, map[string]string{volumeLabel: "pi-build"}); err != nil {
		return err
	}
	defer func() { _, _ = d.Output(bg, "volume", "rm", "-f", cacheVol) }()
	if _, err := d.ImageID(ctx, config.PiBuildImage); err != nil {
		defer func() { _, _ = d.Output(bg, "image", "rm", config.PiBuildImage) }()
	}
	bun := config.Bun
	bunSHA := bun.Checksum("linux", arch)
	if bunSHA == "" {
		return fmt.Errorf("no Bun build for linux/%s", arch)
	}
	env := []string{"-e", "PI_VERSION=" + rel.Version, "-e", "PI_SHA256=" + rel.SourceSHA256,
		"-e", "BUN_VERSION=" + bun.Version, "-e", "BUN_SHA256=" + bunSHA, "-e", "BUN_URL=" + bun.DownloadURL("linux", arch),
		"-e", "BUN_ARCH=" + bunArch(arch), "-e", "LIBCS=" + strings.Join(libcs, " ")}
	mounts := []string{"--mount", "type=volume,src=" + cacheVol + ",dst=/cache"}
	for i, libc := range libcs {
		mounts = append(mounts, "--mount", "type=volume,src="+vols[i]+",dst=/"+libc)
	}
	run := func(image string) []string {
		args := append(append([]string{"run", "--rm", "-i", "--user", "0", "--label", volumeLabel + "=pi-build"}, env...), mounts...)
		return append(args, "--entrypoint", "sh", image, "-s")
	}
	if err := createVols(); err != nil {
		return err
	}
	if err := d.Stream(ctx, strings.NewReader(piFetch), log, log, run(FillImage)...); err != nil {
		return fmt.Errorf("downloading pi %s's source: %w", rel.Version, err)
	}
	if err := createVols(); err != nil {
		return err
	}
	if err := d.Stream(ctx, strings.NewReader(piBuild), log, log, run(config.PiBuildImage)...); err != nil {
		return fmt.Errorf("building pi %s: %w", rel.Version, err)
	}
	return nil
}

// lockFile takes an exclusive lock on path, waiting for another holder.
func lockFile(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}

// PiBuilding reports whether a build holds the lock right now.
func PiBuilding(dir string) bool {
	f, err := os.OpenFile(filepath.Join(dir, "pi", "build.lock"), os.O_RDWR, 0)
	if err != nil {
		return false
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.Is(err, syscall.EWOULDBLOCK)
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false
}

// InstalledPi returns the newest complete pi build for libc and arch.
func InstalledPi(ctx context.Context, d Docker, libc, arch string) (version, vol string) {
	return installedBuild(ctx, d, "pi", piVolumePrefix, libc, arch, func(name string) string {
		return PiVolumeName(name, libc, arch)
	})
}

// InstalledPiArchs lists the architectures that have a pi build here.
func InstalledPiArchs(ctx context.Context, d Docker) []string {
	seen := map[string]bool{}
	var out []string
	for _, pair := range installedPlatforms(ctx, d, "pi", piVolumePrefix) {
		if _, arch, _ := strings.Cut(pair, " "); !seen[arch] {
			seen[arch] = true
			out = append(out, arch)
		}
	}
	sort.Strings(out)
	return out
}

// CheckPi runs `pi --version` once per image and pi version.
func CheckPi(ctx context.Context, d Docker, dir, image string, info *ImageInfo, version, vol string) error {
	return checkBuild(ctx, d, dir, image, info, "pi "+version, func() error {
		_, err := d.Output(ctx, "run", "--rm", "--network", "none", "--user", "65534:65534", "--cap-drop", "ALL",
			"-e", "HOME=/tmp", "-e", "PI_OFFLINE=1",
			"--mount", "type=volume,src="+vol+",dst="+PiMount+",readonly",
			"--entrypoint", PiCommand, image, "--version")
		if err != nil {
			return fmt.Errorf("pi %s (%s build) doesn't run in image %s: %w", version, info.Libc, image, err)
		}
		return nil
	})
}

// piShared are the ~/.pi/agent directories every session reads.
var piShared = []string{"skills", "prompts", "themes", "extensions"}

// PiMounts returns the --mount specs for the host's ~/.pi/agent (the shared
// directories read-only, at their own paths, which are the sandbox's agent
// directory's too), ~/.agents/skills (the Agent Skills location pi reads
// too, read-only) and the project's session directory, at the place pi
// keeps a working directory's sessions.
func PiMounts(hostHome, sessionDir, containerWorkdir string) ([]string, error) {
	hostPiAgent := filepath.Join(hostHome, ".pi", "agent")
	m := newMountSet()
	if err := m.sharedDirs(hostPiAgent, piShared); err != nil {
		return nil, err
	}
	if err := m.sharedDirs(filepath.Join(hostHome, ".agents"), []string{"skills"}); err != nil {
		return nil, err
	}
	if err := m.add(sessionDir, filepath.Join(hostPiAgent, "sessions", PiSessionKey(containerWorkdir)), false); err != nil {
		return nil, err
	}
	return m.sorted(), nil
}

// PiPrepareScript prepares pi's agent directory in the home volume, as root
// in a one-shot container with the volume at $H and the host's ~/.pi/agent
// read-only at /src and the sandbox tools at /tools: settings.json merged
// (the sandbox's own, where pi saves /model choices and the changelog it
// showed, then the host's on top, then projects trusted: the container is
// the boundary; jq from the sandbox tools, the container has no network),
// AGENTS.md, models.json without keys (PiModels), the shared login as a symlink (pi locks the file it points to, so
// sandboxes running at once take turns refreshing it), the session mount
// point, and everything handed to the agent's user.
const PiPrepareScript = `set -eu
A="$H/.pi/agent"
mkdir -p "$A/sessions/$KEY"
S="$A/settings.json"
[ -f "$S" ] || echo '{}' > "$S"
set -- "$S"
[ -f /src/settings.json ] && set -- "$S" /src/settings.json
if ! /tools/jq -s '(.[0] // {}) * (.[1] // {}) * {"defaultProjectTrust": "always"}' "$@" > "$S.tmp" 2>/dev/null; then
  # A file that doesn't parse: the host's alone, else the trust alone.
  /tools/jq '. * {"defaultProjectTrust": "always"}' /src/settings.json > "$S.tmp" 2>/dev/null ||
    echo '{"defaultProjectTrust": "always"}' > "$S.tmp"
fi
mv "$S.tmp" "$S"
for f in AGENTS.md keybindings.json; do
  if [ -f "/src/$f" ]; then cp "/src/$f" "$A/$f"; else rm -f "$A/$f"; fi
done
if [ -n "${MODELS:-}" ]; then printf '%s' "$MODELS" > "$A/models.json"; else rm -f "$A/models.json"; fi
ln -sfn ` + PiAuthMount + `/auth.json "$A/auth.json"
if [ "$(stat -c %u:%g "$H")" != "$OWNER" ]; then chown -R "$OWNER" "$H"; else chown -h "$OWNER" "$H/.pi" "$A" "$A"/* "$A/sessions" "$A/sessions/$KEY" 2>/dev/null || true; fi`

// PiPrepareEnv returns the variables for PiPrepareScript; models is
// PiModels' content ("" for none).
func PiPrepareEnv(home, workdir, owner, models string) []string {
	return []string{"H=" + home, "KEY=" + PiSessionKey(workdir), "OWNER=" + owner, "MODELS=" + models}
}

// PiModels is the host's ~/.pi/agent/models.json for the sandbox: every
// apiKey and header value that isn't a "$NAME" environment reference is
// dropped, since a literal key would enter the sandbox and a "!command"
// would run there. stripped lists the providers that lost one (their keys
// go in as zdev credentials: a placeholder in the variable, swapped in by
// the proxy). "" when there is no file; an error when it doesn't parse.
func PiModels(hostPiAgent string) (content string, stripped []string, err error) {
	b, err := os.ReadFile(filepath.Join(hostPiAgent, "models.json"))
	if os.IsNotExist(err) {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		return "", nil, fmt.Errorf("~/.pi/agent/models.json: %w", err)
	}
	keep := func(v any) bool {
		s, ok := v.(string)
		return ok && strings.HasPrefix(s, "$")
	}
	var strip func(v any) bool
	strip = func(v any) (changed bool) {
		switch x := v.(type) {
		case map[string]any:
			for k, val := range x {
				switch {
				case k == "apiKey" && !keep(val):
					delete(x, k)
					changed = true
				case k == "headers":
					if h, ok := val.(map[string]any); ok {
						for hk, hv := range h {
							if !keep(hv) {
								delete(h, hk)
								changed = true
							}
						}
					}
				default:
					if strip(val) {
						changed = true
					}
				}
			}
		case []any:
			for _, e := range x {
				if strip(e) {
					changed = true
				}
			}
		}
		return changed
	}
	if providers, ok := doc["providers"].(map[string]any); ok {
		for name, p := range providers {
			if strip(p) {
				stripped = append(stripped, name)
			}
		}
	}
	strip(doc)
	sort.Strings(stripped)
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", nil, err
	}
	return string(out) + "\n", stripped, nil
}

// PiExtension is zdev's extension, loaded with -e: it keeps the musl
// launcher's library path away from the commands pi runs and, with turnHook,
// tells the host when the agent settled (the moment a stale sandbox can be
// replaced), as Claude's Stop hook does, with pi's session id: the session a
// replaced sandbox resumes, also after /new or /resume in pi.
func PiExtension(turnHook bool) string {
	var b strings.Builder
	b.WriteString(`// zdev's extension for pi in an agent sandbox (generated).
const lib = process.env.ZDEV_PI_LIB;
if (lib) {
  const rest = (process.env.LD_LIBRARY_PATH || "").split(":").filter((p) => p && p !== lib);
  if (rest.length) process.env.LD_LIBRARY_PATH = rest.join(":");
  else delete process.env.LD_LIBRARY_PATH;
  delete process.env.ZDEV_PI_LIB;
}

export default function (pi) {
`)
	if turnHook {
		b.WriteString(`  pi.on("agent_settled", async (_event, ctx) => {
    try {
      await pi.exec("/opt/zdev/bin/zdev", ["sandbox", "turn-end", ctx.sessionManager.getSessionId()]);
    } catch {}
  });
`)
	}
	b.WriteString("}\n")
	return b.String()
}

// PiHosts are allowed under egress enforce whatever the list says: pi's
// default provider, Anthropic (API and login refresh). Other providers go in
// allow_hosts.
var PiHosts = ClaudeHosts
