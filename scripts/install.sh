#!/usr/bin/env bash
# ============================================================================
#  Dockmesh one-line installer.
#
#  Usage:
#    curl -fsSL https://get.dockmesh.dev | sudo bash
#    curl -fsSL https://get.dockmesh.dev | DOCKMESH_VERSION=v1.2.3 bash
#    curl -fsSL https://get.dockmesh.dev | DOCKMESH_INSTALL_DIR=/opt/bin bash
#
#  Env vars:
#    DOCKMESH_VERSION       tag to install (default: latest release)
#    DOCKMESH_CHANNEL       stable | testing      (default: stable)
#    DOCKMESH_INSTALL_DIR   bin directory         (default: /usr/local/bin)
#    DOCKMESH_NO_SUDO       1 to skip sudo        (default: sudo if not root)
#    DOCKMESH_FORCE         1 to reinstall even if already on latest version
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

# Pin numeric formatting to POSIX "C" locale. Without this, `awk printf
# "%.1f"` honours the user's LC_NUMERIC — so on a German/French/etc
# system it prints "0,6" instead of "0.6". That captured value then
# gets interpolated into the next awk expression as `if (0,6>0)`, which
# awk parses as a comma-separated argument list and aborts with
# "syntax error … context is BEGIN { if (0,". Affects every non-US
# locale, not just macOS — just more visible there because bash 3.2
# also forced the BSD-date fallback path.
export LC_ALL=C

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
    # Strip ANSI sequences so ${#} measures visible width.
    # The body line format is:
    #   │<2sp><content><pad><2sp>│   → 1+2+N+2+1 = N+6 = w, so N = w-6.
    # Previously we used w-4 which made every body line 2 chars too wide
    # and left the right border misaligned.
    local stripped
    stripped=$(printf '%s' "$line" | sed -E $'s/\033\\[[0-9;]*m//g')
    local pad=$((w - 6 - ${#stripped}))
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
    # BSD date (macOS, bash 3.2 default) doesn't support %N — it silently
    # emits a literal "N", which then poisons every downstream awk math
    # expression ("1745345678.N - …" = syntax error). Detect that and
    # drop to whole-second precision rather than printing garbage.
    local t
    t=$(date +%s.%N 2>/dev/null || date +%s)
    case "$t" in
      *N|*.N) date +%s ;;
      *) printf '%s' "$t" ;;
    esac
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
# Agent-enrollment assets live next to the install root, matching the
# LSB-ish layout: bin → /usr/local/bin, assets → /usr/local/share.
# Derived from INSTALL_DIR so `DOCKMESH_INSTALL_DIR=/opt/bin` places
# assets at /opt/share/dockmesh. User can override via DOCKMESH_ASSET_DIR.
ASSET_DIR="${DOCKMESH_ASSET_DIR:-$(dirname "$INSTALL_DIR")/share/dockmesh}"
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
HAS_LAUNCHD_UNIT=0
LAUNCHD_ACTIVE=0
if command -v systemctl >/dev/null 2>&1; then
  if systemctl list-unit-files dockmesh.service 2>/dev/null | grep -q '^dockmesh\.service'; then
    HAS_SYSTEMD_UNIT=1
    systemctl is-active --quiet dockmesh   && SYSTEMD_ACTIVE=1
    systemctl is-enabled --quiet dockmesh  && SYSTEMD_ENABLED=1
    SYSTEMD_PID="$(systemctl show -p MainPID --value dockmesh 2>/dev/null || true)"
    [ "$SYSTEMD_PID" = "0" ] && SYSTEMD_PID=""
  fi
fi
# macOS equivalent: probe the LaunchDaemon plist + whether launchd sees
# the service. Stays 0 on Linux because launchctl doesn't exist there.
if [ "$(uname -s)" = "Darwin" ] && command -v launchctl >/dev/null 2>&1; then
  if [ -f /Library/LaunchDaemons/dev.dockmesh.service.plist ]; then
    HAS_LAUNCHD_UNIT=1
    launchctl print system/dev.dockmesh.service >/dev/null 2>&1 && LAUNCHD_ACTIVE=1
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
  linux)  ok "OS              linux" ;;
  darwin) ok "OS              macOS ($(sw_vers -productVersion 2>/dev/null || echo unknown))"
          # Data + install paths differ on macOS — Homebrew convention.
          # systemctl doesn't exist; launchd handles the service story.
          : "${DOCKMESH_INSTALL_DIR:=/usr/local/bin}"
          ;;
  *) die "unsupported OS: $OS" ;;
