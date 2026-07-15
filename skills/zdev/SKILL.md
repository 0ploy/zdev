---
name: zdev
description: |
  Local development environment framework using Docker. Use this skill whenever the user's project
  has a `.zdev/` directory, when they ask to set up a local dev environment, start/stop containers,
  run commands inside containers, check logs, configure routing, or debug container issues.
  Also trigger when: the user says "start the project", "run tests in docker", "check the logs",
  "open mailpit", "set up zdev", "add zdev to this project", mentions `.zdev/config.yaml`,
  or asks about shared services, HTTPS certificates, or file sync on macOS.
  Also trigger when the user wants to create an zdev template, scaffold a new project template,
  build a starter template for a framework, or asks about `zdev create`, template authoring,
  setup.just files, or the .setup-complete pattern.
  Also trigger for secrets in the dev environment: 1Password integration, `op-env://` references,
  the `op-env:` service field, moving API keys or passwords out of committed config or `.env`
  files, rotating secrets, or "how do I share dev secrets with my team".
---

# zdev - Local Development Environment

zdev manages Docker-based local dev environments. One command starts an entire project with HTTPS,
routing, shared services (mail, DB browser, Redis browser), and container isolation.

**How it works:** Each project has `.zdev/config.yaml` defining its services. `zdev start` creates
an isolated Docker network, starts containers, and connects shared services. Traefik routes HTTPS
to `https://{name}.0ploy.dev` (wildcard DNS to 127.0.0.1). On macOS, Mutagen provides
fast file sync.

## Prerequisites

```bash
zdev version  # Check if installed
# If not: curl -fsSL https://raw.githubusercontent.com/0ploy/zdev/main/install.sh | sh && zdev systemcheck
```

## CLI Reference

```bash
# Project lifecycle
zdev create <template> [name]   # Create from template (GitHub repo or local dir)
zdev start                      # Start project
zdev start <service>            # Start one service (project setup runs idempotently)
zdev start -q                   # Start quietly (no info display, for scripts)
zdev stop                       # Stop all containers
zdev stop <service>             # Stop one service container
zdev restart                    # Stop + start every service
zdev restart <service>          # Bounce one service container in-place
zdev update                     # Apply config.yaml + Dockerfile changes (recreates only drifted services)
zdev start/update --build       # Force rebuilding dockerfile: images
zdev start/update --no-build    # Never build; fail if a dockerfile: image is missing
zdev down                       # Remove containers and network
zdev down -v                    # Remove everything including volumes

# Run commands in containers
zdev exec <service> <command>   # e.g. zdev exec app pnpm test
zdev exec app bash              # Interactive shell

# Print a visually distinct progress marker (for use inside .zdev/commands/*.just)
zdev step "Installing dependencies"   # 2 blank lines + cyan ▶ + bold text;
                                       # makes phase headers pop against composer/npm/apk
                                       # output. Auto-plain on non-TTY / NO_COLOR.

# Logs and info
zdev logs [service]             # First service if omitted. -f to follow, --tail N to limit
zdev info / status / config     # Project info, status, resolved config
zdev open [project]             # Open project URL in browser (current project, or a registered one by name)
zdev rename <new-name>          # Migrate containers/volumes/network/link memberships to a new name

# Shared services
zdev mail / db / redis          # Open in browser
zdev services status            # Check shared services
zdev services recreate          # Rebuild shared containers

# Local DNS fallback (only if the router blocks the wildcard - see Debugging)
zdev dns enable / disable / status

# Cross-project networks (links) — see "Linking projects" below
zdev link create <name>         # Make a shared Docker network
zdev link join <name> <proj>[.<svc>] ...   # Attach a whole project or one service
zdev link leave <name> <member> ...
zdev link ls / status <name>    # Inspect
zdev link delete <name>         # Remove network + disconnect members

# Templates
zdev create express my-app              # 0ploy/zdev-template-express
zdev create nuxt4 my-app               # 0ploy/zdev-template-nuxt4
zdev create myorg/my-template my-app    # Any GitHub repo
zdev create ./local-dir my-app          # Local directory
zdev create express my-app --branch dev # Specific branch/tag
```

After `zdev create`: `cd my-app && zdev setup`

## Project Configuration

