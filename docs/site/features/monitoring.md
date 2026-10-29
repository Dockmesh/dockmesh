---
title: Monitoring
description: Prometheus metrics, health endpoints and the built-in container metrics history.
---

Dockmesh exposes Prometheus metrics and health endpoints for external monitoring, and keeps a short history of container metrics for its own charts.

## Prometheus metrics

The server serves Prometheus metrics at `/metrics` on its HTTP port (not under `/api/v1`).

By default the endpoint needs a bearer token with the `metrics.view` permission. Create an [API token](./api-tokens.md) whose role has only that permission and give it to Prometheus:

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

Set `DOCKMESH_METRICS_AUTH=false` to serve `/metrics` without authentication. Only do that when the port isn't reachable from untrusted networks.

### Metrics that are emitted

| Metric | Type | Labels |
|---|---|---|
| `dockmesh_api_requests_total` | counter | `method`, `path` (route pattern), `status` |
| `dockmesh_audit_entries_total` | counter | `action` |
| `dockmesh_alerts_fired_total` | counter | `severity`, `rule` |
| `dockmesh_hosts_total` | gauge | `status` |
| `dockmesh_agent_last_seen_seconds` | gauge | `agent`, `host` |
| Go runtime (`go_*`) and process (`process_*`) metrics | | |

The counters start at zero when the server starts.

No other `dockmesh_*` series are exported. In particular there are no stack, container, backup, deploy-duration or agent round-trip metrics, so you'll find no `dockmesh_stacks_total`, `dockmesh_containers_total` or `dockmesh_backup_*` series in the scrape. For backup failures, watch the bell feed or the backup runs list instead.

A useful alert is a stale agent:

```yaml
- alert: DockmeshAgentStale
  expr: dockmesh_agent_last_seen_seconds > 120
  for: 5m
```

## Health endpoints

| Endpoint | Auth | Response |
|---|---|---|
| `GET /healthz/live` | none | `200 ok` while the process runs |
| `GET /healthz/ready` | none | `200 ready` when the database answers. `503` if it doesn't, or while the server is shutting down |
| `GET /api/v1/health` | none | JSON with `status`, `version` and `docker` (whether the Docker daemon is connected) |
| `GET /api/v1/system/health` | signed in | Checks shown in the UI: backups, proxy, agents, disk, Docker, scanner |

Use `/healthz/ready` for load balancers and container health checks, and `/healthz/live` for liveness probes. During shutdown, `/healthz/ready` returns `503` with `Retry-After: 10`, so a load balancer can stop sending traffic before the process exits.

## Container metrics history

Dockmesh samples CPU, memory, network and disk I/O of the local host's containers every 30 seconds and stores them in its SQLite database for the charts on container pages:

| Resolution | Kept for |
|---|---|
| raw (30 s) | 24 hours |
| 1 minute | 30 days |
| 1 hour | 365 days |

These periods are fixed. The history lives in the main database file, which grows with the number of containers. SQLite doesn't give freed space back on its own, so the file doesn't shrink after old samples are deleted.

Containers on agent hosts have live stats (the streaming charts on their pages) but no history. Host CPU, memory and disk figures on **Hosts** and the dashboard are live values only. On cgroup v2 hosts, container memory can read higher than `docker stats` shows.

See also [Alerts](./alerts.md), [Ports](../reference/ports.md) and [Troubleshooting](../troubleshooting/troubleshooting.md).
