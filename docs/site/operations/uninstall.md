---
title: Uninstall
description: Remove the Dockmesh server with uninstall.sh, remove agents, and clean up what the script leaves behind.
---

`scripts/uninstall.sh` removes the server installation. It is attached to every GitHub release as `uninstall.sh` and served at `get.dockmesh.dev/uninstall`. It never stops or removes containers and never touches Docker itself. Stacks you deployed keep running after Dockmesh is gone.

Back up first if there is any chance you want the data again, see [Disaster recovery](./disaster-recovery.md).

## Run the uninstaller

```bash
curl -fsSL https://get.dockmesh.dev/uninstall | sudo bash
```

If you'd rather read it before running it, download `uninstall.sh` from the [release](https://github.com/dockmesh/dockmesh/releases) and run `sudo bash uninstall.sh`. It must run as root and supports Linux and macOS.

It asks one question per component:

| Question | Default | Removes |
|---|---|---|
| Stop the running service? | yes | `systemctl disable --now dockmesh.service` (macOS: `launchctl bootout`), then kills remaining `/usr/local/bin/dockmesh` processes |
| Remove service unit / plist? | yes | `/etc/systemd/system/dockmesh.service` (macOS: `/Library/LaunchDaemons/dev.dockmesh.service.plist`) |
| Remove the dockmesh binary? | yes | `dockmesh`, `dockmesh.bak` and `dmctl` in `/usr/local/bin` (or `DOCKMESH_INSTALL_DIR`) |
| Remove agent installer + bundled binaries? | yes | The asset directory, normally `/usr/local/share/dockmesh` |
| Remove data directory (DB, stacks, age-keys)? | **no** | The whole data root, see below |
| Remove the 'dockmesh' service user? | yes | `userdel dockmesh` (Linux only), after killing its processes |

| Flag | Effect |
|---|---|
| `--yes` | Accept every default without asking. The data root is kept |
| `--purge` | Like `--yes`, and also removes the data root. Cannot be undone |
| `--dry-run` | Print the commands instead of running them |

```bash
curl -fsSL https://get.dockmesh.dev/uninstall | sudo bash -s -- --yes
```

### How the data root is found

The script looks for `dockmesh.env` (or the symlink to `/etc/dockmesh/dockmesh.env` an upgrade leaves) in `/var/lib/dockmesh`, `/usr/local/var/dockmesh`, `/opt/dockmesh` and `/data`, and uses the first directory where it finds one. `--purge` also removes `/etc/dockmesh`. It reads the asset paths from that file. Without an env file it assumes `/var/lib/dockmesh` (macOS: `/usr/local/var/dockmesh`). It does not read `DOCKMESH_DATA_DIR` from `/etc/dockmesh/dockmesh.env`, so a data root you chose elsewhere in the setup wizard is not found or removed; delete it by hand.

Before deleting, it checks that the directory contains `data/`, `stacks/` or `dockmesh.env`, and skips it with a warning otherwise. If the directory is a mount point, only its contents are removed.

Removing the data root deletes the database and its pre-migration snapshots, all stack files (including data that containers wrote through relative bind mounts into their stack directory) and the age key. Encrypted backups stored elsewhere can no longer be decrypted unless you kept a copy of the key. A local backup target inside the data root, such as the default `data/backups`, is deleted as well.

## What the script leaves behind

These are not touched by `uninstall.sh`. Remove them by hand if you want a clean host.

**Stacks.** Containers, networks and volumes of your stacks keep running. To remove a stack, run `docker compose down` in its directory before deleting the data root (add `-v` to delete its volumes as well). Dockmesh labels containers with the stack name as the Compose project name. If secrets encryption was on, the stack's variables are in `.env.age`, which Docker Compose cannot read. Decrypt the file with the age key when the compose file needs those variables:

```bash
sudo age -d -i /var/lib/dockmesh/data/secrets.age-key \n  -o /var/lib/dockmesh/stacks/<name>/.env /var/lib/dockmesh/stacks/<name>/.env.age
sudo docker compose -f /var/lib/dockmesh/stacks/<name>/compose.yaml -p <name> down
```

