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
#    DOCKMESH_VERSION       tag to install (default: latest release)
#    DOCKMESH_CHANNEL       stable | testing      (default: stable)
#    DOCKMESH_INSTALL_DIR   bin directory         (default: /usr/local/bin)
#    DOCKMESH_NO_SUDO       1 to skip sudo        (default: sudo if not root)
#    NO_COLOR               1 to disable ANSI colors
#
#  What this script does:
#    - Detects OS, architecture, distribution
#    - Preflight checks every required tool (with distro-aware install hints
#      for anything missing)
#    - Resolves latest release via the GitHub Releases API
#    - Downloads + sha256-verifies the release tarball
#    - Installs the binary to $DOCKMESH_INSTALL_DIR
#    - On upgrade: backs up the old binary, auto-restarts the systemd service
#    - On fresh install: hands off to `sudo dockmesh init` for setup
#
#  This script never touches DB/admin/systemd-unit state on fresh installs —
#  all of that lives in `dockmesh init`, which runs interactively and is
#  idempotent (safe to re-run).
# ============================================================================
set -euo pipefail

START_TS=${EPOCHREALTIME:-$SECONDS}

# ------------------------------------------------------------------
#  Color + TTY setup
#
#  The script writes UI to stderr so that `curl | bash` doesn't swallow
#  colors via pipe redirection — stderr stays a TTY when piped that way.
#  Falls back to plain text when NO_COLOR is set or TERM=dumb.
# ------------------------------------------------------------------
if [ -t 2 ] && [ -z "${NO_COLOR:-}" ] && [ "${TERM:-}" != "dumb" ]; then
  # 256-color palette — uniform across modern terminals. If the terminal
  # only supports 8 colors, these still degrade to "close enough" hues.
  BOLD=$'\033[1m'; DIM=$'\033[2m'; RST=$'\033[0m'
  FG_TITLE=$'\033[38;5;51m'       # bright cyan — headings, banner
  FG_ACCENT=$'\033[38;5;44m'      # cyan — box borders, emphasis
  FG_OK=$'\033[38;5;42m'          # green — success
  FG_WARN=$'\033[38;5;214m'       # amber — warnings
  FG_FAIL=$'\033[38;5;196m'       # red — failures
  FG_INFO=$'\033[38;5;38m'        # soft cyan — info lines
  FG_MUTED=$'\033[38;5;240m'      # gray — timestamps, paths
  CH_OK='✔'; CH_INFO='ℹ'; CH_WARN='!'; CH_FAIL='✘'; CH_STEP='▸'
else
  BOLD=; DIM=; RST=
  FG_TITLE=; FG_ACCENT=; FG_OK=; FG_WARN=; FG_FAIL=; FG_INFO=; FG_MUTED=
  CH_OK='[ok]'; CH_INFO='[i]'; CH_WARN='[!]'; CH_FAIL='[x]'; CH_STEP='>'
fi

# Pick the heaviest box-drawing characters the terminal supports. Users
# on a raw TTY (no UTF-8) get ASCII pipes — script still reads cleanly.
if [ -t 2 ] && [ -z "${NO_COLOR:-}" ] && locale 2>/dev/null | grep -qi 'utf' ; then
  BOX_TL='╭'; BOX_TR='╮'; BOX_BL='╰'; BOX_BR='╯'
  BOX_H='─'; BOX_V='│'
  RULE='━'
else
  BOX_TL='+'; BOX_TR='+'; BOX_BL='+'; BOX_BR='+'
  BOX_H='-'; BOX_V='|'
  RULE='-'
fi

BOX_WIDTH=70

# ------------------------------------------------------------------
#  Helpers
# ------------------------------------------------------------------
say()   { printf '%s\n' "$*" >&2; }
ok()    { printf '   %s%s%s %s\n' "$FG_OK" "$CH_OK" "$RST" "$*" >&2; }
info()  { printf '   %s%s%s %s\n' "$FG_INFO" "$CH_INFO" "$RST" "$*" >&2; }
warn()  { printf '   %s%s%s %s\n' "$FG_WARN" "$CH_WARN" "$RST" "$*" >&2; }
fail()  { printf '   %s%s%s %s\n' "$FG_FAIL" "$CH_FAIL" "$RST" "$*" >&2; }