esac

ARCH_RAW="$(uname -m)"
case "$ARCH_RAW" in
  x86_64|amd64) ARCH="amd64"; ok "Architecture    amd64 ($ARCH_RAW)" ;;
  aarch64|arm64) ARCH="arm64"; ok "Architecture    arm64 ($ARCH_RAW)" ;;
  *) die "unsupported architecture: $ARCH_RAW" ;;
esac

# macOS PATH handling. Under `curl | sudo bash` we inherit sudo's
# secure_path, which on macOS typically omits both Homebrew prefixes:
#   /opt/homebrew/bin  (Apple Silicon default)
#   /usr/local/bin     (Intel default, also INSTALL_DIR target)
# Without these prepended, brew-installed deps (coreutils → sha256sum,
# modern bash, etc.) are invisible to require_tool. Prepend both so
# detection is consistent regardless of Mac CPU.
if [ "$OS" = "darwin" ]; then
  export PATH="/opt/homebrew/bin:/opt/homebrew/sbin:/usr/local/bin:/usr/local/sbin:$PATH"
fi

# Distro detection — drives the "to install X, run Y" hints for any
# missing dependency. os-release is the de-facto cross-distro standard
# on Linux; macOS uses sw_vers / Homebrew instead.
DISTRO_ID="unknown"
DISTRO_NAME="$ARCH_RAW"
if [ "$OS" = "darwin" ]; then
  DISTRO_ID="macos"
  DISTRO_NAME="macOS $(sw_vers -productVersion 2>/dev/null || echo)"
elif [ -r /etc/os-release ]; then
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
    macos)
      printf 'brew install %s' "$tool" ;;
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
    # Extract just the version number (e.g. 7.88.1, 9.1, 1.34) from the
    # --version output. Using the first two tokens was naive — on GNU
    # tools the second token is often a parenthesised vendor name
    # ("tar (GNU tar) 1.34" → "tar (GNU", ugly). Regex-pick the first
    # digit.digit[.digit] group instead.
    local line ver
    line="$("$cmd" --version 2>/dev/null | head -1 || true)"
    ver="$(printf '%s' "$line" | grep -oE '[0-9]+\.[0-9]+(\.[0-9]+)?' | head -1)"
    if [ -z "$ver" ]; then
      ver="installed"
    fi
    ok "$(printf '%-16s %s' "$cmd" "$ver")"
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
    "  curl -fsSL https://get.dockmesh.dev | sudo bash"
  exit 1
}

require_tool curl
require_tool tar
# sha256 tool — sha256sum on Linux coreutils, `shasum -a 256` on macOS /
# BSD. Pick whichever is available and wrap it behind a shared helper so
# the rest of the script stays portable.
if command -v sha256sum >/dev/null 2>&1; then
  ok "$(printf '%-16s %s' "sha256sum" "$(sha256sum --version 2>/dev/null | head -1 | grep -oE '[0-9]+\.[0-9]+' | head -1 || echo installed)")"
  sha256_of() { sha256sum "$1" | awk '{print $1}'; }
elif command -v shasum >/dev/null 2>&1; then
  ok "$(printf '%-16s %s' "shasum" "$(shasum --version 2>/dev/null | head -1 || echo installed) (sha256)")"
  sha256_of() { shasum -a 256 "$1" | awk '{print $1}'; }
else
  fail "$(printf '%-16s not found' "sha256sum/shasum")"
  printf '\n' >&2
  box "Missing sha256 tool" \
    "Need either 'sha256sum' (Linux coreutils) or 'shasum' (macOS)." \
    "" \
    "Install on $DISTRO_NAME:" \
    "  $(install_hint coreutils)" \
    "" \
    "Then re-run:" \
    "  curl -fsSL https://get.dockmesh.dev | sudo bash"
  exit 1
