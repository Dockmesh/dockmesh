---
title: Stacks
description: Create, edit, deploy, roll back, adopt and delete Compose stacks from the Dockmesh web UI.
---

This page covers the **Stacks** pages in the UI. How a stack is stored on disk and what a deploy does to your containers is explained in [Stacks (concepts)](../concepts/stacks.md); read that first if you are moving existing Compose projects over.

## The stack list

**Stacks** in the sidebar lists the stacks of the host selected in the host switcher (or of every host with **All hosts**) with their services, host, status and the time of the last deploy. The filter above the list switches between **All**, **Running**, **Attention** (unhealthy or partly running), **Stopped** and, when there are any, **Not deployed**. The search box filters by stack or service name, and the **Name · services**, **Status** and **Last deploy** column headers sort the list.

A stack that was never deployed shows as **Not deployed**. Until it is deployed it belongs to the host it was created for, and only users who may see that host see it.

If a stack has a deployment record or running containers but its `compose.yaml` is missing or empty, a box above the list says how many stacks need recovery, with **Open recovery** per stack (see [Recovering a stack](#recovering-a-stack)).

Each row's menu offers **Open**, then, with the Deploy level on stacks, **Deploy** (never deployed), **Start** (stopped) or **Redeploy**, and **Stop** while something runs. **Delete…** opens the same dialog as on the stack page (see [Deleting a stack](#deleting-a-stack)).

## Creating a stack

Click **New stack**. The **Create stack** dialog asks for a name (2 to 63 characters, lowercase letters, digits and hyphens, starting and ending with a letter or digit; `dockmesh`, `agent`, `system`, `api`, `admin` and `config` are reserved) and offers two sources:

- **Compose editor**: paste a `compose.yaml` and, optionally, the contents of a `.env` file.
- **From git repository**: clone the compose file from a repository. The fields are described in [Git sources](./git-sources.md).

On a server with more than one host, **Host** picks the host the stack is for. Until the stack is deployed it belongs to that host: users whose access is limited to that host (or one of its tags) can read, edit and deploy it. A user limited to some hosts has to pick one of them. A stack name that someone's stack assignment names is reserved; only an admin, or a user whose own assignment names it, can create a stack with it. A name that already belongs to a stack is refused, also for git sources.

| Button | What it does |
|---|---|
| **Create** (or **Import & sync** for git) | Writes the files under `<stacks root>/<name>/`. Nothing runs yet, unless the git source has **Auto-deploy on new commits** on: then the first sync deploys the stack on the chosen host. |
| **Create & deploy** | Writes the files, then deploys on the chosen host. Needs the Deploy level. |

### Converting a `docker run` command

**Import from docker run** in the create dialog opens a box for a complete `docker run` command. **Convert** turns it into a compose service and fills the editor. Ports, volumes, environment, networks, restart policy, labels and capabilities are converted. Flags the converter does not handle are listed under **Converter warnings**, so you can add them by hand. Multi-line commands with trailing backslashes work as pasted.

### Importing a directory of compose projects

To bring many existing projects in at once, run this on the server:

```bash
sudo dockmesh import compose-dir --path /srv/compose --dry-run
sudo dockmesh import compose-dir --path /srv/compose
```

Every subdirectory of `--path` that contains `compose.yaml`, `compose.yml`, `docker-compose.yaml` or `docker-compose.yml` becomes a stack. The directory name is turned into a valid stack name, and a `.env` next to the compose file is imported too (encrypted if secrets encryption is on). Other files in the directory are not copied.

| Flag | Effect |
|---|---|
| `--path <dir>` | Source directory (required). |
| `--prefix <text>` | Prepended to every stack name, for example `--prefix legacy-`. |
| `--force` | Overwrite stacks that already exist instead of skipping them. |
| `--dry-run` | Print what would happen without writing anything. |

The command reads the server's env file like other `dockmesh` subcommands. Stacks written while the server is running only show up after a server restart. Import only writes files; it does not start or bind any containers. If the projects are already running, adopt them instead.

## The stack page

The header shows the stack's status, how many services are running, its host and the last deploy, and during a deploy the current phase. The buttons are **Deploy**, **Migrate** (with more than one host, see [Migration and drain](./migration-and-drain.md)) and **Stop** (only while something runs). There is no restart action for a whole stack; deploy again instead. The **More stack actions** menu holds **Connect a git repository** or **Edit git source**, **Sync from git now**, **Edit dependencies**, **Reload from disk** and **Delete stack…**.

The tabs are **Overview**, **Compose**, **Environment**, **Logs**, **History** (with the Deploy level) and **Settings**.

Banners above the tabs report files changed outside Dockmesh, a service that mounts the stack directory, a running migration and git sync problems. See [Editing files outside Dockmesh](../concepts/stacks.md#editing-files-outside-dockmesh).

**Overview** lists each service with its image, state, CPU and memory; click a row to open the container. Each service's menu has **Open container**, **Rolling update…** and **Scale…** (see [Scaling](./scaling.md)) and, for stacks on the server's host, **Update image…** (or **Pull and update image…**). The side column shows rolled-up resources, the host, the source (compose editor or git repository), published ports, the stack's networks and volumes, dependencies, and the three most recent deploys.

**Logs** merges the logs of all services into one stream, each line tagged with its service. Click service names to show only those services; **Show all** shows all again. **Pause** and **Follow** stop and resume the stream, **Clear** empties it. The view keeps the last 1500 lines.

### Editing compose.yaml

The **Compose** tab shows the file read-only. Click **Edit** (Deploy level) to get a plain text editor, then **Save** to write the file or **Save & deploy** to write it and deploy right away. There is no schema validation in the editor; syntax errors show up when you deploy.

If the file changes on disk while the page is open (an editor, `git pull`, a git sync), a banner says it was modified outside Dockmesh. **Reload** picks up the new version and discards unsaved edits.

### The .env file

The **Environment** tab edits the stack's `.env` as a table. **Add variable** adds a row, the pencil icon edits one, **Save** writes the file. Values whose key contains `password`, `secret`, `token`, `api_key`, `credential` or `access_key` are masked until you click **Show secrets**; this is display masking only. The **Used by** column shows which services reference the key, or **unused**.

Reading the `.env` needs the Deploy level on the stack or the extra **See environment values (inspect, .env)**; without either, the tab says the file is hidden.

Variables are used for `${VAR}` substitution in `compose.yaml` at deploy time. How they combine with global variables, and where the values are stored, is in [Environment variables](./environment-variables.md).

## Deploying and stopping

**Deploy** runs the stack on the host selected in the host switcher (with **All hosts** selected, on the host it is deployed on or was created for). Every deploy pulls missing images and recreates every container; the details are in [How a deploy reconciles](../concepts/stacks.md#how-a-deploy-reconciles).

If `compose.yaml`, `.env` or `.env.age` changed since Dockmesh last wrote them, the deploy first asks **Stack files changed outside Dockmesh** and lists the files. **Deploy anyway** deploys them as they are and records them as reviewed.

On the server's host the header shows live progress: pulling, creating, starting, healthcheck, then `deploy ok` or `deploy failed`. Deploys on agent hosts show no phases. A deploy on an agent may take up to 15 minutes, image pulls included.

Common failures are translated into readable messages, for example a host port that is already in use or a pull that failed because registry credentials are missing (see [Registries](./registries.md)).

**Stop** stops and removes all of the stack's containers. Networks, volumes and files stay, and the next deploy creates the containers again.

Relative bind mounts such as `./data:/data` point into the stack's directory (on agent hosts, into the agent's copy of it). Stacks that an earlier version deployed with such mounts need one redeploy to move their data there. See [Relative paths and the staging directory](../concepts/stacks.md#relative-paths-and-the-staging-directory).

### Updating a service's image

**Update image…** on a service pulls its image and, when it changed, recreates every replica of the service from the stack's current `compose.yaml`, records the deploy in the history, and carries the volumes over. Because it applies `compose.yaml` as it is now, it needs the Deploy level on the stack as well as the Manage level on containers. The action is offered for stacks on the server's host. Image updates of containers outside a stack are covered in [Image updates](./image-updates.md).

## History and rollback

The **History** tab lists past deploys, newest first, with the change, the user, the time, the duration and the result. The change column shows `manual`, `rollback to #3`, `git auto-deploy`, `env override` or a custom note, and the git commit for git deploys. A button per entry expands the image of each service and marks the ones that changed against the previous entry; **Compose** opens the deployed `compose.yaml` or a diff.

Manual deploys are recorded only when they succeed. Git auto-deploys are recorded whether they succeed or fail.

**Roll back** on an entry overwrites `compose.yaml` with that entry's content and deploys it. Keep in mind:

- History stores the image **tag** each service used, not the image digest. If you deploy `app:latest` and the tag has moved since, a rollback pulls whatever `latest` points to now. Pin versioned tags if you want rollback to mean something.
- The current `.env` is kept as it is. Roll environment values back by hand if needed.
- An entry deployed with an environment override applies that override again; the override file must still exist.
- The rollback deploys on the host the stack page works on, not necessarily the host the entry was deployed on.
- If the redeploy fails, `compose.yaml` has already been overwritten. Fix it, or roll forward.
- Entries of override deploys made before v0.5.0 lost their compose snapshot, because it held `.env` values. They can't be rolled back to.

## Environment overrides

Put a file named `compose.<env>.yaml` (for example `compose.prod.yaml`) next to `compose.yaml` in the stack directory on the server. It appears under **Overlays** on the **Environment** tab. Click its name to make it the default for this stack, or **base** to deploy `compose.yaml` alone (Deploy level). The choice is saved in `.dockmesh.meta.json`, and the API also accepts `?environment=<env>` per deploy.

There is no UI to create or edit override files; write them on the server. The merge rules are in [Environment overrides](../concepts/stacks.md#environment-overrides).

## Dependencies between stacks

The **Dependencies** section on the **Settings** tab (and in the side column of **Overview**) shows which stacks this one **Needs** and which stacks it is **Needed by**. **Edit dependencies** opens a dialog where you add or remove prerequisites. Cycles are rejected; names of stacks that do not exist yet are accepted.

When you deploy a stack, each prerequisite that is not fully running is deployed first, on the same host, in dependency order. A prerequisite deployed this way uses its base `compose.yaml` (no environment override). The deploy is refused when a prerequisite runs on another host, when you may not deploy it there, when it is missing on disk or when it fails to deploy.

Stopping or deleting a stack that other stacks depend on is refused with a list of the dependents. The API accepts `?force=true` to do it anyway; deleting removes the stack's dependency edges.

## Git-sourced stacks

A stack can follow a git repository: **Connect a git repository** in the **More stack actions** menu, or **Connect a repository** under **Git source** on the **Settings** tab. The **Git source** section shows the repository, branch, how deploys happen and the last sync, with **Sync now**, **Edit** and **Disconnect**. Auto-deploys run on the host the stack is deployed on or was created for. Everything else about syncing, polling and webhooks is in [Git sources](./git-sources.md).

## Adopting a running project

Adopting takes over a Compose project that is already running (started with `docker compose up`) without restarting it. Dockmesh writes a `compose.yaml` into the stacks directory under the project's name; the containers are matched through their `com.docker.compose.project` label. Adopting needs the Manage level on stacks. Names of existing stacks and reserved names are refused, and a project that runs Dockmesh itself can only be adopted by an admin.

**From the UI:** click **Adopt existing…** on the stack list. It lists the unmanaged projects on the selected host (or every host); click **Adopt…** next to one. The dialog is pre-filled with the service names and images it found. Replace that skeleton with your real `compose.yaml` and click **Adopt**. Only the compose file is written; files the project references by relative path are not copied.

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
- **Restore from last backup** (**Restore**): restores the compose file and the named volumes from the stack's most recent stack backup, if one exists, replacing the volumes' contents (see [Backups](../operations/backups.md#restore)). Containers are not started; deploy afterwards.
- **Remove stack record** (**Discard record**): forgets the stack in Dockmesh. Running containers are not touched.

## Deleting a stack

**Delete stack…** (in the **More stack actions** menu, at the bottom of the **Settings** tab, or **Delete…** in the stack list) opens **Delete stack &lt;name&gt;**. Type the stack's name to confirm. Deleting always removes the stack directory, which on the server's host includes the data of relative bind mounts, and the stack's git source with its clone, so a later commit can't bring the stack back. You choose what else is cleaned up:

| Option | Default | Notes |
|---|---|---|
| **Stop and remove containers** | on if containers exist | Unchecked, they keep running and can be adopted again. |
| **Remove project networks** | on | |
| **Remove volumes** | off | Deletes the data permanently. External volumes are never removed. |
| **Remove images** | off | Images shared with other projects are kept. |

The dialog lists exactly which networks, volumes and images would be removed. Network, volume and image cleanup only works on the server's host; for a stack on an agent host the dialog says **Resource cleanup unavailable for this host**. **Stop and remove containers** still works there, and the files are removed. Clean up on that host with `docker` afterwards.

## Permissions

Stack permissions come from the **Stacks** area of a role.

| Level | Allows |
|---|---|
| View | See stacks, services and compose, read logs |
| Operate | Stop stacks; start, stop and restart their containers |
| Deploy | Deploy and redeploy, edit `compose.yaml` and `.env`, read `.env`, history and rollback, scaling and rolling updates, overrides, dependencies, sync a git source |
| Manage | Create and delete stacks, migrate, adopt, connect git repositories |

Reading `.env` without the Deploy level needs the extra **See environment values (inspect, .env)**. Assignments can be limited to a host, a host tag or a single stack. See [Permissions](../concepts/permissions.md).