die() {
  printf '\n   %s%s%s %s\n\n' "$FG_FAIL" "$CH_FAIL" "$RST" "$*" >&2
  exit 1
}

# Print a rounded box with a title line and N body lines.
# Usage: box "Title" "line1" "line2" ...
box() {
  local title="$1"; shift
  local w=$BOX_WIDTH inner=$((BOX_WIDTH - 4))
  local line
  printf '\n%s%s' "$FG_ACCENT" "$BOX_TL"
  if [ -n "$title" ]; then
    printf '%s %s%s%s %s' "$BOX_H" "$BOLD" "$title" "$RST$FG_ACCENT" ""
    # pad remaining dashes
    local used=$((${#title} + 4))
    local i
    for ((i=used; i<w-2; i++)); do printf '%s' "$BOX_H"; done
  else
    local i
    for ((i=0; i<w-2; i++)); do printf '%s' "$BOX_H"; done
  fi
  printf '%s%s\n' "$BOX_TR" "$RST"

  # blank line for breathing room
  printf '%s%s%s%*s%s%s%s\n' "$FG_ACCENT" "$BOX_V" "$RST" $((w-2)) '' "$FG_ACCENT" "$BOX_V" "$RST"

  for line in "$@"; do
    # strip ANSI codes when measuring width so padding stays accurate
    local stripped=${line//$'\033'[\[][0-9\;]*m/}
    local pad=$((w - 4 - ${#stripped}))
    [ $pad -lt 0 ] && pad=0
    printf '%s%s%s  %s%*s  %s%s%s\n' \
      "$FG_ACCENT" "$BOX_V" "$RST" \
      "$line" "$pad" '' \
      "$FG_ACCENT" "$BOX_V" "$RST"
  done

  printf '%s%s%s%*s%s%s%s\n' "$FG_ACCENT" "$BOX_V" "$RST" $((w-2)) '' "$FG_ACCENT" "$BOX_V" "$RST"
  printf '%s%s' "$FG_ACCENT" "$BOX_BL"
  local i
  for ((i=0; i<w-2; i++)); do printf '%s' "$BOX_H"; done
  printf '%s%s\n\n' "$BOX_BR" "$RST"
}

# Step N/M header with right-aligned elapsed-time placeholder.
# Usage: step N M "Title"
__step_time=0
step() {
  local n="$1" m="$2" title="$3"
  __step_time=$(get_time)
  printf '\n%s[%s/%s]%s  %s%s%s\n' \
    "$FG_TITLE" "$n" "$m" "$RST" "$BOLD" "$title" "$RST" >&2
}

# Finalize the current step — prints elapsed since `step` was called.
step_done() {
  local now=$(get_time)
  local dur
  dur=$(awk "BEGIN { printf \"%.1f\", $now - $__step_time }")
  printf '   %s%ss%s\n' "$FG_MUTED" "$dur" "$RST" >&2
}

# Sub-second timing helper. Prefer $EPOCHREALTIME (bash 5+); fall back to
# `date +%s.%N` on older bash. Returns a seconds-with-fraction string.
get_time() {
  if [ -n "${EPOCHREALTIME:-}" ]; then
    printf '%s' "$EPOCHREALTIME"
  else
    date +%s.%N 2>/dev/null || date +%s
  fi
}

# ------------------------------------------------------------------
#  Banner
# ------------------------------------------------------------------
cat >&2 <<BANNER

${FG_TITLE}${BOLD}██████╗   ██████╗   ██████╗██╗  ██╗███╗   ███╗███████╗███████╗██╗  ██╗${RST}
${FG_TITLE}${BOLD}██╔══██╗ ██╔═══██╗ ██╔════╝██║ ██╔╝████╗ ████║██╔════╝██╔════╝██║  ██║${RST}
${FG_TITLE}${BOLD}██║  ██║ ██║   ██║ ██║     █████╔╝ ██╔████╔██║█████╗  ███████╗███████║${RST}
${FG_TITLE}${BOLD}██║  ██║ ██║   ██║ ██║     ██╔═██╗ ██║╚██╔╝██║██╔══╝  ╚════██║██╔══██║${RST}
${FG_TITLE}${BOLD}██████╔╝ ╚██████╔╝ ╚██████╗██║  ██╗██║ ╚═╝ ██║███████╗███████║██║  ██║${RST}
${FG_TITLE}${BOLD}╚═════╝   ╚═════╝   ╚═════╝╚═╝  ╚═╝╚═╝     ╚═╝╚══════╝╚══════╝╚═╝  ╚═╝${RST}

   ${DIM}Single-binary Docker fleet manager · dockmesh.dev${RST}
BANNER

# ------------------------------------------------------------------
#  Inputs
# ------------------------------------------------------------------
REPO="dockmesh/dockmesh"
# DM_VERSION instead of VERSION because /etc/os-release (sourced below)
# defines $VERSION as the distro release ("12 (bookworm)" on Debian)
# which silently clobbers any local VERSION var we set.
DM_VERSION="${DOCKMESH_VERSION:-latest}"
CHANNEL="${DOCKMESH_CHANNEL:-stable}"
INSTALL_DIR="${DOCKMESH_INSTALL_DIR:-/usr/local/bin}"
USE_SUDO="sudo"
if [ "$(id -u)" = "0" ] || [ "${DOCKMESH_NO_SUDO:-0}" = "1" ]; then
  USE_SUDO=""
fi

# ------------------------------------------------------------------
#  Detect upgrade state — determines the install flow + end banner.
# ------------------------------------------------------------------
IS_UPGRADE=0
PREV_VERSION_LINE=""
PREV_VERSION=""
if [ -x "$INSTALL_DIR/dockmesh" ]; then
  if PREV_VERSION_LINE="$("$INSTALL_DIR/dockmesh" --version 2>/dev/null | head -1)"; then
    IS_UPGRADE=1
    # Extract bare version tag (first vX.Y.Z token) for the summary box.
    PREV_VERSION="$(printf '%s' "$PREV_VERSION_LINE" | grep -oE 'v[0-9]+\.[0-9]+\.[0-9]+' | head -1)"
  fi
fi

HAS_SYSTEMD_UNIT=0
SYSTEMD_ACTIVE=0
SYSTEMD_ENABLED=0
SYSTEMD_PID=""
if command -v systemctl >/dev/null 2>&1; then
  if systemctl list-unit-files dockmesh.service 2>/dev/null | grep -q '^dockmesh\.service'; then
    HAS_SYSTEMD_UNIT=1
    systemctl is-active --quiet dockmesh   && SYSTEMD_ACTIVE=1
    systemctl is-enabled --quiet dockmesh  && SYSTEMD_ENABLED=1
    SYSTEMD_PID="$(systemctl show -p MainPID --value dockmesh 2>/dev/null || true)"
    [ "$SYSTEMD_PID" = "0" ] && SYSTEMD_PID=""
  fi
fi

if [ "$IS_UPGRADE" = "1" ]; then
  printf '\n   %supgrade detected%s\n' "$FG_TITLE$BOLD" "$RST" >&2
fi

# ------------------------------------------------------------------
#  [1]  System checks
# ------------------------------------------------------------------
TOTAL_STEPS=6
[ "$IS_UPGRADE" = "1" ] && TOTAL_STEPS=5

step 1 $TOTAL_STEPS "System checks"

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$OS" in
  linux) ok "OS              linux" ;;
  darwin) die "macOS binary not shipped yet — build from source: https://github.com/$REPO" ;;
  *) die "unsupported OS: $OS" ;;
esac

ARCH_RAW="$(uname -m)"
case "$ARCH_RAW" in
  x86_64|amd64) ARCH="amd64"; ok "Architecture    amd64 ($ARCH_RAW)" ;;
  aarch64|arm64) ARCH="arm64"; ok "Architecture    arm64 ($ARCH_RAW)" ;;
  *) die "unsupported architecture: $ARCH_RAW" ;;
esac

# Distro detection — drives the "to install X, run Y" hints for any
# missing dependency. os-release is the de-facto cross-distro standard.
DISTRO_ID="unknown"
DISTRO_NAME="$ARCH_RAW Linux"
if [ -r /etc/os-release ]; then
  # shellcheck disable=SC1091
  . /etc/os-release
  DISTRO_ID="${ID:-unknown}"
  DISTRO_NAME="${PRETTY_NAME:-$DISTRO_ID}"
fi
ok "Distribution    $DISTRO_NAME"

# install_hint <toolname>  — echo a distro-appropriate install command.
install_hint() {
  local tool="$1"
  case "$DISTRO_ID" in
    ubuntu|debian|linuxmint|pop|raspbian)
      printf 'sudo apt update && sudo apt install -y %s' "$tool" ;;
    fedora|rhel|centos|rocky|almalinux|ol)
      printf 'sudo dnf install -y %s' "$tool" ;;
    alpine)
      printf 'sudo apk add %s' "$tool" ;;
    arch|manjaro|endeavouros)
      printf 'sudo pacman -S --noconfirm %s' "$tool" ;;
    opensuse*|sles|sled)
      printf 'sudo zypper install -y %s' "$tool" ;;
    *)
      printf 'install "%s" via your package manager' "$tool" ;;
  esac
}

