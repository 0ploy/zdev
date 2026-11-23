# Troubleshooting

## "DNS doesn't resolve"

`0ploy.dev` uses wildcard DNS pointing to `127.0.0.1`. If it doesn't work:

1. Check: `dig my-app.0ploy.dev`
2. Corporate VPNs sometimes block external DNS - try a different network
3. Add entries to `/etc/hosts` as a workaround

## "Containers won't start"

```bash
zdev down           # clean up
zdev start          # try again
zdev logs -f app    # check what's happening
```

## "File sync is slow" (macOS)

```bash
zdev mutagen status   # check if Mutagen is running
zdev mutagen reset    # recreate sync sessions if stuck
```

## "Port already in use"

zdev uses ports 80 and 443 for the shared router. Check what's using them:

```bash
lsof -i :80
lsof -i :443
```

## 1Password: "No accounts configured for use with 1Password CLI" (macOS)

`op` finds your account through the 1Password desktop app, not its own config. If `op account list`
prints nothing:

1. The 1Password app is installed in `/Applications`, signed in and unlocked as the same macOS user
   that runs `op`.
2. In the app, **Settings > Developer > Integrate with 1Password CLI** is on. Quit the app (⌘Q),
   reopen it, then open a new terminal window.
3. **macOS permission (common on fresh installs):** the first time `op` talks to the app, macOS asks
   whether your terminal may access data from another app. If that prompt was dismissed or denied,
   `op` silently finds no accounts. Allow your terminal (Terminal, iTerm, Ghostty, cmux, …) under
   **System Settings > Privacy & Security > App Management**, or reset the decision so macOS asks
   again: `tccutil reset All <terminal-bundle-id>` (e.g. `com.apple.Terminal`), restart the
   terminal, run `op account list` and click **Allow**.

If `op account list --debug` still shows nothing useful, try the built-in Terminal.app: if it works
there, the permission in step 3 is the cause.

## Symfony/Sylius/Laravel: stuck "Loading…" debug toolbar, broken admin login, mixed-content errors

Traefik terminates HTTPS and forwards plain HTTP to your app on its internal port. Without a
trusted-proxy config, Symfony (and Laravel) treat the inbound request as HTTP and generate
`http://` URLs inside the HTTPS page - the browser blocks them as mixed content, so the Symfony
debug toolbar hangs on "Loading…" and features that redirect or build absolute URLs (admin login,
password reset emails, asset manifests) break.

Fix - add one env var on the app service:

```yaml
services:
  app:
    environment:
      SYMFONY_TRUSTED_PROXIES: private_ranges    # Symfony/Sylius (RFC1918 + 127.0.0.1)
      # Laravel equivalent:
      # TRUSTED_PROXIES: "*"                      # for the TrustProxies middleware
```

Then apply with `zdev update`. Any framework that generates absolute URLs while running behind
a reverse proxy needs similar awareness.
