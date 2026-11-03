---
title: Alerts
description: Alert rules on container metrics and events, hosts, disks, backups and certificates, with scoping, grouping, cooldown and mute.
---

Alert rules watch containers, hosts, disks, backups and proxy certificates and notify [channels](./notifications.md) when something happens. They're configured under **Alerts** in the sidebar, which has the tabs **Rules**, **Channels** and **History**.

## Rule types

Click **New rule** and pick what to **Watch**. The dialog adapts to the type and shows in one sentence when the rule fires.

| Group | Type | Fires when | Settings |
|---|---|---|---|
| Containers | **Container CPU / memory** | CPU or memory is above or below a threshold for at least a duration | Metric, comparison, percent, CPU unit, duration (immediately to 1 h) |
| Containers | **Container restart loop** | A container restarts more than *n* times within a window | Restarts (at least 2), window (1 min to 1 h) |
| Containers | **Container stopped unexpectedly** | A container exits with a non-zero code; stops through Dockmesh or `docker stop` don't count | — |
| Containers | **Container unhealthy** | A container's health check reports unhealthy | — |
| Containers | **Container killed by OOM** | A container is killed for running out of memory | — |
| Hosts | **Host offline** | An agent is offline for at least a duration | Duration |
| Hosts | **Disk usage** | A mount is at least *n* % full | Percent |
| Backups | **Backup run failed** | A backup run fails | — |
| Backups | **Backup overdue** | A backup job has no successful run within its schedule (one interval plus a grace of a quarter interval, at least 15 minutes) | — |
| Reverse proxy | **TLS certificate expiring** | A proxy site's certificate expires within *n* days | Days |

Each type has a filter field: `*` for all, or one exact container name, agent name, mount point, backup job name or proxy domain. For **Disk usage**, `*` covers the mounts holding Docker, containerd, stacks or Dockmesh data. The type of an existing rule can't be changed.

Every rule also has a **Severity** (**Info**, **Warning**, **Critical**), an optional name, the channels to **Notify**, and a repeat interval (the cooldown, below).

### CPU unit

A CPU rule states its unit:

- **% of host**: the share of the whole host, as the charts show it. The default for new rules.
- **% of one core**: 100 per busy core, as `docker stats` shows it.

On a 4-core host, 80 % of one core is 20 % of the host. Rules created before the unit existed are labelled **% of one core** and behave as before; edit a rule to switch it. Memory is used memory as a share of the container's limit. The API field is `cpu_unit` (`host` or `core`); API clients that leave it out get `host`.

### Scope

**Applies to** limits a rule to one host (**Containers on** *host*), to hosts with a tag (**Hosts with tag** *tag*) or to one stack (**Stack** *name*). Without a scope the rule covers every host, the server and every agent. CPU and memory rules can be limited to a host or a host tag, not to a stack. Backup and certificate rules belong to no host.

Container event rules on agent hosts need an agent that streams Docker events; older agents are listed in an admin notice. CPU and memory rules read the stored metrics history, which agents send to the server every minute. Agents that don't record history yet aren't covered. See [Monitoring](./monitoring.md#metrics-history).

## Built-in rules

Dockmesh ships these rules. They can be edited and disabled, but not deleted. They have no channel, so until you attach one they only record history and post a bell notification.

| Rule | Default | On |
|---|---|---|
| Container CPU > 90% (sustained) | > 90 % of one core for 5 min, warning | yes |
| Container CPU > 95% (critical) | > 95 % of one core for 15 min, critical | yes |
| Container memory > 90% | > 90 % for 5 min, warning | yes |
| Container memory > 98% (near-OOM) | > 98 % for 1 min, critical | yes |
| Container restart loop | more than 3 restarts within 10 min, warning | yes |
| Container killed by OOM | critical | yes |
| Container stopped unexpectedly | warning | no |
| Container unhealthy | warning | no |
| Host offline | offline for 5 min, critical | yes |
| Disk usage above 90% | 90 % on Docker-related mounts, warning | yes |
| Backup run failed | critical | yes |
| Backup overdue | warning | yes |
| TLS certificate expiring | within 14 days, warning | yes |

Caddy renews certificates 30 days before expiry, so the certificate rule only fires when renewal keeps failing.

## How rules fire

CPU and memory rules are evaluated every 30 seconds against the metrics history. A rule fires once the breach has lasted for its duration, with one missed sample tolerated, and resolves only after the value has been back to normal for a full sampling interval, so a single good sample doesn't make it flap. Event rules fire when the event arrives. Disk usage resolves once the mount is 2 points below the threshold. OOM kills and failed backup runs fire once and have nothing to resolve.

When a rule fires or resolves, Dockmesh writes an entry to the history and sends a message to the rule's channels. A fire also posts a bell notification to users who can see alert history on that host.

- **Grouping**: when a rule fires for many targets on one host within a few seconds, the channels and the bell get one message ("12 containers on dm-agent1") instead of one per target. The resolved message is grouped the same way.
- **Cooldown**: after a fire is announced, the same target stays quiet for the repeat interval (1 min to 24 h, default 5 min). Fires inside it are recorded but not sent. Repeated OOM kills and failed backup runs inside the cooldown are counted on the earlier history entry instead of adding new ones.
- **Mute**: **Mute for 1 hour** in a rule's menu or **Mute until** in the edit dialog. A muted rule keeps evaluating and recording, but notifies no channel, also not with "resolved". A weekly quiet-hours schedule (`mute_schedule`, API only) works the same way. Pausing a rule with its switch stops evaluation altogether.

History entries that went out silently are marked **silenced**. `GET /api/v1/alerts/rules/{id}/stats` counts a rule's fires, repeats included.

## History

The **History** tab lists fired and resolved alerts, with a filter and **Export CSV**. Entries are pruned after 30 days; an alert that is still active keeps its entry. A rule tracks at most 500 targets per host.

## Permissions

| Action | Permission |
|---|---|
| Open the Alerts page, read rules and channels | `alerts.view` |
| Create, duplicate | `alerts.create` |
| Edit, pause, mute, test channels | `alerts.update` |
| Delete | `alerts.delete` |
| Read the alert history | `alerts.history` |

Rules and channels are instance-wide: `alerts.view`, `alerts.create`, `alerts.update` and `alerts.delete` only count from an assignment on everything. `alerts.history` can be scoped: a user limited to some hosts sees the history entries and bell notifications of those hosts, but not those of backup and certificate rules, which belong to no host. See [Permissions](../concepts/permissions.md).

See also [Notifications](./notifications.md) and [Monitoring](./monitoring.md).
