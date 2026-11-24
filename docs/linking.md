# Linking projects

You don't need a link to call another project by its URL (`https://other.0ploy.dev`): that works from every container through the router, see [Routing](routing.md#calling-other-projects-from-a-container). Use a link when a container needs another project's internal port directly, without Traefik, or a service that isn't routed at all.

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

**Container names vs. project domains:** on a link network, container names (e.g., `app.sec-scan-decoder.zdev`) resolve to the container itself, so traffic goes straight to its internal port without Traefik or TLS. Project domains (`sec-scan-decoder.0ploy.dev`) also work from containers, link or not, but go through the router ([Routing](routing.md#calling-other-projects-from-a-container)).

The container name pattern is `<service>.<project>.zdev` - the same name shown by `zdev link status`.

Links are stored in the global state file and survive restarts - when a linked project starts, its containers are automatically reconnected to the link network. Each link creates its own Docker network (`zdev_link_<name>`), so different link groups stay isolated from each other.

Link names may only contain alphanumeric characters, hyphens, and underscores.
