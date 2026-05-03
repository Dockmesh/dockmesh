#!/usr/bin/env bash
# ============================================================================
#  Dockmesh interactive uninstaller.
#
#  Usage:
#    curl -fsSL https://get.dockmesh.dev/uninstall | sudo bash
#    curl -fsSL https://get.dockmesh.dev/uninstall | sudo bash -s -- --yes
#    curl -fsSL https://get.dockmesh.dev/uninstall | sudo bash -s -- --purge --yes
#
#  Flags:
#    --yes       Accept every default (safe defaults — keeps data dir).
#    --purge     Also remove the data directory (DB, stacks, age keys).
#                Implies --yes. CANNOT BE UNDONE.
#    --dry-run   Print every action without executing.
#    NO_COLOR=1  Disable ANSI colors.
#
#  What this script NEVER does:
#    - Touches running containers. Stacks deployed through dockmesh keep
#      running after uninstall; you manage them manually from then on.
#    - Removes Docker itself.
#    - Removes files outside the dockmesh data dir, asset dir, binary
#      dir, and service manager directories.
# ============================================================================
set -euo pipefail

export LC_ALL=C

# ---------------------------------------------------------------------------
#  Color / TTY (mirrors install.sh so the two feel like one product).
# ---------------------------------------------------------------------------
if [ -t 2 ] && [ -z "${NO_COLOR:-}" ] && [ "${TERM:-}" != "dumb" ]; then
  BOLD=$'\033[1m'; DIM=$'\033[2m'; RST=$'\033[0m'
  FG_TITLE=$'\033[38;5;51m'
  FG_ACCENT=$'\033[38;5;44m'
  FG_OK=$'\033[38;5;42m'
  FG_WARN=$'\033[38;5;214m'
  FG_FAIL=$'\033[38;5;196m'
  FG_INFO=$'\033[38;5;38m'
  FG_MUTED=$'\033[38;5;240m'
  CH_OK='✔'; CH_INFO='ℹ'; CH_WARN='!'; CH_FAIL='✘'
else
  BOLD=; DIM=; RST=
  FG_TITLE=; FG_ACCENT=; FG_OK=; FG_WARN=; FG_FAIL=; FG_INFO=; FG_MUTED=
  CH_OK='[ok]'; CH_INFO='[i]'; CH_WARN='[!]'; CH_FAIL='[x]'
fi

say()  { printf '%s\n' "$*" >&2; }
ok()   { printf '   %s%s%s %s\n' "$FG_OK" "$CH_OK" "$RST" "$*" >&2; }
info() { printf '   %s%s%s %s\n' "$FG_INFO" "$CH_INFO" "$RST" "$*" >&2; }
warn() { printf '   %s%s%s %s\n' "$FG_WARN" "$CH_WARN" "$RST" "$*" >&2; }
fail() { printf '   %s%s%s %s\n' "$FG_FAIL" "$CH_FAIL" "$RST" "$*" >&2; }
die()  { printf '\n   %s%s%s %s\n\n' "$FG_FAIL" "$CH_FAIL" "$RST" "$*" >&2; exit 1; }

step() {
  printf '\n%s━━━━  %s%s%s  ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n\n' \
    "$FG_TITLE" "$BOLD" "$*" "$RST" >&2
}

# ---------------------------------------------------------------------------
#  Args
# ---------------------------------------------------------------------------
YES=0
PURGE=0
DRYRUN=0
for arg in "$@"; do
  case "$arg" in
    --yes)     YES=1 ;;
    --purge)   PURGE=1; YES=1 ;;
    --dry-run) DRYRUN=1 ;;
    -h|--help)
      sed -n '2,26p' "$0" | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    *) die "unknown flag: $arg" ;;
  esac
done

# ---------------------------------------------------------------------------
#  Root check — most removals need root and the messaging is cleanest
#  if we fail up front rather than after the first "permission denied".
# ---------------------------------------------------------------------------
if [ "$(id -u)" != "0" ]; then
  die "This installer must run as root. Re-run with sudo:
     curl -fsSL https://get.dockmesh.dev/uninstall | sudo bash"
fi

