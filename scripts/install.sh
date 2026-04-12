#!/usr/bin/env bash
# Dockmesh one-line installer.
#   curl -fsSL https://get.dockmesh.io | bash
set -euo pipefail

REPO="dockmesh/dockmesh"
VERSION="${DOCKMESH_VERSION:-latest}"
INSTALL_DIR="${DOCKMESH_INSTALL_DIR:-/usr/local/bin}"

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) echo "unsupported arch: $ARCH"; exit 1 ;;
esac

if [ "$VERSION" = "latest" ]; then
  VERSION="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" | grep -oE '"tag_name":\s*"[^"]+"' | head -1 | cut -d'"' -f4)"
fi

URL="https://github.com/${REPO}/releases/download/${VERSION}/dockmesh_${OS}_${ARCH}.tar.gz"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo ">> downloading $URL"
curl -fsSL "$URL" | tar -xz -C "$TMP"

echo ">> installing to $INSTALL_DIR"
install -m 0755 "$TMP/dockmesh" "$INSTALL_DIR/dockmesh"

echo ">> done: $("$INSTALL_DIR/dockmesh" --version 2>/dev/null || echo dockmesh installed)"
