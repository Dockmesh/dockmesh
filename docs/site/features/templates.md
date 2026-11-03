---
title: Templates
description: Deploy ready-made stacks from the template library, write your own templates with parameters, and export them as YAML.
---

A template is a `compose.yaml` (and optionally a `.env`) with `{{placeholders}}`. Deploying a template asks for the parameter values, writes a new stack and deploys it in one step. Templates are listed under **Templates** in the sidebar, as a table with name and slug, description, images, number of parameters and source (**built-in** or **custom**).

## Built-in templates

Dockmesh ships six templates. They are embedded in the binary and written to the database on every start, so they can't be edited or deleted.

| Template | Slug | Image | Parameters |
|---|---|---|---|
| MySQL 8 | `mysql-8` | `mysql:8` | `db_name` (app), `db_user` (app), `db_password` and `root_password` (generated), `port` (3306) |
| Nginx | `nginx` | `nginx:alpine` | `port` (8080) |
| PostgreSQL 16 | `postgres-16` | `postgres:16-alpine` | `db_name` (app), `db_user` (postgres), `db_password` (generated), `port` (5432) |
| Redis 7 | `redis-7` | `redis:7-alpine` | `password` (empty), `port` (6379) |
| Uptime Kuma | `uptime-kuma` | `louislam/uptime-kuma:1` | `port` (3001) |
| Vaultwarden | `vaultwarden` | `vaultwarden/server:latest` | `port` (8080), `admin_token` (generated), `signups_allowed` (`true` or `false`, default false) |

Defaults are in parentheses; "generated" means a random value is created when you leave the field blank. Port parameters must be 2 to 5 digits. All six keep their data in named volumes.

The Redis template inserts `password` into the `redis-server` command line exactly as entered. To turn on authentication, enter `--requirepass <password>`, not the password alone.

## Deploying a template

Click **Deploy** in a template's row. The dialog asks for:

- **Stack name**: the name of the new stack. It must not belong to an existing stack.
- **Target host**: the server host or one of the agents.
- One field per parameter, named after the placeholder. Required parameters are marked **Required**, parameters with a fixed list of values are a drop-down, and secret parameters without a description say **Generated when left empty.** For parameters whose name contains `port`, the dialog warns when the port is already published by a container on the chosen host.

**Preview compose.yaml** shows the file with the values filled in. **Deploy** renders the template, writes `stacks/<name>/compose.yaml` (and `.env` if the template has one) and deploys it on the target host. Global environment variables are applied as for a normal deploy, and registry credentials are used for private images, also on agent hosts. The deploy is recorded in the stack's **History** with the note `template: <name>`. From then on the stack is an ordinary stack; it keeps no link to the template, and changing or deleting the template later does not change it.

Things to know:

- If the deploy fails, the stack files stay on disk so you can fix them and deploy from the stack page.
- Secret parameters left blank get a random 43-character value. The value is written into `compose.yaml` in plain text (not into the encrypted `.env`), so it is also in the deploy history entry and in the `compose` field of the deploy response; the `values` field of the response shows `<generated>` instead. Read the password from the stack's **Compose** tab when you need it.

## Writing a custom template

**New template** (needs `templates.create`) opens a form with **Name**, **Slug** (filled in from the name; lowercase letters, digits, `-` and `_`), **Description** and two tabs:

- **compose.yaml**: the compose text with placeholders.
- **Parameters**: one row per placeholder found in the compose text, with a **Label in the deploy dialog** (shown under the field, which itself is named after the placeholder), a **Default** and whether it is a **Secret**. Labels and defaults set here win over the ones in the compose text.

**Create template** saves it. Custom templates have **Edit…** and **Delete…** in their row menu (`templates.update`, `templates.delete`). The compose text must be valid YAML.

Placeholders use this syntax:

| Placeholder | Meaning |
|---|---|
| `{{name}}` | Replaced with the value entered for `name`. |
| `{{name\|default:value}}` | Use `value` when nothing is entered. |
| `{{name\|secret}}` | Generate a random value when nothing is entered. |
| `{{name\|required}}` | Refuse the deploy when nothing is entered. |
| `{{name\|enum:a,b,c}}` | Only allow one of the listed values (shown as a drop-down). |
| `{{name\|pattern:^[a-z]+$}}` | Validate the value against a regular expression. |

Flags can be combined, for example `{{port|default:8080|pattern:^[0-9]+$}}`. Because `|` separates flags, a pattern can't contain `|`. Every placeholder in the compose text becomes a parameter. Where a parameter declaration sets a default, secret, enum or pattern, it overrides the inline flag; inline flags fill in what the declaration leaves empty. The form edits labels and defaults; `required`, `enum` and `pattern` are set inline or through the API (`parameters` in `POST /api/v1/templates`), which also takes an `env` text with placeholders.

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

`GET /api/v1/templates/{id}/export` returns a template as a YAML file in the same format as the built-in templates (slug, name, description, icon URL, compose, env, parameters, author, version). **Download YAML** in a template's row menu links to that URL, but a plain link carries no session token, so the download fails with 401. Use an [API token](./api-tokens.md) instead:

```bash
curl -H "Authorization: Bearer $DOCKMESH_TOKEN" \
  -o mytemplate.yaml \
  https://dockmesh.example.com/api/v1/templates/7/export
```

There is no import of exported YAML files. To copy a template to another server, create it there with the form, or send the fields as JSON to `POST /api/v1/templates`. See [REST API](../reference/api.md).

## Permissions

| Action | Permission |
|---|---|
| List and view templates, export | `templates.view` (Stacks: View) |
| Deploy a template | `stacks.deploy` (Stacks: Deploy) on the target host, plus the right to create a stack there |
| Create, edit, delete custom templates | `templates.create`, `templates.update`, `templates.delete` (Stacks: Manage) |