fi

# Service manager preflight — systemd on Linux, launchd on macOS.
# Neither is a hard requirement; the binary runs fine without one,
# we just lose auto-start + auto-restart-on-upgrade in that case.
if [ "$OS" = "darwin" ]; then
  if command -v launchctl >/dev/null 2>&1; then
    ok "$(printf '%-16s %s' "launchctl" "present")"
  else
    warn "$(printf '%-16s %s' "launchctl" "not found — auto-start on boot will need manual setup")"
  fi
else
  if command -v systemctl >/dev/null 2>&1; then
    ok "$(printf '%-16s %s' "systemctl" "$(systemctl --version | head -1 | awk '{print $1, $2}')")"
  else
    warn "$(printf '%-16s %s' "systemctl" "not found — auto-start on boot will need manual setup")"
  fi
fi

# $INSTALL_DIR writable? If the script is root we're fine; else need sudo.
if [ -w "$INSTALL_DIR" ]; then
  ok "$(printf '%-16s writable' "$INSTALL_DIR")"
elif [ -n "$USE_SUDO" ]; then
  ok "$(printf '%-16s writable (via sudo)' "$INSTALL_DIR")"
else
  die "$INSTALL_DIR is not writable and sudo is disabled — set DOCKMESH_INSTALL_DIR to a writable path"
fi

# Docker is a soft-warn. dockmesh manages Docker — without it, 99% of
# the app is useless, but we let the install proceed so people can set
# up a fresh host in any order.
if command -v docker >/dev/null 2>&1; then
  DOCKER_VER="$(docker --version 2>/dev/null | awk '{print $3}' | tr -d ',' || true)"
  if docker info >/dev/null 2>&1; then
    ok "$(printf '%-16s %s  (running)' "Docker" "${DOCKER_VER:-installed}")"
  else
    warn "$(printf '%-16s installed but daemon is not responding' "Docker")"
  fi
  # macOS: `docker info` can succeed via the user's context socket while
  # the server (Docker Go SDK, unix:///var/run/docker.sock by default)
  # has no socket to connect to. Docker Desktop ships that symlink OFF
  # by default in recent releases — operator has to tick
  # "Allow the default Docker socket to be used" in Settings → Advanced.
  # Catch that here so the daemon-connect failure doesn't blindside them
  # post-`dockmesh init`.
  if [ "$OS" = "darwin" ] && [ ! -S /var/run/docker.sock ]; then
    warn "$(printf '%-16s %s' "docker.sock" "/var/run/docker.sock missing — dockmesh can't connect to the daemon")"
    say "                    Docker Desktop → Settings → Advanced →"
    say "                    enable \"Allow the default Docker socket to be used\""
  fi
else
  warn "$(printf '%-16s %s' "Docker" "not detected")"
  # Distro-specific install hint rather than the generic docs link —
  # operator shouldn't have to translate "install docker" into the
  # right command for their system.
  case "$DISTRO_ID" in
    ubuntu|debian|linuxmint|pop|raspbian)
      say "                    install:  sudo apt install -y docker.io" ;;
    fedora|rhel|centos|rocky|almalinux|ol)
      say "                    install:  sudo dnf install -y docker-ce && sudo systemctl enable --now docker" ;;
    alpine)
      say "                    install:  sudo apk add docker && sudo rc-update add docker default && sudo service docker start" ;;
    arch|manjaro|endeavouros)
      say "                    install:  sudo pacman -S --noconfirm docker && sudo systemctl enable --now docker" ;;
    opensuse*|sles|sled)
      say "                    install:  sudo zypper install -y docker && sudo systemctl enable --now docker" ;;
    macos)
      say "                    install:  https://www.docker.com/products/docker-desktop/  (or: brew install --cask docker)" ;;
    *)
      say "                    install:  https://docs.docker.com/engine/install/" ;;
  esac
fi

