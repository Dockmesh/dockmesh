#!/usr/bin/env bash
# ============================================================================
#  Dockmesh one-line installer.
#
#  Usage:
#    curl -fsSL https://get.dockmesh.dev | bash
#    curl -fsSL https://get.dockmesh.dev | DOCKMESH_VERSION=v1.2.3 bash
#    curl -fsSL https://get.dockmesh.dev | DOCKMESH_INSTALL_DIR=/opt/bin bash
#
#  Env vars:
#    DOCKMESH_VERSION      tag to install (default: latest release)
#    DOCKMESH_INSTALL_DIR  bin directory      (default: /usr/local/bin)
#    DOCKMESH_NO_SUDO      set to 1 to skip sudo (default: sudo if not root)
#
#  Reviews + unpacks release tarball from github.com/dockmesh/dockmesh
#  into INSTALL_DIR. This script never edits system state beyond that —
#  for DB / admin / systemd unit setup, run the interactive wizard:
#      sudo dockmesh init
# ============================================================================
set -euo pipefail

# ------------------------------------------------------------------
#  Colors — only if stdout is a TTY. `curl | bash` pipes stdout
#  through so TTY-detection is actually on stderr; we write the UI
#  to stderr so colours always render, and fall back to plain on
#  dumb terminals (TERM=dumb, NO_COLOR=1).
# ------------------------------------------------------------------
if [ -t 2 ] && [ "${NO_COLOR:-}" = "" ] && [ "${TERM:-}" != "dumb" ]; then
  BOLD=$'\033[1m'; DIM=$'\033[2m'; RST=$'\033[0m'
  CYAN=$'\033[36m'; GREEN=$'\033[32m'; YELLOW=$'\033[33m'; RED=$'\033[31m'
else
  BOLD=; DIM=; RST=; CYAN=; GREEN=; YELLOW=; RED=
fi

say()   { printf '%s\n' "$*" >&2; }
info()  { printf '%s==>%s %s\n' "$CYAN" "$RST" "$*" >&2; }
ok()    { printf '%s✓%s %s\n' "$GREEN" "$RST" "$*" >&2; }
warn()  { printf '%s!%s %s\n' "$YELLOW" "$RST" "$*" >&2; }
die()   { printf '%sx%s %s\n' "$RED" "$RST" "$*" >&2; exit 1; }

# ------------------------------------------------------------------
#  Banner
# ------------------------------------------------------------------
cat >&2 <<BANNER

