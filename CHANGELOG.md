## Unreleased

### Features

- **Local DNS fallback for routers that block the wildcard record.** zdev's URLs rely on `*.0ploy.dev` resolving to `127.0.0.1`; some routers enforce DNS rebinding protection and strip that loopback answer, so projects fail to resolve on those networks. `zdev systemcheck` now detects this and offers to fix it, or run `zdev dns enable` directly: a small local dnsmasq container answers the zdev domain authoritatively, and your OS is pointed at it **for that domain only** (via `/etc/resolver` on macOS, a systemd-resolved drop-in on Linux) - the router is bypassed for the zdev domain while all other DNS is untouched. One `sudo` prompt configures the host resolver. `zdev dns status` shows the current state and `zdev dns disable` reverts it (cleaning up all host resolver config zdev wrote). The fallback covers the configured domain and every subdomain under it; a project that overrides its domain to a different base domain is not covered.

## v0.9.2

### Bug Fixes

- **`zdev self-update` run from a locally built binary no longer hijacks the global install.** The legacy-layout migration ran before the version check and unconditionally copied the running binary to `~/.zdev/bin/zdev` - so invoking `self-update` from a source build silently replaced the installed zdev with the dev build (usually followed by "Already up to date") and turned the local build artifact into a symlink. Migration now only runs when the executable is the zdev that `PATH` resolves to; from any other location, `self-update` still updates the canonical install but never copies itself there or touches the local file.

## v0.9.1

### Improvements

- **Automatic updates now announce themselves.** The once-a-day auto-updater used to download and install new versions completely silently - the only clue was an unexplained pause of up to a minute, and the "updated" banner intended for the next run could never fire on the standard install layout. zdev now prints a one-line notice to stderr when it starts installing an update ("zdev vX.Y.Z is available, installing update..."), confirms success ("updated to vX.Y.Z; the next zdev command will use it"), and on a failed install points to `zdev self-update` instead of staying silent.

## v0.9.0

### Features

- **1Password Environments as the project secrets source.** Two forms of one token: `services.<name>.op-env: <environment-id>` injects EVERY variable of a [1Password Environment](https://www.1password.dev/environments) into the container env (explicit `environment:` entries win), and `op-env://<environment-id>/<VARIABLE>` values in `environment:` inject single variables - self-contained references, so one project can mix multiple Environments, with `${VAR}` substitution for keeping the ID DRY (`variables: {op-env: <id>}` then `op-env: ${op-env}`). The Environment ID is not secret (1Password app > Developer > Environments > Copy environment ID) and safe to commit. Values are resolved through the 1Password CLI when the container is created, so config stays committable and the team manages secrets in one place with 1Password's Environments UI (.env import included). Resolution only happens at container creation - `zdev start` of an existing container, `restart`, `status`, and no-op `update` never contact 1Password - and each distinct Environment is fetched in one cached `op environment read` call (at most one Touch ID prompt per operation; values never touch disk). On recreates, secrets are resolved BEFORE the old container is removed, so a resolution failure leaves the running service intact. The container config hash covers the unresolved reference and the attached Environment ID (stamped as a `zdev.op-env` label), so changing the wiring recreates services while secret rotation never causes surprise recreates; run `zdev update --refresh-secrets` to check 1Password and recreate only services whose values actually changed - including variables added to or removed from an attached Environment (tracked via a one-way `zdev.secrets-hash` label - no secret material is stored anywhere). When `op` is missing, zdev offers to `brew install 1password-cli@beta`; when signed out, it drives `op signin` interactively (TTY only - non-interactive runs get an actionable error instead). For CI, set `OP_SERVICE_ACCOUNT_TOKEN` (service account with read access to the Environment) - zdev passes it through to `op`. `zdev systemcheck` reports the CLI, Environments support, and sign-in state. **Heads-up:** 1Password Environments are beta (CLI >= 2.33.0-beta.02 required), and configs using `op-env` require zdev with this feature - older binaries reject the unknown field.

### Bug Fixes

- **`ZDEV_HOME` now relocates ALL zdev storage.** Previously only the global config, Mutagen session tracking, and `${ZDEV_HOME}` config substitution honored the variable, while the project registry (state.yaml), certificates, downloaded tools, the Adminer config, and the self-update cache were hardcoded to `~/.zdev` - setting `ZDEV_HOME` produced a split installation. Every path now flows through `config.GetZdevHome()`.

## v0.8.0

### Features

- **`services.<name>.dockerfile` builds custom dev images automatically.** Point a service at a Dockerfile instead of a pre-built image (`dockerfile: .zdev/Dockerfile`) and `zdev start` builds it when it's missing or the Dockerfile changed - a fresh clone starts with zero manual build steps. Staleness is tracked via a `zdev.build-hash` image label (sha256 of the Dockerfile contents; editing bind-mounted source never triggers a rebuild, and Docker's layer cache keeps real rebuilds fast). `zdev update` treats a stale image as config drift and recreates the service with the rebuilt image; `zdev start` recreates an existing container when the image was rebuilt underneath it. The build context is always the project root, so `COPY` paths resolve from there and a root `.dockerignore` trims large repos. The tag defaults to `zdev-<project>-<service>:latest`; set `image:` alongside `dockerfile:` to name it. New `--build` / `--no-build` flags on `start` and `update` force or skip building (`--no-build` fails clearly on a missing image). Dev containers only by design: build args, multi-stage targets, custom contexts, secrets, multi-arch, and registry push stay with a custom build command plus `image:`. **Heads-up:** configs using `dockerfile:` require zdev >= 0.8.0 - older binaries reject the unknown field.

