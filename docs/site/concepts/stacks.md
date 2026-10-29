---
title: Stacks
description: How Dockmesh stores stacks as Compose files on disk, what it keeps in the database, and exactly what a deploy does with your compose.yaml.
---

A stack is one Compose project: a directory under the stacks root with a `compose.yaml` in it. The directory name is the stack name and the Compose project name, so every container Dockmesh creates for the stack carries the label `com.docker.compose.project=<name>`.

## The filesystem is the source of truth

Each stack lives in its own directory under `DOCKMESH_STACKS_ROOT` (default `./stacks`). There is no per-host subdirectory; which host a stack runs on is recorded in the database.

| File | Purpose |
|---|---|
| `compose.yaml` | The Compose definition. Required. |
| `.env.age` | The stack's environment variables, encrypted with the server's age key. Used when secrets encryption is on (`DOCKMESH_SECRETS_ENCRYPT_ENV`, default `true`). |
| `.env` | The same variables in plaintext (mode `0600`). Used when secrets encryption is off. |
| `compose.<env>.yaml` | Optional environment override files, for example `compose.prod.yaml`. |
| `.dockmesh.meta.json` | Dockmesh's per-stack settings: the active environment override and auto-scaling rules. |

Only one of `.env` and `.env.age` exists at a time; saving the environment in the UI writes one and removes the other. When both are present, `.env.age` wins. If you drop a hand-written `.env` next to an existing `.env.age`, Dockmesh ignores it. If `.env.age` exists but encryption is turned off, reading the stack fails.

Keep `secrets.age-key` in the data directory backed up. Without it, every `.env.age` is unreadable.

The encryption covers the stack's `.env` at rest in the stacks directory. Plaintext copies of the environment still exist in other places: in the temporary staging directory during a deploy, in the copy an agent keeps on a remote host, and in global environment variables (stored unencrypted in the database). Templates that generate secrets write them into `compose.yaml`.

The full directory tree is in [Filesystem layout](../reference/filesystem-layout.md).

### Stack names

Names must be 2 to 63 characters, match `^[a-z0-9][a-z0-9-]*[a-z0-9]$` (lowercase letters, digits and hyphens, no leading or trailing hyphen), and must not be one of the reserved names `dockmesh`, `agent`, `system`, `api`, `admin` or `config`. Directories under the stacks root that break these rules, or that have no `compose.yaml`, are ignored.

### Editing files outside Dockmesh

<!-- update after: backups: scope hooks, limit targets, check sftp keys; fix bind mounts -->
You can edit stack files with any editor, `git pull` or `rsync`. Dockmesh watches the stacks root and each stack directory. When `compose.yaml`, `.env` or `.dockmesh.meta.json` changes, the stack page shows a banner saying the file was changed outside Dockmesh. Dockmesh's own writes are not flagged (it ignores events on files it wrote in the last two seconds). Changes to `.env.age` are not watched.

Nothing is redeployed automatically. The next deploy picks up whatever is on disk.

A stack directory you create by hand while the server is running does not appear in the stack list until the server restarts. To bring in existing Compose projects, use `dockmesh import compose-dir` or adopt a running project from the UI (see [Stacks](../features/stacks.md)).

## What the database holds

- **Deployment record:** one row per stack name with the host ID and a status (`deployed` or `stopped`).
- **Deploy history:** the compose text that was deployed (with the environment override already merged) and the image tag each service used. Rollback redeploys one of these entries. The `.env` file itself is not stored, but when an environment override was active the stored compose text has the `.env` values already filled in.
- **Dependencies** between stacks, **git-source** bindings, **templates** and **global environment variables**.

### One host per stack

The deployment record is keyed by stack name, so a stack is deployed on exactly one host. If you deploy the same stack to a second host, the record now points at the second host and the containers on the first host keep running unmanaged. To run the same app on two hosts, create two stacks (`app-eu`, `app-us`).

The stack page deploys to the host selected in the host switcher. With **All hosts** selected it uses the host from the deployment record.

## How a deploy reconciles

Every deploy runs the same steps, on the local host or inside the agent:

1. Write `compose.yaml` and the environment into a temporary staging directory and parse them with the Compose library (compose-go). Your `.env` values are used for `${VAR}` interpolation.
2. Create missing project networks and volumes. Existing ones are left alone.
3. Pull images for all services. An image is pulled when it is missing locally, when the service sets `pull_policy: always`, or when a git-source auto-deploy forces it. If a pull fails for an image that is not cached, the deploy stops here and the running containers are not touched.
4. For each service in **alphabetical order**: stop and remove the existing container with the same name, create a new one, start it and wait for it to become healthy.

