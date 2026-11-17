# Project commands with justfiles (deprecated)

> [!WARNING]
> **Justfiles are deprecated and will be removed in a future release.** Project commands now live in `.zdev/run/*.toml` and run with `zdev run` - see [Project commands](project-commands.md). Convert existing justfiles with `zdev run --convert-just` (details in [Coming from justfiles](project-commands.md#coming-from-justfiles)). This page documents the old way for projects that haven't converted yet.

## How it works

Project tasks are [just](https://github.com/casey/just) files in `.zdev/commands/`. The filename becomes the command:

```
.zdev/commands/
  setup.just     ->  zdev setup
  test.just      ->  zdev test
  seed.just      ->  zdev seed
```

Commands are written as just recipes. A justfile can have multiple recipes, arguments, dependencies between recipes, conditional logic, and more; see the [just documentation](https://just.systems/man/en/) for the full syntax. zdev downloads the just binary itself.

When you run `zdev <command>`, zdev looks for `.zdev/commands/<command>.just` and executes it. If the justfile has multiple recipes, run a specific one with `zdev <command> <recipe>`. Without a recipe, just runs the first one (by convention called `default`):

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

- Justfiles run on the **host**, not inside the container. Use `zdev exec app <command>` to run things inside the container.
- Recipes run with cwd = **project root** (not `.zdev/commands/`), so relative paths like `services/app/Dockerfile` resolve from the project root.
- Recipes get `PROJECTNAME`, `PROJECTPATH`, `PROJECTDIR`, `ZDEV_DOMAIN`, `ZDEV_HOME` and the project-level `environment:` from the config.
- `zdev --help` lists the justfiles under "Project Commands".

## Transparent forwarding (colon-namespaced subcommands)

To wrap CLIs like `bin/console cache:clear` or `artisan migrate:fresh`, declare a recipe named after the file. zdev auto-prepends it so arguments with colons pass through as recipe parameters instead of being parsed as just's module path:

```just
# .zdev/commands/console.just
console *args:
    zdev exec app php bin/console {{args}}
```

Now `zdev console cache:clear` -> `bin/console cache:clear`. Without a filename-matching recipe, the first argument is the recipe name, so `test.just` above keeps working.

## Progress markers (`zdev step`)

Setup recipes print a lot of output. Plain `@echo` lines get buried in it, so use `@zdev step "<message>"` for each top-level phase: it prints two leading blank lines, a cyan `▶`, and the message in bold. Styling is stripped when stdout isn't a terminal, when `NO_COLOR` is set, or in plain mode.

```just
# .zdev/commands/setup.just
[no-exit-message]
default:
    zdev start -q
    @zdev step "Installing dependencies"
    zdev exec app sh -c "your install commands here && touch .setup-complete"
    @zdev step "Setup complete! App will start automatically."
    zdev info
```

- `@` stops just from echoing the command line; keep echo on for `zdev start`, `zdev exec` and `zdev info` so the user sees what runs.
- `[no-exit-message]` suppresses just's own exit message.

## Limitations (why it was replaced)

- **Built-ins shadow justfiles.** Built-in commands take precedence, so a `db.just` or `logs.just` never runs: `zdev db` opens Adminer instead. Using such a built-in prints a warning, and `zdev status` lists shadowed files under "Shadowed commands". Every new built-in can shadow a project's command the same way.
- **Every run prints a deprecation warning** pointing at `zdev run --convert-just`.
- **just has its own syntax and quirks**: each recipe line runs in its own shell (a `cd` doesn't carry over), arguments are substituted as text, and colons in arguments need the forwarding trick above.
- **Recipes can only run on the host**; `zdev run` jobs can also run inside a container (`service = "app"`).

Convert with:

```bash
zdev run --convert-just        # all .zdev/commands/*.just
zdev run --convert-just db     # one file
```
