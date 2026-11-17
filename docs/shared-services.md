# Shared services

These run once and are shared across all your projects. No per-project configuration needed.

| Service | URL | What it does |
|---------|-----|--------------|
| Router | `https://router.shared.0ploy.dev` | Routing dashboard - see all routes |
| Mail | `https://mail.shared.0ploy.dev` | Catches all outgoing email ([Mailpit](https://github.com/axllent/mailpit)) |
| DB | `https://db.shared.0ploy.dev` | Browse any project's database ([Adminer](https://www.adminer.org/)) |
| Redis | `https://redis.shared.0ploy.dev` | Inspect Redis keys and data ([Redis Insights](https://redis.io/insight/)) |
| Logs | `https://logs.shared.0ploy.dev` | Tail container logs across all projects ([Dozzle](https://dozzle.dev/)), grouped per project |

**Connecting from your app containers:** Configure your app to send mail to `mail:1025` (SMTP, no auth). For databases and Redis, use your project's own service names (e.g., `db:5432`, `redis:6379`) - Adminer and Redis Insights are browser UIs, not the services themselves.

**Log retention:** Dozzle is a viewer, not a store. Logs come from Docker's per-container ring buffer (default in Docker Desktop: ~50 MB rotated, ~5 files), so they survive `zdev down` and Docker Desktop restarts but are **lost when a container is recreated** - that includes `zdev update` (on config drift), `zdev remove`, and `zdev services recreate`. To grow the per-container buffer, set `log-opts: { max-size, max-file }` in Docker Desktop's daemon JSON. Persistent log history across recreates needs a separate log shipper (Loki, Vector, etc.) and is out of scope for zdev.

**Per-project visibility:** Dozzle only shows containers from projects that opt in via `shared.logs: true`. Projects without it stay hidden, even though Dozzle has full Docker socket access. Shared service containers (router, mail, db, redis, logs) are always visible.

**In-container shell is off by default.** Dozzle can open a shell into any container it sees, but the router publishes ports on all interfaces, so on an untrusted network (shared office, coffee-shop Wi-Fi) that shell is reachable by anyone who can hit the host. Enable it only on a network you trust by setting `shared.logs.shell: true` in `~/.zdev/global-config.yaml` (changes take effect on the next `zdev services recreate` or `zdev start`).

Open them directly:

```bash
zdev mail        # open Mailpit
zdev db          # open Adminer
zdev redis       # open Redis Insights
zdev logs --open # open Dozzle log viewer
```

**Per-project opt-in/out.** `router`, `mail`, and `logs` are connected to every project by default; `db` and `redis` are opt-in. Override individual fields under `shared:` in `.zdev/config.yaml`; missing fields keep their defaults.

```yaml
# .zdev/config.yaml
shared:
  db: true       # opt in  (default: false)
  redis: true    # opt in  (default: false)
  mail: false    # opt out (default: true)
  # router, logs not listed -> stay at default true
```
