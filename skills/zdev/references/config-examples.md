# Config Examples by Stack

## Node.js (Nuxt/Next/Vite)

```yaml
name: my-app

services:
  app:
    image: node:22-alpine
    command: corepack enable && pnpm install && pnpm dev --host 0.0.0.0
    working_dir: /app
    volumes:
      - ${PROJECTPATH}:/app
    routing:
      port: 3000

mutagen:
  ignore:
    - node_modules
    - .pnpm-store
    - .nuxt
    - .output
```

## PHP + MySQL (Laravel/Symfony/Shopware/Sylius)

```yaml
name: my-shop

variables:
  DB_PASSWORD: root
  DB_NAME: ${PROJECTNAME}

services:
  app:
    image: webdevops/php-nginx:8.3
    working_dir: /app
    volumes:
      - ${PROJECTPATH}:/app
    environment:
      WEB_DOCUMENT_ROOT: /app/public
      DATABASE_URL: mysql://root:${DB_PASSWORD}@db:3306/${DB_NAME}
      MAILER_DSN: smtp://mail:1025                  # Mailpit (see Email SMTP below)
      SYMFONY_TRUSTED_PROXIES: private_ranges       # REQUIRED for Symfony behind Traefik — see below
    routing:
      port: 80

  db:
    image: mysql:8.0
    volumes:
      - db_data:/var/lib/mysql
    environment:
      MYSQL_ROOT_PASSWORD: ${DB_PASSWORD}
      MYSQL_DATABASE: ${DB_NAME}

mutagen:
  ignore:
    - vendor
    - var/cache
    - var/log
    - public/build         # if the app has a Webpack/Vite asset pipeline
```

**Trusted proxies**: Traefik terminates HTTPS and forwards plain HTTP to the app. Symfony needs
`SYMFONY_TRUSTED_PROXIES=private_ranges` to trust the `X-Forwarded-Proto` header and generate
`https://` URLs. Laravel equivalent: `TRUSTED_PROXIES=*`. Without this, the debug toolbar hangs
on "Loading…" and any feature that generates an absolute URL (login redirect, asset manifests,
emails) breaks with mixed-content errors.

**Assets**: projects with Webpack Encore / Vite (Sylius 2.x, older Shopware, many themes) need
Node.js in the container and `npm run build` in setup. See `references/templates.md` →
"Runtime gotchas for PHP frameworks".

## PHP + MySQL + RabbitMQ (Symfony Messenger)

Shows three non-trivial patterns in one config: **multi-service HTTPS routing** with per-service
`routing.domain`, a **worker service** that shares the app's image + code but runs a different
long-running command, and **RabbitMQ** reachable from both the web and the worker by service
name.

```yaml
name: my-shop

variables:
  DB_PASSWORD: root
  DB_NAME: ${PROJECTNAME}
  RABBITMQ_USER: app
  RABBITMQ_PASSWORD: app

services:
  app:
    image: php:8.3-cli-alpine
    command: >-
      sh -c "composer install --no-interaction &&
      exec symfony server:start --no-tls --port=8000 --allow-all-ip"
    working_dir: /app
    volumes:
      - ${PROJECTPATH}:/app
    environment:
      DATABASE_URL: mysql://root:${DB_PASSWORD}@db:3306/${DB_NAME}?serverVersion=8.0&charset=utf8mb4
      MESSENGER_TRANSPORT_DSN: amqp://${RABBITMQ_USER}:${RABBITMQ_PASSWORD}@rabbitmq:5672/%2f/messages
      MAILER_DSN: smtp://mail:1025
      SYMFONY_TRUSTED_PROXIES: private_ranges
    routing:
      port: 8000                                        # https://my-shop.0ploy.dev

  worker:                                               # Long-lived consumer: same image + code, different command
    image: php:8.3-cli-alpine
    command: >-
      sh -c "composer install --no-interaction &&
      exec php bin/console messenger:consume async -vv"
    working_dir: /app
    volumes:
      - ${PROJECTPATH}:/app
    environment:
      DATABASE_URL: mysql://root:${DB_PASSWORD}@db:3306/${DB_NAME}?serverVersion=8.0&charset=utf8mb4
      MESSENGER_TRANSPORT_DSN: amqp://${RABBITMQ_USER}:${RABBITMQ_PASSWORD}@rabbitmq:5672/%2f/messages

  db:
    image: mysql:8.0
    volumes:
      - db_data:/var/lib/mysql
    environment:
      MYSQL_ROOT_PASSWORD: ${DB_PASSWORD}
      MYSQL_DATABASE: ${DB_NAME}

  rabbitmq:
    image: rabbitmq:3-management-alpine
    volumes:
      - rabbitmq_data:/var/lib/rabbitmq
    environment:
      RABBITMQ_DEFAULT_USER: ${RABBITMQ_USER}
      RABBITMQ_DEFAULT_PASS: ${RABBITMQ_PASSWORD}
    routing:
      port: 15672                                       # Management UI
      domain: rabbitmq.${PROJECTNAME}.${ZDEV_DOMAIN}   # https://rabbitmq.my-shop.0ploy.dev

mutagen:
  ignore:
    - vendor
    - var/cache
    - var/log
```

