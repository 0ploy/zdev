# Stack Gotchas

Runtime behaviors that bite when running a language/framework stack inside an zdev container.
These apply to **any** zdev project — scaffolded from a template or added to an existing repo.
For template-authoring patterns (`.setup-complete`, scaffolding, `setup.just`), see `templates.md`.

## Node.js / pnpm

- Set `COREPACK_ENABLE_DOWNLOAD_PROMPT: "0"` in config.yaml environment (not in each command).
  Without it, `corepack enable` prompts interactively in the no-TTY entrypoint and hangs.
- Native module build scripts must be approved. pnpm blocks them by default — and in a **non-TTY
  boot path** (entrypoint/`entrypoint.d`) it can't prompt, so pnpm v11 **exits non-zero**
  (`ERR_PNPM_IGNORED_BUILDS`), which fails a `set -e` script and aborts the container. So at boot use
  `pnpm install --config.dangerouslyAllowAllBuilds=true` (native modules like `esbuild`,
  `better-sqlite3`, `@parcel/watcher` need this). In an interactive `setup.just` (with a TTY via
  `zdev exec`), the equivalent is `pnpm approve-builds --all` after install.
- Scaffolding with `nuxi init` (create-nuxt): a non-TTY requires an explicit `-t/--template`
  (e.g. `--template minimal`) — without it, it errors "Missing required argument: --template".
  Use `--no-install` when scaffolding at create time (install happens at boot).
- `HOST: "0.0.0.0"` in environment so dev server is accessible from outside the container.
  Otherwise Traefik gets connection-refused — the dev server only bound to loopback.
- Add to mutagen ignore: `node_modules`, `.pnpm-store`, `.zdev`, `.setup-complete`.
  `.pnpm-store` especially — it's a ~500 MB content-addressable store with platform-specific native
  binaries. Syncing it breaks the container when the image changes (glibc vs musl mismatch).
- Framework build artifacts to ignore: `.nuxt`, `.output` (Nuxt), `.next` (Next.js).
- File watching: Node 22+ has `node --watch`; frameworks have their own HMR.

## PHP / Composer

