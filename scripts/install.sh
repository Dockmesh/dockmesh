#!/usr/bin/env bash
# ============================================================================
#  Dockmesh installer.
#
#  Usage:
#    curl -fsSL https://get.dockmesh.dev | sudo bash
#    curl -fsSL https://get.dockmesh.dev | sudo bash -s -- --version v1.3.0-rc.1
#    curl -fsSL https://get.dockmesh.dev | sudo DOCKMESH_INSTALL_DIR=/opt/bin bash
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
#    DOCKMESH_NO_BOOTSTRAP    1 to install binaries, user and units without
#                             starting setup mode
#    DOCKMESH_SKIP_DOWNLOAD   1 to use a binary already placed in the bin directory
#    DOCKMESH_SKIP_CHECKSUM   1 to install without checksum verification (default: verify)
#    DOCKMESH_SKIP_SIGNATURE  1 to trust checksums.txt without its signature: releases
#                             before v0.6.0, or hosts without OpenSSL 3 (default: verify)
#    NO_COLOR                 set to disable colours
#
#  A new install gets the binaries, the dockmesh user, an empty data dir and
#  the systemd units, and starts setup mode: the admin account, data dir and
#  address are chosen in the browser, with the setup code printed at the end.
#  A host with any trace of an earlier install (binary, unit, config or
#  database) is upgraded instead and never gets a setup code.
# ============================================================================
set -euo pipefail

# Box drawing follows the caller's locale; everything else runs in "C" so
# awk prints "0.6", not "0,6", under a German or French LC_NUMERIC.
CALLER_LOCALE="${LC_ALL:-${LC_CTYPE:-${LANG:-}}}"
export LC_ALL=C

START_TS=${EPOCHREALTIME:-$SECONDS}

# The installer writes to stderr: under `curl | bash` that stays a TTY.
if [ -t 2 ] && [ -z "${NO_COLOR:-}" ] && [ "${TERM:-}" != "dumb" ]; then
  BOLD=$'\033[1m'; DIM=$'\033[2m'; RST=$'\033[0m'
  FG_STEP=$'\033[38;5;44m'
  FG_OK=$'\033[38;5;42m'
  FG_WARN=$'\033[38;5;214m'
  FG_FAIL=$'\033[38;5;196m'
else
  BOLD=; DIM=; RST=
  FG_STEP=; FG_OK=; FG_WARN=; FG_FAIL=
fi

case "$CALLER_LOCALE" in
  *[Uu][Tt][Ff]-8*|*[Uu][Tt][Ff]8*)
    BOX_TL='┌'; BOX_TR='┐'; BOX_BL='└'; BOX_BR='┘'; BOX_H='─'; BOX_V='│' ;;
  *)
    BOX_TL='+'; BOX_TR='+'; BOX_BL='+'; BOX_BR='+'; BOX_H='-'; BOX_V='|' ;;
esac

BOX_WIDTH=65

say()    { printf '%s\n' "$*" >&2; }
# A status word in front of a line: warning, notice, moved, ok.
status() { local word="$1" colour="$2"; shift 2; printf '%s%s%s  %s\n' "$colour" "$word" "$RST" "$*" >&2; }
warn()   { status warning "$FG_WARN" "$*"; }
notice() { status notice "$FG_STEP" "$*"; }
# Details of a step are not printed; the step line sums them up.
ok()     { :; }
info()   { :; }

die() {
  printf '\n%serror%s  %s\n\n' "$FG_FAIL" "$RST" "$*" >&2
  exit 1
}

