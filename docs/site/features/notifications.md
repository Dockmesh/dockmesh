---
title: Notifications
description: Alert channels for external services, the in-app notification feed and Activity page, and notices.
---

Dockmesh has three separate ways of telling you something:

| Mechanism | Where it shows | What feeds it |
|---|---|---|
| Channels | Slack, email, ntfy and other external services | [Alert rules](./alerts.md) only |
| Notification feed | The bell in the top bar and **Activity** → **Notifications** | Alerts, failed backups, scans, image updates, agents going offline, migrations and more |
| Notices | A banner at the top of the app, or **Activity** | Conditions that need someone to act |

## Channels

Channels are managed on **Alerts** → **Channels**. Click **New channel**, pick a type and fill in its fields. A channel receives messages only from the alert rules that select it, both when a rule fires and when it resolves. Backups, scans and other events don't go to channels.

| Type | Config fields (required in bold) |
|---|---|
| `webhook` (**Generic Webhook**) | **`url`** |
| `ntfy` | **`url`** (topic URL), `token`, `priority` (1–5; unset follows the level: 5 critical, 4 warning, 3 otherwise) |
| `discord` | **`url`** (webhook URL) |
| `slack` | **`url`** (incoming webhook URL) |
| `teams` | **`url`** (webhook URL) |
| `gotify` | **`url`**, **`token`** (application token) |
| `email` (**Email (SMTP)**) | **`host`**, `port` (default 587), `username`, `password`, **`from`**, **`to`** (list) |
| `pagerduty` | **`integration_key`** (Events v2 routing key), `client`, `client_url` |
| `pushover` | **`app_token`**, **`user_key`**, `device`, `sound` |

The generic webhook POSTs JSON with `title`, `body`, `level`, `container`, `metric`, `value`, `threshold` and `timestamp` (Unix seconds). Use it to feed Telegram bots, n8n, Home Assistant and similar tools.

### Language

Each channel has a **Language** (English or German) for the messages it sends; channels without one send in English. Email is sent per recipient: an address that belongs to a Dockmesh user gets the mail in the language that user picked under **Account**, every other address gets the channel's language. Recipients with different languages get separate mails.

### Testing and delivery

**Test** on a channel's row sends a test message. The **Last delivery** column shows when the channel last sent, or the error of the last failed delivery.

A failed delivery is retried twice (after 2 and 10 seconds) before Dockmesh gives up. HTTP channels time out after 10 seconds per attempt; SMTP has no timeout.

Each channel can be disabled, and it can carry a weekly quiet-hours schedule (`mute_schedule`, API only) during which it's skipped:

```json
{"type":"weekly","ranges":[{"days":[1,2,3,4,5],"from":"22:00","to":"06:00"}]}
```

### Permissions and secrets

Reading channels needs `alerts.view`, managing them needs `alerts.create`, `alerts.update` (also for **Test**) and `alerts.delete`, all from an assignment on everything. Secrets are never returned: tokens, passwords, keys and the webhook URLs of `webhook`, `ntfy`, `discord`, `slack`, `teams` and `gotify` come back empty with a `<field>_set: true` marker. Leave such a field empty when you edit the channel to keep the stored value.

## Notification feed

The bell in the top bar shows the latest notifications with an unread count; **Activity** → **Notifications** has the full history, together with finished operations and open notices. You can mark entries read, mark all read, or delete them.

Entries come from these producers. Each entry only reaches users who hold the listed permission on the affected host or stack:

| Kind | When | Who gets it |
|---|---|---|
| `alert.fire` | An alert rule fires | `alerts.history` on the host |
| `backup.fail` | A backup run fails | `backups.view` on the job's host; `system.update` on everything for the system backup |
| `backup.ok` | A backup run finishes with warnings (clean runs send nothing) | Same as `backup.fail` |
| `cve.found` | A vulnerability scan finds critical or high findings | `images.view` on the server host |
| `image.update` | A running image on the server host has a newer version upstream | `images.view` on the server host |
| `agent.offline` | An agent disconnects | `hosts.view` on that host |
| `migration.complete`, `migration.failed` | A stack migration ends | `stacks.view` on the stack |
| `deploy.ok`, `deploy.fail` | A git-triggered deploy ends | `stacks.view` on the stack |
| `system.upgrade` | A newer Dockmesh release is available | `system.upgrade` |
| `invite.accepted` | Someone accepts an invite | The user who created the invite |

Entries aren't pruned.

## Notices

Notices are raised for conditions that need someone to act. Dockmesh raises one while the condition holds and removes it once it's fixed. Each notice targets an audience: everyone, admins, specific users, or holders of a permission. Severity is info, warning or critical.

- Only notices about something that stopped working are shown as a banner. You can hide one for 24 hours; it comes back while the problem lasts.
- Warning and critical notices are listed under **Needs attention** on the overview and under **Activity**. A notice hidden from the banner moves there until it is resolved.
- Info notices explain a change and are listed under **Activity** → **Notifications**. They can be dismissed; critical notices that aren't in the banner stay until resolved.

Examples are legacy API tokens without an owner (see [API tokens](./api-tokens.md#legacy-tokens)), proxy sites that aren't served because of their upstream (see [Reverse proxy](./reverse-proxy.md#sites)) and agents too old to stream container events for alert rules (see [Alerts](./alerts.md#scope)).

Notices are checked at startup and whenever the condition changes. The API is `GET /api/v1/notices` and `POST /api/v1/notices/{id}/dismiss`.

See also [Alerts](./alerts.md) and [Monitoring](./monitoring.md).
