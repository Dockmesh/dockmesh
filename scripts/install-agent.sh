#!/usr/bin/env bash
#
# Dockmesh agent installer — concept §3.1.
#
# This script is templated by the dockmesh server at request time. The
# {{ }} tokens are replaced with the real token, URLs and binary location
# before being served, so the user only ever sees a single one-line curl
# command in the UI.
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
BINARY_URL="{{BINARY_URL}}"

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

# -----------------------------------------------------------------------------
# Pre-flight
# -----------------------------------------------------------------------------
require_root
require_systemd

if [[ -z "$TOKEN" ]]; then
  die "no enrollment token — did you fetch this script with ?token=… ?"
fi

ARCH="$(detect_arch)"
log "host: $(uname -s)/$ARCH  systemd: yes"
log "server: $SERVER_URL"

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
DATA_DIR=/var/lib/dockmesh
ENV_DIR=/etc/dockmesh-agent

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

# -----------------------------------------------------------------------------
# Filesystem layout
# -----------------------------------------------------------------------------
log "creating directories"
install -d -m 0700 -o "$RUN_USER" -g "$RUN_GROUP" "$DATA_DIR"
install -d -m 0750 -o root -g "$RUN_GROUP" "$ENV_DIR"

# -----------------------------------------------------------------------------
# Binary download
# -----------------------------------------------------------------------------
TMP_BIN="$(mktemp)"
log "downloading agent binary from $BINARY_URL"
http_get "$BINARY_URL" "$TMP_BIN"
chmod 0755 "$TMP_BIN"
install -m 0755 -o root -g root "$TMP_BIN" /usr/local/bin/dockmesh-agent
rm -f "$TMP_BIN"
log "installed: $(/usr/local/bin/dockmesh-agent --version 2>&1 | head -1 || echo dockmesh-agent)"

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
Documentation=https://github.com/BlinkMSP/dockmesh
After=network-online.target docker.service
Wants=network-online.target
Requires=docker.service

[Service]
Type=simple
$USER_LINES
EnvironmentFile=$ENV_FILE
ExecStart=/usr/local/bin/dockmesh-agent
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
# Self-upgrade (UpgradeAgent frame) writes a new binary to
# /usr/local/bin/dockmesh-agent.new and atomic-renames it over the
# running binary. Under ProtectSystem=strict, /usr is read-only by
# default — whitelist the install dir so the upgrade succeeds
# without requiring an out-of-band sudo edit of this unit.
ReadWritePaths=$DATA_DIR /usr/local/bin

[Install]
WantedBy=multi-user.target
EOF

# -----------------------------------------------------------------------------
# Start
# -----------------------------------------------------------------------------
log "reloading systemd"
systemctl daemon-reload
systemctl enable --now dockmesh-agent.service

log "waiting for agent to connect"
for i in 1 2 3 4 5 6 7 8 9 10; do
  if systemctl is-active --quiet dockmesh-agent; then
    break
  fi
  sleep 1
done

if systemctl is-active --quiet dockmesh-agent; then
  printf '\n\033[1;32m✓\033[0m dockmesh-agent installed and running\n'
  printf '  user:    %s\n' "$RUN_USER"
  printf '  data:    %s\n' "$DATA_DIR"
  printf '  status:  systemctl status dockmesh-agent\n'
  printf '  logs:    journalctl -u dockmesh-agent -f\n'
else
  printf '\n\033[1;31m!!\033[0m dockmesh-agent failed to start. Recent logs:\n\n'
  journalctl -u dockmesh-agent --no-pager -n 30 || true
  exit 1
fi
