---
name: zdev
description: |
  Local development environment framework using Docker. Use this skill whenever the user's project
  has a `.zdev/` directory, when they ask to set up a local dev environment, start/stop containers,
  run commands inside containers, run project tasks, check logs, configure routing, or debug
  container issues. Also trigger when: the user says "start the project", "run tests in docker",
  "check the logs", "open mailpit", "set up zdev", "add zdev to this project", mentions
  `.zdev/config.yaml`, or asks about shared services, HTTPS certificates, or file sync on macOS.
  Also trigger for project commands: `zdev run`, `.zdev/run/*.toml` job files, or converting
  `.zdev/commands/*.just` justfiles (`zdev run --convert-just`).
  Also trigger when the user wants to create an zdev template, scaffold a new project template,
  build a starter template for a framework, or asks about `zdev create`, template authoring,
  the setup command, or the .setup-complete pattern.
  Also trigger for secrets in the dev environment: 1Password integration, `op-env://` references,
  the `op-env:` service field, moving API keys or passwords out of committed config or `.env`
  files, rotating secrets, or "how do I share dev secrets with my team".
---

# zdev - Local Development Environment

zdev runs Docker-based dev environments. Each project has `.zdev/config.yaml` defining its services;
`zdev start` creates an isolated Docker network, starts the containers, and connects shared services
(Traefik router, Mailpit, Adminer, Redis Insights, Dozzle). The app is served at
`https://{name}.0ploy.dev` (wildcard DNS to 127.0.0.1). On macOS, Mutagen syncs files at native speed.

Check it's installed with `zdev version`; otherwise
`curl -fsSL https://raw.githubusercontent.com/0ploy/zdev/main/install.sh | sh && zdev systemcheck`.

## Ground rules

- **Operate through zdev, not raw `docker`.** zdev knows container names, networks, sync state and
  env; `docker` commands bypass that and leave things zdev doesn't expect.
- **Run things inside containers with `zdev exec <service> <cmd>`**, not on the host - the host may
  lack the runtime, and the container is where dependencies and the database live.
- **Project tasks are `zdev run` commands - list them before guessing** (see below). They encode how
  this project does tests, migrations, consoles; reinventing them gets details wrong.
- **Config edits need `zdev update`, not `zdev restart`.** Restart keeps the old container; update
  recreates exactly the services whose config changed.
- **Inside containers, other services are reached by service name** (`db:5432`, `mail:1025`), never
  `localhost`. Other projects are reached by their real URL (`https://api.0ploy.dev`, through the
  router; containers trust the zdev CA out of the box, see `references/configuration.md`, Routing).
- **Only use config fields that exist** (`references/configuration.md`): unknown fields make the
  whole config fail to load.

## CLI

```bash
zdev start [service] [-q]       # start project (or one service); -q skips the info display
zdev stop [service] / restart [service]
zdev update [--build] [--refresh-secrets]   # apply config changes; rebuild image; re-read 1Password
zdev down [-v]                  # remove containers + network (-v: also volumes = data loss)
zdev exec <service> <cmd>       # e.g. zdev exec app pnpm test; zdev exec app bash
zdev logs [service] [-f] [--tail N]
zdev info / status / config     # URLs and services / health incl. stale mounts / resolved config
zdev open [project]             # open in browser
zdev mail / db / redis          # open shared service UIs
zdev services status|start|stop|recreate
zdev create <template> <name>   # express, nuxt4, symfony, any GitHub repo, ./local-dir; --branch <ref>
                                # then follow its printed next steps (usually zdev start, sometimes zdev run setup)
zdev link create|join|leave|ls|status|delete    # cross-project networks (references/linking.md)
zdev dns enable|disable|status  # only if the router blocks the wildcard (references/debugging.md)
zdev mutagen status|flush|reset # macOS file sync
zdev rename <new-name> / cleanup
```

## Project commands (`zdev run`)

```bash
zdev run                        # list commands and jobs - do this first
zdev run --json                 # same, machine-readable (files, jobs, services, descriptions)
zdev run <cmd> [args]           # single-job command; args reach the script verbatim
zdev run <cmd> <job> [args]     # multi-job command: the job name is required (no default job)
zdev run --show <cmd> [job]     # what would run (dependencies, service, script) - without running
```

- zdev's own flags go **before** the command name; everything after it belongs to the job.
- A job with `confirm` needs a terminal; in non-interactive runs pass `--yes` only if the user agreed.
- `zdev run` never resolves to built-ins: `zdev run db` is the project's `db.toml`, `zdev db` opens Adminer.
- Legacy `.zdev/commands/*.just` run as `zdev <name>` and are deprecated.

**Creating or editing a job file, or converting justfiles: read `references/project-commands.md`.**

## Common problems

| Symptom | Fix |
|---|---|
| Config change has no effect | `zdev update` (not restart) |
| URL redirects to the docs page | container down, `routing.port` wrong, or dev server not bound to `0.0.0.0` |
| `DB connection refused` | use the service name (`db`), not `localhost` |
| Container `Exited (2)`, `sh: 0: Illegal option --` | `command:` runs via `sh -c`; call the binary explicitly |
| Symfony toolbar stuck "Loading…", mixed content | `SYMFONY_TRUSTED_PROXIES: private_ranges`, then `zdev update` |

Everything else (DNS, file sync, stale mounts, secrets, cross-project access): `references/debugging.md`.

## References

Read the one that matches the task; don't load them all.

| Task | Read |
|---|---|
| Write or change `.zdev/config.yaml` (fields, volumes, routing, shared services, `dockerfile:`, local overrides) | `references/configuration.md` |
| Complete config for a stack (Node, PHP, Symfony, workers, RabbitMQ, ...) | `references/config-examples.md` |
| Add zdev to an existing repository | `references/existing-project.md` |
| Write or convert project commands (`.zdev/run/*.toml`, justfiles) | `references/project-commands.md` |
| Something is broken | `references/debugging.md` |
| Secrets, 1Password, `op-env`, `.env` migration | `references/secrets.md` |
| A container calls another project (real URL, no link) | `references/configuration.md` (Routing) |
| Direct access to another project's internal port (link networks) | `references/linking.md` |
| Build a template for `zdev create` (scaffold hook, setup command) | `references/templates.md` |
| Stack runtime landmines (corepack, pnpm builds, PHP extensions, asset builds) | `references/stack-gotchas.md` |

Verify web changes in a browser, not only with curl: mixed content, CSP and JS errors are invisible
to `curl`.
