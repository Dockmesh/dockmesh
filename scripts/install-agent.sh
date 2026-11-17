#!/usr/bin/env bash
#
# Dockmesh agent installer.
#
# This script is templated by the dockmesh server at request time. The
# {{ }} tokens are replaced with the URLs, the agent listener's key pin,
# the agents CA and the agent binary checksums. The agent listener serves
# it without a token; the enrollment token is passed with --token.
#
# The script checks the host, installs the verified binary to
# /usr/local/bin and creates the dockmesh-agent account. Everything else
# (agent.env, the agent unit and its updater units) is written by
# `dockmesh-agent system install`, the same code the root updater runs.
# A re-run without a token keeps the enrollment and moves an agent of the
# legacy layout to this one; with a token it enrolls again.

set -euo pipefail

# -----------------------------------------------------------------------------
# Templated by the server
# -----------------------------------------------------------------------------
# Filled in only by the deprecated web-port route (/install/agent.sh?token=).
TOKEN="{{TOKEN}}"
INSTALL_URL="{{INSTALL_URL}}"
INSTALL_PIN="{{INSTALL_PIN}}"
ENROLL_URL="{{ENROLL_URL}}"
AGENT_URL="{{AGENT_URL}}"
CA_FINGERPRINT="{{CA_FINGERPRINT}}"
BINARY_SHA256_AMD64="{{BINARY_SHA256_AMD64}}"
BINARY_SHA256_ARM64="{{BINARY_SHA256_ARM64}}"
read -r -d '' CA_CERT_PEM <<'CAPEM' || true
{{CA_CERT_PEM}}
CAPEM

# -----------------------------------------------------------------------------
# Flags
# -----------------------------------------------------------------------------
AS_ROOT=0
SWITCH_USER=0
SKIP_DOCKER=0
DATA_DIR_FLAG=""
STACKS_DIR=""
need_value() { [[ $1 -ge 2 ]] || { echo "$2 needs a value" >&2; exit 1; }; }
while [[ $# -gt 0 ]]; do
  case "$1" in
    --token)        need_value $# "$1"; TOKEN="$2"; shift 2 ;;
    --token=*)      TOKEN="${1#--token=}"; shift ;;
    --data-dir)     need_value $# "$1"; DATA_DIR_FLAG="$2"; shift 2 ;;
    --data-dir=*)   DATA_DIR_FLAG="${1#--data-dir=}"; shift ;;
    --stacks-dir)   need_value $# "$1"; STACKS_DIR="$2"; shift 2 ;;
    --stacks-dir=*) STACKS_DIR="${1#--stacks-dir=}"; shift ;;
    --as-root)      AS_ROOT=1; shift ;;
    --switch-to-service-user) SWITCH_USER=1; shift ;;
    --skip-docker)  SKIP_DOCKER=1; shift ;;
    -h|--help)
      cat <<USAGE
Dockmesh agent installer.

Usage:
  curl -fsSLk --pinnedpubkey '<pin>' ${INSTALL_URL}/install/agent.sh | sudo bash -s -- --token <token> [flags]

Copy the full command from Hosts › Add host in Dockmesh. Run it again
without --token to repair an agent or move it to the current layout.

Flags:
  --token <token>       One-time enrollment token from the install command.
                        On an enrolled host it enrolls again (the old
                        certificate stays until the new one is in place).
  --data-dir <path>     Data directory of the agent, first install only
                        (default /var/lib/dockmesh-agent).
  --stacks-dir <path>   Stacks directory of the agent (default <data-dir>/stacks).
  --switch-to-service-user
                        Move an agent that runs as root to the
                        dockmesh-agent user (re-run only).
  --skip-docker         Don't install Docker when it is missing.
  -h, --help            Show this help.
USAGE
      exit 0
      ;;
    *) echo "unknown flag: $1" >&2; exit 1 ;;
  esac
done

