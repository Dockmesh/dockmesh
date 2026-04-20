#!/usr/bin/env bash
# Ad-hoc load test: deploy N stacks with 3 alpine-sleep services each.
# Measures end-to-end latencies + dockmesh RSS. Runs locally but hits
# the 164 API over the network.
set -eu

BASE="http://192.168.10.164:8080/api/v1"
ADMIN_PW="${ADMIN_PW:-DrTest2026!}"
N="${N:-100}"
PAR="${PAR:-10}"
PREFIX="${PREFIX:-lt}"

TOKEN=$(curl -sk -X POST "$BASE/auth/login" -H 'Content-Type: application/json' \
  -d "{\"username\":\"admin\",\"password\":\"$ADMIN_PW\"}" \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["access_token"])')

# Single-line compose so JSON stays simple via a Python one-liner.
COMPOSE='services:
  a:
    image: alpine:3
    command: sleep 3600
  b:
    image: alpine:3
    command: sleep 3600
  c:
    image: alpine:3
    command: sleep 3600
'

export BASE TOKEN PREFIX COMPOSE

deploy_one() {
  local i=$1
  local name
  name="${PREFIX}-$(printf '%03d' "$i")"
  local body
  body=$(python3 -c "
import json,os
print(json.dumps({'name':'$name','compose':os.environ['COMPOSE'],'env':'','host_id':'local'}))
")
  curl -sk -X POST "$BASE/stacks" -H "Authorization: Bearer $TOKEN" \
    -H 'Content-Type: application/json' -d "$body" -o /dev/null -w ""
  curl -sk -X POST "$BASE/stacks/$name/deploy" -H "Authorization: Bearer $TOKEN" \
    -o /dev/null -w ""
}

probe() {
  local label="$1"
  local stacks_ms cont_ms dash_ms rss
  stacks_ms=$(curl -sk -o /dev/null -w '%{time_total}' -H "Authorization: Bearer $TOKEN" "$BASE/stacks")
  cont_ms=$(curl -sk -o /dev/null -w '%{time_total}' -H "Authorization: Bearer $TOKEN" "$BASE/containers?all=true")
  dash_ms=$(curl -sk -o /dev/null -w '%{time_total}' -H "Authorization: Bearer $TOKEN" "$BASE/system/metrics")
  rss=$(ssh dockmesh@192.168.10.164 "ps -o rss= -p \$(pgrep -f '/home/dockmesh/dockmesh/dockmesh' | head -1)" | tr -d ' ')
  local nct
  nct=$(ssh dockmesh@192.168.10.164 "docker ps -a --format '{{.Names}}' | grep -c '^${PREFIX}-' || true")
  echo "${label} | stacks=${stacks_ms}s cont=${cont_ms}s metrics=${dash_ms}s rss=${rss}KB containers=${nct}"
}

probe "baseline"

export -f deploy_one
start=$(date +%s)
seq 1 "$N" | xargs -P "$PAR" -I {} bash -c 'deploy_one "$@"' _ {}
end=$(date +%s)
elapsed=$((end - start))
echo "-- deploy done in ${elapsed}s"

probe "after_deploy"
