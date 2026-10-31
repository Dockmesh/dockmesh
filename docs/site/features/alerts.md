---
title: Alerts
description: Threshold alert rules on container CPU and memory, and their current limitations.
---

Alert rules watch container CPU and memory on the local host and notify [channels](./notifications.md) when a threshold is crossed. They're configured on the **Alerts** page, which has **Rules**, **Channels** and **History** tabs.

<!-- update after: alerts: add event rules for containers, hosts, disks, backups, certs -->
Only two metrics are supported, and only for containers on the local host, because the rules evaluate the local metrics history. Host, disk, backup, certificate and container-state events can't be alerted on.

## Rule fields

Create a rule with **New rule** (needs `alerts.create`).

| Field | Values |
|---|---|
| **Rule name** | Free text |
| **Severity** | `info`, `warning`, `critical` |
| Metric | **CPU %** (`cpu_percent`) or **Memory %** (`mem_percent`, used / limit) |
| Operator | **is greater than** (`gt`) or **is less than** (`lt`) |
| Threshold | Percent |
| Duration | 0 s to 1 h |
| **Container filter** | `*` for all containers, or one exact container name |
| **Cooldown** | 1 min to 24 h |
| **Notify these channels** | One or more channels |
| **Mute until (optional)** | A point in time |

Rules are evaluated every 30 seconds. When a rule fires or resolves, Dockmesh writes an entry to the history, sends a message to the selected channels and posts a bell notification on fire. The four built-in rules (CPU and memory, warning and critical) can be edited and disabled, but not deleted.

## Known limitations

<!-- update after: alerts: fix duration gate, cooldown and mute handling -->
The duration check doesn't work as intended. A rule with a duration of 0 never fires. A rule with a longer duration only fires when a metric sample happens to land exactly on the edge of the window, and then tends to resolve and fire again. The built-in rules are effectively silent. Don't rely on these rules as your only alerting. Scrape [Prometheus metrics](./monitoring.md) or use an external monitor as well.

<!-- update after: alerts: fix duration gate, cooldown and mute handling -->
**Cooldown** and **Mute until** are saved but not applied: a rule notifies again on every new breach, and a muted rule still sends. The fire count from `GET /api/v1/alerts/rules/{id}/stats` is always 0. A weekly quiet-hours schedule (`mute_schedule`, API only) is applied when a rule fires, but not when it resolves.

See also [Notifications](./notifications.md) and [Monitoring](./monitoring.md).
