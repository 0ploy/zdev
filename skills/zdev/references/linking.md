# Linking projects

Each zdev project has its own isolated Docker network, so by default two projects can't talk to
each other. When they need to (a monolith calling a microservice you're also developing locally,
split front-end/back-end, shared gateway), create a named **link** - a shared Docker network that
any project can join.

```bash
zdev link create backend              # Create a shared network (Docker: zdev_link_backend)
zdev link join backend api            # Attach all services of project "api"
zdev link join backend worker.app     # Or just one service - project.service notation
zdev link status backend              # Show members + running state
zdev link leave backend api           # Detach; zdev link delete backend tears down the network
```

## DNS on link networks

Across a link, containers are reachable by their fully-qualified name `<service>.<project>.zdev`
(e.g. `app.api.zdev`, `db.api.zdev`). The short service name (`app`, `db`) is a DNS alias zdev
only injects on *the project's own* network - on a link network multiple projects could have a
service called `app`, so always use the long name for cross-project calls.

## Cross-project calls are HTTP to the internal port, not HTTPS on 443

The public `https://*.0ploy.dev` domains resolve to `127.0.0.1`, which from inside a
container means the container's own loopback - not the host's Traefik. TLS termination and Traefik
routing only exist host-side. From one container to another, go direct:

```
http://app.api.zdev:8000      # ✓ cross-project via link (FQDN + internal port)
http://app:8000                # ✓ same project only (short alias)
https://api.0ploy.dev # ✗ resolves to 127.0.0.1, hits container's own loopback
```

When wiring one project's URL into another's env, use the internal port the target service listens
on (the value of its `routing.port`), not 443.

## Persistence

Link membership is stored in zdev state. `zdev start` auto-reconnects a project to every link
it's a member of - you don't re-run `zdev link join` after a restart, down, or reboot. `zdev
rename` migrates memberships to the new name too. `zdev down` disconnects the project from its
links (containers are gone); the link itself survives until `zdev link delete`.

## Member granularity

Joining a whole project attaches every service container; joining `project.service` attaches only
that one. Use per-service joins when exposing a narrow surface (e.g. only the API gateway, not the
DB) across projects.