# -----------------------------------------------------------------------------
# Output
# -----------------------------------------------------------------------------
# Plain output without a terminal, with NO_COLOR or TERM=dumb.
if [[ -t 1 && -z "${NO_COLOR:-}" && "${TERM:-dumb}" != dumb ]]; then
  C_BOLD=$'\033[1m'; C_DIM=$'\033[2m'; C_RED=$'\033[1;31m'; C_GREEN=$'\033[1;32m'; C_YELLOW=$'\033[1;33m'; C_OFF=$'\033[0m'
  BOX_TL='┌'; BOX_TR='┐'; BOX_BL='└'; BOX_BR='┘'; BOX_H='─'; BOX_V='│'
else
  C_BOLD=''; C_DIM=''; C_RED=''; C_GREEN=''; C_YELLOW=''; C_OFF=''
  BOX_TL='+'; BOX_TR='+'; BOX_BL='+'; BOX_BR='+'; BOX_H='-'; BOX_V='|'
fi
STEP=0
STEPS=6
step() { STEP=$((STEP + 1)); printf '%s[%d/%d]%s %-24s %s\n' "$C_BOLD" "$STEP" "$STEPS" "$C_OFF" "$1" "$2"; }
note() { printf '%s%-7s%s  %s\n' "$C_DIM" "$1" "$C_OFF" "$2"; }
warn() { printf '%swarning%s  %s\n' "$C_YELLOW" "$C_OFF" "$*" >&2; }
die() { printf '%s%s%s\n' "$C_RED" "$*" "$C_OFF" >&2; exit 1; }

