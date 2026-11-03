---
title: Images
description: List, inspect, pull, remove and prune Docker images, and see which containers use them.
---

Images, volumes and networks share one page: **Resources** in the sidebar, with a tab for each. The **Images** tab lists the images on the host selected in the host switcher, or on every host in your scope with **All hosts**. If a host doesn't answer, a banner names it and its images are missing from the list.

## The list

The summary row shows the total number of images, dangling ones, the space a prune would reclaim, and how many are in use by a container.

| Column | Shows |
|---|---|
| **Repository : tag** | Reference; untagged images show `<none>:<none>` and a **Dangling** badge |
| **Size** | Size on disk |
| **Created** | Build time |
| **Used by** | **in use** when at least one container (running or stopped) uses the image |
| **Vulnerabilities** | Counts per severity from the last scan, or a scan button |
| **Host** | Host that holds the image |

Filter with the search box (repository, tag or ID) and **Show**: **all**, **used**, **unused**, **dangling** and **CVE** (images whose last scan found critical or high vulnerabilities).

Scanning is described in [Vulnerability scanning](./vulnerability-scanning.md).

## Pulling an image

**Pull image** (needs `images.create`) asks for a reference such as `postgres:16-alpine` or `ghcr.io/acme/app:1.4.2`. While you type, the dialog suggests matching Docker Hub repositories; that search runs from your browser directly against `hub.docker.com`.

Pulls always run on the server host, whatever host is selected. If a saved [registry](./registries.md) matches the image's registry, its credentials are used. To get an image onto an agent host, deploy a stack that uses it there.

## Image details

Click an image to open its page. The tabs are:

- **Overview**: ID, tags, digests, architecture, creation time, author, size, and from the image config the command, entrypoint, working directory, user, exposed ports, environment and labels.
- **Vulnerabilities**: the scan report, with **Scan now** or **Re-scan** (needs `images.scan`).
- **Layers**: the layer list.
- **History**: meant for the build history, but it always shows **No history**, because the image data Dockmesh loads does not include it. Use `docker history` on the host.
- **Containers**: containers using this image, running or stopped.

**Re-pull** pulls the same `repository:tag` again on the server host, which fetches a newer image if the tag has moved. Running containers keep the old image until they are recreated; see [Image updates](./image-updates.md) for updating them.

## Removing and pruning

| Action | Where | Effect |
|---|---|---|
| Remove | trash icon in a row, **Remove image** on the detail page, or several selected rows with **Remove** | Force-removes the image. Docker refuses only while a running container uses it; an image used only by stopped containers is removed. |
| **Prune dangling** | Images tab | Removes dangling images only. Tagged images that no container uses are kept. |
| **System cleanup…** | page header | Prunes dangling images, unused volumes and unused networks in one go. See [Volumes](./volumes.md#removing-and-pruning) before you use it. |

Removing and pruning from the UI, **System cleanup…** included, always act on the server host, even when an agent host is selected and although the cleanup dialog speaks of the selected host. The API accepts `?host=<id>` on `DELETE /api/v1/images/{id}` and `POST /api/v1/images/prune` if you need to do this on an agent.

There is no scheduled prune. Call `POST /api/v1/images/prune` from cron with an [API token](./api-tokens.md) if you want one.

## Permissions

| Action | Permission |
|---|---|
| List and inspect | `images.view` |
| Pull | `images.create` |
| Remove and prune | `images.delete` |
| Scan | `images.scan` |

Each permission is checked on the host the action targets: listing and inspecting on the host you look at, pulling and scanning on the server host, removing and pruning on the host named by `?host=` (the server host by default). In the role editor they belong to the **Images, volumes, networks & registries** area: **View** gives `images.view`, **Manage** the other three. See [Permissions](../concepts/permissions.md).
