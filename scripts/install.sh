#!/usr/bin/env bash
# ============================================================================
#  Dockmesh one-line installer.
#
#  Usage:
#    curl -fsSL https://get.dockmesh.dev | sudo bash
#    curl -fsSL https://get.dockmesh.dev | DOCKMESH_VERSION=v1.2.3 bash
#    curl -fsSL <release>/install.sh | sudo bash -s -- --version v1.3.0-rc.1
#    curl -fsSL https://get.dockmesh.dev | DOCKMESH_INSTALL_DIR=/opt/bin bash
#
#  Env vars:
#    DOCKMESH_VERSION         tag to install, same as --version (default: latest
#                             stable release; pre-releases only when named)
#    DOCKMESH_CHANNEL         stable | testing      (default: stable; testing
#                             is deprecated and goes away in 0.7.0)
#    DOCKMESH_INSTALL_DIR     bin directory         (default: /usr/local/bin)
#    DOCKMESH_NO_SUDO         1 to skip sudo        (default: sudo if not root)
#    DOCKMESH_FORCE           1 to reinstall even if already on latest version
#    DOCKMESH_NO_SCANNER      1 to skip Grype CVE scanner install (default: install)
#    DOCKMESH_SKIP_CHECKSUM   1 to install without checksum verification (default: verify)
#    DOCKMESH_SKIP_SIGNATURE  1 to trust checksums.txt without its signature: releases
#                             before v0.6.0, or hosts without OpenSSL 3 (default: verify)
#    NO_COLOR                 1 to disable ANSI colors
#
#  What this script does:
#    - Detects OS, architecture, distribution
#    - Preflight checks every required tool (with distro-aware install hints
#      for anything missing)
#    - Resolves latest release via the GitHub Releases API
#    - Downloads the release tarball, checks the signature of checksums.txt
#      and the tarball's sha256
#    - Installs the binary to $DOCKMESH_INSTALL_DIR
#    - On upgrade: backs up the old binary, auto-restarts the systemd service
#    - On fresh install: starts the service so you can finish setup in the web wizard
#
#  The admin account and public URL are set in the web wizard at /setup;
#  `dockmesh init` remains available for scripted, non-interactive setups.
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
#  Env file helpers (exercised by scripts/install_test.go)
#
#  The data dir belongs to the service user, so anything in it may be a
#  link planted to make root write elsewhere. Files are only created with
#  O_EXCL (bash noclobber), which never follows a link, and moved into
#  place with rename.
# ------------------------------------------------------------------

# Env files of an earlier install at the script's default data dirs. Once
# `dockmesh system migrate-layout` has run, each is a symlink to ETC_ENV_FILE.
ENV_FILE_CANDIDATES=(/var/lib/dockmesh/dockmesh.env /data/dockmesh.env /usr/local/var/dockmesh/dockmesh.env)
ETC_ENV_FILE=/etc/dockmesh/dockmesh.env

# Owners an env file of an earlier install may have to be reused.
TRUSTED_ENV_OWNERS="root dockmesh"

# env_file_trusted FILE succeeds when FILE may become the config of a
# service with Docker access: named dockmesh.env, and the file and its
# directory not writable by group or others and owned by a trusted owner
# or by a UID without an account (uninstall.sh deletes the service user
# but keeps its data).
env_file_trusted() {
  local f="$1" p meta uid mode owner
  [ "$(basename "$f")" = "dockmesh.env" ] || return 1
  for p in "$f" "$(dirname "$f")"; do
    meta="$($USE_SUDO stat -L -c '%u %a' "$p" 2>/dev/null || $USE_SUDO stat -L -f '%u %Lp' "$p" 2>/dev/null)" || return 1
    uid="${meta%% *}"
    mode="${meta##* }"
    if owner="$(id -nu "$uid" 2>/dev/null)"; then
      case " $TRUSTED_ENV_OWNERS " in *" $owner "*) ;; *) return 1 ;; esac
    fi
    [ $(( 8#$mode & 8#022 )) -eq 0 ] || return 1
  done
}

# data_dir_allowed DIR fails for system directories the script must never
# take over as a data dir.
data_dir_allowed() {
  case "${1%/}" in
    ""|/|/bin|/boot|/dev|/etc|/home|/lib|/lib64|/media|/mnt|/opt|/proc|/root|/run|/sbin|/srv|/sys|/tmp|/usr|/usr/local|/var|/var/lib) return 1 ;;
  esac
}

# find_existing_env_file [PATH...] prints the first regular env file among
# the given paths and ENV_FILE_CANDIDATES. A reinstall with a different
# INSTALL_DIR, or over a data dir kept by uninstall.sh, picks up that file
# instead of writing a new one. One that fails env_file_trusted or sits in
# a system directory stops the install.
find_existing_env_file() {
  local p
  for p in "$@" "${ENV_FILE_CANDIDATES[@]}"; do
    [ -n "$p" ] || continue
    if $USE_SUDO test -f "$p" && { ! $USE_SUDO test -L "$p" || [ "$($USE_SUDO readlink "$p")" = "$ETC_ENV_FILE" ]; }; then
      if ! env_file_trusted "$p"; then
        die "refusing to reuse $p: it must be named dockmesh.env and, like its directory, be owned by root or dockmesh and not writable by group or others"
      fi
      if ! data_dir_allowed "$(dirname "$p")"; then
        die "refusing to reuse $p: $(dirname "$p") is a system directory, not a Dockmesh data dir"
      fi
      printf '%s' "$p"
      return 0
    fi
  done
  return 1
}

# warn_stale_asset_paths ENV_FILE ASSET_DIR warns when a kept env file
# points the agent installer or binaries at a path that no longer exists,
# and names the lines to change. The file itself is left alone.
warn_stale_asset_paths() {
  local env_file="$1" asset_dir="$2" key want have
  for key in DOCKMESH_INSTALL_SCRIPT DOCKMESH_BINARY_DIR; do
    case "$key" in
      DOCKMESH_INSTALL_SCRIPT) want="$asset_dir/install-agent.sh" ;;
      *) want="$asset_dir/bin" ;;
    esac
    have="$($USE_SUDO grep -E "^[[:space:]]*(export[[:space:]]+)?$key=" "$env_file" | tail -n 1 | sed -E 's/^[^=]*=//' | tr -d "\"'" || true)"
    if [ -n "$have" ] && [ "$have" != "$want" ] && ! $USE_SUDO test -e "$have"; then
      warn "$env_file sets $key=$have, which does not exist; change it to $key=$want"
    fi
  done
}