# box prints a closing box titled $1 with the lines after it.
box() {
  local title="$1" width=63 line fill
  shift
  printf -v fill '%*s' $((width - ${#title} - 2)) ''
  printf '\n%s %s %s%s\n' "$BOX_TL" "$title" "${fill// /$BOX_H}" "$BOX_TR"
  printf '%s%*s%s\n' "$BOX_V" "$width" '' "$BOX_V"
  for line in "$@"; do
    printf '%s   %s%*s%s\n' "$BOX_V" "$line" $((width - 3 - ${#line})) '' "$BOX_V"
  done
  printf -v fill '%*s' "$width" ''
  printf '%s%s%s\n' "$BOX_BL" "${fill// /$BOX_H}" "$BOX_BR"
}
# end box

# -----------------------------------------------------------------------------
# Helpers
# -----------------------------------------------------------------------------
detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64)        echo amd64 ;;
    aarch64|arm64)       echo arm64 ;;
    *) die "unsupported architecture: $(uname -m)" ;;
  esac
}

# pinned_get downloads $1 from the agent listener to $2. -k only skips the
# chain check: curl still refuses any server whose key is not INSTALL_PIN.
pinned_get() {
  curl -fsSLk --pinnedpubkey "$INSTALL_PIN" --retry 3 --retry-delay 2 -o "$2" "$1"
}

# agent_log_state classifies the agent's JSON log lines ($1) as connected,
# rejected (the server refused the token), failed (enrollment failed for
# another reason) or waiting. A failed re-enrollment is not final: the
# agent goes on with its previous certificate.
agent_log_state() {
  local logs="$1"
  if grep -q '"msg":"connected"' <<<"$logs"; then
    echo connected
  elif grep '"msg":"enrollment failed"' <<<"$logs" | grep -q 'enroll http 401'; then
    echo rejected
  elif grep -q '"msg":"enrollment failed"' <<<"$logs"; then
    echo failed
  else
    echo waiting
  fi
}
# end agent_log_state

# log_field prints the JSON string field $2 of the last log line in $1
# whose msg is $3.
log_field() {
  grep "\"msg\":\"$3\"" <<<"$1" | tail -1 | sed -n "s/.*\"$2\":\"\\([^\"]*\\)\".*/\\1/p"
}
# end log_field

# is_server_data_dir succeeds if a Dockmesh server keeps its data in $1.
# Agents installed by older releases defaulted to the server's directory.
is_server_data_dir() {
  local d="${1%/}"
  [[ -e "$d/dockmesh.db" || -e "$d/data/dockmesh.db" || -e "$d/dockmesh.env" ]] && return 0
  [[ "$d" == /var/lib/dockmesh && ( -e /etc/dockmesh/dockmesh.env || -e /etc/systemd/system/dockmesh.service ) ]]
}
# end is_server_data_dir

# migrate_agent_files moves the agent's own files from $1 to $2 and leaves
# everything else in $1 alone. It runs as the account that owns both
# directories (as_agent).
migrate_agent_files() {
  local from="${1%/}" to="${2%/}" f
  for f in agent.crt agent.key agent.crt.next agent.key.next agent.url ca.crt enroll-ca.pem enroll-token certs staging; do
    if [[ -e "$from/$f" || -L "$from/$f" ]]; then
      rm -rf "${to:?}/$f"
      mv -T "$from/$f" "$to/$f"
    fi
  done
}
# end migrate_agent_files

# remove_legacy_bin removes the legacy binary in $1/bin once nothing runs
# it any more; as_agent runs it as the account that owns $1.
remove_legacy_bin() {
  if [[ -d "$1/bin" && ! -L "$1/bin" ]]; then
    rm -f "$1/bin/dockmesh-agent"
    rmdir "$1/bin" 2>/dev/null || true
  fi
}
# end remove_legacy_bin

# write_agent_file writes stdin to $1 as the account $2:$3 that owns the
# data dir, mode 0600: written as root, a link the account planted there
# would be followed.
write_agent_file() {
  local dest="$1" user="$2" group="$3"
  local script='umask 077 && tmp="$(mktemp "$1.XXXXXX")" && cat >"$tmp" && { mv -fT "$tmp" "$1" || { rm -f "$tmp"; exit 1; }; }'
  if [[ "$user" == root ]]; then
    sh -c "$script" sh "$dest"
  else
    setpriv --reuid="$user" --regid="$group" --clear-groups sh -c "$script" sh "$dest"
  fi
}
# end write_agent_file

# as_agent runs the shell function $3 with the arguments after it as the
# account $1:$2, so whatever that account planted in its directories
# cannot redirect a root operation.
as_agent() {
  local user="$1" group="$2" fn="$3"
  shift 3
  if [[ "$user" == root ]]; then
    "$fn" "$@"
  else
    setpriv --reuid="$user" --regid="$group" --clear-groups bash -c "set -euo pipefail; $(declare -f "$fn"); $fn \"\$@\"" "$fn" "$@"
  fi
}
# end as_agent

# selinux_enabled succeeds while SELinux is on, enforcing or permissive.
selinux_enabled() {
  if command -v selinuxenabled >/dev/null 2>&1; then
    selinuxenabled
    return
  fi
  [[ -e "${SELINUX_ENFORCE_FILE:-/sys/fs/selinux/enforce}" ]]
}
# end selinux_enabled

# start_failure_hint tells what to check when the service does not run
# the agent binary $1, SELinux first when it is on.
start_failure_hint() {
  local bin="$1" mode label
  if selinux_enabled; then
    mode="$(getenforce 2>/dev/null || echo enabled)"
    label="$(stat -c %C -- "$bin" 2>/dev/null || echo unknown)"
    printf 'SELinux is %s on this host. The agent binary needs the type bin_t and has %s.\n' "$mode" "$label"
    printf '  denials:  ausearch -m avc -ts recent | grep dockmesh\n'
    printf '  fix:      restorecon -v %s && systemctl restart dockmesh-agent\n' "$bin"
  fi
  printf '  status:   systemctl status dockmesh-agent\n'
  printf '  logs:     journalctl -u dockmesh-agent -n 50\n'
  printf 'Run the install command again once the cause is fixed.\n'
}
# end start_failure_hint

# unit_user prints the User= of the agent unit, drop-ins included, empty
# for root; $1 is the unit file, read when systemd can't be asked.
unit_user() {
  local u
  if [[ "$(systemctl show -p LoadState --value dockmesh-agent.service 2>/dev/null)" == loaded ]]; then
    u="$(systemctl show -p User --value dockmesh-agent.service)"
  else
    u="$(sed -n 's/^[[:space:]]*User=[[:space:]]*//p' "$1" | tail -1)"
  fi
  [[ "$u" == root ]] && u=""
  printf '%s' "$u"
}
# end unit_user

# -----------------------------------------------------------------------------
# What is installed already
# -----------------------------------------------------------------------------
PATH_BIN=/usr/local/bin/dockmesh-agent
ENV_DIR=/etc/dockmesh-agent
ENV_FILE="$ENV_DIR/agent.env"
UNIT_FILE=/etc/systemd/system/dockmesh-agent.service
LEGACY_SCRIPT="$ENV_DIR/refresh-path-copy"
DEFAULT_DATA_DIR=/var/lib/dockmesh-agent
SERVER_HOST="$(sed -E 's#^[a-z]+://##; s#/.*##' <<<"$INSTALL_URL")"

case "$(uname -s)" in
  Linux) ;;
  *) die "The Dockmesh agent installer supports Linux only. This host runs $(uname -s)." ;;