# ---------------------------------------------------------------------------
#  Runner — respects --dry-run and logs every command
# ---------------------------------------------------------------------------
run() {
  if [ "$DRYRUN" = "1" ]; then
    printf '   %s[dry-run]%s %s\n' "$FG_MUTED" "$RST" "$*" >&2
    return 0
  fi
  "$@"
}

# ---------------------------------------------------------------------------
#  Banner
# ---------------------------------------------------------------------------
cat >&2 <<BANNER

${FG_TITLE}${BOLD}  Dockmesh uninstaller${RST}
${DIM}  Interactive — you pick what gets removed.${RST}
BANNER

# ---------------------------------------------------------------------------
#  Detect install layout
# ---------------------------------------------------------------------------
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$OS" in
  linux|darwin) ;;
  *) die "unsupported OS: $OS" ;;
esac

BIN_DIR="${DOCKMESH_INSTALL_DIR:-/usr/local/bin}"
BIN_PATH="$BIN_DIR/dockmesh"

DATA_DIR_DEFAULT="/var/lib/dockmesh"
[ "$OS" = "darwin" ] && DATA_DIR_DEFAULT="/usr/local/var/dockmesh"

# Env file lives at <data_dir>/dockmesh.env. Use it to discover the
# *actual* data + asset paths the install used, rather than assuming
# the defaults — an operator might have passed DOCKMESH_DATA_DIR or
# similar at install time.
DATA_DIR="$DATA_DIR_DEFAULT"
ASSET_DIR=""
# The detection list covers the platform defaults plus /data (a common
# operator override on VMs where /data is a separate, expandable
# volume — install.sh / dockmesh init both accept --data-dir /data).
# Without /data here, an operator who ran with that flag would see
# "skipping for safety" and the install would survive uninstall.
for candidate in \
  "/var/lib/dockmesh/dockmesh.env" \
  "/usr/local/var/dockmesh/dockmesh.env" \
  "/opt/dockmesh/dockmesh.env" \
  "/data/dockmesh.env"
do
  if [ -f "$candidate" ]; then
    DATA_DIR="$(dirname "$candidate")"
    # shellcheck disable=SC1090
    set +u; . "$candidate"; set -u
    if [ -n "${DOCKMESH_INSTALL_SCRIPT:-}" ]; then
      ASSET_DIR="$(dirname "${DOCKMESH_INSTALL_SCRIPT}")"
    fi
    if [ -n "${DOCKMESH_BINARY_DIR:-}" ]; then
      ASSET_DIR="$(dirname "${DOCKMESH_BINARY_DIR}")"
    fi
    break
  fi
done
# Fallback for asset dir if env file didn't have it.
if [ -z "$ASSET_DIR" ]; then
  ASSET_DIR="$(dirname "$BIN_DIR")/share/dockmesh"
fi

# Service file / label per platform.
SYSTEMD_UNIT="/etc/systemd/system/dockmesh.service"
LAUNCHD_PLIST="/Library/LaunchDaemons/dev.dockmesh.service.plist"
LAUNCHD_LABEL="system/dev.dockmesh.service"

# ---------------------------------------------------------------------------
#  Show plan
# ---------------------------------------------------------------------------
step "Detected install"
ok "OS                  $OS"
ok "Binary              $BIN_PATH"
ok "Data directory      $DATA_DIR"
ok "Asset directory     $ASSET_DIR"
case "$OS" in
  linux)  ok "Service unit        $SYSTEMD_UNIT" ;;
  darwin) ok "LaunchDaemon plist  $LAUNCHD_PLIST" ;;
esac

# ---------------------------------------------------------------------------
#  Interactive choices (skipped when --yes)
# ---------------------------------------------------------------------------
ask() {
  # ask "prompt" "default [Y/n]"  → echoes 1 / 0
  local prompt="$1" default="$2"
  if [ "$YES" = "1" ]; then
    case "$default" in
      y|Y) echo 1 ;;
      *) echo 0 ;;
    esac
    return
  fi
  local hint
  case "$default" in
    y|Y) hint="[Y/n]" ;;
    *)   hint="[y/N]" ;;
  esac
  printf '   %s %s: ' "$prompt" "$hint" >&2
  local ans=""
  read -r ans </dev/tty || ans=""
  case "${ans,,}" in
    y|yes) echo 1 ;;
    n|no)  echo 0 ;;
    '')    case "$default" in y|Y) echo 1 ;; *) echo 0 ;; esac ;;
    *)     echo 0 ;;
  esac
}

