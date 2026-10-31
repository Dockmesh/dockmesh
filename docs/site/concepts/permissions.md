---
title: Permissions
description: How Dockmesh's role-based access control works, what each built-in role can do, how scopes narrow access, and where the model has gaps you should plan around.
---

Every user has exactly one role. A role is a set of permissions, and a permission allows one kind of action on one kind of resource. Scopes can additionally narrow a user or role to certain hosts or stacks. RBAC, custom roles and scopes are all part of the open-source build.

Manage users and roles under **Users & Roles**, which has a **Users** tab and a **Roles** tab.

## Permissions

Permissions are written `domain.verb`, for example `stacks.deploy` or `containers.exec`. There are 71 permissions in 17 domains:

| Domain | Permissions |
|---|---|
| `containers` | `view`, `update` (start, stop, restart, pause, kill, remove, one-click image update and rollback), `delete` (not checked), `exec`, `logs` |
| `stacks` | `view`, `create`, `update` (edit files), `delete`, `deploy` (deploy, stop, scale, rollback, deploy history, dependencies, environment overrides, scaling rules, template deploy), `migrate`, `adopt` |
| `volumes` | `view`, `create`, `delete`, `browse`, `read_file` |
| `networks` | `view`, `create`, `delete` |
| `images` | `view`, `create` (pull), `delete`, `scan` |
| `registries` | `view`, `create`, `update`, `delete` |
| `hosts` | `view`, `create` (add a host), `update` (drain, upgrade, re-issue enrollment token, agent upgrade policy), `delete` (revoke), `tag` |
| `users` | `view`, `create`, `update`, `delete`, `password_reset`, `suspend` |
| `roles` | `view`, `create`, `update`, `delete` |
| `tokens` | `view`, `create`, `delete`, `manage_others` |
| `backups` | `view`, `create`, `update`, `delete`, `restore` |
| `proxy` | `view`, `create`, `update`, `delete` |
| `alerts` | `view`, `create`, `update`, `delete` |
| `templates` | `view`, `create`, `update`, `delete` |
| `audit` | `view`, `export`, `write` |
| `system` | `view`, `update` (settings, global environment, sign-in policy, SSO), `upgrade` |
| `metrics` | `view` (the Prometheus `/metrics` endpoint) |

`GET /api/v1/roles/permissions` returns the full list with descriptions; the role editor builds its permission matrix from it.

Six permissions appear in the matrix but are not checked by any endpoint: `audit.export`, `users.password_reset`, `users.suspend`, `volumes.read_file`, `containers.delete` and `templates.view`. Reading a file inside a volume is covered by `volumes.browse`, removing a container by `containers.update`, and any signed-in user can read templates. Resetting another user's password requires the built-in `admin` role, whatever permissions a custom role holds.

## Built-in roles

Dockmesh ships five roles, from least to most privileged: `viewer`, `operator`, `deployer`, `host-admin` and `admin`. They cannot be edited or deleted. Select a role in the **Roles** tab to see its exact permissions.

<!-- update after: rbac: scope read endpoints, limit grants, gate sensitive downloads -->
| Domain | viewer | operator | deployer | host-admin | admin |
|---|---|---|---|---|---|
| containers | view | view, update, exec, logs | all | all | all |
| stacks | view | view, deploy | all except migrate | all | all |
| volumes | view | view | view, create | all | all |
| networks | view | view | view, create | all | all |
| images | view | view, scan | view, create, scan | all | all |
| registries | view | view | view | all | all |
| hosts | view | view | view | all | all |
| users, roles | view | view | view | view | all |
| tokens (own) | view | view, delete | view, create, delete | view, create, delete | all, incl. `manage_others` |
| backups | view | view | view | all | all |
| proxy, alerts | view | view | view | all | all |
| templates | view | view | view, create, update | all | all |
| audit | view | view | view | view, export | all |
| system | view | view | view | view, update | all |
| metrics | view | view | view | view | view |

Viewers read everything, operators run existing stacks and containers, deployers also write compose files and pull images, host-admins run the whole fleet and its integrations, and only admins manage users, roles, audit settings and secrets-key rotation, and run an on-demand update check. Host-admins can still turn automatic update checks off or change their interval, because those are ordinary settings.