# Succeeds only on a host without an earlier install: no service unit, no
# env file at the default locations, and none of the given paths (target
# env file, databases) present. A directory we can't look into counts as
# "present". Without the flag the server opens the wizard by itself when
# no admin exists.
setup_flag_wanted() {
  local p dir
  if [ "${HAS_SYSTEMD_UNIT:-0}" = "1" ] || [ "${HAS_LAUNCHD_UNIT:-0}" = "1" ]; then
    return 1
  fi
  for p in "$ETC_ENV_FILE" "${ENV_FILE_CANDIDATES[@]}" "$@"; do
    [ -n "$p" ] || continue
    if $USE_SUDO test -e "$p" || $USE_SUDO test -L "$p"; then
      return 1
    fi
    dir="$(dirname "$p")"
    if $USE_SUDO test -d "$dir" && ! $USE_SUDO test -x "$dir"; then
      return 1
    fi
  done
  return 0
}

# write_env_file ENV_FILE DATA_DIR ASSET_DIR [OWNER] creates the bootstrap
# env file. An existing one is the live config of an earlier install and is
# never rewritten.
write_env_file() {
  local env_file="$1" data_dir="$2" asset_dir="$3" owner="${4:-}"
  local setup_line="" tmp
  if $USE_SUDO test -L "$env_file"; then
    die "refusing to write $env_file: it is a symlink — remove it and re-run"
  fi
  if $USE_SUDO test -d "$env_file"; then
    die "refusing to write $env_file: it is a directory"
  fi
  if $USE_SUDO test -e "$env_file"; then
    info "env file        $env_file exists — kept unchanged"
    return 0
  fi
  if setup_flag_wanted "$env_file" "$data_dir/data/dockmesh.db"; then
    setup_line="DOCKMESH_SETUP_FORCE=true"
  else
    info "earlier install found — setup wizard not forced open"
  fi
  # shellcheck disable=SC2016
  tmp="$($USE_SUDO bash -c 'set -C; umask 077
    for i in 1 2 3; do
      t="$1.new.$$.$RANDOM"
      if { cat >"$t"; } 2>/dev/null; then printf "%s" "$t"; exit 0; fi
    done
    exit 1' _ "$env_file" <<EOF
# Written by install.sh — minimal bootstrap config so the install
# wizard can take over. The wizard removes DOCKMESH_SETUP_FORCE on
# successful commit; this file stays as the live config afterwards.
DOCKMESH_HTTP_ADDR=:8080
DOCKMESH_AGENT_LISTEN=:8443
DOCKMESH_DATA_DIR=$data_dir
DOCKMESH_DB_PATH=$data_dir/data/dockmesh.db
DOCKMESH_STACKS_ROOT=$data_dir/stacks
DOCKMESH_SECRETS_PATH=$data_dir/data/secrets.env
DOCKMESH_SECRETS_KEY_PATH=$data_dir/data/secrets.age-key
DOCKMESH_AUDIT_GENESIS_PATH=$data_dir/data/audit-genesis.sha256
DOCKMESH_INSTALL_SCRIPT=$asset_dir/install-agent.sh
DOCKMESH_BINARY_DIR=$asset_dir/bin
$setup_line
EOF
  )" || die "could not create a temp file next to $env_file"
  if [ -n "$owner" ]; then
    $USE_SUDO chown -h "$owner" "$tmp"
  fi
  $USE_SUDO mv -f "$tmp" "$env_file"
}

# systemd_unit_loaded avoids `systemctl list-unit-files | grep -q`: under
# pipefail grep -q exits on the first match, systemctl gets SIGPIPE and the
# whole pipeline reports failure intermittently.
systemd_unit_loaded() {
  [ "$(systemctl show -p LoadState --value dockmesh.service 2>/dev/null)" = "loaded" ]
}

# wait_for_service waits up to 5 s for dockmesh.service to be active.
wait_for_service() {
  for _ in 1 2 3 4 5 6 7 8 9 10; do
    sleep 0.5
    systemctl is-active --quiet dockmesh && return 0
  done
  return 1
}