# Distro-specific package-name overrides. e.g. sha256sum is coreutils.
package_for() {
  local tool="$1"
  case "$tool" in
    sha256sum) echo "coreutils" ;;
    *) echo "$tool" ;;
  esac
}

# require_tool <cmd>  — fail with a clean distro-aware hint.
require_tool() {
  local cmd="$1" pkg
  if command -v "$cmd" >/dev/null 2>&1; then
    # Take just the first two tokens of the first version line.
    # curl --version otherwise dumps the full feature list on one line,
    # which blows out the column alignment. "curl 7.88.1" is enough.
    local ver
    ver="$("$cmd" --version 2>/dev/null | head -1 | awk '{print $1, $2}' || echo '')"
    ok "$(printf '%-16s %s' "$cmd" "${ver:-installed}")"
    return 0
  fi
  pkg="$(package_for "$cmd")"
  fail "$(printf '%-16s not found' "$cmd")"
  printf '\n' >&2
  box "Missing required tool: $cmd" \
    "$cmd is required to continue." \
    "" \
    "Install on $DISTRO_NAME:" \
    "  $(install_hint "$pkg")" \
    "" \
    "Then re-run:" \
    "  curl -fsSL https://get.dockmesh.dev | bash"
  exit 1
}

require_tool curl
require_tool tar
require_tool sha256sum

