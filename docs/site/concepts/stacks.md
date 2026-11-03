---
title: Stacks
description: How Dockmesh stores stacks as Compose files on disk, what it keeps in the database, and exactly what a deploy does with your compose.yaml.
---

A stack is one Compose project: a directory under the stacks root with a `compose.yaml` in it. The directory name is the stack name and the Compose project name, so every container Dockmesh creates for the stack carries the label `com.docker.compose.project=<name>`.

## The filesystem is the source of truth

Each stack lives in its own directory under `DOCKMESH_STACKS_ROOT` (default `./stacks`; `install.sh` sets `/var/lib/dockmesh/stacks`). There is no per-host subdirectory; which host a stack runs on is recorded in the database.

| File | Purpose |
|---|---|
| `compose.yaml` | The Compose definition. Required. |
| `.env.age` | The stack's environment variables, encrypted with the server's age key. Used when secrets encryption is on (`DOCKMESH_SECRETS_ENCRYPT_ENV`, default `true`). |
| `.env` | The same variables in plaintext (mode `0600`). Used when secrets encryption is off. |
| `compose.<env>.yaml` | Optional environment override files, for example `compose.prod.yaml`. |
| `.dockmesh.meta.json` | Dockmesh's per-stack settings: the default environment override and auto-scaling rules. |

Only one of `.env` and `.env.age` exists at a time; saving the environment in the UI writes one and removes the other. When both are present, `.env.age` wins, so a hand-written `.env` next to an existing `.env.age` is ignored. If `.env.age` exists but encryption is turned off, reading the stack fails.

Keep `secrets.age-key` in the data directory backed up. Without it, every `.env.age` is unreadable.

The encryption covers the stack's `.env` at rest in the stacks directory. Plaintext copies of the environment still exist in other places: in the temporary staging directory during a deploy, and in the copy an agent keeps of each stack deployed on its host. Template placeholders used in `compose.yaml`, generated secrets included, are written into `compose.yaml` in plain text.

The full directory tree is in [Filesystem layout](../reference/filesystem-layout.md).

### Stack names

Names must be 2 to 63 characters, match `^[a-z0-9][a-z0-9-]*[a-z0-9]$` (lowercase letters, digits and hyphens, no leading or trailing hyphen), and must not be one of the reserved names `dockmesh`, `agent`, `system`, `api`, `admin` or `config`. Directories under the stacks root that break these rules, or that have no `compose.yaml`, are ignored.

### Editing files outside Dockmesh

You can edit stack files with any editor, `git pull` or `rsync`. Dockmesh notices such changes in two ways:

- **While a stack page is open**, a watcher on the stacks root and each stack directory reports changes to `compose.yaml`, `.env` and `.dockmesh.meta.json` as a banner ("… was modified outside Dockmesh") with **Reload** and **Ignore**. Dockmesh's own writes are not reported.
- **On every deploy**, Dockmesh compares `compose.yaml`, `.env` and `.env.age` with the checksums it recorded when it last wrote them. The checksums are kept in the database, so a container that can write the stack directory can't update them. Changed files are listed on the stack page ("Modified outside Dockmesh: …"), and a deploy, rollback or migration from them asks for confirmation first (**Deploy anyway**). A git auto-deploy can't ask, so it is held back and the stack's deployers get a notice to review and deploy by hand.

Nothing is redeployed automatically. The next deploy picks up whatever is on disk.

A service that bind-mounts the stack directory itself writable (`.:/app`) can rewrite its own `compose.yaml` and `.env`; the stack page warns about it. Mount a subdirectory, or add `:ro`.

A stack directory you create by hand while the server is running does not appear in the stack list until the server restarts. To bring in existing Compose projects, use `dockmesh import compose-dir` or adopt a running project from the UI (see [Stacks](../features/stacks.md)).

## What the database holds

- **Deployment record:** one row per stack name with the host ID and a status (`deployed` or `stopped`). A stack that was never deployed has no record and shows as **Not deployed**; until its first deploy it belongs to the host it was created for.
- **Deploy history:** for each deploy the base `compose.yaml`, the name of the environment override it used, and the image tag each service ran. Rollback redeploys one of these entries. The `.env` is not stored.
- **File checksums** of `compose.yaml`, `.env` and `.env.age` as Dockmesh last wrote them.
- **Dependencies** between stacks, **git-source** bindings, **templates** and **global environment variables**.

