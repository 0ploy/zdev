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

**Which image:** the service you name first, else `agent.projects.<project>.service` in `~/.zdev/global-config.yaml`, else the service named `app`, else the one service that mounts the project. When none of these decides (no `app`, several services mount the project), `zdev claude` asks you to name one, and `zdev agent setup` asks once and stores your choice.

**Log in once** with `/login` inside Claude. Every sandbox shares that login (stored in `~/.zdev/agent/claude/auth/`); you log in again only when Claude asks.

**Claude Code updates:** a session never waits for a download. zdev looks for a new release once a day and downloads it in the background; the session after the download uses it (Claude's own updater is off in the sandbox). `zdev agent update` gets it right away. The very first download happens in `zdev agent setup`, which waits for it (for the C library of the project's image; outside a project for the bootstrap image). A session that finds no Claude for its image starts that download in the background and asks you to start again in a minute, or to run `zdev agent setup claude` to wait. Pick the release line with `agent.claude.channel` ([Configuration](configuration.md#agent-sandboxes-agent)). Superseded versions are removed a day after the new one was first used.

**What Claude can do there:**

| | |
|---|---|
| Build, test, run the project's tools | directly in the sandbox (the service's image); zdev adds `jq`, `gh`, `glab`, `fd` and `rg` for images without them |
| See and manage the running app | through zdev: `zdev exec app ps aux`, `zdev logs app`, `zdev restart app`, `zdev exec app <cmd>`; an interrupted `zdev exec` stops in the container too |
| `zdev status`, `zdev info`, `zdev run` | yes; `zdev run` jobs without a service run in the sandbox, never on your machine |
| Edit `.zdev/` and apply it: `zdev start`, `stop`, `restart`, `update` (`--build`, `--refresh-secrets`) | yes, unless the config reaches outside the project beyond what you applied yourself (see below) |
| `zdev down`, `link`, `cleanup`, `rename`, other projects | refused, with a reason Claude passes on to you |
| Git | commit, branch, merge in the synced repository; `.git/hooks` and `.git/config` are read-only, so pushes, remotes and git config stay with you; nested repositories, submodules and worktrees Claude creates stay in the sandbox |
| Network | the project's services directly; the internet only through a proxy in `zdev claude` on your machine, which logs every host; not your machine (`host.docker.internal` or any of its addresses), your local network or private networks |

zdev checks every request from the sandbox on your machine, where the container can't reach it. Claude gets a short system prompt that tells it all this, so it doesn't spend turns finding out.

**Config changes from the sandbox.** Everything that stays inside containers applies directly: images, Dockerfiles, environment, commands, new services, routes under your zdev domain. What reaches outside a container needs you, unless you applied it before:

- bind mounts outside the project, or writable ones of `.git/hooks` or `.git/config`
- 1Password Environments (`op-env`, `op-env://`) the service doesn't use yet
- router labels (`traefik.*`), host ports (`routing.host_port`, published on every interface), hostnames outside the zdev domain or of another project
- a Dockerfile outside the project, and the project's `name:` (never from the sandbox)
- `${VAR}` from your machine's environment that your own runs didn't read. From the sandbox those stay unsubstituted, so a config can't hand Claude your tokens through `zdev info` or a service's environment

zdev records what your own `zdev start`, `update` and `restart` applied (in `~/.zdev/agent/approved/`), and refuses the rest from the sandbox; Claude then asks you to review `.zdev/` and run `zdev update`. That run approves it, and it lists what it applies that the sandbox couldn't. `zdev claude` asks the same before it starts a project whose config changed since. `zdev down` and `zdev rename` drop the recorded approvals.

When the agent's own service changes (an update from Claude or from you: config, image or secrets), zdev replaces the sandbox at the end of Claude's turn and resumes the conversation in the new one, with a first message saying why. Claude uses `zdev exec app` for the new version until its turn ends. Sessions with `-p` aren't replaced.

**Internet access** goes through an HTTP proxy that `zdev claude` runs on your machine (the sandbox has no other route out). It refuses your machine's own addresses (public ones too, such as its global IPv6 address), the networks it sits on, and private networks, also when a public name resolves to them, and project domains (those are reached directly). Every connection is logged in `~/.zdev/agent/logs/<project>/`, and the session ends with a summary of hosts and refusals. `egress: enforce` limits a project to its `allow_hosts` (Claude's own hosts always work); that guards against mistakes, not against an attacker, who can still go through `zdev exec app`.

