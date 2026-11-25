# CLI reference

Every command supports `--help`. Project commands (`zdev run`) are documented in [project-commands.md](project-commands.md).

## Lifecycle

```bash
zdev start              # Start every service in the project
zdev start <service>    # Start a single service (project setup runs idempotently)
zdev start --build      # Force rebuilding dockerfile: images
zdev start --no-build   # Never build; fail if a dockerfile: image is missing
zdev stop               # Stop containers (keeps them for quick restart)
zdev stop <service>     # Stop a single service container
zdev restart            # Stop + start every service
zdev restart <service>  # Bounce a single service container in-place
zdev update             # Apply config changes and remove services deleted from config
zdev update --refresh-secrets  # Also check 1Password and recreate services whose secrets changed
zdev down        # Remove containers and network
zdev down -v     # Remove everything including volumes
zdev rename <n>  # Rename project, migrate volumes, restart
```

## Development

```bash
zdev exec app bash              # Shell into a container
zdev exec app pnpm test         # Run a command
zdev run                        # List project commands (.zdev/run/)
zdev run <cmd> [job] [args]     # Run a project command
zdev logs                       # View logs
zdev logs -f app                # Follow logs for a service
zdev claude [service] [args]    # Claude Code in a sandbox container of the project (see docs/agents.md)
zdev claude update              # download the newest Claude Code release for the sandboxes now
```

## Information

```bash
zdev info        # Show project info, URLs, services
zdev list        # List all projects
zdev config      # Show resolved configuration
zdev status      # Quick status check
zdev open        # Open the current project's URL in the browser
zdev open my-app # Open another registered project's URL
```

`zdev status` also lists running **agent sandboxes** (`zdev claude`) and warns when the sandbox's git sync stopped on a conflict (fix: `zdev mutagen repair-git`). `zdev stop` and `zdev down` end the project's sandboxes (a `zdev stop` from inside a sandbox doesn't end itself).

`zdev status` also flags **stale file mounts**. A bind mount of a single file is resolved to an inode, not a name, so when an editor (or any tool that writes a temp file and renames it into place) replaces the file, the container keeps the old, deleted one - the host copy looks perfectly fine while the container reads stale content or nothing at all. zdev reports which service and which path, and restarting that service reattaches the mount.

## Shared Services

```bash
zdev services status    # Check shared service status
zdev services start     # Start shared services
zdev services stop      # Stop shared services
zdev services recreate  # Rebuild shared service containers
```

## Link networks

```bash
zdev link create <name>                        # Create a named link network
zdev link join <name> <member> [<member>...]   # Add projects or services
zdev link leave <name> <member> [<member>...]  # Remove members
zdev link delete <name>                        # Remove link and disconnect all
zdev link ls                                   # List all links
zdev link status <name>                        # Show members and connection state
```

Details: [linking.md](linking.md).

## File Sync (macOS)

```bash
zdev mutagen status  # Check sync status
zdev mutagen flush   # Wait for sync to complete
zdev mutagen reset   # Recreate sync sessions (if stuck)
zdev mutagen repair-git  # Resolve .git sync conflicts with an agent sandbox
```

`zdev mutagen status` reports one line per synced directory, with the state of that mount's own sync session.

## Maintenance

```bash
zdev cleanup         # Prune resources no live project owns
zdev cleanup --force # ...without the confirmation prompt
```

`zdev cleanup` lists what it would delete and asks before touching anything. It covers containers and volumes that no registered project's current config references, networks that no project or link owns, and state entries whose project directory is gone from disk.

Networks matter more than their size suggests: Docker's default address pool runs out after roughly 31 of them, and every project that was started and then deleted without `zdev down` leaves one behind. A network is kept whenever any container still references it, stopped ones included, because a container pins its network by ID and would otherwise refuse to start. A project whose config cannot be read is skipped with a warning and everything belonging to it is preserved.