**Reverse proxy.** If the proxy was enabled, the `dockmesh-proxy` container (restart policy `unless-stopped`) keeps running and keeps serving its last configuration on ports 80 and 443. Remove it together with its certificate and config volumes:

```bash
sudo docker rm -f dockmesh-proxy
sudo docker volume rm dockmesh-caddy-data dockmesh-caddy-config
```

**Backups outside the data root.** Local backup targets elsewhere, for example under `/srv/backups`, and archives on remote targets are not touched.

**Helper images.** Dockmesh pulls a `caddy` image for the proxy (by default `caddy:2.11.7` pinned by digest, or the Caddy version you switched to, or `DOCKMESH_PROXY_IMAGE`), `alpine:3.24` pinned by digest for volume backups, restores and the volume browser, and `busybox:1.38` pinned by digest for volume tar streams, for deleting stack data owned by another user and for moving bind-mount data of containers deployed by older versions. Agents pull the same pinned `busybox:1.38` on their hosts, and the pinned `alpine:3.24` to browse a volume they can't read directly. Remove the images with `docker image rm` if nothing else uses them.

**Scanner.** `install.sh` installs `grype` into `/usr/local/bin`. Remove it with `sudo rm /usr/local/bin/grype` if you don't use it otherwise.

**Docker group.** Removing the `dockmesh` user also removes it from the `docker` group. Nothing else changes in your Docker setup.

## Remove an agent

Run this on the agent host:

```bash
sudo dockmesh-agent uninstall
```

It lists what it removes and what it keeps, then asks before it changes anything:

- **Removed:** the `dockmesh-agent` service and its updater units, the binary (`/usr/local/bin/dockmesh-agent`, and `<data>/bin` of agents in the old layout), `/etc/dockmesh-agent` with `agent.env`, and the agent's certificate, key, staging and update files in its data directory.
- **Kept:** the stacks directory and the `dockmesh-agent` user. Drop-in files you wrote for the service (`dockmesh-agent.service.d`) are kept as well.
- **Never touched:** containers, volumes, images and Docker itself. Stacks keep running; manage them with `docker compose` in `<stacks dir>/<stack>`.

When the agent is connected, it tells Dockmesh before it stops, and the host shows **Removed from host**. Dockmesh does not delete the host on its own: open the host and click **Delete host**. If the agent was not connected, revoke the host in Dockmesh yourself (**Hosts** › the host › **Maintenance** › **Revoke…**).

Options:

| Option | Effect |
|---|---|
| `--purge` | Also remove the data directory, the stacks directories and the `dockmesh-agent` user. Asks you to type the host name |
| `--yes` | Do not ask |
| `--dry-run` | Only list what would be removed and kept |

`--purge` removes only what Dockmesh wrote. A stacks directory that holds anything else, such as data a container wrote through a relative bind mount (`app/data/db.sqlite`), is kept as a whole, and so is the data directory around it and the user that owns them. A stacks directory you created yourself before you chose it in Dockmesh is emptied but not removed. Every kept item is listed with the reason. When the agent's data directory is also the data directory of a Dockmesh server on the same host (agents installed by old releases used `/var/lib/dockmesh`), only the agent's own files are removed.

If you revoke the host in Dockmesh first, the agent's certificate is no longer accepted. The agent then logs `This host was removed from Dockmesh. Run: sudo dockmesh-agent uninstall` once, and after ten minutes of refusals it retries only once an hour.

## Reinstall later

If you kept the data root, run the installer again:

```bash
curl -fsSL https://get.dockmesh.dev | sudo bash
```

Because the binary is gone it runs as a fresh install, but it finds the kept `dockmesh.env` in `/var/lib/dockmesh`, `/data` or `/usr/local/var/dockmesh` (or at `$DOCKMESH_DATA_DIR/dockmesh.env`), keeps the file unchanged and uses the database, stacks and keys next to it. It does not open the setup wizard; sign in with your existing account. A recreated `dockmesh` user can get a new UID, so the installer hands `data/` and `stacks/` back to it. The installer refuses to reuse an env file when the file or its directory is writable by group or others, or belongs to an account other than root or `dockmesh`.
