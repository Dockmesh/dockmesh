---
title: Upgrade
description: Upgrade the Dockmesh server and its agents, what happens on the first start of a new release, and how to roll back.
---

An upgrade replaces the `dockmesh` binary and restarts the service. On the first start the server takes a snapshot of the database and migrates it to the new schema. Containers keep running throughout; only the UI and API are down for the few seconds of the restart.

## Find out what changed

Dockmesh asks the GitHub releases API for the latest release 30 seconds after start and then every 120 minutes. When a newer release exists, every signed-in user gets an **Update available** banner with a **Release notes** link, and users with Settings & audit: Manage on everything get a notification in the bell once per new release (and once more after each server restart). Hiding the banner hides it for that release in this browser.

**Settings → Updates** (needs Settings & audit: Manage on everything) shows the running and the latest version, an **Update now** button (see [Update from the UI](#update-from-the-ui)) and the installer command as an alternative, with a copy button. A security release is marked **Security release** and lists its advisories and security fixes. Under **Automatic update checks** you can switch off **Check for updates** (useful without outbound internet access) and set the interval (15 to 10080 minutes). **Check now** runs a check immediately, also while automatic checks are off. The check never downloads or installs anything.

### Update channel

**Update channel** on the same page decides which releases the check offers:

- **Stable** (the default, also for installs from before the setting existed) offers only releases, never a pre-release.
- **Pre-release** also offers release candidates (`vX.Y.Z-rc.N`) and always offers the newest version of either kind, so an install on a candidate is offered the next candidate and then the final release.

Release candidates are for testing an upcoming release; don't use them on a production server. Only users who may install Dockmesh updates (Settings & audit: Manage on everything) can change the channel, and every change is written to the audit log. Saving a new channel runs a check right away. For a candidate the page shows that release's own installer, because get.dockmesh.dev only serves stable releases:

```bash
curl -fsSL https://github.com/dockmesh/dockmesh/releases/download/v0.6.0-rc.1/install.sh | sudo bash -s -- --version v0.6.0-rc.1
```

Agents don't have a channel of their own: the server updates them to its own version. Going back to **Stable** doesn't downgrade anything; the install is offered the next stable release that is newer than the one it runs.

Read the release notes before you upgrade. They are generated from [`CHANGELOG.md`](https://github.com/dockmesh/dockmesh/blob/main/CHANGELOG.md):

| Part | Meaning |
|---|---|
| Severity in the release header | `security` (upgrade as soon as possible), `recommended` or `normal` |
| **Highlights** | Three or four lines on what changed most; every minor release has them |
| **Upgrade notes** | Things someone has to do after the upgrade; notes only admins have to act on are marked **Admins** |
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
2. It picks `amd64` or `arm64` from `uname -m`, downloads `dockmesh_<os>_<arch>.tar.gz` from the GitHub release, checks the minisign signature of the release's `checksums.txt` and then the archive's SHA-256 against it. A missing or invalid signature, a mismatch, a missing `checksums.txt` or a missing entry aborts the upgrade. `DOCKMESH_SKIP_SIGNATURE=1` skips only the signature check (needed for releases before v0.6.0), `DOCKMESH_SKIP_CHECKSUM=1` skips both ([Verify a release](./verify-release.md)).
3. It copies the old binary to `/usr/local/bin/dockmesh.bak`, installs the new `dockmesh` and `dmctl`, and refreshes `install-agent.sh` and the agent binaries under `/usr/local/share/dockmesh/`. Linux archives carry the agent for both `amd64` and `arm64`, so the server can enroll and upgrade agents of either architecture.
4. It installs `grype` if it is missing, unless `DOCKMESH_NO_SCANNER=1`.
5. If the service still runs as root, it moves it to the `dockmesh` user: it chowns the data root to `dockmesh:dockmesh` (mode 700) and adds `User=dockmesh` and `Group=docker` to the unit.
6. It moves `dockmesh.env` to `/etc/dockmesh/dockmesh.env` and leaves a symlink at the old place (see [`dockmesh system migrate-layout`](../reference/dockmesh-cli.md#system-migrate-layout)). `DOCKMESH_DATA_DIR` is added for the data root the install already uses; no data moves. If the move fails, the installer prints a warning and the server keeps reading the file from its old place.
7. It restarts `dockmesh.service`, checks that the service came up with a new process started from the new binary, and that the installed binary reports the new version.

Apart from steps 5 and 6, `dockmesh.env`, the data root and the systemd unit are left as they are. A server that runs from a config the installer has not moved yet, for example after you replaced only the binary, shows an admin notice until the next installer upgrade moves it. To install a specific version, pass `--version v0.5.3` (`curl -fsSL https://get.dockmesh.dev | sudo bash -s -- --version v0.5.3`) or set `DOCKMESH_VERSION=v0.5.3`. A pre-release is only installed when you name it this way. (`DOCKMESH_CHANNEL=testing` still works in 0.6 but prints a warning and is removed in 0.7.0.) If the installed version is newer than the latest release, for example a release candidate, the installer stops instead of downgrading unless you name a version or set `DOCKMESH_FORCE=1`. Put the variables after `sudo`, for example `curl -fsSL https://get.dockmesh.dev | sudo DOCKMESH_VERSION=v0.5.3 bash`.

> **Warning:** If you installed with a custom `DOCKMESH_INSTALL_DIR`, pass the same value again. Otherwise the script does not find the existing binary and runs a fresh install into `/usr/local/bin`: it keeps `dockmesh.env` and the data, but rewrites the systemd unit to start the new copy, leaves the old binary where it is and makes no `.bak`.

Without systemd, the script replaces the binary but cannot restart the server; restart it yourself. On macOS it restarts the launchd job `dev.dockmesh.service`. If you built from source, pull the new source and rebuild with `make build`.

To upgrade by hand, for example on an air-gapped host:

```bash
TAG=v0.6.0
ARCH=amd64   # or arm64
curl -fLO "https://github.com/dockmesh/dockmesh/releases/download/$TAG/dockmesh_linux_$ARCH.tar.gz"
curl -fLO "https://github.com/dockmesh/dockmesh/releases/download/$TAG/checksums.txt"
curl -fLO "https://github.com/dockmesh/dockmesh/releases/download/$TAG/checksums.txt.minisig"
minisign -Vm checksums.txt -P RWRaHI41HpSgAcJDtFP8X2HnLIjHDLUdnj0GyCoGiGCdDW1rlLDWXzK1 | grep -Fqx "Trusted comment: dockmesh $TAG checksums.txt" || { echo "bad signature, aborting"; exit 1; }
sha256sum -c --ignore-missing checksums.txt || { echo "checksum mismatch, aborting"; exit 1; }
tar -xzf "dockmesh_linux_$ARCH.tar.gz"
sudo cp -a /usr/local/bin/dockmesh /usr/local/bin/dockmesh.bak
sudo install -m 0755 dockmesh dmctl /usr/local/bin/
sudo install -m 0755 dockmesh-agent-linux-amd64 dockmesh-agent-linux-arm64 /usr/local/share/dockmesh/bin/
sudo install -m 0755 install-agent.sh /usr/local/share/dockmesh/install-agent.sh
sudo systemctl restart dockmesh
```

### Update from the UI

**Update now** on **Settings → Updates** installs the newer release through the root updater described below. A dialog asks for confirmation, then shows each step: download and signature check, the wait for running stack operations, the restart, and the check of the new version. At the end it shows the result the root updater wrote: **Updated**, **rolled back** (with the reason, the old version runs again) or **refused** (nothing was changed). The outcome is also written to the audit log as a system entry. You can close the dialog while it runs; the update goes on, and reopening the page shows its progress again.

The button is only shown when the server can install updates itself:

- On a server that runs in a container, the page says to pull the new image and recreate the container instead.
- Before the root updater is installed, the page says to run the installer once to set it up.
- A pre-release is only installed on the **Pre-release** channel.

### Install through the API (root updater)

From v0.6.0 the installer also sets up a root updater on Linux with systemd (`dockmesh-update.path` and `dockmesh-update.service`, see [`dockmesh update apply`](../reference/dockmesh-cli.md#update-apply)). Once it is there, a user with Settings & audit: Manage on everything can install a newer release through the API:

```bash
curl -fsS -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"version":"v0.6.1"}' https://dockmesh.example.com/api/v1/system/update/install
```

1. The server downloads the archive, `checksums.txt` and its signature into `<data directory>/update/` and checks them. A bad signature or checksum stops here.
2. It waits up to 10 minutes for running deploys, scales, image updates and other stack operations on every host to finish; new ones are refused with "server update" in the meantime. If a stack is still busy after 10 minutes, nothing is installed.
3. It asks the root updater to install the release. The updater checks the files again on its own copy, stops the server, snapshots the database, installs the new binaries and units and starts the server. If the new version does not answer within 180 seconds, it puts back the old binaries, units and database and starts the old version.

After the restart the new version serves the UI and API at once, but starts no background job that changes containers or files (backup schedules, git polling and the pending git deploys a freed stack would start, auto-scaling, agent upgrades, audit retention, migration cleanup) until the root updater confirmed the update. A rollback within that window therefore never meets containers or files the new version already changed. A normal restart, without an update in flight, starts them right away. If an update stays unconfirmed for over an hour, for example because the updater was killed, admins get a critical banner and the jobs stay paused until `sudo systemctl start dockmesh-update.service` finishes or rolls back the update.

`GET /api/v1/system/update/install` shows how far an install got and, after the restart, the root updater's result (`last_result`). Admins also see a notice when the update was refused or rolled back; a successful update raises none. If the rollback itself failed, a critical banner says so; fix the cause and run `sudo systemctl start dockmesh-update.service` to finish it.

> **Note:** A rollback puts back the database as it was before the update. Everything changed in Dockmesh while the new version was running, at most about 180 seconds, is lost, audit entries included.

The installer one-liner keeps working as before and is the only way to install the first v0.6.0 release.

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

A snapshot from v0.4.x is deleted on the first start once the upgrade checks pass, because it holds secrets in plaintext (see What happens on the first start). Going back to v0.4.x after that needs the backup from step 1 or a system backup made on v0.4.x.

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
TAG=v0.6.0
ARCH=amd64   # or arm64
curl -fLO "https://github.com/dockmesh/dockmesh/releases/download/$TAG/dockmesh_linux_$ARCH.tar.gz"
curl -fLO "https://github.com/dockmesh/dockmesh/releases/download/$TAG/checksums.txt"
curl -fLO "https://github.com/dockmesh/dockmesh/releases/download/$TAG/checksums.txt.minisig"
minisign -Vm checksums.txt -P RWRaHI41HpSgAcJDtFP8X2HnLIjHDLUdnj0GyCoGiGCdDW1rlLDWXzK1 | grep -Fqx "Trusted comment: dockmesh $TAG checksums.txt" || { echo "bad signature, aborting"; exit 1; }
sha256sum -c --ignore-missing checksums.txt || { echo "checksum mismatch, aborting"; exit 1; }
tar -xzf "dockmesh_linux_$ARCH.tar.gz" "dockmesh-agent-linux-$ARCH"
sudo install -m 0755 -o dockmesh-agent -g dockmesh-agent "dockmesh-agent-linux-$ARCH" /var/lib/dockmesh-agent/bin/dockmesh-agent
sudo systemctl restart dockmesh-agent
```

The binary lives in the agent's data directory, `DOCKMESH_DATA_DIR` in `/etc/dockmesh-agent/agent.env` (`/var/lib/dockmesh-agent` for current installs). If the agent was installed with `--as-root`, use `-o root -g root`. The service refreshes the copy in `/usr/local/bin/dockmesh-agent` when it starts. Keep agents on the same version as the server.
