# Contributing to zdev

## Getting Started

```bash
git clone https://github.com/0ploy/zdev.git
cd zdev
make build    # Build binary
make test     # Run unit tests
```

Requirements: Go 1.25+, Docker Desktop (for integration tests)

## Project Structure

```
cmd/                     # Cobra commands (one file per command)
internal/
  config/                # Config parsing, defaults, variable substitution
  create/                # Template resolution, download, copy (zdev create)
  project/               # Project lifecycle (start, stop, down, exec, routing)
  runtime/               # Docker abstraction (interface + DockerCLI implementation)
  services/              # Shared infrastructure (router, mail, adminer, redis)
  mutagen/               # Mutagen binary wrapper
  state/                 # Global project registry (~/.zdev/state.yaml)
  tools/                 # External tool management (mkcert, just, mutagen)
  firstrun/              # First-time setup (certs, shared network)
  ssl/                   # Certificate management
  ui/                    # Terminal output (colors, hyperlinks, markdown)
skills/zdev/            # AI agent skill (SKILL.md + references/)
templates/               # Template authoring guide
testdata/projects/       # Test fixtures (minimal, full, variables, mutagen)
```

## Testing

### Unit tests (`make test`)

Run on every change before committing. Fast (seconds), no external dependencies.

```bash
make test                           # All unit tests
go test ./internal/create/ -v       # Single package
go test ./... -run TestValidateName # Single test
```

Unit tests use the mock runtime (`internal/runtime/mock.go`) for Docker operations and test fixtures in `testdata/projects/`.

### Integration tests (`make test-integration`)

Run before releases, after refactoring core logic (project lifecycle, routing, Mutagen), or when changing code that unit tests can't cover. Requires Docker - spins up real containers.

```bash
make test-integration                                      # All integration tests
go test -v -tags=integration -count=1 ./internal/project/  # Single package
```

Integration tests are tagged with `//go:build integration` so they don't run during `make test`. They create real Docker containers, networks, and volumes, and clean up after themselves (including state registry).

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

1. Create `internal/services/<service>.go` with container name constant and config function
2. Add `Start<Service>`, `Stop<Service>`, `<Service>Status`, `Connect<Service>ToProject`, `Disconnect<Service>FromProject` methods to `manager.go`
3. **Pass network aliases in `Connect<Service>ToProject`** - without aliases, the service won't be resolvable by its short name from project containers (e.g., `mail` instead of `zdev_mail`)
4. Add entry to `sharedServiceRegistry()` in `cmd/services.go` (handles start/stop/status/recreate)
5. Add connect/disconnect methods to `internal/project/shared_services.go` using `connectSharedService` helper
6. Add image constant to `internal/config/defaults.go`
7. Add to `ProjectSharedConfig` in `internal/config/config.go`
8. Update `cmd/info.go` to display the service status

## Link Networks

Link networks enable cross-project container communication. The implementation spans:

- **`internal/state/state.go`** - `LinkEntry`, `LinkMember` types and CRUD methods (`CreateLink`, `DeleteLink`, `AddLinkMembers`, `RemoveLinkMembers`, `GetLinksForProject`)
- **`internal/project/links.go`** - `connectLinks()` and `disconnectLinks()` called during Start/Down lifecycle
- **`cmd/link.go`** - All subcommands (create, delete, join, leave, ls, status)

Links are stored in the global state file (`~/.zdev/state.yaml`), not in project config. They are a runtime relationship between projects, not a property of any single project. Each link creates a dedicated Docker network (`zdev_link_<name>`) so different link groups stay isolated from each other.

On `zdev start`, a project checks if it's a member of any links and auto-connects. On `zdev down`, it disconnects. Container DNS resolution happens automatically via Docker's embedded DNS - no explicit network aliases are needed since containers are already named with the `<service>.<project>.zdev` pattern.

## Project Rename

`zdev rename` changes the project name and migrates all Docker resources. The implementation lives in:

- **`internal/project/rename.go`** - Core `Rename()` method and `updateConfigName()` helper
- **`internal/state/state.go`** - `RenameProject()` atomically updates the project entry and all link memberships
- **`cmd/rename.go`** - CLI command with validation and confirmation

The rename process: stop containers, migrate volumes (create new + copy data via temp alpine container + remove old), remove old network, update state, rewrite `name:` in config.yaml (preserving formatting), reload and start. Docker has no native volume rename, so `CopyVolume()` on the Runtime interface handles the data migration.

## Key Architecture Decisions

### Why shell out to Docker CLI (not the SDK)?