# box TITLE LINE... draws a box with the title in its top border. Lines
# are measured in bytes, so keep them ASCII.
box() {
  local title="$1" inner=$((BOX_WIDTH - 2)) line pad i
  shift
  printf '\n%s%s %s%s%s ' "$FG_STEP" "$BOX_TL" "$BOLD" "$title" "$RST$FG_STEP" >&2
  for ((i = ${#title} + 2; i < inner; i++)); do printf '%s' "$BOX_H" >&2; done
  printf '%s%s\n' "$BOX_TR" "$RST" >&2
  for line in "" "$@"; do
    pad=$((inner - 3 - ${#line}))
    [ "$pad" -lt 0 ] && pad=0
    printf '%s%s%s   %s%*s%s%s%s\n' "$FG_STEP" "$BOX_V" "$RST" "$line" "$pad" '' "$FG_STEP" "$BOX_V" "$RST" >&2
  done
  printf '%s%s' "$FG_STEP" "$BOX_BL" >&2
  for ((i = 0; i < inner; i++)); do printf '%s' "$BOX_H" >&2; done
  printf '%s%s\n' "$BOX_BR" "$RST" >&2
}

# step N TITLE DETAIL prints the line of a finished step.
step() {
  printf '%s[%s/%s]%s %-26s %s\n' "$FG_STEP" "$1" "$TOTAL_STEPS" "$RST" "$2" "$3" >&2
}

get_time() {
  if [ -n "${EPOCHREALTIME:-}" ]; then
    printf '%s' "$EPOCHREALTIME"
  else
    # BSD date has no %N and prints a literal N.
    local t
    t=$(date +%s.%N 2>/dev/null || date +%s)
    case "$t" in
      *N) date +%s ;;
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
  case "$out" in "wrote /etc/systemd/system/dockmesh.service "*|*$'\n'"wrote /etc/systemd/system/dockmesh.service "*) UNIT_REWRITTEN=1 ;; esac
  printf '%s\n' "$out" | sed -n 's/^warning: //p' | while IFS= read -r line; do warn "$line"; done
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
#  Earlier installs
# ------------------------------------------------------------------

# Where an earlier install may have left something; the binary list gets
# the bin directory of this run in front.
BINARY_CANDIDATES=(/usr/local/bin/dockmesh)
UNIT_FILES=(/etc/systemd/system/dockmesh.service /lib/systemd/system/dockmesh.service /usr/lib/systemd/system/dockmesh.service /Library/LaunchDaemons/dev.dockmesh.service.plist)
DB_CANDIDATES=(/var/lib/dockmesh/data/dockmesh.db /data/data/dockmesh.db /usr/local/var/dockmesh/data/dockmesh.db)

# present PATH succeeds for anything at PATH, a dangling symlink included.
# A directory we can't look into counts as holding it.
present() {
  local dir
  if $USE_SUDO test -e "$1" || $USE_SUDO test -L "$1"; then
    return 0
  fi
  dir="$(dirname "$1")"
  $USE_SUDO test -d "$dir" && ! $USE_SUDO test -x "$dir"
}

# detect_install STAGED UNIT_ENV decides, once, whether this run upgrades
# an earlier install: a binary, service unit, config or database of one
# at any known place. STAGED is the binary DOCKMESH_SKIP_DOWNLOAD placed,
# which does not count; UNIT_ENV is the env file the loaded unit names.
# Sets FOUND_BINARY, FOUND_UNIT, FOUND_CONFIG and FOUND_DB to the first
# path of each kind and succeeds when any is set.
detect_install() {
  local staged="$1" unit_env="$2" p
  FOUND_BINARY="" FOUND_UNIT="" FOUND_CONFIG="" FOUND_DB=""
  for p in "${BINARY_CANDIDATES[@]}"; do
    if [ -n "$p" ] && [ "$p" != "$staged" ] && present "$p"; then
      FOUND_BINARY="$p"
      break
    fi
  done
  if [ "${HAS_SYSTEMD_UNIT:-0}" = "1" ] || [ "${HAS_LAUNCHD_UNIT:-0}" = "1" ]; then
    FOUND_UNIT="dockmesh.service"
  else
    for p in "${UNIT_FILES[@]}"; do
      if present "$p"; then
        FOUND_UNIT="$p"
        break
      fi
    done
  fi
  for p in "$ETC_ENV_FILE" "$unit_env" "${DOCKMESH_DATA_DIR:+$DOCKMESH_DATA_DIR/dockmesh.env}" "${ENV_FILE_CANDIDATES[@]}"; do
    if [ -n "$p" ] && present "$p"; then
      FOUND_CONFIG="$p"
      break
    fi
  done
  for p in "${DOCKMESH_DB_PATH:-}" "${DOCKMESH_DATA_DIR:+$DOCKMESH_DATA_DIR/data/dockmesh.db}" \
    "${unit_env:+$(dirname "$unit_env")/data/dockmesh.db}" "${DB_CANDIDATES[@]}"; do
    if [ -n "$p" ] && present "$p"; then
      FOUND_DB="$p"
      break
    fi
  done
  [ -n "$FOUND_BINARY$FOUND_UNIT$FOUND_CONFIG$FOUND_DB" ]
}

# kv KEY TEXT prints the value of the KEY=… line in TEXT, the --porcelain
# output of a dockmesh subcommand.
kv() {
  printf '%s\n' "$2" | sed -n "s/^$1=//p" | head -n 1
}

# fingerprint_lines FP prints a colon-separated SHA-256 fingerprint as two
# lines of 16 bytes each.
fingerprint_lines() {
  printf '%s\n%s\n' "${1:0:47}" "${1:48}"
}

# ------------------------------------------------------------------
#  Banner
# ------------------------------------------------------------------
REPO="dockmesh/dockmesh"
# DM_VERSION, not VERSION: /etc/os-release, sourced below, sets VERSION.
parse_args "$@"
DM_VERSION="${DOCKMESH_VERSION:-latest}"
if [ "$DM_VERSION" != "latest" ] && ! valid_tag "$DM_VERSION"; then
  die "release tag '$DM_VERSION' is not of the form vX.Y.Z or vX.Y.Z-rc.N"
fi
CHANNEL="${DOCKMESH_CHANNEL:-stable}"
INSTALL_DIR="${DOCKMESH_INSTALL_DIR:-/usr/local/bin}"
# Assets sit next to the bin directory: /usr/local/bin → /usr/local/share.
ASSET_DIR="${DOCKMESH_ASSET_DIR:-$(dirname "$INSTALL_DIR")/share/dockmesh}"
USE_SUDO="sudo"
if [ "$(id -u)" = "0" ] || [ "${DOCKMESH_NO_SUDO:-0}" = "1" ]; then
  USE_SUDO=""
fi
SKIP_DOWNLOAD="${DOCKMESH_SKIP_DOWNLOAD:-0}"
NO_BOOTSTRAP="${DOCKMESH_NO_BOOTSTRAP:-0}"
SEP='·'; ARROW='→'
if [ "$BOX_H" = "-" ]; then SEP='-'; ARROW='->'; fi

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$OS" in
  linux|darwin) ;;
  *) die "unsupported OS: $OS" ;;
esac
ARCH_RAW="$(uname -m)"
case "$ARCH_RAW" in
  x86_64|amd64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) die "unsupported architecture: $ARCH_RAW" ;;
esac
# Under `sudo bash` on macOS, secure_path hides the Homebrew prefixes.
if [ "$OS" = "darwin" ]; then
  export PATH="/opt/homebrew/bin:/opt/homebrew/sbin:/usr/local/bin:/usr/local/sbin:$PATH"
fi

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

install_hint() {
  local tool="$1"
  case "$DISTRO_ID" in
    macos) printf 'brew install %s' "$tool" ;;
    ubuntu|debian|linuxmint|pop|raspbian) printf 'sudo apt update && sudo apt install -y %s' "$tool" ;;
    fedora|rhel|centos|rocky|almalinux|ol) printf 'sudo dnf install -y %s' "$tool" ;;
    alpine) printf 'sudo apk add %s' "$tool" ;;
    arch|manjaro|endeavouros) printf 'sudo pacman -S --noconfirm %s' "$tool" ;;
    opensuse*|sles|sled) printf 'sudo zypper install -y %s' "$tool" ;;
    *) printf 'install "%s" with your package manager' "$tool" ;;
  esac
}

