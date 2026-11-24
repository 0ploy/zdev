# Linking projects

To call another project by its URL you don't need a link: `https://api.0ploy.dev` works from every
container through the router (`configuration.md`, Routing). Use a link only to reach another project's
internal port without Traefik, or services that aren't routed.

## Link networks

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

## Over a link, use the internal port

A link bypasses Traefik, so there is no TLS and no routing - call the container directly:

```
http://app.api.zdev:8000      # ✓ cross-project via link (FQDN + internal port)
http://app:8000                # ✓ same project only (short alias)
https://api.0ploy.dev          # ✓ via the router, no link needed
```

Over a link, use the internal port the target service listens on (its `routing.port`), not 443.

## Persistence

Link membership is stored in zdev state. `zdev start` auto-reconnects a project to every link
it's a member of - you don't re-run `zdev link join` after a restart, down, or reboot. `zdev
rename` migrates memberships to the new name too. `zdev down` disconnects the project from its
links (containers are gone); the link itself survives until `zdev link delete`.

## Member granularity

Joining a whole project attaches every service container; joining `project.service` attaches only
that one. Use per-service joins when exposing a narrow surface (e.g. only the API gateway, not the
DB) across projects.
