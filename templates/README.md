# Creating zdev Templates

Templates let users scaffold new projects with `zdev create`. A template is a GitHub repository or local directory that contains an `.zdev/` configuration and optionally starter files.

```bash
zdev create myorg/my-template my-app   # Any GitHub repo
zdev create express my-app             # Shorthand for 0ploy/zdev-template-express
zdev create ./local-dir my-app         # Local directory (for development/testing)
```

---

> [!TIP]
> ## 🤖  Let Claude Code help you build the template
>
> **Install the `zdev` skill** and Claude Code can drive the whole template-authoring workflow for you — picking images, writing `config.yaml`, scaffolding `setup.just`, testing the run, and iterating when something breaks.
>
> ```bash
> npx skills add 0ploy/zdev
> ```
>
> Then start a Claude Code session and say something like *"create an zdev template for Foo"*. The skill triggers automatically on template-authoring phrases and on any `.zdev/config.yaml` or `setup.just` question.

---

## What's in a template

Every template has the same base structure:

```
my-template/
  .zdev/
    config.yaml              # Container configuration (image, ports, volumes, etc.)
    commands/
      setup.just             # Setup script (install deps, scaffold project)
  README.md                  # Usage instructions
```

Beyond this, a template may or may not include app source files. This depends on the framework:

**Include source files** when there's no scaffolding command. Example: an Express template ships with `app.js` and `package.json` because Express has no `create` command - you just write files and install dependencies.

**Don't include source files** when the framework has its own scaffolding. Example: a Nuxt template ships only `.zdev/` because `nuxi init` generates all app files. A Symfony template ships only `.zdev/` because `symfony new` does the same. Including source files that the scaffolder also creates would cause conflicts.

## The setup lifecycle

After `zdev create`, the user's workflow is:

```bash
zdev create <template> my-app
cd my-app
zdev setup
```

`zdev setup` runs the `setup.just` file which handles everything: starting containers, installing dependencies, scaffolding the project (if needed), and signaling that the app is ready to run.

### Why setup.just is needed

Templates need a setup step because the container alone isn't enough. Dependencies must be installed, frameworks may need scaffolding, and the app needs to be configured before it can serve requests. Without a setup step, the container would start but have nothing to run.

The setup justfile runs on the **host** and uses `zdev exec` to run commands inside the container. This is important because `zdev exec` provides an interactive terminal - the user can respond to prompts from package managers and scaffolding tools. The container entrypoint has no terminal, so interactive prompts would crash there.