Key points:

- **Worker pattern**: same `image:` and bind-mounted `/app` as the app service, only the `command:`
  differs. Add more workers (email dispatcher, import processor) by duplicating and changing the
  command. Because workers hold a TTY-less long-running process, don't try to scaffold things in
  their command — keep scaffolding in `setup.just`.
- **Per-service routing domain** (see SKILL.md → "Multiple routed services in one project"): without
  `routing.domain` on `rabbitmq`, the management UI would collide with the app on the same host and
  only one would work.
- **Service-name DNS**: `app` and `worker` reach RabbitMQ at `rabbitmq:5672` and MySQL at `db:3306`
  inside the project's Docker network. No IPs, no `localhost`.
- **AMQP from the host**: if you need a desktop AMQP client, add a TCP routing entry:
  `routing: { protocol: tcp, port: 5672, host_port: 5672 }` (also works alongside the HTTP UI
  routing — use two separate services if needed, or omit the UI).

## Python + PostgreSQL (Django/FastAPI)

```yaml
name: my-api

variables:
  DB_PASSWORD: postgres

services:
  app:
    image: python:3.12-slim
    command: pip install -r requirements.txt && python manage.py runserver 0.0.0.0:8000
    working_dir: /app
    volumes:
      - ${PROJECTPATH}:/app
    environment:
      DATABASE_URL: postgres://postgres:${DB_PASSWORD}@db:5432/app
    routing:
      port: 8000

  db:
    image: postgres:16-alpine
    volumes:
      - db_data:/var/lib/postgresql/data
    environment:
      POSTGRES_PASSWORD: ${DB_PASSWORD}

mutagen:
  ignore:
    - __pycache__
    - .venv
    - "*.pyc"
```

## Custom Dev Image (`dockerfile:`)

When a stock image is missing system packages, extensions, or tooling, write a dev Dockerfile in
`.zdev/` and point the service at it — zdev builds it automatically on start and rebuilds when
the Dockerfile changes. The build context is always the project root (so `COPY` paths resolve
from there); the tag defaults to `zdev-<project>-<service>:latest`, or set `image:` to name it.

```yaml
name: my-app

services:
  app:
    dockerfile: .zdev/Dockerfile     # build this instead of pulling an image
    command: pnpm dev --host 0.0.0.0
    working_dir: /app
    volumes:
      - ${PROJECTPATH}:/app
    routing:
      port: 3000

mutagen:
  ignore:
    - node_modules
    - .pnpm-store
```

```dockerfile
# .zdev/Dockerfile — dev-only image: toolchain and system deps, no app code
# (source is bind-mounted at runtime)
FROM node:24-alpine
RUN apk add --no-cache openssl git
```

Dev containers only: build args, multi-stage targets, custom contexts, secrets, and registry push
are out of scope — for those, keep a custom build command (justfile) producing the `image:` tag.
Files the Dockerfile `COPY`s are not change-detected; after editing them run `zdev update --build`.

## Mutagen Ignore by Stack

| Stack | Always ignore |
|-------|--------------|
| Node.js/pnpm | `node_modules`, `.pnpm-store`, `.nuxt`, `.next`, `.output` |
| Node.js/npm | `node_modules` |
| PHP/Composer | `vendor`, `var/cache`, `var/log` |
| Python | `__pycache__`, `.venv`, `*.pyc` |
| General | Build output dirs, cache dirs, log dirs |

## Email SMTP Config by Stack

Configure your app to use zdev's shared Mailpit. From inside any project container, the hostname
is `mail` on port `1025` (no auth, no TLS) — identical across stacks:

- Node.js (nodemailer): `{ host: 'mail', port: 1025, secure: false }`
- PHP (Symfony/Sylius): `MAILER_DSN=smtp://mail:1025`
- PHP (Laravel): `MAIL_HOST=mail MAIL_PORT=1025 MAIL_ENCRYPTION=null`
- Python (Django): `EMAIL_HOST='mail' EMAIL_PORT=1025`

Web UI: `zdev mail` or `https://mail.shared.0ploy.dev`.
