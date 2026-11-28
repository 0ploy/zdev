# Debugging zdev projects

Start with `zdev status` (services, stale file mounts, shadowed justfiles, shared services) and `zdev logs -f <service>`.

**Container crashes:** `zdev logs -f app` to see why. `zdev restart app` bounces just that one container; `zdev restart` does the whole project. `zdev down && zdev start` for a full clean restart.

**Container exits immediately with `Exited (2)` and `sh: 0: Illegal option --`:** `command:` runs through `sh -c`; bare `--flags` meant for the image's entrypoint break it. Call the binary explicitly (`exec mariadbd --flag`) or mount a config file.

**Config changes aren't taking effect:** `zdev restart` (with or without a service name) just stops+starts the existing container, so edits to `environment:`, `image:`, `command:`, routing, or volume mounts don't apply. Use `zdev update` - it diffs the config against the running containers, recreates only the services that actually changed, and removes services deleted or renamed in the config. Code changes are live via bind mount / Mutagen - no restart or update needed.

**Secrets changed in 1Password but the container has old values:** `zdev update --refresh-secrets` (restart and plain update don't contact 1Password). More in `secrets.md`.

**Dockerfile changes aren't taking effect:** `zdev update` (or `start`) detects edits to the Dockerfile itself and rebuilds. But files the Dockerfile `COPY`s are NOT hashed - after changing those, run `zdev update --build` to force the rebuild.

**A shared service runs an older version than zdev's default** (e.g. the Traefik dashboard shows an old version): `~/.zdev/global-config.yaml` pins its image, often a line an older zdev wrote there. `zdev status` and `zdev services status` warn about such lines; deleting them is the developer's call (the file is theirs, and outside a sandbox's reach).

**Redirects to the docs page:** either the container isn't running or `routing.port` doesn't match the app's port (or the dev server listens on 127.0.0.1 instead of 0.0.0.0). For shared service UIs (mail, db, redis), also check `zdev services status` - the service needs to be running AND the project must have the corresponding `shared.*` option enabled.

**`https://*.0ploy.dev` won't resolve at all (DNS verification failed on `zdev start`):** the network's router likely enforces DNS rebinding protection - it drops the wildcard record's `127.0.0.1` answer. Run `zdev dns enable` (or let `zdev systemcheck` offer it): a local dnsmasq container answers the zdev domain and the OS is pointed at it for that domain only (one sudo prompt; `/etc/resolver` on macOS, systemd-resolved on Linux). `zdev dns status` / `zdev dns disable` manage it. Covers the global zdev domain and all subdomains; a project that overrides `domain:` to a different base domain is not covered. Once enabled it behaves as a shared service, so `zdev services stop` also stops name resolution until the next start - `zdev dns disable` switches it off for good.

**File sync issues (macOS):** `zdev mutagen status` / `zdev mutagen flush` / `zdev mutagen reset`. A service whose sync couldn't be established is held at a sync-ready gate (container alive, app not started); fix the cause and `zdev start` again. Conflicts (a file changed on the host and in the container) are named by `zdev start` and `zdev mutagen status`; `zdev mutagen reset` recreates the session with the host's versions (container-only files are kept), the same reconcile zdev runs whenever it recreates a session. A directory that is empty only at container startup (init scripts, nginx config) needs `mutagen.no_sync` (`configuration.md`).

**A single mounted file looks right on the host but stale in the container:** the editor replaced the file and the bind mount still points at the deleted inode. `zdev status` lists it under "Stale file mounts"; restart that service.

**DB connection refused:** use the service name (`db`), not `localhost`. Example: `postgres://postgres:postgres@db:5432/app`.

**Can't reach `https://other-project.0ploy.dev` from inside a container:** the router carries every registered routed hostname as an alias on each project network, so this normally resolves to Traefik. Check in order:
- `getent hosts other-project.0ploy.dev` in the container. `127.0.0.1` means the name isn't an alias: the target was never started with zdev (not registered), isn't routed (`shared.router` off), or the host is a wildcard from custom labels (not covered). `zdev start` the target, then the caller.
- Certificate error: the client doesn't trust the zdev CA. zdev mounts a bundle with it over the system CA files and sets `NODE_EXTRA_CA_CERTS`/`REQUESTS_CA_BUNDLE` (`configuration.md`, Routing). `ls /etc/zdev` empty or missing: `zdev update` to recreate the container. A user mount or env var on those paths overrides zdev's. Java needs a `keytool` import.
- To bypass Traefik entirely, use a link and `http://<service>.<project>.zdev:<internal-port>`.

**Symfony/Sylius behind Traefik - stuck "Loading…" debug toolbar, broken admin login, mixed-content errors:** Traefik terminates TLS and forwards HTTP to the app. Without trusted-proxy config, Symfony generates `http://` URLs inside the HTTPS page and the browser blocks them. Fix: `SYMFONY_TRUSTED_PROXIES: private_ranges` in the app service `environment:`, then `zdev update`. Laravel: `TRUSTED_PROXIES=*`.

**A project command fails or is missing:** `zdev run` lists broken job files with the reason; `zdev run --show <cmd> [job]` shows what would run. A container job needs its service running. A `.zdev/commands/<name>.just` named like a built-in (`db`, `logs`) never runs - `zdev status` flags it; convert with `zdev run --convert-just`.

**`curl 200 OK` isn't enough for HTML apps:** mixed-content, CSP failures, and JS errors are browser-only failure modes. After a web change, open the project in a browser (e.g. the chrome-devtools MCP tools: `new_page`, `list_console_messages`, `list_network_requests`) and confirm the console is clean and all requests are `https://`.

**Why `.pnpm-store` must be ignored:** pnpm creates a ~500MB platform-specific store. If synced, wrong binaries (glibc vs musl) break the container on image changes.