To learn how to write setup files, see [Writing setup.just](#writing-setupjust).

### Why .setup-complete is needed

There's a circular dependency between the container and setup:
- The container must be running for `zdev exec` to work (setup needs the container)
- But the app can't start until setup finishes (the container needs setup)

The `.setup-complete` marker file solves this. The container entrypoint checks for it:

```yaml
command: >-
  sh -c "
  if [ ! -f .setup-complete ]; then
    echo 'Waiting for setup... Run: zdev setup';
    while [ ! -f .setup-complete ]; do sleep 2; done;
  fi;
  pnpm install && exec pnpm dev"
```

1. **First start** - no `.setup-complete` exists. The container enters a wait loop, staying alive without crashing. Now `zdev exec` works.
2. **Setup runs** - installs deps, scaffolds if needed, then `touch .setup-complete`. The wait loop detects the marker and starts the app.
3. **On restart** - `.setup-complete` exists, so the container skips the wait loop, runs dependency install (to pick up any new packages), and starts the app immediately.

The dependency install command (`pnpm install`, `composer install`, etc.) must be in the entrypoint so that restarting the container after adding a new package works without re-running setup.

## Writing config.yaml

The config defines your containers, routing, and sync settings:

```yaml
version: 1
name: ${PROJECTDIR}
domain: ${PROJECTNAME}.${ZDEV_DOMAIN}

info: |
  ## ${PROJECTNAME}
  
  Description of the project.
  Run `zdev setup` to get started.

variables:                    # reusable values for ${VAR} substitution in this file
  DB_PASSWORD: root
  DB_NAME: ${PROJECTNAME}

shared:                       # connect shared services to project's docker network
                              # router/mail/logs default to true; db/redis default to false
                              # missing fields keep their defaults, so list only what you want to change
  router: true                # default: true  - shared Traefik router (HTTPS, subdomains)
  mail: true                  # default: true  - Mailpit (SMTP at mail:1025)
  db: true                    # default: false - Adminer database browser
  redis: true                 # default: false - Redis Insights
  logs: true                  # default: true  - Dozzle log viewer (per-project grouping)

environment:                  # env vars passed to ALL containers
  APP_ENV: dev

services:
  app:
    image: node:22-alpine
    command: >-
      sh -c "corepack enable &&
      if [ ! -f .setup-complete ]; then
        echo 'Waiting for setup... Run: zdev setup';
        while [ ! -f .setup-complete ]; do sleep 2; done;
      fi;
      pnpm install && exec pnpm dev"
    working_dir: /app
    volumes:
      - ${PROJECTPATH}:/app
    environment:                # env vars passed to THIS container only
      NODE_ENV: development
      COREPACK_ENABLE_DOWNLOAD_PROMPT: "0"
      HOST: "0.0.0.0"
      PORT: "3000"
      DATABASE_URL: mysql://root:${DB_PASSWORD}@db:3306/${DB_NAME}
    routing:
      port: 3000
      # domain: api.${PROJECTNAME}.${ZDEV_DOMAIN}  # optional: custom domain for this service

  db:
    image: mysql:8.0
    volumes:
      - db_data:/var/lib/mysql
    environment:
      MYSQL_ROOT_PASSWORD: ${DB_PASSWORD}
      MYSQL_DATABASE: ${DB_NAME}

mutagen:
  ignore:
    - node_modules
    - .pnpm-store
    - .zdev
    - .setup-complete
```

### Variables vs environment

**`variables`** define reusable `${VAR}` placeholders that are substituted throughout the config file. They are not passed to containers. Use them to avoid duplicating values like database passwords across services.

**`environment`** (project-level) defines environment variables passed to ALL containers. **`services.<name>.environment`** (service-level) defines environment variables for that specific container only, and overrides project-level env vars with the same name.

Built-in variables are always available: `${PROJECTDIR}`, `${PROJECTPATH}`, `${PROJECTNAME}`, `${ZDEV_DOMAIN}`, `${ZDEV_HOME}`, `${USER}`, `${HOME}`, plus all host environment variables. User-defined `variables` can reference built-in ones (e.g. `DB_NAME: ${PROJECTNAME}_db`).

**Local overrides:** if `.zdev/local/config.yaml` exists, it deep-merges on top of the committed `config.yaml` before substitution. Templates should add `.zdev/local/` to `.gitignore` so per-developer secrets and tweaks (e.g. `variables: { STRIPE_KEY: ... }`) stay off the repo. Maps merge recursively; scalars and slices in the local file replace the base value.

**Dev server binding:** Dev servers typically listen on `localhost` by default, which isn't accessible from outside the container. Set `HOST=0.0.0.0` (or the framework's equivalent) in the environment so the dev server binds to all interfaces.

**Multi-service routing:** Projects with multiple HTTP services (frontend + backend) can give each service its own domain using `routing.domain`. Only available for HTTP/HTTPS routing. Without it, all services share the project domain.

### Volumes: bind mounts vs named volumes

Volumes in the `services.<name>.volumes` list come in two forms:

**Bind mounts** map a host directory into the container. Your source code goes here - edits on the host are reflected in the container immediately (via Mutagen on macOS, direct mount on Linux).

```yaml
volumes:
  - ${PROJECTPATH}:/app              # host directory -> container path
```

**Named volumes** are persistent storage managed by Docker. Use them for data that should survive container recreation but doesn't belong on the host - database files, dependency directories, caches.

```yaml
volumes:
  - ${PROJECTPATH}:/app              # bind mount: source code
  - db_data:/var/lib/mysql           # named volume: database files
  - node_modules:/app/node_modules   # named volume: dependencies (alternative to Mutagen ignore)
```

How to tell them apart: if the left side starts with `/`, `./`, `../`, or `${` it's a bind mount. Otherwise it's a named volume.

Named volumes persist across `zdev stop`/`zdev start` and `zdev down`. They are only removed with `zdev down -v`. No top-level declaration needed - zdev discovers them automatically from the volume entries.

**When to use named volumes vs Mutagen ignore for dependencies:** Both approaches keep dependencies inside the container. Named volumes are explicit and work everywhere. Mutagen ignore is simpler (no extra volume entry) but only applies when Mutagen is active (macOS). For templates, prefer Mutagen ignore for Node.js `node_modules` since it's the standard zdev pattern.

