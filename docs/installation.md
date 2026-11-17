# Installation

## Requirements

A Docker engine. zdev talks to Docker through the `docker` CLI and the active docker context, so it works with [Docker Desktop](https://www.docker.com/products/docker-desktop/), [OrbStack](https://orbstack.dev/), or [Colima](https://colima.run/) on macOS, and Docker Engine on Linux. On macOS, **OrbStack** is the fastest option and **Colima** is a free, no-GUI alternative - see [ALTERNATIVE_BACKENDS.md](../ALTERNATIVE_BACKENDS.md) for a benchmark and trade-offs.

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/0ploy/zdev/main/install.sh | sh
zdev systemcheck
```

`zdev systemcheck` installs the local SSL certificates and starts the [shared services](shared-services.md) (router, mail catcher, DB browser). If your router blocks the `*.0ploy.dev` wildcard, it offers the [DNS fallback](https-and-dns.md).

## Updates

The installer places the real binary at `~/.zdev/bin/zdev` and symlinks it into `/usr/local/bin` (one-time sudo prompt). After that, zdev checks GitHub at most once per 24 hours during command startup. When a new release exists it announces the update, downloads and verifies it synchronously, then installs it into the user-owned canonical path. The current command keeps running its in-memory version; the next invocation uses the new binary.

Set `ZDEV_NO_UPDATE_CHECK=1` to disable. `zdev self-update` still works for on-demand updates and auto-migrates legacy installs to the symlink layout.

## Agent skill

```bash
npx skills add 0ploy/zdev
```

What it teaches your coding agent and how to get the most out of zdev with agents: [Agents](agents.md).