### One host per stack

The deployment record is keyed by stack name, so a stack is deployed on exactly one host. If you deploy the same stack to a second host, the record now points at the second host and the containers on the first host keep running unmanaged. To run the same app on two hosts, create two stacks (`app-eu`, `app-us`).

The stack page deploys to the host selected in the host switcher. With **All hosts** selected it uses the host from the deployment record, or the host the stack was created for.

## How a deploy reconciles

Before a deploy, Dockmesh puts the global environment variables in front of the stack's `.env` (a key in the stack's `.env` wins) and, when an [environment override](#environment-overrides) is active, merges it into `compose.yaml`. The deploy itself runs the same steps on the server's host or inside the agent:

1. Write `compose.yaml` and the environment into a temporary staging directory and parse them with the Compose library (compose-go). The environment is used for `${VAR}` interpolation.
2. Create missing project networks and volumes. Existing ones are left alone.
3. Pull images for all services. An image is pulled when it is missing locally, when the service sets `pull_policy: always`, or when a git auto-deploy forces it. If a pull fails for an image that is not cached, the deploy stops here and the running containers are not touched.
4. For each service in **alphabetical order**: stop and remove the existing container with the same name, create a new one, start it and wait for it to become healthy.

Every deploy recreates every container in the stack, even when nothing changed. There is no config-hash comparison. Expect a short restart of each service on each deploy.

Containers are named `container_name` if set, otherwise `<stack>-<service>-1`. Networks and volumes without an explicit `name` become `<stack>_<key>`.

**Health gate.** If the service has a `healthcheck`, Dockmesh waits up to `start_period + interval × retries + 10s` (retries defaults to 3; 70 s if neither is set) for `healthy`. Without a healthcheck it waits 5 seconds and fails only if the container exited with a non-zero code. A failed health gate aborts the deploy at that service. Services earlier in the alphabet are already replaced, later ones still run the old version, and there is no automatic rollback.

**Removed services.** Services you delete from `compose.yaml` are not removed; their containers keep running. Remove them by hand.

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

The compose file is parsed inside a temporary staging directory that is deleted after the deploy. Relative bind mounts are pointed back at the stack's persistent directory, as `docker compose` would mount them:

- On the server's host, `./data:/data` mounts `<stacks root>/<stack>/data`.
- On an agent host, it mounts the agent's copy of the stack directory, `/var/lib/dockmesh/stacks/<stack>/data` with the agent's default data directory.

Data in such a directory on the server's host is part of every stack and system backup, because both archive the stack directory. Data on an agent host is not backed up; use named volumes there.

Other relative paths still resolve against the staging directory, which only holds `compose.yaml` and `.env`: `env_file:` entries other than `.env`, and `extends` or `include` of other files, are not found.

Stacks deployed by earlier versions may still run with bind mounts into an old staging directory, which no longer exists on disk. Admins get a notice per stack ("Redeploy stack … on …"); on agent hosts, upgrade the agent first. The next deploy moves the files the old container wrote into the stack directory, if that directory is still empty, and then recreates the container. If the copy fails, the deploy stops, the old container is started again, and admins get a notice. Scaling and rolling updates are refused until the stack has been redeployed.

When an environment override is active, the files are merged in the stack directory on the server first, so relative paths become absolute paths under the server's stacks root. That works on the server's host; on an agent host the path usually does not exist.

## Environment overrides

An override file `compose.<env>.yaml` sits next to `compose.yaml`. `<env>` starts with a lowercase letter or digit and may contain lowercase letters, digits, `-` and `_`. On deploy, Dockmesh merges `compose.yaml` with the override chosen for that deploy, or with the stack's default override from `.dockmesh.meta.json`. The merge uses the stack's `.env` together with the global variables for interpolation. Git auto-deploys and migrations apply the default override too, but only if the last manual deploy used that override with the same file content; otherwise they are refused until you deploy the stack once by hand. Deploy history stores the base file and the override name, and a rollback applies the override again. See [Environment variables](../features/environment-variables.md).

## Related

- [Stacks](../features/stacks.md): creating, editing, deploying, rollback and adopt in the UI
- [Scaling](../features/scaling.md): running more than one container per service
- [Git sources](../features/git-sources.md): keeping a stack in sync with a repository
- [Migration and drain](../features/migration-and-drain.md): moving a stack to another host
