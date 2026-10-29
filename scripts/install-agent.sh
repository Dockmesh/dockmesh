#!/usr/bin/env bash
#
# Dockmesh agent installer — concept §3.1.
#
# This script is templated by the dockmesh server at request time. The
# {{ }} tokens are replaced with the real token, URLs, the agents CA and
# the agent binary checksums before being served, so the user only ever
# sees a single one-line curl command in the UI.
#
# Default mode creates a dedicated `dockmesh-agent` system user, locks it,
# adds it to the `docker` group, and runs the agent under systemd with
# common hardening directives applied.
#
# Pass --as-root to skip user creation and run the agent as root instead.
# Useful for homelabs where the audit story doesn't matter.

set -euo pipefail

# -----------------------------------------------------------------------------
# Templated by the server
# -----------------------------------------------------------------------------
TOKEN="{{TOKEN}}"
SERVER_URL="{{SERVER_URL}}"
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
SKIP_DOCKER=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --as-root)     AS_ROOT=1; shift ;;
    --skip-docker) SKIP_DOCKER=1; shift ;;
    -h|--help)
      cat <<USAGE
Dockmesh agent installer.

Usage:
  curl -fsSL <server>/install/agent.sh?token=XYZ | sudo bash
  curl -fsSL <server>/install/agent.sh?token=XYZ | sudo bash -s -- [flags]

Flags:
  --as-root        Run the agent as root instead of a dedicated user.
                   Skips user creation. Less secure but simplest.
  --skip-docker    Don't auto-install Docker even if missing.
  -h, --help       Show this help.
USAGE
      exit 0
      ;;
    *) echo "unknown flag: $1" >&2; exit 1 ;;
  esac
done

# -----------------------------------------------------------------------------
# Helpers
# -----------------------------------------------------------------------------
log() { printf '\033[1;36m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m!!\033[0m %s\n' "$*" >&2; }
die() { printf '\033[1;31m!!\033[0m %s\n' "$*" >&2; exit 1; }

require_root() {
  if [[ $EUID -ne 0 ]]; then
    die "This installer must run as root. Re-run with sudo:
  curl -fsSL ${SERVER_URL}/install/agent.sh?token=… | sudo bash"
  fi
}

require_systemd() {
  if ! command -v systemctl >/dev/null 2>&1; then
    die "systemd is required (systemctl not found)."
  fi
}

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64)        echo amd64 ;;
    aarch64|arm64)       echo arm64 ;;
    *) die "unsupported architecture: $(uname -m)" ;;
  esac
}

http_get() {
  # $1 = url, $2 = output file
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL --retry 3 --retry-delay 2 -o "$2" "$1"
  elif command -v wget >/dev/null 2>&1; then
    wget -q -O "$2" "$1"
  else
    die "neither curl nor wget found"
  fi
}

# agent_log_state classifies the agent's JSON log lines ($1) as connected,
# failed (enrollment rejected) or waiting.
agent_log_state() {
  local logs="$1"
  if grep -q '"msg":"connected"' <<<"$logs"; then
    echo connected
  elif grep -q '"msg":"enrollment failed"' <<<"$logs"; then
    echo failed
  else
    echo waiting
  fi
}
# end agent_log_state

# is_server_data_dir succeeds if $1 is, or looks like, the Dockmesh server's
# data directory. Agents installed by older releases defaulted to it.
is_server_data_dir() {
  local d="${1%/}"
  case "$d" in
    /var/lib/dockmesh|/data) return 0 ;;
  esac
  [[ -e "$d/dockmesh.db" || -e "$d/dockmesh.env" ]]
}
# end is_server_data_dir

# migrate_agent_files moves the agent's own files from $1 to $2 and leaves
# everything else in $1 alone.
migrate_agent_files() {
  local from="${1%/}" to="${2%/}" f
  for f in agent.crt agent.key agent.url ca.crt enroll-ca.pem certs staging; do
    if [[ -e "$from/$f" || -L "$from/$f" ]]; then
      rm -rf "${to:?}/$f"
      mv "$from/$f" "$to/$f"
    fi
  done
  if [[ -e "$from/bin/dockmesh-agent" || -L "$from/bin/dockmesh-agent" ]]; then
    mkdir -p "$to/bin"
    mv -f "$from/bin/dockmesh-agent" "$to/bin/dockmesh-agent"
    rmdir "$from/bin" 2>/dev/null || true
  fi
}
# end migrate_agent_files