docker_hint() {
  case "$DISTRO_ID" in
    ubuntu|debian|linuxmint|pop|raspbian) printf 'sudo apt install -y docker.io' ;;
    fedora|rhel|centos|rocky|almalinux|ol) printf 'sudo dnf install -y docker-ce && sudo systemctl enable --now docker' ;;
    alpine) printf 'sudo apk add docker && sudo rc-update add docker default && sudo service docker start' ;;
    arch|manjaro|endeavouros) printf 'sudo pacman -S --noconfirm docker && sudo systemctl enable --now docker' ;;
    opensuse*|sles|sled) printf 'sudo zypper install -y docker && sudo systemctl enable --now docker' ;;
    macos) printf 'https://www.docker.com/products/docker-desktop/' ;;
    *) printf 'https://docs.docker.com/engine/install/' ;;
  esac
}

missing_tool() {
  box "Missing required tool: $1" \
    "$1 is required to continue. Install it on $DISTRO_NAME:" \
    "  $(install_hint "$2")" \
    "" \
    "Then run the installer again."
  exit 1
}

# ------------------------------------------------------------------
#  Upgrade or new install, decided once, before anything changes
# ------------------------------------------------------------------
HAS_SYSTEMD=0
HAS_SYSTEMD_UNIT=0
SYSTEMD_ACTIVE=0
SYSTEMD_PID=""
UNIT_ENV_FILE=""
HAS_LAUNCHD_UNIT=0
if [ "$OS" = "linux" ] && command -v systemctl >/dev/null 2>&1; then
  HAS_SYSTEMD=1
  if systemd_unit_loaded; then
    HAS_SYSTEMD_UNIT=1
    systemctl is-active --quiet dockmesh && SYSTEMD_ACTIVE=1
    SYSTEMD_PID="$(systemctl show -p MainPID --value dockmesh 2>/dev/null || true)"
    [ "$SYSTEMD_PID" = "0" ] && SYSTEMD_PID=""
    UNIT_ENV_FILE="$(systemctl show -p EnvironmentFiles --value dockmesh 2>/dev/null | awk '{print $1}' | sed 's/^-//' || true)"
  fi
fi
if [ "$OS" = "darwin" ] && [ -f /Library/LaunchDaemons/dev.dockmesh.service.plist ]; then
  HAS_LAUNCHD_UNIT=1
fi

STAGED=""
if [ "$SKIP_DOWNLOAD" = "1" ]; then
  [ -x "$INSTALL_DIR/dockmesh" ] || die "DOCKMESH_SKIP_DOWNLOAD=1 but $INSTALL_DIR/dockmesh is missing or not executable"
  STAGED="$INSTALL_DIR/dockmesh"
fi
BINARY_CANDIDATES=("$INSTALL_DIR/dockmesh" "${BINARY_CANDIDATES[@]}")
IS_UPGRADE=0
if detect_install "$STAGED" "$UNIT_ENV_FILE"; then
  IS_UPGRADE=1
fi
PREV_VERSION=""
if [ -n "$FOUND_BINARY" ] && [ -x "$FOUND_BINARY" ]; then
  PREV_VERSION="$("$FOUND_BINARY" --version 2>/dev/null | head -1 | grep -oE 'v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?' | head -1 || true)"
fi

# The config of an earlier install, wherever it was kept. One that fails
# the trust check stops the run here, before anything changed.
KEPT_ENV_FILE=""
if [ "$IS_UPGRADE" = "1" ] && [ "$OS" = "linux" ]; then
  KEPT_ENV_FILE="$(find_existing_env_file "${DOCKMESH_DATA_DIR:+$DOCKMESH_DATA_DIR/dockmesh.env}" "$UNIT_ENV_FILE" || true)"
  if [ -n "$KEPT_ENV_FILE" ] && [ -n "${DOCKMESH_DATA_DIR:-}" ] && [ "$(dirname "$KEPT_ENV_FILE")" != "${DOCKMESH_DATA_DIR%/}" ]; then
    die "DOCKMESH_DATA_DIR=$DOCKMESH_DATA_DIR, but the earlier install's config is $KEPT_ENV_FILE; set DOCKMESH_DATA_DIR=$(dirname "$KEPT_ENV_FILE") or move that file away"
  fi
fi

# ------------------------------------------------------------------
#  Checks
# ------------------------------------------------------------------
WARNINGS=()
command -v curl >/dev/null 2>&1 || missing_tool curl curl
command -v tar >/dev/null 2>&1 || missing_tool tar tar
if command -v sha256sum >/dev/null 2>&1; then
  sha256_of() { sha256sum "$1" | awk '{print $1}'; }
elif command -v shasum >/dev/null 2>&1; then
  sha256_of() { shasum -a 256 "$1" | awk '{print $1}'; }
else
  missing_tool sha256sum coreutils
