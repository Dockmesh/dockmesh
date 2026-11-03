---
title: Volumes
description: List and inspect Docker volumes, browse their files read-only, and create, remove or prune them.
---

The **Volumes** tab on the **Resources** page lists the Docker volumes on the host selected in the host switcher, or on every host with **All hosts**. Bind mounts are not volumes and do not appear here; you see them on the container page under **Mounts**.

## The list

The summary row counts volumes (and how many come from stacks), orphaned volumes (no container attached), anonymous volumes and drivers in use.

| Column | Shows |
|---|---|
| Name | Volume name, with the owning stack for volumes created by a Compose project and an **anon** tag for auto-named ones |
| Driver | Usually `local` |
| Mountpoint | Path on the host |
| Used by | Number of containers that mount the volume, or **Orphan** |
| Created | Creation date |
| Host | Shown with **All hosts** |

Filter with the search box and the buttons **all**, **in use**, **orphan**, **anon** and **compose** (created by a Compose project). Hosts that could not be reached are named above the list.

Volume sizes are not shown; Docker only reports them through a per-volume `du`.

Users whose assignment is limited to a stack see the volumes of that stack's Compose project.

## Volume details

Click a volume to open its page. **Overview** shows driver, mountpoint, scope, creation time, labels and options; **Containers** lists the containers that mount it, with the mount path and mode; **Files** is the volume browser. **Delete** in the header removes the volume (see [Removing and pruning](#removing-and-pruning)).

## Browsing files

The **Files** tab shows the volume's content as a read-only file tree. Click a folder to open it, click a file to preview it. Text files up to 1 MiB are shown inline; of larger files only the first 1 MiB is shown, and binary files are not displayed. **Download** saves the content the preview loaded, so for files over 1 MiB you only get the first 1 MiB.

You cannot upload, edit or delete files here. To change data in a volume, open a terminal in a container that mounts it, or use the container's **Files** tab (see [Containers](./containers.md)).

Dockmesh reads the volume's mountpoint directly when it may. Docker volume directories are usually readable by root only; when access is denied, Dockmesh starts a short-lived helper container from `alpine:3.24` (pinned by digest) with the volume mounted read-only and reads through that. Agents do the same on their hosts.

Every path stays inside the volume. A requested path that would leave the volume is refused, and so is a symbolic link that points outside it, both when Dockmesh reads directly and through the helper. Symbolic links show up in the listing but are not followed out of the volume.

Every directory listing and every file read is written to the audit log with the path and the host.

Dockmesh's own volumes can only be browsed, backed up and restored into by an admin: `dockmesh-caddy-data` holds the proxy's ACME account and TLS private keys, `dockmesh-caddy-config` the proxy's full config with its credentials, and `dockmesh-agent` an agent's client certificate.

## Creating a volume

**Create volume** asks for a **Name**, a **Driver** (default `local`) and, with several hosts, a **Host**. The volume is always created on the server's host; the host choice is not used. Stacks normally create their volumes on deploy, so you rarely need this.

## Restoring data into a volume

The volume pages have no restore action. Restore a volume or stack backup from **Backups** instead: the restore dialog asks for the volume (or stack), the host, and whether to **Replace** the volume's contents or **Merge** into them. See [Backups](../operations/backups.md#restore).

## Removing and pruning

| Action | Effect |
|---|---|
| Delete (row, detail page or several selected rows) | Force-removes the volume. Docker still refuses while a container uses it. The data is gone. |
| **Prune orphan** | Calls Docker's volume prune for volumes no container uses. |
| **System cleanup…** | Prunes dangling images, unused volumes and unused networks together. |

What a prune removes is decided by Docker. On Docker Engine 23 and later, a prune without filters removes only unused **anonymous** volumes; named volumes stay. On older engines it removes every unused volume, named or not. Check your engine version (`docker version`) before you rely on either behaviour.

> **Warning:** Creating, deleting and pruning volumes always act on the server's host, even when an agent host is selected. Deleting a volume listed for an agent host removes the volume of the same name on the server's host, if there is one.

There is no undo. Take a backup of volumes whose data matters first; see [Backups](../operations/backups.md). Stack migration refuses stacks with named volumes; see [Migration and drain](./migration-and-drain.md).

## Permissions

| Action | Needs |
|---|---|
| List and inspect | **Images, volumes, networks & registries**: View |
| Create, delete and prune | **Images, volumes, networks & registries**: Manage on an assignment that covers the server's host |
| Browse and read files | The extra **Browse and read files in containers and volumes** on an assignment that covers the volume's host (everything, the host or one of its tags); an assignment on a single stack is not enough |

See [Permissions](../concepts/permissions.md).