# write_unit writes dockmesh.service from the template in the new binary
# (extra arguments go to `dockmesh system write-unit`). UNIT_REWRITTEN=1
# when it replaced the unit, so a failed start can put the old one back.
UNIT_REWRITTEN=0
write_unit() {
  local out
  if ! out="$($USE_SUDO "$INSTALL_DIR/dockmesh" system write-unit "$@" 2>&1)"; then
    printf '%s\n' "$out" >&2
    return 1
  fi
  case "$out" in wrote*) UNIT_REWRITTEN=1 ;; esac
  ok "systemd unit    /etc/systemd/system/dockmesh.service"
}

# start_service runs `systemctl $1 dockmesh` and waits for it. If the
# service does not come up and write_unit replaced the unit in this run,
# the earlier unit is restored and the service started once more.
start_service() {
  $USE_SUDO systemctl $1 dockmesh || true
  wait_for_service && return 0
  [ "$UNIT_REWRITTEN" = "1" ] || return 1
  warn "dockmesh did not start with the new unit — restoring the earlier unit"
  $USE_SUDO "$INSTALL_DIR/dockmesh" system write-unit --restore >&2 || return 1
  UNIT_REWRITTEN=0
  $USE_SUDO systemctl restart dockmesh || true
  wait_for_service || return 1
  warn "running with the earlier unit — see: journalctl -u dockmesh --since '5 min ago'"
}

# service_runs_new_binary succeeds when dockmesh.service has a main process
# that is not $1 (the PID from before the restart) and whose executable was
# not replaced underneath it.
service_runs_new_binary() {
  local old_pid="$1" pid exe
  pid="$(systemctl show -p MainPID --value dockmesh 2>/dev/null || true)"
  { [ -n "$pid" ] && [ "$pid" != "0" ] && [ "$pid" != "$old_pid" ]; } || return 1
  exe="$(readlink "/proc/$pid/exe" 2>/dev/null || true)"
  case "$exe" in *" (deleted)") return 1 ;; esac
  return 0
}

# install_agent_binaries copies the agent binaries from an extracted release
# ($1) into $2/bin. Linux tarballs carry one per architecture; older ones and
# the macOS tarballs carry a single dockmesh-agent for the host's own $3-$4.
install_agent_binaries() {
  local src="$1" asset_dir="$2" os="$3" arch="$4" f name found=0
  for f in "$src"/dockmesh-agent-linux-*; do
    [ -f "$f" ] || continue
    name="$(basename "$f")"
    $USE_SUDO install -m 0755 "$f" "$asset_dir/bin/$name"
    ok "agent binary    $asset_dir/bin/$name"
    found=1
  done
  if [ "$found" = "0" ] && [ -f "$src/dockmesh-agent" ]; then
    name="dockmesh-agent-${os}-${arch}"
    $USE_SUDO install -m 0755 "$src/dockmesh-agent" "$asset_dir/bin/$name"
    ok "agent binary    $asset_dir/bin/$name"
  fi
}

# ------------------------------------------------------------------
#  Version helpers
# ------------------------------------------------------------------

# Arguments come after `bash -s --` in the one-liner.
parse_args() {
  while [ $# -gt 0 ]; do
    case "$1" in
      --version)
        [ $# -ge 2 ] || die "--version needs a release tag, e.g. --version v1.3.0-rc.1"
        DOCKMESH_VERSION="$2"
        shift 2
        ;;
      --version=*)
        DOCKMESH_VERSION="${1#--version=}"
        shift
        ;;
      *) die "unknown argument: $1 (supported: --version <tag>)" ;;
    esac
  done
}

