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
| Remove the 'dockmesh' service user? | yes | `userdel dockmesh` (Linux only), after killing its processes |

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

The script refuses a data root that is not a plain absolute path, has a symlink anywhere in it, or is `/` or a system directory such as `/etc`, `/usr`, `/var`, `/home`, `/root`, `/opt`, `/srv`, `/data` or `/mnt`. It prints why and leaves the directory alone; remove Dockmesh's files there by hand. It also skips a directory that holds none of `data/`, `stacks/`, `.setup/`, `update/` or `dockmesh.env`.

### What `--purge` removes

`--purge` removes only the files Dockmesh writes, from the data root and from `/var/lib/dockmesh` (macOS: `/usr/local/var/dockmesh`), where setup keeps its state before the wizard has chosen the data root:

- the database with its WAL, SHM and pre-migration and pre-update snapshots
- `secrets.env`, the age key `secrets.age-key`, `audit-genesis.sha256` and the agent and web certificates and keys
- the scanner, Git and proxy socket caches in `data/`, the update, agent release, setup and restore staging directories, and `dockmesh.env`
- every stack directory that holds only `compose.yaml`, `compose.<environment>.yaml`, `.env`, `.env.age`, `.dockmesh.meta.json` and empty directories

Everything else stays, and the script prints one `keep` line with the reason for each path, for example:

```text
keep    /srv/dockmesh/stacks/app: contains files not written by Dockmesh (e.g. data/db.sqlite)
```

That covers a stack directory with data a container wrote through a relative bind mount, a symlink, or any other file; local backups and audit archives such as `data/backups`; what a restore or a failed setup set aside; and anything the script doesn't know. A data root that is used by an agent on the same host is kept as well. The data root itself is removed only once it is empty. If it is a mount point, the mount point stays. `--purge` also removes `/etc/dockmesh`.

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

If an agent is installed on the same host as the server, `uninstall.sh` leaves it alone and says so.

Remove the host in Dockmesh first: **Hosts** → row menu → **Revoke…**, then type the host's name to confirm. Revoking invalidates the agent's certificate and closes its connection, but the agent service on the host keeps trying to reconnect, so stop it there as well.

`install-agent.sh` has no uninstall option. Look up the agent's data directory first; current installs use `/var/lib/dockmesh-agent`:

```bash
sudo grep DOCKMESH_DATA_DIR /etc/dockmesh-agent/agent.env
```

Then, on the agent host:

```bash
sudo systemctl disable --now dockmesh-agent
sudo rm /etc/systemd/system/dockmesh-agent.service
sudo systemctl daemon-reload
sudo rm -rf /etc/dockmesh-agent /var/lib/dockmesh-agent
sudo rm -f /usr/local/bin/dockmesh-agent
sudo userdel dockmesh-agent
```

> **Warning:** agents installed by older releases used `/var/lib/dockmesh`, which is also the default data root of the server. Never run `rm -rf /var/lib/dockmesh` on a host where the Dockmesh server is installed.

`/etc/dockmesh-agent/agent.env` still contains the enrollment token, so delete it even if you keep the rest. Containers that Dockmesh deployed on the host keep running; their stack files are mirrored under `stacks/` in the agent's data directory and are gone after the cleanup above.

## Reinstall later

If you kept the data root, run the installer again:

```bash
curl -fsSL https://get.dockmesh.dev | sudo bash
```

Because the binary is gone it runs as a fresh install, but it finds the kept `dockmesh.env` in `/var/lib/dockmesh`, `/data` or `/usr/local/var/dockmesh` (or at `$DOCKMESH_DATA_DIR/dockmesh.env`), keeps the file unchanged and uses the database, stacks and keys next to it. It does not open the setup wizard; sign in with your existing account. A recreated `dockmesh` user can get a new UID, so the installer hands `data/` and `stacks/` back to it. The installer refuses to reuse an env file when the file or its directory is writable by group or others, or belongs to an account other than root or `dockmesh`.
