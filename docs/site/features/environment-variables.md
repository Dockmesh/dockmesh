---
title: Environment variables
description: How a stack's .env and the server-wide global variables are combined, where they apply, who can read them and how they are stored.
---

Dockmesh has two sources of variables for `${VAR}` substitution in `compose.yaml`:

- the stack's own `.env`, edited on the stack's **Environment** tab;
- **global variables**, edited under **Settings → Global environment** and shared by all stacks.

Both are used only to fill in `${VAR}` references when the compose file is parsed. They are not passed into containers by themselves. To give a container a variable, reference it in the service:

```yaml
services:
  app:
    image: ghcr.io/acme/app:1.4.2
    environment:
      TZ: ${TZ}
      DATABASE_URL: postgres://app:${DB_PASSWORD}@db:5432/app
```

Compose defaults work as usual, for example `${LOG_LEVEL:-info}`.

## The stack .env

Open a stack and go to the **Environment** tab. Each row is a `KEY=value` line; **Add variable** adds one, **Save** writes the file. The **Used by** column shows which services reference the key. Keys whose name contains `password`, `secret`, `token`, `api_key`, `credential` or `access_key` are masked until you click **Show secrets**.

You can also edit the file on the server, as long as encryption is off (see [Storage and encryption](#storage-and-encryption)). Dockmesh notices the change and asks you to confirm it at the next deploy.

## Global variables

**Settings → Global environment** (needs `system.update`) holds variables for all stacks, grouped by a free-form **Group**.

- **Add variable** creates one. The form upper-cases the key and strips characters other than `A-Z`, `0-9` and `_`. Tick **Secret** to store the value encrypted (see below).
- **Templates** adds common sets with one click: System (`TZ`, `PUID`, `PGID`, `UMASK`), Database, SMTP and Proxy. Adjust the values afterwards; `TZ` defaults to `Europe/Vienna`.
- **Bulk import** takes the contents of a `.env` file and creates one variable per line in the group you name (lower-cased, `misc` when empty). Comments, blank lines and keys that already exist are skipped. Keys containing `password`, `secret`, `key` or `token` are ticked as secrets in the preview; change each tick before you import.

Deleting a variable that stacks still reference is not blocked, and those stacks then deploy with an empty value. The page does not show references; the API does (`refs_count` in the list, and `GET /api/v1/global-env/{id}/refs` for the stack names), so check there before you remove a shared variable.

### Secret global variables

A variable marked secret is stored encrypted with the server's secrets key, and its value is never returned by the API: the list shows **Hidden**, and editing it with the value field left empty keeps the stored value. A secret can't be turned back into a plain variable; delete it and create it again. Variables that were marked encrypted by an older version and still held plaintext are encrypted at the first start of this version.

If the stored value can't be decrypted (a different secrets key, or encryption turned off), the list shows **Can't decrypt** and every deploy that applies global variables fails until you enter the value again.

## Precedence

When both define the same key, the stack's `.env` wins over the global variable. Dockmesh puts the global variables first and the stack's lines after them, and the last definition of a key counts.

## Where global variables apply

| Action | Stack `.env` | Global variables |
|---|---|---|
| **Deploy** from the stack page, the API or `dmctl` | yes | yes |
| Deploy with an [environment override](../concepts/stacks.md#environment-overrides) | yes | yes |
| Prerequisite deployed through a [dependency](./stacks.md#dependencies-between-stacks) | yes | yes |
| **Roll back** | yes | yes |
| Template deploy | yes | yes |
| Git auto-deploy | yes | yes |
| Stack migration | yes | yes |
| Scale, rolling update, auto-scaling | yes | no |

Where global variables do not apply, a `${VAR}` that only exists globally resolves to an empty string. Scaled replicas and rolling updates of a service that depends on a global variable get that empty value; see [Scaling](./scaling.md).

## Who can read the values

| What | Who sees the values |
|---|---|
| A stack's `.env` (API, **Environment** tab, `dmctl stacks get`) | Users with `stacks.deploy` on that stack, or with the **See environment values** extra (`containers.env`) on it. Everyone else with `stacks.view` sees the stack, but `.env` is hidden. |
| Plain global variables | Users with `system.update`, which only an assignment on everything grants. |
| Secret global variables | Nobody through the API; the value only reaches deploys. |

The same rule as for the stack `.env` applies to environment values in container inspect. See [Permissions](../concepts/permissions.md).

## Storage and encryption

The stack `.env` is encrypted at rest when secrets encryption is on (`DOCKMESH_SECRETS_ENCRYPT_ENV`, default `true`): Dockmesh stores it as `.env.age`, encrypted with the age key `secrets.age-key` in the data directory. With encryption off it is a plaintext `.env` with mode `0600`.

Secret global variables are encrypted with the same key. With encryption off they are stored in plaintext in the database, still marked secret and still hidden from the API. Plain global variables are always stored in plaintext in the database.

The values exist in plaintext in these places as well:

- the temporary staging directory during a deploy (removed afterwards); on an agent host the staged `.env` includes the global variables;
- the copy of the stack an agent keeps on a remote host (`.env`, mode `0640`, the stack's own lines only);
- `compose.yaml` itself, for values generated by [templates](./templates.md), and therefore also the deploy history entry of a template deploy.

Deploy history of a deploy with an environment override keeps the base `compose.yaml` and the name of the override, not the merged file, so `.env` values don't end up in history. A rollback to such an entry applies the override again. History entries of override deploys made before v0.5.0 lost their compose snapshot during the upgrade, because it held `.env` values, and can no longer be rolled back to.

Back up `secrets.age-key`. Without it, every `.env.age` and every secret global variable is unreadable; see [Backups](../operations/backups.md). `dockmesh secrets rotate` and **Rotate key…** under **Settings → Encryption key** re-encrypt both with a new key.

## API

| Method and path | Permission |
|---|---|
| `GET`, `POST /api/v1/global-env` | `system.update` |
| `PUT`, `DELETE /api/v1/global-env/{id}` | `system.update` |
| `GET /api/v1/global-env/groups` | `system.update` |
| `GET /api/v1/global-env/{id}/refs` | `system.update` |

A variable is `{"key": "...", "value": "...", "group_name": "...", "encrypted": true}`; `encrypted` marks it secret. The stack `.env` is part of the stack and is written with `PUT /api/v1/stacks/{name}` (`stacks.update`). See [REST API](../reference/api.md).