Enables future Podman support. The `Runtime` interface (`internal/runtime/runtime.go`) abstracts all container operations. The `DockerCLI` implementation shells out to `docker`. Swapping to Podman means implementing the same interface with `podman` commands.

A side benefit: because zdev only ever uses the `docker` CLI and the active docker context, it is engine-agnostic across Docker Desktop, OrbStack, and Colima with no per-engine code. The one place that needs care is the Docker socket bind-mount for the Traefik router and Dozzle: never hardcode `/var/run/docker.sock` as the host source (that symlink isn't guaranteed on OrbStack-without-admin or Colima). Use `runtime.HostDockerSocketPath()`, which resolves the active context's real socket and falls back to the conventional path. See `ALTERNATIVE_BACKENDS.md` for the compatibility analysis and a cross-engine filesystem benchmark.

### Why justfiles for custom commands?

Each `.zdev/commands/<name>.just` file becomes a `zdev <name>` command. We chose [just](https://github.com/casey/just) over Makefiles because:
- No build system baggage (no implicit rules, no tab sensitivity issues)
- Multiple recipes per file (e.g., `zdev test` runs default, `zdev test watch` runs the `watch` recipe)
- Justfiles are discoverable - agents and developers can `ls .zdev/commands/` to see all available commands
- Just supports arguments, dependencies, and conditional logic

Commands run on the **host**, not in containers. Use `zdev exec app <cmd>` inside justfiles to run commands in containers.

### Why auto-discover named volumes?

Unlike Docker Compose, zdev does not require a top-level `volumes:` section. Named volumes are detected automatically from service `volumes:` entries - if the source doesn't start with `/`, `.`, or `${`, it's a named volume. This reduces config boilerplate and eliminates a common source of "volume not declared" errors.

### Why variables AND environment?

`variables:` defines `${VAR}` placeholders substituted throughout the config file (e.g., sharing a DB password between app and db services). They are NOT passed to containers. `environment:` defines actual env vars passed to containers. This separation prevents config-internal values from leaking into the container runtime.

### Why Mutagen for file sync?

Docker bind mounts on macOS are slow - `pnpm install` takes 5x longer than native. Mutagen syncs files bidirectionally at native speed. It's auto-enabled on macOS and disabled on Linux (where bind mounts are already fast). The `mutagen.ignore` list keeps dependency dirs (`node_modules`, `vendor`) inside the container.

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

## Documentation

Three docs must stay in sync with the code:

| File | Purpose | Update when |
|------|---------|-------------|
| `README.md` | User-facing docs and marketing | New commands, config options, features |
| `templates/README.md` | Template authoring guide | Config changes, scaffolding patterns, new framework notes |
| `CLAUDE.md` | Agent guidance (decisions, gotchas) | Architecture changes, new conventions, non-obvious patterns |

Don't duplicate information across docs. README has config reference and examples. `templates/README.md` has template-specific patterns. CLAUDE.md has decisions and gotchas only.

## Creating Templates

Templates enable `zdev create <template> <name>` for one-command project scaffolding. See the [Template Authoring Guide](templates/README.md) for the full reference.

Key patterns:
- **`.setup-complete` marker** solves the container startup vs setup circular dependency
- **Scaffold in-place** (`--force`) for frameworks that support non-empty dirs (Nuxt)
- **Scaffold in /tmp** for frameworks that require empty dirs (Symfony) - safe for PHP, not for Node.js
- **`setup.just`** runs on host with interactive terminal - framework prompts work here but not in the container entrypoint

## Style

- Never use em-dashes (-). Use regular hyphens (-) everywhere.
- Error messages: `fmt.Errorf("failed to <action>: %w", err)` - wrap with context
- User-facing output: `fmt.Printf` for status, `fmt.Println` for section headers
- Top-level progress markers during multi-step flows (setup, start, sync): use `ui.StatusStep(msg, plainMode)` instead of plain `fmt.Println`. Adds two blank lines + cyan `▶` + bold text so framework messages stand out from verbose nested command output. Same styling is exposed as `zdev step <msg>` for template justfiles.
- Commands return errors (Cobra handles display with `SilenceUsage: true`)

## Release Process

1. Update `CHANGELOG.md` - add new `## vX.Y.Z` section at top
2. Run `make test` and `make test-integration`
3. Commit, tag, push:
   ```bash
   git add -A && git commit -m "Release vX.Y.Z"
   git tag vX.Y.Z && git push origin main && git push origin vX.Y.Z
   ```
4. CI builds binaries for darwin/linux (arm64/amd64) and creates a GitHub Release with changelog
5. Users update via `zdev self-update`
