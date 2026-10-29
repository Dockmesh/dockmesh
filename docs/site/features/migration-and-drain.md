---
title: Migration and drain
description: Move a stack to another host, or empty a host by migrating all of its stacks, and what these operations do not move.
---

A migration redeploys one stack on a different host. A drain runs migrations for every stack deployed on one host. Both are built for stateless stacks.

> **Warning:** Migration does not copy volume data. Named volumes are created empty on the target host. Bind-mounted host paths aren't moved either. If a stack keeps state in volumes (databases, uploads, configuration written at runtime), move that data yourself, for example with a [backup](../operations/backups.md) restored on the target, or the stack starts on the target without its data.

The source host keeps its volumes. A migration stops the source stack but doesn't delete its volumes, so nothing is lost there, and a rollback brings the old data back.

## Migrate a stack

1. Open the stack and click **Migrate** in the header.
2. Pick the **Target host**. Dockmesh runs the preflight checks and shows the result.
3. If all checks pass, click **Start migration**.

The stack has to be deployed already, and the target must differ from its current host. You need the `stacks.migrate` permission. The migration runs in the background. Its progress shows on the host's **Migrations** tab and in the active-migration indicator in the top bar. A notification is posted when it completes or fails.

### Preflight checks

| Check | Passes when |
|---|---|
| `target_online` | The target host is reachable |
| `no_concurrent` | No other migration of this stack is running |
| `source_online` | The source host is reachable |
| `source_metrics` | The source host's CPU and memory can be read |
| `target_capacity` | Free memory on the target is at least 1.2× the memory in use on the source host |
| `target_disk` | Free disk on the target is at least 1.1× the disk in use on the source host |
| `arch_match` | Source and target CPU architecture are the same, when both are known |

The capacity and disk checks compare against the whole source host, not just the stack. Migrating off a busy host onto a smaller one can fail them even when the stack itself is small. Ports, image availability and Docker version aren't checked up front. Problems there show up when the stack starts on the target.

### Phases

| Phase | What happens |
|---|---|
| `preflight` | The checks above run again |
| `preparing` | The compose file is parsed. Images are pulled later, during the deploy |
| `pre_dump` | Optional pre-dump hook runs in a container on the source |
| `stopping` | The stack's containers are stopped and removed on the source. Volumes stay |
| `syncing` | Named volumes are listed. No data is transferred |
| `starting` | The stack is deployed on the target |
| `post_restore` | Optional post-restore hook runs on the target |
| `health_check` | Waits up to 5 minutes for every container to be running |
| `committing` | The stack is recorded as deployed on the target |

The stack is down from `stopping` until the target containers are running. If `starting`, `post_restore` or `health_check` fails, Dockmesh redeploys the stack on the source automatically. A failure in an earlier phase leaves the stack stopped on the source. Deploy it again from the stack page.

Hooks are read from the `migration` section of the stack's `.dockmesh.meta.json` (`pre_dump`, `post_restore`). Their exit codes aren't checked, so a failing hook doesn't stop the migration.

A server restart during a migration marks it failed. It doesn't resume.

### Roll back and clean up

A completed migration can be rolled back with `POST /api/v1/stacks/{name}/migrate/{id}/rollback`. Dockmesh stops the stack on the target and deploys it on the source again, where the original volumes are still in place. Volume data written on the target stays on the target.

`DELETE /api/v1/stacks/{name}/migrate/{id}/source` only stops leftover containers on the source. It doesn't delete the source volumes or files. Remove those yourself once you no longer need a rollback.

## Drain a host

A drain migrates every stack deployed on a host to the other online hosts.

1. Open the host and click **Drain** in the header, or **Plan drain…** on the **Maintenance** tab.
2. Review the plan: each stack with its proposed target.
3. Click **Begin drain**. The button is only enabled when every stack in the plan is feasible.

The local host can't be drained. Draining needs `hosts.update`, and each migration in it needs the same conditions as a manual one.

### How the plan is made

Each stack is assumed to need 500 MB, whatever it actually uses. Stacks are assigned one by one to the online host with the most free disk, and that host's free space is reduced by 500 MB each time. A stack is marked not feasible if its target has less than 550 MB free. You can't pick targets per stack.

### What happens during a drain

- Stacks migrate one at a time, in plan order. When a drain is started through the API with a partly feasible plan, the stacks marked not feasible are skipped.
- If a migration fails or can't start, the drain pauses and waits. Resume or abort it with `POST /api/v1/hosts/{id}/drain/{drain_id}/resume` or `/abort`. There are no buttons for pause, resume or abort in the UI.
- A drain doesn't mark the host as unschedulable. You can still deploy to it during and after the drain.
- Stacks aren't moved back afterwards. Migrate them back one by one if you want them on the original host.
- Drains don't survive a server restart.

Everything said above about volume data applies to every stack in a drain. Draining a host that runs stateful stacks leaves those stacks without their data on the new host.

See also [Multi-host](./multi-host.md) and [Backups](../operations/backups.md).
