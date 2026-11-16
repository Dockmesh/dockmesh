#!/usr/bin/env bash
# Install, use and uninstall Dockmesh on the host it runs on, as root:
#   sudo DOCKMESH_SMOKE_DESTRUCTIVE=1 bash scripts/arm64-smoke.sh <dir with dockmesh, dockmesh-agent, dmctl>
# Used by the arm64 smoke workflow on a throw-away runner. It installs into
# the default places and purges them again, so it only runs on a GitHub
# runner or with DOCKMESH_SMOKE_DESTRUCTIVE=1.
set -euo pipefail

DIST="$(cd "${1:?usage: arm64-smoke.sh <dist dir>}" && pwd)"
HERE="$(cd "$(dirname "$0")" && pwd)"
ARCH="$(uname -m)"
case "$ARCH" in
  aarch64|arm64) GOARCH=arm64 ;;
  x86_64) GOARCH=amd64 ;;
  *) echo "unsupported arch $ARCH" >&2; exit 1 ;;
esac
[ "$(id -u)" = 0 ] || { echo "run as root" >&2; exit 1; }
if [ "${GITHUB_ACTIONS:-}" != true ] && [ "${DOCKMESH_SMOKE_DESTRUCTIVE:-}" != 1 ]; then
  echo "purges the Dockmesh install of this host; set DOCKMESH_SMOKE_DESTRUCTIVE=1 to run it outside CI" >&2
  exit 1
fi
if [ -e /usr/local/bin/dockmesh ] || [ -e /var/lib/dockmesh ] || [ -e /etc/dockmesh ] || systemctl cat dockmesh.service >/dev/null 2>&1; then
  echo "this host already has a Dockmesh install; refusing" >&2
  exit 1
fi

BASE=https://127.0.0.1:8080
# The setup file and its password file must sit below root-only directories.
WORK="$(mktemp -d -p /root)"
trap 'rm -rf "$WORK"' EXIT
CURL=(curl -sS -k --fail-with-body --max-time 120)
ALPINE=alpine:3.20@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc
step() { printf '\n== %s\n' "$*"; }
fail() { echo "FAIL: $*" >&2; exit 1; }

# The token and request bodies go through a file and stdin, never curl's
# command line, where every local user could read them.
api() {
  local method=$1 path=$2 body=${3:-}
  if [ -n "$body" ]; then
    printf '%s' "$body" | "${CURL[@]}" -X "$method" -H @"$WORK/auth-header" -H 'Content-Type: application/json' \
      --data-binary @- "$BASE$path"
  else
    "${CURL[@]}" -X "$method" -H @"$WORK/auth-header" "$BASE$path"
  fi
}

step "stage binaries"
install -m 0755 "$DIST/dockmesh" /usr/local/bin/dockmesh
install -m 0755 "$DIST/dmctl" /usr/local/bin/dmctl
install -d -m 0755 /usr/local/share/dockmesh/bin
install -m 0755 "$DIST/dockmesh-agent" "/usr/local/share/dockmesh/bin/dockmesh-agent-linux-$GOARCH"
install -m 0755 "$HERE/install-agent.sh" /usr/local/share/dockmesh/install-agent.sh

step "install with the installer and a setup file"
PW="smoke-$(head -c 18 /dev/urandom | base64 | tr -dc 'A-Za-z0-9')"
umask 077
printf '%s' "$PW" > "$WORK/admin-password"
cat > "$WORK/setup.yaml" <<EOF
data_dir: /var/lib/dockmesh
admin:
  username: admin
  password_file: $WORK/admin-password
address:
  base_url: $BASE
  http_addr: ":8080"
tls:
  mode: internal
EOF
umask 022
DOCKMESH_SKIP_DOWNLOAD=1 DOCKMESH_NO_SCANNER=1 DOCKMESH_SETUP_FILE="$WORK/setup.yaml" NO_COLOR=1 \
  bash "$HERE/install.sh"
