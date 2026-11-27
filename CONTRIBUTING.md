# Contributing to zdev

## Getting Started

```bash
git clone https://github.com/0ploy/zdev.git
cd zdev
zdev start           # Build and start the dev container (Go toolchain inside)
zdev run build       # Build ./zdev for this machine (+ ./zdev-linux-<arch>)
zdev run test all    # go vet + gofmt + unit tests
zdev run install     # Build and install the dev build to ~/.zdev/bin
```

Requirements: zdev and a Docker engine. No Go on the host: zdev develops itself as a zdev project. The `app` service (`.zdev/Dockerfile`) is a toolbox container that holds the Go toolchain and stays up, with nothing routed; the project commands in `.zdev/run/` run Go there and cross-compile for the host. Inside the container (`zdev exec app bash`, or a `zdev claude` session) `go` and `make` work directly. A host Go install still works with the `make` targets below.

## Project Structure

```
cmd/                     # Cobra commands (one file per command)
internal/
  config/                # Config parsing, defaults, variable substitution
  create/                # Template resolution, download, copy (zdev create)
  project/               # Project lifecycle (start, stop, down, exec, routing)
  run/                   # zdev run: job file loader, resolution, plan, runner
  justconvert/           # .just -> .zdev/run/*.toml converter (removed with just)
  runtime/               # Docker abstraction (interface + DockerCLI implementation)
  services/              # Shared infrastructure (router, mail, adminer, redis)
  mutagen/               # Mutagen binary wrapper
  state/                 # Global project registry (~/.zdev/state.yaml)
  tools/                 # External tool management (mkcert, just (deprecated), mutagen)
  firstrun/              # First-time setup (certs, shared network)
  ssl/                   # Certificate management
  ui/                    # Terminal output (colors, hyperlinks, markdown)
  agent/                 # zdev claude: Claude volumes, image probe, system prompt, fence, relay
skills/zdev/            # AI agent skill (SKILL.md + references/)
testdata/projects/       # Test fixtures (minimal, full, variables, mutagen)
```

## Testing

### Unit tests (`zdev run test unit`)

Run on every change before committing. Fast (seconds), no external dependencies.

```bash
zdev run test unit                                 # All unit tests
zdev run test unit ./internal/create/ -v           # Single package (arguments go to go test)
zdev run test unit ./... -run TestValidateName     # Single test
zdev run test all                                  # go vet + gofmt + unit tests
```

Unit tests use the mock runtime (`internal/runtime/mock.go`) for Docker operations and test fixtures in `testdata/projects/`.

### Integration tests (`zdev run test integration`)

Run before releases, after refactoring core logic (project lifecycle, routing, Mutagen), or when changing code that unit tests can't cover. Requires Docker - spins up real containers.

```bash
zdev run test integration                          # All integration tests
zdev run test integration -test.run TestProject_   # Filter (arguments go to the test binaries)
```

They drive the host's Docker, Mutagen and `~/.zdev`, so they can't run inside a container: the job compiles one test binary per package for the host OS/arch in the `app` container (`go test -c`, into the gitignored `.itest/`), waits for file sync, and runs them on the host from each package's directory, one after the other. They are refused in a `zdev claude` sandbox. With Go on the host, `make test-integration` does the same directly.

Integration tests are tagged with `//go:build integration` so they don't run with the unit tests. They create real Docker containers, networks, and volumes, and clean up after themselves (including state registry).
Both the job and the Make target run packages serially (`-p 1`) because multiple packages snapshot and restore the same shared-service containers.

### Writing tests

- **Unit tests:** Use the mock runtime. Test config parsing, variable substitution, name validation, label generation, etc.
- **Integration tests:** Use real Docker. Test full lifecycle (start/stop/down), exec, routing, Mutagen sync. Always defer `proj.Down(ctx, false)` for cleanup. `Down()` handles state unregistration and router port refresh automatically.
- **Shared service restoration:** Integration tests that tear down shared services (router, mail, db, redis) must snapshot what's running before the test and restore it afterward. Use `snapshotSharedServices`/`restoreSharedServices` helpers. Without this, tests silently break the developer's running environment.
- **Test fixtures:** Add to `testdata/projects/` for config loading tests. Keep fixtures minimal.

## Adding a New Command

1. Create `cmd/<name>.go` with a Cobra command
2. Register with `rootCmd.AddCommand()` in `init()`
3. If the command needs Docker, call `requireDocker(ctx)` at the top of `RunE`
4. Follow the existing pattern: context with timeout, `project.Load()`, return errors

