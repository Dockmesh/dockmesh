#!/bin/bash
# Records the golden files of the Swarm converter tests: every case under
# internal/swarmgolden/testdata/cases is deployed with `docker stack
# deploy` on a Swarm manager and the specs the CLI sent are written to the
# case's recorded/ directory. Lab tooling only; Dockmesh itself never runs
# the docker CLI. See internal/swarmgolden/README.md.
#
# Usage: scripts/swarm-golden/record.sh <ssh host of a manager> [case ...]
set -euo pipefail
host=$1
shift
here=$(cd "$(dirname "$0")" && pwd)
cases_dir=$(cd "$here/../../internal/swarmgolden/testdata/cases" && pwd)
cases=("$@")
if [ ${#cases[@]} -eq 0 ]; then
  for d in "$cases_dir"/*/; do cases+=("$(basename "$d")"); done
fi
for c in "${cases[@]}"; do
  [[ $c =~ ^[a-z0-9][a-z0-9-]*$ ]] || { echo "bad case name: $c" >&2; exit 2; }
  [ -f "$cases_dir/$c/compose.yaml" ] || { echo "no case $c" >&2; exit 2; }
done

remote=$(ssh "$host" mktemp -d /tmp/swarm-golden.XXXXXXXX)
[[ $remote =~ ^/tmp/swarm-golden\.[A-Za-z0-9]+$ ]] || { echo "unexpected remote dir" >&2; exit 1; }
trap 'ssh "$host" rm -rf "$remote"' EXIT
ssh "$host" "cat > $remote/sockproxy.py" < "$here/sockproxy.py"
ssh "$host" "docker version --format '{{.Server.Version}}'" > "$cases_dir/../engine-version"
for c in "${cases[@]}"; do
  echo "== $c"
  src=$cases_dir/$c
  ssh "$host" "mkdir $remote/$c"
  for f in compose.yaml golden.env; do
    if [ -f "$src/$f" ]; then ssh "$host" "cat > $remote/$c/$f" < "$src/$f"; fi
  done
  ssh "$host" bash -s "$remote/$c" "gt-$c" < "$here/remote.sh"
  mkdir -p "$src/recorded"
  for f in requests.jsonl service-specs.json stack-config.yaml deploy.log; do
    ssh "$host" "cat $remote/$c/$f" > "$src/recorded/$f"
  done
  tail -1 "$src/recorded/deploy.log"
done