### Configuring Mutagen ignores

Docker bind mounts on macOS are notoriously slow - operations like `pnpm install` or `composer install` that touch thousands of files can take 5-10x longer than native. zdev solves this by using [Mutagen](https://mutagen.io/) for fast bidirectional file sync between your host and a Docker volume. This happens automatically on macOS (on Linux, bind mounts are already fast, so Mutagen is not used).

The Mutagen ignore list controls which paths are **not synced** in either direction. Ignored paths exist only inside the container's volume. This is essential for dependencies like `node_modules` or `vendor` - they contain platform-specific binaries that must match the container's OS, and syncing thousands of dependency files would negate Mutagen's performance gains.

Add directories that should stay inside the container and not sync to the host:

```yaml
mutagen:
  ignore:
    - node_modules       # Native modules, platform-specific (Node.js)
    - .pnpm-store        # pnpm content-addressable store
    - vendor             # Composer dependencies (PHP)
    - .zdev             # zdev config (only needed on host)
    - .setup-complete    # Marker file (must persist in container volume)
```

Add framework-specific build artifacts:
- Nuxt: `.nuxt`, `.output`
- Next.js: `.next`
- Symfony: `var` (cache, logs)

**Critical:** `.setup-complete` MUST be in the ignore list. Since it's ignored, it persists in the container's Mutagen volume independently of the host. If it were synced, it could be deleted on one side and propagate to the other, breaking the setup state.

### Configuring file ownership inside the container

Templates whose runtime user isn't root - PHP/Apache (`www-data`), Nginx (`nginx`), Node images (`node`) - need synced files to be readable by that user. By default, Mutagen writes files as root inside the container, so the template's process can't access them.

Set per-service ownership in the service's `mutagen:` block:

```yaml
services:
  web:
    image: my-php-apache:latest
    volumes:
      - ${PROJECTPATH}:/var/www/html
    mutagen:
      user: www-data            # owner stamped on synced files
      group: www-data           # group stamped on synced files
      file_mode: "0644"         # optional - mode for files
      directory_mode: "0755"    # optional - mode for directories
```

These map to Mutagen's `--default-*-beta` flags, which apply to **new** files. zdev tracks these values per session and, when they change, terminates the session, recreates it, and runs `chown -R` inside the container so pre-existing files pick up the new ownership too.

## Commands (justfiles)

Templates can include commands in `.zdev/commands/`. Each `.just` file becomes a `zdev` subcommand:

```
.zdev/commands/
  setup.just     ->  zdev setup
  test.just      ->  zdev test
  seed.just      ->  zdev seed
```

Commands are written as [just](https://github.com/casey/just) recipes. Just is a command runner (think `make` without the build system baggage). A justfile can have multiple recipes, arguments, dependencies between recipes, conditional logic, and more. See the [just documentation](https://just.systems/man/en/) for the full syntax.

When you run `zdev <command>`, zdev looks for `.zdev/commands/<command>.just` and executes it. If the justfile has multiple recipes, you can run a specific one with `zdev <command> <recipe>`. If no recipe is given, just runs the `default` recipe (if defined). For example:

```just
# .zdev/commands/test.just

default: unit           # zdev test -> runs unit tests

unit:                   # zdev test unit
    zdev exec app pnpm test

watch:                  # zdev test watch
    zdev exec app pnpm test --watch

e2e:                    # zdev test e2e
    zdev exec app pnpm test:e2e
```

### Transparent forwarding (colon-namespaced subcommands)

To wrap CLIs like `bin/console cache:clear` or `artisan migrate:fresh`, declare a recipe named after the file. zdev auto-prepends it so args with colons pass through as recipe parameters instead of being parsed as just's module path:

```just
# .zdev/commands/console.just
console *args:
    zdev exec app php bin/console {{args}}
```

Now `zdev console cache:clear` -> `bin/console cache:clear`. Without a filename-matching recipe, the legacy behavior holds (first arg is the recipe name), so `test.just` above keeps working.

Templates typically include at least `setup.just`. You can add more commands for common tasks like running tests, seeding databases, or deploying. These commands are discoverable - `zdev --help` lists them, and agents can `ls .zdev/commands/` to find them.

Justfiles run on the **host**, not inside the container. Use `zdev exec app <command>` to run things inside the container.

## Writing setup.just

Setup often runs thousands of lines of command output (package managers installing deps, scaffolders writing files, compilers building assets). Plain `@echo "Installing PHP extensions..."` lines get buried in that noise and users lose track of which phase is running. Use `@zdev step "<message>"` instead - it prints two leading blank lines, a cyan `▶`, and the message in bold, so each phase reads as a clear section header even when the surrounding output is a wall of text. Styling is stripped when stdout isn't a TTY, when `NO_COLOR` is set, or when the user has plain mode enabled in global config, so the same recipe works in logs and CI too.

```just
# Description of what setup does

[no-exit-message]
default:
    zdev start -q
    @zdev step "Installing dependencies"
    zdev exec app sh -c "your install commands here && touch .setup-complete"
    @zdev step "Setup complete! App will start automatically."
    zdev info
```

**Conventions:**
- `zdev start` goes first - the container must be running before `zdev exec`
- `touch .setup-complete` goes last in the exec - only after everything succeeds
- `@zdev step "<msg>"` for each top-level phase instead of `@echo`; reserve plain `@echo` for sub-detail lines that don't need to stand out
- Keep echo ON for `zdev start`, `zdev exec`, and `zdev info` so the user sees what's running
- Add `[no-exit-message]` to suppress just's default exit message
- Recipes run with cwd = **project root** (not `.zdev/commands/`), so `docker build -f services/app/Dockerfile services/app/` and similar relative paths resolve from the project root

## Handling framework scaffolding

Frameworks like Nuxt and Symfony have their own scaffolding commands (`nuxi init`, `symfony new`). These commands typically expect an empty directory, which conflicts with `.zdev/` already being there.

There are two approaches depending on whether the scaffolding tool supports a force flag.

### Scaffold in-place (when the tool supports --force)

If the scaffolding command can run in a non-empty directory, scaffold directly in `/app`. This is the cleanest approach - no copying, no path issues.

On macOS with Mutagen, `.zdev` is in the ignore list so the container sees an essentially empty `/app`. On Linux with bind mounts, `.zdev/` is visible but scaffolding tools just add their own files alongside it.

**Example - Nuxt** (`nuxi init` supports `--force`) - `.zdev/commands/setup.just`:

```just
[no-exit-message]
default:
    zdev start -q
    @zdev step "Installing tools"
    zdev exec app sh -c "corepack enable && apk add --no-cache git"
    @zdev step "Scaffolding Nuxt project"
    zdev exec app pnpm dlx nuxi@latest init . --packageManager pnpm --gitInit=false --force
    @zdev step "Preparing Nuxt modules"
    zdev exec app npx nuxi prepare
    @zdev step "Approving native module builds"
    zdev exec app pnpm approve-builds --all
    @zdev step "Finalizing"
    zdev exec app sh -c "echo '.setup-complete' >> .gitignore && touch .setup-complete"
    @zdev step "Setup complete! App will start automatically."
    zdev info
```

Key details:
- `nuxi init .` scaffolds into the current directory, not a temp dir
- `--force` allows a non-empty directory
- `npx nuxi prepare` runs Nuxt module initialization, which may prompt to install missing dependencies (e.g. `better-sqlite3` for `@nuxt/content`). This runs interactively via `zdev exec` so prompts work. Without this, the same prompts would fire in the container entrypoint where there's no terminal, crashing the container.
- `pnpm approve-builds --all` approves native module build scripts after prepare (which may have installed new packages)
- `echo '.setup-complete' >> .gitignore` appends our marker to the scaffolder's gitignore
- `COREPACK_ENABLE_DOWNLOAD_PROMPT` is set in config.yaml's environment, not repeated in each command

### Scaffold in /tmp (when the tool requires an empty directory)

Some scaffolding tools have no force flag and strictly require an empty directory. In this case, scaffold in `/tmp` inside the container, then copy the files to `/app`.

**This approach is safe for PHP** because Composer's autoloader uses `__DIR__` relative paths resolved at runtime. Moving `vendor/` between directories works fine. **It does NOT work reliably for Node.js/pnpm** because pnpm uses a symlink-based content-addressable store with paths tied to the install location.

**Example - Symfony** (`symfony new` requires an empty directory) - `.zdev/commands/setup.just`:

```just
[no-exit-message]
default:
    zdev start -q
    @zdev step "Installing dependencies"
    zdev exec app apk add --no-cache bash
    @zdev step "Installing Composer"
    zdev exec app sh -c "wget -qO- https://getcomposer.org/installer | php -- --install-dir=/usr/local/bin --filename=composer"
    @zdev step "Installing Symfony CLI"
    zdev exec app sh -c "wget https://get.symfony.com/cli/installer -O - 2>/dev/null | bash && cp \$HOME/.symfony5/bin/symfony /usr/local/bin/symfony"
    @zdev step "Scaffolding Symfony project"
    zdev exec app symfony new /tmp/app --no-git
    @zdev step "Copying project files"
    zdev exec app sh -c "cp -r /tmp/app/. /app/ && rm -rf /tmp/app && echo '.setup-complete' >> .gitignore && touch .setup-complete"
    @zdev step "Setup complete! App will start automatically."
    zdev info
```

Key details:
- `symfony new /tmp/app --no-git` scaffolds into a temp directory (Symfony has no `--force` for existing dirs)
- `--no-git` skips git init, avoiding the need for git config in the container
- Composer and Symfony CLI are installed to `/usr/local/bin` so they're available in subsequent exec calls
- `cp -r /tmp/app/. /app/` copies all files (including dotfiles) into the project directory. The `.zdev/` directory in `/app` is preserved because Symfony doesn't create one.
- Composer and Symfony CLI are installed at runtime since `php:8.4-cli-alpine` doesn't include them

### When to use which approach

| Approach | Use when | Examples |
|----------|----------|---------|
| In-place with `--force` | The scaffolding tool supports non-empty directories | Nuxt (`nuxi init . --force`) |
| /tmp + copy | The tool strictly requires an empty directory AND the ecosystem's dependency dir is portable | Symfony (`symfony new`), Laravel |
| No scaffolding needed | The template includes all source files | Express, static sites |

**Rule of thumb:** Prefer in-place scaffolding when possible. Only use the /tmp approach when the tool has no force flag, and only when the language ecosystem supports moving the dependency directory (PHP/Composer: yes, Node.js/pnpm: no).

## Framework-specific notes

### Node.js with pnpm

**Corepack prompt:** Node.js ships with corepack but pnpm must be downloaded on first use. Suppress the confirmation prompt:

```sh
export COREPACK_ENABLE_DOWNLOAD_PROMPT=0 && corepack enable
```

Always `export` the variable so it applies to all subsequent commands in the same `sh -c`.

**Native module build scripts:** pnpm v10 blocks build scripts by default. After installing dependencies, run:

```sh
pnpm approve-builds --all
```

This approves all pending native modules (like `better-sqlite3`, `esbuild`, `@parcel/watcher`) non-interactively and triggers their build/download of prebuilt binaries. Run it AFTER `pnpm install` so there are packages to approve. It saves the approvals to `package.json` so they persist across reinstalls.

**File watching:** Use Node.js built-in `--watch` mode (Node 22+) for automatic restarts:

```json
"scripts": {
  "start": "node --watch app.js"
}
```

Frameworks like Nuxt and Next.js have their own HMR - no extra config needed.

**Runtime dependency prompts:** Some Nuxt modules (like `@nuxt/content`) prompt to install missing dependencies at runtime. These prompts need a terminal which the container entrypoint doesn't have. Fix: run the framework's prepare step during setup (when `zdev exec` provides a terminal). For Nuxt: `npx nuxi prepare`.

### PHP with Composer

**Installing Composer:** The `php:*-cli-alpine` images don't include Composer. Install it at runtime:

```sh
wget -qO- https://getcomposer.org/installer | php -- --install-dir=/usr/local/bin --filename=composer
```

**Installing Symfony CLI:** For the Symfony dev server and `symfony new` command:

```sh
apk add --no-cache bash
wget https://get.symfony.com/cli/installer -O - 2>/dev/null | bash
export PATH="$HOME/.symfony5/bin:$PATH"
```

**Dev server:** Use the Symfony CLI server for full compatibility:

```sh
symfony server:start --no-tls --port=8000 --allow-all-ip
```

`--no-tls` because zdev handles HTTPS via Traefik. `--allow-all-ip` binds to `0.0.0.0`.

**vendor/ portability:** Unlike Node.js, PHP's `vendor/` directory uses `__DIR__` for path resolution at runtime. Copying `vendor/` between directories (e.g. from `/tmp/app` to `/app`) works fine. This is why the /tmp scaffolding approach is safe for PHP but not for Node.js.

**Raise `memory_limit`:** the PHP CLI default (128 MB) OOMs on Symfony `cache:clear` post-install scripts, Composer dependency solving on large projects, and anything that loads the full container. Add a drop-in ini file once in setup and again guard it in the entrypoint so it survives container recreation:

```sh
printf 'memory_limit=-1\n' > /usr/local/etc/php/conf.d/zz-app.ini
```

**Install missing PHP extensions:** `php:8.3-cli-alpine` ships only a minimal set. Projects like Sylius, Shopware, and Akeneo need `intl pdo_mysql gd bcmath opcache exif zip` at minimum. Use [install-php-extensions](https://github.com/mlocati/docker-php-extension-installer):

```sh
wget -qO /usr/local/bin/install-php-extensions \
  https://github.com/mlocati/docker-php-extension-installer/releases/latest/download/install-php-extensions
chmod +x /usr/local/bin/install-php-extensions
install-php-extensions intl pdo_mysql gd bcmath opcache exif zip
```

Guard with `[ ! -f /usr/local/bin/install-php-extensions ]` in the entrypoint so restarts skip the reinstall.

**Trusted proxies (Symfony/Sylius/Laravel/Shopware):** Traefik terminates HTTPS and forwards HTTP to the app. Without a trusted-proxy config, Symfony generates `http://` URLs inside the HTTPS page - the browser blocks them as mixed content, the debug toolbar hangs on "Loading…", and admin login breaks. Set this in the app service `environment:`:

```yaml
environment:
  SYMFONY_TRUSTED_PROXIES: private_ranges    # Symfony 6.3+ shorthand
  # Laravel equivalent uses TRUSTED_PROXIES=* for the TrustProxies middleware
```

This is required for any PHP framework that builds absolute URLs while running behind a reverse proxy. It's the single most common runtime surprise on PHP framework templates.

**Asset pipelines (Webpack Encore, Vite):** Many PHP framework templates ship a `package.json` and build JS/CSS at install time (Sylius 2.x via Webpack Encore, Shopware 6 admin, most custom themes). Add Node to the app container and build assets in setup:

```sh
apk add --no-cache nodejs npm
npm install --no-audit --no-fund && npm run build
```

Also add an idempotent rebuild in the entrypoint so `zdev down && zdev start` regenerates the bundle:

```sh
if [ -f package.json ] && [ ! -f public/build/shop/manifest.json ]; then
  npm install --no-audit --no-fund && npm run build;
fi
```

This matters because `public/build` lives in `mutagen.ignore` (binary, regenerable), so it's lost on container recreation. Without the entrypoint rebuild, the first page load after a clean start 500s on a missing `manifest.json`.

**Mailer DSN:** for any Symfony/Sylius app, wire mail to Mailpit with `MAILER_DSN: "smtp://mail:1025"` (no auth, no TLS). For Laravel: `MAIL_HOST=mail MAIL_PORT=1025 MAIL_ENCRYPTION=null`.

**Log retention with `shared.logs: true`:** Dozzle streams from Docker's per-container log buffer; it does not store logs itself. Logs survive `zdev down` and Docker Desktop restarts, but are lost whenever the container is recreated (any `zdev update` that hits config drift, `zdev remove`, or `zdev services recreate`). If your template's setup flow depends on grepping a previous run's output, don't - dump the relevant info to a synced file inside the project instead.

### Don't include files that the scaffolder creates

For scaffold templates, don't include `.gitignore`, `README.md`, or any files that the scaffolding tool will create. The scaffolder's versions will take precedence. If you need zdev-specific entries (like `.setup-complete`), append them to the scaffolder's `.gitignore` after setup:

```sh
echo '.setup-complete' >> .gitignore
```

## Naming and publishing

Name your template repository `zdev-template-<name>`:

```
myorg/zdev-template-react    -> zdev create myorg/zdev-template-react my-app
myorg/zdev-template-django   -> zdev create myorg/zdev-template-django my-app
```

The `0ploy` org has a shorthand - bare names resolve to `0ploy/zdev-template-<name>`:

```
zdev create express   -> 0ploy/zdev-template-express
zdev create nuxt4     -> 0ploy/zdev-template-nuxt4
```

## Testing

During development, test your template locally by referencing the directory:

```bash
zdev create ./my-template test-app
cd test-app
zdev setup
```

Verify:
- `zdev setup` completes without errors
- The app URL (`https://test-app.0ploy.dev`) loads correctly
- `zdev restart` works (entrypoint picks up dependencies)
- File changes are reflected (HMR or `--watch` mode)

## Existing templates

Browse all available templates on GitHub: [0ploy repositories matching `zdev-template-`](https://github.com/orgs/0ploy/repositories?q=zdev-template-). Each template's README explains what it includes and how to use it.

## Common pitfalls

**Container crashes before setup runs.**
The entrypoint must keep the container alive when `.setup-complete` doesn't exist. Use the wait loop pattern. Never use a command that can fail before setup (like `pnpm start` unconditionally).

**Auxiliary container (db, queue, cache) exits immediately with `sh: 0: Illegal option --`.**
The `command:` field in `.zdev/config.yaml` is wrapped in `sh -c "<value>"`, not passed as a raw Docker CMD array. Flag-style args like `command: --group_concat_max_len=320000` go straight to `sh` which rejects them. If you need to pass flags to the image's default binary (e.g. MariaDB, RabbitMQ), wrap them yourself: `command: exec mariadbd --group_concat_max_len=320000 --sort_buffer_size=2M` — or supply a config file via a volume mount instead.

**`zdev exec` fails with "service not running".**
The container crashed. Check `zdev logs` to see why. Common causes: the entrypoint command failed, missing `.setup-complete` wait loop, or a syntax error in the shell command.

**Native modules fail with "Ignored build scripts".**
pnpm v10 blocks build scripts by default. Run `pnpm approve-builds --all` after `pnpm install` to approve and rebuild them.

**Framework prompts crash with "TTY initialization failed".**
Some frameworks prompt interactively at runtime (e.g. `@nuxt/content` asking to install `better-sqlite3`). These prompts need a terminal which the container entrypoint doesn't have. Fix: trigger these checks during setup (when `zdev exec` provides a terminal) by running the framework's prepare step. For Nuxt: `npx nuxi prepare`.

**Corepack asks "Do you want to continue?"**
Set `export COREPACK_ENABLE_DOWNLOAD_PROMPT=0` before running `corepack enable`. Must be `export` so it applies to subsequent commands in the same `sh -c`.

**Dev server not accessible via browser.**
The dev server is listening on `localhost` inside the container. Set `HOST=0.0.0.0` in the environment (Node.js) or use `--allow-all-ip` (Symfony CLI) so it binds to all interfaces.

**Scaffolding tool complains about non-empty directory.**
If the tool supports a force flag (`--force`), use it to scaffold in-place. If not (like `symfony new`), scaffold in `/tmp` and copy back. See "Handling framework scaffolding" above.

**Changes on host not reflected in container (or vice versa).**
Check the Mutagen ignore list. Ignored paths are not synced in either direction. Dependency directories (`node_modules`, `vendor`) should be ignored (they stay in the container). Source files should NOT be ignored.

**Symfony/Sylius page renders but the Web Debug Toolbar is stuck on "Loading…", admin login bounces, or the browser console shows mixed-content errors.**
The app is generating `http://` URLs inside an HTTPS page because Symfony doesn't see that the outer request was HTTPS. Traefik terminates TLS and forwards plain HTTP to the app, but Symfony ignores the `X-Forwarded-Proto` header unless you trust the proxy. Set `SYMFONY_TRUSTED_PROXIES: private_ranges` in the service environment. Laravel equivalent: `TRUSTED_PROXIES=*`. See "PHP with Composer" above for details.

**Storefront renders HTTP 500 with "Asset manifest file /app/public/build/*/manifest.json does not exist".**
Webpack Encore (or similar bundler) assets haven't been built. Run `zdev exec app npm run build` once, then add `npm install && npm run build` to `setup.just` and an idempotent rebuild in the container entrypoint (see "PHP with Composer" above).

**`curl` returns 200 but the browser sees errors.**
HTTP status alone doesn't cover mixed-content blocks, CSP violations, or JS errors - those are browser-only failure modes. When finishing a template, verify in an actual browser (e.g. the chrome-devtools MCP tools) and check both the console and the network tab, not just the status code.