```go
var myCmd = &cobra.Command{
    Use:   "mycommand",
    Short: "Brief description",
    RunE:  runMyCommand,
}

func init() {
    rootCmd.AddCommand(myCmd)
}

func runMyCommand(cmd *cobra.Command, args []string) error {
    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()

    if err := requireDocker(ctx); err != nil {
        return err
    }

    proj, err := project.Load()
    if err != nil {
        return err
    }
    // ...
}
```

## Adding a New Shared Service

This has multiple touch points that are easy to miss:

1. Create `internal/services/<service>.go` with the container name constant and a `<Service>ContainerConfig` function built on `webUIContainerConfig` (`internal/services/webui.go`) - it owns the Traefik label wiring and stamps the config hash; only port/alias/env/volumes vary per service
2. Add `Start<Service>` and `<Service>Status` methods to `manager.go` (no per-service stop/connect/disconnect methods - the registry closures call the generic helpers)
3. Add an entry to `AllSharedServices()` in `internal/services/registry.go` using `stopClosure`/`connectClosure`/`disconnectClosure` - **pass the network alias to `connectClosure`**, otherwise the service won't be resolvable by its short name from project containers (e.g., `mail` instead of `zdev_mail`). This single registry drives CLI start/stop/status/recreate and per-project connect/disconnect
4. Add the image constant to `internal/config/defaults.go`
5. Add the opt-in flag to `ProjectSharedConfig` in `internal/config/config.go` and reference it in the registry entry's `ProjectEnabled` closure
6. Update `cmd/info.go` and `cmd/status.go` to display the service status

**Not every shared container fits the default shape.** The registry holds every shared container, including ones that don't attach to project networks or serve a web UI - the DNS fallback (`internal/services/dns.go`) is the current example: the host queries its published loopback ports directly, and it only exists on hosts where the split-DNS resolver file is installed. Three predicates on `SharedServiceDef` express that, and every consumer must branch on them rather than assume:

- `HasWebUI()` - false when `Subdomain` is empty; skip URL printing.
- `JoinsProjectNetworks()` - false when `Connect`/`Disconnect` are nil; never call them unguarded.
- `IsEnabled(mgr)` - false when a host-level `Enabled` gate says the service doesn't exist here; `services start`, `status`, and `recreate` skip it. Stopping goes through `stopSharedService` (`cmd/services.go`), which skips a gated-off service *unless its container is running* - a leftover from a since-disabled service still stops, without printing "not running" for everyone else.

Keeping such a service in the registry is what makes `zdev services start/stop/status/recreate` and `zdev status` cover it - when the DNS fallback is enabled it is load-bearing infrastructure, exactly like the router, not an optional extra. See the CLAUDE.md architecture anchor for the full reasoning.

## Link Networks

Link networks enable cross-project container communication. The implementation spans:

- **`internal/state/state.go`** - `LinkEntry`, `LinkMember` types and CRUD methods (`CreateLink`, `DeleteLink`, `AddLinkMembers`, `RemoveLinkMembers`, `GetLinksForProject`)
- **`internal/project/links.go`** - `connectLinks()` and `disconnectLinks()` called during Start/Down lifecycle
- **`cmd/link.go`** - All subcommands (create, delete, join, leave, ls, status)

Links are stored in the global state file (`~/.zdev/state.yaml`), not in project config. They are a runtime relationship between projects, not a property of any single project. Each link creates a dedicated Docker network (`zdev_link_<name>`) so different link groups stay isolated from each other.

All global state mutations must go through `state.Manager.Mutate`. It holds both an in-process mutex and a cross-process file lock for the complete read-modify-write cycle, then replaces `state.yaml` atomically. Do not add direct state-file writes or split a mutation into separate `Load` and save operations.

On `zdev start`, a project checks if it's a member of any links and auto-connects. On `zdev down`, it disconnects. Container DNS resolution happens automatically via Docker's embedded DNS - no explicit network aliases are needed since containers are already named with the `<service>.<project>.zdev` pattern.

## Project Rename

`zdev rename` changes the project name and migrates all Docker resources. The implementation lives in:

- **`internal/project/rename.go`** - Core `Rename()` method and `updateConfigName()` helper
- **`internal/state/state.go`** - `RenameProject()` atomically updates the project entry and all link memberships
- **`cmd/rename.go`** - CLI command with validation and confirmation