### Minimal config

```yaml
name: my-app

services:
  app:
    image: node:22-alpine
    command: corepack enable && pnpm install && pnpm dev --host 0.0.0.0
    working_dir: /app
    volumes:
      - ${PROJECTPATH}:/app
    routing:
      port: 3000

mutagen:
  ignore:
    - node_modules
    - .pnpm-store
```

Result: `https://my-app.0ploy.dev` with HTTPS, isolation, shared services.

### Shared services: hostnames and access

Shared services have two access modes: **web UI** (browser) and **container-internal** (from app code).

| Service | Web UI URL | Container hostname | Container port | Purpose |
|---------|-----------|-------------------|----------------|---------|
| Mailpit | `mail.shared.0ploy.dev` | `mail` | `1025` (SMTP) | Email catching |
| Adminer | `db.shared.0ploy.dev` | `adminer` | `8080` (HTTP) | Database browser |
| Redis Insights | `redis.shared.0ploy.dev` | `redis-insights` | `5540` (HTTP) | Redis browser |
| Traefik | `router.shared.0ploy.dev` | `router` | - | Routing dashboard |

**From app containers** (e.g., configuring mail in your app):
- SMTP: `mail:1025` (no auth, no encryption) - all outgoing mail is caught by Mailpit
- Database: use the project service name (e.g., `db:5432` for postgres), NOT `adminer`
- Redis: use the project service name (e.g., `redis:6379`), NOT `redis-insights`

**From browser** (web UIs):
- URLs follow the pattern `https://{service}.shared.0ploy.dev` (or `http://` without TLS)
- If a shared service isn't running, the URL redirects to the docs page (this is normal)
- `zdev mail`, `zdev db`, `zdev redis` open the web UIs directly

**Important distinction:** Adminer and Redis Insights are **browsers** - they connect to your
project's own database/Redis services. They don't provide a database or Redis themselves. Your app
connects to its own `db` or `redis` service, not to `adminer` or `redis-insights`.

### Full config

```yaml
name: my-project

variables:              # reusable ${VAR} substitution (NOT passed to containers)
  DB_PASSWORD: postgres
  DB_NAME: ${PROJECTNAME}

shared:
  router: true          # Traefik (default: true)
  mail: true            # Mailpit
  db: true              # Adminer
  redis: true            # Redis Insights

environment:            # env vars for ALL containers
  APP_ENV: dev

services:
  app:
    image: node:22-alpine
    command: corepack enable && pnpm install && pnpm dev --host 0.0.0.0
    working_dir: /app
    volumes:
      - ${PROJECTPATH}:/app        # bind mount (source code)
    environment:                   # env vars for THIS container only
      DATABASE_URL: postgres://postgres:${DB_PASSWORD}@db:5432/${DB_NAME}
    routing:
      protocol: http               # http (default), https, tcp, udp
      port: 3000

  db:
    image: postgres:16-alpine
    volumes:
      - db_data:/var/lib/postgresql/data  # named volume (persistent)
    environment:
      POSTGRES_PASSWORD: ${DB_PASSWORD}
      POSTGRES_DB: ${DB_NAME}

mutagen:
  ignore:
    - node_modules
    - .pnpm-store
    - .nuxt
    - .output
```

### Key concepts

**Variables** (`variables:`) are substituted as `${VAR}` throughout the config file. NOT passed to
containers. Use for values shared across services (DB passwords, names).

**Environment** - project-level `environment:` goes to ALL containers. `services.<name>.environment`
goes to THAT container only and overrides project-level.

**1Password secrets** - `services.<name>.op-env: <environment-id>` injects every variable of a
1Password Environment into the container env (the ID is not secret and commits safely; explicit
`environment:` entries win); single variables use `op-env://<environment-id>/<VARIABLE>` env
values. Resolution happens only at container creation; changes in 1Password are picked up with
`zdev update --refresh-secrets` (NOT by `zdev restart`). For setup, the resolution/refresh rules,
CI via service accounts, and troubleshooting, read `references/secrets.md`.

**Built-in variables:** `${PROJECTPATH}`, `${PROJECTNAME}`, `${PROJECTDIR}`, `${ZDEV_DOMAIN}`, `${ZDEV_HOME}` + all host env vars.

