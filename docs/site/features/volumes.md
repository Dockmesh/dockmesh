---
title: Volumes
description: List and inspect Docker volumes, browse their files read-only, and create, remove or prune them.
---

The **Volumes** tab on the **Resources** page lists the Docker volumes on the host selected in the host switcher, or on every host with **All hosts**. Bind mounts are not volumes and do not appear here; you see them on the container page under **Mounts**.

## The list

The summary row counts volumes, orphans (no container attached), anonymous volumes and drivers in use.

| Column | Shows |
|---|---|
| name | Volume name |
| driver | Usually `local` |
| mountpoint | Path on the host |
| used by | Containers that mount the volume |
| created | Creation time |
| host | Shown with **All hosts** |

Filter with the search box and the pills **all**, **in use**, **orphan**, **anon** (auto-named) and **compose** (created by a Compose project).

Volume sizes are not shown.

## Volume details

Click a volume to open its page. **Overview** shows driver, mountpoint, scope, labels and options; **Containers** lists the containers that mount it; **Files** is the volume browser.

## Browsing files

The **Files** tab (needs `volumes.browse`) shows the volume's content as a read-only file tree. Click a folder to open it, click a file to preview it. Text files up to 1 MiB are shown inline; larger files show the first 1 MiB, binary files are not displayed. **Download** saves the previewed content, so for files over 1 MiB you only get the first 1 MiB.

You cannot upload, edit or delete files here. To change data in a volume, open a terminal in a container that mounts it (see [Containers](./containers.md)).

On the local host, Dockmesh reads the volume's mountpoint directly. Docker volume directories are usually readable by root only; when the server lacks access, it starts a short-lived `alpine` container with the volume mounted and reads through that. On a remote host the agent reads the mountpoint itself, without the helper container, so browsing fails when the agent does not run as root.

Every directory listing and every file read is written to the audit log with the path.

<!-- update after: fs: contain paths in volume browse, restore and git sources -->
> **Warning:** Paths are checked lexically, and symlinks inside a volume are followed. A volume whose content is controlled by a container can therefore expose files outside the volume on the host. Grant `volumes.browse` only to people you would trust with root on the host.

## Creating a volume

**Create volume** asks for a **Name**, a **Driver** (default `local`) and, with several hosts, a **Host**. The volume is always created on the local host; the host choice is not used. Stacks normally create their volumes on deploy, so you rarely need this.

## Removing and pruning

| Action | Effect |
|---|---|
| Delete (row, detail page or several rows) | Force-removes the volume. Docker still refuses while a container uses it. The data is gone. |
| **Prune orphan** | Calls Docker's volume prune for volumes no container uses. |
| **System cleanup…** | Prunes dangling images, unused volumes and unused networks together. |

What a prune removes is decided by Docker. On Docker Engine 23 and later, a prune without filters removes only unused **anonymous** volumes; named volumes stay. On older engines it removes every unused volume, named or not. Check your engine version (`docker version`) before you rely on either behaviour.

Creating, deleting and pruning volumes always act on the local host, even when a remote host is selected.

There is no undo. Take a backup of volumes whose data matters first; see [Backups](../operations/backups.md). Stack migration does not move volume data either; see [Migration and drain](./migration-and-drain.md).

## Permissions

| Action | Permission |
|---|---|
| List and inspect | `volumes.view` |
| Create | `volumes.create` |
| Delete and prune | `volumes.delete` |
| Browse and read files | `volumes.browse` |