The rename process: stop containers, migrate volumes (create new + copy data + remove old), remove old network, update state, delete the old name's per-project certificate, rewrite `name:` in config.yaml (preserving formatting), reload and start. Docker has no native volume rename, so `CopyVolume()` on the Runtime interface handles the data migration - it runs a temp container using one of the project's own service images (`firstServiceImage()`, guaranteed present locally), not a separately-pulled `alpine`.

## Key Architecture Decisions

### Why shell out to Docker CLI (not the SDK)?

Enables future Podman support. The `Runtime` interface (`internal/runtime/runtime.go`) abstracts all container operations. The `DockerCLI` implementation shells out to `docker`. Swapping to Podman means implementing the same interface with `podman` commands.

A side benefit: because zdev only ever uses the `docker` CLI and the active docker context, it is engine-agnostic across Docker Desktop, OrbStack, and Colima with no per-engine code. The one place that needs care is the Docker socket bind-mount for the Traefik router and Dozzle: never hardcode `/var/run/docker.sock` as the host source (that symlink isn't guaranteed on OrbStack-without-admin or Colima). Use `runtime.HostDockerSocketPath()`, which resolves the active context's real socket and falls back to the conventional path. See `ALTERNATIVE_BACKENDS.md` for the compatibility analysis and a cross-engine filesystem benchmark.

### Why `zdev run` with TOML job files (and not just)?

Project commands live in `.zdev/run/<cmd>.toml` and run as `zdev run <cmd> [job] [args]`. They replaced `.zdev/commands/*.just` (deprecated, removal planned; `zdev run --convert-just` converts). Why:

- **Own namespace.** Justfiles ran as `zdev <name>` and built-ins took precedence, so every new built-in could silently shadow a project's command. `zdev run <cmd>` never resolves to a built-in.
- **No extra language or binary.** The script is plain shell (or any shebang); TOML only carries metadata (`service`, `needs`, `output`, ...) and keeps script bodies verbatim in `''''` strings. No third-party download, no argument quirks (`cache:clear` passes through as is).
- **Not in `.zdev/config.yaml`:** a new config key breaks every released binary (`KnownFields(true)`). Job files are read only by new code and treat unknown keys as warnings, so they stay forward-compatible.
- **More than just:** jobs can run inside a service container, run quietly, ask for confirmation, and list themselves as JSON for agents.

Code: `internal/run/` (loader `load.go`, resolution `resolve.go`, dependency plan `plan.go`, execution `exec.go`, quiet output `output.go`) and `cmd/run.go`. Container jobs go through `Runtime.ExecStream`. Host jobs are tested against real bash, container jobs against the mock runtime.

### Why auto-discover named volumes?

Unlike Docker Compose, zdev does not require a top-level `volumes:` section. Named volumes are detected automatically from service `volumes:` entries - if the source doesn't start with `/`, `.`, or `${`, it's a named volume. This reduces config boilerplate and eliminates a common source of "volume not declared" errors.

### Why variables AND environment?

`variables:` defines `${VAR}` placeholders substituted throughout the config file (e.g., sharing a DB password between app and db services). They are NOT passed to containers. `environment:` defines actual env vars passed to containers. This separation prevents config-internal values from leaking into the container runtime.

### Why Mutagen for file sync?

Docker bind mounts on macOS are slow - `pnpm install` takes 5x longer than native. Mutagen syncs files bidirectionally at native speed. It's auto-enabled on macOS and disabled on Linux (where bind mounts are already fast). The `mutagen.ignore` list keeps dependency dirs (`node_modules`, `vendor`) inside the container.

### Why does `zdev claude` run the agent in its own container?

The agent needs the project's toolchain, so the container is built from a service's image (through `buildContainerConfig`, so it never drifts from the service), but it is not the service's container: `zdev update` or `restart` would kill Claude mid-turn, and hardening (no capabilities, the host fence) would hit the app. The host side is the `zdev claude` process itself, no daemon: it serves the relay (the SSH protocol over a `docker exec` pipe) and checks every request. `PLAN_2_AGENT_SANDBOX.md` has the design and the alternatives that were rejected. The pure parts (request policy, fence rules, system prompt, volume cleanup, the config policy) have unit tests; the Docker parts are tested by hand against a project.

The agent may change `.zdev/` and apply it, so applying is checked (`internal/project/agentpolicy.go`): what stays inside containers is free, what reaches outside them must already be in effect, as recorded after the developer's own start or update. When you add a project config field, `TestAgentPolicyClassifiesFields` fails until you classify it in `agentFieldClass`; a field that gives a container access to the host (a mount, a port, a capability, a route) needs a rule in `CheckAgentPolicy` first.

### Why the Runtime interface uses a mock for tests?

`internal/runtime/mock.go` implements the `Runtime` interface with in-memory state. This lets unit tests verify project lifecycle logic (start, stop, down, volume discovery, label generation) without Docker. Integration tests use the real `DockerCLI` implementation.

## Config System

Two-pass variable substitution in `internal/config/loader.go`:
1. First pass: substitute built-in variables (`${PROJECTDIR}`, `${PROJECTPATH}`, `${ZDEV_DOMAIN}`, etc.) to resolve the `name:` field
2. Second pass: add `${PROJECTNAME}` + user-defined `variables:` to the map, re-substitute everything

User variables can reference built-in vars (e.g., `DB_NAME: ${PROJECTNAME}_db`) but not other user variables (map iteration order is undefined in Go).

`buildContainerConfig()` in `project.go` is the single source of truth for container configuration. Both `startServiceWithMutagen()` (creating containers) and `serviceNeedsRecreate()` (comparing against running containers) use it. It stamps a `zdev.config-hash` label (deterministic sha256 of image, env, volumes, command, working dir, routing labels, ports, aliases, and network). `zdev update` recreates any service whose stamped hash differs from the freshly built one. Pre-hash containers have no label and get recreated once on first update after upgrading.

### Dockerfile builds (`services.<name>.dockerfile`)

A service with `dockerfile:` gets its image built locally instead of pulled (`internal/project/build.go`). The build context is always the project root; the tag is `image:` if set, else `zdev-<project>-<service>:latest`. Staleness is a sha256 of the Dockerfile contents stamped on the image as a `zdev.build-hash` label - deliberately NOT a hash of the build context (source is bind-mounted live; Docker's layer cache handles COPY'd files on forced rebuilds). Because the tag doesn't change on rebuild, the container config hash can't see Dockerfile edits - `Update()` calls `serviceBuildStale()` separately and folds the result into the recreate decision. `Project.BuildMode` (set from `--build`/`--no-build` on `start` and `update`) forces or skips the build. Kept intentionally minimal: no build args, targets, custom contexts, or secrets - those use a custom command plus `image:`.

### 1Password secrets (`op-env`)

`internal/secrets/` defines the `Resolver` interface (`ReadEnvironment(ctx, envID)` returns a whole Environment as a map), the `OnePasswordCLI` implementation, and a `Mock` for unit tests (same pattern as `runtime.MockRuntime`). Secrets come from 1Password Environments in two config forms: `services.<name>.op-env: <environment-id>` injects every variable of the Environment into the container env (explicit `environment:` entries win), and `op-env://<environment-id>/<VARIABLE>` env values inject single variables - references are self-contained, so services can mix Environments. One `op environment read <id>` call (beta CLI only) fetches a whole Environment - parsed as `KEY=VALUE` lines (split on first `=`, duplicate keys last-wins, multiline values unsupported by the format) and cached per process per ID, so N services on one Environment still cost one op call. `Project.Secrets` holds the resolver like `Project.Runtime` holds the runtime. Resolution happens in `startServiceWithMutagen` AFTER `buildContainerConfig` stamps the config hash and before `CreateContainer` - so the hash covers the unresolved references (and the attached ID via the `zdev.op-env` label) and the `serviceNeedsRecreate` compare path never contacts 1Password; on the Update recreate path `prefetchSecrets` resolves before the old container is removed so failures leave it intact. Change detection is a separate staleness check like `serviceBuildStale`: creation stamps a `zdev.secrets-hash` label (sha256 over the injected key/value set, excluded from the config hash), and `Update()` with `Project.RefreshSecrets` resolves fresh values and recreates on mismatch - covering rotated values and variables added/removed in an attached Environment. Interactive flows (brew install offer for `1password-cli@beta`, `op signin` recovery with a single retry) live behind injected function fields on `OnePasswordCLI` so they unit-test without op; they are gated on stdin being a TTY so CI never prompts.

### Per-project certificates

The main mkcert cert covers `*.<domain>`, `*.shared.<domain>` and `<domain>`. A TLS wildcard matches exactly one label, so routed hosts nested deeper (`api.myproj.<domain>`) or outside the domain get one extra cert per project: `~/.zdev/certs/projects/<name>.pem` / `<name>.key` plus `~/.zdev/traefik/project-<name>.yaml`, which Traefik's watched file provider loads live (selected by SNI, no router restart, outside the config hash so no container recreate). Logic lives in `internal/ssl/projectcerts.go`; the project side is `ensureProjectCert` (`internal/project/certs.go`).

Ownership is split on purpose. The **project lifecycle** issues the cert, because only project config knows the hostnames: `ensureProjectCert` runs in `registerAndConnect` (every start and update, after registration), and every unregister path (`Down`, `zdev remove`, `zdev cleanup`, `Rename`) calls `ssl.RemoveProjectCert`. The **router** (`refreshProjectCerts` in `StartRouter`) needs no project config: it deletes certs of projects not in state and re-signs the main and per-project certs from their existing SANs when the mkcert CA changes. Hosts come from `routedHosts()`, built on the same helpers as the Traefik labels, so certs and routes can't diverge.

Testing: `ssl.Signer` is the mkcert seam (`NewCertManagerWithSigner`). `internal/ssl` tests use an in-process CA that signs real certs; `internal/project` and `internal/services` each expose a package-level `newCertManager` to swap. The unit tests never need the mkcert binary.

## Documentation

These docs must stay in sync with the code:

| File | Purpose | Update when |
|------|---------|-------------|
| `README.md` | Lean entry page: intro, quick start, how it works, links | Rarely - only when the big picture changes |
| `docs/*.md` | User docs, one topic per file (CLI, configuration, project commands, templates, ...) | New commands, config options, features |
| `skills/zdev/` | Agent skill: terse rules and gotchas, deep dives in `references/` | Every user-visible change (same trigger as `docs/`) |
| `CLAUDE.md` | Agent guidance for working on zdev (decisions, gotchas) | Architecture changes, new conventions, non-obvious patterns |

Every user-visible change needs a check of both `docs/` and `skills/zdev/`. Within the human docs, keep one home per topic and link instead of repeating. CLAUDE.md has decisions and gotchas only.

## Creating Templates

Templates enable `zdev create <template> <name>` for one-command project scaffolding. See the [template guide](docs/templates.md) for the full reference.

Key patterns:
- **Create-time scaffold hook** (`.zdev/scaffold.sh`, preferred) - `zdev create` runs it once in a throwaway container (entrypoint overridden to a shell) to generate the project, then auto-disables it. Keeps scaffolding machinery out of the created project (no `.setup-complete`, no re-scaffolding setup command). Wired in `cmd/create.go` + `internal/project/scaffold.go`.
- **`.setup-complete` marker** (setup-command scaffolding) solves the container startup vs setup circular dependency
- **Scaffold in-place** (`--force`) for frameworks that support non-empty dirs (Nuxt)
- **Scaffold in /tmp** for frameworks that require empty dirs (Symfony) - safe for PHP, not for Node.js
- **`.zdev/run/setup.toml`** (`zdev run setup`, also run by `zdev create --auto-setup`) runs on host with interactive terminal - framework prompts work here but not in the container entrypoint

## Style

- Never use em-dashes (-). Use regular hyphens (-) everywhere.
- Error messages: `fmt.Errorf("failed to <action>: %w", err)` - wrap with context
- User-facing output: `fmt.Printf` for status, `fmt.Println` for section headers
- Top-level progress markers during multi-step flows (setup, start, sync): use `ui.StatusStep(msg, plainMode)` instead of plain `fmt.Println`. Adds two blank lines + cyan `▶` + bold text so framework messages stand out from verbose nested command output. Job scripts get the same styling from the `step "<msg>"` helper (`zdev step <msg>` for legacy justfiles, removed with just).
- Commands return errors (Cobra handles display with `SilenceUsage: true`)

## Release Process

1. Update `CHANGELOG.md` - add new `## vX.Y.Z` section at top
2. Run `zdev run test all` and `zdev run test integration`
3. Commit, tag, push:
   ```bash
   git add -A && git commit -m "Release vX.Y.Z"
   git tag vX.Y.Z && git push origin main && git push origin vX.Y.Z
   ```
4. CI builds binaries for darwin/linux (arm64/amd64) and creates a GitHub Release with changelog
5. Users update via `zdev self-update`
