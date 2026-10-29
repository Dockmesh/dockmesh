---
title: Upgrading
description: What happens when you update a Dockmesh server to a new release.
---

To update a server installed with the installer script, run the installer again:

```bash
curl -fsSL https://get.dockmesh.dev | sudo bash
```

When `dockmesh` already exists in the install directory, the script switches to upgrade mode. It downloads and verifies the new release, keeps the old binary as `/usr/local/bin/dockmesh.bak`, installs the new `dockmesh` and `dmctl`, refreshes the agent installer, and restarts `dockmesh.service`. Your `dockmesh.env`, database, keys and stacks are not touched. **Settings → Updates** shows the same command with a copy button when an update is available.

On the first start the server applies pending database migrations before it serves requests. Migrations only go forward, so take a backup first if you want to be able to roll back.

When something needs attention after an upgrade, Dockmesh shows a notice as a banner at the top of the UI with a link to the page where you fix it. The notice disappears once the condition is fixed. Steps the server cannot check are listed in the release's **Upgrade notes** on the [releases page](https://github.com/dockmesh/dockmesh/releases).

Upgrading the server does not upgrade agents on remote hosts.

[Upgrade](../operations/upgrade.md) has the full procedure: backing up, installing a specific version, upgrading by hand, rolling back and upgrading agents.