esac
[[ $EUID -eq 0 ]] || die "The agent installer must run as root: … | sudo bash -s -- --token <token>"
command -v systemctl >/dev/null 2>&1 && [[ -d /run/systemd/system ]] ||
  die "Dockmesh agents need systemd. This host has none. See https://dockmesh.dev/docs/features/multi-host#requirements"
ARCH="$(detect_arch)"
for tool in curl sha256sum setpriv; do
  command -v "$tool" >/dev/null 2>&1 || die "$tool is required (install curl, coreutils and util-linux) and run again."
done

PREV_DATA_DIR=""
if [[ -r "$ENV_FILE" ]]; then
  PREV_DATA_DIR="$(sed -n 's/^DOCKMESH_DATA_DIR=//p' "$ENV_FILE" | head -1)"
fi
if [[ -n "$PREV_DATA_DIR" ]]; then
  DATA_DIR="$PREV_DATA_DIR"
elif [[ ! -e "$DEFAULT_DATA_DIR/agent.crt" && -e /var/lib/dockmesh/agent.crt ]]; then
  DATA_DIR=/var/lib/dockmesh
else
  DATA_DIR="$DEFAULT_DATA_DIR"
fi

UNIT_EXISTS=0
[[ -f "$UNIT_FILE" ]] && UNIT_EXISTS=1
ENROLLED=0
if [[ -e "$DATA_DIR/ca.crt" && ( -e "$DATA_DIR/agent.crt" || -e "$DATA_DIR/agent.crt.next" ) ]]; then
  ENROLLED=1
fi
RERUN=0
[[ $UNIT_EXISTS -eq 1 || $ENROLLED -eq 1 ]] && RERUN=1
LEGACY=0
if [[ -e "$LEGACY_SCRIPT" || -e "$DATA_DIR/bin/dockmesh-agent" || -L "$DATA_DIR/bin/dockmesh-agent" ]]; then
  LEGACY=1
elif [[ $UNIT_EXISTS -eq 1 ]] && ! grep -qx "ExecStart=$PATH_BIN" "$UNIT_FILE"; then
  LEGACY=1
fi
# OWNER is the account that owns the agent's files now, RUN_USER the one
# it runs as after this run; they differ only with --switch-to-service-user.
OWNER=dockmesh-agent
if [[ $UNIT_EXISTS -eq 1 && -z "$(unit_user "$UNIT_FILE")" ]]; then
  OWNER=root
fi
RUN_USER="$OWNER"
SWITCHING=0
if [[ $SWITCH_USER -eq 1 && $OWNER == root ]]; then
  RUN_USER=dockmesh-agent
  SWITCHING=1
fi

if [[ $RERUN -eq 0 ]]; then
  MODE=fresh
  printf '%sDockmesh agent installer%s · server %s\n\n' "$C_BOLD" "$C_OFF" "$SERVER_HOST"
elif [[ -n "$TOKEN" ]]; then
  MODE=reenroll
  printf '%sDockmesh agent installer%s · re-enroll · server %s\n\n' "$C_BOLD" "$C_OFF" "$SERVER_HOST"
else
  MODE=rerun
  printf '%sDockmesh agent installer%s · re-run · server %s\n\n' "$C_BOLD" "$C_OFF" "$SERVER_HOST"
fi

