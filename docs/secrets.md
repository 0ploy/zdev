# Team secrets from 1Password

The classic onboarding wall: the app needs API keys and passwords, so someone digs up the current `.env` and sends it over Slack - and from that moment every developer has their own drifting, plaintext copy. zdev removes the handoff entirely: secrets live in a [1Password Environment](https://www.1password.dev/environments) your team already shares, and the committed config only holds references.

```yaml
variables:
  op-env: b7qmzx3kfpwj4hn2c6t8vydl5a    # your team's 1Password Environment (ID is not secret)

services:
  app:
    op-env: ${op-env}                    # injects every variable of the Environment
    environment:
      API_URL: https://example.com/api   # plain values mix freely - and win over injection
      SENDGRID_KEY: op-env://${op-env}/SENDGRID_KEY   # or inject single variables, from any Environment
```

A new teammate clones the repo, runs `zdev start`, approves one Touch ID prompt - done. No `.env` to obtain, nothing secret in git. When the Environment changes in 1Password - rotated values or newly added variables - `zdev update --refresh-secrets` recreates exactly the services affected. Manage the variables as a table in the 1Password app (Developer > Environments), including one-click `.env` import for existing projects.

Day-to-day commands (`start`/`stop`/`restart`/`status`) never contact 1Password, so there are no surprise authorization prompts. Requires the beta 1Password CLI - zdev offers to install it and walks you through sign-in when needed. The rules and CI setup are below.

## Reference

Two forms of one token, and the precise rules:

- **`services.<name>.op-env`** holds a 1Password Environment ID (app: Developer > Environments > Manage environment > **Copy environment ID**) and injects EVERY variable of that Environment into the container env. Explicit `environment:` entries - plain values and references alike - always win over injected ones.
- **`op-env://<environment-id>/<VARIABLE>`** as an env value injects a single variable. Works in project-level and service-level `environment:`; the env var name may differ from the variable name (`DB_PASS: op-env://<id>/MYSQL_PASSWORD`). References are self-contained, so one project can mix variables from multiple Environments.
- Neither the ID nor the references contain secret material - both are safe to commit. Keep the ID DRY with `${VAR}` substitution (it runs first, at config load): `variables: {op-env: <id>}`, then `op-env: ${op-env}` and `op-env://${op-env}/VARIABLE`.
- A reference must be the **entire** env value - no mid-string interpolation (`mysql://dev:op-env://<id>/DB_PASS@db/...` does not work). For composite values like a `DATABASE_URL`, store the whole assembled string in the Environment.
- Resolution happens only when a container is **created**; each distinct Environment is fetched in one cached `op` call per operation (at most one authorization prompt), and values are never written to disk by zdev. `start` of an existing container, `restart`, `status`, and no-op `update` never contact 1Password. On recreates, secrets resolve BEFORE the old container is removed, so a failure leaves the running service intact.
- **Changes in 1Password are not auto-detected.** `zdev update --refresh-secrets` checks 1Password and recreates only the services whose values changed - rotated values and variables added to or removed from an attached Environment. `zdev restart` does NOT refresh secrets.
- Requires the **beta** CLI build: `brew install 1password-cli@beta` (2.33.0-beta.02+; Environments don't exist in the stable build). If `op` is missing, zdev offers the install; if you're signed out, it walks you through `op signin`. `zdev systemcheck` reports CLI, Environments support, and sign-in state.
- CI / non-interactive: create a [service account](https://www.1password.dev/service-accounts/) with read access to the Environment and set `OP_SERVICE_ACCOUNT_TOKEN` - zdev passes it through and never prompts when stdin isn't a terminal.
- Values must be single-line; duplicate keys in the Environment resolve to the last occurrence.
- Like any container env var (docker-compose included), resolved values are visible to `docker inspect` on your machine.
