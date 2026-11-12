---
title: Permissions
description: How access works in Dockmesh - roles as a level per area plus extras, assignments of a role on a scope, the built-in roles, API tokens and identity provider groups.
---

Access in Dockmesh comes from **assignments**. An assignment gives a user a **role** on a **scope**: everything, one host, all hosts with a host tag, or one stack. A user can have several assignments; they add up, and none of them can take away what another gives. Roles, custom roles and assignments are part of the open-source build.

Manage them under **Users & roles**. The page has the tabs **Users**, **Roles** and **Invites**. Each user has a detail page whose **Access** tab lists the assignments, shows what the user can do where, and has an **Access check** that answers "Can this user do X on Y?" with the assignment that allows it.

## Roles

A role picks one level per area. Levels are cumulative: Deploy includes Operate and View.

| Area | Levels | What each level adds |
|---|---|---|
| Stacks | None, View, Operate, Deploy, Manage | View: stacks, services and compose, logs, templates. Operate: stop stacks, start, stop and restart their containers. Deploy: deploy and redeploy, edit compose and `.env`, roll back to an earlier deploy, deploy from a template. Manage: create and delete stacks, migrate to another host, adopt existing containers, connect git repositories, create, edit and delete templates |
| Containers | None, View, Operate, Manage | View: containers and their metrics, logs. Operate: start, stop, restart, pause. Manage: remove containers, update and roll back images |
| Images, volumes, networks & registries | None, View, Manage | View: images, volumes, networks, the registry list. Manage: pull, remove and prune, create volumes and networks, vulnerability scans, add, edit and remove registries |
| Hosts | None, View, Manage | View: hosts and host metrics. Manage: add and revoke hosts, edit host tags, drain hosts and upgrade agents |
| Proxy | None, View, Manage | View: routes and certificates. Manage: add, edit and delete routes, start and stop the proxy |
| Backups | None, View, Operate, Manage | View: jobs, runs and targets. Operate: run jobs now, restore a run. Manage: create and edit jobs and targets |
| Alerts | None, View, Manage | View: rules and alert history. Manage: rules and notification channels |
| Users & roles | None, View, Manage | View: users, roles and assignments. Manage: invite, edit and remove users, edit roles and assignments, revoke other users' API tokens |
| Settings & audit | None, Metrics, View, Manage | Metrics: the Prometheus `/metrics` scrape. View: also the audit log. Manage: instance settings, sign-in and identity providers, audit retention, export and webhook, update checks, rotating the encryption key, restoring a system backup |

On top of the levels, a role can hold **extras**. No level includes them:

| Extra | What it allows |
|---|---|
| **Open a terminal in containers** | A shell in containers. Together with the files extra: upload, edit and delete container files |
| **Browse and read files in containers and volumes** | The container **Files** tab and the volume browser, read-only |
| **See environment values (inspect, .env)** | Environment values in container inspect and a stack's `.env`. Stacks: Deploy shows a stack's `.env` too |
| **Download backup archives** | Download decrypted backup archives |
| **Export the backup encryption key** | Export the key that decrypts every backup. Only on an assignment on everything |

### Built-in roles

| Role | Levels | Extras |
|---|---|---|
| Admin | every area at its top level | all five |
| Host Admin | Stacks, Containers, Images/volumes/networks & registries, Hosts, Proxy, Backups, Alerts: Manage; Settings & audit: View | terminal, files, environment values, download backups |
| Deployer | Stacks: Deploy; Containers: Operate; Images/volumes/networks & registries, Hosts, Proxy, Backups, Alerts: View | none |
| Operator | Stacks, Containers: Operate; Images/volumes/networks & registries, Hosts, Backups, Alerts: View | terminal |
| Viewer | every area except Users & roles: View | none |

Built-in roles can't be changed. **Duplicate** one, or click **New role**, to make a custom role.

### Permission keys

Other pages, API error messages and `dmctl whoami -o json` name single permissions by key. Each key belongs to the lowest level or extra that grants it:

| Key | Granted by |
|---|---|
| `stacks.view`, `templates.view` | Stacks: View |
| `containers.logs` | Stacks: View or Containers: View |
| `stacks.operate` | Stacks: Operate |
| `containers.update` | Stacks: Operate or Containers: Operate |
| `stacks.deploy`, `stacks.update` | Stacks: Deploy |
| `stacks.create`, `stacks.delete`, `stacks.migrate`, `stacks.adopt`, `stacks.git`, `templates.create`, `templates.update`, `templates.delete` | Stacks: Manage |
| `containers.view` | Containers: View |
| `containers.delete`, `containers.upgrade` | Containers: Manage |
| `images.view`, `volumes.view`, `networks.view`, `registries.view` | Images, volumes, networks & registries: View |
| `images.create`, `images.delete`, `images.scan`, `volumes.create`, `volumes.delete`, `networks.create`, `networks.delete`, `registries.create`, `registries.update`, `registries.delete` | Images, volumes, networks & registries: Manage |
| `hosts.view` | Hosts: View |
| `hosts.create`, `hosts.update`, `hosts.delete`, `hosts.tag` | Hosts: Manage |
| `proxy.view` | Proxy: View |
| `proxy.create`, `proxy.update`, `proxy.delete` | Proxy: Manage |
| `backups.view` | Backups: View |
| `backups.run`, `backups.restore` | Backups: Operate |
| `backups.create`, `backups.update`, `backups.delete` | Backups: Manage |
| `alerts.view`, `alerts.history` | Alerts: View |
| `alerts.create`, `alerts.update`, `alerts.delete` | Alerts: Manage |
| `users.view`, `roles.view` | Users & roles: View |
| `users.create`, `users.update`, `users.delete`, `roles.create`, `roles.update`, `roles.delete`, `tokens.manage_others` | Users & roles: Manage |
| `metrics.view` | Settings & audit: Metrics |
| `audit.view` | Settings & audit: View |
| `audit.export`, `audit.write`, `system.update`, `system.upgrade` | Settings & audit: Manage |
| `containers.exec` | extra **Open a terminal in containers** |
| `containers.files`, `volumes.browse` | extra **Browse and read files in containers and volumes** |
| `containers.env` | extra **See environment values (inspect, .env)** |
| `backups.download` | extra **Download backup archives** |
| `backups.key_export` | extra **Export the backup encryption key** |
| `tokens.view`, `tokens.create`, `tokens.delete`, `system.view` | every signed-in user (own tokens, server status) |

## Scopes

| Scope | Covers |
|---|---|
| Everything | every host and stack, and the instance-wide areas |
| Host | one host, its containers, images, volumes, networks and stacks |
| Host tag | every host carrying the tag, now and later |
| Stack | one stack. Stack names are unique; a stack scope with a host covers the stack while it runs there, one without a host covers it anywhere, also before it is deployed |

Proxy, alert rules and notification channels, users and roles, settings, the audit log and the `/metrics` scrape are instance-wide. Only an assignment on **everything** grants them; a narrower assignment of the same role does not. Editing host tags, adding new hosts, adding, editing and removing registries, creating, editing and deleting templates, other users' API tokens and the backup key are instance-wide for the same reason. Alert history, the registry list and hosts follow the scope.

Changing the **Base URL** in Settings needs Users & roles: Manage on everything in addition to Settings & audit: Manage, because invite links and SSO callback URLs are built from it. Every change is written to the audit log with the old and the new address. Changing the **Agent public URL** needs Hosts: Manage on everything as well, because every install command is built from it.

A stack that exists only as files and was never deployed has no host yet. Only assignments on everything or on that stack without a host see it, change it or deploy it.

Every signed-in user can always see their own profile, sessions, 2FA and API tokens, server health and version, and receive live updates. With Users & roles: Manage you can also see and end another user's sessions on their **Sessions & 2FA** tab, as long as you could grant each of their assignments yourself. A page none of your roles allows shows a no-access message.

## Who can grant what

To add or remove an assignment you need Users & roles: Manage on everything, and you must hold every permission of the role everywhere the scope reaches. Instance-wide permissions don't count for a narrower scope, because the assignment can't grant them there. Roles follow the same rule on everything: you can only create, change or delete a role whose levels and extras you hold yourself on everything. Managing a user (editing, unlocking, sessions, turning off 2FA) needs that you could grant each of the user's current assignments.

The last assignment of an admin role on everything can't be removed, and such a role can't be narrowed while it is the last one. An admin role is one that can change instance settings and view, invite, edit and remove users.

## API tokens

A token acts with its owner's **current** access. When you create it you can restrict it further to a role on a scope; it never gets more than the owner has. See [API tokens](../features/api-tokens.md).

## Identity providers

Group mappings on an OIDC, SAML, LDAP or OAuth2 provider map a group to a role on a scope. At every sign-in Dockmesh replaces the user's identity provider assignments with one per matching group; with no match, the provider's **Default role** applies on everything. Assignments you add by hand stay. Saving a provider needs that you could grant its default role and every mapping yourself.

On the user's **Overview** tab, **Stop updating from groups** keeps sign-ins from changing the account's access; **Update from groups at sign-in** turns it back on.

If a sign-in would remove the last admin assignment on everything, Dockmesh keeps it and tells admins in a notice.

## Audit

Adding or removing an assignment, creating, changing or deleting a role, and creating an API token are recorded in the audit log under the **Access** category. See [Audit log](../features/audit-log.md).

## Upgrading from v0.4

The upgrade moves every user's role, the role's scopes and the user's host tags onto assignments that give the same access. Where that isn't possible, for example a scoped role combined with user host tags, the access is narrowed: such users get one assignment per host that matched at upgrade time. Custom roles get, per area, the highest level their old permissions fully covered. API tokens whose role was limited to more than one scope are revoked, because a token restriction holds one scope. Nothing gains access, except that the built-in roles follow the new definitions above. An admin notice lists every account, role and token that changed, with before and after.