# -----------------------------------------------------------------------------
# [1] Checks
# -----------------------------------------------------------------------------
if [[ -n "$TOKEN" && ! "$TOKEN" =~ ^[A-Za-z0-9_-]{16,128}$ ]]; then
  die "The enrollment token is malformed. Copy the install command from Hosts › Add host in Dockmesh."
fi
if [[ $MODE == fresh && -z "$TOKEN" ]]; then
  die "No enrollment token. Copy the install command from Hosts › Add host in Dockmesh."
fi
if [[ $MODE == rerun && $ENROLLED -eq 0 ]]; then
  die "This host is not enrolled. Copy the install command from Hosts › Add host in Dockmesh."
fi
if [[ $RERUN -eq 0 && $AS_ROOT -eq 1 ]]; then
  die "--as-root was removed in 0.6.0. The agent runs as the dockmesh-agent user with access to Docker.
See https://dockmesh.dev/docs/features/multi-host#agent-user"
fi
if [[ $RERUN -eq 1 && $AS_ROOT -eq 1 && $OWNER != root ]]; then
  die "The agent runs as dockmesh-agent; --as-root does not switch it back to root."
fi
if [[ $SWITCH_USER -eq 1 && $RERUN -eq 0 ]]; then
  die "--switch-to-service-user moves an installed agent. A new agent runs as dockmesh-agent anyway."
fi
if [[ $SWITCH_USER -eq 1 && $AS_ROOT -eq 1 ]]; then
  die "--switch-to-service-user and --as-root contradict each other."
fi
if [[ -n "$DATA_DIR_FLAG" ]]; then
  if [[ $RERUN -eq 1 && "${DATA_DIR_FLAG%/}" != "${DATA_DIR%/}" ]]; then
    die "The agent's data directory is $DATA_DIR. It is chosen at the first install only."
  fi
  [[ "$DATA_DIR_FLAG" =~ ^/[A-Za-z0-9._/-]+$ ]] ||
    die "--data-dir must be an absolute path of letters, digits and ._-/"
  DATA_DIR="${DATA_DIR_FLAG%/}"
  [[ $RERUN -eq 0 ]] && is_server_data_dir "$DATA_DIR" &&
    die "$DATA_DIR holds a Dockmesh server's data. Choose another --data-dir."
fi
if [[ -n "$STACKS_DIR" && ! "$STACKS_DIR" =~ ^/[A-Za-z0-9._/-]+$ ]]; then
  die "--stacks-dir must be an absolute path of letters, digits and ._-/"
fi

case "$ARCH" in
  amd64) BINARY_SHA256="$BINARY_SHA256_AMD64" ;;
  arm64) BINARY_SHA256="$BINARY_SHA256_ARM64" ;;
esac
[[ -n "$BINARY_SHA256" ]] || die "The server has no dockmesh-agent binary for linux/$ARCH."

if ! command -v docker >/dev/null 2>&1; then
  [[ $SKIP_DOCKER -eq 0 ]] || die "Docker is not installed. Install it, or run again without --skip-docker."
  STEPS=7
  step "Checking the system" "Linux $ARCH · systemd · Docker missing"
  DOCKER_LOG="$(mktemp /tmp/dockmesh-docker-install.XXXXXX)"
  if ! curl -fsSL https://get.docker.com | sh >"$DOCKER_LOG" 2>&1; then
    tail -n 20 "$DOCKER_LOG" >&2
    die "Installing Docker from get.docker.com failed (full log: $DOCKER_LOG). Install Docker yourself and run again."
  fi
  rm -f "$DOCKER_LOG"
  systemctl enable --now docker >/dev/null 2>&1 || true
  step "Installing Docker" "get.docker.com"
else
  systemctl is-active --quiet docker || systemctl enable --now docker >/dev/null 2>&1 || true
  step "Checking the system" "Linux $ARCH · systemd · Docker $(docker version --format '{{.Server.Version}}' 2>/dev/null || echo unknown)"
fi
SOCKET="/var/run/docker.sock"
[[ -S "$SOCKET" ]] || die "The Docker socket $SOCKET was not found. Start Docker and run again."

