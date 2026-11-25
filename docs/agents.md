# Using zdev with coding agents

zdev is built so an AI coding agent (Claude Code, Cursor, Copilot, ...) can run a project without guessing: one command to start, predictable URLs, one config file, and project tasks it can list instead of reverse-engineering from a README.

## Run Claude Code sandboxed: `zdev claude`

`zdev claude` starts Claude Code in a container of your project instead of on your machine. The container is built from a service's image (default `app`), so Claude has the project's own toolchain (cargo, pnpm, php, ...), the same source, environment and network as the app, and nothing else: no Docker socket, no access to your machine or its localhost services, no other projects' files. It is removed when the session ends; the source, Claude's memory and the conversation stay.

```bash
zdev claude                      # Claude in the app service's image
zdev claude --continue           # continue the last conversation (arguments go to Claude unchanged)
zdev claude node                 # in the node service's image (first argument, when it names a service)
zdev claude "fix the login bug"  # start with a prompt
zdev claude -- app               # pass "app" to Claude (a prompt that equals a service name)
```

**Log in once** with `/login` inside Claude. Every sandbox shares that login (stored in `~/.zdev/agent/claude-auth/`); you log in again only when Claude asks.

**What Claude can do there:**

| | |
|---|---|
| Build, test, run the project's tools | directly in the sandbox (the service's image) |
| See and manage the running app | through zdev: `zdev exec app ps aux`, `zdev logs app`, `zdev restart app`, `zdev exec app <cmd>` |
| `zdev status`, `zdev info`, `zdev run` | yes; `zdev run` jobs without a service run in the sandbox, never on your machine |
| `zdev down`, `link`, `cleanup`, other projects | refused, with a reason Claude passes on to you |
| Git | commit, branch, merge in the synced repository; `.git/hooks` and `.git/config` are read-only, so pushes, remotes and git config stay with you |
| Network | the project's services and the internet; not your machine (`host.docker.internal`) or private networks |

zdev checks every request from the sandbox on your machine, where the container can't reach it. Claude gets a short system prompt that tells it all this, so it doesn't spend turns finding out.

**Your Claude setup comes along:** skills, agents, commands, plugins, `CLAUDE.md`, settings and status line from `~/.claude` (read-only, so a session can't change them for every project), plus this project's memory and conversations (read-write, in `~/.claude/projects/`). Plugins that only work on the host can be switched off for sandboxes with `agent.disable_plugins` ([Configuration](configuration.md#agent-sandboxes-agent)).

**Git sync (macOS):** zdev syncs `.git` and `.zdev/` into the agent's service for the sandbox. When git writes the same file on your machine and in the sandbox at the same moment (your editor's background `git status` while Claude commits), Mutagen stops syncing it; `zdev status` says so, and `zdev mutagen repair-git` fixes it.

**Requirements:** Docker Engine 26 or newer (`zdev systemcheck` shows "Agent mode"), and `bash` and `git` in the service's image (`zdev claude` names what is missing). Run as the owner of the synced files: set `services.<name>.mutagen.user` to the app's user; without it the agent runs as root with no capabilities. A project where a service mounts the Docker socket is refused, since whoever reaches that service is root on your machine; you can accept that per project in host config.

**A repository without `.zdev/`:** `zdev claude` starts a bootstrap session in a small image of zdev's own (bash, git, GNU tools) to set the project up. Afterwards run `zdev start` and continue with `zdev claude --continue` in the project's own image.

**Settings** live only in `~/.zdev/global-config.yaml` (never in the project config, which the agent can edit): see [Configuration](configuration.md#agent-sandboxes-agent).

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

- **With `zdev claude` the agent itself runs in a container**, not only its commands: it can't read your files, keys or other projects, and every zdev request it makes is checked on your machine (see above).
- **The agent's commands run in containers**, so a malicious dependency it installs is sandboxed, not on your machine (see the security note in the [README](../README.md)).
- **No secrets in the config**: with [1Password secrets](secrets.md), the committed config only holds references, so an agent can read, edit and commit it without ever seeing secret values.
- **Destructive project commands can ask first**: a job with `confirm` won't run without a terminal unless `--yes` is passed explicitly, so an agent can't wipe your database by accident.