# systemctl is strongly recommended (90% of hosts have it), but not a
# hard fail — the binary runs fine without systemd, we just lose auto-
# start and the auto-restart-on-upgrade behaviour.
if command -v systemctl >/dev/null 2>&1; then
  ok "$(printf '%-16s %s' "systemctl" "$(systemctl --version | head -1 | awk '{print $1, $2}')")"
else
  warn "$(printf '%-16s %s' "systemctl" "not found — auto-start on boot will need manual setup")"
fi

# $INSTALL_DIR writable? If the script is root we're fine; else need sudo.
if [ -w "$INSTALL_DIR" ]; then
  ok "$(printf '%-16s writable' "$INSTALL_DIR")"
elif [ -n "$USE_SUDO" ]; then
  ok "$(printf '%-16s writable (via sudo)' "$INSTALL_DIR")"
else
  die "$INSTALL_DIR is not writable and sudo is disabled — set DOCKMESH_INSTALL_DIR to a writable path"
fi

# Docker is a soft-warn. Dockmesh manages Docker — without it, 99% of
# the app is useless, but we let the install proceed so people can set
# up a fresh host in any order.
if command -v docker >/dev/null 2>&1; then
  DOCKER_VER="$(docker --version 2>/dev/null | awk '{print $3}' | tr -d ',' || true)"
  if docker info >/dev/null 2>&1; then
    ok "$(printf '%-16s %s  (running)' "Docker" "${DOCKER_VER:-installed}")"
  else
    warn "$(printf '%-16s installed but daemon is not responding' "Docker")"
  fi
else
  warn "$(printf '%-16s %s' "Docker" "not detected — install before first deploy")"
  say  "                    https://docs.docker.com/engine/install/"
fi