# -----------------------------------------------------------------------------
# [2] Download
# -----------------------------------------------------------------------------
TMP_BIN="$(mktemp /usr/local/bin/.dockmesh-agent.XXXXXX)"
trap 'rm -f "$TMP_BIN"' EXIT
pinned_get "$INSTALL_URL/install/dockmesh-agent-linux-$ARCH" "$TMP_BIN" ||
  die "Could not download the agent binary from $INSTALL_URL."
GOT_SHA256="$(sha256sum "$TMP_BIN" | cut -d' ' -f1)"
[[ "$GOT_SHA256" == "$BINARY_SHA256" ]] ||
  die "The agent binary does not match its checksum (expected $BINARY_SHA256, got $GOT_SHA256)."
chown root:root "$TMP_BIN"
chmod 0755 "$TMP_BIN"
AGENT_VERSION="$("$TMP_BIN" --version 2>/dev/null | awk '{print $2}' | sed 's/^v//')"
step "Downloading agent ${AGENT_VERSION:-}" "verified (sha256, pinned server)"

# -----------------------------------------------------------------------------
# [3] Binary
# -----------------------------------------------------------------------------
# From here on a failed re-run starts the agent again, with whatever
# units are in place, and puts moved files back.
OLD_DATA_DIR=""
on_exit() {
  local rc=$?
  rm -f "$TMP_BIN"
  [[ $rc -eq 0 ]] && return
  if [[ -n "$OLD_DATA_DIR" ]]; then
    as_agent "$OWNER" "$OWNER_GROUP" migrate_agent_files "$DATA_DIR" "$OLD_DATA_DIR" || true
  fi
  [[ $RERUN -eq 1 ]] && { systemctl start dockmesh-agent.service >/dev/null 2>&1 || true; }
  exit "$rc"
}
if [[ $RERUN -eq 1 ]]; then
  systemctl stop dockmesh-agent 2>/dev/null || true
  trap on_exit EXIT
fi
[[ $LEGACY -eq 1 && -e "$DATA_DIR/bin/dockmesh-agent" ]] &&
  note moved "$DATA_DIR/bin/dockmesh-agent → $PATH_BIN"
mv -fT "$TMP_BIN" "$PATH_BIN"
if selinux_enabled && command -v restorecon >/dev/null 2>&1; then
  restorecon -- "$PATH_BIN" || true
fi
step "Installing binary" "$PATH_BIN"

# -----------------------------------------------------------------------------
# [4] Account
# -----------------------------------------------------------------------------
SOCK_GID="$(stat -Lc %g "$SOCKET")"
DOCKER_GID="$(getent group docker | cut -d: -f3 || true)"
GROUPS_LINE=""
if [[ -n "$DOCKER_GID" ]]; then
  GROUPS_LINE="docker"
fi
if [[ "$SOCK_GID" != 0 && "$SOCK_GID" != "$DOCKER_GID" ]]; then
  SOCK_GROUP="$(getent group "$SOCK_GID" | cut -d: -f1 || true)"
  GROUPS_LINE="${GROUPS_LINE:+$GROUPS_LINE, }${SOCK_GROUP:-$SOCK_GID} (Docker socket)"
fi
if [[ $RUN_USER == root ]]; then
  step "Checking user" "root (kept)"
else
  label="Checking user"
  if ! getent passwd "$RUN_USER" >/dev/null; then
    label="Creating user"
    useradd --system --no-create-home --home-dir "$DATA_DIR" --shell /usr/sbin/nologin \
      --comment "Dockmesh Agent" --user-group "$RUN_USER"
    passwd -l "$RUN_USER" >/dev/null 2>&1 || true
  fi
  if [[ -n "$DOCKER_GID" ]] && ! id -nG "$RUN_USER" | tr ' ' '\n' | grep -qx docker; then
    usermod -aG docker "$RUN_USER"
  fi
  if [[ $SWITCHING -eq 1 ]]; then
    step "Switching to $RUN_USER" "groups: ${GROUPS_LINE:-none}"
  else
    step "$label" "$RUN_USER · groups: ${GROUPS_LINE:-none}"
  fi
fi
RUN_GROUP="$(id -gn "$RUN_USER")"
OWNER_GROUP="$(id -gn "$OWNER")"