valid_tag() {
  [[ $1 =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]
}

# channel_warning prints the deprecation notice for DOCKMESH_CHANNEL=testing,
# which --version replaces.
channel_warning() {
  if [ "$1" = "testing" ]; then
    warn "DOCKMESH_CHANNEL=testing is deprecated and will be removed in 0.7.0; install a pre-release with --version <tag>"
  fi
}

# version_older A B: A's major.minor.patch is lower than B's. A release
# candidate shares its release's numbers, so v0.6.0 is not older than
# v0.6.0-rc.2.
version_older() {
  local a b
  a="$(printf '%s' "${1#v}" | sed -E 's/[-+].*//')"
  b="$(printf '%s' "${2#v}" | sed -E 's/[-+].*//')"
  [ "$a" != "$b" ] && [ "$(printf '%s\n%s\n' "$a" "$b" | sort -t. -k1,1n -k2,2n -k3,3n | head -1)" = "$a" ]
}

# download_speed <bytes> <seconds>: awk gets the numbers as variables, so a
# download that took 0.0 s doesn't make it divide by a constant zero.
download_speed() {
  awk -v b="$1" -v d="$2" 'BEGIN { if (d > 0) printf "%.1f MB/s", (b / 1048576) / d; else print "—" }'
}

# Minisign public keys of Dockmesh releases. The first signs every release;
# the second stays offline and only signs the release that rotates the first.
RELEASE_PUBKEYS="RWRaHI41HpSgAcJDtFP8X2HnLIjHDLUdnj0GyCoGiGCdDW1rlLDWXzK1 RWQDX3H4Uce7c0kKscCE0WRymm/CkSETtzGn4IKKN3tdJyfO9y6ZyuLA"

# signature_tool_ok: OpenSSL 3 verifies Ed25519 over raw input (pkeyutl
# -rawin), which is what a legacy-mode minisign signature is. LibreSSL and
# OpenSSL 1.1 can't, so they count as missing.
signature_tool_ok() {
  command -v openssl >/dev/null 2>&1 && command -v base64 >/dev/null 2>&1 && command -v od >/dev/null 2>&1 || return 1
  case "$(openssl version 2>/dev/null)" in
    "OpenSSL "[3-9].* | "OpenSSL "[1-9][0-9].*) return 0 ;;
  esac
  return 1
}

hex_of() { od -An -tx1 -v | tr -d ' \n'; }
byte_count() { wc -c < "$1" | tr -d ' '; }

# ed25519_verify <public key DER> <message> <raw signature>
ed25519_verify() {
  openssl pkeyutl -verify -pubin -keyform DER -inkey "$1" -rawin -in "$2" -sigfile "$3" >/dev/null 2>&1
}

# verify_release_signature <checksums.txt> <checksums.txt.minisig> <tag> <keys> <workdir>
# Succeeds only if one of <keys> made the signature over the file and over the
# trusted comment, and the trusted comment names <tag>. Prints the key id.
verify_release_signature() {
  local file="$1" minisig="$2" tag="$3" keys="$4" dir="$5" keyid trusted k
  [ "$(wc -l < "$minisig" | tr -d ' ')" -le 4 ] || return 1
  sed -n 2p "$minisig" | base64 -d > "$dir/sig.bin" 2>/dev/null || return 1
  sed -n 4p "$minisig" | base64 -d > "$dir/global.sig" 2>/dev/null || return 1
  trusted="$(sed -n 3p "$minisig")"
  [ "$(byte_count "$dir/sig.bin")" = 74 ] && [ "$(byte_count "$dir/global.sig")" = 64 ] || return 1
  [ "$(head -c 2 "$dir/sig.bin")" = "Ed" ] || return 1
  [ "$trusted" = "trusted comment: dockmesh $tag checksums.txt" ] || return 1
  keyid="$(head -c 10 "$dir/sig.bin" | tail -c 8 | hex_of)"
  tail -c 64 "$dir/sig.bin" > "$dir/file.sig"
  { cat "$dir/file.sig"; printf '%s' "${trusted#trusted comment: }"; } > "$dir/global.msg"
  for k in $keys; do
    printf '%s' "$k" | base64 -d > "$dir/key.bin" 2>/dev/null || continue
    [ "$(byte_count "$dir/key.bin")" = 42 ] || continue
    [ "$(head -c 10 "$dir/key.bin" | tail -c 8 | hex_of)" = "$keyid" ] || continue
    { printf '\x30\x2a\x30\x05\x06\x03\x2b\x65\x70\x03\x21\x00'; tail -c 32 "$dir/key.bin"; } > "$dir/key.der"
    ed25519_verify "$dir/key.der" "$file" "$dir/file.sig" || return 1
    ed25519_verify "$dir/key.der" "$dir/global.msg" "$dir/global.sig" || return 1
    printf '%s\n' "$keyid" | sed -E 's/(..)(..)(..)(..)(..)(..)(..)(..)/\8\7\6\5\4\3\2\1/' | tr 'a-f' 'A-F'
    return 0
  done
  return 1
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
parse_args "$@"
DM_VERSION="${DOCKMESH_VERSION:-latest}"
if [ "$DM_VERSION" != "latest" ] && ! valid_tag "$DM_VERSION"; then
  die "release tag '$DM_VERSION' is not of the form vX.Y.Z or vX.Y.Z-rc.N"
fi
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
    # Extract the version tag (vX.Y.Z or vX.Y.Z-rc.N) for the summary box.
    # `|| true` keeps `set -o pipefail` happy for dev and nightly builds,
    # whose version line has no tag.
    PREV_VERSION="$(printf '%s' "$PREV_VERSION_LINE" | grep -oE 'v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?' | head -1 || true)"
  fi
fi

HAS_SYSTEMD_UNIT=0
SYSTEMD_ACTIVE=0
SYSTEMD_ENABLED=0
SYSTEMD_PID=""
HAS_LAUNCHD_UNIT=0
LAUNCHD_ACTIVE=0
if command -v systemctl >/dev/null 2>&1; then
  if systemd_unit_loaded; then
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

# Release signatures are checked with OpenSSL 3. Without it the installer
# stops here rather than downloading something it can't authenticate.
if [ "${DOCKMESH_SKIP_DOWNLOAD:-0}" != "1" ] && [ "${DOCKMESH_SKIP_CHECKSUM:-0}" != "1" ] &&
  [ "${DOCKMESH_SKIP_SIGNATURE:-0}" != "1" ]; then
  if signature_tool_ok; then
    ok "$(printf '%-16s %s' "openssl" "$(openssl version | awk '{print $2}')")"
  else
    fail "$(printf '%-16s %s' "openssl" "OpenSSL 3 not found")"
    printf '\n' >&2
    box "Cannot verify the release signature" \
      "Dockmesh releases are signed; checking the signature needs" \
      "OpenSSL 3 or newer ('openssl', plus 'base64' and 'od')." \
      "" \
      "Install on $DISTRO_NAME:" \
      "  $(install_hint openssl)" \
      "" \
      "Or check the files with minisign and install them by hand:" \
      "  https://dockmesh.dev/docs/operations/verify-release/" \
      "" \
      "DOCKMESH_SKIP_SIGNATURE=1 installs without the check (not" \
      "recommended: the files are then trusted unchecked)."
    exit 1
  fi
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
#  Local-binary fast-path (P.14.6 testing).
#  When DOCKMESH_SKIP_DOWNLOAD=1 + the binary is already at
#  $INSTALL_DIR/dockmesh, skip GitHub download / verify / install
#  steps and jump straight to the bootstrap section. Used by CI and
#  by the local-test wrapper that scp's a freshly-built binary
#  before invoking the script.
# ------------------------------------------------------------------
if [ "${DOCKMESH_SKIP_DOWNLOAD:-0}" = "1" ]; then
  if [ ! -x "$INSTALL_DIR/dockmesh" ]; then
    die "DOCKMESH_SKIP_DOWNLOAD=1 but $INSTALL_DIR/dockmesh is missing or not executable"
  fi
  info "skipping download — using pre-staged $INSTALL_DIR/dockmesh"
  DM_VERSION="$("$INSTALL_DIR/dockmesh" --version 2>/dev/null | head -1 | grep -oE 'v?[0-9A-Za-z.-]+' | head -1 || echo local)"
  # Force fresh-install behaviour even when an existing binary is on
  # disk — we WANT to run the bootstrap section against this host,
  # not the upgrade-restart-existing-systemd path that would normally
  # fire for an upgrade.
  IS_UPGRADE=0
  TOTAL_STEPS=2  # rebrand: preflight + bootstrap from here on
  goto_bootstrap=1
  step_done
fi

if [ "${goto_bootstrap:-0}" != "1" ]; then

# ------------------------------------------------------------------
#  [2]  Resolve release
# ------------------------------------------------------------------
step 2 $TOTAL_STEPS "Resolving release"
info "channel         $CHANNEL"
channel_warning "$CHANNEL"

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
  valid_tag "$DM_VERSION" || die "GitHub returned an unexpected release tag '$DM_VERSION'"
fi
ok "latest          $DM_VERSION"

# An install running a release candidate is ahead of the latest stable
# release until the final version is out.
if [ "$IS_UPGRADE" = "1" ] && [ -z "${DOCKMESH_VERSION:-}" ] && [ -n "$PREV_VERSION" ] &&
  version_older "$DM_VERSION" "$PREV_VERSION" && [ "${DOCKMESH_FORCE:-0}" != "1" ]; then
  die "installed $PREV_VERSION is newer than $DM_VERSION; pass --version <tag> to pick a release (DOCKMESH_FORCE=1 installs $DM_VERSION anyway)"
fi

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

# Grype is pinned to one release and checked against the SHA-256 below
# before anything runs as root. Bump the version and all four sums
# together from the release's grype_<version>_checksums.txt.
GRYPE_VERSION="0.121.1"
grype_sha256() {
  case "$1" in
    linux_amd64)  echo "bc3ea3f9e6fd56ac781c08c6c5e614271b79f438200757f6932085c19b0a4657" ;;
    linux_arm64)  echo "33670612aad351c48cf3c37f4a85d8b043d1b64b21e6034e675bb00b956d78c1" ;;
    darwin_amd64) echo "20fefd09105fb7bf2bbfe29c6bb14edbe4c17214436753735c172465fffe69f6" ;;
    darwin_arm64) echo "daa9ffdc0022b34fb476f94cbcabac9d850882c895d83e4a5ecf5e0ab13bce77" ;;
  esac
}