systemctl is-active --quiet dockmesh || fail "dockmesh service is not active"

for _ in $(seq 1 60); do
  "${CURL[@]}" "$BASE/healthz/ready" >/dev/null 2>&1 && break
  sleep 2
done
"${CURL[@]}" "$BASE/healthz/ready" >/dev/null || fail "server not ready"

TOKEN="$(jq -n --arg p "$PW" '{username:"admin",password:$p}' | "${CURL[@]}" -H 'Content-Type: application/json' \
  --data-binary @- "$BASE/api/v1/auth/login" | jq -r .access_token)"
[ -n "$TOKEN" ] && [ "$TOKEN" != null ] || fail "admin sign-in failed"
(umask 077; printf 'Authorization: Bearer %s\n' "$TOKEN" > "$WORK/auth-header")

step "deploy a stack"
COMPOSE='services:
  app:
    image: alpine:3.20@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc
    command: ["sh", "-c", "[ -f /data/marker ] || echo smoke-marker > /data/marker; exec sleep 3600"]
    volumes:
      - data:/data
volumes:
  data: {}
'
api POST /api/v1/stacks "$(jq -n --arg c "$COMPOSE" '{name:"smoke",compose:$c,host_id:"local"}')" >/dev/null
api POST '/api/v1/stacks/smoke/deploy?host=local' >/dev/null
for _ in $(seq 1 60); do
  [ "$(docker run --rm -v smoke_data:/d "$ALPINE" cat /d/marker 2>/dev/null)" = smoke-marker ] && break
  sleep 2
done
[ "$(docker run --rm -v smoke_data:/d "$ALPINE" cat /d/marker)" = smoke-marker ] || fail "stack volume has no marker"

step "back up the volume"
TID="$(api POST /api/v1/backups/targets '{"name":"smoke-local","type":"local","config":{"path":""}}' | jq -r .id)"
JID="$(api POST /api/v1/backups/jobs "$(jq -n --argjson t "$TID" '{name:"smoke-vol",host_id:"local",target_id:$t,
  sources:[{type:"volume",name:"smoke_data"}],schedule:"0 3 * * *",retention_count:0,retention_days:0,encrypt:true,enabled:true}')" | jq -r .id)"
RID="$(api POST "/api/v1/backups/jobs/$JID/run" '{}' | jq -r .id)"
STATUS=""
for _ in $(seq 1 90); do
  STATUS="$(api GET "/api/v1/backups/runs/$RID" | jq -r .status)"
  case "$STATUS" in running|null|"") sleep 2 ;; *) break ;; esac
done
[ "$STATUS" = success ] || fail "backup run ended with status $STATUS"

step "restore into a new volume"
api POST "/api/v1/backups/runs/$RID/restore" '{"dest_volume":"smoke_restored","host_id":"local"}' >/dev/null
GOT=""
for _ in $(seq 1 60); do
  GOT="$(docker run --rm -v smoke_restored:/d "$ALPINE" sh -c 'find /d -name marker -exec cat {} \;' 2>/dev/null || true)"
  [ "$GOT" = smoke-marker ] && break
  sleep 2
done
[ "$GOT" = smoke-marker ] || fail "restored volume has no marker (got '$GOT')"

step "uninstall"
api DELETE '/api/v1/stacks/smoke?host=local' >/dev/null || true
bash "$HERE/uninstall.sh" --purge
for p in /usr/local/bin/dockmesh /var/lib/dockmesh /etc/systemd/system/dockmesh.service; do
  [ ! -e "$p" ] || fail "$p still exists after uninstall --purge"
done
if systemctl list-unit-files dockmesh.service 2>/dev/null | grep -q dockmesh; then
  fail "dockmesh.service still known to systemd"
fi

echo
echo "arm64 smoke: ok ($ARCH, $("$DIST/dockmesh" version | head -1))"
if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
  echo "arm64 smoke passed on $ARCH: install, deploy, backup, restore, uninstall" >> "$GITHUB_STEP_SUMMARY"
fi