## v0.7.7

### Features

- **Per-service Mutagen ownership** via `services.<name>.mutagen.{user,group,file_mode,directory_mode}` in `.zdev/config.yaml`. Maps to Mutagen's `--default-*-beta` flags. Fixes the "files synced as root, but apache/nginx/node runs as a non-root user and can't read them" class of bugs for custom images. Example for PHP/Apache: `mutagen: { user: www-data, group: www-data }`. zdev tracks the values per session at `~/.zdev/mutagen/sessions/<session>` and, when they change, terminates the session, recreates it, and runs `chown -R` inside the container so PRE-EXISTING files also pick up the new ownership (Mutagen's flags only stamp newly-created files).
- **Alternative Docker engines (OrbStack, Colima) are now supported.** zdev resolves the active docker context's real socket path when mounting the Docker socket into the Traefik router and Dozzle, instead of assuming the legacy `/var/run/docker.sock` host symlink exists. That symlink isn't guaranteed on OrbStack (only created with admin) or Colima, so the router and log viewer previously got a dead socket on those engines. Honors `DOCKER_HOST` and falls back to `/var/run/docker.sock`. See `ALTERNATIVE_BACKENDS.md` for a cross-engine filesystem benchmark and trade-offs (TL;DR: OrbStack is fastest, Colima is the free no-GUI option, keep Mutagen on everywhere).

### Bug Fixes

- **`zdev update` now picks up Mutagen-only config changes.** Previously, changing `mutagen.user` (or any other Mutagen-only knob) didn't trigger session recreation because `Update` only ran the Mutagen prepare/finalize steps when a container actually needed recreating. Now those steps run unconditionally, so drift detection fires on every `zdev update`. No-op when the project doesn't use Mutagen.

## v0.7.6

### Features

- **`.zdev/local/config.yaml` deep-merges into `.zdev/config.yaml`** before variable substitution. Lets you keep per-developer overrides and secrets out of git: define `variables:` in the local file and reference them as `${VAR}` from the committed config, or override service `image:` / `environment:` / `command:` for your machine. Maps merge recursively; scalars and slices replace. Add `.zdev/local/` to `.gitignore`.

### Bug Fixes

- **Detect a manually-edited `name:` in `.zdev/config.yaml`** before the port-ownership check fires. `zdev update` and `zdev start` now error out with a clear message pointing to `zdev rename` instead of the misleading "port already used by project X" that came from the old name still being registered in state for the same directory.

## v0.7.5

### Tests

- **Add `TestProject_PerServiceLifecycle` integration test** (`-tags=integration`) that exercises `StartService` / `StopService` / `RestartService` against real Docker on the multi-service `testdata/projects/full` fixture. Verifies the per-service variants only touch the named container, leave siblings untouched, and error cleanly on unknown service names without Docker side effects. Closes a coverage gap from v0.7.2-v0.7.4 where these methods only had mock-runtime unit tests.

## v0.7.4

### Features

- **`zdev stop <service>`** scopes the stop to a single container; other services keep running. Symmetric to the per-service `zdev start`/`zdev restart` added in v0.7.2. Pauses only the Mutagen sync sessions tied to that service.

## v0.7.3

### Bug Fixes

- **Justfile cwd fix in v0.7.2 was incomplete and didn't actually work.** `just` chdirs to the justfile's parent directory internally when `--justfile` is passed, so the v0.7.2 fix (only `exec.Cmd.Dir`) was overridden and recipes still ran from `.zdev/commands/`. v0.7.3 also passes `--working-directory` so recipes really do run from the project root. Verified with a regression test that writes `pwd` to a marker file from inside a recipe.

