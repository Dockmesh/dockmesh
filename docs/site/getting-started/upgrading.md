---
title: Upgrading
description: Update a single Dockmesh server to a new release and know what happens on the first start.
---

This page covers updating one server installed with the installer script. For agents on remote hosts, rollback and a checklist, see [Upgrade](../operations/upgrade.md).

## Before you upgrade

Read the release notes on the [releases page](https://github.com/dockmesh/dockmesh/releases), in particular the **Upgrade notes** section, which lists anything you have to do afterwards.

Take a copy of the database. `db backup` writes a consistent snapshot while the server is running and refuses to overwrite an existing file:

```bash
sudo dockmesh db backup --out /root/dockmesh-before-upgrade.db
```

A run of a **Whole dockmesh** backup job also works; see [Backups](../operations/backups.md).

## Run the installer again

```bash
curl -fsSL https://get.dockmesh.dev | sudo bash
```

When `dockmesh` already exists in the install directory, the script switches to upgrade mode:

1. It resolves the target release. If the installed version is already that release it stops without changes; set `DOCKMESH_FORCE=1` to reinstall anyway.
2. It downloads and verifies the release archive.
3. It copies the current binary to `/usr/local/bin/dockmesh.bak`, then installs the new `dockmesh` and `dmctl` and refreshes the agent installer and agent binary in `/usr/local/share/dockmesh/`.
4. It installs `grype` if it is missing, unless `DOCKMESH_NO_SCANNER=1`.
5. If the systemd service still runs as root (very old installs), it moves it to the `dockmesh` user.
6. It restarts `dockmesh.service` and waits for it to become active.

Your `dockmesh.env`, database, keys and stacks are not touched. **Settings → Updates** shows the same command with a copy button when an update is available.

To install a specific version, set `DOCKMESH_VERSION`:

```bash
curl -fsSL https://get.dockmesh.dev | sudo DOCKMESH_VERSION=v0.3.3 bash
```

> **Warning:** If you installed with a custom `DOCKMESH_INSTALL_DIR`, pass the same value again. Otherwise the script does not find the existing binary, runs a fresh install, and overwrites `dockmesh.env` and the systemd unit with defaults, including `DOCKMESH_SETUP_FORCE=true`.

Without systemd, the script replaces the binary but cannot restart the server; restart it yourself. On macOS it restarts the launchd job `dev.dockmesh.service`.

If you built from source or run the locally built container, pull the new source and rebuild (`make build`, or `docker compose up -d --build`).

## What happens on the first start

The server applies pending database migrations before it starts serving requests. They are embedded in the binary, applied in order, and recorded in the `schema_migrations` table, so each runs once. If a migration fails, the server logs `db migrate failed` and exits; check `journalctl -u dockmesh`. `sudo dockmesh db migrate` applies migrations without starting the server.

Migrations only go forward. The previous binary is kept as `dockmesh.bak`, but after a migration it may not work with the upgraded database; to go back, restore the database copy you took as well. [Upgrade](../operations/upgrade.md) has the steps.

Some releases sign everyone out once, for example when the session format changes. The sign-in page then says why; sign in again.

## Notices after an upgrade

When something needs attention after an upgrade, Dockmesh shows a notice as a banner at the top of the UI and at the top of the notifications drawer, with a link to the page where you fix it. Examples are API tokens created before tokens had owners. A notice disappears on its own once the condition is fixed; non-critical notices can be hidden from the banner.

Notices only cover conditions the server can detect. Steps listed in the release's **Upgrade notes** that the server cannot check are not shown in the UI.

## The update check

The server asks the GitHub releases API for the latest release 30 seconds after start and then every 120 minutes. When a newer version exists, users who can see system information get an **Update available** banner with a link to the release notes, and a notification is posted once per new release. The banner can be dismissed per version.

The check never downloads or installs anything; you upgrade with the installer as above.

Under **Settings → Updates** you can turn **Automatic update checks** off (useful without outbound internet access), change the **Check interval (minutes)** (15 to 10080), and run **Check now**, which needs the `system.upgrade` permission.

## Agents

Upgrading the server does not upgrade agents on remote hosts. How and when agents follow is described in [Upgrade](../operations/upgrade.md).
