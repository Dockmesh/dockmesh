#!/usr/bin/env bash
# Runs the Playwright suite against a throwaway Dockmesh: fresh data dir,
# random admin password, server on a free local port. Needs a built
# ./dockmesh binary (make build) and `npx playwright install chromium`.
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
bin="${DOCKMESH_BIN:-$root/dockmesh}"
port="${DOCKMESH_E2E_PORT:-18080}"
work="$(mktemp -d)"
trap 'kill "${pid:-0}" 2>/dev/null && wait "$pid" 2>/dev/null; rm -rf "$work"' EXIT

export DOCKMESH_DB_PATH="$work/data/dockmesh.db"
export DOCKMESH_STACKS_ROOT="$work/stacks"
export DOCKMESH_SECRETS_PATH="$work/data/secrets.env"
export DOCKMESH_SECRETS_KEY_PATH="$work/data/secrets.age-key"
export DOCKMESH_AUDIT_GENESIS_PATH="$work/data/audit-genesis.sha256"
export DOCKMESH_HTTP_ADDR="127.0.0.1:$port"
export DOCKMESH_AGENT_LISTEN="127.0.0.1:0"
export DOCKMESH_BASE_URL="http://127.0.0.1:$port"
export DOCKMESH_SCANNER_ENABLED=false
mkdir -p "$work/data" "$work/stacks"

random_secret() { head -c 18 /dev/urandom | base64 | tr -dc 'A-Za-z0-9'; }
export DOCKMESH_E2E_USER=admin
DOCKMESH_E2E_PASSWORD=$(random_secret)
export DOCKMESH_E2E_PASSWORD
printf '%s\n' "$DOCKMESH_E2E_PASSWORD" | "$bin" admin create --username admin --role admin >/dev/null

"$bin" serve >"$work/server.log" 2>&1 &
pid=$!
for _ in $(seq 1 60); do
  curl -fs "http://127.0.0.1:$port/api/v1/health" >/dev/null && break
  kill -0 "$pid" 2>/dev/null || { cat "$work/server.log"; exit 1; }
  sleep 1
done

export DOCKMESH_URL="http://127.0.0.1:$port"
cd "$root/web"
status=0
npx playwright test -c e2e.config.ts "$@" || status=$?
[ "$status" -eq 0 ] || tail -n 50 "$work/server.log"
exit "$status"