fi
if [ "$SKIP_DOWNLOAD" != "1" ] && [ "${DOCKMESH_SKIP_CHECKSUM:-0}" != "1" ] &&
  [ "${DOCKMESH_SKIP_SIGNATURE:-0}" != "1" ] && ! signature_tool_ok; then
  box "Cannot verify the release signature" \
    "Dockmesh releases are signed; checking the signature needs" \
    "OpenSSL 3 or newer ('openssl', plus 'base64' and 'od')." \
    "Install it on $DISTRO_NAME:" \
    "  $(install_hint openssl)" \
    "" \
    "Or check the files with minisign and install them by hand:" \
    "  https://dockmesh.dev/docs/operations/verify-release/" \
    "" \
    "DOCKMESH_SKIP_SIGNATURE=1 installs without the check (not" \
    "recommended: the files are then trusted unchecked)."
  exit 1
fi
if [ ! -w "$INSTALL_DIR" ] && [ -z "$USE_SUDO" ]; then
  die "$INSTALL_DIR is not writable and sudo is disabled; set DOCKMESH_INSTALL_DIR to a writable path"
fi

SERVICE_MANAGER="no service manager"
if [ "$HAS_SYSTEMD" = "1" ]; then
  SERVICE_MANAGER="systemd"
elif [ "$OS" = "darwin" ] && command -v launchctl >/dev/null 2>&1; then
  SERVICE_MANAGER="launchd"
else
  WARNINGS+=("no systemd found: Dockmesh will not start on boot by itself")
fi

DOCKER_DETAIL="no Docker"
if command -v docker >/dev/null 2>&1; then
  if DOCKER_VER="$(docker version --format '{{.Server.Version}}' 2>/dev/null)" && [ -n "$DOCKER_VER" ]; then
    DOCKER_DETAIL="Docker $DOCKER_VER"
  else
    DOCKER_DETAIL="Docker (not running)"
    WARNINGS+=("Docker is installed but its daemon does not answer")
  fi
  if [ "$OS" = "darwin" ] && [ ! -S /var/run/docker.sock ]; then
    WARNINGS+=("/var/run/docker.sock is missing: in Docker Desktop, enable Settings > Advanced > \"Allow the default Docker socket to be used\"")
  fi
else
  WARNINGS+=("Docker was not found. Install it: $(docker_hint)")
fi

port_free() {
  local p="$1"
  if [ "$OS" = "darwin" ]; then
    command -v lsof >/dev/null 2>&1 || return 0
    ! lsof -iTCP:"$p" -sTCP:LISTEN -Pn >/dev/null 2>&1
  elif command -v ss >/dev/null 2>&1; then
    ! ss -Htln "sport = :$p" 2>/dev/null | grep -q .
  elif command -v netstat >/dev/null 2>&1; then
    ! netstat -tln 2>/dev/null | awk '{print $4}' | grep -Eq ":$p$"
  else
    return 0
  fi
}
if [ "$IS_UPGRADE" = "0" ]; then
  for p in 8080 8443; do
    port_free "$p" || WARNINGS+=("port $p is in use; Dockmesh needs it")
  done
  if [ -n "${DOCKMESH_DATA_DIR:-}" ] && [ "$OS" = "linux" ]; then
    WARNINGS+=("DOCKMESH_DATA_DIR is not used by a new install: choose the data directory in the setup wizard")
  fi
fi

OS_LABEL="Linux"
[ "$OS" = "darwin" ] && OS_LABEL="macOS"
CHECK_DETAIL="$OS_LABEL $ARCH $SEP $SERVICE_MANAGER $SEP $DOCKER_DETAIL"

# ------------------------------------------------------------------
#  Release
# ------------------------------------------------------------------
if [ "$SKIP_DOWNLOAD" = "1" ]; then
  DM_VERSION="$("$INSTALL_DIR/dockmesh" --version 2>/dev/null | head -1 | grep -oE 'v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?' | head -1 || true)"
  DM_VERSION="${DM_VERSION:-dev}"
else
  channel_warning "$CHANNEL"
  if [ "$DM_VERSION" = "latest" ]; then
    META_URL="https://api.github.com/repos/$REPO/releases/latest"
    if [ "$CHANNEL" = "testing" ]; then
      META_URL="https://api.github.com/repos/$REPO/releases?per_page=1"
    fi
    if ! META="$(curl -fsSL --retry 3 --retry-delay 2 "$META_URL" 2>&1)"; then
      die "failed to query $META_URL; check the network"
    fi
    DM_VERSION="$(printf '%s' "$META" | grep -oE '"tag_name"\s*:\s*"[^"]+"' | head -1 | sed -E 's/.*"([^"]+)"$/\1/')"
    [ -n "$DM_VERSION" ] || die "could not read the latest release tag from the GitHub API"
    valid_tag "$DM_VERSION" || die "GitHub returned an unexpected release tag '$DM_VERSION'"
  fi
  # An install running a release candidate is ahead of the latest stable
  # release until the final version is out.
  if [ "$IS_UPGRADE" = "1" ] && [ -z "${DOCKMESH_VERSION:-}" ] && [ -n "$PREV_VERSION" ] &&
    version_older "$DM_VERSION" "$PREV_VERSION" && [ "${DOCKMESH_FORCE:-0}" != "1" ]; then
    die "installed $PREV_VERSION is newer than $DM_VERSION; pass --version <tag> to pick a release (DOCKMESH_FORCE=1 installs $DM_VERSION anyway)"
  fi
  if [ "$IS_UPGRADE" = "1" ] && [ "$PREV_VERSION" = "$DM_VERSION" ] && [ "${DOCKMESH_FORCE:-0}" != "1" ]; then
    box "Already on ${DM_VERSION#v}" \
      "Dockmesh ${DM_VERSION#v} is installed already. Nothing was changed." \
      "" \
      "Reinstall anyway:" \
      "  curl -fsSL https://get.dockmesh.dev | sudo DOCKMESH_FORCE=1 bash"
    exit 0
  fi
