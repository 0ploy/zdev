# HTTPS and DNS

Every project and shared service gets locally-trusted HTTPS certificates. Your browser shows a green lock, cookies work with `Secure` flag, and your local environment matches production.

## Certificates

zdev signs its certificates with a local [mkcert](https://github.com/FiloSottile/mkcert) CA that `zdev systemcheck` installs once. The main certificate covers `*.0ploy.dev`, `*.shared.0ploy.dev` and `0ploy.dev`. A TLS wildcard matches exactly one label, so a routing domain nested deeper (`api.my-app.0ploy.dev`) or outside the zdev domain gets its own per-project certificate. `zdev start` and `zdev update` issue and refresh it automatically when the project's routing domains change; no router restart and no sudo are needed. If you reinstall the local CA, the next router start re-signs every certificate.

## HTTPS from inside containers

Containers trust the zdev CA without any setup, so HTTPS calls to other projects ([Routing](routing.md#calling-other-projects-from-a-container)) verify. zdev keeps the mkcert root CA and a bundle (your host's public root CAs plus the zdev CA) in `~/.zdev/trust/` and mounts them read-only into every project container:

| Container path | What reads it |
|---|---|
| `/etc/zdev/ca.pem`, `/etc/zdev/ca-bundle.pem` | `NODE_EXTRA_CA_CERTS` (Node), `REQUESTS_CA_BUNDLE` (Python `requests`) |
| `/etc/ssl/certs/ca-certificates.crt` (bundle) | OpenSSL, curl, PHP, Python, Go (and other OpenSSL-based runtimes) on Debian, Ubuntu, Alpine, Wolfi, Arch, distroless, `scratch` |
| `/etc/pki/tls/certs/ca-bundle.crt` (bundle) | the same on Fedora, RHEL, Amazon Linux |
| `/etc/ssl/ca-bundle.pem` (bundle) | the same on openSUSE |

The bundle replaces the image's own CA list. It contains all public roots, so calls to public HTTPS sites keep working. A CA change reaches the files in running containers right away, but processes that load the CA at startup (Node, Go) keep the old one until they restart (`zdev restart`). If you set `NODE_EXTRA_CA_CERTS`/`REQUESTS_CA_BUNDLE` yourself or mount your own file on one of these paths, yours wins.

Not covered: Java (its own keystore; import `/etc/zdev/ca.pem` with `keytool`), Rust built with `webpki-roots`, images that rebuild their CA file at runtime, and corporate CAs that live only in the macOS keychain.

## Router blocking the wildcard? (DNS rebinding protection)

zdev's URLs work because `*.0ploy.dev` is a public wildcard record pointing at `127.0.0.1`. Some routers refuse to return answers that point at loopback ("DNS rebinding protection"), so `https://my-app.0ploy.dev` fails to resolve on those networks.

`zdev systemcheck` detects this automatically and offers to fix it. You can also run it yourself:

```bash
zdev dns enable    # one sudo prompt to configure the host resolver
```

This runs a tiny local DNS container and points your OS at it **for the zdev domain only** (via `/etc/resolver` on macOS, systemd-resolved on Linux) - the router is bypassed for that domain and all other DNS is untouched. `zdev dns status` shows the current state; `zdev dns disable` reverts it.

The fallback covers your global zdev domain and every subdomain under it (so all your projects are handled by a single `zdev dns enable`). A project that overrides its `domain:` to a *different base domain* is not covered - it would still need that domain to resolve to `127.0.0.1` by other means.

Once enabled, the DNS container is a shared service like the router: it shows up in `zdev services status` and `zdev status`, and `zdev services start`, `stop`, and `recreate` manage it alongside the others. On machines that don't need the fallback it stays invisible. Note that `zdev services stop` stops it too, so your zdev URLs won't resolve until the next `zdev services start` or `zdev start` - to turn the fallback off permanently use `zdev dns disable`, which reverts the host resolver config so normal DNS takes over.