**Tokens and git: HTTPS only, the token stays on your machine.** You define a token once in host config (`agent.credentials`: the command, 1Password item or Environment, or owner-only file it comes from, the hosts it is for, the variables to set) and grant it per project ([Configuration](configuration.md#agent-sandboxes-agent)). `zdev claude` reads it when the session starts and keeps it in its own process. The sandbox gets a placeholder in those variables (e.g. `GH_TOKEN`) and a git credential helper that hands git the placeholder. For the token's hosts the proxy opens the TLS connection itself, with a certificate from a CA that exists only for this session, replaces the placeholder with the real token and forwards the request; every request is logged. Anywhere else the placeholder is a worthless string, so a prompt injection can't take the token with it. It can still use it through the proxy while the session runs, so grant tokens with the narrowest rights that do the job (a fine-grained GitHub token or a GitLab project access token for this one repository). `zdev agent setup` sets that up for the host of the project's `origin`, GitHub or GitLab (also self-hosted): on GitHub it opens the token page pre-filled and checks that the pasted token reaches the repository and no other private one of its owner; on GitLab, which can't pre-fill a project access token, it opens the project's token page with the values to enter and checks that the token reaches the project and nothing else. Then it stores the token and grants it to the project.

**No SSH from the sandbox.** The proxy refuses port 22, and git remotes on `git@` and `ssh://` URLs of GitHub, GitLab, Bitbucket and every credential host are rewritten to HTTPS in the sandbox (your `.git/config` stays as it is). Forwarding your SSH agent would be easy (the relay speaks SSH), but it would let the agent use your key for the whole session: on every repository and every server that accepts it, without anything to log or limit. An HTTPS token bound to one host and scoped to one repository is narrower. If a project needs SSH later (submodules or registries that only speak it), that is a new decision: a key of its own per project held by zdev, not your agent.

**Your Claude setup comes along:** skills, agents, commands, plugins, `CLAUDE.md`, settings and status line from `~/.claude` (read-only, so a session can't change them for every project), plus this project's memory. Top-level `~/.claude/*.sh` scripts come along too, so a status line or hook script works there as long as it only needs `jq`, `git` and what the image has: zdev puts static `jq`, `gh`, `glab`, `fd` and `rg` (pinned versions, checksums checked) last on the sandbox's PATH, so an image's own copies win. Sandboxes keep memory and conversations of their own, in `~/.zdev/agent/claude/projects/`: the first session copies the project's memory from `~/.claude/projects/`, and from then on neither side sees what the other writes. So `zdev claude --continue` continues the last sandbox conversation, never one you had with Claude on your Mac, and nothing an agent writes is loaded by Claude on your Mac. Plugins that only work on the host can be switched off for sandboxes with `agent.disable_plugins` ([Configuration](configuration.md#agent-sandboxes-agent)).

**Git hooks and config stay yours:** your machine runs `.git/hooks` and what git config names (`core.fsmonitor`, for one) without asking, and git reads config from more places than `.git/config`. So in a project you use `zdev claude` with, no container can change any of them: the sync leaves out `.git/hooks`, `.git/config`, `.git/commondir`, `.git/modules`, `.git/worktrees`, `.git/config.worktree`, and every `.git` below the project root (macOS). Services that mount the project directly (Linux, `mutagen.no_sync`) get the existing ones read-only; the first `zdev claude` recreates those services once for that. **On such a direct mount the protection is partial:** a container writes your project directory itself, so it can create `.git/commondir` or a nested repository, which git on your machine then reads. Don't run git in that project on your machine while a session may have changed it, or avoid `no_sync` on the project root.

**Git sync (macOS):** zdev syncs `.git` and `.zdev/` into the agent's service for the sandbox. When git writes the same file on your machine and in the sandbox at the same moment (your editor's background `git status` while Claude commits), Mutagen stops syncing it; `zdev status` says so, and `zdev mutagen repair-git` fixes it.

**Requirements:** `bash` and `git` in the service's image. When they're missing, `zdev claude` says exactly what to add for that image's package manager (apk, apt-get, dnf, …, switching to root for the install when the image runs as another user): a line for the service's Dockerfile, or, for a plain `image:`, a small `.zdev/<service>.Dockerfile` built `FROM` it. `zdev agent setup` checks the image up front and offers to make that change for you (it keeps the comments in `.zdev/config.yaml`); then `zdev update` builds it. An image without a package manager (distroless) can't get them: use another service. Run as the owner of the synced files: set `services.<name>.mutagen.user` to the app's user; without it the agent runs as root with no capabilities. A project where a service mounts the Docker socket is refused, since whoever reaches that service is root on your machine. That includes a directory holding it, such as `/var/run`, `~/.docker` (or the engine's own folder), your home directory or `/`; you can accept that per project in host config.

**A repository without `.zdev/`:** `zdev claude` starts a bootstrap session in a small image of zdev's own (bash, git, GNU tools) to set the project up. Afterwards run `zdev start` and continue with `zdev claude --continue` in the project's own image.

**Your terminal can know about the session:** `agent.host_hook` runs a command of yours on your machine when a session starts and ends, with nothing from the sandbox in it. The recipe for [cmux](https://cmux.com) gives the tab the Claude icon, as Claude on your Mac does ([Configuration](configuration.md#agent-sandboxes-agent)).

**Nothing piles up:** sandboxes go with their session (also after a crash, at the next session start), and so do their generated files. Agent builds and zdev's own volumes for sandboxes go a day after a newer one took over, or after 14 days unused; images of zdev's helper pins go when the pin moves on. Proxy logs are kept 30 days. A project's agent home (`zdev-agent-home-<project>`, holding the agents' settings in the sandbox) goes with `zdev down`.

**Settings** live only in `~/.zdev/global-config.yaml` (never in the project config, which the agent can edit): see [Configuration](configuration.md#agent-sandboxes-agent).

## Run pi sandboxed: `zdev pi`

`zdev pi` runs [pi](https://pi.dev) in the same sandbox as `zdev claude`: the same image choice, source, network, proxy, tokens, git rules and replacement of a stale sandbox (at the end of pi's turn, via a small zdev extension). Everything above applies; these are the differences.

```bash
zdev pi                        # pi in the app service's image
zdev pi --continue             # continue the last session (arguments go to pi unchanged)
zdev pi node -p "run the tests"  # in the node service's image, one prompt, then exit
```

**Log in once** with `/login` inside pi. Every sandbox shares that login (`~/.zdev/agent/pi/auth/auth.json`), and pi renews it there; several sandboxes at once take turns. pi's login waits for the browser to call back, which can't reach the container: paste the final redirect URL (or the code) into pi when it asks. To keep a token out of the sandbox altogether, use an API key or a Claude subscription token (`claude setup-token`) as a credential instead (`zdev agent credentials add --preset anthropic`). pi itself warns that a Claude subscription used through pi is billed as extra usage, per token, not from your plan's limits.

**pi builds:** pi publishes Linux builds for glibc only, so zdev builds pi itself from the newest release's source archive (checksum checked), for glibc and musl images alike, so both always run the same version. A build takes a few minutes on your machine and never holds up a session: `zdev agent setup pi` does the first one and waits for it; a `zdev pi` with nothing built starts it in the background and asks you to come back. zdev looks for a new release once a day and builds it in the background for the next session; `zdev agent update pi` builds it right away. The output of the last build is in `~/.zdev/agent/pi/build.log`. Only the two builds (about 130 MB each) stay: the downloads and npm's cache live in a volume of that build alone, and the Node image (about 350 MB) is removed again if the build pulled it. On a musl image without the C++ runtime (bare Alpine), pi gets it from its own build, for pi alone.

**Your pi setup comes along:** `skills`, `prompts`, `themes` and `extensions` from `~/.pi/agent` and the skills in `~/.agents/skills` (read-only), and `AGENTS.md`, `keybindings.json` and `models.json` copied in. From `models.json` only `"$NAME"` references come along as `apiKey` and header values: literal keys and `"!command"`s stay out, and so does a key in a URL (`https://user:key@host`, removed from `baseUrl`); zdev says which providers that affects. Give pi such a key as a zdev credential (`zdev agent credentials add`, with the provider's host and a variable) and write `"apiKey": "$THAT_VARIABLE"`; the proxy swaps it in, so the key never enters the sandbox. `settings.json` is merged: the sandbox's own (a model picked with `/model` sticks), then yours from the Mac on top, so a key set there applies in every project, e.g. `"warnings": {"anthropicExtraUsage": false}` against the extra usage warning. Projects are trusted in the sandbox (the container is the boundary). Sessions live in `~/.zdev/agent/pi/projects/`, apart from pi on your Mac. pi doesn't contact pi.dev from the sandbox: zdev chooses the version, and the build carries the model catalog.

**`egress: enforce`:** pi's own hosts are Anthropic's (its default provider and login). Another provider's API host goes into `allow_hosts`.

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