${CYAN}${BOLD}     _            _                      _    ${RST}
${CYAN}${BOLD}  __| | ___   ___| | ___ __ ___   ___ ___| |__ ${RST}
${CYAN}${BOLD} / _\` |/ _ \\ / __| |/ / '_ \` _ \\ / _ / __| '_ \\${RST}
${CYAN}${BOLD}| (_| | (_) | (__|   <| | | | | |  __\\__ \\ | | |${RST}
${CYAN}${BOLD} \\__,_|\\___/ \\___|_|\\_\\_| |_| |_|\\___|___/_| |_|${RST}

${DIM}The single-binary Docker fleet manager.${RST}
${DIM}https://dockmesh.dev${RST}

BANNER

# ------------------------------------------------------------------
#  Inputs
# ------------------------------------------------------------------
REPO="dockmesh/dockmesh"
VERSION="${DOCKMESH_VERSION:-latest}"
INSTALL_DIR="${DOCKMESH_INSTALL_DIR:-/usr/local/bin}"
USE_SUDO="sudo"
if [ "$(id -u)" = "0" ] || [ "${DOCKMESH_NO_SUDO:-0}" = "1" ]; then
  USE_SUDO=""
fi

# ------------------------------------------------------------------
#  Detect OS + arch
# ------------------------------------------------------------------
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$OS" in
  linux) ;;
  darwin) die "macOS binary not shipped yet — build from source: https://github.com/$REPO" ;;
  *) die "unsupported OS: $OS" ;;
esac

ARCH_RAW="$(uname -m)"
case "$ARCH_RAW" in
  x86_64|amd64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) die "unsupported architecture: $ARCH_RAW" ;;
esac

info "detected $OS/$ARCH"

# ------------------------------------------------------------------
#  Pre-flight
# ------------------------------------------------------------------
command -v curl >/dev/null || die "curl not found — install: apt-get install -y curl"
command -v tar  >/dev/null || die "tar not found"

# Non-blocking notice if Docker is missing. The binary runs regardless,
# but 90% of users need Docker.
if ! command -v docker >/dev/null; then
  warn "docker not detected — install it first for Dockmesh to manage anything"
  say  "    https://docs.docker.com/engine/install/"
fi

# ------------------------------------------------------------------
#  Resolve latest version
# ------------------------------------------------------------------
if [ "$VERSION" = "latest" ]; then
  info "querying latest release..."
  VERSION="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
    | grep -oE '"tag_name":\s*"[^"]+"' | head -1 | cut -d'"' -f4 || true)"
  if [ -z "$VERSION" ]; then
    die "could not resolve 'latest' — GitHub API rate-limited? set DOCKMESH_VERSION explicitly"
  fi
fi

TARBALL="dockmesh_${OS}_${ARCH}.tar.gz"
URL="https://github.com/${REPO}/releases/download/${VERSION}/${TARBALL}"
CHECKSUMS_URL="https://github.com/${REPO}/releases/download/${VERSION}/checksums.txt"

info "version $BOLD$VERSION$RST"

# ------------------------------------------------------------------
#  Download + verify
# ------------------------------------------------------------------
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

info "fetching $TARBALL..."
if ! curl -fsSL --progress-bar "$URL" -o "$TMP/$TARBALL"; then
  die "download failed — check release exists: $URL"
fi

# Checksum verification — treat missing checksums file as a soft fail
# since older pre-release builds didn't ship one. Once v1.0+ it's
# always there.
info "verifying checksum..."
if curl -fsSL -o "$TMP/checksums.txt" "$CHECKSUMS_URL" 2>/dev/null; then
  EXPECTED="$(grep " ${TARBALL}$" "$TMP/checksums.txt" | awk '{print $1}' || true)"
  if [ -n "$EXPECTED" ]; then
    ACTUAL="$(sha256sum "$TMP/$TARBALL" | awk '{print $1}')"
    if [ "$EXPECTED" != "$ACTUAL" ]; then
      die "checksum mismatch! expected $EXPECTED, got $ACTUAL"
    fi
    ok "checksum verified ($EXPECTED)"
  else
    warn "no checksum entry for $TARBALL — continuing without verification"
  fi
else
  warn "no checksums.txt published for $VERSION — skipping verification"
fi

# ------------------------------------------------------------------
#  Unpack + install
# ------------------------------------------------------------------
info "unpacking..."
tar -xzf "$TMP/$TARBALL" -C "$TMP"
if [ ! -x "$TMP/dockmesh" ]; then
  die "tarball missing 'dockmesh' binary"
fi

info "installing to $INSTALL_DIR/dockmesh..."
$USE_SUDO install -m 0755 "$TMP/dockmesh" "$INSTALL_DIR/dockmesh"

# ------------------------------------------------------------------
#  Post-install banner
# ------------------------------------------------------------------
INSTALLED_VERSION="$("$INSTALL_DIR/dockmesh" --version 2>/dev/null | head -1 || echo "$VERSION")"

cat >&2 <<POST

${GREEN}${BOLD}Dockmesh installed.${RST}

    ${DIM}binary:${RST}  $INSTALL_DIR/dockmesh
    ${DIM}version:${RST} $INSTALLED_VERSION

${BOLD}Next step — run the guided setup:${RST}

    ${CYAN}sudo dockmesh init${RST}

It walks through database choice, admin user, listen port, and
optionally installs a systemd unit so the server starts on boot.

${DIM}Docs: https://dockmesh.dev/docs/install${RST}
${DIM}Issues: https://github.com/${REPO}/issues${RST}

POST
