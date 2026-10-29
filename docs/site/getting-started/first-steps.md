---
title: First steps
description: Sign in, deploy a first stack, look at its containers and logs, invite a user, add a host and set up a backup.
---

This page walks through a fresh install from first login to a backed-up stack. It assumes the server is running on `http://<host>:8080` (see [Installation](./installation.md)).

## Sign in

How you get the first admin account depends on how you installed:

| Install method | First admin |
|---|---|
| Installer script | You created it in the setup wizard at `/setup`. |
| `dockmesh init` | `init` printed the username and, if you left the password empty, the generated password. |
| From source or in Docker | The server created `admin` on first start and logged the password once. Search the log for `bootstrap admin created` (`journalctl -u dockmesh`, or `docker compose logs dockmesh`). |

If you lose the admin password, reset it on the server:

```bash
sudo dockmesh admin reset-password --user admin
```

After signing in you land on the **Dashboard**. Turn on two-factor authentication for the admin account on the **Account** page (user menu → **Profile & security**); see [Authentication](../features/authentication.md).

## Create a stack

1. Open **Stacks** in the sidebar and click **New stack**.
2. Enter a **Name**. Names are lowercase letters, digits and hyphens, 2 to 63 characters, and must start and end with a letter or digit.
3. Paste a compose file into the editor, for example:

   ```yaml
   services:
     web:
       image: nginx:alpine
       ports:
         - "8081:80"
   ```

4. Optionally add `.env` content. It is stored next to the compose file (encrypted at rest by default).
5. Click **Create** to save the stack without starting it, or **Create & deploy** to start it right away. With more than one host connected, **Deploy on host** picks the target for **Create & deploy**.

The stack is now a directory `<stacks root>/<name>/` with a `compose.yaml` in it; on an installer setup that is `/var/lib/dockmesh/stacks/<name>/`. The UI reads and writes that file, so you can also edit it on disk.

Instead of pasting YAML you can use **Import from docker run** to convert a `docker run` command into a compose file, switch to **From git repository** to sync the stack from Git (see [Git sources](../features/git-sources.md)), or start from a template under **Templates** (see [Templates](../features/templates.md)).

<!-- update after: backups: scope hooks, limit targets, check sftp keys; fix bind mounts -->
> **Note:** Relative bind mounts such as `./data:/data` do not persist on deploy: they resolve inside a temporary directory that is removed afterwards. Use named volumes or absolute host paths.

## Deploy

On the stack's page, click **Deploy**. Dockmesh first pulls every image that is not on the host yet, plus images of services with `pull_policy: always`. If a pull fails for an image that isn't cached, it aborts before touching any container; if a cached copy exists, it logs a warning and uses that. It then recreates every service's container, in alphabetical order of service name, and waits for each to become healthy (or, without a healthcheck, to stay up for a few seconds). Every deploy recreates all containers of the stack, even when nothing changed.

With the example above, `http://<host>:8081` now shows the nginx welcome page. If the host port is already taken, the deploy fails with a port-conflict error.

Each successful deploy is recorded under the stack's **History** tab, from where you can roll back to an earlier compose file. More in [Stacks](../features/stacks.md).

## Look at containers and logs

- The stack's **Logs** tab streams the logs of the stack's running containers.
- **Containers** in the sidebar lists the containers of the host selected in the top bar (or of all hosts). Open one to get the **Overview**, **Logs**, **Terminal** (a shell inside the container, needs exec permission), **Inspect**, **Network** and **Files** tabs, plus **Updates**. **Files** and **Updates** are only available on the local host.

See [Containers](../features/containers.md).

## Invite a user

1. Open **Users & Roles** and click **Invite user**.
2. Enter one or more email addresses, pick a **Role** and, optionally, limit access to hosts with certain tags.
3. Generate the links and send each one to its recipient yourself. Dockmesh does not email them.

The recipient opens the link, picks a username and a password that meets the password policy (at least 8 characters by default) and is signed in with the role you chose. Links are single-use and expire after 24 hours. The dialog's **Initial password** and **Link expiry** fields have no effect.

The built-in roles are `admin`, `host-admin`, `deployer`, `operator` and `viewer`; you can also create your own. See [Permissions](../concepts/permissions.md). For single sign-on instead of local accounts, see [SSO](../features/sso.md).

## Add a remote host

A remote host runs `dockmesh-agent`, which connects out to the server on port 8443. Before you add one, make sure the server's public and agent URLs point at an address the remote host can reach; [Multi-host](../features/multi-host.md) explains the settings.

1. Open **Hosts** and click **Add host**.
2. Enter a **Hostname** and optional tags, then click **Generate install command**.
3. Run the displayed `curl -fsSL <server>/install/agent.sh?token=... | sudo bash` command on the remote host as root.
4. The dialog waits until the agent connects and then shows the host as online.

<!-- update after: agents: pin the CA at enrollment and verify self-upgrades -->
The agent installer needs Linux with systemd on the remote host. It installs Docker from get.docker.com if Docker is missing (`--skip-docker` turns that off), and it always downloads the `linux-amd64` agent build. Some features work on the local host only; [Multi-host](../features/multi-host.md) lists them.

## Set up a backup

1. Open **Backups** and click **New backup job**.
2. Pick a source: **Whole dockmesh** (database, stacks and data directory), **Single stack** (compose files and named volumes) or **Single volume**.
3. Set a schedule, pick a target (local directory, S3, SFTP, SMB or WebDAV) and set the retention.
4. Save, then open the job and click **Run now** to check that it works.

<!-- update after: backups: run in the background, set the time zone, use saved targets -->
**Run now** runs while the request is open; keep the page open until it finishes. Schedules use the server's local time.

Start with a **Whole dockmesh** job to a target that is not on the same machine, and keep a copy of the encryption key somewhere else: without it, encrypted backups cannot be restored. [Backups](../operations/backups.md) covers targets and restores, [Disaster recovery](../operations/disaster-recovery.md) the key.

## Next steps

- [Architecture](../concepts/architecture.md) explains how the server, agents and stacks fit together.
- [Hardening](../operations/hardening.md) before exposing the server beyond your LAN.
- [Upgrading](./upgrading.md) when a new release comes out.