# ensure_dir_owner refuses to take over an existing directory $1 unless it
# belongs to the service user $2, or to root and is empty or already the
# agent's (an earlier --as-root install).
ensure_dir_owner() {
  local owner
  [[ -d "$1" ]] || return 0
  owner="$(stat -c %U "$1")"
  [[ "$owner" == "$2" ]] && return 0
  if [[ "$owner" == root ]]; then
    if [[ -e "$1/agent.crt" || -e "$1/bin/dockmesh-agent" || -z "$(ls -A "$1")" ]]; then
      return 0
    fi
  fi
  die "$1 already exists and belongs to $owner. Move it away or set a different DOCKMESH_DATA_DIR in /etc/dockmesh-agent/agent.env, then re-run."
}
# end ensure_dir_owner

# -----------------------------------------------------------------------------
# Pre-flight
# -----------------------------------------------------------------------------
# Agent enrollment is Linux-only today — systemd unit + useradd + getent
# all assume GNU userland. macOS agents run via `dockmesh-agent` built
# from source; the one-line enroll installer doesn't target them yet.
case "$(uname -s)" in
  Linux) ;;
  Darwin)
    die "This enroll installer is Linux-only today. To enroll a macOS host
as an agent, build dockmesh-agent from source and start it manually
with the enrollment token — see https://dockmesh.dev/docs/features/multi-host/"
    ;;
  *) die "unsupported OS: $(uname -s)" ;;
esac

require_root
require_systemd

if [[ -z "$TOKEN" ]]; then
  die "no enrollment token — did you fetch this script with ?token=… ?"
fi

ARCH="$(detect_arch)"
log "host: $(uname -s)/$ARCH  systemd: yes"
log "server: $SERVER_URL"

BINARY_URL="$SERVER_URL/install/dockmesh-agent-linux-$ARCH"
case "$ARCH" in
  amd64) BINARY_SHA256="$BINARY_SHA256_AMD64" ;;
  arm64) BINARY_SHA256="$BINARY_SHA256_ARM64" ;;
esac
if [[ -z "$BINARY_SHA256" ]]; then
  die "the server has no dockmesh-agent binary for linux/$ARCH bundled."
fi
if ! command -v sha256sum >/dev/null 2>&1; then
  die "sha256sum is required to verify the agent binary (install coreutils)."
fi

# -----------------------------------------------------------------------------
# Install missing tools
# -----------------------------------------------------------------------------
if ! command -v curl >/dev/null 2>&1; then
  log "installing curl"
  if command -v apt-get >/dev/null 2>&1; then
    apt-get update -qq && apt-get install -y -qq curl ca-certificates
  elif command -v dnf >/dev/null 2>&1; then
    dnf install -y -q curl ca-certificates
  elif command -v yum >/dev/null 2>&1; then
    yum install -y -q curl ca-certificates
  else
    die "couldn't install curl — please install it manually and re-run."
  fi
fi

# -----------------------------------------------------------------------------
# Docker
# -----------------------------------------------------------------------------
if ! command -v docker >/dev/null 2>&1; then
  if [[ $SKIP_DOCKER -eq 1 ]]; then
    die "Docker not found and --skip-docker set."
  fi
  log "installing Docker via get.docker.com"
  curl -fsSL https://get.docker.com | sh
  systemctl enable --now docker
else
  log "Docker present: $(docker --version 2>/dev/null || echo unknown)"
fi
if ! systemctl is-active --quiet docker; then
  systemctl enable --now docker
fi

# -----------------------------------------------------------------------------
# Service account
# -----------------------------------------------------------------------------
RUN_USER=root
RUN_GROUP=root
ENV_DIR=/etc/dockmesh-agent
# The server defaults to /var/lib/dockmesh; an agent on the same host must not
# share it. A re-run keeps the directory an earlier install already used,
# unless that is the server's, in which case the agent's files move out.
DATA_DIR=/var/lib/dockmesh-agent
OLD_DATA_DIR=""
if [[ -r "$ENV_DIR/agent.env" ]]; then
  PREV_DATA_DIR="$(sed -n 's/^DOCKMESH_DATA_DIR=//p' "$ENV_DIR/agent.env" | head -1)"
  if [[ -n "$PREV_DATA_DIR" ]]; then
    if is_server_data_dir "$PREV_DATA_DIR"; then
      OLD_DATA_DIR="$PREV_DATA_DIR"
    else
      DATA_DIR="$PREV_DATA_DIR"
    fi
  fi
