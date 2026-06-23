# Alternative Docker Backends

zdev talks to Docker exclusively through the `docker` CLI and the active docker
context (never the Docker SDK or a hardcoded daemon). That makes it engine
agnostic: anything that exposes a standard `docker` CLI + context + unix socket
works. This document records what we verified, how the common macOS engines
compare, and the one code change required to make zdev fully portable across
them.

## TL;DR

- zdev works with **Docker Desktop, OrbStack, and Colima** with no behavioral
  changes beyond resolving the Docker socket path (see "Required code change").
- On macOS there is **no native `dockerd`**. Every option runs `dockerd` +
  `containerd` + `runc` inside a lightweight Linux VM. The choice is about the
  VM manager and wrapper, not the engine.
- **OrbStack is the performance pick** (fastest bind mounts, lowest idle
  resource use). **Colima** is the fully-free, no-GUI, CLI-only pick.
- **Keep Mutagen auto-on for macOS on every engine.** A native volume still
  beats a bind mount everywhere; the penalty just varies (2x on OrbStack, up to
  ~8x on Docker Desktop with the Docker VMM beta).

## Why there is always a VM on macOS

`dockerd`, `containerd`, and `runc` are Linux binaries that need a Linux kernel.
On macOS, every engine provides that kernel via a VM. Docker Desktop's overhead
is not the engine; it is the Electron GUI, a fixed-size VM, and the vpnkit
network proxy. Lighter managers (OrbStack, Colima) run the same engine with a
leaner wrapper.

## Filesystem benchmark

Workload: `pnpm install` of a representative front-end dependency tree (16,099
files, ~185 MB `node_modules`), copied from a pre-warmed pnpm store so no network
I/O is timed. `package-import-method=copy` makes every scenario do identical work
except for the destination filesystem. Average of 2 runs each, same machine.

- **bind mount** = host directory mounted into the container = zdev **without**
  Mutagen.
- **named volume** = native ext4 inside the VM = zdev **with** Mutagen at
  runtime (Mutagen syncs into such a volume; `node_modules` / `.pnpm-store` are
  ignored and live only there).

| Engine / backend | bind mount (no Mutagen) | named volume (~ Mutagen) | boundary penalty |
| --- | --- | --- | --- |
| **OrbStack** | **1516 ms** | 761 ms | 2.0x |
| Docker Desktop - Apple Virtualization.framework | 3740 ms | 833 ms | 4.5x |
| Docker Desktop - Docker VMM (beta) | 5147 ms | 651 ms | 7.9x |

Reproduce with `scripts/fsbench.sh` (see below). Switch the active engine, then
re-run; output is auto-labeled by `docker context show`.

### Reading the results

- The **named-volume** column is the floor and is roughly constant across engines
  (~650-833 ms). That is expected (native ext4 in a VM) and confirms the
  methodology. All the signal is in the **bind-mount** column.
- **OrbStack bind mounts are 2.5x faster than Docker Desktop (Apple VZ)** and
  **3.4x faster than the Docker VMM beta.** OrbStack uses the same VirtioFS base
  as current Docker Desktop but adds a custom caching layer that cuts per-call
  overhead (their figure: up to ~10x), which is where the win comes from. Both
  use VirtioFS now, so the old "VirtioFS vs FUSE" framing is stale.
- **Docker VMM (beta) is a regression for file-bound work.** It made bind mounts
  ~38% slower than Apple VZ while making in-VM volume I/O slightly faster. Its
  host-to-VM file-sharing layer is clearly less mature than the one Docker tuned
  over Apple's framework. For zdev (which exercises the boundary heavily), prefer
  Apple Virtualization.framework over Docker VMM on Docker Desktop for now.
- **Mutagen never becomes redundant.** Even on OrbStack the native volume is 2x
  faster; on the Docker VMM beta the gap is ~8x. This is why the macOS
  Mutagen-on default stays correct on every engine.

Caveats: single machine, 2 runs, one bulk-write workload. It does not measure
Mutagen's own costs (background sync lag on incremental edits, the sync-ready
gate, the ownership chown step) or its known OrbStack agent-handshake bugs. It
also does not capture light edit/save/HMR cycles, where the gap is smaller.

## CPU, memory, and battery

