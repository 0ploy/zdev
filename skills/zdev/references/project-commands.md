# Writing project commands (`.zdev/run/*.toml`)

Read this when creating or changing a job file, or converting `.zdev/commands/*.just`. For just running commands, SKILL.md is enough.

## File shapes

**Single-job file** - top-level `run` or `script`. Every argument after the command name reaches the script as `"$@"`:

```toml
# .zdev/run/console.toml  ->  zdev run console cache:clear
description = "Symfony console"
run = '''
zdev exec app php bin/console "$@"
'''
```

**Multi-job file** - `[jobs.<name>]` tables (never bare `[name]` tables, never both shapes in one file). There is no default job: `zdev run test` lists the jobs, `zdev run test unit` runs one.

```toml
# .zdev/run/test.toml
description = "Tests"
service = "app"            # file-level values are defaults for every job

[jobs.unit]
description = "Unit tests"
run = "pnpm test"

[jobs.all]
needs = ["unit"]           # dependencies run first, each once
output = "quiet"
run = "pnpm test:e2e"
```

Names: lowercase letters, digits, `-`, `_`. Only `*.toml` directly in `.zdev/run/` are job files, so helper scripts can live in `.zdev/run/scripts/`.

## Keys

| Key | Where | Meaning |
|---|---|---|
| `description` | file, job | Shown in listings; always write one |
| `run` | job / single-job file | Inline script. Shebang picks the interpreter, else bash |
| `script` | job / single-job file | Script file path instead of `run` (host: relative to project root; container: path inside it). Arguments are appended. Must be executable |
| `needs` | job | Dependencies: `"build"` (same file), `"db reset"` (other file), `"build {1}"` (with argument). Space-split, no quoting |
| `service` | file, job | `host` (default) or a service from config.yaml |
| `workdir` | file, job | Host: relative to project root (default). Container: relative to the service's working dir |
| `user` | file, job | Container jobs only (an error on host jobs) |
| `env`, `env_file` | file, job | Env vars; `env_file` is a simple dotenv relative to the project root |
| `output` | file, job | `show` (default) or `quiet` (one line on success, full output on failure) |
| `confirm` | file, job | Question asked before anything runs. Fails without a terminal unless `--yes` |
| `hidden` | job | Not listed (helper jobs); still runnable by name |
| `aliases` | file, job | Alternative names |

A job may have only `needs` (groups other jobs). Unknown keys only warn.

## Host or container

- **Host job (default):** cwd = project root; env `PROJECTNAME`, `PROJECTPATH`, `PROJECTDIR`, `ZDEV_DOMAIN`, `ZDEV_HOME`, `ZDEV_CALLER_DIR`, plus the config's project-level `environment:`. Use it when the script orchestrates (`zdev start`, several `zdev exec` calls, git, host tools).
- **Container job (`service = "app"`):** the whole script runs inside the running container; simpler for single-container tasks (`php bin/console "$@"` instead of `zdev exec app php ...`). Gets only `PROJECTNAME` and `ZDEV_DOMAIN`. No shebang -> bash if the image has it, else sh, so keep container scripts POSIX unless the image has bash.

## Arguments and placeholders

- In the script: `"$@"`, `$1`, `${2:-default}` (shell), `sys.argv` (Python). zdev never substitutes into script bodies.
- In TOML fields `needs`, `workdir`, `env` values, `script`, `confirm`: `{1}`, `{2}`, `{all}`. Missing argument = empty, except in `needs`/`workdir` (error).
- Quote `"$@"` so arguments with spaces stay intact.

## Shell scripts

- Stop at the first failing line (`set -e` + pipefail are on). Append `|| true` where failure is fine.
- Helpers: `step "Phase"` (header that stands out in noisy output - use it instead of `echo` for top-level progress), `warn "..."`, `fail "..."` (error + exit 1). Other languages print plain lines starting with `▶ `, `⚠ `, `✗ `.
- Keep host scripts bash 3.2 compatible (macOS) unless the shebang asks for a newer bash.

## Check your work

```bash
zdev run                    # the new file must be listed, not under "Broken"
zdev run --show <cmd> [job] # resolved order, service, workdir, script - without running
```

## Converting justfiles (`.zdev/commands/*.just`)

Justfiles are deprecated (run as `zdev <name>`, built-ins win, so some never run; `zdev status` lists those). Convert with the tool instead of by hand:

```bash
zdev run --convert-just        # all
zdev run --convert-just db     # one
```

It writes `.zdev/run/<name>.toml`, renames the original to `.just.converted`, and prints `review:` notes. Act on them:

- **`cd` / `export` now affects the following lines** - just ran each line in its own shell. Check the lines after it still work.
- **No default job** - bare `zdev test` used to run the first recipe; now it's `zdev run test <job>`. Update docs/scripts that call it.
- **Single recipe renamed** - `zdev db reset` becomes `zdev run db`.

Refused files (modules, expression variables, `[linux]`/OS attributes, `&&` post-dependencies, `{{...}}` inside Python, ...) stay untouched with the reason. Convert those by hand only after understanding the reason.
