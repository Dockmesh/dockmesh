---
title: Scaling
description: Run several containers for one service, replace them one at a time with a rolling update, and what auto-scaling rules do.
---

Dockmesh can run more than one container for a service of a stack and replace them one by one. This is container-level scaling on one host. There is no load balancer; containers of a scaled service share the service name as a network alias, so other containers on the same network reach them through Docker's DNS.

## Scaling a service

On the stack's **Overview** tab, click the scale icon in a service row. **Scale** sets the number of containers for that service, from 0 to 100. It needs `stacks.deploy`.

Replicas are named `<stack>-<service>-1`, `<stack>-<service>-2` and so on. Scaling up creates the missing ones; scaling down removes the highest numbers first.

Scaling beyond 1 is refused when the service:

- sets `container_name` (two containers cannot share a name), or
- publishes a fixed host port such as `8080:80` (only one container can bind it). Leave the host port out, or use a range such as `"8080-8085:80"`.

When the image looks like a database (`postgres`, `mysql`, `mariadb`, `mongo`, `redis`, `elasticsearch` and similar), the dialog warns that replicas would share the data, whether or not the service mounts volumes. You have to tick **I understand the risk — proceed anyway** to continue.

Things to know:

- Scaling does not pull images. The image must already be on the host.
- Added replicas get the restart policy `unless-stopped` when the service sets none.
- Replicas are built from the base `compose.yaml` and the stack's `.env`, without the active environment override and without global environment variables.
- The replica count is not saved anywhere. A stack **Deploy** only recreates replica 1; the other replicas keep running with their previous configuration until you scale again. Scale down to 1 before a deploy that changes the service, then scale up again.
- `deploy.replicas` in `compose.yaml` is ignored.

## Rolling updates

The rolling-update icon in a service row replaces every container of the service one after the other, so that some replicas keep serving while others restart. It needs `stacks.deploy`.

| Option | Choices |
|---|---|
| **Order** | **stop-first**: stop the old container, then start the new one. **start-first**: start the new one next to the old one, then stop the old one. |
| **Parallelism** | How many containers to replace at once. 1 is the safest. |
| **On failure** | **pause**: stop at the first failed replica. **continue**: replace the rest anyway. **rollback**: restart the old replicas and abort. |

The dialog always sends its own **Order**, **Parallelism** and **On failure** values (preset to **stop-first**, 1, **pause**), so from the UI only `delay` comes from `deploy.update_config`. API calls that leave fields out fall back to `deploy.update_config` on the service, then to those defaults. Each new container must pass its health check, as in a normal deploy. **start-first** is refused for services with `container_name` or a fixed host port, because two containers would collide.

Rolling updates work on the local host only. On a remote host the request fails with an error.

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
      "scale_up":   {"metric": "cpu", "threshold_percent": 75, "duration_seconds": 120},
      "scale_down": {"metric": "cpu", "threshold_percent": 20, "duration_seconds": 300},
      "cooldown_seconds": 300
    }]
  }'
```

`GET` returns the rules and `DELETE` removes them, on the same path. The rules are stored in the stack's `.dockmesh.meta.json`. Every 30 seconds Dockmesh compares the service's average usage with the thresholds and adds or removes one replica at a time, then waits for the cooldown. A manual scale does not pause or update the auto-scaler; it may scale the service again on its next evaluation.

Before you rely on it, note the limits:

- Rules are evaluated for every stack, but metrics are only collected for local containers and scaling always acts on the local host, so rules only work for stacks on the local host.
- The current replica count is kept in memory and starts at 1 after a server restart, whatever is actually running.
- When no metrics are available for the service, the average counts as 0, which can trigger a scale-down.
- Both thresholds are compared against the `scale_up` metric; the `metric` of `scale_down` is not used. Only the first 20 replicas are included in the average.
- The same scaling restrictions apply (`container_name`, fixed host ports, no image pull).

## API

| Method and path | Purpose |
|---|---|
| `GET /api/v1/stacks/{name}/scale` | Replica counts of all services |
| `GET`, `POST /api/v1/stacks/{name}/services/{service}/scale` | Read or set replicas (`{"replicas": 3, "force": false}`) |
| `POST /api/v1/stacks/{name}/services/{service}/rolling-update` | Rolling update (`parallelism`, `delay_seconds`, `order`, `failure_action`) |
| `GET`, `PUT`, `DELETE /api/v1/stacks/{name}/scaling-rules` | Auto-scaling rules |

All of them need `stacks.deploy`. See [API](../reference/api.md).