# Port availability for fresh installs. Upgrade case: the ports are
# presumably already ours, don't complain.
if [ "$IS_UPGRADE" = "0" ]; then
  port_free() {
    local p="$1"
    if command -v ss >/dev/null 2>&1; then
      ! ss -Htln "sport = :$p" 2>/dev/null | grep -q .
    elif command -v netstat >/dev/null 2>&1; then
      ! netstat -tln 2>/dev/null | awk '{print $4}' | grep -Eq ":$p$"
    else
      return 0
    fi
  }
  for p in 8080 8443; do
    if port_free "$p"; then
      ok "$(printf ':%s  free' "$p")"
    else
      warn "$(printf ':%s  in use — Dockmesh default will conflict, adjust in init' "$p")"
    fi
  done
else
  ok "existing install    $INSTALL_DIR/dockmesh (${PREV_VERSION:-unknown})"
  if [ "$HAS_SYSTEMD_UNIT" = "1" ]; then
    local_state="installed"
    [ "$SYSTEMD_ENABLED" = "1" ] && local_state="enabled"
    [ "$SYSTEMD_ACTIVE" = "1" ] && local_state="$local_state, active"
    [ -n "$SYSTEMD_PID" ] && local_state="$local_state, PID $SYSTEMD_PID"
    ok "systemd unit        dockmesh.service ($local_state)"
  else
    info "no systemd unit     manual restart required after upgrade"
  fi
fi
step_done

# ------------------------------------------------------------------
#  [2]  Resolve release
# ------------------------------------------------------------------
step 2 $TOTAL_STEPS "Resolving release"
info "channel         $CHANNEL"

if [ "$DM_VERSION" = "latest" ]; then
  META_URL="https://api.github.com/repos/$REPO/releases/latest"
  if [ "$CHANNEL" = "testing" ]; then
    # 'testing' means "include pre-releases" — pull the full list and
    # take the top entry regardless of draft/prerelease status.
    META_URL="https://api.github.com/repos/$REPO/releases?per_page=1"
  fi
  if ! META="$(curl -fsSL --retry 3 --retry-delay 2 "$META_URL" 2>&1)"; then
    die "failed to query $META_URL — check network + that the repo is public"
  fi
  DM_VERSION="$(printf '%s' "$META" | grep -oE '"tag_name"\s*:\s*"[^"]+"' | head -1 | sed -E 's/.*"([^"]+)"$/\1/')"
  [ -z "$DM_VERSION" ] && die "could not parse latest release tag from GitHub API response"
fi
ok "latest          $DM_VERSION"

TARBALL="dockmesh_linux_${ARCH}.tar.gz"
URL="https://github.com/$REPO/releases/download/$DM_VERSION/$TARBALL"
CHECKSUMS_URL="https://github.com/$REPO/releases/download/$DM_VERSION/checksums.txt"
info "artifact        $TARBALL"
step_done

# ------------------------------------------------------------------
#  [3]  Download
# ------------------------------------------------------------------
step 3 $TOTAL_STEPS "Downloading"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# curl -# prints a hashmark progress bar that works inside `curl | bash`
# pipelines (unlike --progress-bar which needs a TTY on stdout). The
# redirection 2>&1 captures stderr-progress onto stderr of this script,
# which is what the user actually sees.
if ! curl -fL --retry 3 --retry-delay 2 -# "$URL" -o "$TMP/$TARBALL" 2>&1 >/dev/null ; then
  die "download failed — verify the release exists: $URL"
fi
step_done

# ------------------------------------------------------------------
#  [4]  Verify checksum
# ------------------------------------------------------------------
step 4 $TOTAL_STEPS "Verifying"
if curl -fsSL --retry 2 -o "$TMP/checksums.txt" "$CHECKSUMS_URL" 2>/dev/null; then
  EXPECTED="$(grep " ${TARBALL}$" "$TMP/checksums.txt" | awk '{print $1}' || true)"
  if [ -n "$EXPECTED" ]; then
    ACTUAL="$(sha256sum "$TMP/$TARBALL" | awk '{print $1}')"
    if [ "$EXPECTED" != "$ACTUAL" ]; then
      die "checksum mismatch — expected $EXPECTED got $ACTUAL"
    fi
    ok "sha256          ${EXPECTED:0:16}…${EXPECTED: -16}"
  else
    warn "no checksum entry for $TARBALL — continuing without verification"
  fi
else
  warn "no checksums.txt published for $DM_VERSION — continuing without verification"
fi
step_done