## v0.7.2

### Bug Fixes

- **Justfile recipes now run from the project root**, not `.zdev/commands/`. Relative paths in `setup.just` like `docker build -f services/app/Dockerfile services/app/` now resolve where users author them. Previously `just` was invoked with cwd set to the directory of the justfile, so paths relative to the project root failed with "path not found" while the same command worked when run by hand.
- **Fix flaky `TestManager_DocsRoutes/NonExistingReturns302`** under `make test-integration`. The test fabricated a synthetic `GlobalConfig{SSL.Enabled: false}` while concurrently-running `internal/project` integration tests' deferred restore reloaded the real global config and rewrote `~/.zdev/traefik/docs.yaml` to match. Now the test loads the same real global config so its expected redirect URL stays consistent regardless of who writes `docs.yaml` last.

### Features

- **`zdev start <service>` and `zdev restart <service>`** scope the action to a single container. Project-wide setup (network, volumes, state registration, shared service connections) runs idempotently, so single-service starts work whether or not the project has been started before. `zdev restart <service>` is a true in-place bounce - to pick up config changes use `zdev update`.

## v0.7.1

### Bug Fixes

- **Fix `just` auto-download URL on fresh installs** - `JustURLTemplate` was missing the version segment in the filename and produced 404s like `just-aarch64-apple-darwin.tar.gz`. Real release filenames embed the version twice: `just-1.49.0-aarch64-apple-darwin.tar.gz`. The bug was masked on machines that already had `just` in `PATH` (e.g. via Homebrew), since `EnsureTool` checks `PATH` before downloading. The strengthened `TestBuildDownloadURLWithCustomBuilder` now asserts the exact URL string so this can't regress unnoticed.

### Features

- **`zdev systemcheck` now reports `just` and `mutagen` versions** alongside the existing `mkcert` line. Both are auto-downloaded on demand (`just` on first project command, `mutagen` on first sync-enabled start), so absence shows as `SKIP (not yet downloaded)` rather than a failure. The `mutagen` line honors `IsMutagenEnabled()` (Linux / explicit `false` shows `SKIP (disabled in global config)`).

## v0.7.0

### BREAKING: Project renamed scdev → zdev, moved to github.com/0ploy

- **Binary renamed `scdev` → `zdev`.** The CLI, all subcommands, and the install path (`~/.zdev/bin/zdev`) follow.
- **Module path is now `github.com/0ploy/zdev`.** Old `github.com/ScaleCommerce-DEV/scdev` URLs redirect (GitHub permanent transfer redirect), but the canonical home is `github.com/0ploy/zdev`. Old `git clone` and `raw.githubusercontent.com` URLs continue to resolve via the redirect.
- **Default domain changed `scalecommerce.site` → `0ploy.dev`.** Projects are now reachable at `https://<name>.0ploy.dev`, shared services at `https://<svc>.shared.0ploy.dev`. Wildcard DNS `*.0ploy.dev` resolves to `127.0.0.1`.
- **Global state directory `~/.scdev/` → `~/.zdev/`.** Project marker `.scdev/` → `.zdev/`. Container hostname suffix `.scdev` → `.zdev`. Container labels `scdev.*` → `zdev.*`. Network prefix `scdev_link_*` → `zdev_link_*`. Sync-volume names `sync.<svc>.<project>.scdev` → `sync.<svc>.<project>.zdev`.
- **Template repos renamed `scdev-template-*` → `zdev-template-*`** under the `0ploy` org. `zdev create <name>` resolves against `github.com/0ploy/zdev-template-<name>`.

### Upgrade (clean break - no in-place migration)

There is no automatic state migration from scdev. Old `~/.scdev/state.yaml` references containers/volumes/networks under the previous naming scheme; the new binary will not see them.

1. With the **old `scdev` binary**: `scdev down -v` in every project you have registered (or `scdev list` then loop). This stops containers, removes networks, and (with `-v`) removes named volumes you don't need anymore.
2. Stop the old shared services: `scdev services stop`. Then remove the old state directory: `rm -rf ~/.scdev`.
3. Install zdev: `curl -fsSL https://raw.githubusercontent.com/0ploy/zdev/main/install.sh | sh`
4. `zdev systemcheck` - regenerates the mkcert wildcard cert for `*.0ploy.dev` (the local mkcert root CA from the previous install is reused, so browsers keep trusting).
5. In each project on disk: `mv .scdev .zdev`. The contents of the directory (config.yaml, commands/) are unchanged in shape; their previously-loaded `${SCDEV_DOMAIN}` / `${SCDEV_HOME}` placeholders are now `${ZDEV_DOMAIN}` / `${ZDEV_HOME}`. If you wrote any custom `.just` files that referenced these, update them.
6. `zdev start` - fresh containers, fresh certs, fresh state.

