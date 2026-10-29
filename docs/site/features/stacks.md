---
title: Stacks
description: Create, edit, deploy, roll back, adopt and delete Compose stacks from the Dockmesh web UI.
---

This page covers the **Stacks** pages in the UI. How a stack is stored on disk and what a deploy does to your containers is explained in [Stacks (concepts)](../concepts/stacks.md); read that first if you are moving existing Compose projects over.

## The stack list

**Stacks** lists every stack under the stacks root with its services, host, status and the time of the last deploy. The tabs **All**, **Running**, **Issues** and **Stopped** filter the list; the search box and the sort menu (name, status, last deployed) refine it.

Two banners can appear above the list:

- **needs attention**: a stack has a deployment record or running containers, but its `compose.yaml` is missing or empty. Open the stack to recover it (see [Recovering a stack](#recovering-a-stack)).
- **unmanaged compose projects detected**: containers with a `com.docker.compose.project` label that no stack owns. You can adopt them (see [Adopting a running project](#adopting-a-running-project)). Only shown to users with `stacks.adopt`.

## Creating a stack

Click **New stack** (needs `stacks.create`). The **Create stack** dialog asks for a name (2 to 63 characters, lowercase letters, digits and hyphens) and offers two sources:

- **Compose editor**: paste a `compose.yaml` and, optionally, the contents of a `.env` file.
- **From git repository**: clone the compose file from a repository. The fields are described in [Git sources](./git-sources.md).

On a server with more than one host, **Deploy on host** picks where **Create & deploy** runs the stack. The files themselves are host-neutral.

| Button | What it does |
|---|---|
| **Create** (or **Import & sync** for git) | Writes the files under `<stacks root>/<name>/`. Nothing runs yet. |
| **Create & deploy** | Writes the files, then deploys on the chosen host. Needs `stacks.deploy`. |

### Converting a `docker run` command

**Import compose** in the page header (or **Import from docker run** in the create dialog) opens a box for a complete `docker run` command. **Convert** turns it into a compose service and fills the editor. Ports, volumes, environment, networks, restart policy, labels and capabilities are converted. Flags the converter does not handle are listed under **Converter warnings**, so you can add them by hand. Multi-line commands with trailing backslashes work as pasted.

### Importing a directory of compose projects

To bring many existing projects in at once, run this on the server:

```bash
dockmesh import compose-dir --path /srv/compose --dry-run
dockmesh import compose-dir --path /srv/compose
```

Every subdirectory of `--path` that contains `compose.yaml`, `compose.yml`, `docker-compose.yaml` or `docker-compose.yml` becomes a stack. The directory name is turned into a valid stack name, and a `.env` next to the compose file is imported too (encrypted if secrets encryption is on). Other files in the directory are not copied.

| Flag | Effect |
|---|---|
| `--path <dir>` | Source directory (required). |
| `--prefix <text>` | Prepended to every stack name, for example `--prefix legacy-`. |
| `--force` | Overwrite stacks that already exist instead of skipping them. |
| `--dry-run` | Print what would happen without writing anything. |

The command reads the same `DOCKMESH_*` environment variables as the server, so run it with the server's environment loaded. Stacks written while the server is running only show up after a server restart. Import only writes files; it does not start or bind any containers. If the projects are already running, adopt them instead.

## The stack page

The header shows how many services are running and, during a deploy, the current phase. The actions are **Deploy**, **Stop** and, with more than one host, **Migrate** (see [Migration and drain](./migration-and-drain.md)). There is no restart action for a whole stack; deploy again instead.

The tabs are **Overview**, **Compose**, **Environment**, **Logs**, **History** and **Settings**.

**Overview** lists each service with its container state and image, plus rolled-up resource usage, published ports, and the stack's networks and volumes. Each service row has buttons for scaling and rolling updates (see [Scaling](./scaling.md)).

**Logs** streams the logs of all services side by side. Click a service name to show or hide it.

### Editing compose.yaml

The **Compose** tab shows the file read-only. Click **Edit** (needs `stacks.update`) to get a plain text editor, then **Save** to write the file or **Save & deploy** to write it and deploy right away. There is no schema validation in the editor; syntax errors show up when you deploy.

If the file changes on disk while the page is open (an editor, `git pull`, a git sync), a banner says it was changed outside Dockmesh. **Reload** picks up the new version and discards unsaved edits.

### The .env file

The **Environment** tab edits the stack's `.env` as a table. **Add variable** adds a row, the pencil icon edits one, **Save** writes the file. Values whose key contains `password`, `secret`, `token`, `api_key`, `credential` or `access_key` are masked until you click **Show secrets**; this is display masking only. The **Scope** column shows which services reference the key, or **unused**.

Variables are used for `${VAR}` substitution in `compose.yaml` at deploy time. How they combine with global variables, and where the values are stored, is in [Environment variables](./environment-variables.md).

## Deploying and stopping

**Deploy** runs the stack on the host selected in the host switcher (with **All hosts** selected, on the host it was last deployed to). Deploy needs `stacks.deploy`. Every deploy pulls missing images and recreates every container; the details are in [How a deploy reconciles](../concepts/stacks.md#how-a-deploy-reconciles).

<!-- update after: agents: survive disconnects, flow-control streams, renew certs -->
The request runs until the deploy finishes. On the local host the header shows live progress: pulling, creating, starting, healthcheck, then `deploy ok` or `deploy failed`. Deploys on remote hosts show no progress, and a call to an agent times out after 30 seconds, including image pulls. For large images on a remote host, pull the image first (or deploy once and retry).

Common failures are translated into readable messages, for example a host port that is already in use or a pull that failed because registry credentials are missing (see [Registries](./registries.md)).

**Stop** stops and removes all of the stack's containers. Networks, volumes and files stay, and the next deploy creates the containers again.

<!-- update after: backups: scope hooks, limit targets, check sftp keys; fix bind mounts -->
> **Warning:** Relative bind mounts such as `./data:/data` do not point into the stack directory. Use named volumes or absolute host paths for data you need to keep. See [Relative paths and the staging directory](../concepts/stacks.md#relative-paths-and-the-staging-directory).

## History and rollback

The **History** tab lists past deploys, newest first, with the user, the time, the duration and the image of each service. Entries are labelled, for example `rollback to #3`, `git auto-deploy`, `env: prod` or `auto-deployed as dependency`. A delta marker shows which service images changed against the previous entry. Click the file icon to view the deployed `compose.yaml` or a diff.

Manual deploys are recorded only when they succeed. Git auto-deploys are recorded whether they succeed or fail. History is kept forever.

**Roll back** on an entry overwrites `compose.yaml` with that entry's content and deploys it. Keep in mind:

- History stores the image **tag** each service used, not the image digest. If you deploy `app:latest` and the tag has moved since, a rollback pulls whatever `latest` points to now. Pin versioned tags if you want rollback to mean something.
- The current `.env` is kept as it is. Roll environment values back by hand if needed.
- The rollback deploys on the host selected in the host switcher, not necessarily the host the entry was deployed on.
- If the redeploy fails, `compose.yaml` has already been overwritten. Fix it, or roll forward.

## Environment overrides

Put a file named `compose.<env>.yaml` (for example `compose.prod.yaml`) next to `compose.yaml` in the stack directory on the server. It appears under **Overlays** on the **Environment** tab. Click its name to make it the default for this stack, or **base** to deploy `compose.yaml` alone. The choice is saved in `.dockmesh.meta.json`, and the API also accepts `?environment=<env>` per deploy.

There is no UI to create or edit override files; write them on the server. With an override active, global environment variables are not substituted, and the deploy history entry contains the merged compose with your `.env` values already filled in. The merge rules are in [Environment overrides](../concepts/stacks.md#environment-overrides).

## Dependencies between stacks

The **Dependencies** section on the **Environment** tab shows which stacks this one **needs** and which stacks it is **needed by**. **Edit** opens a dialog where you add or remove prerequisites. Cycles are rejected; names of stacks that do not exist yet are accepted.

When you deploy a stack, each prerequisite that is not fully running is deployed first, on the same host, in dependency order. A prerequisite deployed this way uses its base `compose.yaml` (no environment override). If a prerequisite is missing on disk or fails to deploy, the main deploy is aborted.

Stopping or deleting a stack that other stacks depend on is refused with a list of the dependents. The API accepts `?force=true` to do it anyway; deleting removes the stack's dependency edges.

> **Note:** Dependency deploys do not check the caller's stack scope. A user who may deploy stack `api` can thereby deploy its prerequisites. Keep this in mind when you use stack-scoped roles.

## Adopting a running project

Adopting takes over a Compose project that is already running (started with `docker compose up`) without restarting it. Dockmesh writes a `compose.yaml` into the stacks directory under the project's name; the containers are matched through their `com.docker.compose.project` label. Adopting needs `stacks.adopt`.

**From the UI:** click **Adopt** next to a project in the unmanaged-projects banner. The dialog is pre-filled with the service names and images it found. Replace that skeleton with your real `compose.yaml` and click **Adopt**. Only the compose file is written; files the project references by relative path are not copied.

**From a shell with `dmctl`:** run this in the project directory. It uploads `compose.yaml` together with the other files in the directory (build contexts, config files):

```bash
cd ~/docker/myapp
dmctl stack adopt . --dry-run
dmctl stack adopt .
```

| Flag | Effect |
|---|---|
| `--name <name>` | Project name to adopt; defaults to the directory name. Must match the containers' project label. |
| `--dry-run` | Show what would be adopted without uploading. |
| `--yes` | Skip the confirmation prompt. |
| `--with-env` | Also upload `.env` (off by default because it usually holds secrets). |
| `--max-size <bytes>` | Maximum bundle size, default 100 MiB. The server enforces the same limit. |

The global `--host` flag of `dmctl` picks the host when the project runs on an agent.

Adoption changes nothing on the containers. The first **Deploy** afterwards recreates every container from the adopted `compose.yaml`, so check the file first, especially relative bind mounts and the [fields Dockmesh ignores](../concepts/stacks.md#compose-fields-that-are-silently-ignored).

## Recovering a stack

When a stack's `compose.yaml` is missing or empty, the stack page hides the tabs and offers three options:

- **Recover from running containers** (**Recover**): rebuilds a best-effort `compose.yaml` from the live containers' configuration. Review the warnings it shows before you deploy.
- **Restore from last backup** (**Restore**): restores the stack from its most recent stack backup, if one exists (see [Backups](../operations/backups.md)).
- **Remove stack record** (**Discard record**): forgets the stack in Dockmesh. Running containers are not touched.

## Deleting a stack

The **Delete** button at the bottom of the **Settings** tab opens **Delete stack**. Deleting always removes the stack directory. You choose what else is cleaned up:

| Option | Default | Notes |
|---|---|---|
| **Stop and remove containers** | on if containers exist | |
| **Remove project networks** | on | |
| **Remove volumes** | off | Deletes the data permanently. External volumes are never removed. |
| **Remove images** | off | Images shared with other projects are kept. |

The dialog lists exactly which networks, volumes and images would be removed. Network, volume and image cleanup only works on the local host; for a stack on a remote host the dialog says so, and only the files are removed. Clean up on that host with `docker` afterwards.

A git source connected to the stack is not removed with it. Disconnect it first, or the next sync recreates the stack.

The other controls on the **Settings** tab (auto-update images, webhook on deploy, restart policy) are not functional and have no effect.

## Permissions

| Action | Permission |
|---|---|
| View stacks and their status | `stacks.view` |
| Stream logs on the **Logs** tab | `containers.logs` |
| Create a stack, convert `docker run` | `stacks.create` |
| Edit compose and `.env`, connect a git source | `stacks.update` |
| Deploy, stop, view history, roll back, scale, dependencies, overrides | `stacks.deploy` |
| Delete or discard | `stacks.delete` |
| Adopt | `stacks.adopt` |
| Migrate | `stacks.migrate` |

<!-- update after: rbac: scope read endpoints, limit grants, gate sensitive downloads -->
Anyone with `stacks.view` can read the stack's `.env` values through the API, regardless of scope. Do not give `stacks.view` to users who must not see the secrets of every stack. See [Permissions](../concepts/permissions.md).