# ------------------------------------------------------------------
#  [5]  Install (fresh) or Upgrade (with backup)
# ------------------------------------------------------------------
if [ "$IS_UPGRADE" = "1" ]; then
  step 5 $TOTAL_STEPS "Upgrading"
  tar -xzf "$TMP/$TARBALL" -C "$TMP"
  [ -x "$TMP/dockmesh" ] || die "tarball missing 'dockmesh' binary"

  # Backup the existing binary so the user has a tested rollback path.
  # Using cp (not mv) preserves the running binary for in-flight requests
  # — most kernels hold the old inode until all fds close.
  if $USE_SUDO cp -a "$INSTALL_DIR/dockmesh" "$INSTALL_DIR/dockmesh.bak" 2>/dev/null; then
    ok "backup          $INSTALL_DIR/dockmesh.bak  ($PREV_VERSION)"
  else
    warn "backup failed — rolling forward without rollback safety net"
  fi

  $USE_SUDO install -m 0755 "$TMP/dockmesh" "$INSTALL_DIR/dockmesh"
  NEW_VERSION_LINE="$("$INSTALL_DIR/dockmesh" --version 2>/dev/null | head -1 || echo "$DM_VERSION")"
  ok "replaced        $INSTALL_DIR/dockmesh      ($DM_VERSION)"

  if [ "$HAS_SYSTEMD_UNIT" = "1" ]; then
    info "restarting dockmesh.service..."
    RESTART_T0=$(get_time)
    if $USE_SUDO systemctl restart dockmesh 2>/dev/null; then
      # Wait for the service to come back healthy. We probe the systemd
      # state rather than HTTP because the listen port is whatever the
      # user configured — we don't know it from here.
      for i in 1 2 3 4 5 6 7 8 9 10; do
        sleep 0.5
        systemctl is-active --quiet dockmesh && break
      done
      RESTART_T1=$(get_time)
      DOWNTIME=$(awk "BEGIN { printf \"%.1f\", $RESTART_T1 - $RESTART_T0 }")
      if systemctl is-active --quiet dockmesh; then
        ok "service restarted"
        ok "health OK       downtime ${DOWNTIME}s"
      else
        fail "service failed to start — see: journalctl -u dockmesh --since '1 min ago'"
      fi
    else
      warn "restart failed — run manually: $USE_SUDO systemctl restart dockmesh"
    fi
  fi
  step_done

  box "Upgraded  ${PREV_VERSION:-prev} → $DM_VERSION" \
    "" \
    "Data, stacks, and configuration are untouched." \
    "" \
    "Rollback (keeps data, reverts the binary):" \
    "  $USE_SUDO mv $INSTALL_DIR/dockmesh.bak $INSTALL_DIR/dockmesh" \
    "  $USE_SUDO systemctl restart dockmesh"
  exit 0
fi

# Fresh install path.
step 5 $TOTAL_STEPS "Installing"
tar -xzf "$TMP/$TARBALL" -C "$TMP"
[ -x "$TMP/dockmesh" ] || die "tarball missing 'dockmesh' binary"
$USE_SUDO install -m 0755 "$TMP/dockmesh" "$INSTALL_DIR/dockmesh"
ok "binary          $INSTALL_DIR/dockmesh"
ok "mode            0755 (root:root)"
step_done

# ------------------------------------------------------------------
#  [6]  Summary / next step
# ------------------------------------------------------------------
step 6 $TOTAL_STEPS "Ready"
INSTALLED="$("$INSTALL_DIR/dockmesh" --version 2>/dev/null | head -1 || echo "$DM_VERSION")"
ok "installed       $INSTALLED"
TOTAL_ELAPSED=$(awk "BEGIN { printf \"%.1f\", $(get_time) - $START_TS }")
printf '   %s%ss total%s\n' "$FG_MUTED" "$TOTAL_ELAPSED" "$RST" >&2

box "Next step" \
  "" \
  "  $USE_SUDO dockmesh init" \
  "" \
  "Guided wizard — data dir, admin user, listen port, systemd unit." \
  "Everything defaults to sane values. ~2 minutes."

printf '   %sDocs%s     https://dockmesh.dev/docs\n'   "$DIM" "$RST" >&2
printf '   %sIssues%s   https://github.com/%s/issues\n\n' "$DIM" "$RST" "$REPO" >&2
