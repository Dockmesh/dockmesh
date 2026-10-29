---
title: Templates
description: Deploy ready-made stacks from the template library, write your own templates with parameters, and export them as YAML.
---

A template is a `compose.yaml` (and optionally a `.env`) with `{{placeholders}}`. Deploying a template asks for the parameter values, writes a new stack and deploys it in one step. Templates are listed under **Templates** in the sidebar.

## Built-in templates

Dockmesh ships six templates. They are embedded in the binary and refreshed on every start, so they cannot be edited or deleted.

| Template | Image | Parameters |
|---|---|---|
| MySQL 8 | `mysql:8` | `db_name` (app), `db_user` (app), `db_password` and `root_password` (generated), `port` (3306) |
| Nginx | `nginx:alpine` | `port` (8080) |
| PostgreSQL 16 | `postgres:16-alpine` | `db_name` (app), `db_user` (postgres), `db_password` (generated), `port` (5432) |
| Redis 7 | `redis:7-alpine` | `password` (empty), `port` (6379) |
| Uptime Kuma | `louislam/uptime-kuma:1` | `port` (3001) |
| Vaultwarden | `vaultwarden/server:latest` | `port` (8080), `admin_token` (generated), `signups_allowed` (false) |

Defaults are in parentheses; "generated" means a random value is created when you leave the field blank. Each card shows the description, the images, the number of parameters and the version.

## Deploying a template

Click **Deploy** on a card. The drawer asks for:

- **Stack name**: the name of the new stack. It must not exist yet.
- **Target host**: the local host or one of the agents.
- One field per parameter. Required parameters are marked **required**. For port parameters, the drawer warns when the port is already published by a container on the chosen host.

**Deploy** renders the template, writes `stacks/<name>/compose.yaml` (and `.env` if the template has one) and deploys it. Global environment variables are applied as for a normal deploy. From then on the stack is an ordinary stack; it keeps no link to the template, and changing the template later does not change it.

Things to know:

- If the deploy fails, the stack files stay on disk so you can fix them and deploy from the stack page.
- Template deploys are not recorded in the stack's **History** and do not create a deployment record. The stack's first manual deploy does.
- Parameters marked as secret are filled with a random 43-character value when you leave them blank. The value is written into `compose.yaml` in plain text (not into the encrypted `.env`), and the API response of the deploy contains the rendered compose, so it includes the generated value. Read the password from the stack's **Compose** tab when you need it.

Deploying a template needs `stacks.deploy`. It does not check host or stack scope, so a scoped user can deploy a template to any host. Give `stacks.deploy` to scoped users with that in mind.

## Writing a custom template

**New custom template** (shown to users with `stacks.create`; saving needs `templates.create`) opens a form with **Slug**, **Display name**, **Description**, the compose text and an optional list of parameter declarations. Edit and delete work on custom templates only and need `templates.update` and `templates.delete`.

Placeholders use this syntax:

| Placeholder | Meaning |
|---|---|
| `{{name}}` | Replaced with the value entered for `name`. |
| `{{name\|default:value}}` | Use `value` when nothing is entered. |
| `{{name\|secret}}` | Generate a random value when nothing is entered. |
| `{{name\|enum:a,b,c}}` | Only allow one of the listed values (shown as a drop-down). |
| `{{name\|pattern:^[a-z]+$}}` | Validate the value against a regular expression. |

Flags can be combined, for example `{{port|default:8080|pattern:^[0-9]+$}}`. Every placeholder found in the compose text becomes a parameter automatically. Declare a parameter explicitly when you want a description, or to mark it required. A declaration overrides the inline flags of the same name.

Example:

```yaml
services:
  app:
    image: "ghcr.io/acme/app:{{version|default:1.4.2}}"
    restart: unless-stopped
    environment:
      APP_SECRET: "{{app_secret|secret}}"
      LOG_LEVEL: "{{log_level|enum:debug,info,warn|default:info}}"
    ports:
      - "{{port|default:8080}}:8080"
```

Substitution is plain text replacement. There are no conditionals or loops. `${VAR}` references are left alone and are resolved from the stack's `.env` and global variables at deploy time, as in any stack.

## Exporting and importing

`GET /api/v1/templates/{id}/export` returns a template as a YAML file in the same format as the built-in templates (slug, name, description, compose, env, parameters, author, version). The download icon on a card links to that URL, but the browser does not send your session token with a plain link, so the download fails with 401. Use an [API token](./api-tokens.md) instead:

```bash
curl -H "Authorization: Bearer $DOCKMESH_TOKEN" \
  -o mytemplate.yaml \
  https://dockmesh.example.com/api/v1/templates/7/export
```

There is no import of exported YAML files. To copy a template to another server, create it there with the form, or send the fields as JSON to `POST /api/v1/templates`. See [API](../reference/api.md).

## Permissions

| Action | Permission |
|---|---|
| List and view templates | any signed-in user |
| Deploy a template | `stacks.deploy` |
| Create, edit, delete custom templates | `templates.create`, `templates.update`, `templates.delete` |
