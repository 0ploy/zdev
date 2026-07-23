# Creating zdev Templates

Templates let users scaffold new projects with `zdev create`. A template is a GitHub repo
(or local directory) containing `.zdev/` configuration and optionally starter files.

## Template Structure

```
my-template/
  .zdev/
    config.yaml              # Container config, routing, volumes, mutagen
    Dockerfile               # Optional: custom dev image, referenced via dockerfile:
    commands/
      setup.just             # Setup script
  README.md
```

**Static templates** (e.g. Express) also include source files (`app.js`, `package.json`, `.gitignore`).
**Scaffold templates** (e.g. Nuxt, Symfony) ship only `.zdev/` - the framework's create command generates the app files.

There are two ways a scaffold template can generate those files: a **create-time scaffold hook** (`.zdev/scaffold.sh`, preferred - keeps scaffolding out of the created project), or scaffolding inside **`setup.just`** (the older approach, with a `.setup-complete` gate). New templates should prefer the hook.

## Create-time scaffold hook (`.zdev/scaffold.sh`) - preferred (zdev >= v0.11.0)

Ship a `.zdev/scaffold.sh`. `zdev create` runs it **once**, right after copying the template, inside a throwaway container built from your `.zdev/Dockerfile` with the entrypoint overridden to a plain shell (`docker run --entrypoint sh`). The image's normal init (zpinit, a PHP entrypoint) is bypassed, so the hook only generates the project. The project dir is bind-mounted at `/app`, so files land on the host synchronously (no Mutagen session).

```sh
# .zdev/scaffold.sh - runs inside the throwaway container (cwd /app)
#!/bin/sh
set -eu
# `zdev create` attaches a TTY when run from a terminal, so the hook can prompt
# interactively (create-nuxt's template/module picker). Guard with `[ -t 0 ]`
# and provide a non-interactive fallback for CI/piped runs (where nuxi needs an
# explicit --template).
if [ -t 0 ]; then
  pnpm dlx nuxi@latest init . --no-install --packageManager pnpm --gitInit=false --force
else
  pnpm dlx nuxi@latest init . --template minimal --no-install --packageManager pnpm --gitInit=false --force
fi
```

- **Scaffold source only; install at boot.** Use `--no-install`; install deps in the boot path (a zpinit `entrypoint.d/` step, or the container `command:`) so a clone just runs `zdev start` and pulled dependency changes are picked up. Install-at-scaffold-time only populates the throwaway bind mount, not the persistent volume.
- **Interactive when a terminal is present.** zdev attaches a TTY to the hook when `zdev create` runs from a terminal, so a scaffolder can prompt (framework/module pickers). Always keep a non-interactive fallback (`[ -t 0 ]`) so `zdev create` in CI/piped contexts doesn't block on a prompt it can't show.
- **No gate, no `.setup-complete`.** Because scaffolding happens at create time, the steady-state image needs no wait loop and the created project carries no re-scaffolding `setup.just`.
- **zdev auto-disables the hook** after a successful run (renames `scaffold.sh` -> `scaffold.sh.disabled`, never deletes it). A `.disabled` hook is skipped, so reusing the project as a template won't re-scaffold. Safe even if the user never reads the create output.
- The scaffold runs in the sole service, or the one named `app`. It requires Docker (build/pull + run).

The reference implementation is `0ploy/zdev-template-nuxt4`. The rest of this file covers the older `setup.just` scaffolding approach.

## The .setup-complete Pattern (setup.just scaffolding)

Scaffolding inside `setup.just` must solve a circular dependency: the container needs to be running for `zdev exec`
(used by setup), but the app can't start until setup completes.

**In config.yaml command:**

```yaml
command: >-
  sh -c "corepack enable &&
  if [ ! -f .setup-complete ]; then
    echo 'Waiting for setup... Run: zdev setup';
    while [ ! -f .setup-complete ]; do sleep 2; done;
  fi;
  pnpm install && exec pnpm dev"
```

- No marker: container enters wait loop (stays alive for `zdev exec`)
- Setup creates marker: loop exits, app starts
- On restart: marker exists, skips loop, runs dep install + app

The dep install (`pnpm install`, `composer install`) MUST be in the entrypoint so restart
picks up new packages without re-running setup.

**In setup.just** - `touch .setup-complete` goes last, only after everything succeeds.

**In mutagen.ignore** - `.setup-complete` MUST be ignored so it persists in the container
volume independently of the host.

## Writing setup.just

Setup runs on the **host**, uses `zdev exec` for container commands. The interactive terminal
from `zdev exec` is important - framework prompts work here but would crash in the entrypoint
(which has no TTY).

Setup also produces a wall of output (apk/composer/npm installing hundreds of deps, compilers,
scaffolders). Plain `@echo` status lines disappear in that noise. `@zdev step "<msg>"` prints two
leading blank lines + a cyan `▶` + bold text so each phase reads as a clear section header. Styling
auto-strips when stdout isn't a TTY, when `NO_COLOR` is set, or when global plain mode is on, so
the same recipe works in logs and CI.

