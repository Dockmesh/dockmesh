# Swarm converter golden cases

Dockmesh converts compose files into Swarm service specs itself, on the
server, and follows what `docker stack deploy` does. These cases pin that
behaviour: each one is a compose file next to the exact specs the docker CLI
sent to a real Swarm for it. The package loads the cases and compares specs
(`LoadAll`, `CompareServices`), so the converter's tests can convert the same
compose file with the same stack name and compare the result with the
recording. Nothing here talks to Docker at test time.

## Layout

```
testdata/
  engine-version             Docker Engine the cases were recorded on
  cases/<name>/
    compose.yaml             the input, deployed as stack gt-<name>
    golden.env               optional; variables for interpolation (the CLI got them as its environment)
    case.yaml                optional; pending and accepted differences, see below
    recorded/
      requests.jsonl         every services/create and networks/create body the CLI sent, one per line
      service-specs.json     `.Spec` of `docker service inspect` after the deploy (the spec as the daemon stored it)
      stack-config.yaml      `docker stack config` output (merged, interpolated compose file)
      deploy.log             CLI output; the last line is the exit code (124: tasks did not converge in 300 s)
```

The comparison uses `requests.jsonl`, the spec before the daemon fills in its
own defaults. `service-specs.json` shows those defaults for reference.

Recordings use `--resolve-image never`, so images are not pinned to a digest
and carry no platform constraints. Digest pinning is the deploy executor's
job and is tested there.

## case.yaml

```yaml
pending:            # paths a later converter piece produces
  - "*:TaskTemplate/Networks"
accepted:           # permanent, deliberate differences to the CLI
  - path: "gt-restart-policy_compose-restart:TaskTemplate/RestartPolicy"
    reason: compose `restart:` is not translated; the deploy preview hints at deploy.restart_policy
```

A path is `<service spec name>:<key>/<key>/…` or `*:<key>/…` for every service
of the case, with the JSON names of the Docker API types; label names keep
their dots (`Labels/com.docker.stack.image`). The comparison drops the key and
everything below it. Remove a pending entry when the converter produces that
part of the spec. Every accepted entry needs a reason.

## Adding a case

1. Create `testdata/cases/<name>/compose.yaml` (lower-case name, digits and
   dashes). Use images the lab registry mirror has, no `build:`, no host
   paths, no secrets. Keep it small: one topic per case, one service per
   variant of that topic. `fixture-*` cases are the compose files of the lab
   fixtures as they are, with their own image tags.
2. Record it on a Swarm manager of the lab (see below).
3. Read the recorded `requests.jsonl` and `deploy.log`; check that the CLI did
   what the case is about.
4. Run `go test ./internal/swarmgolden/ ./internal/swarmconvert/`; add
   `case.yaml` entries only for what the converter does not produce yet or
   deliberately does differently.

## Recording

`scripts/swarm-golden/record.sh <ssh host of a manager> [case ...]`

It needs SSH access to a Swarm manager with the docker CLI and python3. Per
case it copies the files to a fresh `/tmp/swarm-golden.*` directory, runs
`docker stack config`, then `docker stack deploy --detach=false
--resolve-image never` through `sockproxy.py`, a small unix-socket proxy
that writes the create requests the CLI sends to the daemon. It then reads
back the service specs, removes the stack, waits until its networks are
gone and copies the recordings into `recorded/`. Without case names it
records every case. The stack names start with `gt-`; use a Swarm that runs
nothing else under that prefix. A case is deployed as it is written, so read
every compose file of a case from outside the project before recording it.

Record everything again when the lab's Docker Engine changes, update
`testdata/engine-version` and `engineVersion` in `golden_test.go` and review
the diff of `recorded/` before committing.