`zdev self-update` is broken across this rename (the binary you have was built against the old module path and old release URL). Use the install.sh one-liner above; future `zdev self-update` runs (against `0ploy/zdev` releases) will work normally.



### Features

- **New `logs` shared service - browser log viewer (Dozzle)** at `https://logs.shared.<domain>`. Tails container stdout/stderr in real time, groups containers per project via the `dev.dozzle.group` label, exposes a shell-into-container button, runs with analytics off, and persists its own UI state (notifications, saved searches) in a `scdev_logs_data` named volume. Open with `scdev logs --open` (the existing `scdev logs [service]` terminal-tail behavior is unchanged).
- **Per-project Dozzle visibility opt-in via `shared.logs: true`** - Dozzle reads the host's Docker socket but is constrained by `DOZZLE_FILTER=label=scdev.shared.logs=true`, so only project containers whose config opts in (and all shared services) appear in the UI. Other containers running on the host stay hidden.
- **Project config defaults now actually default** - `shared.router`, `shared.mail`, and `shared.logs` default to `true`; `shared.db` and `shared.redis` default to `false`. Missing fields in a partial `shared:` block keep their defaults; explicit values always win. Previously the defaulting was claimed in the README but only `shared.router` actually defaulted (and only because every fixture set it explicitly). Locked in by `TestLoadProject_PartialSharedBlockKeepsDefaults`.
- **`scdev info` and `scdev status` now print the Redis Insights URL** when `shared.redis: true` (previously skipped, even though the conditional pre-included it). Cleanup of pre-existing inconsistency, surfaced while wiring in the new logs URL.

### Upgrade Impact

- **First `scdev update` after upgrade recreates every project container.** The new Dozzle labels (`dev.dozzle.group`, `scdev.shared.logs`) participate in the per-container config-hash, so existing containers will be detected as drifted and recreated through the normal update path. Mutagen-aware recreate paths apply, so this is safe; just expect a one-time mass recreate on first update. Set `shared.logs: false` per project to opt out and avoid the recreate for that project.

## v0.6.7

### Bug Fixes

- **Fix `scdev update` dropping Mutagen-ignored data on service recreate** - when a service drifted and `scdev update` rebuilt the container, the new container was built with `mutagenEnabled=false`, silently swapping the named `sync.<svc>.<project>.scdev` volume for a raw bind mount. Anything Mutagen ignores (`vendor/`, `.setup-complete`) that lived only inside the named volume was dropped on every update. Both `Start` and `Update` now go through shared `prepareMutagen` / `finalizeMutagen` helpers; `Update` lazily prepares the Mutagen daemon on the first service that needs recreate (so a true no-op update still pays nothing), reuses that context across all recreates in the same run, and finalizes once at the end so the new containers can pass the sync-ready gate. Covered by `TestProject_MutagenUpdatePreservesSyncVolume`.
- **Suppress Docker Desktop "What's next" hints in scdev shell-outs** - Docker Desktop's CLI prints a trailing hint block ("Try Docker Debug for seamless...") after exec/logs commands by default. scdev now sets `DOCKER_CLI_HINTS=false` on every docker invocation it makes, so the hint no longer trails `scdev exec` / `scdev logs` output. Set per-invocation rather than via process env so the user's own interactive `docker` shell stays unaffected.

## v0.6.6

### Features

- **`scdev cleanup` now prunes only truly unused resources** - previously the bare command deleted every volume of the current project, and `--global` deleted volumes across every registered project plus orphans, contradicting how "cleanup" is normally understood (cf. `docker system prune`, `npm cache clean`) and making the command unsafe to run without thinking. The `--global` flag is gone; `scdev cleanup` now lists and removes (in one combined confirm): (1) state entries whose project directory is missing from disk, (2) containers carrying the `scdev.project` label whose project is no longer registered + on disk, (3) volumes not owned by any still-registered project. Orphaned containers are removed before volumes so the old "volume is in use" failure - which hit projects whose state was dropped but whose containers stayed alive - no longer blocks the volume pass. Resources belonging to still-registered projects are never touched; use `scdev remove` for a full project tear-down.

## v0.6.5

### Features

