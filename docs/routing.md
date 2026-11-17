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

## Project Isolation

Each project runs in its own isolated network. Services within a project reach each other by name (`db`, `redis`, `app`), but projects can't see each other's services. The shared router bridges them to the outside.
