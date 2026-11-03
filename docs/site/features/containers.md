---
title: Containers
description: Find containers across hosts, read their logs, open a shell, browse their files and start, stop or remove them.
---

**Containers** in the sidebar lists every container on the host selected in the host switcher, or on all hosts in your scope with **All hosts**. It includes containers that belong to stacks and standalone ones started outside Dockmesh.

## The list

The summary row shows how many containers run, how many need attention, how many are stopped, and the hosts. If a host doesn't answer, a banner names it; its containers are missing from the list.

| Column | Shows |
|---|---|
| **Name** | Container name, below it the stack (**stack** *name*), **standalone**, or **system** for [Dockmesh's labelled containers](#dockmeshs-own-containers), the short ID and a restart counter when the container restarted since it was created |
| **Status** | Running state and health: **running**, **starting**, **unhealthy**, **paused**, **restarting**, **exited** (with the exit code when it is not 0), **dead** |
| **Host** | The host (hidden in the **By host** layout) |
| **Image** | Image reference; **update** marks a newer image upstream (see [Image updates](./image-updates.md)) |
| **Ports** | The first published port and how many more, or **internal** when the container has only unpublished ports |
| **Uptime** | Time since start, for running containers |

The list is sorted by urgency, containers with problems first; click **Name**, **Status**, **Image** or **Uptime** to sort by it, **Reset** to go back. Filter with the search box (name, stack, image, ID) and **All**, **Running**, **Attention**, **Stopped** and **Standalone**. The layout switch shows a flat **List**, or groups the containers **By host** or **By stack**; standalone containers form their own group, where users with `stacks.adopt` find **Adopt…**.

A row's menu has **Open**, **Logs**, **Terminal**, **Inspect**, **Restart** and **Stop** (or **Start**), **Copy ID**, **Open stack** *name* for stack containers, **Open reverse proxy** for the proxy container, and **Remove…**.

Select several rows with the checkboxes (**system** rows have none) to **Start**, **Stop**, **Restart** or **Remove…** them together. Remove force-removes running containers; volumes are kept.

## The container page

Click a container to open it. The header shows state, image, uptime, restart count, host and ID, plus the actions that fit the current state:

| State | Buttons |
|---|---|
| running | **Restart**, **Stop**, and under the menu: **Pause**, **Kill**, **Copy ID**, **Open stack**, **Remove…** |
| paused | **Resume**, and under the menu: **Kill**, **Copy ID**, **Open stack**, **Remove…** |
| stopped | **Start**, and under the menu: **Copy ID**, **Open stack**, **Remove…** |

**Kill** sends `SIGKILL` after a confirmation. **Remove** force-removes the container; its volumes and the image stay.

If the container belongs to a stack, changes made here are temporary: the next stack deploy recreates the container from `compose.yaml`. Stopping a stack's container does not stop the stack.

The tabs:

- **Overview**: charts of CPU, memory, network and block I/O over time (see [Monitoring](./monitoring.md#charts)), plus identity, health, stack context, environment, endpoints, mounts, networks and restart history.
- **Logs**: live log stream with level filters (**All**, **Error**, **Warn**, **Info**, **Fatal**), a text filter, and a **following** / **paused** toggle.
- **Terminal**: an interactive shell in the container: pick `sh` or `bash`, then click **Attach to /bin/sh** (or **Attach to /bin/bash**). Opening and closing a session is written to the audit log.
- **Inspect**: the `docker inspect` output as JSON, with a copy button.
- **Network**: the networks the container is attached to, with IP and MAC address, its ports (host-mapped or internal) and the network throughput of the last 60 seconds.
- **Files**: browse the container's filesystem (see below).
- **Updates**: check for a newer image and update it, with rollback. Containers on the server host only; see [Image updates](./image-updates.md).

Tabs you lack the permission for are hidden. Logs, stats, the terminal, inspect and files work for containers on agent hosts too; the data is relayed by the agent.

## Files

The **Files** tab lists a directory in the container with size, mode and modification time. Click a file to preview it; text is shown inline (truncated at 1 MiB, binary files are not shown), and every file can be downloaded.

With the terminal extra as well you can also edit a text file and save it, upload a file into the current directory (up to 8 MiB), and delete a file or symbolic link. Writing a file into a container is treated like a shell session, which is why it needs both extras. Delete runs `rm` in the container, so it needs a running container whose image has `rm`. Every directory listing, file read, download, upload, save and delete is written to the audit log with the host, the container and the path.

Previews and edits are limited to 1 MiB; larger files can be downloaded. Paths are resolved inside the container: symbolic links are followed within the container's filesystem, and `/proc`, `/sys`, `/dev` and device files can't be opened, even through a link or a bind mount of the host's `/proc`, `/sys` or `/dev` (as monitoring containers such as node-exporter use). Files of [Dockmesh's own containers](#dockmeshs-own-containers) and of containers with the Docker socket mounted are not accessible.

Uploads and saves are written by Docker as root: a replaced file keeps its owner and permissions, a new file gets the owner of its directory. Setuid, setgid and sticky bits can't be set. Writing as root reaches files the container's own user can't change, such as root-owned files in the image or a writable bind mount of a host directory, so the files and terminal extras together can do more than a terminal in a container that runs as a normal user. Give both only to people you would trust with root in those containers.

The **Files** tab works on the server host and on agent hosts. An agent older than the server answers with a message to update it from the **Hosts** page.

## Dockmesh's own containers

The Dockmesh server (when it runs in a container), the `dockmesh-proxy` reverse proxy, agent containers and the short-lived helper containers Dockmesh starts are its own containers. The server recognises them by its own container ID, the `dockmesh.component` label or a `ghcr.io/dockmesh/` image. Only an admin can start, stop, restart, pause, kill, remove, update or open a terminal in one, and a backup hook can only run in one if an admin sets it up. The **Files** tab is closed for them for everyone, since they hold the secret key, the database, the proxy's TLS keys or an agent's certificate.

In the UI, containers with the `dockmesh.component` label (the proxy and the helpers) are marked **system** and shown read-only, also for admins; the proxy's page links to **Reverse proxy**. The server and agent containers carry no such label, so they are listed like other containers and show the usual buttons; the server refuses those actions for users who are not admins.

## Permissions

| Action | Permission |
|---|---|
| List, inspect, metrics and stats | `containers.view` |
| Stream logs | `containers.logs` |
| Start, stop, restart, pause, resume, kill | `containers.update` |
| Remove | `containers.delete` |
| Update and roll back the image | `containers.upgrade` (see [Image updates](./image-updates.md#permissions)) |
| Environment values in **Inspect** and on the **Overview** | the env extra (`containers.env`) |
| Terminal | the terminal extra (`containers.exec`) |
| Browse, preview and download files | the files extra (`containers.files`) |
| Edit, upload and delete files | `containers.files` and `containers.exec` |

In the role editor the **Containers** area gives `containers.view` and `containers.logs` at **View**, adds `containers.update` at **Operate**, and `containers.delete` and `containers.upgrade` at **Manage**. The three extras are granted separately.

Every permission is checked against the container's host and, for stack containers, its stack, so an assignment limited to some hosts, host tags or stacks only reaches those containers, also for inspect, metrics, logs and file downloads. Without the env extra, inspect returns the container's configuration without its environment variables. See [Permissions](../concepts/permissions.md).