fi

if [[ $AS_ROOT -eq 0 ]]; then
  RUN_USER=dockmesh-agent
  RUN_GROUP=dockmesh-agent
  if ! getent passwd "$RUN_USER" >/dev/null; then
    log "creating service account: $RUN_USER"
    useradd \
      --system \
      --no-create-home \
      --home-dir "$DATA_DIR" \
      --shell /usr/sbin/nologin \
      --comment "Dockmesh Agent" \
      "$RUN_USER"
    passwd -l "$RUN_USER" >/dev/null 2>&1 || true
  else
    log "service account exists: $RUN_USER"
  fi
  if ! id -nG "$RUN_USER" | tr ' ' '\n' | grep -qx docker; then
    log "adding $RUN_USER to docker group"
    usermod -aG docker "$RUN_USER"
  fi
else
  log "running as root (--as-root)"
fi

ensure_dir_owner "$DATA_DIR" "$RUN_USER"

if [[ -n "$OLD_DATA_DIR" && -d "$OLD_DATA_DIR" ]]; then
  systemctl stop dockmesh-agent 2>/dev/null || true
  log "moving agent files out of the server's data directory $OLD_DATA_DIR to $DATA_DIR"
  install -d -m 0700 -o "$RUN_USER" -g "$RUN_GROUP" "$DATA_DIR"
  migrate_agent_files "$OLD_DATA_DIR" "$DATA_DIR"
  if [[ $AS_ROOT -eq 0 ]]; then
    usermod -d "$DATA_DIR" "$RUN_USER" 2>/dev/null || true
  fi
fi

# -----------------------------------------------------------------------------
# Stale-enrollment guard
# -----------------------------------------------------------------------------
# If the host was previously enrolled, its mTLS certs are in $DATA_DIR.
# Running this installer with a *new* enrollment token implies the
# operator wants to re-enroll. The old certs would otherwise survive
# and the agent would reconnect as the previous (now-deleted) identity,
# producing "dial: websocket: bad handshake" forever. Wipe them so the
# agent uses the new token.
if [[ -f "$DATA_DIR/agent.crt" ]] || [[ -f "$DATA_DIR/agent.key" ]] || [[ -d "$DATA_DIR/certs" ]]; then
  systemctl stop dockmesh-agent 2>/dev/null || true
  log "detected existing enrollment in $DATA_DIR — removing certs so the new token takes effect"
  rm -f "$DATA_DIR/agent.crt" "$DATA_DIR/agent.key" "$DATA_DIR/agent.url" "$DATA_DIR/ca.crt" "$DATA_DIR/enroll-ca.pem"
  rm -rf "$DATA_DIR/certs" "$DATA_DIR/staging"
fi

# -----------------------------------------------------------------------------
# Filesystem layout
# -----------------------------------------------------------------------------
log "creating directories"
install -d -m 0700 -o "$RUN_USER" -g "$RUN_GROUP" "$DATA_DIR"
install -d -m 0750 -o root -g "$RUN_GROUP" "$ENV_DIR"

# -----------------------------------------------------------------------------
# Binary download
# -----------------------------------------------------------------------------
# Install the binary into $DATA_DIR/bin/ owned by the service user so
# self-upgrade (UpgradeAgent frame) can atomic-rename a new binary in
# place without needing root. /usr/local/bin is root-owned everywhere
# in the Linux world and non-root users can't create siblings there.
BIN_DIR="$DATA_DIR/bin"
AGENT_BIN="$BIN_DIR/dockmesh-agent"
install -d -m 0750 -o "$RUN_USER" -g "$RUN_GROUP" "$BIN_DIR"

TMP_BIN="$(mktemp)"
log "downloading agent binary from $BINARY_URL"
http_get "$BINARY_URL" "$TMP_BIN"
GOT_SHA256="$(sha256sum "$TMP_BIN" | cut -d' ' -f1)"
if [[ "$GOT_SHA256" != "$BINARY_SHA256" ]]; then
  rm -f "$TMP_BIN"
  die "agent binary checksum mismatch (expected $BINARY_SHA256, got $GOT_SHA256)"
fi
chmod 0755 "$TMP_BIN"
install -m 0755 -o "$RUN_USER" -g "$RUN_GROUP" "$TMP_BIN" "$AGENT_BIN"
rm -f "$TMP_BIN"

