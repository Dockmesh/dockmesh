---
title: Scaling
description: Run several containers for one service, replace them one at a time with a rolling update, and what auto-scaling rules do.
---

Dockmesh can run more than one container for a service of a stack and replace them one by one. This is container-level scaling on one host. There is no load balancer; containers of a scaled service share the service name as a network alias, so other containers on the same network reach them through Docker's DNS.

## Scaling a service

On the stack's **Overview** tab, open the menu of a service row and choose **Scale…**. The slider sets the number of containers for that service from 0 to 10; the API accepts up to 100. It needs `stacks.deploy` on the stack and works on the server host and on agent hosts.

Replicas are named `<stack>-<service>-1`, `<stack>-<service>-2` and so on. Scaling up creates the missing ones; scaling down removes the highest numbers first. Replicas that are not running are replaced on the next scale.

Scaling beyond 1 is refused when the service:

- sets `container_name` (two containers can't share a name), or
- publishes a fixed host port such as `8080:80` (only one container can bind it). Leave the host port out, or use a range such as `"8080-8085:80"`.

When the image looks like a database or another stateful service (`postgres`, `mysql`, `mariadb`, `mongo`, `redis`, `elasticsearch`, `cockroach`, `influxdb`, `clickhouse`, `neo4j`, `cassandra`, `memcached`, `etcd`), the dialog warns that replicas would share the data, whether or not the service mounts volumes. Tick **I understand the risk — proceed anyway** to continue.

Things to know:

- Scaling does not pull images. The image must already be on the host.
- Added replicas get the restart policy `unless-stopped` when the service sets none.
- Replicas are built from the base `compose.yaml` and the stack's `.env`, without the stack's environment override and without global environment variables.
- The replica count is not saved anywhere. A stack **Deploy** only recreates replica 1; the other replicas keep running with their previous configuration until you scale again. Scale down to 1 before a deploy that changes the service, then scale up again.
- `deploy.replicas` in `compose.yaml` is ignored.
- Scaling up waits for each new replica to pass its health check (or, without one, to keep running for a short grace period) before it creates the next one, as a deploy does.
- When stack files changed outside Dockmesh, **Scale…** and **Rolling update…** first ask **Stack files changed outside Dockmesh** and list the files, like a deploy.
- Scaling and rolling updates are refused while the stack's containers still bind-mount a temporary staging directory from an earlier version. Deploy the stack once to move that data into the stack directory.

## Rolling updates

**Rolling update…** in the same service menu replaces every container of the service one after the other, so that some replicas keep serving while others restart. It needs `stacks.deploy`. The container count stays the same; use **Scale…** to change it.

| Option | Choices |
|---|---|
| **Order** | **stop-first**: stop the old container, then start the new one. **start-first**: start the new one next to the old one, then stop the old one. |
| **Parallelism** | How many containers to replace before the next delay. Containers in one batch are still replaced one after the other. 1 is the safest. |
| **On failure** | **pause**: stop at the first failed replica. **continue**: replace the rest anyway. **rollback**: recreate the replicas replaced so far from the image they ran before, then stop. When the image reference is unchanged (same tag), nothing is rolled back. |

The dialog always sends its own **Order**, **Parallelism** and **On failure** values (preset to **stop-first**, 1, **pause**), so from the UI only the delay comes from `deploy.update_config`. API calls that leave fields out fall back to `deploy.update_config` on the service, then to those defaults. Each new container must pass its health check, as in a normal deploy. **start-first** is refused for services with `container_name` or a fixed host port, because two containers would collide. Rolling updates don't pull images either.

Rolling updates work on the server host only. On an agent host the request fails with an error.

## Auto-scaling rules

Auto-scaling rules exist only in the API; there is no UI for them. A rule scales one service between a minimum and maximum replica count based on its average CPU or memory usage:

```bash
curl -X PUT -H "Authorization: Bearer $DOCKMESH_TOKEN" \
  -H "Content-Type: application/json" \
  https://dockmesh.example.com/api/v1/stacks/web/scaling-rules \
  -d '{
    "enabled": true,
    "rules": [{
      "service": "app",
      "min_replicas": 1,
      "max_replicas": 4,
      "cpu_unit": "host",
      "scale_up":   {"metric": "cpu", "threshold_percent": 75, "duration_seconds": 120},
      "scale_down": {"metric": "cpu", "threshold_percent": 20, "duration_seconds": 300},
      "cooldown_seconds": 300
    }]
  }'
```

`GET` returns the rules and `DELETE` removes them, on the same path. The rules are stored in the stack's `.dockmesh.meta.json`, and the rules and their values are checked when they are saved and again every time the auto-scaler reads them. Every 30 seconds Dockmesh averages the service's usage over the last minute, compares it with the thresholds and, once a threshold has been crossed for `duration_seconds`, adds or removes one replica, then waits for the cooldown. A manual scale does not pause or update the auto-scaler; it may scale the service again on its next evaluation.

`cpu_unit` sets what a CPU threshold means:

| `cpu_unit` | Threshold is |
|---|---|
| `host` | Share of the whole host, as the charts show it (0 to 100). Rules saved without `cpu_unit` get this unit. |
| `core` | Percent of one core, 100 per busy core, as `docker stats` shows it. Rules stored before v0.5.0 have this unit and keep behaving as before. |

`GET` shows the unit of every rule. Memory thresholds are a percentage of the container's memory limit.

The auto-scaler builds new replicas from the files on disk with nobody there to review them, so it pauses a stack while:

- a stack file (`compose.yaml`, `.env`, `.dockmesh.meta.json` or a `compose.<env>.yaml` override) was changed outside Dockmesh,
- the stack's default environment override was never confirmed by a manual deploy, or
- the stored rules are invalid.

Users who may deploy the stack get an **Auto-scaling of &lt;stack&gt; is paused** notice that says why, and the stack page shows the same as a banner. Review the files and deploy the stack once (or save the rules again when they are invalid); auto-scaling resumes on its next evaluation.

Before you rely on it, note the limits:

- Rules are read for every stack, but the metrics used are those of the server host and scaling always acts on the server host, so rules only work for stacks on the server host.
- The current replica count is kept in memory and starts at 1 after a server restart, whatever is actually running.
- When no metrics are available for the service, the average counts as 0, which can trigger a scale-down. A container without a memory limit counts as 0 % memory.
- Both thresholds are compared against the `scale_up` metric; the `metric` of `scale_down` is not used. Only the first 20 replicas are included in the average.
- The same scaling restrictions apply (`container_name`, fixed host ports, no image pull, no global variables). The database warning does not: database-like images are scaled without asking.

## API

| Method and path | Purpose |
|---|---|
| `GET /api/v1/stacks/{name}/scale` | Replica counts of all services |
| `GET`, `POST /api/v1/stacks/{name}/services/{service}/scale` | Scale check, or set replicas (`{"replicas": 3, "force": false}`) |
| `POST /api/v1/stacks/{name}/services/{service}/rolling-update` | Rolling update (`parallelism`, `delay_seconds`, `order`, `failure_action`) |
| `GET`, `PUT`, `DELETE /api/v1/stacks/{name}/scaling-rules` | Auto-scaling rules |

All of them need `stacks.deploy`. Scale and rolling-update requests take `?host=` for the host the stack runs on. Scaling a database-like image beyond 1 without `"force": true` answers `409` with `force_needed`. Like a deploy, scale and rolling-update requests answer `409` with code `modified_outside` and the changed `files` when stack files changed outside Dockmesh; repeat the request with `?confirm_external=1` after reviewing them. See [REST API](../reference/api.md).
