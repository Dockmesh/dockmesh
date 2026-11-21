#!/bin/bash
# Runs on a Swarm manager: deploys one golden case with the docker CLI
# through sockproxy.py and leaves the recordings in the case directory.
# Usage: remote.sh <case dir> <stack name>
set -u
dir=$1 stack=$2
[[ $stack =~ ^gt-[a-z0-9][a-z0-9-]*$ ]] || { echo "bad stack name $stack" >&2; exit 2; }
cd "$dir" || exit 1
proxy=
cleanup() {
  [ -n "$proxy" ] && kill "$proxy" 2>/dev/null
  docker stack rm "$stack" > /dev/null 2>&1
  for _ in $(seq 120); do
    [ -z "$(docker network ls -q --filter "label=com.docker.stack.namespace=$stack")" ] && break
    sleep 1
  done
  # Volumes stay after `stack rm`; only this manager's copies are removed.
  docker volume ls -q --filter "label=com.docker.stack.namespace=$stack" | xargs -r docker volume rm > /dev/null 2>&1
}
trap cleanup EXIT
# golden.env is data, never sourced and never exported into this script:
# upper-case KEY=VALUE lines, handed only to the two docker stack commands.
vars=()
if [ -f golden.env ]; then
  while IFS= read -r line || [ -n "$line" ]; do
    [[ -z $line || $line == \#* ]] && continue
    [[ $line =~ ^([A-Z][A-Z0-9_]*)=(.*)$ ]] || { echo "bad golden.env line" >&2; exit 2; }
    case ${BASH_REMATCH[1]} in
      PATH | HOME | IFS | SHELL | USER | LD_* | DOCKER_* | BASH* | PYTHON* | COMPOSE_*) echo "reserved golden.env name" >&2; exit 2 ;;
    esac
    vars+=("$line")
  done < golden.env
fi
env "${vars[@]}" docker stack config -c compose.yaml > stack-config.yaml 2> stack-config.err || true
python3 ../sockproxy.py "$dir/docker.sock" "$dir/requests.jsonl" 2> proxy.log &
proxy=$!
for _ in $(seq 50); do [ -S docker.sock ] && break; sleep 0.1; done
DOCKER_HOST="unix://$dir/docker.sock" env "${vars[@]}" timeout 300 \
  docker stack deploy --detach=false --resolve-image never -c compose.yaml "$stack" > deploy.log 2>&1
echo "exit $?" >> deploy.log
kill "$proxy"
proxy=
# Only the stored specs: the rest of the inspect output is runtime state
# (task addresses, node ids) that differs per run.
docker service ls --filter "label=com.docker.stack.namespace=$stack" -q | xargs -r docker service inspect |
  python3 -c 'import json,sys; s=sorted((x["Spec"] for x in json.load(sys.stdin)), key=lambda x: x["Name"]); json.dump(s, sys.stdout, indent=2, sort_keys=True)' > service-specs.json
