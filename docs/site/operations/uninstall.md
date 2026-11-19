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
| Remove data directory (DB, stacks, age-keys)? | **no** | Dockmesh's files in the data root, see below |
| Remove the 'dockmesh' service user? | yes | `userdel dockmesh` (Linux only), after killing its processes. Skipped while the account still owns the kept data root or `/etc/dockmesh` |

| Flag | Effect |
|---|---|
| `--yes` | Accept every default without asking. The data root is kept |
| `--purge` | Like `--yes`, and also removes Dockmesh's files from the data root. Cannot be undone |
| `--dry-run` | Print the commands and what would be kept instead of running them |

```bash
curl -fsSL https://get.dockmesh.dev/uninstall | sudo bash -s -- --yes
```

### How the data root is found

The script reads `DOCKMESH_DATA_DIR` from `/etc/dockmesh/dockmesh.env`, so a data root you chose in the setup wizard is found. Older configs without it name the database with `DOCKMESH_DB_PATH`; the data root is then the directory above `data/dockmesh.db`. `DOCKMESH_DB_PATH`, `DOCKMESH_STACKS_ROOT` and the secrets, age key and audit genesis paths in that file are followed too. The file is read as plain text and never run, and it is only used when it is a regular file that root owns and nobody else can write.

Without a data root in that file, the script looks for `dockmesh.env` (or the symlink to `/etc/dockmesh/dockmesh.env` an upgrade leaves) in `/var/lib/dockmesh`, `/usr/local/var/dockmesh`, `/opt/dockmesh` and `/data`, and uses the first directory where it finds one, else `/var/lib/dockmesh` (macOS: `/usr/local/var/dockmesh`).

The script refuses a data root that is not a plain absolute path, has a symlink anywhere in it, or is `/` or a system directory such as `/etc`, `/usr`, `/var`, `/home` or `/root`. It prints why and leaves the directory alone; remove Dockmesh's files there by hand. A data root of `/data`, `/opt`, `/srv` or `/mnt` gets Dockmesh's files removed, but the directory stays, and its `data/`, `stacks/` and other directories are removed only when the `dockmesh` account owns them. It also skips a directory that holds none of `data/`, `stacks/`, `.setup/`, `update/` or `dockmesh.env`.

### What `--purge` removes

`--purge` removes only the files Dockmesh writes, from the data root and from `/var/lib/dockmesh` (macOS: `/usr/local/var/dockmesh`), where setup keeps its state before the wizard has chosen the data root:

- the database with its WAL, SHM and pre-migration and pre-update snapshots
- `secrets.env`, the age key `secrets.age-key` with its key history, `audit-genesis.sha256` and the agent and web certificates and keys
- the scanner, Git and proxy socket caches in `data/`, the update, agent release, setup and restore staging directories, and `dockmesh.env`
- every stack directory that holds only `compose.yaml`, `compose.<environment>.yaml`, `.env`, `.env.age`, `.dockmesh.meta.json` and empty directories

Everything else stays, and the script prints one `keep` line with the reason for each path, for example:

```text
keep    /srv/dockmesh/stacks/app: contains files not written by Dockmesh (e.g. data/db.sqlite)
```

That covers a stack directory with data a container wrote through a relative bind mount, a directory a container mounts (even an empty one), a symlink, or any other file; all stacks when Docker can't be asked which directories containers mount; directories not owned by the `dockmesh` account; local backups and audit archives such as `data/backups`; what a restore or a failed setup set aside; and anything the script doesn't know. A data root that is used by an agent on the same host is kept as well. The data root itself is removed only once it is empty. If it is a mount point, the mount point stays. `--purge` stops the server first and also removes `/etc/dockmesh`. When something it keeps still belongs to the `dockmesh` account, the account stays too, so a new account can't inherit those files. The summary at the end lists every kept path again.

When the data root itself stays because something in it was kept, `--purge` leaves an empty `.dockmesh-purged` file in it. A new install then does not count the kept stacks and restore set-asides there as the data of an earlier install and starts in setup mode; a database, `secrets.env`, an age key or the agent CA that is still there keeps the new install closed as before. The setup wizard removes the file when it takes the directory over.

Encrypted backups stored elsewhere can no longer be decrypted unless you kept a copy of the age key. Run `--purge --dry-run` first to see what would be removed and kept.

## What the script leaves behind

These are not touched by `uninstall.sh`. Remove them by hand if you want a clean host.

**Stacks.** Containers, networks and volumes of your stacks keep running. To remove a stack, run `docker compose down` in its directory before purging the data root (add `-v` to delete its volumes as well). Dockmesh labels containers with the stack name as the Compose project name. If secrets encryption was on, the stack's variables are in `.env.age`, which Docker Compose cannot read. Decrypt the file with the age key when the compose file needs those variables:

```bash
sudo age -d -i /var/lib/dockmesh/data/secrets.age-key \n  -o /var/lib/dockmesh/stacks/<name>/.env /var/lib/dockmesh/stacks/<name>/.env.age
sudo docker compose -f /var/lib/dockmesh/stacks/<name>/compose.yaml -p <name> down
```

**Reverse proxy.** If the proxy was enabled, the `dockmesh-proxy` container (restart policy `unless-stopped`) keeps running and keeps serving its last configuration on ports 80 and 443. Remove it together with its certificate and config volumes:

```bash
sudo docker rm -f dockmesh-proxy
sudo docker volume rm dockmesh-caddy-data dockmesh-caddy-config
```

**Backups.** Local backup targets, in the data root (`data/backups`) or elsewhere such as `/srv/backups`, and archives on remote targets are not touched.

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

`--purge` removes only what Dockmesh wrote. A stacks directory that holds anything else, such as data a container wrote through a relative bind mount (`app/data/db.sqlite`), is kept as a whole, and so is the data directory around it and the user that owns them. A stacks directory you created yourself before you chose it in Dockmesh is emptied but not removed. Every kept item is listed with the reason. When the agent's data directory is also the data directory of a Dockmesh server on the same host (agents installed by old releases used `/var/lib/dockmesh`), only the agent's own files are removed. `uninstall.sh` on a server host leaves the agent alone and says so.

If you revoke the host in Dockmesh first, the agent's certificate is no longer accepted. The agent then logs `This host was removed from Dockmesh. Run: sudo dockmesh-agent uninstall` once, and after ten minutes of refusals it retries only once an hour.

## Reinstall later

If you kept the data root, run the installer again:

```bash
curl -fsSL https://get.dockmesh.dev | sudo bash
```

Because the binary is gone it runs as a fresh install, but it finds the kept `dockmesh.env` in `/var/lib/dockmesh`, `/data` or `/usr/local/var/dockmesh` (or at `$DOCKMESH_DATA_DIR/dockmesh.env`), keeps the file unchanged and uses the database, stacks and keys next to it. It does not open the setup wizard; sign in with your existing account. If the `dockmesh` user was removed anyway, a recreated one can get a new UID, so the installer hands `data/` and `stacks/` back to it.

After `--purge` the installer runs a new install in setup mode and prints the setup code. Choose a new data directory in the wizard if the old one still holds kept paths. The installer refuses to reuse an env file when the file or its directory is writable by group or others, or belongs to an account other than root or `dockmesh`.