fi
SHOWN_VERSION="${DM_VERSION#v}"

# ------------------------------------------------------------------
#  Steps
# ------------------------------------------------------------------
if [ "$IS_UPGRADE" = "1" ]; then
  if [ -n "$PREV_VERSION" ]; then
    say "${BOLD}Dockmesh installer${RST} $SEP upgrade ${PREV_VERSION#v} $ARROW $SHOWN_VERSION"
  else
    say "${BOLD}Dockmesh installer${RST} $SEP upgrade to $SHOWN_VERSION"
  fi
  TOTAL_STEPS=6
  [ "$OS" = "darwin" ] && TOTAL_STEPS=4
else
  say "${BOLD}Dockmesh installer${RST}"
  if [ "$OS" = "darwin" ] || [ "$HAS_SYSTEMD" = "0" ]; then
    TOTAL_STEPS=4
  elif [ "$NO_BOOTSTRAP" = "1" ]; then
    TOTAL_STEPS=5
  else
    TOTAL_STEPS=6
  fi
fi
say ""
N=0

TMP=""
NEW_BINARY=""
STOPPED=0
cleanup() {
  if [ -n "$TMP" ]; then rm -rf "$TMP"; fi
  if [ -n "$NEW_BINARY" ]; then $USE_SUDO rm -f "$NEW_BINARY"; fi
  # A run that stopped the service for its snapshot never leaves it down.
  if [ "$STOPPED" = "1" ]; then $USE_SUDO systemctl start dockmesh || true; fi
}
trap cleanup EXIT

N=$((N + 1)); step "$N" "Checking the system" "$CHECK_DETAIL"
for w in "${WARNINGS[@]+"${WARNINGS[@]}"}"; do warn "$w"; done

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

# ---- Download ------------------------------------------------------
if [ "$SKIP_DOWNLOAD" = "1" ]; then
  N=$((N + 1)); step "$N" "Downloading" "skipped, using $INSTALL_DIR/dockmesh (DOCKMESH_SKIP_DOWNLOAD)"
else
  TMP="$(mktemp -d)"
  TARBALL="dockmesh_${OS}_${ARCH}.tar.gz"
  URL="https://github.com/$REPO/releases/download/$DM_VERSION/$TARBALL"
  CHECKSUMS_URL="https://github.com/$REPO/releases/download/$DM_VERSION/checksums.txt"
  if ! curl -fL --retry 3 --retry-delay 2 --silent --show-error "$URL" -o "$TMP/$TARBALL"; then
    die "download failed; check that the release exists: $URL"
  fi
  if [ "${DOCKMESH_SKIP_CHECKSUM:-0}" = "1" ]; then
    DL_DETAIL="not verified (DOCKMESH_SKIP_CHECKSUM=1)"
  else
    if ! curl -fsSL --retry 2 -o "$TMP/checksums.txt" "$CHECKSUMS_URL" 2>/dev/null; then
      die "no checksums.txt published for $DM_VERSION; refusing to install an unverified binary (DOCKMESH_SKIP_CHECKSUM=1 overrides)"
    fi
    if [ "${DOCKMESH_SKIP_SIGNATURE:-0}" = "1" ]; then
      DL_DETAIL="checksum only (DOCKMESH_SKIP_SIGNATURE=1)"
    else
      if ! curl -fsSL --retry 2 -o "$TMP/checksums.txt.minisig" "$CHECKSUMS_URL.minisig" 2>/dev/null; then
        if version_older "$DM_VERSION" v0.6.0; then
          die "$DM_VERSION was released before signing existed and has no checksums.txt.minisig; install it with DOCKMESH_SKIP_SIGNATURE=1"
        fi
        die "$DM_VERSION has no checksums.txt.minisig, but every release from v0.6.0 on is signed: the release files may have been tampered with. Do not install it; report it to security@dockmesh.dev"
      fi
      mkdir -p "$TMP/sig"
      if ! SIGNED_BY="$(verify_release_signature "$TMP/checksums.txt" "$TMP/checksums.txt.minisig" "$DM_VERSION" "$RELEASE_PUBKEYS" "$TMP/sig")"; then
        die "the signature of checksums.txt does not verify for $DM_VERSION; refusing to install"
      fi
      DL_DETAIL="verified (minisign, key $SIGNED_BY)"
    fi
    EXPECTED="$(grep " ${TARBALL}$" "$TMP/checksums.txt" | awk '{print $1}' || true)"
    [ -n "$EXPECTED" ] || die "no checksum entry for $TARBALL; refusing to install an unverified binary (DOCKMESH_SKIP_CHECKSUM=1 overrides)"
    ACTUAL="$(sha256_of "$TMP/$TARBALL")"
    [ "$EXPECTED" = "$ACTUAL" ] || die "checksum mismatch: expected $EXPECTED, got $ACTUAL"
  fi
  tar -xzf "$TMP/$TARBALL" -C "$TMP"
  [ -x "$TMP/dockmesh" ] || die "the release archive has no dockmesh binary"
  N=$((N + 1)); step "$N" "Downloading $SHOWN_VERSION" "$DL_DETAIL"
  if [ "${DOCKMESH_SKIP_CHECKSUM:-0}" = "1" ]; then
    warn "checksum and signature were not checked (DOCKMESH_SKIP_CHECKSUM=1)"
  elif [ "${DOCKMESH_SKIP_SIGNATURE:-0}" = "1" ]; then
    warn "checksums.txt was not authenticated (DOCKMESH_SKIP_SIGNATURE=1)"
  fi
