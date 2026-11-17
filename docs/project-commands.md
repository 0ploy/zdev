# Project commands (`zdev run`)

> [!NOTE]
> **This is the current way to define project commands.** It supersedes justfiles in `.zdev/commands/` ([deprecated, documented here](project-commands-justfile.md)), which will be removed in a future release.

Every project has recurring tasks: install deps, run migrations, seed data, run tests. Instead of documenting them in a README, define them in `.zdev/run/` and run them with `zdev run`. Each `.toml` file is a command; the script itself is plain shell (or any language with a shebang).

```bash
zdev run                     # list all commands and jobs
zdev run db                  # list the jobs of db.toml
zdev run db reset            # run a job (and its dependencies)
zdev run test --watch        # everything after the command reaches the job as-is
zdev run --show db reset     # print what would run, without running it
zdev run --json              # everything as JSON, for tools and agents
```

A project command can never be confused with a built-in: `zdev run db` always means `.zdev/run/db.toml`, while `zdev db` opens Adminer.

## Single-job files

A file with a top-level `run` (or `script`) is one command. Every argument after its name reaches the script as `"$@"`, colons and spaces included:

```toml
# .zdev/run/console.toml  ->  zdev run console cache:clear
description = "Symfony console"
service = "app"            # run inside the app container; omit to run on the host
run = '''
php bin/console "$@"
'''
```

## Multi-job files

A file with `[jobs.<name>]` tables holds several jobs. There is no default job: `zdev run db` lists the jobs, and the token after the file name must be a job name, so adding a job never changes what an existing command line does.

```toml
# .zdev/run/db.toml
description = "Database tasks"
service = "app"            # file-level values are defaults for every job

[jobs.reset]
description = "Drop, migrate and seed"
needs = ["migrate"]
confirm = "Wipe the local database?"
run = '''
step "Seeding"
pnpm db:seed
'''

[jobs.migrate]
output = "quiet"           # one line on success, full output on failure
run = "npx prisma migrate deploy"
```

## Keys

File-level values are defaults for every job in the file; a job's own value wins (`env` merges per variable).

| Key | Where | Meaning |
|---|---|---|
| `description` | file, job | Shown by `zdev run` and `zdev run <file>` |
| `run` | job (file: single-job) | Inline script. A shebang picks the interpreter, otherwise bash |
| `script` | job (file: single-job) | Path to a script file instead of `run`; arguments are appended |
| `needs` | job | Dependencies, run first (see below) |
| `service` | file, job | `host` (default) or a service from `.zdev/config.yaml` |
| `workdir` | file, job | Working directory. Host: relative to the project root (the default). Container: relative to the service's working directory |
| `user` | file, job | User inside the container (container jobs only) |
| `env` | file, job | Extra environment variables |
| `env_file` | file, job | Dotenv file (`KEY=VALUE`) loaded before `env`, relative to the project root |
| `output` | file, job | `show` (default) or `quiet` |
| `confirm` | file, job | Question asked before anything runs; `--yes` answers it |
| `hidden` | job | Not listed; for helper jobs used as dependencies. Still runnable by name |
| `aliases` | file, job | Alternative names (`aliases = ["t"]`) |

Unknown keys produce a warning instead of an error, so a job file written for a newer zdev still works on an older one.

## Arguments and placeholders

Arguments reach the script verbatim: `$1`, `"$@"`, `${2:-default}` in shell, `sys.argv` in Python. zdev never substitutes anything into a script body.

The TOML fields `needs`, `workdir`, `env` values, `script` and `confirm` can use placeholders: `{1}`, `{2}`, ... for positions and `{all}` for all arguments.

```toml
[jobs.test]
workdir = "packages/{1}"   # zdev run monorepo test api --watch
run = '''
pnpm test "${@:2}"
'''
```

A missing argument becomes an empty string, except in `needs` and `workdir`, where it is an error.

## Dependencies

- `needs = ["build", "migrate"]` runs jobs of the same file first, in order. `"db reset"` refers to a job in another file, `"build {1}"` passes an argument.
- Each job runs at most once per `zdev run`, even if several jobs need it.
- The run stops at the first failure, with that job's exit code.
- A job can consist of `needs` only, to group other jobs.

## Where jobs run

- **Host (default):** working directory is the project root. Jobs get `PROJECTNAME`, `PROJECTPATH`, `PROJECTDIR`, `ZDEV_DOMAIN`, `ZDEV_HOME`, the project-level `environment:` from the config, and `ZDEV_CALLER_DIR` (where you ran `zdev run`). Use `zdev exec` for individual container commands.
- **Container (`service = "app"`):** the job runs inside the running service container (`zdev start` first). It gets `PROJECTNAME` and `ZDEV_DOMAIN`. A script without shebang uses bash if the image has it, sh otherwise.

## Shell helpers and output

Shell scripts stop at the first failing line (append `|| true` where failure is fine) and get three helpers:

- `step "Installing dependencies"` - a progress header that stands out against noisy output
- `warn "..."` - a yellow warning
- `fail "..."` - a red error, then exit 1

Other languages print the same markers as plain lines starting with `▶ `, `⚠ ` or `✗ `.

With `output = "quiet"`, a job's output is captured: on success zdev prints one line (`✓ db migrate (2.1s)`), on failure the full output. Marker lines still show as they happen. `zdev run -v` shows everything.

Line numbers in shell error messages match the job file.

## Coming from justfiles

[`.zdev/commands/*.just`](project-commands-justfile.md) (run as `zdev <name>`) still works but is deprecated and will be removed in a future release. Every run prints a warning, and `zdev status` lists justfiles shadowed by a built-in command (they never run).

```bash
zdev run --convert-just        # convert all
zdev run --convert-just db     # convert one
```

The converter writes `.zdev/run/<name>.toml`, keeps the original as `.just.converted`, and prints notes worth reviewing: for example a `cd` line that now affects the following lines (just ran each line in its own shell), or the missing default job. Files it can't translate safely stay untouched, with the reason.
