---
title: Audit log
description: What Dockmesh records in its audit log, how the hash chain and verification work, retention, the webhook and CSV export.
---

Dockmesh writes security-relevant actions to an append-only audit log in its SQLite database. The **Audit log** page shows the entries grouped by day, with filters and search. Reading the log needs `audit.view`. Changing retention and the webhook needs `audit.write`.

## What's recorded

Each entry has a timestamp, the user, an action, a target and a JSON details field. Recorded actions include:

| Area | Actions |
|---|---|
| Sign-in | `auth.login`, `auth.login_failed` |
| Users and roles | `user.create`, `user.update`, `user.delete`, `user.password`, `role.create`, `role.update`, `role.delete` |
| Stacks | `stack.create`, `stack.update`, `stack.delete`, `stack.deploy`, `stack.stop`, `stack.adopt` |
| Containers | `container.start`, `container.stop`, `container.restart`, `container.remove`, `container.update`, `container.rollback`, `container.exec_start`, `container.exec_end`, `container.browse`, `container.read_file`, `container.write_file` |
| Images, volumes, networks | `image.pull`, `image.remove`, `image.prune`, `image.scan`, `volume.create`, `volume.remove`, `volume.prune`, `volume.browse`, `volume.read_file`, `network.create`, `network.remove` |
| Access control | `auth.scope_denied` (a request blocked by a role or user scope) |
| Log itself | `audit.genesis`, `audit.chain_bridge` |

Other handlers write their own action names, for example `proxy.enable` and `proxy.disable`. Some operations are logged under a broader action: a host drain appears as `stack.deploy` with `"action": "drain"` in its details, and backup actions use stack and container action names.

## Hash chain

Every entry stores the hash of the previous entry and its own hash, SHA-256 over the previous hash, timestamp, user, action, target and details. Changing or deleting an entry in the middle breaks the chain from that point on.

The first entry's previous hash is the genesis value, also stored in `audit-genesis.sha256` in the data directory (path set by `DOCKMESH_AUDIT_GENESIS_PATH`).

Click **Verify chain** to recompute the chain (`GET /api/v1/audit/verify`). The check stops at the first broken link and shows it (**Tamper detected**), or reports the chain intact, and earlier runs are listed under **Verify history**. Entries written before the chain existed are reported as warnings and skipped.

Verification starts from the first chained entry in the table. It doesn't check that entry against the genesis file, so someone who can rewrite the whole table can rebuild a valid chain. To make the log tamper-evident in practice, copy `audit-genesis.sha256` somewhere the server can't write, and forward entries to an external system with the webhook below.

## Retention

Set under **Retention policy** on the **Audit log** page:

| Option | Behaviour |
|---|---|
| **Keep forever** | Nothing is pruned (default) |
| **Prune after N days** | Older entries are deleted |
| **Archive after N days** | Older entries are written to an NDJSON file in a local directory (the page pre-fills `/var/lib/dockmesh/audit-archive`; without a directory the server uses `./data/audit-archive`), then deleted |
| **Archive to backup target** | Older entries are written to a saved backup target, then deleted |

Retention runs daily at 03:00 server time, or right away with the run-now button. The page shows how many entries would be pruned. After pruning, Dockmesh writes an `audit.chain_bridge` entry so the remaining chain still verifies.

## Webhook

Under **Webhook receiver**, set a URL. Dockmesh then POSTs every new entry as JSON (`id`, `ts`, `user_id`, `username`, `action`, `target`, `details`, `row_hash`).

- **Forward only** limits forwarding to certain actions. `stack` matches only the action `stack`, `stack.*` matches `stack` and every `stack.<x>` action, and any other trailing `*` matches by prefix.
- If you set a secret, each request carries `X-Audit-Signature: sha256=<hex>`, an HMAC-SHA256 of the request body with that secret. Check it on the receiver.
- Each delivery is tried up to 5 times in total, with backoff starting at 500 ms, with a 10-second timeout per attempt. The queue holds 256 entries. When it's full, the oldest are dropped.
- Use the test button to send a sample entry.

The secret is stored unencrypted in the database. Use a dedicated secret for this webhook.

## Export

**Export CSV** downloads the entries currently loaded on the page, according to the active filters and the row limit (100, 500 or 1000). It runs in the browser. There's no server-side export of the whole log. For a complete copy, use the archive retention modes or the webhook.

See also [Authentication](./authentication.md) and [Hardening](../operations/hardening.md).