Every deploy recreates every container in the stack, even when nothing changed. There is no config-hash comparison. Expect a short restart of each service on each deploy.

Containers are named `container_name` if set, otherwise `<stack>-<service>-1`. Networks and volumes without an explicit `name` become `<stack>_<key>`.

**Health gate.** If the service has a `healthcheck`, Dockmesh waits up to `start_period + interval × retries + 10s` (retries defaults to 3; 70 s if neither is set) for `healthy`. Without a healthcheck it waits 5 seconds and fails only if the container exited with a non-zero code. A failed health gate aborts the deploy at that service. Services earlier in the alphabet are already replaced, later ones still run the old version, and there is no automatic rollback.

**Removed services.** Services you delete from `compose.yaml` are not removed; their containers keep running. Use the stack's cleanup action on the local host, or remove them by hand.

**Stop** removes all of the stack's containers. Networks and volumes stay.

### Compose fields that are honoured

| Area | Fields |
|---|---|
| Service | `image`, `container_name`, `command`, `entrypoint`, `environment` (including values loaded through `env_file`), `labels`, `user`, `working_dir`, `hostname`, `restart`, `privileged`, `read_only`, `cap_add`, `cap_drop`, `tty`, `stdin_open`, `healthcheck` (test, interval, timeout, retries, start_period, start_interval, disable), `pull_policy: always`, `network_mode` |
| Ports | `ports` with host IP, published port, target port and protocol |
| Service volumes | bind mounts with `:ro`, named volumes with `read_only`, `tmpfs` (target only) |
| Service networks | `networks` with `aliases` (the service name is always added as an alias) |
| Top-level networks | `name`, `external`, `driver`, `driver_opts`, `internal`, `attachable`, `labels` |
| Top-level volumes | `name`, `external`, `driver`, `driver_opts`, `labels` |

### Compose fields that are silently ignored

These parse without error but have no effect on the container. Check this list before you migrate an existing project:

- `depends_on` (ordering is alphabetical, conditions are not checked)
- `deploy` and everything under it: `replicas`, `resources` (CPU and memory limits), `restart_policy`, `placement`. `deploy.update_config` is read only by rolling updates.
- `mem_limit`, `cpus`, `shm_size`, `ulimits`, `sysctls`, `devices`, `gpus`
- `extra_hosts`, `dns`, `dns_search`, `expose`, `mac_address`, `ipv4_address`/`ipv6_address`, top-level network `ipam` and `enable_ipv6`
- `logging`, `security_opt`, `pid`, `ipc`, `init`, `stop_signal`, `stop_grace_period`, `group_add`, `platform`, `runtime`, `links`
- `secrets` and `configs`
- bind-mount options other than read-only (propagation, SELinux `:z`/`:Z`, `create_host_path`), volume `nocopy`/`subpath`, tmpfs `size`
- the service-level `tmpfs:` key and `volumes_from` (use a `type: tmpfs` volume entry instead of `tmpfs:`)
- `build` when `image` is also set (the image is pulled instead)

Two cases fail instead of being ignored: a service with `build:` and no `image:` is rejected, and services that declare `profiles:` are skipped because no profile is ever activated. `restart: on-failure:N` is passed to Docker as one string; use `on-failure` without a count.

### Relative paths and the staging directory

<!-- update after: backups: scope hooks, limit targets, check sftp keys; fix bind mounts -->
Because the compose file is parsed inside a temporary staging directory, relative paths resolve against that directory, not the stack directory, and the staging directory is deleted after the deploy. A bind mount like `./data:/data` therefore points at a throwaway path, so its data does not survive the next deploy. Use absolute host paths or named volumes for anything you need to keep. The same applies to `env_file:` entries other than `.env`, and to `extends` or `include` of other files: they are not found.

When an environment override is active, the files are merged in the real stack directory on the server first, so relative paths become absolute paths under the server's stacks root. That works on the local host; on a remote host the path usually does not exist.

## Environment overrides

An override file `compose.<env>.yaml` sits next to `compose.yaml`. `<env>` may contain lowercase letters, digits, `-` and `_`. On deploy, Dockmesh merges `compose.yaml` with the override chosen for that deploy, or with the stack's default override from `.dockmesh.meta.json`. Interpolation in the merged file uses the stack's `.env` only, not global environment variables. Deploy history stores the merged result. See [Environment variables](../features/environment-variables.md).

## Related

- [Stacks](../features/stacks.md): creating, editing, deploying, rollback and adopt in the UI
- [Scaling](../features/scaling.md): running more than one container per service
- [Git sources](../features/git-sources.md): keeping a stack in sync with a repository
- [Migration and drain](../features/migration-and-drain.md): moving a stack to another host