fi

# install_release_files installs dmctl, the scanner and the agent assets
# from the release and prints what it installed after the server binary.
install_release_files() {
  local list=""
  if [ -f "$TMP/dmctl" ]; then
    $USE_SUDO install -m 0755 "$TMP/dmctl" "$INSTALL_DIR/dmctl"
    list="$list, dmctl"
  fi
  if [ "${DOCKMESH_NO_SCANNER:-0}" != "1" ] && ! command -v grype >/dev/null 2>&1; then
    if install_grype; then
      list="$list, grype"
    else
      warn "the Grype scanner could not be installed; image scans stay off (https://github.com/anchore/grype#installation)"
    fi
  fi
  $USE_SUDO mkdir -p "$ASSET_DIR/bin"
  if [ -f "$TMP/install-agent.sh" ]; then
    $USE_SUDO install -m 0755 "$TMP/install-agent.sh" "$ASSET_DIR/install-agent.sh"
  fi
  install_agent_binaries "$TMP" "$ASSET_DIR" "$OS" "$ARCH"
  printf '%s' "$list"
}

USER_CREATED=0
ensure_service_user() {
  if ! id dockmesh >/dev/null 2>&1; then
    $USE_SUDO useradd --system --no-create-home --shell /usr/sbin/nologin dockmesh
    USER_CREATED=1
  fi
  if getent group docker >/dev/null 2>&1; then
    $USE_SUDO usermod -aG docker dockmesh
  fi
}

# ------------------------------------------------------------------
#  Upgrade
# ------------------------------------------------------------------
if [ "$IS_UPGRADE" = "1" ] && [ "$OS" = "darwin" ]; then
  if [ "$SKIP_DOWNLOAD" = "1" ]; then
    N=$((N + 1)); step "$N" "Installing binaries" "pre-staged"
  else
    $USE_SUDO cp -a "$INSTALL_DIR/dockmesh" "$INSTALL_DIR/dockmesh.bak" 2>/dev/null || true
    $USE_SUDO install -m 0755 "$TMP/dockmesh" "$INSTALL_DIR/dockmesh"
    EXTRA="$(install_release_files)"
    N=$((N + 1)); step "$N" "Installing binaries" "$INSTALL_DIR/dockmesh$EXTRA"
  fi
  if launchctl print system/dev.dockmesh.service >/dev/null 2>&1 &&
    $USE_SUDO launchctl kickstart -k system/dev.dockmesh.service 2>/dev/null; then
    N=$((N + 1)); step "$N" "Restarting" "ok"
  else
    N=$((N + 1)); step "$N" "Restarting" "not restarted: sudo launchctl kickstart -k system/dev.dockmesh.service"
  fi
  say ""
  status ok "$FG_OK" "Dockmesh $SHOWN_VERSION is installed. Sign in as usual."
  exit 0
fi

