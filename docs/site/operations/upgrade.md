---
title: Upgrade
description: Upgrade the Dockmesh server and its agents, what happens on the first start of a new release, and how to roll back.
---

An upgrade replaces the `dockmesh` binary and restarts the service. On the first start the server takes a snapshot of the database and migrates it to the new schema. Containers keep running throughout; only the UI and API are down for the few seconds of the restart.

## Find out what changed

Dockmesh asks the GitHub releases API for the latest release 30 seconds after start and then every 120 minutes. When a newer release exists, every signed-in user gets an **Update available** banner with a **Release notes** link, and users with Settings & audit: Manage on everything get a notification in the bell once per new release (and once more after each server restart). Hiding the banner hides it for that release in this browser.

**Settings → Updates** (needs Settings & audit: Manage on everything) shows the running and the latest version and the command to run, with a copy button. Under **Automatic update checks** you can switch off **Check for updates** (useful without outbound internet access) and set the interval (15 to 10080 minutes). **Check now** runs a check immediately, also while automatic checks are off. The check never downloads or installs anything.

Read the release notes before you upgrade. They are generated from [`CHANGELOG.md`](https://github.com/dockmesh/dockmesh/blob/main/CHANGELOG.md):

| Part | Meaning |
|---|---|
| Severity in the release header | `security` (upgrade as soon as possible), `recommended` or `normal` |
| **Highlights** | Three or four lines on what changed most; every minor release has them |
| **Upgrade notes** | Things someone has to do after the upgrade, marked for everyone or for admins only |
| "Direct upgrades are supported from X.Y.Z or later" | Older installs must first upgrade to that version. Neither the installer nor the server checks this, so check it yourself |

The release notes of the running version are also compiled into the binary. `GET /api/v1/system/whats-new` returns them, together with the notes of a newer release if one was found. The web UI does not show them; use the [releases page](https://github.com/dockmesh/dockmesh/releases) instead.

## 1. Back up

Take a copy you can roll back to. Migrations only go forward; there are no down-migrations. The server takes its own database snapshot before migrating (see [What happens on the first start](#what-happens-on-the-first-start)), but that covers the database only. The safest copy is the whole data root, taken while the service is stopped:

```bash
sudo systemctl stop dockmesh
sudo tar -czf /root/dockmesh-pre-upgrade-$(date +%F).tar.gz -C /var/lib dockmesh
sudo systemctl start dockmesh
```

This includes the database, keys, PKI, `stacks/` and `dockmesh.env`. If your local backup target lives under `/var/lib/dockmesh`, exclude it with `--exclude`. A Dockmesh system backup is also fine, as long as you have a copy of the age key (see [Disaster recovery](./disaster-recovery.md#export-the-age-key)).

## 2. Install the new release

Run the installer again:

```bash
curl -fsSL https://get.dockmesh.dev | sudo bash
```

When `/usr/local/bin/dockmesh` already exists, the installer switches to upgrade mode:

1. If the installed version is already the latest release, it stops without changing anything. `DOCKMESH_FORCE=1` reinstalls anyway.
2. It picks `amd64` or `arm64` from `uname -m`, downloads `dockmesh_<os>_<arch>.tar.gz` from the GitHub release and checks its SHA-256 against the release's `checksums.txt`. A mismatch, a missing `checksums.txt` or a missing entry aborts the upgrade; `DOCKMESH_SKIP_CHECKSUM=1` skips the check.
3. It copies the old binary to `/usr/local/bin/dockmesh.bak`, installs the new `dockmesh` and `dmctl`, and refreshes `install-agent.sh` and the agent binaries under `/usr/local/share/dockmesh/`. Linux archives carry the agent for both `amd64` and `arm64`, so the server can enroll and upgrade agents of either architecture.
4. It installs `grype` if it is missing, unless `DOCKMESH_NO_SCANNER=1`.
5. If the service still runs as root, it moves it to the `dockmesh` user: it chowns the data root to `dockmesh:dockmesh` (mode 700) and adds `User=dockmesh` and `Group=docker` to the unit.
6. It restarts `dockmesh.service`, checks that the service came up with a new process started from the new binary, and that the installed binary reports the new version.

Apart from step 5, `dockmesh.env`, the data root and the systemd unit are left as they are. To install a specific version, set `DOCKMESH_VERSION=v0.5.0`; `DOCKMESH_CHANNEL=testing` includes pre-releases. Put the variables after `sudo`, for example `curl -fsSL https://get.dockmesh.dev | sudo DOCKMESH_VERSION=v0.5.0 bash`.

> **Warning:** If you installed with a custom `DOCKMESH_INSTALL_DIR`, pass the same value again. Otherwise the script does not find the existing binary and runs a fresh install into `/usr/local/bin`: it keeps `dockmesh.env` and the data, but rewrites the systemd unit to start the new copy, leaves the old binary where it is and makes no `.bak`.

Without systemd, the script replaces the binary but cannot restart the server; restart it yourself. On macOS it restarts the launchd job `dev.dockmesh.service`. If you built from source or run the locally built container, pull the new source and rebuild (`make build`, or `docker compose up -d --build`).

To upgrade by hand, for example on an air-gapped host:

```bash
TAG=v0.5.0
ARCH=amd64   # or arm64
curl -fLO "https://github.com/dockmesh/dockmesh/releases/download/$TAG/dockmesh_linux_$ARCH.tar.gz"
curl -fLO "https://github.com/dockmesh/dockmesh/releases/download/$TAG/checksums.txt"
sha256sum -c --ignore-missing checksums.txt || { echo "checksum mismatch, aborting"; exit 1; }
tar -xzf "dockmesh_linux_$ARCH.tar.gz"
sudo cp -a /usr/local/bin/dockmesh /usr/local/bin/dockmesh.bak
sudo install -m 0755 dockmesh dmctl /usr/local/bin/
sudo install -m 0755 dockmesh-agent-linux-amd64 dockmesh-agent-linux-arm64 /usr/local/share/dockmesh/bin/
sudo install -m 0755 install-agent.sh /usr/local/share/dockmesh/install-agent.sh
sudo systemctl restart dockmesh
```

## What happens on the first start

Before it serves requests, the server brings the database up to date:

1. **Snapshot.** If migrations are pending on an existing database, it first writes a copy of the database next to it: `dockmesh.db.pre-migrate-<UTC time>.bak` (for example `/var/lib/dockmesh/data/dockmesh.db.pre-migrate-20261103T091500.000Z.bak`), owned like the database and readable only by its owner (mode 600). It keeps the newest three snapshots and deletes older ones. If a migration fails on every start, the snapshot from before the first attempt is reused instead of being rotated out. If the snapshot cannot be written (for example the disk is full), no migration runs and the server exits; free space next to the database, or set `DOCKMESH_SKIP_PREMIGRATE_BACKUP=1` in `dockmesh.env` to migrate without a snapshot.
2. **Migrations.** The migrations ship inside the binary. Each pending one runs in its own transaction and is recorded in the `schema_migrations` table only when it succeeds, so a failed migration leaves the database as it was and is retried on the next start. If one fails, the server logs `db migrate failed` and exits, and systemd keeps restarting it. In that case roll back.
3. **Notices.** Some changes need a person to act, for example reviewing access after a permission change. The server checks for these conditions and raises a notice for the users concerned (often admins only). A notice disappears by itself once the condition is fixed.

Notices about something that stopped working show as a banner at the top of the UI, with a link to the page where you fix it. **Hide for 24 h** moves such a notice to **Needs attention** for a day; it comes back while the problem lasts. Other warning and critical notices are listed under **Needs attention** on the **Overview** page and on the **Activity** page. Info notices, which only announce a change, appear in the notification feed on the **Activity** page. Critical notices that are not in the banner cannot be hidden. Steps the server cannot check are listed only in the release's **Upgrade notes**.

Snapshots are not included in system backups. A snapshot from before v0.5.0 holds backup credentials and secret variables in plaintext. With secrets encryption on, the server deletes such snapshots on the first start once every migration is applied, the database passes SQLite's integrity check and every stored secret is encrypted and readable with the current key. It then compacts the database so the old values don't stay in its free space either. An info notice **Pre-upgrade database copies removed** lists the deleted files; from then on the only way back to v0.4.x is a system backup made on v0.4.x. If a check fails, the snapshot is kept and the warning notice **Pre-upgrade database copies still hold plaintext secrets** says why; delete it once Dockmesh works. Snapshots of a database that already stores its secrets encrypted are kept as restore points.

## 3. Verify

```bash
dockmesh version
systemctl status dockmesh
journalctl -u dockmesh --since "5 min ago"
```

The log shows `database snapshot written before migration` with the snapshot's path when migrations ran.

Some releases sign everyone out once, for example when the session format changes; the sign-in page then says "You were signed out after a security update." After the restart, sign in, check the notice banner and **Needs attention**, check that every host under **Hosts** is online, and open one or two stacks. Then work through the release's upgrade notes.

## Rolling back

Putting only the previous binary back is not a supported downgrade. Migrations only go forward, and the older binary does not check the schema version: it starts on the migrated database, and whether it then works depends on what the migrations changed. From v0.5.0 back to v0.4.2 it does not start at all (`setup state init failed ... no such column: role`).

The way back is the previous binary together with the pre-migration snapshot the upgrade wrote. Changes made after the upgrade are lost.

```bash
sudo systemctl stop dockmesh
sudo mv /usr/local/bin/dockmesh.bak /usr/local/bin/dockmesh
sudo sh -c 'ls /var/lib/dockmesh/data/dockmesh.db.pre-migrate-*.bak'
sudo rm -f /var/lib/dockmesh/data/dockmesh.db-wal /var/lib/dockmesh/data/dockmesh.db-shm
sudo cp -p /var/lib/dockmesh/data/dockmesh.db.pre-migrate-20261103T091500.000Z.bak /var/lib/dockmesh/data/dockmesh.db
sudo systemctl start dockmesh
```

Use the newest snapshot that was written by the upgrade you are undoing (the log line from step 3 names it). The snapshot holds the database only. To undo everything else as well, restore the copy from step 1 instead:

```bash
sudo systemctl stop dockmesh
sudo mv /usr/local/bin/dockmesh.bak /usr/local/bin/dockmesh
sudo rm -rf /var/lib/dockmesh
sudo tar -xzf /root/dockmesh-pre-upgrade-2026-11-03.tar.gz -C /var/lib
sudo systemctl start dockmesh
```

## Upgrading agents

Agents are not upgraded together with the server. Each agent reports its version and build when it connects; an agent whose build differs from the server's counts as outdated, and the **Hosts** page shows how many connected agents are behind.

Three upgrade modes exist. The default is `manual`:

| Mode | Behaviour |
|---|---|
| `manual` | Nothing happens until you start it: **Upgrade agent** in a host's row menu, or select several hosts and click **Upgrade** |
| `auto` | Every outdated, connected agent is told to upgrade when the server starts and on every check (every 60 seconds) |
| `staged` | Each check upgrades `stage_percent` of the outdated agents (default 10, at least one) |

In `auto` and `staged` mode the server tries each agent at most three times per new agent build, at least five minutes apart. **Upgrade agent** always goes through. Upgrading an agent that already runs the server's build is refused with an explanation.

The **Hosts** page shows the mode and, outside `manual`, a **Run policy now** button, but cannot change the mode. Use the API (needs Hosts: Manage on everything, because the policy applies to every host):

```bash
curl -X PUT -H "Authorization: Bearer dmt_xxxxxxxx" -H "Content-Type: application/json" \
  -d '{"mode": "staged", "stage_percent": 25}' \
  https://dockmesh.example.com/api/v1/agents/upgrade-policy
```

`POST /api/v1/agents/upgrade-policy/run` runs a check right away.

During an upgrade the server sends the agent the SHA-256 of `dockmesh-agent-linux-<arch>` for the agent's architecture, taken from `DOCKMESH_BINARY_DIR` (`/usr/local/share/dockmesh/bin` on installs made by `install.sh`, `./bin` when the variable is unset). The agent downloads that file from the server's agent listener over HTTPS with its client certificate, checks it against the checksum, replaces its own binary and restarts. If the file for an architecture is missing from `DOCKMESH_BINARY_DIR`, agents of that architecture cannot be upgraded until you put it there.

### Upgrade an agent by hand

On the agent host, take the agent binary from the release archive. Linux archives contain both architectures:

```bash
TAG=v0.5.0
ARCH=amd64   # or arm64
curl -fLO "https://github.com/dockmesh/dockmesh/releases/download/$TAG/dockmesh_linux_$ARCH.tar.gz"
curl -fLO "https://github.com/dockmesh/dockmesh/releases/download/$TAG/checksums.txt"
sha256sum -c --ignore-missing checksums.txt
tar -xzf "dockmesh_linux_$ARCH.tar.gz" "dockmesh-agent-linux-$ARCH"
sudo install -m 0755 -o dockmesh-agent -g dockmesh-agent "dockmesh-agent-linux-$ARCH" /var/lib/dockmesh-agent/bin/dockmesh-agent
sudo systemctl restart dockmesh-agent
```

The binary lives in the agent's data directory, `DOCKMESH_DATA_DIR` in `/etc/dockmesh-agent/agent.env` (`/var/lib/dockmesh-agent` for current installs). If the agent was installed with `--as-root`, use `-o root -g root`. The service refreshes the copy in `/usr/local/bin/dockmesh-agent` when it starts. Keep agents on the same version as the server.
