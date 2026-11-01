---
title: Permissions
description: How access works in Dockmesh - roles as a level per area plus extras, assignments of a role on a scope, the built-in roles, API tokens and identity provider groups.
---

Access in Dockmesh comes from **assignments**. An assignment gives a user a **role** on a **scope**: everything, one host, all hosts with a host tag, or one stack. A user can have several assignments; they add up, and none of them can take away what another gives. Roles, custom roles and assignments are part of the open-source build.

Manage them under **Users & roles**. Each user has a detail page with an **Access** tab that lists the assignments, shows what the user can do where, and answers "Can this user do X on Y?" with the assignment that allows it.

## Roles

A role picks one level per area. Levels are cumulative: Deploy includes Operate and View.

| Area | Levels | What the top levels add |
|---|---|---|
| Stacks | None, View, Operate, Deploy, Manage | View: stacks, compose, logs, templates. Operate: stop stacks, start/stop/restart their containers. Deploy: deploy, edit compose and `.env`, roll back, deploy templates. Manage: create, delete, migrate, adopt, connect git, manage templates |
| Containers | None, View, Operate, Manage | View: containers, metrics, logs. Operate: start, stop, restart, pause. Manage: remove, update and roll back images |
| Images, volumes, networks & registries | None, View, Manage | Manage: pull, remove, prune, create volumes and networks, scans, registry credentials |
| Hosts | None, View, Manage | View: hosts and host metrics. Manage: add and revoke hosts, host tags, drain, agent upgrades |
| Proxy | None, View, Manage | Manage: routes, the proxy itself |
| Backups | None, View, Operate, Manage | Operate: run jobs, restore. Manage: jobs and targets |
| Alerts | None, View, Manage | View: rules and alert history. Manage: rules and notification channels |
| Users & roles | None, View, Manage | Manage: users, invites, roles, assignments, other users' API tokens |
| Settings & audit | None, View, Manage | View: the audit log. Manage: instance settings, sign-in and identity providers, audit retention and webhook, Dockmesh updates |

On top of the levels, a role can hold **extras**. No level includes them:

- **Terminal**: open a shell in containers, write container files.
- **Files**: browse and read files in containers and volumes.
- **Environment values**: see environment values in container inspect and a stack's `.env`. Deploy on a stack shows its `.env` too.
- **Download backups**: download decrypted backup archives.
- **Export the backup key**: export the key that decrypts every backup. Only on an assignment on everything.

### Built-in roles

| Role | Levels | Extras |
|---|---|---|
| Admin | everything at the top level | all |
| Host Admin | Stacks, Containers, Resources, Hosts, Proxy, Backups, Alerts: Manage; Settings & audit: View | terminal, files, env, download backups |
| Deployer | Stacks: Deploy; Containers: Operate; Resources, Hosts, Proxy, Backups, Alerts: View | none |
| Operator | Stacks, Containers: Operate; Resources, Hosts, Backups, Alerts: View | terminal |
| Viewer | Stacks, Containers, Resources, Hosts, Proxy, Backups, Alerts, Settings & audit: View | none |

Built-in roles can't be changed. **Duplicate** one to start a custom role.

## Scopes

| Scope | Covers |
|---|---|
| Everything | every host and stack, and the instance-wide areas |
| Host | one host, its containers, images, volumes, networks and stacks |
| Host tag | every host carrying the tag, now and later |
| Stack | one stack on one host. Stack names are unique; the scope covers the stack while it runs on that host or isn't deployed |

Proxy, alert rules and notification channels, users and roles, settings and the audit log are instance-wide. Only an assignment on **everything** grants them; a narrower assignment of the same role does not. Host tags, enrolling new hosts, registry credentials, templates and the backup key are instance-wide for the same reason.

A stack that exists only as files and was never deployed has no host yet. Only assignments on everything or on that stack see it.

Every signed-in user can always see their own profile, sessions, 2FA and API tokens, server health and version, and receive live updates.

## Who can grant what

To add or remove an assignment, you need Users & roles: Manage on everything, and you must hold every permission of the role everywhere the scope reaches. The same applies to roles: you can only create or change a role whose levels and extras you hold yourself. The last assignment of an admin role on everything can't be removed, and such a role can't be narrowed while it is the last one.

## API tokens

A token acts with its owner's **current** access. When you create it you can restrict it further to a role on a scope; it never gets more than the owner has. See [API tokens](../features/api-tokens.md).

## Identity providers

Group mappings on an OIDC, SAML, LDAP or OAuth2 provider map a group to a role on a scope. At every sign-in, Dockmesh replaces the user's identity provider assignments with one per matching group; with no match the provider's default role applies on everything. Assignments you add by hand stay. On the user's **Overview** tab you can switch off that sign-in updates the user's access from groups.

If a sign-in would remove the last admin assignment on everything, Dockmesh keeps it and tells admins in a notice.

## Audit

Adding or removing an assignment, creating, changing or deleting a role, and creating an API token are recorded in the audit log under the **access** category.

## Upgrading from v0.4

The upgrade moves every user's role, the role's scopes and the user's host tags onto assignments that give the same access. Where that isn't possible, for example a scoped role combined with user host tags, the access is narrowed: such users get one assignment per host that matched at upgrade time. Custom roles get, per area, the highest level their old permissions fully covered. Nothing gains access, except that the built-in roles follow the new definitions above. An admin notice lists every account, role and token that changed, with before and after.