install_grype() {
  local platform="${OS}_${ARCH}" want got tarball dir
  want="$(grype_sha256 "$platform")"
  [ -n "$want" ] || return 1
  tarball="grype_${GRYPE_VERSION}_${platform}.tar.gz"
  dir="$TMP/grype"
  mkdir -p "$dir"
  curl -fsSL --proto '=https' -o "$dir/$tarball" \
    "https://github.com/anchore/grype/releases/download/v${GRYPE_VERSION}/${tarball}" || return 1
  got="$(sha256_of "$dir/$tarball")"
  if [ "$got" != "$want" ]; then
    warn "grype download checksum mismatch (got $got, want $want)"
    return 1
  fi
  tar -xzf "$dir/$tarball" -C "$dir" grype || return 1
  $USE_SUDO install -m 0755 "$dir/grype" "$INSTALL_DIR/grype"
}

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
DL_SPEED="$(download_speed "$DL_BYTES" "$DL_DUR")"

ok "$DL_HUMAN in ${DL_DUR}s · $DL_SPEED"
step_done

# ------------------------------------------------------------------
#  [4]  Verify checksum
# ------------------------------------------------------------------
step 4 $TOTAL_STEPS "Verifying"
if [ "${DOCKMESH_SKIP_CHECKSUM:-0}" = "1" ]; then
  warn "checksum and signature verification skipped — DOCKMESH_SKIP_CHECKSUM=1"