if [ "$IS_UPGRADE" = "1" ]; then
  # A unit is written when the host had one, or kept a config or database
  # for it; a lone binary someone runs by hand gets no service.
  WRITE_UNIT=0
  if [ "$HAS_SYSTEMD" = "1" ] && [ -n "$FOUND_UNIT$FOUND_CONFIG$FOUND_DB" ]; then
    WRITE_UNIT=1
  fi

  # ---- Snapshot ----------------------------------------------------
  # The new binary takes the snapshot, so it does not depend on what the
  # installed version knows; it is staged next to the old one first.
  NEW_BIN="$INSTALL_DIR/dockmesh"
  if [ "$SKIP_DOWNLOAD" != "1" ]; then
    NEW_BINARY="$INSTALL_DIR/.dockmesh.new"
    $USE_SUDO install -m 0755 "$TMP/dockmesh" "$NEW_BINARY"
    NEW_BIN="$NEW_BINARY"
  fi
  SNAP_DETAIL="skipped, no systemd"
  if [ "$OS" = "linux" ]; then
    ensure_service_user
    if [ "$USER_CREATED" = "1" ]; then
      # A recreated service user may have a new UID; the data of the
      # earlier install is handed back to it.
      KEPT_DATA=""
      if [ -n "$KEPT_ENV_FILE" ]; then
        KEPT_DATA="$(dirname "$KEPT_ENV_FILE")"
      elif [ -n "$FOUND_DB" ] && [ "$(basename "$(dirname "$FOUND_DB")")" = "data" ]; then
        KEPT_DATA="$(dirname "$(dirname "$FOUND_DB")")"
      fi
      if [ -n "$KEPT_DATA" ] && data_dir_allowed "$KEPT_DATA"; then
        $USE_SUDO mkdir -p "$KEPT_DATA/data" "$KEPT_DATA/stacks"
        $USE_SUDO chown dockmesh:docker "$KEPT_DATA"
        $USE_SUDO chown -R dockmesh:docker "$KEPT_DATA/data" "$KEPT_DATA/stacks"
        $USE_SUDO chmod 750 "$KEPT_DATA"
      fi
    fi
    if [ "$HAS_SYSTEMD_UNIT" = "1" ]; then
      # Installs up to v0.1.3 ran the service as root; the unit written
      # below runs it as dockmesh, which needs the data dir.
      CURRENT_USER="$(systemctl show -p User --value dockmesh 2>/dev/null || true)"
      if { [ -z "$CURRENT_USER" ] || [ "$CURRENT_USER" = "root" ]; } && [ -n "$UNIT_ENV_FILE" ] && $USE_SUDO test -f "$UNIT_ENV_FILE"; then
        ROOT_DATA="$(dirname "$UNIT_ENV_FILE")"
        if data_dir_allowed "$ROOT_DATA"; then
          $USE_SUDO chown -R dockmesh:dockmesh "$ROOT_DATA"
          $USE_SUDO chmod 700 "$ROOT_DATA"
        fi
      fi
      if [ "$SYSTEMD_ACTIVE" = "1" ]; then
        STOPPED=1
        $USE_SUDO systemctl stop dockmesh || true
      fi
    fi
    RESTART_T0=$(get_time)
    if SNAP_OUT="$($USE_SUDO "$NEW_BIN" system snapshot-db 2>&1)"; then
      SNAP_DETAIL="$(printf '%s\n' "$SNAP_OUT" | tail -n 1)"
    else
      SNAP_DETAIL="skipped"
      warn "no database snapshot: $(printf '%s' "$SNAP_OUT" | tail -n 1)"
    fi
  fi
  N=$((N + 1)); step "$N" "Taking a snapshot" "$SNAP_DETAIL"

  # ---- Binaries ----------------------------------------------------
  if [ "$SKIP_DOWNLOAD" = "1" ]; then
    N=$((N + 1)); step "$N" "Installing binaries" "pre-staged $INSTALL_DIR/dockmesh"
  else
    BAK_NOTE=""
    if $USE_SUDO test -f "$INSTALL_DIR/dockmesh" && $USE_SUDO cp -a "$INSTALL_DIR/dockmesh" "$INSTALL_DIR/dockmesh.bak" 2>/dev/null; then
      BAK_NOTE=" (previous kept as dockmesh.bak)"
    fi
    $USE_SUDO mv -f "$NEW_BINARY" "$INSTALL_DIR/dockmesh"
    NEW_BINARY=""
    EXTRA="$(install_release_files)"
    N=$((N + 1)); step "$N" "Installing binaries" "$INSTALL_DIR/dockmesh$EXTRA$BAK_NOTE"
  fi

  # ---- Config and unit ---------------------------------------------
  MIGRATE_DETAIL="no service unit"
  if [ "$OS" = "linux" ]; then
    MOVED_LINES=""
    if MIGRATE_OUT="$($USE_SUDO "$INSTALL_DIR/dockmesh" system migrate-layout ${KEPT_ENV_FILE:+--from "$KEPT_ENV_FILE"} 2>&1)"; then
      MOVED_LINES="$(printf '%s\n' "$MIGRATE_OUT" | grep '^moved  ' || true)"
    else
      warn "config left at its old place, Dockmesh keeps using it: $(printf '%s' "$MIGRATE_OUT" | tail -n 1)"
    fi
    # The unit loads the root-owned file, never the symlink left behind.
    ENV_FILE="$KEPT_ENV_FILE"
    if [ -n "$ENV_FILE" ] && $USE_SUDO test -L "$ENV_FILE" && [ "$($USE_SUDO readlink "$ENV_FILE")" = "$ETC_ENV_FILE" ]; then
      ENV_FILE="$ETC_ENV_FILE"
    fi
    if [ -n "$ENV_FILE" ]; then
      warn_stale_asset_paths "$ENV_FILE" "$ASSET_DIR"
    fi
    if $USE_SUDO test -f "$ETC_ENV_FILE"; then
      MIGRATE_DETAIL="config in $ETC_ENV_FILE"
    elif [ -n "$ENV_FILE" ]; then
      MIGRATE_DETAIL="config stays in $ENV_FILE"
    else
      MIGRATE_DETAIL="no config yet"
    fi
    if [ "$WRITE_UNIT" = "1" ]; then
      if write_unit ${ENV_FILE:+--env-file "$ENV_FILE"}; then
        MIGRATE_DETAIL="$MIGRATE_DETAIL, unit from the template"
      else
        warn "unit kept as it was; Dockmesh keeps running with it"
      fi
    fi
  fi
  N=$((N + 1)); step "$N" "Migrating config" "$MIGRATE_DETAIL"
  if [ -n "${MOVED_LINES:-}" ]; then
    printf '%s\n' "$MOVED_LINES" | while IFS= read -r line; do status moved "$FG_OK" "${line#moved  }"; done
  fi

  # ---- Restart -----------------------------------------------------
  if [ "$WRITE_UNIT" = "1" ]; then
    VERB="restart"
    [ "$HAS_SYSTEMD_UNIT" = "1" ] || VERB="enable --now"
    if ! start_service "$VERB"; then
      die "dockmesh did not start; see: journalctl -u dockmesh --since '5 min ago'"
    fi
    STOPPED=0
    if ! service_runs_new_binary "$SYSTEMD_PID"; then
      die "dockmesh.service still runs the old binary; run: sudo systemctl restart dockmesh"
    fi
    RUNNING_VERSION="$("$INSTALL_DIR/dockmesh" --version 2>/dev/null | head -1 || true)"
    case "$RUNNING_VERSION" in
      *"$DM_VERSION"*) ;;
      *) die "the installed binary reports '${RUNNING_VERSION:-unknown}', expected $DM_VERSION" ;;
    esac
    DOWNTIME=$(awk -v a="$(get_time)" -v b="${RESTART_T0:-0}" 'BEGIN { printf "%.1f", a - b }')
    N=$((N + 1)); step "$N" "Restarting" "ok, down ${DOWNTIME} s"
  else
    N=$((N + 1)); step "$N" "Restarting" "skipped: no dockmesh service on this host, restart dockmesh yourself"
  fi

  say ""
  STATUS_OUT=""
  if [ "$OS" = "linux" ]; then
    STATUS_OUT="$($USE_SUDO "$INSTALL_DIR/dockmesh" setup status --porcelain 2>/dev/null || true)"
  fi
  if [ "$(kv setup "$STATUS_OUT")" = "unfinished" ]; then
    status ok "$FG_OK" "Dockmesh $SHOWN_VERSION is running."
    box "Setup was never finished" \
      "Get a setup code with: sudo dockmesh setup new-code" \
      "Then open $(kv url "$STATUS_OUT")"
  else
    status ok "$FG_OK" "Dockmesh $SHOWN_VERSION is running. Sign in as usual."
  fi
  say ""
  say "   ${DIM}Docs${RST}     https://dockmesh.dev/docs"
  say ""
  exit 0
