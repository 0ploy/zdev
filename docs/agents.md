# Using zdev with coding agents

zdev is built so an AI coding agent (Claude Code, Cursor, Copilot, ...) can run a project without guessing: one command to start, predictable URLs, one config file, and project tasks it can list instead of reverse-engineering from a README.

## Install the zdev skill

```bash
npx skills add 0ploy/zdev
```

The skill teaches the agent how zdev works and loads automatically whenever a project has a `.zdev/` folder or the conversation is about zdev. It covers:

- **Operating a project:** starting and stopping, running commands in containers (`zdev exec`), logs, applying config changes (`zdev update`, not `restart`).
- **Project commands:** listing and running `zdev run` jobs, writing new job files, converting old justfiles.
- **Configuration:** writing `.zdev/config.yaml`, with ready-made examples for common stacks.
- **Adding zdev to an existing repository** step by step, including the usual stack pitfalls (dev server bound to `0.0.0.0`, database host, Mailpit, file sync ignores).
- **Debugging:** routing, DNS, file sync, connections between containers and projects.
- **Secrets** via 1Password and **building templates** for `zdev create`.

Then just ask, for example: *"set up zdev for this project"*, *"add a zdev command that resets the database"*, *"why doesn't my config change show up?"*.

## Tell agents how your project runs

Agents without the skill (or a teammate's different tool) still read your project's `CLAUDE.md` / `AGENTS.md`. A few lines there prevent most wrong guesses:

```markdown
## Local environment (zdev)

- Start with `zdev start`; the app runs at https://<project>.0ploy.dev.
- Run tools inside containers: `zdev exec app <command>` (never on the host, never raw `docker`).
- Project tasks: `zdev run` lists them, `zdev run <cmd> [job] [args]` runs one.
- After editing `.zdev/config.yaml`, apply it with `zdev update` (`zdev restart` keeps the old container).
- Inside containers, other services are reached by name (`db:5432`, `mail:1025`), not `localhost`.
```

## Commands made for agents

| Command | What an agent gets |
|---|---|
| `zdev run --json` | Every project command and job with description, service and dependencies, as JSON |
| `zdev run --show <cmd> [job]` | Exactly what a job would run (dependency order, service, workdir, script) without running it |
| `zdev status` | Service states, stale file mounts, shadowed legacy commands, shared services |
| `zdev info` | Project URLs and services |
| `zdev config` | The fully resolved config, after local overrides and variable substitution |
| `zdev logs <service> --tail 100` | Recent logs without following |

Project commands can't be confused with zdev's own commands: `zdev run db` is always the project's `db` command, while `zdev db` opens the database browser.

## Safety

- **The agent's commands run in containers**, so a malicious dependency it installs is sandboxed, not on your machine (see the security note in the [README](../README.md)).
- **No secrets in the config**: with [1Password secrets](secrets.md), the committed config only holds references, so an agent can read, edit and commit it without ever seeing secret values.
- **Destructive project commands can ask first**: a job with `confirm` won't run without a terminal unless `--yes` is passed explicitly, so an agent can't wipe your database by accident.
