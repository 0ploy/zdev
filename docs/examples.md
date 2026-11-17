# Config examples

Complete `.zdev/config.yaml` files for common stacks. Field reference: [configuration.md](configuration.md).

## PHP + MySQL

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
      MAILER_DSN: smtp://mail:1025            # catch outgoing mail in Mailpit
      SYMFONY_TRUSTED_PROXIES: private_ranges # Symfony/Sylius behind Traefik (see troubleshooting.md)
    routing:
      port: 80

  db:
    image: mysql:8.0
    volumes:
      - db_data:/var/lib/mysql
    environment:
      MYSQL_ROOT_PASSWORD: ${DB_PASSWORD}
      MYSQL_DATABASE: ${DB_NAME}
```

## Node.js + PostgreSQL

```yaml
name: my-api

services:
  app:
    image: node:22-alpine
    command: corepack enable && pnpm install && pnpm dev --host 0.0.0.0
    working_dir: /app
    volumes:
      - ${PROJECTPATH}:/app
    environment:
      DATABASE_URL: postgres://postgres:postgres@db:5432/app
    routing:
      port: 3000

  db:
    image: postgres:16-alpine
    volumes:
      - db_data:/var/lib/postgresql/data
    environment:
      POSTGRES_PASSWORD: postgres
      POSTGRES_DB: app

mutagen:
  ignore:
    - node_modules
    - .pnpm-store
    - .nuxt
```

## Static Site / Frontend

```yaml
name: my-docs

services:
  app:
    image: node:22-alpine
    command: corepack enable && pnpm install && pnpm dev --host 0.0.0.0
    working_dir: /app
    volumes:
      - ${PROJECTPATH}:/app
    routing:
      port: 5173

mutagen:
  ignore:
    - node_modules
    - .pnpm-store
```
