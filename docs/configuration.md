# Configuration

A project is configured by `.zdev/config.yaml`; machine-wide settings live in `~/.zdev/global-config.yaml`.

## Writing a config by hand

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
    - .nuxt
```

`${PROJECTPATH}` is resolved automatically to your project's absolute path. Other available variables: `${PROJECTNAME}`, `${PROJECTDIR}`, `${ZDEV_DOMAIN}`.

Then `zdev start`. More complete files for common stacks: [examples.md](examples.md).

## Project Configuration Reference (`.zdev/config.yaml`)

### Project-level fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `name` | string | directory name | Project name, used in domain and container names |
| `domain` | string | `{name}.<global domain>` (`{name}.0ploy.dev` by default) | Project domain for HTTP routing. The suffix comes from `domain` in `~/.zdev/global-config.yaml` |
| `variables` | map | - | Reusable `${VAR}` placeholders substituted throughout the config (not passed to containers) |
| `environment` | map | - | Environment variables passed to ALL containers. Values may reference 1Password Environment variables (`op-env://<environment-id>/NAME`) |
| `shared.router` | bool | `true` | Connect to shared Traefik router |
| `shared.mail` | bool | `true` | Connect to shared Mailpit |
| `shared.db` | bool | `false` | Connect to shared Adminer |
| `shared.redis` | bool | `false` | Connect to shared Redis Insights |
| `shared.logs` | bool | `true` | Connect to shared Dozzle log viewer |
| `mutagen.ignore` | list | - | Paths excluded from file sync (macOS). Mutagen itself is configured globally, see below |

### Service fields (`services.<name>.`)

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `image` | string | required unless `dockerfile:` is set | Docker image (with `dockerfile:`, the tag the build produces) |
| `dockerfile` | string | - | Build a local dev image from this Dockerfile (path relative to the project root, which is also the build context). Auto-builds on start when missing or changed |
| `command` | string | - | Container command |
| `working_dir` | string | - | Working directory inside container |
| `volumes` | list | - | Volume mounts (bind mounts and named volumes) |
| `environment` | map | - | Env vars for this container (overrides project-level). Values may reference 1Password Environment variables (`op-env://<environment-id>/NAME`) |
| `op-env` | string | - | 1Password Environment ID: injects every variable of that Environment into the container env. Explicit `environment:` entries win. Not secret, safe to commit |
| `routing.protocol` | string | `http` | `http`, `https`, `tcp`, `udp` |
| `routing.port` | int | 80 (http), 443 (https) | Container port to route to |
| `routing.host_port` | int | - | Host port for TCP/UDP (required for tcp/udp) |
| `routing.domain` | string | project domain | Custom domain for this service (http/https only) |
| `labels` | map | - | Docker labels |
| `mutagen.user` | string | - | Owner stamped on synced files inside the container (e.g. `www-data`). Use when the in-container process runs as a non-root user that must read/write the synced tree |
| `mutagen.group` | string | - | Group stamped on synced files inside the container |
| `mutagen.file_mode` | string | - | Octal mode for synced files (e.g. `"0644"`) |
| `mutagen.directory_mode` | string | - | Octal mode for synced directories (e.g. `"0755"`) |
| `mutagen.no_sync` | list | - | Container paths that stay plain Docker binds instead of being synced. For directories read during container startup, which a sync volume cannot guarantee are filled yet |

Config loading validates project and service names, requires every service to set `image:` or `dockerfile:`, checks routing protocols and port ranges, rejects duplicate TCP/UDP host ports, validates Mutagen file modes, and checks that every `mutagen.no_sync` path matches one of the service's mounts. Unknown fields are rejected in both project and global config files.

**Per-service Mutagen ownership** is for images whose runtime user differs from root - PHP/Apache as `www-data`, Node as `node`, etc. Without it, Mutagen writes files as root and the container's process can't read them. Example:

```yaml
services:
  web:
    image: my-php-apache:latest   # apache runs as www-data
    volumes:
      - ${PROJECTPATH}:/var/www/html
    mutagen:
      user: www-data
      group: www-data
```