else
  if ! curl -fsSL --retry 2 -o "$TMP/checksums.txt" "$CHECKSUMS_URL" 2>/dev/null; then
    die "no checksums.txt published for $DM_VERSION — refusing to install an unverified binary (DOCKMESH_SKIP_CHECKSUM=1 overrides)"
  fi
  if [ "${DOCKMESH_SKIP_SIGNATURE:-0}" = "1" ]; then
    warn "signature check skipped — DOCKMESH_SKIP_SIGNATURE=1; checksums.txt is not authenticated"
  else
    if ! curl -fsSL --retry 2 -o "$TMP/checksums.txt.minisig" "$CHECKSUMS_URL.minisig" 2>/dev/null; then
      if version_older "$DM_VERSION" v0.6.0; then
        die "$DM_VERSION was released before signing existed and has no checksums.txt.minisig; install it with DOCKMESH_SKIP_SIGNATURE=1"
      fi
      die "$DM_VERSION has no checksums.txt.minisig, but every release from v0.6.0 on is signed: the release files may have been tampered with. Do not install it; report it to security@dockmesh.dev"
    fi
    mkdir -p "$TMP/sig"
    if ! SIGNED_BY="$(verify_release_signature "$TMP/checksums.txt" "$TMP/checksums.txt.minisig" "$DM_VERSION" "$RELEASE_PUBKEYS" "$TMP/sig")"; then
      die "the signature of checksums.txt does not verify for $DM_VERSION — refusing to install"
    fi
    ok "signature       key $SIGNED_BY"
  fi
  EXPECTED="$(grep " ${TARBALL}$" "$TMP/checksums.txt" | awk '{print $1}' || true)"
  if [ -z "$EXPECTED" ]; then
    die "no checksum entry for $TARBALL — refusing to install an unverified binary (DOCKMESH_SKIP_CHECKSUM=1 overrides)"
  fi
  ACTUAL="$(sha256_of "$TMP/$TARBALL")"
  if [ "$EXPECTED" != "$ACTUAL" ]; then
    die "checksum mismatch — expected $EXPECTED got $ACTUAL"
  fi
  ok "sha256          ${EXPECTED:0:16}…${EXPECTED: -16}"
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

  # Backfill grype on upgrade for installs predating the scanner-bundle.
  # Same skip flag as fresh install. We keep the old binary if grype is
  # already on the host — `grype` self-updates its DB, not its binary.
  if [ "${DOCKMESH_NO_SCANNER:-0}" != "1" ] && ! command -v grype >/dev/null 2>&1; then
    info "installing grype (CVE scanner)..."
    if install_grype; then
      GRYPE_VER="$(grype version 2>/dev/null | awk -F: '/^[Vv]ersion:/{gsub(/ /, "", $2); print $2; exit}')"
      ok "grype scanner   installed (v${GRYPE_VER:-unknown})"
    else
      warn "grype install failed — CVE scanning disabled"
    fi
  fi

  # dmctl ships alongside the server binary since v0.2.0 (stack adopt,
  # scripted deploys, CI). Keep it in sync on upgrades too — older
  # installs predate dmctl entirely and an upgrade there is the first
  # time the CLI lands on the host.
  if [ -f "$TMP/dmctl" ]; then
    $USE_SUDO install -m 0755 "$TMP/dmctl" "$INSTALL_DIR/dmctl"
    ok "dmctl CLI       $INSTALL_DIR/dmctl"
  fi

  # Refresh agent assets on upgrade too — they carry the bundled
  # install-agent.sh + the agent binaries for enrollment.
  $USE_SUDO mkdir -p "$ASSET_DIR/bin"
  [ -f "$TMP/install-agent.sh" ] && $USE_SUDO install -m 0755 "$TMP/install-agent.sh" "$ASSET_DIR/install-agent.sh"
  install_agent_binaries "$TMP" "$ASSET_DIR" "$OS" "$ARCH"
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
    if ! $USE_SUDO "$INSTALL_DIR/dockmesh" system migrate-layout; then
      warn "config left at its old place; Dockmesh keeps using it"
    fi
    if ! write_unit; then
      warn "unit kept as it was; Dockmesh keeps running with it"
    fi
    info "restarting dockmesh.service..."
    RESTART_T0=$(get_time)
    # Probe the systemd state rather than HTTP because the listen port is
    # whatever the user configured.
    if ! start_service restart; then
      die "service failed to start — see: journalctl -u dockmesh --since '1 min ago'"
    fi
    RESTART_T1=$(get_time)
    DOWNTIME=$(awk "BEGIN { printf \"%.1f\", $RESTART_T1 - $RESTART_T0 }")
    if ! service_runs_new_binary "$SYSTEMD_PID"; then
      die "dockmesh.service is still running the old binary — run: $USE_SUDO systemctl restart dockmesh"
    fi
    ok "service restarted"
    ok "health OK       downtime ${DOWNTIME}s"
    RUNNING_VERSION="$("$INSTALL_DIR/dockmesh" --version 2>/dev/null | head -1 || true)"
    case "$RUNNING_VERSION" in
      *"$DM_VERSION"*) ok "running         $RUNNING_VERSION" ;;
      *) die "installed binary reports '${RUNNING_VERSION:-unknown}', expected $DM_VERSION" ;;
    esac
  else
    warn "no systemd unit found — restart dockmesh manually to run the new version"
  fi
  step_done

  # Rollback commands always show `sudo` regardless of current USE_SUDO,
  # because the user who later runs them may not be root — "mv" without
  # sudo will silently fail to replace a 0755 root-owned binary.
  box "Upgraded  ${PREV_VERSION:-prev} → $DM_VERSION" \
    "Data and stacks are untouched." \
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

