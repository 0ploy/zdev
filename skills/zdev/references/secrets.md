# 1Password Secrets Integration (`op-env://`)

zdev resolves secrets from a **1Password Environment** - a key-value table managed in the
1Password app (Developer section), separate from vault items. The project config commits only
non-secret references; real values are fetched by the `op` CLI when containers are created.

## Mental model

```
1Password app                        .zdev/config.yaml (committed)
┌─ Environment "myapp dev" ─┐        secrets:
│ API_KEY   = sk-abc123     │  ←──   op-env: b7qmzx3kfpwj4hn2c6t8vydl5a
│ DB_PASS   = hunter2       │
│ LOG_LEVEL = debug         │        services:
└───────────────────────────┘          app:
                                         environment:
     resolved at container                 API_KEY: op-env://API_KEY
     creation, one op call                 DB_PASS: op-env://DB_PASS
```

- `secrets.op-env` is the Environment's **ID** - not secret, safe to commit. One per project.
- Any env value `op-env://<NAME>` is replaced with that variable's value from the Environment.
  The env var name and the Environment key don't have to match (`DB_PASS: op-env://MYSQL_PASSWORD`).
- Plain values and `op-env://` references mix freely in the same `environment:` block. Works at
  project level and service level.
- A reference must be the ENTIRE env value - there is no mid-string interpolation
  (`mysql://dev:op-env://DB_PASS@db/...` does not work). For composite values like a
  `DATABASE_URL`, store the whole assembled string in the Environment and reference that.
- `${VAR}` substitution composes (it runs first, at config load).

## Requirements

- **Beta 1Password CLI** - Environments don't exist in the stable build:
  `brew install 1password-cli@beta` (>= 2.33.0-beta.02). If `op` is missing when a project needs
  it, zdev offers the brew install itself on an interactive terminal.
- 1Password desktop app with the CLI integration on: **Settings > Developer > Integrate with
  1Password CLI**. First resolution pops an authorization prompt (Touch ID).
- zdev >= the release that shipped `secrets.op-env` - older binaries reject the unknown config
  field with a config parse error. Don't add it to a team project before that release is out.

## Setting up a project

1. In the 1Password app: **Developer > Environments > New Environment**. Add variables by hand or
   import an existing `.env` file directly.
2. **Manage environment > Copy environment ID**.
3. Wire the project:

```yaml
secrets:
  op-env: <environment-id>

services:
  app:
    environment:
      API_URL: https://example.com/api      # plain value, untouched
      API_KEY: op-env://API_KEY             # resolved from the Environment
```

4. `zdev start`. Approve the 1Password prompt when it appears.

When converting an existing project that passes secrets via a gitignored `.env` or
`.zdev/local/config.yaml`: import the `.env` into a new Environment (step 1 supports file import),
replace the secret values in `environment:` with `op-env://` references, and delete the local
copies. Non-secret env vars can stay as plain values - only move what's actually secret.

## When resolution happens (and when it doesn't)

Secrets are resolved **only when a container is created**. This is deliberate: the container's
config hash covers the *unresolved* reference string, so routine commands never contact 1Password
and never pop auth prompts.

| Command | Contacts 1Password? |
|---------|--------------------|
| `zdev start` (container exists, stopped) | No - env already baked in |
| `zdev restart`, `zdev stop`, `zdev status` | No |
| `zdev update` with no config changes | No |
| First start / after `zdev down` / recreate on config change | Yes - one call |
| `zdev update --refresh-secrets` | Yes - one call |

All references resolve in a single `op environment read` per operation regardless of count, so
worst case is one auth prompt.

## Rotated secrets

Rotation is NOT auto-detected (that would require a 1Password call on every update check).
After changing values in the 1Password app:

```bash
zdev update --refresh-secrets
```

This fetches fresh values, compares them against a one-way hash stamped on each container
(`zdev.secrets-hash` label - no secret material stored), and recreates **only** the services whose
values actually changed. Unchanged services print "Secrets unchanged" and are left alone, so the
flag is cheap to run habitually.

Common trap: `zdev restart` does NOT refresh secrets - it stops and starts the existing container
with its baked-in env.

## CI / non-interactive use

Create a **service account** in the 1Password web UI with read access to the Environment, then:

```bash
export OP_SERVICE_ACCOUNT_TOKEN=<token>
zdev start
```

zdev passes the token through to `op` and never prompts when stdin isn't a terminal - auth
failures become actionable errors instead of hangs.

## Troubleshooting

| Symptom | Cause / fix |
|---------|-------------|
| `1Password CLI (op) not found in PATH` | Install the beta: `brew install 1password-cli@beta`. On a TTY zdev offers to run this itself |
| `authorization timeout` | The 1Password app's approval dialog went unanswered - unlock the app, rerun, approve the popup |
| `not signed in` / `no accounts configured` | On a TTY zdev drives `op signin` / `op account add` interactively; otherwise enable the desktop-app CLI integration or set `OP_SERVICE_ACCOUNT_TOKEN` |
| `does not support Environments` | Stable `op` build installed - Environments need the beta (`1password-cli@beta`) |
| `variable(s) X not found in 1Password Environment` | Key missing in the Environment (names are case-sensitive) - check Developer > Environments in the app |
| `secrets.op-env is not set` | Config uses `op-env://` values but no Environment ID - add the `secrets:` block |
| Team member's zdev errors on `secrets:` field | Their zdev predates the feature - they must update zdev |
| Container has the literal `op-env://...` string | Same cause: a pre-feature zdev binary passes references through unresolved |

`zdev systemcheck` reports the op CLI, whether it supports Environments, and sign-in state.

## Limits

- Values must be single-line (`op environment read` output is line-based). Multiline secrets
  (PEM keys) need a different transport - e.g. base64 the value in 1Password and decode in the app.
- Duplicate keys in an Environment resolve to the last occurrence.
- Resolved values are visible via `docker inspect` on the developer's machine - inherent to
  container env (same as docker-compose), not a zdev-specific exposure.
