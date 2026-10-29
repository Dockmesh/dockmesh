---
title: Upgrade
description: Upgrade the Dockmesh server and its agents, and roll back if something goes wrong.
---

An upgrade replaces the `dockmesh` binary and restarts the service. The database schema is migrated automatically on start. Containers keep running throughout. Only the UI and API are down for the few seconds of the restart.

## Find out what changed

Dockmesh asks the GitHub releases API for the latest release 30 seconds after start and then every 120 minutes. When one is out, users who can see system information get an **Update available** banner with a **Release notes** link, and a notification is posted once per new release. **Settings** → **Updates** shows the current and latest version and the command to run; there you can also turn **Automatic update checks** off (useful without outbound internet access), change the **Check interval (minutes)** (15 to 10080), and run **Check now** (needs `system.upgrade`). The check never downloads or installs anything.

Read the release notes before you upgrade. Releases after v0.3.3 take their notes from [`CHANGELOG.md`](https://github.com/dockmesh/dockmesh/blob/main/CHANGELOG.md):

- The release header carries a severity: `security`, `recommended` or `normal`.
- A line `Minimum upgrade from: X.Y.Z` means older installs must first upgrade to that version.
- **Upgrade notes** list what someone has to do after the upgrade, marked for `all` users or `admins` only.

Some of these steps are also checked by the server itself after the upgrade. The result appears as a notice banner at the top of the UI for the users concerned (for example admins), and stays until the problem is fixed. Critical notices cannot be dismissed. Older release notes are on the [releases page](https://github.com/dockmesh/dockmesh/releases).

## 1. Back up

Take a copy you can roll back to. Database migrations only go forward, and there are no down-migrations. The simplest safe copy is the whole data root, taken while the service is stopped:

```bash
sudo systemctl stop dockmesh
sudo tar -czf /root/dockmesh-pre-upgrade-$(date +%F).tar.gz -C /var/lib dockmesh
sudo systemctl start dockmesh
```

This includes the database, keys, PKI, `stacks/` and `dockmesh.env`. If your local backup target lives under `/var/lib/dockmesh`, exclude it with `--exclude`. A system backup job is also fine, as long as you have a copy of the age key (see [Disaster recovery](./disaster-recovery.md#export-the-age-key)).

## 2. Install the new release

Run the installer again:

```bash
curl -fsSL https://get.dockmesh.dev | sudo bash
```

When `/usr/local/bin/dockmesh` already exists, the installer switches to upgrade mode:

1. If the installed version is already the latest release, it stops without changing anything. `DOCKMESH_FORCE=1` reinstalls anyway.
2. It downloads `dockmesh_<os>_<arch>.tar.gz` from the GitHub release and checks its SHA-256 against the release's `checksums.txt`. A mismatch aborts. If `checksums.txt` or the entry is missing, it prints a warning and continues.
3. It copies the old binary to `/usr/local/bin/dockmesh.bak`, installs the new one, updates `dmctl`, and refreshes the agent installer and the agent binary for the server's own OS and architecture under `/usr/local/share/dockmesh/`.
4. It installs `grype` if it is missing, unless `DOCKMESH_NO_SCANNER=1`.
5. It moves installs that still run as root to the `dockmesh` user, then restarts `dockmesh.service` and checks that it comes back up.

`dockmesh.env`, the data root and the systemd unit are left as they are. To install a specific version, set `DOCKMESH_VERSION=v0.3.3`; `DOCKMESH_CHANNEL=testing` includes pre-releases.

> **Warning:** If you installed with a custom `DOCKMESH_INSTALL_DIR`, pass the same value again. Otherwise the script does not find the existing binary, runs a fresh install, and overwrites `dockmesh.env` and the systemd unit with defaults, including `DOCKMESH_SETUP_FORCE=true`.

Without systemd, the script replaces the binary but cannot restart the server; restart it yourself. On macOS it restarts the launchd job `dev.dockmesh.service`. If you built from source or run the locally built container, pull the new source and rebuild (`make build`, or `docker compose up -d --build`).

To upgrade by hand, for example on an air-gapped host:

```bash
TAG=v0.3.3
curl -fLO "https://github.com/dockmesh/dockmesh/releases/download/$TAG/dockmesh_linux_amd64.tar.gz"
curl -fLO "https://github.com/dockmesh/dockmesh/releases/download/$TAG/checksums.txt"
sha256sum -c --ignore-missing checksums.txt
tar -xzf dockmesh_linux_amd64.tar.gz dockmesh dmctl
sudo cp -a /usr/local/bin/dockmesh /usr/local/bin/dockmesh.bak
sudo install -m 0755 dockmesh dmctl /usr/local/bin/
sudo systemctl restart dockmesh
```

## 3. Verify

```bash
dockmesh version
systemctl status dockmesh
journalctl -u dockmesh --since "5 min ago"
```

Pending migrations run in order at startup, each in its own transaction. If one fails, the server logs `db migrate failed` and exits, and systemd keeps restarting it. In that case roll back.

Some releases sign everyone out once, for example when the session format changes; the sign-in page then says why. After the restart, sign in, check that every host under **Hosts** is online, and open one or two stacks. Then work through the release's upgrade notes.

## Rolling back

To go back to the previous binary:

```bash
sudo mv /usr/local/bin/dockmesh.bak /usr/local/bin/dockmesh
sudo systemctl restart dockmesh
```

The older binary does not check the schema version. It starts on a database that the newer version has already migrated, and whether it then works depends on what the migrations changed. For a clean rollback, restore the copy from step 1 as well. Changes made after the upgrade are lost.

```bash
sudo systemctl stop dockmesh
sudo mv /usr/local/bin/dockmesh.bak /usr/local/bin/dockmesh
sudo rm -rf /var/lib/dockmesh
sudo tar -xzf /root/dockmesh-pre-upgrade-2026-10-29.tar.gz -C /var/lib
sudo systemctl start dockmesh
```

## Upgrading agents

Agents are not upgraded together with the server. Each agent reports its version when it connects, and the **Hosts** page shows how many hosts are behind the server version. Any difference from the server version counts as outdated.

Three upgrade modes exist. The default is `manual`:

| Mode | Behaviour |
|---|---|
| `manual` | Nothing happens until you start it: **Upgrade agent** in a host's row menu, or select several hosts and click **Bulk upgrade** |
| `auto` | Every outdated, connected agent is told to upgrade on the next check (every 60 seconds) |
| `staged` | Each check upgrades `stage_percent` of the outdated agents (default 10, at least one) |

The UI shows the mode but cannot change it. Use the API, with `hosts.update`:

```bash
curl -X PUT -H "Authorization: Bearer dmt_xxxxxxxx" -H "Content-Type: application/json" \
  -d '{"mode": "staged", "stage_percent": 25}' \
  https://dockmesh.example.com/api/v1/agents/upgrade-policy
```

`POST /api/v1/agents/upgrade-policy/run` runs a check right away.

<!-- update after: agents: pin the CA at enrollment and verify self-upgrades -->
During an upgrade the agent downloads `dockmesh-agent-linux-<arch>` from the server, replaces its own binary and restarts. The download is not checked against a checksum. The server can only hand out binaries that are in its `DOCKMESH_BINARY_DIR` (`/usr/local/share/dockmesh/bin`), and `install.sh` only puts the agent for the server's own architecture there. An arm64 agent behind an amd64 server cannot upgrade itself until you place `dockmesh-agent-linux-arm64` in that directory.

### Upgrade an agent by hand

On the agent host, take the agent binary from the release tarball for that host's architecture:

```bash
TAG=v0.3.3
ARCH=amd64   # or arm64
curl -fLO "https://github.com/dockmesh/dockmesh/releases/download/$TAG/dockmesh_linux_$ARCH.tar.gz"
curl -fLO "https://github.com/dockmesh/dockmesh/releases/download/$TAG/checksums.txt"
sha256sum -c --ignore-missing checksums.txt
tar -xzf "dockmesh_linux_$ARCH.tar.gz" dockmesh-agent
sudo install -m 0755 -o dockmesh-agent -g dockmesh-agent dockmesh-agent /var/lib/dockmesh/bin/dockmesh-agent
sudo systemctl restart dockmesh-agent
```

If the agent was installed with `--as-root`, use `-o root -g root`. Keep agents on the same version as the server.
