---
title: Audit log
description: What Dockmesh records in its audit log, the categories, how the hash chain and verification work, retention, the webhook and CSV export.
---

Dockmesh writes security-relevant actions to an append-only audit log in its SQLite database. The **Audit log** page under **Admin** in the sidebar shows the entries with category tabs, a user filter and search; click an entry to see its details as JSON. Reading the log needs Settings & audit: View on everything. Changing retention and the webhook needs Settings & audit: Manage on everything.

## What's recorded

Each entry has a timestamp, the user, an action, a target and a JSON details field. Entries written by a request with an [API token](./api-tokens.md) name the token's owner and carry `api_token_id` in the details. Entries the server writes on its own have no user and show as system.

The log records changes and security events, not every read. Listing stacks, containers or logs isn't recorded; reading data that can hold secrets is: opening a terminal, browsing and reading container and volume files, downloading backups and exporting the backup key.

Every action belongs to one of four categories. The page shows them as tabs and opens on **Changes**:

| Category | API value | What it holds | Examples |
|---|---|---|---|
| **Changes** | `changes` | something was created, changed, deployed or removed | `stack.deploy`, `stack.update`, `container.restart`, `container.write_file`, `image.pull`, `volume.remove`, `backup.run`, `backup.restore`, `backup.job_create`, `agent.create`, `host.drain`, `proxy.route_update`, `alerts.rule_create`, `notify.channel_update`, `registry.update`, `template.deploy`, `user.create`, `user.invite_created` |
| **Access** | `access` | data was read, or who may access what changed | `container.exec_start`, `container.browse`, `container.read_file`, `volume.browse`, `backup.download`, `backup.key_export`, `role.create`, `role.update`, `assignment.add`, `assignment.remove`, `api_token.create` |
| **Sign-in & security** | `auth` | sign-in, sessions, tokens, 2FA, identity providers, denied requests | `auth.login`, `auth.login_failed`, `auth.lockout`, `auth.sso_login`, `auth.session_revoke`, `auth.policy_update`, `auth.scope_denied`, `api_token.revoke`, `user.password`, `user.unlock`, `user.mfa_enroll`, `user.mfa_reset`, `oidc.provider_update`, `auth.sso_rekey` |
| **System** | `system` | the server itself and the audit trail | `audit.genesis`, `audit.chain_bridge`, `audit.retention_configure`, `audit.webhook_configure`, `secrets.rotate`, `system.update_check`, `system.update_install`, `system.update_applied`, `system.update_refused`, `system.update_rolled_back`, `system.update_failed`, `system.restore_apply` |

Actions the server doesn't know, for example ones a newer or older version wrote, count as system. `auth.scope_denied` is a request that a user's assignments didn't allow.

## Filter through the API

`GET /api/v1/audit` returns the newest entries first. Each entry carries its `category`.

| Parameter | Effect |
|---|---|
| `category` | one of `changes`, `access`, `auth`, `system`; anything else is rejected with 400 |
| `action` | actions starting with this text, for example `stack.` |
| `user_id` | entries of one user |
| `limit` | number of entries, up to 1000; other values give 100 |

```bash
curl -H "Authorization: Bearer dmt_…" \
  "https://dockmesh.example.com/api/v1/audit?category=access&limit=500"
```

## Hash chain

Every entry stores the hash of the previous entry and its own hash, SHA-256 over the previous hash, timestamp, user, action, target and details. Changing or deleting an entry in the middle breaks the chain from that point on.

The first entry's previous hash is the genesis value, stored in `audit-genesis.sha256` (path set by `DOCKMESH_AUDIT_GENESIS_PATH`, default `<data root>/data/audit-genesis.sha256`). The server writes it once with mode `0400` and logs it at first start.

Click **Verify chain** to recompute the chain (`GET /api/v1/audit/verify`). The check walks every entry in order, stops at the first broken link and shows it, or reports the chain as intact. Entries written before the chain existed are reported as warnings and skipped.

Verification starts from the first chained entry in the table. It doesn't check that entry against the genesis file, so someone who can rewrite the whole table can rebuild a valid chain. To make the log tamper-evident in practice, copy `audit-genesis.sha256` somewhere the server can't write, and forward entries to an external system with the webhook below.

## Retention

Click **Audit settings** on the **Audit log** page and open the **Retention** tab:

| Option | Behaviour |
|---|---|
| **Keep forever** | Nothing is removed (default) |
| **Remove old entries** | Entries older than **Older than (days)** are deleted |
| **Archive to a folder** | Older entries are written to an NDJSON file in a folder on the server, then deleted. The page pre-fills `audit-archive` in the server's data directory (`/var/lib/dockmesh/audit-archive` by default), which is also where the server writes when no folder is set |
| **Archive to a backup target** | Older entries are written to a saved backup target as NDJSON, then deleted |

The server's systemd unit makes the folder writable for the service, so choosing a folder other than the default needs the permission to install Dockmesh updates. It must be an absolute path outside the system directories (`/etc`, `/usr` and the like) that doesn't contain the data directory. Run `sudo dockmesh system write-unit` and restart the service after changing it.

Up to v0.5.3 the server wrote to `data/audit-archive` below its working directory when no folder was set (`/data/audit-archive` for the systemd service). A server that finds that folder keeps archiving there, so old and new archives stay together.

Retention runs daily at 03:00 server time, or right away with **Run now…**. The tab shows how many entries the next run would remove. After removing entries, Dockmesh writes an `audit.chain_bridge` entry so the remaining chain still verifies. The backup target the log is archived to is hidden from non-admins, and only admins can change, delete or pick it for a job.

## Webhook

On the **Forwarding** tab of **Audit settings**, turn on **Send every new entry to a webhook (SIEM)** and set a URL. Dockmesh then POSTs every new entry as JSON with `id`, `ts`, `action`, `row_hash` and, when set, `user_id`, `target` and `details`.

- **Only these actions** limits forwarding. Empty sends everything. `stack` matches only the action `stack`, `stack.*` matches `stack` and every `stack.<x>` action, and any other trailing `*` matches by prefix.
- With a **Signing secret**, each request carries `X-Audit-Signature: sha256=<hex>`, an HMAC-SHA256 of the request body with that secret. Check it on the receiver.
- Each delivery is tried up to 5 times, with backoff starting at 500 ms and doubling, and a 10-second timeout per attempt. The queue holds 256 entries. When it's full, the oldest are dropped. Deliveries that still fail are only logged on the server.
- **Send test** sends a sample entry.

The secret is stored unencrypted in the database. Use a dedicated secret for this webhook.

## Export

**Export CSV** downloads the entries currently shown on the page: the loaded entries (the newest 100, 500 or 1000, default 500), narrowed by the active category tab, user filter and search. It runs in the browser. There's no server-side export of the whole log. For a complete copy, use the archive retention options or the webhook. The API returns at most the newest 1000 entries per filter.

See also [Authentication](./authentication.md) and [Security hardening](../operations/hardening.md).
