# Helpers for the host jobs in .zdev/run (sourced, cwd = project root).

# Host OS/arch in Go's spelling.
host_os=$(uname -s | tr '[:upper:]' '[:lower:]')
host_arch=$(uname -m)
case "$host_arch" in
  x86_64) host_arch=amd64 ;;
  aarch64) host_arch=arm64 ;;
esac

# Runs a command where Go is: the zdev claude sandbox has the toolchain
# itself, on the host it is the app container.
in_go() {
  if [ -n "${ZDEV_SANDBOX:-}" ]; then
    "$@"
  else
    zdev exec app "$@"
  fi
}

# Waits until files the container wrote have reached the host (Mutagen on
# macOS). Without Mutagen the container writes the host directory directly.
sync_files() {
  [ -n "${ZDEV_SANDBOX:-}" ] && return 0
  local out
  if ! out=$(zdev mutagen flush 2>&1); then
    case "$out" in
      *disabled*) ;;
      *) printf '%s\n' "$out" >&2; fail "file sync did not finish" ;;
    esac
  fi
}
