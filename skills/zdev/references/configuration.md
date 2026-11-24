# Writing `.zdev/config.yaml`

Unknown fields are rejected (the whole config fails to load), so only use fields listed here. For complete stack examples (Node, PHP, Symfony, workers, RabbitMQ, ...) see `config-examples.md`.

## Contents

- Minimal and full config
- Variables, environment, built-ins
- Shared services: hostnames and access
- Volumes and file sync (Mutagen)
- Routing (HTTP, multiple domains, TCP/UDP, calling other projects, HTTPS from containers)
- Custom dev images (`dockerfile:`)
- Local overrides
- Gotchas

## Minimal config

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

## Full config

```yaml
name: my-project

variables:              # reusable ${VAR} substitution (NOT passed to containers)
  DB_PASSWORD: postgres
  DB_NAME: ${PROJECTNAME}

shared:
  router: true          # Traefik (default: true)
  mail: true            # Mailpit (default: true)
  logs: true            # Dozzle (default: true)
  db: true              # Adminer (default: false)
  redis: true           # Redis Insights (default: false)

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

Service fields: `image`, `dockerfile`, `command`, `working_dir`, `volumes`, `environment`, `op-env`, `routing.{protocol,port,host_port,domain}`, `labels`, `mutagen.{user,group,file_mode,directory_mode,no_sync}`. Project fields: `name`, `domain`, `info`, `variables`, `environment`, `shared.*`, `services`, `mutagen.ignore`.

## Variables, environment, built-ins

- **`variables:`** are `${VAR}` placeholders substituted throughout the config at load time. They do NOT reach containers. Use them for values shared across services (DB passwords, names).
- **`environment:`** at project level goes to ALL containers; `services.<name>.environment` goes to that container only and overrides project-level.
- **Built-ins:** `${PROJECTPATH}`, `${PROJECTNAME}`, `${PROJECTDIR}`, `${ZDEV_DOMAIN}`, `${ZDEV_HOME}`, plus all host env vars.
- **Secrets:** `services.<name>.op-env: <environment-id>` injects a whole 1Password Environment; `op-env://<id>/<VAR>` injects one value. Read `secrets.md` before touching secrets.

## Shared services: hostnames and access

| Service | Web UI URL | Container hostname | Container port | Purpose |
|---------|-----------|-------------------|----------------|---------|
| Mailpit | `mail.shared.0ploy.dev` | `mail` | `1025` (SMTP) | Email catching |
| Adminer | `db.shared.0ploy.dev` | `adminer` | `8080` (HTTP) | Database browser |
| Redis Insights | `redis.shared.0ploy.dev` | `redis-insights` | `5540` (HTTP) | Redis browser |
| Dozzle | `logs.shared.0ploy.dev` | - | - | Log viewer (projects with `shared.logs`) |
| Traefik | `router.shared.0ploy.dev` | `router` | - | Routing dashboard |

- **From app containers:** SMTP is `mail:1025` (no auth, no TLS). The database and Redis are the project's OWN services (`db:5432`, `redis:6379`), never `adminer` / `redis-insights` - those are only browsers.
- **From the browser:** `https://{service}.shared.0ploy.dev`; `zdev mail`, `zdev db`, `zdev redis` open them. A stopped shared service redirects to the docs page.
- `db` and `redis` UIs are opt-in per project (`shared.db: true`, `shared.redis: true`).

## Volumes and file sync (Mutagen)

- **Bind mounts** start with `/`, `.` or `${`; **named volumes** are a bare name (`db_data:/var/lib/...`). Named volumes need no top-level declaration, persist across `stop`/`start` and `down`, and are removed only by `zdev down -v`.
- **Mutagen** (macOS) syncs every directory bind at native speed. Always add dependency and build dirs to `mutagen.ignore` (`node_modules`, `vendor`, `.pnpm-store`, `.nuxt`, `.next`, `var/cache`); otherwise installs are 5-10x slower and `.pnpm-store` syncs ~500MB of platform-specific binaries that break on image changes.
- **Read-only binds (`:ro`) are never synced** - they stay plain Docker binds, so they don't need `no_sync`.
- **Directories read during container STARTUP** must be listed in `services.<name>.mutagen.no_sync` (e.g. `/docker-entrypoint-initdb.d`, nginx `conf.d`, certs): containers start before sync resumes, so those dirs would be empty when read. Entries must match one of the service's mount paths.
- **Single-file binds** stay plain bind mounts. Make sure the host file exists first (Docker creates a missing source as a directory). They go stale when an editor replaces the file; `zdev status` flags it, restarting the service fixes it.
- **Ownership:** if the container process runs as a non-root user (`www-data`, `node`), set `services.<name>.mutagen.user` / `group` so synced files are readable.