**Volumes** - bind mounts (start with `/`, `.`, `${`) sync host dirs into containers. Named volumes
(just a name like `db_data`) are Docker-managed persistent storage. Named volumes auto-discovered,
persist across stop/start, removed with `down -v`.

**Routing** - HTTP: automatic HTTPS subdomain. TCP: `{ protocol: tcp, port: 5432, host_port: 5432 }`
exposes raw TCP on host (for DB tools). Services within a project reach each other by name.

**Multiple routed services in one project** - every `routing:` block defaults to the project's
single domain. A second routed service (e.g. RabbitMQ management UI, a separate admin app) will
collide on the same host. Give it a distinct hostname via `routing.domain`:

```yaml
services:
  rabbitmq:
    image: rabbitmq:3-management-alpine
    routing:
      port: 15672
      domain: rabbitmq.${PROJECTNAME}.${ZDEV_DOMAIN}   # https://rabbitmq.my-app.0ploy.dev
```

**Custom dev images** (`dockerfile:`) - when a stock image isn't enough, point the service at a
Dockerfile instead of pre-building manually:

```yaml
services:
  app:
    dockerfile: .zdev/Dockerfile   # build this instead of pulling image:
```

`zdev start` builds automatically when the image is missing or the Dockerfile changed; editing
source never triggers a rebuild (source is bind-mounted). The build context is always the project
root, so `COPY` paths resolve from there. Tag defaults to `zdev-<project>-<service>:latest`; set
`image:` alongside `dockerfile:` to pick the tag. Flags: `zdev start --build` forces a rebuild,
`--no-build` skips it. Dev containers only - build args, targets, multi-arch, build secrets, and
registry push need a custom build command plus `image:` instead.

**Mutagen** (macOS) - fast file sync. Always ignore dependency dirs (`node_modules`, `vendor`) and
build artifacts. Without ignores, installs take 5-10x longer.