# -----------------------------------------------------------------------------
# [5] Services
# -----------------------------------------------------------------------------
if [[ $RERUN -eq 1 && "${DATA_DIR%/}" != "$DEFAULT_DATA_DIR" ]] && is_server_data_dir "$DATA_DIR"; then
  [[ -L "$DEFAULT_DATA_DIR" ]] && die "$DEFAULT_DATA_DIR is a symlink. Move it away and run again."
  if [[ ! -e "$DEFAULT_DATA_DIR" ]]; then
    install -d -m 0700 -o "$OWNER" -g "$OWNER_GROUP" "$DEFAULT_DATA_DIR"
  elif [[ "$(stat -c %U "$DEFAULT_DATA_DIR")" != "$OWNER" ]]; then
    die "$DEFAULT_DATA_DIR exists and does not belong to $OWNER. Move it away and run again."
  fi
  OLD_DATA_DIR="${DATA_DIR%/}"
  DATA_DIR="$DEFAULT_DATA_DIR"
  as_agent "$OWNER" "$OWNER_GROUP" migrate_agent_files "$OLD_DATA_DIR" "$DATA_DIR"
  [[ $RUN_USER == root ]] || usermod -d "$DATA_DIR" "$RUN_USER" 2>/dev/null || true
  note moved "agent files out of the Dockmesh server's data directory $OLD_DATA_DIR → $DATA_DIR"
fi

SYS_ARGS=(--data-dir "$DATA_DIR" --enroll-url "$ENROLL_URL" --agent-url "$AGENT_URL"
  --ca-fingerprint "$CA_FINGERPRINT" --enroll-ca-file "$DATA_DIR/enroll-ca.pem")
[[ -n "$STACKS_DIR" ]] && SYS_ARGS+=(--stacks-dir "$STACKS_DIR")
[[ $RUN_USER == root ]] && SYS_ARGS+=(--user root)
[[ $SWITCHING -eq 1 ]] && SYS_ARGS+=(--switch-to-service-user)
[[ $LEGACY -eq 1 ]] && SYS_ARGS+=(--convert-legacy)
if ! SYS_OUT="$("$PATH_BIN" system install "${SYS_ARGS[@]}" 2>&1)"; then
  printf '%s\n' "$SYS_OUT" >&2
  die "The agent's services could not be installed. Fix the cause above and run again."
fi
if [[ -n "$OLD_DATA_DIR" ]]; then
  as_agent "$RUN_USER" "$RUN_GROUP" remove_legacy_bin "$OLD_DATA_DIR" || warn "could not remove $OLD_DATA_DIR/bin/dockmesh-agent"
  OLD_DATA_DIR=""
fi
printf '%s\n' "$CA_CERT_PEM" | write_agent_file "$DATA_DIR/enroll-ca.pem" "$RUN_USER" "$RUN_GROUP" ||
  die "Could not write the agents CA to $DATA_DIR/enroll-ca.pem."
if [[ -n "$TOKEN" ]]; then
  printf '%s\n' "$TOKEN" | write_agent_file "$DATA_DIR/enroll-token" "$RUN_USER" "$RUN_GROUP" ||
    die "Could not write the enrollment token to $DATA_DIR/enroll-token."
fi
while IFS= read -r line; do
  case "$line" in
    "removed the spent enrollment token from "*) note removed "spent enrollment token from ${line#removed the spent enrollment token from }" ;;
    "warning: "*) warn "${line#warning: }" ;;
    "handed "*" files over to "*) n="${line#handed }"; note changed "owner of ${n%% *} files to $RUN_USER" ;;
  esac
done <<<"$SYS_OUT"
SERVICES="dockmesh-agent.service, dockmesh-agent-update.path"
[[ -n "$STACKS_DIR" ]] && SERVICES="$SERVICES · stacks: $STACKS_DIR"
step "Installing services" "$SERVICES"

# -----------------------------------------------------------------------------
# [6] Start
# -----------------------------------------------------------------------------
START_TS="$(date +"%Y-%m-%d %H:%M:%S")"
STATE=waiting
if ! systemctl restart dockmesh-agent.service; then
  STATE=start_failed