# Port availability for fresh installs. Upgrade case: the ports are
# presumably already ours, don't complain.
if [ "$IS_UPGRADE" = "0" ]; then
  port_free() {
    local p="$1"
    # BSD netstat's `-t` flag isn't "TCP-only" — that's GNU-specific —
    # so the Linux fallback never detected anything on macOS and every
    # port showed as "free". Use lsof on darwin instead; it's preinstalled.
    if [ "$OS" = "darwin" ]; then
      if command -v lsof >/dev/null 2>&1; then
        ! lsof -iTCP:"$p" -sTCP:LISTEN -Pn >/dev/null 2>&1
      else
        return 0
      fi
    elif command -v ss >/dev/null 2>&1; then
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
  elif [ "$HAS_LAUNCHD_UNIT" = "1" ]; then
    local_state="installed"
    [ "$LAUNCHD_ACTIVE" = "1" ] && local_state="$local_state, active"
    ok "launchd service     dev.dockmesh.service ($local_state)"
  else
    info "no service unit     manual restart required after upgrade"
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

TARBALL="dockmesh_${OS}_${ARCH}.tar.gz"
URL="https://github.com/$REPO/releases/download/$DM_VERSION/$TARBALL"
CHECKSUMS_URL="https://github.com/$REPO/releases/download/$DM_VERSION/checksums.txt"
info "artifact        $TARBALL"
step_done

# Already-on-latest early-exit. Skipped when DOCKMESH_FORCE=1 so we can
# still test the full download + backup + restart + health-probe flow
# against an already-current host without cutting a new release tag.
if [ "$IS_UPGRADE" = "1" ] && [ "$PREV_VERSION" = "$DM_VERSION" ] && [ "${DOCKMESH_FORCE:-0}" != "1" ]; then
  printf '\n' >&2
  box "Already on latest  $DM_VERSION" \
    "No upgrade needed — your binary is already at the newest" \
    "published release. Nothing was changed." \
    "" \
    "Reinstall anyway:" \
    "  curl -fsSL https://get.dockmesh.dev | sudo DOCKMESH_FORCE=1 bash"
  exit 0
fi

# ------------------------------------------------------------------
#  [3]  Download
# ------------------------------------------------------------------
step 3 $TOTAL_STEPS "Downloading"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

info "$TARBALL"

# Silent download + post-summary (Homebrew / apt / cargo pattern).
# A live progress bar isn't useful at sub-2-second timescales, and
# release tarballs land in well under that on typical connections
# (20 MiB at 30 MB/s = 0.7s). What the user actually wants is size +
# speed confirmation after the fact — that's what this prints.
DL_T0=$(get_time)
if ! curl -fL --retry 3 --retry-delay 2 --silent --show-error "$URL" -o "$TMP/$TARBALL"; then
  die "download failed — verify the release exists: $URL"
fi
DL_T1=$(get_time)
DL_BYTES=$(wc -c < "$TMP/$TARBALL" | tr -d ' ')
DL_DUR=$(awk "BEGIN { printf \"%.1f\", $DL_T1 - $DL_T0 }")
if command -v numfmt >/dev/null 2>&1; then
  DL_HUMAN=$(numfmt --to=iec-i --suffix=B --format='%.1f' "$DL_BYTES" 2>/dev/null || echo "${DL_BYTES}B")
else
  DL_HUMAN=$(awk "BEGIN { b=$DL_BYTES; if (b>1048576) printf \"%.1fMiB\", b/1048576; else if (b>1024) printf \"%.1fKiB\", b/1024; else printf \"%dB\", b }")
fi
DL_SPEED=$(awk "BEGIN { if ($DL_DUR>0) printf \"%.1f MB/s\", ($DL_BYTES / 1048576) / $DL_DUR; else print \"—\" }")

ok "$DL_HUMAN in ${DL_DUR}s · $DL_SPEED"
step_done

# ------------------------------------------------------------------
#  [4]  Verify checksum
# ------------------------------------------------------------------
step 4 $TOTAL_STEPS "Verifying"
if curl -fsSL --retry 2 -o "$TMP/checksums.txt" "$CHECKSUMS_URL" 2>/dev/null; then
  EXPECTED="$(grep " ${TARBALL}$" "$TMP/checksums.txt" | awk '{print $1}' || true)"
  if [ -n "$EXPECTED" ]; then
    ACTUAL="$(sha256_of "$TMP/$TARBALL")"
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

  # dmctl ships alongside the server binary since v0.2.0 (stack adopt,
  # scripted deploys, CI). Keep it in sync on upgrades too — older
  # installs predate dmctl entirely and an upgrade there is the first
  # time the CLI lands on the host.
  if [ -f "$TMP/dmctl" ]; then
    $USE_SUDO install -m 0755 "$TMP/dmctl" "$INSTALL_DIR/dmctl"
    ok "dmctl CLI       $INSTALL_DIR/dmctl"
  fi

  # Refresh agent assets on upgrade too — they carry the bundled
  # install-agent.sh + host-matched agent binary for enrollment.
  $USE_SUDO mkdir -p "$ASSET_DIR/bin"
  [ -f "$TMP/install-agent.sh" ] && $USE_SUDO install -m 0755 "$TMP/install-agent.sh" "$ASSET_DIR/install-agent.sh"
  if [ -f "$TMP/dockmesh-agent" ]; then
    AGENT_NAME="dockmesh-agent-${OS}-${ARCH}"
    $USE_SUDO install -m 0755 "$TMP/dockmesh-agent" "$ASSET_DIR/bin/$AGENT_NAME"
  fi
  ok "agent assets    $ASSET_DIR/"

  # ----------------------------------------------------------------
  # macOS upgrade path: restart the launchd service. No user-migration
  # story here — launchd daemons run as root by default and creating
  # a non-root dockmesh user on macOS requires dscl + is outside the
  # standard single-user-Mac homelab pattern.
  # ----------------------------------------------------------------
  if [ "$OS" = "darwin" ]; then
    if launchctl print system/dev.dockmesh.service >/dev/null 2>&1; then
      info "restarting dev.dockmesh.service (launchd)..."
      $USE_SUDO launchctl kickstart -k system/dev.dockmesh.service 2>/dev/null && \
        ok "service restarted" || \
        warn "restart failed — run manually: sudo launchctl kickstart -k system/dev.dockmesh.service"
    fi
    step_done
    box "Upgraded  ${PREV_VERSION:-prev} → $DM_VERSION" \
      "Data, stacks, and configuration are untouched." \
      "" \
      "Release notes:" \
      "  https://github.com/$REPO/releases/tag/$DM_VERSION" \
      "" \
      "Logs: /usr/local/var/log/dockmesh.{log,err}"
    exit 0
  fi

  # ----------------------------------------------------------------
  # Linux upgrade path with service-user migration.
  # Older installs (v0.1.3 and earlier) ran the service as root
  # because the systemd unit had no User= directive. v0.1.4+ ships
  # a dedicated `dockmesh` system user in the `docker` group —
  # smaller blast radius if the HTTP/agent handlers ever get
  # exploited. Idempotent: if already migrated, all no-ops.
  # ----------------------------------------------------------------
  if [ "$HAS_SYSTEMD_UNIT" = "1" ]; then
    CURRENT_USER="$(systemctl show -p User --value dockmesh 2>/dev/null)"
    if [ -z "$CURRENT_USER" ] || [ "$CURRENT_USER" = "root" ]; then
      info "migrating service to non-root 'dockmesh' user..."
      # Create user + docker-group membership (idempotent).
      $USE_SUDO useradd --system --no-create-home --shell /usr/sbin/nologin dockmesh 2>/dev/null || true
      $USE_SUDO usermod -aG docker dockmesh 2>/dev/null || true
      # Find the data dir from the unit's EnvironmentFile — every install
      # writes it as /…/dockmesh.env containing DOCKMESH_DB_PATH etc.
      ENV_FILE="$(systemctl show -p EnvironmentFiles --value dockmesh 2>/dev/null | awk '{print $1}' | sed 's/^-//' )"
      if [ -n "$ENV_FILE" ] && [ -f "$ENV_FILE" ]; then
        DATA_DIR="$(dirname "$ENV_FILE")"
        $USE_SUDO chown -R dockmesh:dockmesh "$DATA_DIR" 2>/dev/null || true
        $USE_SUDO chmod 700 "$DATA_DIR" 2>/dev/null || true
        # Rewrite the unit in-place by re-running `dockmesh init` with
        # --yes? Too invasive; instead, patch the existing unit so it
        # sets User=dockmesh Group=docker. A first-principles sed
        # would be fragile across variant unit contents, so we just
        # inject the two lines after [Service] if they're not present.
        UNIT_PATH="$(systemctl show -p FragmentPath --value dockmesh 2>/dev/null)"
        if [ -n "$UNIT_PATH" ] && [ -f "$UNIT_PATH" ]; then
          if ! grep -q '^User=' "$UNIT_PATH"; then
            $USE_SUDO sed -i '/^\[Service\]/a User=dockmesh\nGroup=docker' "$UNIT_PATH"
            $USE_SUDO systemctl daemon-reload
            ok "unit patched    $UNIT_PATH (User=dockmesh Group=docker)"
          fi
        fi
        ok "data dir owner  dockmesh:dockmesh"
      else
        warn "could not locate data dir — run 'dockmesh init' to finish migration manually"
      fi
    fi
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

  # Rollback commands always show `sudo` regardless of current USE_SUDO,
  # because the user who later runs them may not be root — "mv" without
  # sudo will silently fail to replace a 0755 root-owned binary.
  box "Upgraded  ${PREV_VERSION:-prev} → $DM_VERSION" \
    "Data, stacks, and configuration are untouched." \
    "" \
    "Release notes:" \
    "  https://github.com/$REPO/releases/tag/$DM_VERSION" \
    "" \
    "Rollback (keeps data, reverts the binary):" \
    "  sudo mv $INSTALL_DIR/dockmesh.bak $INSTALL_DIR/dockmesh" \
    "  sudo systemctl restart dockmesh"
  exit 0
fi

# Fresh install path.
step 5 $TOTAL_STEPS "Installing"
tar -xzf "$TMP/$TARBALL" -C "$TMP"
[ -x "$TMP/dockmesh" ] || die "tarball missing 'dockmesh' binary"
$USE_SUDO install -m 0755 "$TMP/dockmesh" "$INSTALL_DIR/dockmesh"
ok "binary          $INSTALL_DIR/dockmesh"

# dmctl — the operator CLI (stack adopt, scripted deploys, CI). Shipped
# inside the release tarball; drop it into the same PATH directory as
# the server binary so `dmctl` just works without extra setup.
if [ -f "$TMP/dmctl" ]; then
  $USE_SUDO install -m 0755 "$TMP/dmctl" "$INSTALL_DIR/dmctl"
  ok "dmctl CLI       $INSTALL_DIR/dmctl"
fi

# Agent assets: the server serves install-agent.sh + the agent binaries
# to hosts that want to enroll. Both lived at relative paths in early
# releases (./scripts/install-agent.sh, ./bin/dockmesh-agent-*), which
# 503'd under systemd (cwd=/). Install them to $ASSET_DIR (derived
# from $INSTALL_DIR up at the top) and point the server at them via
# env vars in dockmesh.env (written by `dockmesh init`).
$USE_SUDO mkdir -p "$ASSET_DIR/bin"
if [ -f "$TMP/install-agent.sh" ]; then
  $USE_SUDO install -m 0755 "$TMP/install-agent.sh" "$ASSET_DIR/install-agent.sh"
  ok "agent installer $ASSET_DIR/install-agent.sh"
fi
if [ -f "$TMP/dockmesh-agent" ]; then
  AGENT_NAME="dockmesh-agent-${OS}-${ARCH}"
  $USE_SUDO install -m 0755 "$TMP/dockmesh-agent" "$ASSET_DIR/bin/$AGENT_NAME"
  ok "agent binary    $ASSET_DIR/bin/$AGENT_NAME"
fi
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
