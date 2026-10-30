---
title: Notifications
description: Alert channels for external services, the in-app notification feed, and the action-required banners.
---

Dockmesh has three separate ways of telling you something:

| Mechanism | Where it shows | What feeds it |
|---|---|---|
| Channels | Slack, email, ntfy and other external services | [Alert rules](./alerts.md) only |
| Notification feed | The bell in the top bar | Alerts, backups, scans, updates, migrations and more |
| Notices | A banner at the top of the app | Conditions that need an admin to act |

## Channels

Channels are managed on **Alerts** → **Channels**. Click **New channel**, pick a type and fill in its fields. A channel receives messages only from the alert rules that select it, both when a rule fires and when it resolves. Backups, scans and other events don't go to channels.

| Type | Config fields (required in bold) |
|---|---|
| `webhook` | **`url`** |
| `ntfy` | **`url`** (topic URL), `token`, `priority` (1–5; unset follows the level: 5 critical, 4 warning, 3 otherwise) |
| `discord` | **`url`** (webhook URL) |
| `slack` | **`url`** (incoming webhook URL) |
| `teams` | **`url`** (webhook URL) |
| `gotify` | **`url`**, **`token`** (application token) |
| `email` | **`host`**, `port` (default 587), `username`, `password`, **`from`**, **`to`** (list) |
| `pagerduty` | **`integration_key`**, `client`, `client_url` |
| `pushover` | **`app_token`**, **`user_key`**, `device`, `sound` |

The generic webhook POSTs JSON with `title`, `body`, `level`, `container`, `metric`, `value`, `threshold` and `timestamp` (Unix seconds). Use it to feed Telegram bots, n8n, Home Assistant and similar tools.

Use **Send test notification** on a channel's row to check it. Each channel can be disabled, and it can carry a weekly quiet-hours schedule (`mute_schedule`, API only) during which it's skipped:

```json
{"type":"weekly","ranges":[{"days":[1,2,3,4,5],"from":"22:00","to":"06:00"}]}
```

<!-- update after: alerts: fix duration gate, cooldown and mute handling -->
Messages are sent once. A failed delivery isn't retried and isn't recorded anywhere except the server log. HTTP channels time out after 10 seconds, SMTP has no timeout.

Managing channels needs `alerts.create`, `alerts.update` and `alerts.delete`. Anyone with `alerts.view` can read the channel list, and the API returns each channel's config, including tokens and passwords, as stored. Give `alerts.view` only to people you'd trust with those credentials.

## Notification feed

The bell in the top bar shows a per-user feed with an unread count. You can mark entries read, mark all read, or delete them. The feed is always available to signed-in users, without extra permissions.

Entries come from these producers:

| Kind | When |
|---|---|
| `alert.fire` | An alert rule fires |
| `backup.ok`, `backup.fail` | A backup run finishes |
| `cve.found` | A vulnerability scan finds critical or high findings |
| `image.update` | A running image has a newer version upstream |
| `agent.offline` | An agent disconnects |
| `migration.complete`, `migration.failed` | A stack migration ends |
| `system.upgrade` | A newer Dockmesh release is available |
| `invite.accepted` | Someone accepts an invite |

Most events are sent to every active user, not only to users who have access to the affected stack or host. Old entries aren't pruned.

## Notices

Notices are banners for conditions that need someone to act. Dockmesh raises one while the condition holds and removes it once it's fixed. Each notice targets an audience: everyone, admins (users with `system.update`), specific users, or holders of a permission. Severity is info, warning or critical. Info and warning notices can be hidden. Critical notices stay until resolved.

Notices:

| Key | Audience | Raised when |
|---|---|---|
| `api-tokens.legacy` | Admins | Active API tokens exist that were created before tokens had an owner. See [API tokens](./api-tokens.md#legacy-tokens). |

Notices are checked at startup and whenever the condition changes. The API is `GET /api/v1/notices` and `POST /api/v1/notices/{id}/dismiss`.

See also [Alerts](./alerts.md).