When you change any of these values, zdev detects the drift, recreates the sync session, and runs `chown -R` inside the container so pre-existing files pick up the new ownership too. (Mutagen's own defaults only stamp newly-created files.)

### Variables and environment

**`variables`** define `${VAR}` placeholders substituted throughout the config file. They are NOT passed to containers. Use them to avoid duplicating values like database passwords across services.

**`environment`** (project-level) is passed to ALL containers. **`services.<name>.environment`** is passed to that specific container and overrides project-level values with the same name.

**Built-in variables:** `${PROJECTNAME}`, `${PROJECTPATH}`, `${PROJECTDIR}`, `${ZDEV_DOMAIN}`, `${ZDEV_HOME}`, `${USER}`, `${HOME}`, plus all host environment variables. User-defined `variables` can reference built-in ones (e.g. `DB_NAME: ${PROJECTNAME}_db`).

### 1Password secrets (`op-env`)

`services.<name>.op-env` and `op-env://<environment-id>/<VARIABLE>` values are explained, with all rules, in [secrets.md](secrets.md).

### Local overrides (`.zdev/local/config.yaml`)

If `.zdev/local/config.yaml` exists, it is deep-merged on top of `.zdev/config.yaml` before variable substitution. Use it for per-developer settings that shouldn't be committed (machine-specific tweaks, override images, personal secrets - though for team secrets, prefer `op-env://` references above). Add `.zdev/local/` to `.gitignore`.

Merge rules:
- **Maps** merge recursively - e.g. add a single env var to a service without copying the rest.
- **Scalars** in local replace the base value.
- **Slices** in local replace the base slice entirely (no append). To tweak `volumes:` or `mutagen.ignore:`, copy the full list.

Because the merge happens before substitution, you can put `variables:` in the local file and reference them with `${VAR}` from the committed config:

```yaml
# .zdev/config.yaml (committed)
services:
  app:
    environment:
      STRIPE_KEY: ${STRIPE_KEY}

# .zdev/local/config.yaml (gitignored)
variables:
  STRIPE_KEY: sk_test_abc123
```

**A second checkout of the same project** (a git worktree, a copy) needs its own name, because containers, volumes and the network are keyed by project name. zdev refuses to start, update or `down` a checkout whose name is already registered to another directory that still holds that project. Set a different `name:` in the second checkout's `.zdev/local/config.yaml`. A project whose registered directory was moved or deleted is simply taken over.

## Volumes

**Bind mounts** (`${PROJECTPATH}:/app`) sync your source code into the container. Edits on the host are reflected immediately. On macOS, zdev handles fast sync automatically via Mutagen. Add `node_modules`, `.pnpm-store`, and build caches to `mutagen.ignore` so they stay inside the container (fast) and don't sync back to the host.

A service can declare as many directory binds as it needs - each one gets its own sync volume and its own sync session, so a second bind is synced just as fully as the first.

Some directories must **not** be synced. A container starts before its sync sessions are resumed, so a directory the entrypoint reads once during startup - `/docker-entrypoint-initdb.d` for MySQL and Postgres, nginx `conf.d`, mounted certificates - would be empty at exactly the moment it is read. List those paths under `mutagen.no_sync` and they stay plain Docker binds, present from the container's first instant:

```yaml
services:
  mysql:
    image: mysql:8
    volumes:
      - ${PROJECTPATH}/initdb:/docker-entrypoint-initdb.d
      - ${PROJECTPATH}/src:/app
    mutagen:
      no_sync:
        - /docker-entrypoint-initdb.d   # seeded before startup, never synced
```

Read-only binds (`./conf:/etc/app:ro`) are never synced either: Mutagen writes into the container through the mount, so they always stay plain Docker binds and keep their `:ro`.

Each entry must match one of that service's mount paths, otherwise the config is rejected - a typo would silently sync the directory you meant to keep native. Single-file binds (`${PROJECTPATH}/etc/app/.env:/app/config/.env`) are not synced and stay plain bind mounts - but make sure the file exists on the host before starting, because Docker creates a missing bind source as a *directory*, which turns the entry into a synced directory bind.

zdev holds every synced service until the initial sync is done, so it never starts against an empty or stale directory: a `command:` is wrapped in a short wait, and a service without one gets its image's own entrypoint wrapped the same way (php-fpm, nginx, zpinit in supervise mode keep starting as they always do, just after the sync). Only an image without `sh` can't be held; zdev warns about it at start. If a sync session can't be established, the service's command is held at the sync-ready gate instead of starting against an empty directory, and `zdev start` reports the failure and exits non-zero. Fix the cause and run `zdev start` again - the gate re-arms on every container start, so a service never needs to be removed to recover.

**Named volumes** (`db_data:/var/lib/postgresql/data`) are persistent storage managed by zdev. Use these for data that must survive `zdev down` - database files, uploaded assets, SQLite databases:

```yaml
volumes:
  - ${PROJECTPATH}:/app              # your source code (synced to host)
  - db_data:/var/lib/postgresql/data  # database files (persists across down)
  - data:/app/data                   # SQLite, uploads, etc.
```

Named volumes persist across `zdev stop`/`zdev start` AND `zdev down`. Only removed with `zdev down -v`. That command also removes project-scoped volumes left by services deleted from the config. No separate declaration needed - zdev discovers them automatically.

## Custom Dev Images (`dockerfile:`)

When a stock image isn't enough - you need system packages, a compiled binary, or a specific toolchain baked into the container - point the service at a Dockerfile instead of hand-rolling a build script:

```yaml
services:
  app:
    dockerfile: .zdev/Dockerfile   # build this instead of pulling an image
    command: pnpm dev --host 0.0.0.0
    volumes:
      - ${PROJECTPATH}:/app
```

`zdev start` builds the image automatically when it's missing or when the Dockerfile changed - so a fresh clone starts with zero manual steps. Editing source files never triggers a rebuild (source is bind-mounted live); Docker's layer cache keeps rebuilds fast when they do happen. `zdev update` treats a stale image as a config change and recreates the service with the rebuilt image.

The build context is always the **project root**: `COPY`/`ADD` paths in the Dockerfile resolve from there (regardless of where the Dockerfile lives), and a `.dockerignore` at the root keeps builds fast on large repos. The image is tagged `zdev-<project>-<service>:latest` by default; set `image:` alongside `dockerfile:` to choose the tag yourself. Force or skip building with flags on `start` and `update`:

```bash
zdev start --build      # rebuild even if nothing changed
zdev start --no-build   # never build; fails clearly if the image is missing
```

`dockerfile:` is for **dev containers only**. Build args, multi-stage targets, custom contexts, multi-arch builds, build secrets, registry push/pull auth, and production images are intentionally not supported - for those, keep a custom project command (`.zdev/run/build-image.toml`) that produces the `image:` tag.

## Global Configuration Reference (`~/.zdev/global-config.yaml`)

Applies to all projects. Auto-created on first run. Usually you don't need to touch this.

```yaml
domain: 0ploy.dev
ssl:
  enabled: true
mutagen:
  enabled: auto       # "auto" (macOS only), "true" (always), "false" (never)
  sync_mode: two-way-safe  # two-way-safe (default), two-way-resolved, one-way-safe, one-way-replica
shared:
  logs:
    shell: false      # enable Dozzle's in-container shell (default false; see below)
```

Shared-service images (`shared.<service>.image`, `dns.image`) default to the versions the installed zdev was tested with and follow them on upgrade. Set one only to pin your own version or to use a mirror. zdev up to v0.14 wrote every default image into this file, which kept those versions forever; `zdev start`, `zdev status`, `zdev update`, `zdev services` and `zdev systemcheck` warn about such lines (another tag of the default image). Delete them to follow the defaults again.

`mutagen.sync_mode` is passed to every Mutagen session as `--sync-mode`. Changing it recreates the sync sessions on the next `zdev start`. The one-way modes sync host to container only, so files a container writes (`vendor/` from `composer install`, generated code) never come back to the host.

### Agent sandboxes (`agent:`)

Settings for `zdev claude` ([Agents](agents.md)). They live only here, on your machine: the agent can edit `.zdev/config.yaml`, so nothing there may grant it more access.

```yaml
agent:
  claude:                      # what only Claude Code understands
    channel: stable            # stable (default, about weekly) | latest | a version like 2.1.325
    disable_plugins: [codex@openai-codex]   # Claude plugins switched off in every sandbox (host-only ones)
  host_hook: ~/bin/zdev-agent-hook        # runs on this machine when a session starts and ends (see below)
  defaults:                    # for every project
    claude:
      connectors: false        # claude.ai connectors (Drive, Slack, ...) of your login; off by default
    host_access: []            # ports on this machine the agent may reach (e.g. 11434 for Ollama)
    limits: {cpus: "4", memory: 8g, pids: 4096}
    egress: log                # log (every public host, logged) | enforce (only allow_hosts, credential hosts and Claude's own hosts)
    allow_hosts: []            # under enforce: "registry.npmjs.org", "*.github.com"
    credentials: []            # names from agent.credentials below
  projects:
    shop:                      # per project, over defaults
      service: web             # image the agent runs in when the session names none (default: app)
      claude: {connectors: true}
    ci-runner:
      docker_socket: accept    # a service mounts the Docker socket: run anyway, the sandbox is no boundary
  credentials:                 # tokens the egress proxy swaps in; the sandbox only sees placeholders
    github:
      hosts: [github.com, api.github.com]
      env: [GH_TOKEN, GITHUB_TOKEN]
      command: gh auth token   # exactly one of: command, secret, file
      # secret: op://<vault>/<item>/<field>  or  op-env://<environment-id>/GITHUB_TOKEN
      # file: ~/.zdev/agent/credentials/github   (must be mode 0600 and yours)
      # placeholder_prefix: sk-ant-oat01-       (optional: for tools that check a token's shape; swapped together with the rest)
```

`zdev agent credentials` manages this section for you: `add` asks for hosts, variables and the source (paste the token and store it in 1Password or an owner-only file, a 1Password reference, or a command), or takes them as flags (`--host`, `--env`, `--command`/`--secret`/`--file`, `--placeholder-prefix`, `--preset npm|anthropic`); `grant`/`revoke` (in a project, or `--all` for `defaults`), `list`, `test` (reads it, prints only its length) and `remove` (also deletes a token file zdev stored). The `anthropic` preset is for pi with a Claude Pro/Max subscription: a token from `claude setup-token` for `api.anthropic.com` as `ANTHROPIC_OAUTH_TOKEN`, with the placeholder starting `sk-ant-oat01-`, because pi only treats a token of that shape as a subscription login. A project's own `credentials:` list replaces `defaults`, so granting to a project that has none starts from the defaults it gets now. The proxy swaps a token only in request headers: tools that send it in the URL or body, or sign requests with it (AWS), need the real value and can't use a credential.

A project uses a credential when it lists it (`projects.<name>.credentials: [github]`, or in `defaults`). `zdev agent setup`, run in a project, writes both for the host of its `origin` remote: GitHub, or GitLab (gitlab.com or a self-hosted instance, recognized by its API). It walks you through a token for the project's repository (GitHub: a fine-grained token; GitLab: a project access token, role Developer, scope `api`), checks it, stores it in 1Password or a `file:` only you can read, and grants it in place of an earlier credential for the same host, editing this file in place (your comments and order stay). GitHub tokens set `GH_TOKEN`, GitLab tokens `GITLAB_TOKEN`. Prefer a token scoped to the project's repository over your own `gh`/`glab` login: the agent can use whatever the token allows while the session runs. Credentials need zdev's CA bundle in the containers (HTTPS set up, `zdev systemcheck`).

`host_hook` is a command run with `sh` on your machine when a `zdev claude` session starts and when it ends, for example to tell your terminal that Claude runs in this tab. It gets the event in `ZDEV_AGENT_EVENT` (`SessionStart` or `SessionEnd`), the PID of the `zdev claude` process in `ZDEV_AGENT_PID`, the project in `ZDEV_PROJECT`, and on stdin JSON in the shape of Claude Code's hook input: `session_id`, `cwd` (the project directory on your machine) and `hook_event_name`. A replaced sandbox doesn't count as a new session. The hook gets 5 seconds, its output is discarded, and a failure only prints a warning. `SessionEnd` is skipped when `zdev claude` is killed or crashes, so watch `ZDEV_AGENT_PID` if something must not outlive the session. Ignore events you don't know: later versions may send more. For [cmux](https://cmux.com), which then shows the Claude icon on the tab (it clears it when that PID exits):

```sh
#!/bin/sh
case "$ZDEV_AGENT_EVENT" in
  SessionStart) sub=session-start ;;
  SessionEnd)   sub=session-end ;;
  *) exit 0 ;;
esac
CMUX_CLAUDE_PID=$ZDEV_AGENT_PID exec cmux claude-hook "$sub"
```

Outside a cmux tab `cmux claude-hook` does nothing.

What your own `zdev start`, `update` and `restart` applied is recorded in `~/.zdev/agent/approved/<project>.json`; config changes from a sandbox that reach beyond it are refused ([Agents](agents.md)). `zdev down` and `zdev rename` remove the file.

Claude Code releases are downloaded into Docker volumes (`zdev-claude-<version>-<libc>-<arch>`), checked against Anthropic's release manifest; a superseded version is removed a day after a newer one is first used. `zdev claude` asks for the channel's release once a day and never waits for it when a build is already there: the session starts with the newest one here and the new release downloads in the background for the next session (a pinned version, or the very first build, is downloaded before the start). `zdev agent update` asks and downloads right away. Each project keeps the agent's home in a volume (`zdev-agent-home-<project>`, removed by `zdev down -v`).

Environment variables zdev itself honors: `ZDEV_HOME` relocates the zdev home directory (config, state, certs, downloaded tools; default `~/.zdev`), `ZDEV_PLAIN=1` forces plain output (no colors, hyperlinks, or step markers - overrides the `terminal.plain` config setting), `ZDEV_NO_UPDATE_CHECK=1` disables the background self-update check, `OP_SERVICE_ACCOUNT_TOKEN` authenticates 1Password secret resolution in CI, and `ZDEV_AGENT_LINUX_BINARY` points `zdev claude` at a Linux build of zdev for the sandbox (development builds; releases download their own).