```just
# Description

[no-exit-message]
default:
    zdev start -q
    @zdev step "Installing dependencies"
    zdev exec app sh -c "corepack enable && pnpm install && touch .setup-complete"
    @zdev step "Setup complete! App will start automatically."
    zdev info
```

Conventions:
- `zdev start -q` first (quiet: skips info display since setup shows it at the end)
- `touch .setup-complete` last in exec (only on success)
- `@zdev step "<msg>"` for each top-level phase; reserve `@echo` for sub-detail lines that don't need to stand out
- Keep echo ON for `zdev start`, `zdev exec`, `zdev info` so the user sees what's running
- `[no-exit-message]` suppresses just's exit message
- Break long chains into separate `zdev exec` calls with a `@zdev step` marker between them

## Forwarding colon-namespaced CLIs

For wrappers like `bin/console cache:clear` or `artisan migrate:fresh`, declare a recipe
named after the file. zdev auto-prepends it so colon args pass through as recipe params
instead of being parsed as just's module path:

```just
# .zdev/commands/console.just
console *args:
    zdev exec app php bin/console {{args}}
```

`zdev console cache:clear` -> `bin/console cache:clear`. Without a filename-matching
recipe, the first arg is still treated as the recipe name (legacy behavior).

## Handling Framework Scaffolding

When the framework has a create command that expects an empty directory:

### Scaffold in-place (when tool supports --force)

On macOS with Mutagen, `.zdev` is ignored so the container sees an empty `/app`. On Linux,
`.zdev/` is visible but scaffolding tools just add their own files alongside it.

Example - Nuxt (current `nuxi init` needs an explicit `--template` in a non-TTY):

```just
zdev exec app pnpm dlx nuxi@latest init . --template minimal --packageManager pnpm --gitInit=false --force
zdev exec app npx nuxi prepare           # triggers module dep prompts interactively
zdev exec app pnpm approve-builds --all  # approves native module build scripts (esbuild etc.)
zdev exec app sh -c "echo '.setup-complete' >> .gitignore && touch .setup-complete"
```

`npx nuxi prepare` is critical - it runs Nuxt module initialization which may prompt for
missing dependencies (e.g. `better-sqlite3` for `@nuxt/content`). Without it, those prompts
fire in the entrypoint where there's no TTY, crashing the container. `pnpm approve-builds --all`
is the interactive equivalent of the boot-path `pnpm install --config.dangerouslyAllowAllBuilds=true`
(see `stack-gotchas.md`) - pnpm blocks native build scripts by default and errors in a non-TTY.

### Scaffold in /tmp (when tool requires empty directory)

Copy files back after scaffolding. Safe for PHP (Composer uses `__DIR__` relative paths) but
NOT for Node.js/pnpm (symlink-based store with absolute paths).

Example - Symfony:

```just
zdev exec app symfony new /tmp/app --no-git
zdev exec app sh -c "cp -r /tmp/app/. /app/ && rm -rf /tmp/app"
zdev exec app sh -c "echo '.setup-complete' >> .gitignore && touch .setup-complete"
```

### When to use which

| Approach | Use when | Examples |
|----------|----------|---------|
| In-place with `--force` | Tool supports non-empty dirs | Nuxt (`nuxi init . --force`) |
| /tmp + copy | Tool requires empty dir AND deps are portable | Symfony, Laravel |
| No scaffolding | Template includes all source files | Express, static sites |

## Stack-specific runtime gotchas

Language/framework behaviors (Node corepack, pnpm build scripts, PHP extensions,
`SYMFONY_TRUSTED_PROXIES`, Webpack Encore asset pipelines, `memory_limit`, `MAILER_DSN`) are **not
template-authoring-specific** - they apply to any zdev-managed container. See
`stack-gotchas.md` for the full list. System-level deps (PHP extensions, apk/apt packages) belong
in a `.zdev/Dockerfile` referenced via `dockerfile:` (zdev builds it automatically); runtime env
and per-project setup go in the entrypoint and/or `setup.just`. The same split applies when
adding zdev to an existing project.

## Template Naming and Testing

Name repos `zdev-template-<name>`. The `0ploy` org has shorthand:
`zdev create express` -> `0ploy/zdev-template-express`

Test locally: `zdev create ./my-template test-app && cd test-app && zdev start`
(a `scaffold.sh` runs during `zdev create`; older `setup.just` templates need `zdev setup` instead).

Verify: create scaffolds, `zdev start` serves the URL, `zdev restart` works, file changes reflected.

**Iterating gotcha:** editing a file the Dockerfile `COPY`s into the image (e.g. `.zdev/zpinit/entrypoint.d/*`, baked config) does NOT trigger a rebuild on the next `zdev start` - build staleness hashes the Dockerfile *contents* only, not the build context. Force it with `zdev start --build` (or `zdev update --build`). A fresh `zdev create` builds from scratch, so end users creating a new project never hit this; it bites template authors iterating locally. See `stack-gotchas.md`.
