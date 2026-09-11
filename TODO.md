# TODO

Open items. Each one says what is wrong or what is missing, and what was measured.
None of them proposes a fix; that belongs in the session that picks the item up.

Everything else this file used to list shipped in v0.12.0: the sync-ready gate on
non-root images, sessions that report healthy while writing nothing, `chown`
running as the wrong user, and `services.<name>.mutagen.no_sync` for directories
that must stay native binds.

## Missing: a bind source that does not exist on the host is created silently

A single-file bind whose source is missing is not an error. Docker creates the
source as a *directory*, and from the next start that entry is a directory bind:
it gets a sync volume and a session, and the file the config asked for never
exists. Measured in `ZDEV-MOUNT-BUG.md` (SCSHOP-263), where a missing
`etc/shopcockpit/.env.ai` reshaped a whole service's mounts with no warning
anywhere.

`GetMutagenSyncMounts` skips a mount when `os.Stat` fails, which is the same
input, so the mount is silently absent on the first start and silently synced on
the second.

What blocks a fix is a compatibility question, not a technical one: some projects
rely on Docker creating a missing *directory* source (`var/log`, a cache dir that
is not committed). Failing hard on every missing source would break them. Failing
only where a file was clearly meant needs a way to tell the two apart.

## Missing: no sync-ready gate for services that use the image's CMD

`buildContainerConfig` wraps `command:` with the gate. A service that relies on
the image's own `ENTRYPOINT`/`CMD` gets no wrapper, so it starts the moment the
container does, racing the initial sync. zdev warns about this at start since
v0.12.0, but the race is unguarded.

`mutagen.no_sync` covers the common case - a directory read once during startup
can be kept native - but not a CMD that needs the synced source tree itself.

Wrapping an image's own entrypoint means inspecting the image for its
`Entrypoint` and `Cmd` and rebuilding them, including exec-form semantics. That
is the design question, and it is also why the gate cannot cover anything an
ENTRYPOINT does before it execs CMD (`/docker-entrypoint-initdb.d` in the MySQL
and Postgres images is exactly that).