# Keep a symlink at /usr/local/bin/dockmesh-agent for convenience —
# `dockmesh-agent --version` on a user shell still works, but the
# systemd unit runs the real path in $DATA_DIR/bin.
ln -sf "$AGENT_BIN" /usr/local/bin/dockmesh-agent

log "installed: $($AGENT_BIN --version 2>&1 | head -1 || echo dockmesh-agent)"

# -----------------------------------------------------------------------------
# Agents CA (pinned by fingerprint for enrollment)
# -----------------------------------------------------------------------------
ENROLL_CA_FILE="$DATA_DIR/enroll-ca.pem"
printf '%s\n' "$CA_CERT_PEM" > "$ENROLL_CA_FILE"
chown "$RUN_USER":"$RUN_GROUP" "$ENROLL_CA_FILE"
chmod 0644 "$ENROLL_CA_FILE"

# -----------------------------------------------------------------------------
# Environment file (holds the secret token + URLs)
# -----------------------------------------------------------------------------
ENV_FILE="$ENV_DIR/agent.env"
log "writing $ENV_FILE"
cat > "$ENV_FILE" <<EOF
DOCKMESH_DATA_DIR=$DATA_DIR
DOCKMESH_ENROLL_URL=$ENROLL_URL
DOCKMESH_AGENT_URL=$AGENT_URL
DOCKMESH_TOKEN=$TOKEN
DOCKMESH_CA_FINGERPRINT=$CA_FINGERPRINT
DOCKMESH_ENROLL_CA_FILE=$ENROLL_CA_FILE
EOF
chown root:"$RUN_GROUP" "$ENV_FILE"
chmod 0640 "$ENV_FILE"

# -----------------------------------------------------------------------------
# systemd unit
# -----------------------------------------------------------------------------
UNIT_FILE=/etc/systemd/system/dockmesh-agent.service
log "writing $UNIT_FILE"

if [[ $AS_ROOT -eq 1 ]]; then
  USER_LINES=""
else
  USER_LINES="User=$RUN_USER
Group=$RUN_GROUP"
fi

cat > "$UNIT_FILE" <<EOF
[Unit]
Description=Dockmesh Agent
Documentation=https://github.com/Dockmesh/dockmesh
After=network-online.target docker.service
Wants=network-online.target
Requires=docker.service

[Service]
Type=simple
$USER_LINES
EnvironmentFile=$ENV_FILE
ExecStart=$AGENT_BIN
Restart=always
RestartSec=10s
StartLimitInterval=0

# Hardening
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
RestrictNamespaces=yes
LockPersonality=yes
ReadWritePaths=$DATA_DIR

[Install]
WantedBy=multi-user.target
EOF

# -----------------------------------------------------------------------------
# Start
# -----------------------------------------------------------------------------
log "reloading systemd"
systemctl daemon-reload
START_TS="$(date +"%Y-%m-%d %H:%M:%S")"
systemctl enable --now dockmesh-agent.service

AGENT_WAIT_SECS="${AGENT_WAIT_SECS:-60}"
log "waiting for agent to connect (up to ${AGENT_WAIT_SECS}s)"
STATE=waiting
AGENT_LOGS=""
for ((i = 0; i < AGENT_WAIT_SECS; i++)); do
  AGENT_LOGS="$(journalctl -u dockmesh-agent --since "$START_TS" --no-pager -o cat 2>/dev/null || true)"
  STATE="$(agent_log_state "$AGENT_LOGS")"
  [[ "$STATE" != waiting ]] && break
  sleep 1
done

if [[ "$STATE" == connected ]]; then
  printf '\n\033[1;32m✓\033[0m dockmesh-agent installed and connected\n'
  printf '  user:    %s\n' "$RUN_USER"
  printf '  data:    %s\n' "$DATA_DIR"
  printf '  status:  systemctl status dockmesh-agent\n'
  printf '  logs:    journalctl -u dockmesh-agent -f\n'
else
  if [[ "$STATE" == failed ]]; then
    printf '\n\033[1;31m!!\033[0m the agent could not enroll. If the token expired or was already used, copy a fresh command from the Hosts page.\n'
  else
    printf '\n\033[1;31m!!\033[0m the agent did not connect to the server within %ss.\n' "$AGENT_WAIT_SECS"
  fi
  printf 'Last agent log lines:\n\n'
  journalctl -u dockmesh-agent --no-pager -n 20 -o cat || true
  exit 1
fi
