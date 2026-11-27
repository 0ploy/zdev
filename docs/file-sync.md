# Fast file sync (macOS)

File sharing between your host and containers is notoriously slow on macOS. zdev automatically syncs files at native speed - no configuration needed. On Linux this isn't needed (already fast).

How much difference does it make? We benchmarked a Nuxt 4 app with ~1000 dependencies:

| Approach | pnpm install | Cold start to app ready |
|----------|-------------|------------------------|
| Docker bind mount (default macOS) | **34.6s** | ~42s |
| zdev with file sync | **6.7s** | ~17s |
| zdev warm restart (stop + start) | **2.4s** | ~2s |

That's a **5x speedup** on cold start and **instant** warm restarts. The trick: zdev syncs your source code via fast file sync, while keeping `node_modules` and other generated files inside the container where filesystem operations are native speed.

This holds regardless of Docker engine. Even on OrbStack, whose bind mounts are much faster than Docker Desktop's, writing `node_modules` to a native volume is still ~2x faster than to a bind mount - so file sync stays on by default on every macOS engine. See [ALTERNATIVE_BACKENDS.md](../ALTERNATIVE_BACKENDS.md) for the cross-engine numbers.

Exclude paths you don't need synced back to the host:

```yaml
mutagen:
  ignore:
    - node_modules
    - .pnpm-store    # pnpm's content-addressable store - platform-specific, don't sync
    - .nuxt
    - .output
    - var/cache
```

**Important:** Always add `.pnpm-store` to the ignore list for pnpm projects. pnpm creates its package store inside the project directory when running in a container. Without ignoring it, ~500MB of platform-specific binaries sync to the host, causing slow syncs and broken native modules when switching images.

## Commands

```bash
zdev mutagen status  # Check sync status
zdev mutagen flush   # Wait for sync to complete
zdev mutagen reset   # Recreate sync sessions (if stuck)
```

**When a session is (re)created, the host wins.** zdev recreates a session when its config changes (ignores, ownership, sync mode) and on `zdev mutagen reset`. The container volume still holds the old session's files then, and a brand-new session has no shared history, so every file that differs on the two sides would be a conflict two-way-safe never resolves. zdev therefore first reconciles: a short pass in which the host's version wins wherever the two differ, then the configured session from identical trees. Files only the container has are kept and copied to the host. If a session still ends up with conflicts, `zdev start` names the files; `zdev mutagen reset` settles them in the host's favor.

`zdev mutagen status` reports one line per synced directory, with the state of that mount's own sync session.

Directories that must not be synced (`mutagen.no_sync`) and per-service file ownership (`mutagen.user`, `group`, modes) are covered in [configuration.md](configuration.md#volumes) and the [service fields](configuration.md#service-fields-servicesname).
