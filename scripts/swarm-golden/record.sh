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
remote=/tmp/swarm-golden
cases=("$@")
if [ ${#cases[@]} -eq 0 ]; then
  for d in "$cases_dir"/*/; do cases+=("$(basename "$d")"); done
fi

ssh "$host" "mkdir -p $remote && cat > $remote/sockproxy.py" < "$here/sockproxy.py"
ssh "$host" "docker version --format '{{.Server.Version}}'" > "$cases_dir/../engine-version"
for c in "${cases[@]}"; do
  echo "== $c"
  src=$cases_dir/$c
  ssh "$host" "rm -rf $remote/$c && mkdir -p $remote/$c"
  for f in compose.yaml golden.env; do
    [ -f "$src/$f" ] && ssh "$host" "cat > $remote/$c/$f" < "$src/$f"
  done
  ssh "$host" bash -s "$remote/$c" "gt-$c" < "$here/remote.sh"
  mkdir -p "$src/recorded"
  for f in requests.jsonl service-specs.json stack-config.yaml deploy.log; do
    ssh "$host" "cat $remote/$c/$f" > "$src/recorded/$f"
  done
  tail -1 "$src/recorded/deploy.log"
done
ssh "$host" "rm -rf $remote"