step "Choose what to remove"

DO_STOP=$(ask "Stop the running service?"                  "y")
DO_UNIT=$(ask "Remove service unit / plist?"               "y")
DO_BIN=$(ask  "Remove the dockmesh binary?"                "y")
DO_ASSET=$(ask "Remove agent installer + bundled binaries?" "y")

if [ "$PURGE" = "1" ]; then
  DO_DATA=1
else
  DO_DATA=$(ask "Remove data directory (DB, stacks, age-keys)? ${FG_WARN}DESTRUCTIVE — cannot be undone${RST}" "n")
fi

DO_USER=0
if [ "$OS" = "linux" ]; then
  if id dockmesh >/dev/null 2>&1; then
    DO_USER=$(ask "Remove the 'dockmesh' service user?" "y")
  fi
fi

# ---------------------------------------------------------------------------
#  Running-stack warning — we leave containers alone, but the operator
#  should know that before we tear down the management layer.
# ---------------------------------------------------------------------------
if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
  MANAGED_COUNT=$(docker ps --filter "label=com.docker.compose.project" --format '{{ .ID }}' 2>/dev/null | wc -l | tr -d ' ')
  if [ "${MANAGED_COUNT:-0}" -gt 0 ]; then
    step "Containers still running"
    warn "$MANAGED_COUNT container(s) with compose-project labels are running on this host."
    warn "The uninstaller will NOT stop them — they keep running after dockmesh is gone."
    warn "Manage them with plain 'docker compose' from their stack directories going forward."
  fi
fi

# ---------------------------------------------------------------------------
#  Execute
# ---------------------------------------------------------------------------
step "Executing"

# Stop service.
if [ "$DO_STOP" = "1" ]; then
  case "$OS" in
    linux)
      if command -v systemctl >/dev/null 2>&1 && systemctl list-unit-files dockmesh.service >/dev/null 2>&1; then
        run systemctl disable --now dockmesh.service 2>/dev/null || true
        ok "stopped + disabled dockmesh.service"
      else
        info "no systemd unit to stop"
      fi
      ;;
    darwin)
      if [ -f "$LAUNCHD_PLIST" ]; then
        run launchctl bootout "$LAUNCHD_LABEL" 2>/dev/null || true
        ok "unloaded $LAUNCHD_LABEL"
      else
        info "no LaunchDaemon plist to stop"
      fi
      ;;
  esac
  # Kill stragglers — someone may have started 'dockmesh serve' manually.
  if pgrep -f '/usr/local/bin/dockmesh' >/dev/null 2>&1; then
    run pkill -f '/usr/local/bin/dockmesh' 2>/dev/null || true
    ok "killed lingering dockmesh processes"
  fi
fi

# Remove service unit / plist.
if [ "$DO_UNIT" = "1" ]; then
  case "$OS" in
    linux)
      if [ -f "$SYSTEMD_UNIT" ]; then
        run rm -f "$SYSTEMD_UNIT"
        run systemctl daemon-reload 2>/dev/null || true
        ok "removed $SYSTEMD_UNIT"
      fi
      ;;
    darwin)
      if [ -f "$LAUNCHD_PLIST" ]; then
        run rm -f "$LAUNCHD_PLIST"
        ok "removed $LAUNCHD_PLIST"
      fi
      ;;
  esac
fi

# Remove binary.
if [ "$DO_BIN" = "1" ]; then
  if [ -f "$BIN_PATH" ]; then
    run rm -f "$BIN_PATH"
    ok "removed $BIN_PATH"
  fi
  # Also the upgrade backup, if any.
  if [ -f "$BIN_PATH.bak" ]; then
    run rm -f "$BIN_PATH.bak"
    ok "removed $BIN_PATH.bak"
  fi
  # dmctl is part of the release tarball; clean it up too.
  if [ -f "$BIN_DIR/dmctl" ]; then
    run rm -f "$BIN_DIR/dmctl"
    ok "removed $BIN_DIR/dmctl"
  fi
