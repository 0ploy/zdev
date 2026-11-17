# Linking projects

Link networks enable direct container-to-container communication between separate projects. Each project runs on its own isolated Docker network, so by default containers in project A cannot reach containers in project B. Link networks solve this by creating a shared Docker network that selected containers join.

```bash
zdev link create <name>                        # Create a named link network
zdev link join <name> <member> [<member>...]   # Add projects or services
zdev link leave <name> <member> [<member>...]  # Remove members
zdev link delete <name>                        # Remove link and disconnect all
zdev link ls                                   # List all links
zdev link status <name>                        # Show members and connection state
```

Members can be whole projects or individual services:

```bash
zdev link create backend-mesh
zdev link join backend-mesh sec-scan sec-scan-decoder
zdev link join backend-mesh redis-debug.app    # only the app service
```

Linked containers reach each other by their **container name**, not the project domain:

```bash
# From inside sec-scan, reach sec-scan-decoder's app service:
curl http://app.sec-scan-decoder.zdev:3000
```

**Why container names, not project domains?** The project domain (e.g., `sec-scan-decoder.0ploy.dev`) uses wildcard DNS that resolves to `127.0.0.1`. Inside a container, `127.0.0.1` points to the container itself, not the host or Traefik - so the domain is unreachable. Container names (e.g., `app.sec-scan-decoder.zdev`) are resolved by Docker's built-in DNS, which returns the actual container IP on the shared link network. This works reliably and without TLS certificate issues.

The container name pattern is `<service>.<project>.zdev` - the same name shown by `zdev link status`.

Links are stored in the global state file and survive restarts - when a linked project starts, its containers are automatically reconnected to the link network. Each link creates its own Docker network (`zdev_link_<name>`), so different link groups stay isolated from each other.

Link names may only contain alphanumeric characters, hyphens, and underscores.
