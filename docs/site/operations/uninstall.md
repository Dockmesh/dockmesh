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

`--dry-run` is not completely free of side effects: when the service user exists and its removal is selected, it still kills all processes of the `dockmesh` user, which stops a running server.

### How the data root is found

The script looks for `dockmesh.env` in `/var/lib/dockmesh`, `/usr/local/var/dockmesh`, `/opt/dockmesh` and `/data`, and uses the first directory where it finds one. It reads the asset paths from that file. Without an env file it assumes `/var/lib/dockmesh` (macOS: `/usr/local/var/dockmesh`).

Before deleting, it checks that the directory contains `data/`, `stacks/` or `dockmesh.env`, and skips it with a warning otherwise. If the directory is a mount point, only its contents are removed.

Removing the data root deletes the database, all stack files and the age key. Encrypted backups stored elsewhere can no longer be decrypted unless you kept a copy of the key. A local backup target inside the data root is deleted as well.

## What the script leaves behind

These are not touched by `uninstall.sh`. Remove them by hand if you want a clean host.

**Stacks.** Containers, networks and volumes of your stacks keep running. To remove a stack, run `docker compose down` in its directory before deleting the data root (add `-v` to delete its volumes as well). Dockmesh labels containers with the stack name as the Compose project name. If secrets encryption was on, the stack's variables are in `.env.age`, which Docker Compose cannot read. Decrypt the file with the age key when the compose file needs those variables:

```bash
cd /var/lib/dockmesh/stacks/<name>
sudo age -d -i ../../data/secrets.age-key -o .env .env.age
sudo docker compose -p <name> down
```

**Reverse proxy.** If the proxy was enabled, the `dockmesh-proxy` container (restart policy `unless-stopped`) keeps running and keeps serving its last configuration on ports 80 and 443. Remove it together with its certificate and config volumes:

```bash
sudo docker rm -f dockmesh-proxy
sudo docker volume rm dockmesh-caddy-data dockmesh-caddy-config
```

**Helper images.** Dockmesh pulls `caddy:2` for the proxy, `alpine:3.19` for volume backups and restores, `alpine:latest` for the volume browser, and `busybox:latest` for volume tar streams. Remove them with `docker image rm` if nothing else uses them.

**Scanner.** `install.sh` installs `grype` into `/usr/local/bin`. Remove it with `sudo rm /usr/local/bin/grype` if you don't use it otherwise.

**Docker group.** Removing the `dockmesh` user also removes it from the `docker` group. Nothing else changes in your Docker setup.

## Remove an agent

Remove the host in Dockmesh first: **Hosts** → row menu → **Revoke…**. This does not close an agent connection that is currently open, so stop the agent on the host as well.

`install-agent.sh` has no uninstall option. On the agent host:

```bash
sudo systemctl disable --now dockmesh-agent
sudo rm /etc/systemd/system/dockmesh-agent.service
sudo systemctl daemon-reload
sudo rm -rf /etc/dockmesh-agent /var/lib/dockmesh
sudo rm -f /usr/local/bin/dockmesh-agent
sudo userdel dockmesh-agent
```

> **Warning:** the agent uses `/var/lib/dockmesh`, which is also the default data root of the server. Never run `rm -rf /var/lib/dockmesh` on a host where the Dockmesh server is installed.

`/etc/dockmesh-agent/agent.env` still contains the enrollment token, so delete it even if you keep the rest. Containers that Dockmesh deployed on the host keep running; their stack files are mirrored under `/var/lib/dockmesh` and are gone after the cleanup above.

## Reinstall later

If you kept the data root, `install.sh` uses the existing database, stacks and keys again. Because the binary is gone, it runs as a fresh install, though, and rewrites `dockmesh.env` with default values plus `DOCKMESH_SETUP_FORCE=true`. Save the file before reinstalling and put it back afterwards:

```bash
sudo cp /var/lib/dockmesh/dockmesh.env /root/dockmesh.env.saved
curl -fsSL https://get.dockmesh.dev | sudo bash
sudo cp /root/dockmesh.env.saved /var/lib/dockmesh/dockmesh.env
sudo systemctl restart dockmesh
```
