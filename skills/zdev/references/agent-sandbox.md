# Agent sandbox (`zdev claude`)

`zdev claude` runs Claude Code in a container of the project, built from a service's image (default `app`). Inside, `ZDEV_SANDBOX` is set. The session's system prompt describes the specifics; these are the rules that matter.

## When you run in a sandbox

- **zdev commands go to the host and are checked there.** Allowed for this project: `zdev exec`, `logs`, `status`, `info`, `start`, `stop`, `restart`, `update` (`--build`, `--refresh-secrets`). Everything else (`down`, `link`, `cleanup`, `rename`, `--config` to another project) is refused with `zdev: refused (<code>)` and exit 126. Relay the refusal to the developer; don't retry variations.
- **Edit `.zdev/` and apply it with `zdev update`** (`restart` keeps old containers). Applying is refused (`refused (policy)`, with the list) when the config reaches outside the project beyond what the developer applied before: binds outside the project or writable ones of `.git/hooks`/`.git/config`, other 1Password Environments, `traefik.*` labels, `routing.host_port`, hostnames outside the zdev domain or of other projects. Then ask the developer to review `.zdev/` and run `zdev update`. Never change `name:` (`refused (project-changed)`).
- **When `zdev update` changes your own service**, your container keeps its previous version (zdev says so): use `zdev exec app` for what needs the new one; the developer restarts the session with `zdev claude --continue`.
- **The app runs in its own container.** `ps` shows only your processes. Use `zdev exec app ps aux`, `zdev logs app`, `zdev restart app`, `zdev exec app <cmd>` for anything that must happen in the running app (supervised processes: `zdev exec app zpctl status`). A `zdev exec` that is interrupted or times out is stopped inside the app container too.
- **Build and test in the sandbox itself:** it has the service's toolchain. Don't start a second instance of the app: you share its data volumes.
- **`zdev run` runs here.** Jobs without `service` run in the sandbox, never on the developer's machine; jobs with `service = "x"` go to that container through the host. Jobs that need host tools (`open`, `op`, ssh keys) fail here.
- **Git only here, never through `zdev exec`.** `.git/hooks` and `.git/config` are read-only: commit and branch, but ask the developer for pushes, remotes and git config.
- **No tool installs:** no root privileges. Add missing tools to the service's Dockerfile and ask the developer to rebuild.
- **Network:** the project's services (by name or the project URL) and the internet; not the developer's machine or private networks.

## Bootstrap (repository without `.zdev/`)

The sandbox runs zdev's small bootstrap image; zdev commands are refused. Write `.zdev/config.yaml` (see `configuration.md`, `existing-project.md`), then ask the developer to run `zdev start` and `zdev claude --continue`.

## Helping a developer set it up

- Needs `bash` + `git` in the service's image.
- Set `services.<name>.mutagen.user` to the app's user, or the agent runs as root (without capabilities).
- Settings live only in `~/.zdev/global-config.yaml` under `agent:` (service per project, connectors, `host_access`, `disable_plugins`, `docker_socket: accept`), never in the project config.
- A service that mounts the Docker socket makes `zdev claude` refuse the project unless `agent.projects.<name>.docker_socket: accept`.
- Git sync conflict reported by `zdev status`: `zdev mutagen repair-git`.