- `php:8.3-cli-alpine` doesn't include Composer or Symfony CLI — install at runtime in the
  entrypoint (guard with `command -v` so restarts don't reinstall):
  ```sh
  wget -qO- https://getcomposer.org/installer | php -- --install-dir=/usr/local/bin --filename=composer
  wget -q https://get.symfony.com/cli/installer -O - | bash
  cp $HOME/.symfony5/bin/symfony /usr/local/bin/symfony
  ```
- Install tools to `/usr/local/bin` so they're available in subsequent `zdev exec` calls.
- Symfony dev server: `symfony server:start --no-tls --port=8000 --allow-all-ip`.
  `--no-tls` because zdev terminates TLS via Traefik; `--allow-all-ip` binds to `0.0.0.0`.
- Add to mutagen ignore: `vendor`, `var`, `.zdev`, `.setup-complete`.

## PHP frameworks (Symfony / Sylius / Shopware / Laravel / Akeneo) — runtime gotchas

The five landmines below are the most common causes of "container runs, browser shows a wrong
page" on PHP framework projects. Worth applying proactively, not just when debugging.

### 1. `memory_limit=-1`

The PHP CLI default is 128 MB, which OOMs on Symfony's `cache:clear` post-install script, Composer
dependency solving on large projects, and anything that loads the full Symfony container. Drop in
a php.ini fragment:

```sh
printf 'memory_limit=-1\n' > /usr/local/etc/php/conf.d/zz-app.ini
```

Guard with `[ ! -f /usr/local/etc/php/conf.d/zz-app.ini ]` in the entrypoint so restarts don't
re-create the file.

### 2. `SYMFONY_TRUSTED_PROXIES: private_ranges`

Traefik terminates HTTPS and forwards plain HTTP to the app. Without a trusted-proxy config,
Symfony can't tell the outer request was HTTPS and generates `http://` URLs inside the HTTPS
page — the browser blocks them as mixed content. Symptoms: Web Debug Toolbar stuck on "Loading…",
admin login bounces, asset manifest URLs 404, password-reset emails link to `http://`.

Set this in the app service environment for any Symfony / Sylius / Shopware project:

```yaml
environment:
  SYMFONY_TRUSTED_PROXIES: private_ranges   # Symfony 6.3+ shorthand for RFC1918 + 127.0.0.1
```

Laravel equivalent: `TRUSTED_PROXIES=*` for the TrustProxies middleware. Any framework that builds
absolute URLs behind a reverse proxy needs similar awareness.

### 3. Missing PHP extensions

`php:8.3-cli-alpine` ships a minimal set. Sylius, Shopware, Akeneo and similar need at least
`intl pdo_mysql gd bcmath opcache exif zip`. Bake them into a dev image with `dockerfile:` -
zdev builds it automatically, and the extensions survive container recreates instead of being
reinstalled by the entrypoint every time:

```yaml
# .zdev/config.yaml
services:
  app:
    dockerfile: .zdev/Dockerfile
```

```dockerfile
# .zdev/Dockerfile
FROM php:8.3-cli-alpine
ADD --chmod=755 https://github.com/mlocati/docker-php-extension-installer/releases/latest/download/install-php-extensions /usr/local/bin/
RUN install-php-extensions intl pdo_mysql gd bcmath opcache exif zip
```

([install-php-extensions](https://github.com/mlocati/docker-php-extension-installer) resolves
each extension's build deps automatically.)

### 4. Asset pipelines need Node

Projects with `package.json` + `webpack.config.js` / `vite.config.js` (Sylius 2.x via Webpack
Encore, Shopware 6 admin, custom themes) need Node.js in the app container. Add `apk add --no-cache
nodejs npm`, run `npm install && npm run build` at setup, and include an idempotent rebuild in the
entrypoint:

```sh
if [ -f package.json ] && [ ! -f public/build/shop/manifest.json ]; then
  npm install --no-audit --no-fund && npm run build;
fi
```

The entrypoint check matters because `public/build` typically lives in `mutagen.ignore` (binary +
regenerable), so it's lost on `zdev down && zdev start`. Without the rebuild, the first page
load 500s on a missing `manifest.json`.

### 5. Mail to Mailpit

```yaml
environment:
  MAILER_DSN: "smtp://mail:1025"     # Symfony/Sylius
  # MAIL_HOST=mail MAIL_PORT=1025    # Laravel
```

Identical for every stack — no auth, no TLS. See `config-examples.md` for the full table.

## Custom dev images (`dockerfile:`) — rebuild staleness

- Build staleness is a hash of the **Dockerfile contents only**, not the build context. Editing a
  file the Dockerfile `COPY`s into the image (baked config, e.g. `.zdev/zpinit/entrypoint.d/*`,
  zpinit `services/*.toml`) does NOT trigger a rebuild on the next `zdev start`. Force it with
  `zdev start --build` (or `zdev update --build`). A fresh `zdev create` builds from scratch, so a
  user creating a new project never hits this — it bites template authors iterating on baked config.
- App **source** is bind-mounted at runtime, not `COPY`d, so source edits never need a rebuild —
  that's the reason build staleness ignores the context. Keep dev images to toolchain/system deps +
  baked config; never `COPY` app source into a `dockerfile:` dev image.
- **Sync-ready gate is only auto-injected around a config `command:`.** zdev wraps a `command:` with
  a `while [ ! -f /.zdev-sync-ready ]` wait so the app doesn't start before Mutagen's initial sync.
  A `dockerfile:` image that runs via `ENTRYPOINT`/`CMD` (no config `command:`) does NOT get that
  wrap — so a boot step that reads synced files (e.g. `pnpm install` needing `package.json`) must
  wait for `/.zdev-sync-ready` itself. zdev still touches the marker after the first flush regardless.