## Routing

- HTTP (default): automatic HTTPS at the project domain. `routing.port` must match the port the app listens on.
- **Every routed service defaults to the project's single domain**, so a second routed service (RabbitMQ UI, admin app) collides. Give it its own `routing.domain`:

  ```yaml
  services:
    rabbitmq:
      image: rabbitmq:3-management-alpine
      routing:
        port: 15672
        domain: rabbitmq.${PROJECTNAME}.${ZDEV_DOMAIN}   # https://rabbitmq.my-app.0ploy.dev
  ```

  Nested domains like this one get a trusted per-project certificate automatically on `zdev start` / `zdev update` (the main cert's wildcard covers only one label). If HTTPS fails with "no alternative certificate subject name", run `zdev update`.

- **TCP/UDP** for host tools (DB clients): `routing: { protocol: tcp, port: 5432, host_port: 5432 }`. Host ports must be unique across projects.
- Services in one project reach each other by service name (`db`, `redis`).
- **Other projects are reached by their real URL** (`https://api.0ploy.dev`, TCP routes like
  `db.api.0ploy.dev:5432`, shared UIs like `https://mail.shared.0ploy.dev`), no link needed: the
  router carries every registered routed hostname as a network alias on every project network, so
  the name resolves to Traefik, not `127.0.0.1`. Needs `shared.router` on in both projects and the
  target started with zdev at least once. Only config hostnames resolve (no wildcards from
  custom labels).
- **HTTPS from containers works without setup.** zdev mounts `~/.zdev/trust` at `/etc/zdev`
  (`ca.pem` = zdev CA, `ca-bundle.pem` = host public roots + zdev CA), mounts the bundle over
  `/etc/ssl/certs/ca-certificates.crt`, `/etc/pki/tls/certs/ca-bundle.crt` and
  `/etc/ssl/ca-bundle.pem`, and sets `NODE_EXTRA_CA_CERTS` and `REQUESTS_CA_BUNDLE`. Public HTTPS
  keeps working. Don't add `update-ca-certificates` steps or set `SSL_CERT_FILE`. Not covered: Java
  (`keytool` import of `/etc/zdev/ca.pem`), Rust with `webpki-roots`.
- A link (`linking.md`) is only for reaching another project's internal port without Traefik, or
  unrouted services.

## Custom dev images (`dockerfile:`)

```yaml
services:
  app:
    dockerfile: .zdev/Dockerfile   # build this instead of pulling image:
```

`zdev start` builds when the image is missing or the Dockerfile changed; source edits never rebuild (source is bind-mounted). Build context is always the project root (`COPY` paths resolve from there). Tag defaults to `zdev-<project>-<service>:latest`; set `image:` to pick it. `--build` / `--no-build` on `start`/`update`. Files the Dockerfile `COPY`s are NOT hashed: after editing them, `zdev update --build`. Dev containers only: build args, targets, multi-arch, registry push need a project command producing an `image:` tag instead.

## Local overrides

`.zdev/local/config.yaml` (gitignored) deep-merges on top of `.zdev/config.yaml` before variable substitution: maps merge, scalars replace, **lists replace** (copy the whole `volumes:` / `mutagen.ignore:` list to change it). Put per-developer values there as `variables:` and reference them as `${VAR}` in the committed config. Team secrets belong in 1Password instead. A second checkout of the same project (git worktree) must set its own `name:` there - zdev refuses a name registered to another directory that still holds the project, since containers and volumes are keyed by name.

## Gotchas

- **`command:` runs through `sh -c`**, not as a Docker CMD array. Bare `--flag` arguments meant for the image's entrypoint fail with `sh: 0: Illegal option --` (container `Exited (2)`). Call the binary explicitly (`exec mariadbd --group_concat_max_len=320000`) or mount a config file.
- **Bind the dev server to `0.0.0.0`**, otherwise Traefik can't reach it: `HOST=0.0.0.0` (Node), `--host 0.0.0.0` (Vite), `--allow-all-ip` (Symfony CLI), `0.0.0.0:8000` (Django).
- **Config changes need `zdev update`**, not `zdev restart` (restart keeps the old container).
- Stack-specific runtime landmines (corepack, pnpm build scripts, PHP extensions, `memory_limit`, Symfony trusted proxies, asset builds): `stack-gotchas.md`.
