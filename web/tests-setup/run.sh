#!/usr/bin/env bash
# Runs the setup wizard flows against a never set up Dockmesh: the built
# ./dockmesh in a throwaway container, as root, because the setup code
# lives in /etc/dockmesh. Needs Docker and `npx playwright install chromium`.
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
bin="${DOCKMESH_BIN:-$root/dockmesh}"
port="${DOCKMESH_SETUP_PORT:-18443}"
name="dockmesh-setup-e2e-$$"
image="${DOCKMESH_SETUP_IMAGE:-debian:12-slim}"
trap 'docker rm -f "$name" >/dev/null 2>&1 || true' EXIT

docker run -d --name "$name" --restart unless-stopped \
  -e DOCKMESH_TLS_MODE=internal \
  -p "127.0.0.1:$port:8080" \
  -v "$bin:/usr/local/bin/dockmesh:ro" \
  "$image" dockmesh serve >/dev/null

for _ in $(seq 1 60); do
  curl -fsk "https://127.0.0.1:$port/api/v1/health" >/dev/null && break
  sleep 1
done

new_code() { docker exec "$name" dockmesh setup new-code --porcelain | sed -n 's/^code=//p'; }
DOCKMESH_SETUP_CODE="$(new_code)"
export DOCKMESH_SETUP_CODE
export DOCKMESH_SETUP_URL="https://127.0.0.1:$port"
export DOCKMESH_SETUP_CONTAINER="$name"

cd "$root/web"
status=0
npx playwright test -c e2e-setup.config.ts "$@" || status=$?
[ "$status" -eq 0 ] || docker logs --tail 30 "$name" 2>&1 | grep -v "/_app/"
exit "$status"
