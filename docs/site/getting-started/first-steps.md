---
title: First steps
description: Sign in, find your way around, deploy a first stack, look at its containers and logs, invite a user, add a host and set up a backup.
---

This page walks through a fresh install from first login to a backed-up stack. It assumes the server is running on `http://<host>:8080` (see [Installation](./installation.md)).

## Sign in

How you get the first admin account depends on how you installed:

| Install method | First admin |
|---|---|
| Installer script | You created it in the setup wizard at `/setup`. Its last step shows the login URL and your username. |
| `dockmesh init` | `init` printed the username and, if you left the password empty, the generated password. |
| From source or in Docker | The server created `admin` on first start and printed the password once, after the line `Bootstrap admin created`: in the terminal you started `./dockmesh` from, or in `docker compose logs dockmesh`. |

If you lose the admin password, reset it on the server:

```bash
sudo -u dockmesh dockmesh admin reset-password --user admin
```

After signing in you land on the **Overview**, which lists what needs attention and the recent changes from the audit log.

## Find your way around

The sidebar groups the pages:

| Group | Pages |
|---|---|
| (top) | **Overview**, **Activity** |
| Workloads | **Stacks**, **Containers**, **Templates** |
| Infrastructure | **Hosts**, **Resources** (images, volumes, networks), **Topology** |
| Operations | **Updates**, **Backups**, **Reverse proxy**, **Alerts** |
| Admin | **Users & roles**, **Audit log**, **Settings** |

You only see the pages your role allows. **Settings** has its own sub-navigation: **General**, **Updates**, **Encryption key**, **Authentication**, **Registries** and **Global environment**. The host filter in the top bar limits every page to one host (typing `#tag` in it narrows the host list to hosts with that tag); **Ctrl+K** (**⌘K** on macOS) searches pages, stacks, containers, hosts and settings. The top bar also has the running-operations indicator, the notification bell and the light/dark switch.

Your own settings are under the user menu at the bottom of the sidebar → **My account**. Turn on two-factor authentication for the admin account there (**Enable 2FA**); see [Authentication](../features/authentication.md).

### Language

The web UI is available in English and German. Each user sets it under **My account** → **Language**: **English**, **Deutsch**, or **Browser language**. The choice is saved per user and also applies to mail notifications sent to that user. Without a choice, the UI follows the browser's language and falls back to English for languages other than German.

## Create a stack

1. Open **Stacks** in the sidebar and click **New stack**.
2. Enter a **Name**. Names are lowercase letters, digits and hyphens, 2 to 63 characters, and must start and end with a letter or digit.
3. With **Compose editor** selected, paste a compose file into the `compose.yaml` field, for example:

   ```yaml
   services:
     web:
       image: nginx:alpine
       ports:
         - "8081:80"
   ```

4. Optionally add `.env` content. It is stored next to the compose file, encrypted at rest as `.env.age` by default.
5. With more than one host connected, pick the **Host**. Click **Create** to save the stack without starting it, or **Create & deploy** to start it right away.

The stack is now a directory `<stacks root>/<name>/` with a `compose.yaml` in it; on an installer setup that is `/var/lib/dockmesh/stacks/<name>/`. The UI reads and writes that file, so you can also edit it on disk.

Instead of pasting YAML you can use **Import from docker run** to convert a `docker run` command into a compose file, switch to **From git repository** to sync the stack from Git (see [Git sources](../features/git-sources.md)), or start from a template under **Templates** (see [Templates](../features/templates.md)).

> **Note:** Relative bind mounts such as `./data:/data` point at the stack directory, so the example writes to `<stacks root>/<name>/data` on the host.

## Deploy

On the stack's page, click **Deploy**. Dockmesh first pulls every image that is not on the host yet, plus images of services with `pull_policy: always`. If a pull fails for an image that isn't cached, it aborts before touching any container; if a cached copy exists, it logs a warning and uses that. It then recreates every service's container, in alphabetical order of service name, and waits for each to become healthy (or, without a healthcheck, to stay up for a few seconds). Every deploy recreates all containers of the stack, even when nothing changed.

With the example above, `http://<host>:8081` now shows the nginx welcome page. If the host port is already taken, the deploy fails with a "port already in use" error.

Each successful deploy is recorded under the stack's **History** tab, from where you can roll back to an earlier compose file. More in [Stacks](../features/stacks.md).

## Look at containers and logs

- The stack's **Logs** tab streams the logs of the stack's running containers.
- **Containers** in the sidebar lists the containers of the host chosen in the top bar's host filter (or of all hosts). Open one to get the **Overview**, **Logs**, **Terminal** (a shell inside the container, needs the terminal permission), **Inspect**, **Network** and **Files** tabs, plus **Updates** for containers on the server's own host.

See [Containers](../features/containers.md).

## Invite a user

1. Open **Users & roles** and click **Invite**.
2. Optionally enter email addresses (one link each), pick a **Role**, choose what the role applies **On** (everything, a host, a host tag or a stack) and how long the link stays valid (1, 3 or 7 days).
3. Click **Create invite links** and send each link to its recipient yourself. Dockmesh does not email them; the links are shown once.

The recipient opens the link, picks a username and a password that meets the password policy (at least 8 characters by default) and is signed in with the role you chose. Links are single-use.

The built-in roles are **Admin**, **Host Admin**, **Deployer**, **Operator** and **Viewer**; you can also create your own and give a user several assignments. See [Permissions](../concepts/permissions.md). For single sign-on instead of local accounts, see [SSO](../features/sso.md).

## Add a remote host

A remote host runs `dockmesh-agent`, which connects out to the server on port 8443. Before you add one, check under **Settings** → **General** that the **Base URL** and **Agent public URL** point at an address the remote host can reach; [Multi-host](../features/multi-host.md) explains the settings.

1. Open **Hosts** and click **Add host**.
2. Enter a **Hostname** and optional tags, then click **Generate install command**.
3. Run the displayed `curl -fsSL '<server>/install/agent.sh?token=...&ca=...' | sudo bash` command on the remote host as root. The token is shown only once.
4. The dialog waits until the agent connects and then shows the host as online.

The agent installer needs Linux with systemd on an `amd64` or `arm64` host; it picks the matching agent binary and checks it against the checksum the server put into the script. It installs Docker from get.docker.com if Docker is missing (`--skip-docker` turns that off). Some features work on the local host only; [Multi-host](../features/multi-host.md) lists them.

## Set up a backup

1. Open **Backups** and click **New backup job**.
2. Pick a source: **Whole dockmesh** (database, stacks and data directory), **Single stack** (compose files and named volumes) or **Single volume**.
3. Set a schedule and the retention, and under **Write to target** pick a saved target or set up a new one (local directory, S3, SFTP, SMB or WebDAV).
4. Save, then click **Run now** on the job to check that it works.

**Run now** starts the run in the background, so you can leave the page. Its steps and live log are in the running-operations indicator in the top bar and under **Steps and log** in the run's menu. Schedules use the server's time zone unless the `backups.timezone` setting names another.

Start with a **Whole dockmesh** job to a target that is not on the same machine, and keep a copy of the encryption key somewhere else (**Settings** → **Encryption key** → **Download key backup**): without it, encrypted backups cannot be restored. [Backups](../operations/backups.md) covers targets and restores, [Disaster recovery](../operations/disaster-recovery.md) the key.

## Next steps

- [Architecture](../concepts/architecture.md) explains how the server, agents and stacks fit together.
- [Security hardening](../operations/hardening.md) before exposing the server beyond your LAN.
- [Upgrade](../operations/upgrade.md) when a new release comes out.
