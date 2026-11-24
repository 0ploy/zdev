# Routing

## Multi-Service Routing

By default, all HTTP services in a project share the project's domain. For projects with multiple web services (frontend + backend, app + admin), you can assign each service its own domain using `routing.domain`:

```yaml
name: my-app

services:
  frontend:
    image: node:22-alpine
    command: pnpm dev --host 0.0.0.0
    working_dir: /app
    volumes:
      - ${PROJECTPATH}/frontend:/app
    routing:
      port: 3000
      # Uses project domain: my-app.0ploy.dev

  backend:
    image: node:22-alpine
    command: pnpm dev --host 0.0.0.0
    working_dir: /app
    volumes:
      - ${PROJECTPATH}/backend:/app
    routing:
      port: 4000
      domain: api.${PROJECTNAME}.${ZDEV_DOMAIN}
      # Uses custom domain: api.my-app.0ploy.dev
```

The `domain` field supports variable substitution and only applies to HTTP/HTTPS routing (not TCP/UDP). Nested domains like `api.my-app.0ploy.dev` get HTTPS too: zdev issues a per-project certificate for them on `zdev start` (see [Certificates](https-and-dns.md#certificates)).

## TCP/UDP Routing

Beyond HTTPS, zdev can expose raw TCP and UDP ports. This lets you connect to a database inside a project from your host using tools like DBeaver, pgAdmin, or `psql`:

```yaml
services:
  db:
    image: postgres:16-alpine
    environment:
      POSTGRES_PASSWORD: postgres
    routing:
      protocol: tcp
      port: 5432        # container port
      host_port: 5432   # exposed on localhost:5432
```

```bash
psql -h localhost -p 5432 -U postgres   # connect from your host
```

Multiple projects can expose different ports without conflicts. Works for MySQL, Redis, RabbitMQ, or any TCP/UDP service.

## Calling other projects from a container

A container can call another running project by the same URL you use in the browser, no link needed:

```bash
# From inside myscale, call data-core through Traefik:
curl https://datacore.0ploy.dev/api/health
```

The router joins every project network with each routed hostname of every registered project as a network alias, so Docker's DNS answers `<project>.0ploy.dev` with the router instead of the public record's `127.0.0.1`. The request takes the same path as in the browser: HTTPS, Traefik routing, the zdev certificate, which containers trust out of the box ([HTTPS from inside containers](https-and-dns.md#https-from-inside-containers)). TCP routes work the same way (`db.datacore.0ploy.dev:5432`). Shared service UIs too (`https://mail.shared.0ploy.dev`).

Requirements and limits:

- Both projects use the router (`shared.router: true`, the default), and the target has been started with zdev at least once (that registers it). After upgrading zdev, the first `zdev start` of any project picks up the hostnames of every registered project, no restarts needed.
- Containers created by an older zdev don't trust the zdev CA yet. `zdev start` says so ("runs with an outdated config"); `zdev update` recreates them.
- Only hostnames from project config resolve. A wildcard you route yourself (e.g. `*.shop.0ploy.dev` via custom labels) does not.
- When a project with new hostnames starts, zdev reconnects the router to the other project networks to add them (Docker can't change aliases in place). In-flight requests to those projects can fail for a moment.

To reach another project's internal port directly, without Traefik, use a [link](linking.md).

## Project Isolation

Each project runs in its own isolated network. Services within a project reach each other by name (`db`, `redis`, `app`), but projects can't see each other's services. The shared router bridges them to the outside, and to each other by their routed URLs (see above).