<!-- update after: rbac: scope read endpoints, limit grants, gate sensitive downloads -->
> **Warning:** `host-admin` holds `system.update` and `backups.restore`. Together these let a host-admin change sign-in and SSO settings and export the secrets encryption key. Give `host-admin` only to people you would trust as admins.

New local users get the role you pick when you create or invite them. Users created through SSO get the role from the provider's role settings. See [SSO](../features/sso.md).

<!-- update after: sso: link by email only when verified and allowed, keep manual roles -->
SSO sign-ins set the user's role from the provider mapping on every login, so a role you change by hand for an SSO user is overwritten at their next sign-in.

## Custom roles

In the **Roles** tab, click **New** to open **New custom role**. Give it a **Role name**, a **Display name**, an optional description, a permission set from the matrix and optional scopes. Custom roles appear in the role picker for users and invites.

A custom role is the way to give someone less than a built-in role offers, for example a deployer without `containers.exec`, or a viewer without `backups.view`.

## Scopes

Permissions say what a user can do; scopes say where. There are two layers, and a request has to pass both.

**Role scopes** are set on a custom role. Each entry is one of:

| Type | Value | Matches |
|---|---|---|
| `host` | a host ID (`local` or an agent ID) | requests on that host |
| `stack` | a stack name | requests on that stack |
| `host_tag` | a host tag | requests on any host carrying the tag |

A role with several entries matches if any entry matches. A role with no entries is unscoped; all built-in roles are unscoped. A request that carries no stack name (pulling an image, creating a volume, browsing a volume) cannot match a `stack` entry, so a role scoped only to stacks cannot do host-level actions.

**User scope tags** are set per user with **Edit role & scope** in the **Users** tab. They are plain host tags. An empty list means no extra restriction. With tags set, the user can act on a host only if the host carries at least one of them. Untagged hosts, including an untagged local host, are out of reach for that user.

Host tags are described in [Hosts and agents](./hosts-and-agents.md#host-tags). If you send a user update through the API, include `scope_tags`; omitting the field clears the user's scope.

### What scope restricts

<!-- update after: rbac: scope read endpoints, limit grants, gate sensitive downloads -->
Scope is enforced mainly on actions that change something on a specific host or stack: deploy, stop, edit and delete stacks, container lifecycle and removal, logs, exec and stats sessions, image pull, removal and scan, volume and network create, remove and prune, volume browsing, and migrations. The container, host and stack lists are also filtered.

Many reads are not scoped. A scoped user can still see, among other things, the stack detail including its decrypted `.env`, container inspect output including environment variables, image, volume and network lists, metrics, topology, the live event feed, and everything under backups. Scope checks are also missing on stack dependencies, environment overrides, git sources, scaling rules, host tag edits and template deploys. On remote hosts, container actions under a stack-only role scope are denied, because the stack label is looked up on the local Docker engine.

Treat scope as a guard against accidental changes outside a team's area, not as a confidentiality boundary. If teams must not see each other's secrets, run separate Dockmesh servers for them.

## Gaps to plan around

<!-- update after: rbac: scope read endpoints, limit grants, gate sensitive downloads -->
- **Backups:** `backups.view` lists jobs and runs and downloads archives decrypted. A system backup contains the database and the data directory, including the secrets key. Every built-in role, `viewer` included, has `backups.view`. For users you don't fully trust, use a custom role without it.
- **Granting roles:** anyone with `users.create` or `users.update` can assign any role, including `admin`, and anyone with `roles.update` can add any permission to a custom role. Treat these permissions as admin-equivalent. Nothing stops you from removing or demoting the last admin, so keep at least two.
- **Container files:** browsing and downloading files inside a container needs only `containers.view`.

## API tokens

API tokens act on behalf of the user who created them. A token has its own role, which you pick at creation and which must not exceed your own permissions. Each request must be allowed by the token's role and by the owner's current role, and the owner's current scope applies. If the owner is demoted, the token loses those permissions; if the owner is deleted, the token stops working. Tokens created before tokens had owners keep their fixed role and have no scope; Dockmesh shows admins a notice about them. See [API tokens](../features/api-tokens.md).

## UI and API

The UI hides or disables many actions you lack permission for, but not all of them. The server checks permissions on every request, so calling the API directly gives you no more than the UI would. Requests denied by a role scope are written to the audit log. See [Audit log](../features/audit-log.md).
