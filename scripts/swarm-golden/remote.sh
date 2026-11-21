#!/bin/bash
# Runs on a Swarm manager: deploys one golden case with the docker CLI
# through sockproxy.py and leaves the recordings in the case directory.
# Usage: remote.sh <case dir> <stack name>
set -u
dir=$1 stack=$2
cd "$dir" || exit 1
rm -f requests.jsonl service-specs.json stack-config.yaml deploy.log docker.sock
set -a
[ -f golden.env ] && . ./golden.env
set +a
docker stack config -c compose.yaml > stack-config.yaml 2> stack-config.err || true
python3 ../sockproxy.py "$dir/docker.sock" "$dir/requests.jsonl" 2> proxy.log &
proxy=$!
for _ in $(seq 50); do [ -S docker.sock ] && break; sleep 0.1; done
DOCKER_HOST="unix://$dir/docker.sock" timeout 300 \
  docker stack deploy --detach=false --resolve-image never -c compose.yaml "$stack" > deploy.log 2>&1
echo "exit $?" >> deploy.log
kill "$proxy"
# Only the stored specs: the rest of the inspect output is runtime state
# (task addresses, node ids) that differs per run.
docker service ls --filter "label=com.docker.stack.namespace=$stack" -q | xargs -r docker service inspect |
  python3 -c 'import json,sys; s=sorted((x["Spec"] for x in json.load(sys.stdin)), key=lambda x: x["Name"]); json.dump(s, sys.stdout, indent=2, sort_keys=True)' > service-specs.json
docker stack rm "$stack" > /dev/null
for _ in $(seq 120); do
  [ -z "$(docker network ls -q --filter "label=com.docker.stack.namespace=$stack")" ] && break
  sleep 1
done
