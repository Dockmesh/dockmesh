---
title: Images
description: List, inspect, pull, remove and prune Docker images, and see which containers use them.
---

Images, volumes and networks share one page: **Resources** in the sidebar, with a tab for each. The **Images** tab lists the images on the host selected in the host switcher, or on every host with **All hosts**.

## The list

The summary row shows the total number of images, dangling ones, the space a prune would reclaim, and how many are in use by a container.

| Column | Shows |
|---|---|
| repository : tag | Reference; untagged images show `<none>:<none>` and a **dangling** badge |
| size | Size on disk |
| created | Build time |
| used by | **in use** when at least one container (running or stopped) uses the image |
| vulnerabilities | Result of the last scan, **not scanned**, or a scan button |
| host | Host that holds the image |

Filter with the search box (repository, tag or ID) and the pills **all**, **used**, **unused**, **dangling** and **CVE** (images with findings).

Scanning is described in [Vulnerability scanning](./vulnerability-scanning.md).

## Pulling an image

**Pull image** (needs `images.create`) asks for a reference such as `postgres:16-alpine` or `ghcr.io/acme/app:1.4.2`. While you type, the dialog suggests matching Docker Hub repositories; that search runs from your browser directly against `hub.docker.com`.

Pulls always run on the local host, whatever host is selected. If a saved [registry](./registries.md) matches the image's registry, its credentials are used. To get an image onto a remote host, deploy a stack that uses it there.

## Image details

Click an image to open its page. The tabs are:

- **Overview**: ID, tags, digests, architecture, creation time, author, size, and from the image config the command, entrypoint, working directory, user, exposed ports, environment and labels.
- **Vulnerabilities**: the scan report, with **Scan now** or **Re-scan** (needs `images.scan`).
- **Layers**: the layer list.
- **History**: the build history (`docker history`).
- **Containers**: containers using this image.

**Re-pull** pulls the same `repository:tag` again (on the local host), which fetches a newer image if the tag has moved. Running containers keep the old image until they are recreated; see [Image updates](./image-updates.md) for updating them.

## Removing and pruning

| Action | Where | Effect |
|---|---|---|
| Remove | trash icon in a row, **Remove image** on the detail page, or several rows at once | Force-removes the image. Docker refuses only while a running container uses it; an image used only by stopped containers is removed. |
| **Prune dangling** | Images tab | Removes dangling images only (`dangling=true`). Tagged images that no container uses are kept. |
| **System cleanup…** | page header | Prunes dangling images, unused volumes and unused networks in one go. See [Volumes](./volumes.md#removing-and-pruning) before you use it. |

Removing and pruning from the UI always act on the local host, even when a remote host is selected. The API accepts `?host=<id>` on `DELETE /api/v1/images/{id}` and `POST /api/v1/images/prune` if you need to do this on an agent.

There is no scheduled prune. Call `POST /api/v1/images/prune` from cron with an [API token](./api-tokens.md) if you want one.

## Permissions

| Action | Permission |
|---|---|
| List and inspect | `images.view` |
| Pull | `images.create` |
| Remove and prune | `images.delete` |
| Scan | `images.scan` |