**`command:` is wrapped in `sh -c`** — zdev runs the value of `command:` as a shell command, not
as a Docker CMD array. Bare `--flag` arguments meant for the image's default entrypoint fail with
`sh: 0: Illegal option --` and the container exits silently. If you need to pass flags to the
image's binary (e.g. MariaDB tuning), either invoke the binary explicitly (`exec mariadbd
--group_concat_max_len=320000 …`) or drop a config file via a volume mount. Symptom: container
status is `Exited (2)` and `zdev logs db` shows `sh: 0: Illegal option --`.

For stack-specific config examples, read `references/config-examples.md`.

## Custom Commands (Justfiles)

`.zdev/commands/*.just` files become `zdev` subcommands. The filename is the command name.
Uses [just](https://github.com/casey/just) syntax. `default` recipe runs when no recipe specified.

```
.zdev/commands/
  setup.just   ->  zdev setup
  test.just    ->  zdev test [recipe]
```

```just
# .zdev/commands/test.just
default:
    zdev exec app pnpm test

watch:
    zdev exec app pnpm test --watch
```

Justfiles run on the **host**. Always use `zdev exec` for container commands.

**Transparent forwarding of colon-namespaced subcommands.** To wrap CLIs like `bin/console
cache:clear` or `artisan migrate:fresh`, declare a recipe named after the `.just` file. zdev
auto-prepends the recipe name to the just invocation, so args with colons (which `just` otherwise
rejects as module-path syntax) pass through as recipe parameters:

```just
# .zdev/commands/console.just
console *args:
    zdev exec app php bin/console {{args}}
```

Now `zdev console cache:clear` → `bin/console cache:clear`. Without a filename-matching recipe,
the legacy behavior holds (first arg is the recipe name), so simple multi-recipe files like the
`test.just` above keep working unchanged.

## Linking projects

When two zdev projects need to reach each other (a monolith calling a local microservice, split
front-end/back-end, shared gateway), create a named **link** — a shared Docker network any project
can join. `zdev link create/join/status/leave/delete` manages them. Cross-project DNS is
`<service>.<project>.zdev` (FQDN, not the short alias), and cross-project URLs are plain HTTP to
the internal port, not `https://*.0ploy.dev` (that wildcard resolves to the container's
own loopback). For the full details (DNS rules, URL rules, persistence across restarts, per-service
granularity), read `references/linking.md`.

## Setting Up zdev for an Existing Project

Different from template scaffolding: the source code is already on the host, so the container's
`command:` doesn't need the `.setup-complete` wait loop — just install deps and exec the dev
server. `setup.just` is optional; only create one if there's a multi-step onboarding (migrations,
seed data, asset build).

1. **Read the existing stack.** `package.json` / `composer.json` / `requirements.txt` for framework
   and dev command. `.env` / `.env.example` for `DATABASE_URL`, `MAILER_DSN`, `HOST`, `PORT`,
   `APP_URL`. Existing `docker-compose.yml` / `Dockerfile` as a hint (ports, volumes) — not source
   of truth. README for any manual setup steps.
2. **Pick a starting config** from `references/config-examples.md` matching the stack. Prefer a
   stock image; if the app needs system packages or tooling baked in (or compose has a `build:`
   section), write a dev Dockerfile in `.zdev/` and point the service at it with
   `dockerfile: .zdev/Dockerfile` — zdev builds it automatically, no build script needed.
3. **Bind the dev server to all interfaces** in the `command:` — otherwise the container port isn't
   reachable from Traefik: `HOST=0.0.0.0` (Node), `--host 0.0.0.0` (Vite), `--allow-all-ip`
   (Symfony CLI), `0.0.0.0` (Django `runserver`).
4. **Rewire env for the zdev network.** Change `DATABASE_URL` host from `localhost` / `127.0.0.1`
   / compose's service name to the zdev service name you declared (commonly `db`). Set
   `MAILER_DSN: "smtp://mail:1025"` so outbound mail is caught by Mailpit. **For any Symfony /
   Sylius / Shopware / Laravel project**, also set `SYMFONY_TRUSTED_PROXIES: private_ranges` now —
   without it, the debug toolbar and login flows break behind Traefik (see Debugging → "Symfony/Sylius
   behind Traefik"). Laravel equivalent: `TRUSTED_PROXIES=*`.
5. **Mirror the existing DB** (image, version, credentials, database name) — the app's `.env`
   usually tells you all three.
6. **`mutagen.ignore`** for dependency and build artifact dirs: `node_modules`, `.pnpm-store`,
   `vendor`, `var/cache`, `.nuxt`, `.next`, framework-specific build output. Without this, installs
   are 5–10× slower on macOS.
7. **`.gitignore`:** add `.zdev/local/` (always) and `.pnpm-store/` (pnpm projects). `.zdev/local/config.yaml` deep-merges on top of `.zdev/config.yaml` before variable substitution, so per-developer overrides go there (e.g. `variables: { STRIPE_KEY: ... }` referenced as `${STRIPE_KEY}` in the committed config). For secrets the whole team needs, prefer 1Password Environments (`op-env`, see `references/secrets.md`) over per-developer local copies - the existing `.env` can be imported into an Environment directly.
8. **`zdev start`, then verify in a browser** (not just curl): check the console and network tabs
   for mixed-content, missing assets, JS errors. `curl 200` lies for HTML apps.
9. **Document** `zdev start` in the project README and `zdev exec app <cmd>` patterns in its
   `CLAUDE.md` so future agents know how to operate the env.

For stack-specific landmines when writing the config or entrypoint (Node corepack, pnpm build
scripts, PHP `memory_limit`, PHP extensions, Symfony `TRUSTED_PROXIES`, Webpack Encore / Vite
rebuild, mailer DSN), read `references/stack-gotchas.md` — those patterns apply whether you're
wrapping an existing repo or authoring a template.

## Debugging

**Container crashes:** `zdev logs -f app` to see why. `zdev restart app` bounces just that one
container; `zdev restart` does the whole project. `zdev down && zdev start` for a full clean restart.

**Config changes aren't taking effect:** `zdev restart` (with or without a service name) just
stops+starts the existing container, so edits to `environment:`, `image:`, `command:`, routing, or
volume mounts don't apply. Use `zdev update` — it diffs the config against the running containers
and recreates only the services that actually changed. Code changes are live via bind mount /
Mutagen — no restart or update needed.

**Dockerfile changes aren't taking effect:** `zdev update` (or `start`) detects edits to the
Dockerfile itself and rebuilds. But files the Dockerfile `COPY`s are NOT hashed — after changing
those, run `zdev update --build` to force the rebuild.

**Redirects to docs page:** Either the container isn't running or `routing.port` doesn't match the
app's port. For shared service UIs (mail, db, redis), also check `zdev services status` - the
service needs to be running AND the project must have the corresponding `shared.*` option enabled.

**`https://*.0ploy.dev` won't resolve at all (DNS verification failed on `zdev start`):** the
network's router likely enforces DNS rebinding protection - it drops the wildcard record's
`127.0.0.1` answer. Run `zdev dns enable` (or let `zdev systemcheck` offer it): a local dnsmasq
container answers the zdev domain and the OS is pointed at it for that domain only (one sudo prompt;
`/etc/resolver` on macOS, systemd-resolved on Linux). `zdev dns status` / `zdev dns disable` manage it.

**File sync issues (macOS):** `zdev mutagen status` / `zdev mutagen reset`

**DB connection refused:** Use service name (`db`), not `localhost`. Example: `postgres://postgres:postgres@db:5432/app`

**Can't reach `https://other-project.0ploy.dev` from inside a container:** `*.0ploy.dev`
is a wildcard that resolves to `127.0.0.1` — from inside a container that's its own loopback, not
the host's Traefik. Container-to-container traffic must go HTTP-direct using the container name:
`http://<service>.<project>.zdev:<internal-port>`. Both projects must be joined to a shared link
(see "Linking projects").

**Symfony/Sylius behind Traefik — stuck "Loading…" debug toolbar, broken admin login, mixed-content
errors in the console:** Traefik terminates TLS and forwards HTTP to the app. Without trusted-proxy
config, Symfony can't tell the outer request was HTTPS and generates `http://` URLs inside the HTTPS
page — the browser blocks them. Fix: add `SYMFONY_TRUSTED_PROXIES: private_ranges` to the app service
`environment:` (Symfony 6.3+ shorthand for RFC1918 + 127.0.0.1). Laravel equivalent: `TRUSTED_PROXIES=*`
for the TrustProxies middleware. Any framework that generates absolute URLs needs similar awareness.

**`curl 200 OK` isn't enough for HTML apps:** mixed-content, CSP failures, and JS errors are
browser-only failure modes — curl can't see them. After finishing a web template or UI change, open
the project in a browser via the chrome-devtools MCP tools (`new_page`, `list_console_messages`,
`list_network_requests`) and confirm the console is clean and all requests are `https://`.

**Why `.pnpm-store` must be ignored:** pnpm creates a ~500MB platform-specific store. If synced,
wrong binaries (glibc vs musl) break the container on image changes.

## Creating zdev Templates

Templates enable `zdev create <template> my-app` for one-command project scaffolding.

**For template authoring: read `references/templates.md`** for the `.setup-complete` pattern,
scaffolding strategies, and `setup.just` conventions. For the stack-specific runtime behaviors the
template entrypoint must handle (Node, PHP, PHP framework landmines), read
`references/stack-gotchas.md`. Key concepts:

- **`.setup-complete` marker pattern** - solves the container startup vs setup circular dependency.
  Container waits in a loop until setup creates the marker, then starts the app.
- **`setup.just`** runs on the host with `zdev exec` for container commands. Interactive terminal
  means framework prompts work (unlike the container entrypoint which has no TTY).
- **`@zdev step "<msg>"` for phase headers** in setup.just, not `@echo`. Setup output is a wall of
  apk/composer/npm noise; plain echo disappears in it. `zdev step` prints two blank lines + cyan
  `▶` + bold so each phase stands out. Auto-plain on non-TTY / `NO_COLOR`.
- **Scaffold in-place** (`--force`) when the framework supports it (Nuxt). Scaffold in `/tmp` and
  copy back when the tool requires an empty dir (Symfony) - safe for PHP, not for Node.js/pnpm.
- **`pnpm approve-builds --all`** after install for native module prebuilt binaries (pnpm v10).
- **`npx nuxi prepare`** for Nuxt to trigger module dependency prompts during setup (not at runtime).

Test locally: `zdev create ./my-template test-app && cd test-app && zdev setup`