fi
AGENT_WAIT_SECS="${AGENT_WAIT_SECS:-60}"
AGENT_LOGS=""
if [[ "$STATE" == waiting ]]; then
  for ((i = 0; i < AGENT_WAIT_SECS; i++)); do
    if [[ "$(systemctl show -p ExecMainStatus --value dockmesh-agent.service 2>/dev/null || true)" == 203 ]]; then
      STATE=start_failed
      break
    fi
    AGENT_LOGS="$(journalctl -u dockmesh-agent --since "$START_TS" --no-pager -o cat 2>/dev/null || true)"
    STATE="$(agent_log_state "$AGENT_LOGS")"
    [[ "$STATE" != waiting ]] && break
    sleep 1
  done
fi

if [[ "$STATE" != connected ]]; then
  if [[ $MODE == fresh && ( "$STATE" == rejected || "$STATE" == failed ) ]]; then
    # A failed first enrollment would retry every 10 s and count against
    # the server's per-address limit.
    systemctl disable --now dockmesh-agent.service >/dev/null 2>&1 || true
  fi
  case "$STATE" in
    rejected)     printf '\n%sThis install command has expired or was already used. Copy a new one: Hosts › Add host › New command.%s\n' "$C_RED" "$C_OFF" ;;
    failed)       printf '\n%sThe agent could not enroll. The agent service is stopped; run the install command again once the cause below is fixed.%s\n' "$C_RED" "$C_OFF" ;;
    start_failed) printf '\n%ssystemd could not start the agent (%s).%s\n' "$C_RED" "$PATH_BIN" "$C_OFF"; start_failure_hint "$PATH_BIN" ;;
    *)
      printf '\n%sThe agent did not connect within %s s.%s\n' "$C_RED" "$AGENT_WAIT_SECS" "$C_OFF"
      systemctl is-active --quiet dockmesh-agent.service || start_failure_hint "$PATH_BIN"
      ;;
  esac
  printf 'Last agent log lines:\n\n'
  journalctl -u dockmesh-agent --no-pager -n 20 -o cat || true
  exit 1
fi

NAME="$(log_field "$AGENT_LOGS" agent_name connected)"
NAME="${NAME:-this host}"
REENROLL_FAILED=0
case "$MODE" in
  fresh)
    step "Enrolling" "connected as $NAME"
    ;;
  reenroll)
    if grep -q '"msg":"re-enrolled with a new client certificate"' <<<"$AGENT_LOGS"; then
      step "Enrolling" "new certificate for $NAME (host ID kept)"
    else
      REENROLL_FAILED=1
      step "Enrolling" "connected as $NAME (previous enrollment)"
    fi
    ;;
  rerun)
    step "Starting" "connected as $NAME (enrollment kept)"
    ;;
esac

if [[ $MODE == fresh ]]; then
  box "Agent installed" \
    "Host        $NAME" \
    "Data        $DATA_DIR" \
    "Stacks      ${STACKS_DIR:-$DATA_DIR/stacks}" \
    "" \
    "Status      systemctl status dockmesh-agent" \
    "Logs        journalctl -u dockmesh-agent -f"
  exit 0
fi

if [[ $RUN_USER == root ]]; then
  printf '\n%snotice%s  This agent runs as root. It keeps running as root.\n' "$C_YELLOW" "$C_OFF"
  printf '        To switch it to the dockmesh-agent user, run again with --switch-to-service-user.\n'
fi
if [[ $REENROLL_FAILED -eq 1 ]]; then
  reason="$(log_field "$AGENT_LOGS" err "re-enrollment failed")"
  printf '\n%sThe new token was not accepted (%s). The agent keeps its previous enrollment.%s\n' "$C_RED" "${reason:-no answer from the server}" "$C_OFF"
  exit 1
fi
if [[ $SWITCHING -eq 1 ]]; then
  printf '\n%sok%s  The agent on %s runs as %s now.\n' "$C_GREEN" "$C_OFF" "$NAME" "$RUN_USER"
else
  printf '\n%sok%s  The agent on %s uses the managed layout and signed updates now.\n' "$C_GREEN" "$C_OFF" "$NAME"
fi