fi

# ------------------------------------------------------------------
#  New install
# ------------------------------------------------------------------
if [ "$SKIP_DOWNLOAD" = "1" ]; then
  N=$((N + 1)); step "$N" "Installing binaries" "pre-staged $INSTALL_DIR/dockmesh"
else
  $USE_SUDO install -m 0755 "$TMP/dockmesh" "$INSTALL_DIR/dockmesh"
  EXTRA="$(install_release_files)"
  N=$((N + 1)); step "$N" "Installing binaries" "$INSTALL_DIR/dockmesh$EXTRA"
fi

# setup_box prints the setup code box from `dockmesh setup new-code`.
setup_box() {
  local out code fp
  if ! out="$($USE_SUDO "$INSTALL_DIR/dockmesh" setup new-code --porcelain 2>/dev/null)" || [ -z "$(kv code "$out")" ]; then
    box "Finish the setup in your browser" \
      "Get a setup code with: sudo dockmesh setup new-code" \
      "It prints the link to open."
    return
  fi
  code="$(kv code "$out")"
  fp="$(kv fingerprint "$out")"
  local lines=("$(kv url "$out")" "" "Setup code   $code" "Valid until  $(kv valid_until "$out") (2 hours)" "")
  if [ -n "$fp" ]; then
    lines+=("Your browser will warn about the certificate. Check that" "it shows this SHA-256 fingerprint before you continue:")
    while IFS= read -r l; do lines+=("$l"); done < <(fingerprint_lines "$fp")
    lines+=("")
  fi
  lines+=("New code:    sudo dockmesh setup new-code")
  box "Finish the setup in your browser" "${lines[@]}"
}

if [ "$OS" = "darwin" ]; then
  # macOS has no root updater to apply the setup, so the service keeps
  # the bootstrap env file the wizard finishes.
  DATA_DIR="${DOCKMESH_DATA_DIR:-/usr/local/var/dockmesh}"
  $USE_SUDO mkdir -p "$DATA_DIR/data" "$DATA_DIR/stacks"
  ENV_FILE="$DATA_DIR/dockmesh.env"
  write_env_file "$ENV_FILE" "$DATA_DIR" "$ASSET_DIR"
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
  $USE_SUDO launchctl bootstrap system "$PLIST" 2>/dev/null || $USE_SUDO launchctl load -w "$PLIST"
  N=$((N + 1)); step "$N" "Starting setup mode" "ok"
  setup_box
  say ""
  say "   ${DIM}Docs${RST}     https://dockmesh.dev/docs"
  say ""
  exit 0
fi

ensure_service_user
# Setup mode keeps its state in the data dir until the wizard has chosen
# where the data goes.
DATA_DIR=/var/lib/dockmesh
$USE_SUDO mkdir -p "$DATA_DIR"
$USE_SUDO chown dockmesh:docker "$DATA_DIR" 2>/dev/null || $USE_SUDO chown dockmesh "$DATA_DIR"
$USE_SUDO chmod 750 "$DATA_DIR"
N=$((N + 1)); step "$N" "Creating user dockmesh" "groups: $($USE_SUDO "$INSTALL_DIR/dockmesh" system unit-groups 2>/dev/null || echo docker)"

if [ "$HAS_SYSTEMD" = "0" ]; then
  box "Binaries installed" \
    "This host has no systemd. Start Dockmesh with:" \
    "  sudo -u dockmesh $INSTALL_DIR/dockmesh serve" \
    "Then get a setup code with: sudo dockmesh setup new-code"
  exit 0
fi

write_unit || die "could not write /etc/systemd/system/dockmesh.service"
N=$((N + 1)); step "$N" "Installing services" "dockmesh.service, dockmesh-update.path"

if [ "$NO_BOOTSTRAP" = "1" ]; then
  $USE_SUDO systemctl enable dockmesh >/dev/null 2>&1 || true
  box "Binaries installed" \
    "Start the setup with:" \
    "  sudo systemctl start dockmesh && sudo dockmesh setup new-code"
  say "   ${DIM}Docs${RST}     https://dockmesh.dev/docs"
  say ""
  exit 0
fi

if ! start_service "enable --now"; then
  die "dockmesh did not start; see: journalctl -u dockmesh --since '5 min ago'"
fi
# The fingerprint needs the certificate setup mode creates at its start.
for _ in $(seq 1 30); do
  curl -fsk "https://127.0.0.1:8080/api/v1/health" >/dev/null 2>&1 && break
  curl -fs "http://127.0.0.1:8080/api/v1/health" >/dev/null 2>&1 && break
  sleep 1
done
N=$((N + 1)); step "$N" "Starting setup mode" "ok"
setup_box
say ""
say "   ${DIM}Docs${RST}     https://dockmesh.dev/docs"
say ""