fi

# Remove asset directory.
if [ "$DO_ASSET" = "1" ]; then
  if [ -d "$ASSET_DIR" ]; then
    run rm -rf "$ASSET_DIR"
    ok "removed $ASSET_DIR"
  fi
fi

# Remove data directory — triple-check we're pointed at something that
# actually LOOKS like a dockmesh data dir before unlinking. An operator
# who typoed a custom path shouldn't nuke /home by accident.
if [ "$DO_DATA" = "1" ]; then
  if [ -d "$DATA_DIR" ]; then
    LOOKS_LIKE_DOCKMESH=0
    [ -d "$DATA_DIR/data" ]   && LOOKS_LIKE_DOCKMESH=1
    [ -d "$DATA_DIR/stacks" ] && LOOKS_LIKE_DOCKMESH=1
    [ -f "$DATA_DIR/dockmesh.env" ] && LOOKS_LIKE_DOCKMESH=1
    if [ "$LOOKS_LIKE_DOCKMESH" = "1" ]; then
      # When the data dir is a dedicated mount (e.g. /data on a separate
      # volume), `rm -rf $DATA_DIR` would fail with "Device or resource
      # busy" because the mountpoint itself can't be removed. Wipe the
      # contents in that case and leave the empty mountpoint behind.
      # Otherwise (regular subdir), nuke the whole tree.
      if command -v mountpoint >/dev/null 2>&1 && mountpoint -q "$DATA_DIR"; then
        run sh -c "rm -rf \"$DATA_DIR\"/* \"$DATA_DIR\"/.[!.]* \"$DATA_DIR\"/..?* 2>/dev/null || true"
        ok "wiped contents of $DATA_DIR (mountpoint preserved)"
      else
        run rm -rf "$DATA_DIR"
        ok "removed $DATA_DIR (DB, stacks, keys, env)"
      fi
    else
      warn "$DATA_DIR exists but doesn't look like a dockmesh data dir — skipping for safety"
    fi
  fi
fi

# Remove service user. Two fixes for prior bugs:
#   1. systemctl stop sends SIGTERM and waits up to ~10s for the
#      process to exit. Doing userdel right after sometimes raced —
#      userdel refuses while any process owns the UID. We pkill -u
#      dockmesh first to clear any straggler before deleting.
#   2. The previous version emitted `ok "removed user"` unconditionally,
#      even when userdel had failed and warn already fired — claiming
#      success when none happened. Now: only emit ok when userdel
#      actually returns 0.
if [ "$DO_USER" = "1" ] && [ "$OS" = "linux" ]; then
  if id dockmesh >/dev/null 2>&1; then
    pkill -KILL -u dockmesh 2>/dev/null || true
    if run userdel dockmesh 2>/dev/null; then
      ok "removed 'dockmesh' system user"
    else
      warn "userdel dockmesh failed — try manually: sudo pkill -KILL -u dockmesh && sudo userdel dockmesh"
    fi
  fi
fi

# ---------------------------------------------------------------------------
#  Summary
# ---------------------------------------------------------------------------
step "Done"
if [ "$DO_DATA" = "1" ]; then
  say "   $FG_WARN${BOLD}Data directory wiped.${RST} All stacks, deployment history, audit log,"
  say "   age-encryption keys, and backups metadata are gone. If you had encrypted"
  say "   backups stored elsewhere, you can no longer decrypt them — the age key"
  say "   was inside the data dir."
else
  say "   Data directory preserved at ${BOLD}$DATA_DIR${RST}."
  say "   Reinstall later with:"
  say "     curl -fsSL https://get.dockmesh.dev | sudo bash && sudo dockmesh init"
  say "   …and your DB + stacks + keys will be picked back up."
fi
say ""
say "   Running containers were $([ "${MANAGED_COUNT:-0}" -gt 0 ] && echo left alone || echo "not found")."
say ""