# Grype CVE scanner — drives the "Scan" action in the Resources view.
# Without it the backend logs "scanner disabled" and the UI returns
# 503 "scanner not configured" on every scan attempt. A failed install
# only warns: scanning is not required to run Dockmesh. Skippable via
# env var for air-gapped / minimal installs.
if [ "${DOCKMESH_NO_SCANNER:-0}" != "1" ]; then
  if command -v grype >/dev/null 2>&1; then
    GRYPE_VER="$(grype version 2>/dev/null | awk -F: '/^[Vv]ersion:/{gsub(/ /, "", $2); print $2; exit}')"
    ok "grype scanner   already installed (v${GRYPE_VER:-unknown})"
  else
    info "installing grype (CVE scanner)..."
    if install_grype; then
      GRYPE_VER="$(grype version 2>/dev/null | awk -F: '/^[Vv]ersion:/{gsub(/ /, "", $2); print $2; exit}')"
      ok "grype scanner   installed (v${GRYPE_VER:-unknown})"
    else
      warn "grype install failed — CVE scanning disabled"
      say "                    install manually: https://github.com/anchore/grype#installation"
    fi
  fi
else
  info "grype scanner   skipped (DOCKMESH_NO_SCANNER=1)"
fi

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
install_agent_binaries "$TMP" "$ASSET_DIR" "$OS" "$ARCH"
ok "mode            0755 (root:root)"
step_done

fi  # end of "if not skipping download / upgrade-or-fresh install block"

# ------------------------------------------------------------------
#  [6]  Bootstrap for the install wizard (P.14.5)
#
#  Sets up the bare minimum so the dockmesh service starts and the
#  operator can finish the install via the web wizard at /setup.
#  Anything that needs operator input (admin user, public URL, data
#  dir confirmation) is the wizard's job — not this script's.
#
#  Skipped when DOCKMESH_NO_BOOTSTRAP=1 — useful for CI / advanced
#  operators who want to script `dockmesh init` themselves.
# ------------------------------------------------------------------
step 6 $TOTAL_STEPS "Bootstrapping for web setup"

if [ "${DOCKMESH_NO_BOOTSTRAP:-0}" = "1" ]; then
  info "skipped — DOCKMESH_NO_BOOTSTRAP=1"
  info "run 'sudo dockmesh init' interactively when ready"
  step_done
else
  # An env file of an earlier install (kept data, other INSTALL_DIR) wins
  # over the defaults, so its data dir and settings are picked back up.
  UNIT_ENV_FILE=""
  if [ "${HAS_SYSTEMD_UNIT:-0}" = "1" ]; then
    UNIT_ENV_FILE="$(systemctl show -p EnvironmentFiles --value dockmesh 2>/dev/null | awk '{print $1}' | sed 's/^-//' || true)"
  fi
  KEPT_ENV_FILE="$(find_existing_env_file "${DOCKMESH_DATA_DIR:+$DOCKMESH_DATA_DIR/dockmesh.env}" "$UNIT_ENV_FILE" || true)"
  # Default data root: /var/lib/dockmesh on Linux, /usr/local/var/dockmesh
  # on macOS. An existing /data install is found through its env file.
  if [ -n "$KEPT_ENV_FILE" ]; then
    DATA_DIR="$(dirname "$KEPT_ENV_FILE")"
    if [ -n "${DOCKMESH_DATA_DIR:-}" ] && [ "$DATA_DIR" != "$DOCKMESH_DATA_DIR" ]; then
      die "DOCKMESH_DATA_DIR=$DOCKMESH_DATA_DIR, but the earlier install's config is $KEPT_ENV_FILE — set DOCKMESH_DATA_DIR=$DATA_DIR or move that file away"
    fi
    info "earlier install $KEPT_ENV_FILE"
  elif [ "$OS" = "darwin" ]; then
    DATA_DIR="${DOCKMESH_DATA_DIR:-/usr/local/var/dockmesh}"
  else
    DATA_DIR="${DOCKMESH_DATA_DIR:-/var/lib/dockmesh}"
  fi
  info "data dir        $DATA_DIR"

  # Service user. macOS skips — launchd daemons typically run as root
  # and dscl-based user creation is outside the homelab pattern. On
  # Linux: useradd is idempotent because we check first.
  if [ "$OS" = "linux" ]; then
    if id dockmesh >/dev/null 2>&1; then
      ok "service user    dockmesh (already exists)"
    else
      $USE_SUDO useradd --system --no-create-home --shell /usr/sbin/nologin dockmesh
      ok "service user    dockmesh (created)"
    fi
    # docker group membership — silent if docker isn't installed yet.
    if getent group docker >/dev/null 2>&1; then
      $USE_SUDO usermod -aG docker dockmesh
      ok "added to        docker group"
    fi
  fi

  # Data root + sub-dirs. Owned by dockmesh:docker on Linux so the
  # service can write everywhere; on macOS root owns it.
  $USE_SUDO mkdir -p "$DATA_DIR/data" "$DATA_DIR/stacks"
  if [ "$OS" = "linux" ]; then
    if [ -n "$KEPT_ENV_FILE" ]; then
      # A recreated service user may have a new UID; only the dirs the
      # service writes are handed back to it.
      $USE_SUDO chown dockmesh:docker "$DATA_DIR"
      $USE_SUDO chown -R dockmesh:docker "$DATA_DIR/data" "$DATA_DIR/stacks"
    else
      $USE_SUDO chown -R dockmesh:docker "$DATA_DIR"
    fi
    $USE_SUDO chmod 750 "$DATA_DIR"
  fi
  ok "data layout     $DATA_DIR/{data,stacks}"

  # Bootstrap env file. DOCKMESH_SETUP_FORCE=true marks the install as
  # not finished, so the server opens the setup wizard while it has no
  # accounts; the wizard's commit handler scrubs the flag.
  ENV_FILE="$DATA_DIR/dockmesh.env"
  if [ -n "$KEPT_ENV_FILE" ]; then
    ENV_FILE="$KEPT_ENV_FILE"
    if [ "$OS" = "linux" ] && ! $USE_SUDO "$INSTALL_DIR/dockmesh" system migrate-layout --from "$ENV_FILE"; then
      warn "config left at its old place; Dockmesh keeps using it"
    fi
    # The old place is a symlink the service user could swap; the unit
    # loads the root-owned file itself.
    if $USE_SUDO test -L "$ENV_FILE" && [ "$($USE_SUDO readlink "$ENV_FILE")" = "$ETC_ENV_FILE" ]; then
      ENV_FILE="$ETC_ENV_FILE"
    fi
    warn_stale_asset_paths "$ENV_FILE" "$ASSET_DIR"
    ok "env file        $ENV_FILE (kept)"
  else
    if [ "$OS" = "linux" ]; then
      write_env_file "$ENV_FILE" "$DATA_DIR" "$ASSET_DIR" dockmesh:docker
    else
      write_env_file "$ENV_FILE" "$DATA_DIR" "$ASSET_DIR"
    fi
    ok "env file        $ENV_FILE"
  fi

  # Service unit. Linux = systemd; macOS = launchd.
  if [ "$OS" = "linux" ] && command -v systemctl >/dev/null 2>&1; then
    if ! write_unit --env-file "$ENV_FILE"; then
      if [ "${HAS_SYSTEMD_UNIT:-0}" = "1" ]; then
        warn "unit kept as it was"
      else
        die "could not write /etc/systemd/system/dockmesh.service"
      fi
    fi
    # Wait for it to come up before printing the wizard URL.
    if start_service "enable --now"; then
      ok "service         active"
    else
      fail "service failed to start — see: journalctl -u dockmesh --since '1 min ago'"
    fi
  elif [ "$OS" = "darwin" ] && command -v launchctl >/dev/null 2>&1; then
    PLIST="/Library/LaunchDaemons/dev.dockmesh.service.plist"
    $USE_SUDO tee "$PLIST" >/dev/null <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>dev.dockmesh.service</string>
  <key>ProgramArguments</key>
  <array>
    <string>$INSTALL_DIR/dockmesh</string>
    <string>serve</string>
    <string>--env-file</string>
    <string>$ENV_FILE</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>/usr/local/var/log/dockmesh.log</string>
  <key>StandardErrorPath</key><string>/usr/local/var/log/dockmesh.err</string>