The Docker engine is identical and cheap at idle. OrbStack's lower CPU / RAM /
power claims come from replacing the wrapper, not the engine:

- **GUI**: Docker Desktop ships an Electron (Chromium) app that burns CPU/RAM
  whether or not you use it. OrbStack's app is native (Swift) and minimal.
- **Memory**: Docker Desktop reserves a fixed VM memory budget at boot; OrbStack
  integrates with macOS memory management and releases RAM when idle.
- **Idle wakeups**: OrbStack quiesces to ~0.1% CPU; Docker Desktop's helpers keep
  ticking, which is what drains battery (prevents deep CPU sleep).
- **Networking**: Docker Desktop historically used vpnkit (userspace TCP/IP,
  CPU-heavy under load); OrbStack wrote a custom event-based stack.
- **x86 emulation**: QEMU (Docker Desktop's traditional default) vs Rosetta
  (OrbStack). Rosetta is far cheaper in CPU/heat when running amd64 images on
  Apple Silicon. Docker Desktop now offers Rosetta too, narrowing this.

Relevance to zdev: zdev keeps shared services (Traefik, Mailpit, Adminer, DB)
running all day, so the idle regime is exactly where these wrapper differences
compound.

Honesty note: the headline figures (e.g. 3-4x less RAM, ~70% less idle CPU) are
vendor / third-party-blog numbers, not independent controlled benchmarks. Treat
magnitudes as directional. Docker Desktop has also closed part of the gap
(Apple Virtualization.framework, VirtioFS, optional Rosetta).

## Headless / no-GUI options

### OrbStack (recommended for performance)
Effectively headless already: a tiny native menubar app plus the `orb` CLI.
Fastest bind mounts, lowest idle footprint. Requires a paid license for
commercial use (same as Docker Desktop). Socket: `~/.orbstack/run/docker.sock`.
With admin granted at install it also symlinks `/var/run/docker.sock`; without
admin it does not.

### Colima (recommended for fully-free, CLI-only)
No GUI process, open source, no license cost. Runs `dockerd` in a Lima-managed
VM.

```bash
brew install colima docker docker-compose   # docker here is just the CLI client
colima start --vm-type vz --mount-type virtiofs   # Apple Virtualization.framework + fast mounts (macOS 13+)
```

Registers a `colima` docker context automatically. Socket:
`~/.colima/default/docker.sock`. Trade-offs: slower than OrbStack (virtiofs is
less tuned), and there are open virtiofs bind-mount edge cases. Keep Mutagen on -
it matters more here than on OrbStack.

### Podman
Free, rootless, Docker-API-compatible socket via `podman machine` +
`podman system service`, but it is `podman`/`crun`, not `dockerd`. Architecturally
aligned with zdev's "keep the Podman door open" stance, but a different engine,
not a drop-in dockerd. Not yet validated as a zdev backend.

### Apple `container` (2025)
Native macOS, one micro-VM per container. Young, not a Docker-API drop-in. Not
ready as a zdev backend.

## Required code change: resolve the Docker socket path

The only Docker-Desktop-specific assumption in the codebase is the **hardcoded
host-side `/var/run/docker.sock`** bind-mount source used by the Traefik router
(`internal/services/router.go`) and Dozzle (`internal/services/logs.go`). That
host path only exists on OrbStack/Colima when a symlink happens to be present
(OrbStack creates it only with admin). The real socket lives at the
context-specific path.

The fix resolves the active context's real socket path
(`docker context inspect` endpoint, or `DOCKER_HOST`) and falls back to
`/var/run/docker.sock`. The container-side target stays `/var/run/docker.sock`
(what Traefik and Dozzle expect internally). This single change makes zdev work
cleanly on Docker Desktop, OrbStack, and Colima alike.

## Engine detection signal

If we ever need to detect the engine: OrbStack reports `Name: orbstack` and
`OperatingSystem: OrbStack` in `docker info`; Docker Desktop reports
`docker-desktop` / `Docker Desktop`. Do not string-match "Docker Desktop" in
`docker version` output - OrbStack runs an unmodified upstream engine and reports
a plain upstream version.

## Benchmark script

`scripts/fsbench.sh` runs the comparison above against whatever engine the active
docker context points at. Run it on each engine and compare the bind-mount rows.