- **Transparent forwarding of colon-namespaced subcommands via `scdev <cmd>`** - when a justfile declares a recipe literally named after the file (e.g. `console *args:` in `console.just`), scdev now invokes just as `just -f console.just console <args...>` so arguments containing colons (`cache:clear`, `db:migrate`, `dal:refresh:index`) flow through as recipe parameters instead of being parsed as just's module-path separator. Template authors can now wrap CLIs like `bin/console` or `artisan` with a single catch-all recipe - `scdev console cache:clear` forwards straight to `bin/console cache:clear` inside the container. Without a filename-matching recipe, behavior is unchanged (first arg is the recipe name), so existing templates keep working.

## v0.6.4

### Bug Fixes

- **Fix `scdev exec` printing `Error: exit status N` on non-zero shell exit** - when an interactive shell started via `scdev exec <service> sh` exited with a non-zero code (Ctrl+D on some shells returns 130, a failed command inside the shell returns its own code), cobra wrapped the child's exit in `Error: exit status N` and the scdev process itself exited with 1, losing the original exit code. Ctrl+D out of an exec'd shell now returns cleanly to the host shell with no error output, and any non-zero child exit code is propagated verbatim instead of being squashed to 1. Matches how `docker exec` and standard shells behave.

## v0.6.3

### Bug Fixes

- **Fix auto-update rate-limit death spiral on shared IPs** - when the GitHub API call failed (rate limit, 5xx, transient network error), `refreshAndInstall` returned without writing `~/.scdev/update-check.json`, so the next invocation retried immediately instead of waiting the 24h TTL. On a shared egress IP (office NAT, VPN, CI pool) once any user hit GitHub's 60-req/hour unauthenticated limit, every retrying user kept burning credits faster than the window refilled, starving everyone else's next-day refresh. The cache is now always written on error with `LastChecked` bumped and prev `ETag`/`LatestTag`/`InstalledTag` preserved, so a failed attempt counts as "checked recently."

## v0.6.2

### Bug Fixes

- **Fix auto-update never running on fast commands** - the background update check was launched as an unjoined goroutine, so Go's runtime killed it when `main()` returned. Fast commands like `scdev version`, `scdev list`, and `scdev status` finish before the GitHub API round-trip completes, so `~/.scdev/update-check.json` was never written and auto-update effectively never ran. Made the refresh synchronous: API probe bounded to 3s, download/install bounded to 60s. Cache-hit path (99% of invocations) is unchanged at ~6ms; stale-cache path now blocks for ~200ms on a 304 or ~1-2s when a new release is actually being downloaded. Cost is paid at most once per 24h per machine.

## v0.6.1

### Features

- **`scdev open` command** - opens the current project's URL in the default browser. `scdev open` uses the project in the working directory; `scdev open <name>` looks up any registered project by name. Protocol follows the global SSL setting (http/https). Closes the common "I need the URL again, let me run `scdev info` and copy-paste" loop.

## v0.6.0

### Features

- **Background auto-update with banner** - scdev checks for updates in the background (at most once per 24h, conditional GET with ETag) and, if the running binary is in the symlink layout, silently downloads the new release into `~/.scdev/bin/scdev`. No sudo, no re-exec. The next invocation prints a one-line banner informing you that you've been upgraded. Legacy installs (plain binary in `/usr/local/bin/`) still get a "run `scdev self-update` to migrate" banner. Opt out with `SCDEV_NO_UPDATE_CHECK=1`; CI environments are detected automatically.
- **Checksum-verified installs and updates** - every path that downloads an scdev binary (`install.sh`, `scdev self-update`, the background auto-update) now fetches `checksums.txt` alongside the binary and verifies SHA256 before chmod-ing it executable. A compromised release asset or HTTPS MITM via a rogue CA can no longer silently run code on your machine. Releases without `checksums.txt` (pre-0.6) are rejected rather than falling back to unchecked install.

### Bug Fixes