</dict>
</plist>
EOF
    $USE_SUDO launchctl bootstrap system "$PLIST" 2>/dev/null || \
      $USE_SUDO launchctl load -w "$PLIST"
    ok "launchd unit    $PLIST"
  else
    warn "no service manager — start manually: $USE_SUDO $INSTALL_DIR/dockmesh serve --env-file $ENV_FILE"
  fi
  step_done
fi

# ------------------------------------------------------------------
#  [done]  Wizard URL
# ------------------------------------------------------------------
INSTALLED="$("$INSTALL_DIR/dockmesh" --version 2>/dev/null | head -1 || echo "$DM_VERSION")"
ok "installed       $INSTALLED"
TOTAL_ELAPSED=$(awk "BEGIN { printf \"%.1f\", $(get_time) - $START_TS }")
printf '   %s%ss total%s\n' "$FG_MUTED" "$TOTAL_ELAPSED" "$RST" >&2

# Pick a sensible LAN IP for the printed URL — first non-loopback IPv4
# address. Falls back to <hostname> if no IP could be derived.
WIZARD_HOST="$(hostname -I 2>/dev/null | awk '{print $1}')"
[ -z "$WIZARD_HOST" ] && WIZARD_HOST="$(hostname 2>/dev/null || echo localhost)"

if [ "${DOCKMESH_NO_BOOTSTRAP:-0}" = "1" ]; then
  box "Next step" \
    "" \
    "  $USE_SUDO dockmesh init" \
    "" \
    "Guided CLI wizard — data dir, admin user, listen port, systemd unit." \
    "Everything defaults to sane values. ~2 minutes."
elif [ -n "${KEPT_ENV_FILE:-}" ]; then
  box "Dockmesh is back" \
    "" \
    "  http://$WIZARD_HOST:8080" \
    "" \
    "Reused $KEPT_ENV_FILE and the data next to it." \
    "Sign in with your existing account."
else
  # The setup code goes to this terminal only, never to a log. Wait for
  # the service first: new-code asks it whether it is in setup mode.
  for _ in $(seq 1 30); do
    curl -fs "http://127.0.0.1:8080/api/v1/health" >/dev/null 2>&1 && break
    sleep 1
  done
  CODE_LINES=()
  if CODE_OUT="$($USE_SUDO "$INSTALL_DIR/dockmesh" setup new-code 2>&1)"; then
    while IFS= read -r line; do CODE_LINES+=("  $line"); done <<<"$CODE_OUT"
  else
    CODE_LINES=("  Get a setup code with:" "  sudo dockmesh setup new-code")
  fi
  box "Open the install wizard" \
    "" \
    "${CODE_LINES[@]}" \
    "" \
    "The code is valid for 2 hours. A new one, which replaces it:" \
    "  sudo dockmesh setup new-code"
fi

printf '   %sDocs%s     https://dockmesh.dev/docs\n'   "$DIM" "$RST" >&2
printf '   %sIssues%s   https://github.com/%s/issues\n\n' "$DIM" "$RST" "$REPO" >&2
