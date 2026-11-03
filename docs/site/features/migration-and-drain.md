---
title: Migration and drain
description: Move a stack to another host, or empty a host by migrating all of its stacks, and what these operations do not move.
---

A migration redeploys one stack on a different host. A drain runs migrations for every stack deployed on one host. Both are for stacks without state in named volumes.

> **Warning:** Migration doesn't copy data. Stacks that use named volumes are refused. Bind-mounted host paths aren't moved either: the target starts the containers with whatever is at those paths on the target host. To move a stateful stack, back up its volumes and restore them on the target host (see [Backups](../operations/backups.md)), then deploy the stack there.

## Which stacks can be migrated

A stack can be migrated when it is deployed and none of its services mounts a named volume. That covers volumes declared in the compose file's top-level `volumes:` section, including `external` ones. Anonymous volumes (a container path without a name) and bind mounts don't block a migration; anonymous volumes start empty on the target.

The check runs when you start the migration (the API answers `409 Conflict` and lists the volumes), as the `named_volumes` preflight check and again in the `syncing` phase, in case the compose file changed in between.

## Migrate a stack

1. Open the stack and click **Migrate** in the header. The button appears when you can deploy the stack and Dockmesh has more than one host.
2. Pick the **Target host**. Dockmesh runs the **Pre-flight checks** and shows the result.
3. If all checks pass, click **Start migration**.

You need `stacks.migrate` on the stack and the right to deploy it on the target host. If `compose.yaml` or `.env` were changed outside Dockmesh, you are asked to confirm the changes first, like a deploy does.

The migration runs in the background. The stack page shows the target and the current phase, the active-migration indicator in the top bar opens a list of running and recent migrations, and the host's **Migrations** tab lists the migrations involving it. A notification is posted when the migration completes or fails.

The target gets the same deploy as a manual one: global environment variables and the stack's default environment override are applied.

### Preflight checks

| Check | Passes when |
|---|---|
| `target_online` | The target host is reachable |
| `named_volumes` | No service mounts a named volume |
| `no_concurrent` | No other migration of this stack is running |
| `source_online` | The source host is reachable |
| `source_metrics` | The source host's CPU and memory can be read |
| `target_capacity` | Free memory on the target is at least 1.2× the memory in use on the source host |
| `target_disk` | Free disk on the target is at least 1.1× the disk in use on the source host |
| `arch_match` | Always passes: the architecture of agent hosts isn't read, so it is never actually compared |
| `images` | The stack's compose file can be read and parsed. Images missing on the target are listed but don't fail the check |

The capacity and disk checks compare against the whole source host, not just the stack. Migrating off a busy host onto a smaller one can fail them even when the stack itself is small. Ports and Docker version aren't checked up front, and missing images only produce a note. Problems there show up when the stack starts on the target.

### Phases

| Phase | What happens |
|---|---|
| `preflight` | The checks above run again |
| `preparing` | Stack files changed outside Dockmesh since the start stop the migration. The compose file is parsed; images are pulled later, during the deploy |
| `pre_dump` | Optional pre-dump hook runs in a container on the source |
| `stopping` | The stack's containers are stopped and removed on the source |
| `syncing` | Named volumes are checked for again. No data is transferred |
| `starting` | The files are checked for outside changes again, then the stack is deployed on the target |
| `post_restore` | Optional post-restore hook runs on the target |
| `health_check` | Waits up to 5 minutes for every container to be running |
| `committing` | The stack is recorded as deployed on the target |

The stack is down from `stopping` until the target containers are running.

### When a migration fails

| Failed in | What Dockmesh does |
|---|---|
| `preflight`, `preparing`, `pre_dump` | Nothing to undo: the stack still runs on the source. |
| `stopping`, `syncing` | Deploys the stack on the source again. |
| `starting`, `post_restore`, `health_check` | Stops the stack on the target, then deploys it on the source again. |

After a successful rollback the migration's status is `rolled_back`. The rollback deploys the stack files as they are; if they were changed outside Dockmesh in the meantime, it doesn't restart the source and the error says so. Review the files and deploy the stack on the source yourself.

A running migration can't be cancelled. A server restart during a migration marks it failed; it doesn't resume and nothing is rolled back, so check where the stack runs and deploy it if needed.

### Hooks

Hooks are read from the `migration` section of the stack's `.dockmesh.meta.json` (`pre_dump`, `post_restore`). A hook runs with `sh -c` in the first running container of the stack. Its exit code isn't checked, but the migration fails if no running container is found or the command can't be started. A git sync doesn't copy `.dockmesh.meta.json` from the repository, so hooks can't come from a push.

### Roll back and clean up

A completed migration can be rolled back with `POST /api/v1/stacks/{name}/migrate/{id}/rollback`. Dockmesh stops the stack on the target and deploys it on the source again. There is no button for this in the UI.

`DELETE /api/v1/stacks/{name}/migrate/{id}/source` only stops leftover containers on the source. It doesn't delete files there.

## Drain a host

A drain migrates every stack deployed on a host to the other online hosts.

1. Open the host and click **Drain…** in the header, or **Plan drain…** on the **Maintenance** tab.
2. Review the plan: each stack with its proposed target.
3. Click **Begin drain**. The button is only enabled when the plan has room for every stack.

The host must be online. The server's own host has no drain button. Draining needs `hosts.update` on every host, not just this one. Each migration in it has the same rules as a manual one, so a stack with named volumes stops the drain.

### How the plan is made

Each stack is assumed to need 500 MB, whatever it actually uses. The candidates are the other online hosts whose disk usage can be read, the server included. Stacks are assigned one by one to the candidate with the most free disk, and that host's free space is reduced by 500 MB each time. A stack doesn't fit if the chosen host has less than 550 MB free. The plan doesn't look at named volumes, and you can't pick targets per stack.

### What happens during a drain

- Starting the drain makes the plan again on the server. Stacks migrate one at a time, in that order; stacks that don't fit in it are skipped (the API starts a drain even then).
- If a migration fails or can't start, the drain pauses and waits. Resuming continues with the next stack; the failed one isn't tried again. Resume or abort it with `POST /api/v1/hosts/{id}/drain/{drain_id}/resume` or `/abort`; `/pause` pauses it before the next stack. There are no buttons for pause, resume or abort in the UI.
- A drain doesn't mark the host as unschedulable. You can still deploy to it during and after the drain.
- Stacks aren't moved back afterwards. Migrate them back one by one if you want them on the original host.
- Drains don't survive a server restart.

See also [Multi-host](./multi-host.md) and [Backups](../operations/backups.md).