- **Fix stale shared-service containers silently breaking after config changes** - if a shared service (Mail, DB UI, Redis Insights, Router) was created while SSL was off and SSL later got flipped on (or image, domain, or dashboard settings changed), `scdev services start` would just start the stale container instead of recreating it, so the new Traefik labels never took effect. Symptoms: `https://db.shared.<domain>` redirecting to the docs page, HTTPS URLs for other shared services falling through to the 404 catch-all. Shared services now carry the same `scdev.config-hash` label that project services use; `startService` compares it to the hash of the freshly built expected config on every call and recreates the container on drift. The router keeps its "don't recreate just to shrink ports" behavior (other projects' ports would be dropped) via a union-of-ports check before hashing.
- **Fix lost updates to the state file** - every mutating operation on the state manager (`RegisterProject`, `CreateLink`, `AddLinkMembers`, `RenameProject`, etc.) used to Load then Save as two independent locked calls, so a concurrent goroutine (like the background update-check doing its own work in parallel with `scdev start`) could interleave and clobber changes. Operations now hold the manager's lock for the entire read-modify-write cycle via a new `Mutate(fn)` helper. Cross-process concurrency (two `scdev` invocations at once on the same state file) is still unprotected - documented, not a regression.
- **Fix `scdev self-update` hanging indefinitely on slow GitHub responses** - the GitHub API call and binary download now use a 5-minute context timeout, matching what the background update-check was already doing. Previously a stalled GitHub response meant the user had to Ctrl-C.

### Improvements

- **Remove unused config fields** - `shared.tunnel` and `shared.observability` were defined in config but never wired to any service manager; enabling them did nothing except (for observability) print an `observe.shared.<domain>` URL in `scdev info` that 404'd. `pre_start` was in `ServiceConfig` and documented in the README but no code ever executed the listed commands. All three are gone. Re-implementation tracked as SC-127, SC-128, SC-146.
- **Remove phantom `persist_on_delete` help text** - `scdev down -v` and `scdev remove -v` flag help advertised "respects persist_on_delete" but no such field exists anywhere in config or code. Removed the claim; if anyone wants the feature for real it's tracked as SC-147.
- **Smaller, safer `cmd/*` bootstrap** - new `withProject(timeout, fn)` / `withDocker(timeout, fn)` helpers in `cmd/shared.go` collapse the timeout + Docker-availability + project-load boilerplate that every `runXxx` repeated. Roughly 150 lines of duplication gone across 15+ commands.
- **Single shared-service registry** - shared service metadata (name, subdomain, container name, start/stop/status/connect/disconnect funcs, per-project opt-in flag) now lives in one place: `services.AllSharedServices()`. Adding a new shared service is a single-file change instead of updating two hand-rolled registries (`cmd/services.go` and `internal/project/shared_services.go`) plus hoping they stay in sync.
- **Other small cleanups** - `strings.Join`/`strconv.Itoa` for the router's port CSV label, `html.EscapeString` replaces a hand-written HTML escaper on the docs page, shared `teardownContainers` helper between `Project.Down` and `Project.Rename`, and cleaner early-return control flow in `NetworkDisconnect`.

## v0.5.6

### Improvements

- **`scdev step` styling refinement** - the whole header line is now bold cyan (not just the `▶`), and a trailing blank line separates the header from the command output that follows. Each phase reads as a framed section, not a single colored glyph next to white text.

## v0.5.5

### Features

- **`scdev step <message>` subcommand** - prints a visually distinct progress marker (two leading blank lines, cyan `▶`, bold text) for use inside `.scdev/commands/*.just` recipes. During `scdev setup`, top-level status messages like "Installing PHP extensions" previously got buried in the wall of apk/composer/npm output; `@scdev step "..."` replaces `@echo` for phase headers so they pop against the noise. Auto-plain on non-TTY, `NO_COLOR`, or global plain mode. `scdev start`'s own "Starting project ..." line now uses the same helper.

### Improvements

- **Template authoring docs updated** - `templates/README.md`, `skills/scdev/references/templates.md`, and the scdev skill now recommend `@scdev step` for setup.just phase headers and explain why, with the base/Nuxt/Symfony examples rewritten to use it.

## v0.5.4

### Bug Fixes

- **Fix `scdev update` ignoring env/image/volume/command changes** - `serviceNeedsRecreate` only compared Traefik labels and bailed out with "Project is up to date" on any other change, leaving stale containers running. It now compares a deterministic `scdev.config-hash` label stamped on each container, covering image, env, volumes, command, working dir, routing, published ports, and network aliases. Containers created before the label existed are recreated once on first update after upgrade.

### Improvements

- **Skill updates for template creation and existing-project setup** - expanded `skills/scdev/` references (new stack-gotchas reference, richer config examples and template authoring notes, updated SKILL.md with rename/link commands and per-service `routing.domain` guidance) to help agents scaffold new templates and onboard scdev into existing projects.
- **README and template docs: Symfony/Sylius/Laravel trusted-proxies troubleshooting** - documented the mixed-content / stuck debug toolbar / broken admin login failure mode caused by Traefik terminating HTTPS without a trusted-proxy env var, plus PHP template authoring tips (memory_limit drop-in, install-php-extensions, asset-pipeline builds).

## v0.5.3

### Bug Fixes

- **Fix systemcheck reporting "All checks passed" when CA is not trusted** - `checkCA` only checked whether CA files exist, not whether the CA is trusted by the system. On a new install where the user skips CA installation, first-run would say "Setup incomplete" but systemcheck would say "All checks passed!" immediately after. Now systemcheck verifies trust status and reports "not trusted by system" with instructions to fix.

## v0.5.2

### Bug Fixes

- **Fix RedisInsights image not resolved on new installs** - `${RedisInsightsImage}` was missing from the substitution map in `generateDefaultGlobalConfig()`, causing the literal string to be written to `~/.scdev/global-config.yaml` on first run. Docker then failed with "invalid reference format". Existing installs were unaffected because their config file predated the redis_insights section.

### Maintenance

- Bump Go toolchain from 1.25.1 to 1.26.2
- Bump just from 1.40.0 to 1.49.0
- Bump golang.org/x/term from v0.39.0 to v0.42.0
- Bump golang.org/x/sys from v0.40.0 to v0.43.0
- Bump github.com/spf13/pflag from v1.0.9 to v1.0.10

## v0.5.1

### Features

- **`scdev link` command** - create named link networks for direct container-to-container communication between separate projects
  - `scdev link create <name>` / `scdev link delete <name>` - manage named link networks
  - `scdev link join <name> <member>...` / `scdev link leave <name> <member>...` - add/remove projects or individual services
  - `scdev link ls` - list all links and their members
  - `scdev link status <name>` - show members and connection state
  - Members can be whole projects (`sec-scan`) or specific services (`redis-debug.app`)
  - Each link creates a dedicated Docker network (`scdev_link_<name>`) for isolation between link groups
  - Containers resolve each other by container name via Docker's embedded DNS (e.g., `app.project-b.scdev`)
  - Links persist in global state and auto-reconnect on `scdev start`
  - Validation: link name characters, project/service existence, duplicate prevention
- **`scdev info` / `scdev status` / `scdev list` show link information** - links are displayed alongside services and shared services

- **`scdev rename` command** - rename a project with full Docker resource migration
  - Stops containers, migrates volume data (named + Mutagen sync), removes old network
  - Updates state file and link memberships atomically
  - Rewrites `name:` in `.scdev/config.yaml` (preserves formatting and comments)
  - Restarts project with new name
  - Confirmation prompt (skip with `--force`)
  - Validates new name is DNS-safe and not already taken

### Bug Fixes

- **Fix variables in typed config fields** - `${VAR}` placeholders in int/bool fields (e.g., `port: ${PORT}`, `router: ${ENABLE}`) caused "cannot unmarshal !!str into int" errors. The first config parse pass now uses a minimal struct, deferring typed field parsing until after variable substitution.

### Improvements

- Added `ContainerNameFor()` standalone helper for building container names without a loaded Project
- Added `CopyVolume()` to Runtime interface for volume data migration

## v0.4.2

### Bug Fixes

- **Fix shared service hostnames not resolving from project containers** - `docker network connect` now passes `--alias` flags, so shared services (e.g., `mail`, `adminer`, `redis-insights`) are resolvable by their short names on project networks, not just on the shared network
- **Fix `Down()` not releasing TCP/UDP host ports** - `Down()` now refreshes the router to drop ports the project was using, preventing port conflicts on subsequent starts (previously only done in CLI commands, not the library method)

### Improvements

- **Integration tests restore shared services** - tests that tear down shared services now snapshot what's running beforehand and restore it after, preventing silent breakage of the developer's running environment
- Updated docs (README, SKILL.md, templates/README.md) with shared service hostname reference - container-internal hostnames and ports for accessing services from app code vs browser

## v0.4.1

### Improvements

- `Down()` now handles state unregistration (previously only done in `cmd/down.go`), fixing stale entries left by integration tests
- Added integration tests for exec `--` separator, config variables in running containers, per-service routing domain, and Down() state cleanup
- Added CONTRIBUTING.md with developer guide, architecture decisions, and test strategy

## v0.4.0

### Features

- **`scdev create` command** - scaffold new projects from templates (GitHub repos or local directories)
  - Template resolution: bare name (`express`), full repo (`myorg/repo`), or local path (`./dir`)
  - `--branch` and `--tag` flags for GitHub templates
  - `--auto-start` and `--auto-setup` flags for non-interactive setup
  - DNS-safe project name validation
  - GitHub tarball download with security hardening (symlink validation, size limits, mode masking, path traversal checks)
- **Config variables** - `variables:` section for reusable `${VAR}` substitution across the config file (not passed to containers). Variables can reference built-in variables like `${PROJECTNAME}`.
- **Per-service routing domain** - `routing.domain` allows individual HTTP/HTTPS services to have custom domains (e.g. `api.my-app.scalecommerce.site` for a backend service)
- **`scdev start -q/--quiet`** - skip project info display after start (useful in scripts and setup.just)
- **Docker availability check** - all Docker-dependent commands now check if Docker is running and show a clear error message instead of confusing Docker errors
- **`scdev exec` handles `--` separator** - `scdev exec app -- cmd` now works correctly

### Templates

- Three official templates published:
  - [express](https://github.com/ScaleCommerce-DEV/scdev-template-express) - Node.js + Express hello world with `--watch` mode
  - [nuxt4](https://github.com/ScaleCommerce-DEV/scdev-template-nuxt4) - Nuxt 4 with interactive scaffolding, HMR, `nuxi prepare` for module deps
  - [symfony](https://github.com/ScaleCommerce-DEV/scdev-template-symfony) - Symfony with CLI dev server, scaffold-in-/tmp pattern
- Template Authoring Guide at `templates/README.md`
- `.setup-complete` marker pattern for solving container startup vs setup circular dependency

### Improvements

- `shared.redis_insights` renamed to `shared.redis` in project config (consistent with `shared.router`, `shared.mail`, `shared.db`)
- `buildContainerConfig` is now the single source of truth for container configuration (fixes divergence between start and update paths)
- `connectRouter` uses shared helper pattern (consistent with mail/db/redis)
- Extracted `IsDBServiceByName()` to eliminate duplicate DB detection logic
- Reduced redundant `GlobalConfig` loading in status command
- Removed dead `sync_mode` code from Mutagen sync
- Fixed unsafe `append` in cleanup command
- Supply chain security messaging in README

### Documentation

- README: Templates section, multi-service routing, configuration reference tables, supply chain security callout
- Template Authoring Guide: setup lifecycle, scaffolding patterns (in-place vs /tmp), framework-specific notes
- CLAUDE.md: templates docs, Docker check, variables, routing.domain
- scdev skill restructured with progressive disclosure (222-line SKILL.md + references/)

## v0.3.0

### Features

- **Sync-ready gate** - containers wait for Mutagen sync automatically, no more `while [ ! -f ... ]` workarounds
- **Default protocol** - `routing.protocol` defaults to `http` when `port` is set
- **Default domain** - `domain` defaults to `{name}.scalecommerce.site` when not set
- **scdev skill** - installable agent skill (`npx skills add scalecommerce-dev/scdev`) with full CLI reference, config templates for Node/PHP/Python, debugging guides, and setup workflows

### Documentation

- Complete README rewrite with architecture diagram, benchmark data, and coding agents section
- New tagline: "Ever seen a developer and an AI agent fall in love with a dev environment?"
- File sync benchmark: 5x faster cold start vs Docker bind mounts on macOS
- "Standing on the Shoulders of Giants" section crediting all underlying technologies
- TCP/UDP routing, volumes, and custom commands documented in detail
- `.pnpm-store` must be in Mutagen ignore for pnpm projects (prevents native binary corruption across image changes)

## v0.2.0

### Improvements

- Shared service registry pattern — adding a new shared service is now one entry instead of editing 6+ locations
- Split `project.go` god file (1400→868 lines) into `shared_services.go` and `mutagen_sync.go`
- Single source of truth for global config defaults (`newDefaultGlobalConfig()`) — fixes RedisInsights image missing bug
- Removed unused `StartRouterWithPorts` parameters and unnecessary `ConnectToProject`/`DisconnectFromProject` aliases
- Fixed `scdev info` not showing shared services section when only `redis_insights` is enabled
- Consistent display names across all shared service methods

## v0.1.1

### Improvements

- Auto-discover named volumes from service definitions — no more redundant top-level `volumes:` section in project config
- Streamlined CLAUDE.md and planning docs — removed stale TODOs, marked completed phases
- Removed TODO comments from code — all tracked as Completo tickets

## v0.1.0

### Initial Release

First public release of scdev - local development environment framework.

- Single-command project startup with `scdev start`
- Config-driven multi-service containers via `.scdev/config.yaml`
- Variable substitution (`${PROJECTNAME}`, `${PROJECTDIR}`, `${SCDEV_DOMAIN}`)
- Project isolation via Docker networks with inter-service DNS
- Named volumes with persistence across restarts
- Project state tracking with `scdev list` and `scdev info`
- Shared Traefik router with automatic HTTPS via mkcert
- Shared Mailpit email catcher
- Shared Adminer database UI
- Shared Redis Insights browser
- Custom project commands via justfiles (`.scdev/commands/`)
- Mutagen file sync for fast macOS development
- Docs page with dynamic project list at `docs.shared.<domain>`
- Self-update command (`scdev update`)
- Install script for macOS and Linux
