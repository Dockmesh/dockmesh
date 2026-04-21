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

# Detect upgrade-vs-fresh early so the post-install banner can show
# the right next-step. An "upgrade" is: binary already on disk AND we
# successfully run --version on it. If the binary exists but is broken
# we treat it as fresh so the user still gets the init hint.
IS_UPGRADE=0
PREV_VERSION=""
if [ -x "$INSTALL_DIR/dockmesh" ]; then
  if PREV_VERSION="$("$INSTALL_DIR/dockmesh" --version 2>/dev/null | head -1)"; then
    IS_UPGRADE=1
  fi
fi

# Does a systemd unit already exist? Determines whether the upgrade
# message offers "systemctl restart" as the next step, and whether the
# installer can auto-restart for the user.
HAS_SYSTEMD_UNIT=0
if command -v systemctl >/dev/null 2>&1 && \
   systemctl list-unit-files dockmesh.service 2>/dev/null | grep -q '^dockmesh\.service'; then
  HAS_SYSTEMD_UNIT=1
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
#  Post-install banner — branches on fresh install vs upgrade.
# ------------------------------------------------------------------
INSTALLED_VERSION="$("$INSTALL_DIR/dockmesh" --version 2>/dev/null | head -1 || echo "$VERSION")"

if [ "$IS_UPGRADE" = "1" ]; then
  # Upgrade path: binary was already present. Restart the running
  # service automatically if systemd unit exists — that's what the user
  # almost always wants next, and avoids the surprise of an old version
  # still serving traffic after `curl | bash` "succeeded".
  if [ "$HAS_SYSTEMD_UNIT" = "1" ]; then
    info "restarting dockmesh.service..."
    if $USE_SUDO systemctl restart dockmesh 2>/dev/null; then
      ok "service restarted"
    else
      warn "restart failed — run: ${BOLD}$USE_SUDO systemctl restart dockmesh${RST}"
    fi
    cat >&2 <<UPGRADE_SYSTEMD

${GREEN}${BOLD}Dockmesh upgraded.${RST}

    ${DIM}binary:${RST}   $INSTALL_DIR/dockmesh
    ${DIM}previous:${RST} $PREV_VERSION
    ${DIM}new:${RST}      $INSTALLED_VERSION

The service was restarted automatically. Your data, stacks and
configuration are untouched.

${DIM}Changelog: https://github.com/${REPO}/releases${RST}

UPGRADE_SYSTEMD
  else
    cat >&2 <<UPGRADE_MANUAL

${GREEN}${BOLD}Dockmesh upgraded.${RST}

    ${DIM}binary:${RST}   $INSTALL_DIR/dockmesh
    ${DIM}previous:${RST} $PREV_VERSION
    ${DIM}new:${RST}      $INSTALLED_VERSION

${BOLD}Restart your dockmesh process${RST} to load the new binary.
If you use Docker Compose: ${CYAN}docker compose restart dockmesh${RST}

${DIM}Changelog: https://github.com/${REPO}/releases${RST}

UPGRADE_MANUAL
  fi
else
  # Fresh install: walk the user through first-run setup.
  cat >&2 <<FRESH

${GREEN}${BOLD}Dockmesh installed.${RST}

    ${DIM}binary:${RST}  $INSTALL_DIR/dockmesh
    ${DIM}version:${RST} $INSTALLED_VERSION

${BOLD}Next step — run the guided setup:${RST}

    ${CYAN}sudo dockmesh init${RST}

It walks through database choice, admin user, listen port, and
optionally installs a systemd unit so the server starts on boot.

${DIM}Docs: https://dockmesh.dev/docs/install${RST}
${DIM}Issues: https://github.com/${REPO}/issues${RST}

FRESH
fi
