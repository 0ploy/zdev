#!/bin/sh
set -e

REPO="0ploy/zdev"
BINARY="zdev"
BIN_DIR="$HOME/.zdev/bin"
SYMLINK_DIR="/usr/local/bin"

# Detect OS
OS="$(uname -s)"
case "$OS" in
  Darwin) OS="darwin" ;;
  Linux)  OS="linux" ;;
  *)      echo "Unsupported OS: $OS"; exit 1 ;;
esac

# Detect architecture
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64)  ARCH="amd64" ;;
  arm64|aarch64)  ARCH="arm64" ;;
  *)              echo "Unsupported architecture: $ARCH"; exit 1 ;;
esac

# Check for Docker
if ! command -v docker >/dev/null 2>&1; then
  echo "Warning: Docker is not installed. zdev requires Docker to run."
fi

ASSET="${BINARY}-${OS}-${ARCH}"
URL="https://github.com/${REPO}/releases/latest/download/${ASSET}"
CHECKSUMS_URL="https://github.com/${REPO}/releases/latest/download/checksums.txt"

echo "Downloading zdev for ${OS}/${ARCH}..."
mkdir -p "$BIN_DIR"
TMP_BIN="$(mktemp)"
TMP_SUMS="$(mktemp)"
# shellcheck disable=SC2064
trap "rm -f '$TMP_BIN' '$TMP_SUMS'" EXIT

curl -fsSL -o "$TMP_BIN" "$URL"
curl -fsSL -o "$TMP_SUMS" "$CHECKSUMS_URL"

# Verify SHA256. The checksums file is 'sha256  filename' per line; grep
# for our asset and run sha256sum (or shasum on macOS) against it.
EXPECTED="$(grep "  ${ASSET}$" "$TMP_SUMS" | awk '{print $1}')"
if [ -z "$EXPECTED" ]; then
  echo "error: no checksum for ${ASSET} in checksums.txt"
  exit 1
fi

if command -v sha256sum >/dev/null 2>&1; then
  ACTUAL="$(sha256sum "$TMP_BIN" | awk '{print $1}')"
else
  ACTUAL="$(shasum -a 256 "$TMP_BIN" | awk '{print $1}')"
fi

if [ "$EXPECTED" != "$ACTUAL" ]; then
  echo "error: checksum mismatch for ${ASSET}"
  echo "  expected: ${EXPECTED}"
  echo "  actual:   ${ACTUAL}"
  exit 1
fi

mv "$TMP_BIN" "$BIN_DIR/$BINARY"
chmod +x "$BIN_DIR/$BINARY"

# Remove macOS quarantine attribute
if [ "$OS" = "darwin" ] && command -v xattr >/dev/null 2>&1; then
  xattr -d com.apple.quarantine "$BIN_DIR/$BINARY" 2>/dev/null || true
fi

# Symlink into a PATH location. The real binary stays in the user-owned
# $BIN_DIR so future `zdev self-update` runs never need sudo.
# A fresh macOS (no Homebrew) has no /usr/local/bin at all, even though it is
# on the default PATH via /etc/paths - create it rather than failing in ln.
if [ -d "$SYMLINK_DIR" ] && [ -w "$SYMLINK_DIR" ]; then
  ln -sfn "$BIN_DIR/$BINARY" "$SYMLINK_DIR/$BINARY"
else
  echo "Creating symlink in ${SYMLINK_DIR} (requires sudo, one-time)..."
  sudo mkdir -p "$SYMLINK_DIR"
  sudo ln -sfn "$BIN_DIR/$BINARY" "$SYMLINK_DIR/$BINARY"
fi

echo "zdev installed to ${BIN_DIR}/${BINARY}"
echo "  symlinked from ${SYMLINK_DIR}/${BINARY}"
zdev version
