---
title: Monitoring
description: Metrics history and charts, Needs attention, running operations, Prometheus metrics and health endpoints.
---

Dockmesh keeps a history of host and container metrics for its own charts, lists what needs attention, shows running work in the top bar, and exposes Prometheus metrics and health endpoints for external monitoring.

## Metrics history

Every 30 seconds Dockmesh samples CPU, memory, network and block I/O of each container, and CPU, memory and disk of the host. The server records its own host; agents record theirs on the remote host and send the samples to the server every minute. An agent keeps unsent samples for up to 30 minutes, so a short disconnect or a server restart leaves no gap. Agents older than the server keep working with live values only, and their charts say so.

The samples are stored in the server's SQLite database:

| Resolution | Kept for |
|---|---|
| raw (30 s) | 24 hours |
| 1 minute | 30 days |
| 1 hour | 365 days |

These periods are fixed. The history lives in the main database file, which grows with the number of containers. SQLite doesn't give freed space back on its own, so the file doesn't shrink after old samples are deleted.

### Charts

| Where | What |
|---|---|
| Container page, **Overview** tab | CPU, memory, network and block I/O of the container, with markers for the last start, image updates and rollbacks |
| Host page (**Hosts** → a host) | CPU, memory and disk of the host, plus **Top consumers**: the containers using the most CPU or memory |

Pick a range from **live** (streamed values) to 1 h, 6 h, 24 h, 7 d or 30 d. Drag across a chart to zoom, double-click to reset.

- CPU is a share of the whole host: 100 % means every core is busy. The API also returns the per-core value (`cpu_percent_core`, 100 per busy core, as `docker stats` shows it).
- Container memory is relative to the container's limit, or to the host's RAM when it has no limit, and excludes page cache, as `docker stats` shows it on cgroup v2.
- The overview and the host page list disk usage for every real filesystem of a host, with the mounts holding Docker, containerd, stacks, Dockmesh data and local backups labelled. The host's disk figure is the fullest of the Docker-related mounts.

Reading container metrics needs `containers.view`, host metrics need `hosts.view`, both on the host. The API is `GET /api/v1/containers/{id}/metrics`, `GET /api/v1/hosts/{id}/metrics` and `GET /api/v1/hosts/{id}/stats/containers`.

## Needs attention

**Needs attention** collects what someone should look at. The overview lists the five most severe items, and **View all** opens the full list under **Activity** → **Needs attention**.

| Item | Severity |
|---|---|
| Offline host | critical |
| Container that keeps restarting | critical |
| Unhealthy container, container that exited with an error | warning |
| Backup job whose last run failed | critical |
| Firing alert | the rule's severity |
| Proxy certificate that expires within 30 days | warning, critical within 7 days |
| Open warning or critical [notice](./notifications.md#notices) | the notice's severity |
| Agents on a different build than the server (one item) | info |
| Available image updates | info |

Items are sorted most severe first. Each source only appears for users who hold its view permission, and items are filtered to the hosts and stacks in the user's scope. The API is `GET /api/v1/attention`.

## Running operations

Image updates, backup runs, restores and new proxy sites run as operations with steps and a live log. The operations indicator in the top bar lists running and recently finished operations; click one to open its progress from any page. The progress dialog can be closed while the work continues. A toast tells you when your own operation finishes, and a failed one shows the step it failed at with its last log lines.

Running work can be cancelled from its progress dialog and from the backup and update lists:

| Operation | Can be cancelled |
|---|---|
| Backup run | In any step. Post-backup hooks still run, and the unfinished archive is deleted from the target. |
| Image update | While it pulls |
| Restore | While it downloads or verifies the archive |
| New proxy site | During the certificate wait; the site stays |

A cancelled operation names the step it stopped in. Cancelling needs the permission that started the work.

Operations are kept for 7 days. Credentials are masked before anything is stored or shown, and you only see operations on hosts and stacks in your scope. Work still running when the server stops is marked failed on the next start. The API is `GET /api/v1/operations`, `GET /api/v1/operations/{id}` and `POST /api/v1/operations/{id}/cancel`; updates arrive live on `/ws/events`.

## Prometheus metrics

The server serves Prometheus metrics at `/metrics` on its HTTP port (not under `/api/v1`).

By default the endpoint needs a bearer token with the `metrics.view` permission on everything. Create an [API token](./api-tokens.md) restricted to a role with only **Settings & audit: View**, and give it to Prometheus:

```yaml
scrape_configs:
  - job_name: dockmesh
    scheme: https
    authorization:
      type: Bearer
      credentials: dmt_…
    static_configs:
      - targets: ["dockmesh.example.com"]
```

Set `DOCKMESH_METRICS_AUTH=false` in the server environment to serve `/metrics` without authentication. Only do that when the port isn't reachable from untrusted networks.

### Metrics that are emitted

| Metric | Type | Labels |
|---|---|---|
| `dockmesh_api_requests_total` | counter | `method`, `path` (route pattern), `status` |
| `dockmesh_audit_entries_total` | counter | `action` |
| `dockmesh_alerts_fired_total` | counter | `severity`, `rule` |
| `dockmesh_hosts_total` | gauge | `status` (`local` for the server, then the agents by status) |
| `dockmesh_stacks_total` | gauge | `host` (`none` for stacks not deployed anywhere) |
| `dockmesh_agent_last_seen_seconds` | gauge | `agent` (name), `host` (agent ID) |
| `dockmesh_backup_last_run_timestamp_seconds` | gauge | `job` |
| `dockmesh_backup_last_duration_seconds` | gauge | `job` |
| Go runtime (`go_*`) and process (`process_*`) metrics | | |

The counters start at zero when the server starts; the gauges are refreshed every 30 seconds. The backup gauges describe each job's newest successful run (with or without warnings). `dockmesh_containers_total`, `dockmesh_deploy_duration_seconds` and `dockmesh_agent_rtt_seconds` are registered but have no values, so they don't show up in a scrape.

Two useful alerts:

```yaml
- alert: DockmeshAgentStale
  expr: dockmesh_agent_last_seen_seconds > 120
  for: 5m
- alert: DockmeshBackupStale
  expr: time() - dockmesh_backup_last_run_timestamp_seconds > 2 * 86400
```

## Health endpoints

| Endpoint | Auth | Response |
|---|---|---|
| `GET /healthz/live` | none | `200 ok` while the process runs |
| `GET /healthz/ready` | none | `200 ready` when the database answers. `503` if it doesn't, or while the server is shutting down |
| `GET /api/v1/health` | none | JSON with `status`, `version` and `docker` (whether the Docker daemon is connected) |
| `GET /api/v1/system/health` | `system.view` | Checks shown in the UI: system backup, reverse proxy, agents, disk, Docker daemon, CVE scanner |

Use `/healthz/ready` for load balancers and container health checks, and `/healthz/live` for liveness probes. During shutdown, `/healthz/ready` returns `503` with `Retry-After: 10`, so a load balancer can stop sending traffic before the process exits.

See also [Alerts](./alerts.md), [Notifications](./notifications.md), [Ports](../reference/ports.md) and [Troubleshooting](../troubleshooting/troubleshooting.md).
