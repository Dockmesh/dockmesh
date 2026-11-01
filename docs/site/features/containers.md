---
title: Containers
description: Find containers across hosts, read their logs, open a shell, browse their files and start, stop or remove them.
---

**Containers** lists every container on the host selected in the host switcher, or on all hosts at once with **All hosts**. It includes containers that belong to stacks and standalone ones started outside Dockmesh.

## The list

The summary row counts containers, unhealthy ones, stopped ones (exited, paused, created) and hosts. If a host did not answer, a banner names it; its containers are missing from the list.

| Column | Shows |
|---|---|
| name · stack | Container name and the stack it belongs to, or **standalone** |
| state | Running state, with a restart counter when the container restarted since it was created |
| health | **healthy**, **unhealthy**, **starting**, or `—` without a healthcheck |
| image | Image reference; an arrow marks a newer image upstream (see [Image updates](./image-updates.md)) |
| uptime | Time since start |
| ports | Published ports, then internal ones |
| host | Shown with **All hosts** |

Filter with the search box (name, image, stack, ID) and the state pills **all**, **running**, **stopped** and **unhealthy**. Stopped containers are only loaded while **show stopped** is ticked.

Row buttons open the logs or a terminal, and start or stop the container. The **More** menu has **Restart**, **Stats**, **Inspect**, **Copy ID** and **Remove**. **Pause** in this menu does nothing (it shows "Not yet wired in this build"); pause from the container page instead.

Select several rows with the checkboxes to **Start**, **Stop**, **Restart** or **Remove** them together. Remove force-removes running containers; volumes are kept.

## The container page

Click a container to open it. The header shows state, image and host, plus the actions that fit the current state:

| State | Buttons |
|---|---|
| running | **Restart**, **Stop**, **Kill**, and under **More**: **Pause**, **Copy ID**, **Remove** |
| paused | **Resume**, **Kill**, and under **More**: **Copy ID**, **Remove** |
| stopped | **Start**, and under **More**: **Copy ID**, **Remove** |

**Kill** sends `SIGKILL` after a confirmation. **Remove** force-removes the container; named volumes and the image stay.

If the container belongs to a stack, changes made here are temporary: the next stack deploy recreates the container from `compose.yaml`. Stopping a stack's container does not stop the stack.

The tabs:

- **Overview**: live CPU, memory and network rates, plus identity, health, stack context, endpoints, mounts, networks and restart history.
- **Logs**: live log stream with level filters (**All**, **Error**, **Warn**, **Info**, **Fatal**), a text filter, and a **following** / **paused** toggle.
- **Terminal**: an interactive shell in the container, `sh` or `bash`. Needs `containers.exec`. Opening and closing a session is written to the audit log.
- **Inspect**: the full `docker inspect` output as JSON, with a copy button.
- **Network**: the networks the container is attached to, with IP and MAC address, and its ports (host-mapped or internal).
- **Files**: browse the container's filesystem (see below).
- **Updates**: check for a newer image and update in one click, with rollback. Local host only; see [Image updates](./image-updates.md).

Logs, stats, the terminal and inspect work for containers on remote hosts too; the data is relayed by the agent.

## Files

The **Files** tab lists a directory in the container with size, mode and modification time. Click a file to preview it; text is shown inline (large files are truncated, binary files are not shown), and every file can be downloaded.

With the terminal extra as well you can also **Edit** a text file and **Save** it, **Upload** a file into the current directory (up to 8 MiB), and **Delete** a file or symbolic link. Writing a file into a container is treated like a shell session, which is why it needs both extras. Delete runs `rm` in the container, so it needs a running container whose image has `rm`. Every upload, save and delete is written to the audit log with the host, the container and the path.

Previews and edits are limited to 1 MiB; larger files can be downloaded. Paths are resolved inside the container: symbolic links are followed within the container's filesystem, and `/proc`, `/sys`, `/dev` and device files can't be opened, even through a link.

The **Files** tab works on the local host and on agent hosts. An agent older than the server answers with a message to update it from the **Hosts** page.

## Permissions

| Action | Permission |
|---|---|
| List, inspect, stats | `containers.view` |
| Browse, preview and download files | the files extra (`containers.files`) |
| Stream logs | `containers.logs` |
| Terminal | `containers.exec` |
| Edit, upload and delete files | `containers.files` and `containers.exec` |
| Start, stop, restart, pause, kill, remove, image update | `containers.update` |

Container removal is gated by `containers.update`; the separate `containers.delete` permission is not used for it.

<!-- update after: rbac: scope read endpoints, limit grants, gate sensitive downloads -->
**Inspect** shows the container's full configuration, including its environment variables, and the inspect endpoint does not check host or stack scope. Treat `containers.view` as access to the secrets inside your containers, and grant it accordingly. See [Permissions](../concepts/permissions.md).
