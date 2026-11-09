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

The pull runs on the selected host; under **All hosts** the dialog asks which one. If a saved [registry](./registries.md) applies to that host and matches the image's registry, its credentials are used. For an agent host they are sent with the pull request and not stored on the agent. An older agent refuses with a message to update it from the Hosts page. A pull that fails, for example on a registry rate limit, shows the registry's error.

## Image details

Click an image to open its page. The tabs are:

- **Overview**: ID, tags, digests, architecture, creation time, author, size, and from the image config the command, entrypoint, working directory, user, exposed ports, environment and labels.
- **Vulnerabilities**: the scan report, with **Scan now** or **Re-scan** (needs `images.scan`).
- **Layers**: the layer list.
- **History**: the layers the image was built from, newest first, with the command, size and date of each. On an agent host this needs the agent version that ships with this server; an older agent shows a message to update it from the Hosts page.
- **Containers**: containers using this image, running or stopped.

**Re-pull** pulls the same `repository:tag` again on the host the image is on, which fetches a newer image if the tag has moved. Running containers keep the old image until they are recreated; see [Image updates](./image-updates.md) for updating them.

## Removing and pruning

| Action | Where | Effect |
|---|---|---|
| Remove | trash icon in a row, **Remove image** on the detail page, or several selected rows with **Remove** | Force-removes the image. Docker refuses only while a running container uses it; an image used only by stopped containers is removed. |
| **Prune dangling** | Images tab | Removes dangling images only. Tagged images that no container uses are kept. |
| **System cleanup…** | page header | Prunes dangling images, unused volumes and unused networks in one go. See [Volumes](./volumes.md#removing-and-pruning) before you use it. |

Removing acts on the image's host. Pruning, **System cleanup…** included, acts on the selected host; under **All hosts** it runs on every reachable host in view, one after the other. The API takes `?host=<id>` on `DELETE /api/v1/images/{id}` and `POST /api/v1/images/prune`.

There is no scheduled prune. Call `POST /api/v1/images/prune` from cron with an [API token](./api-tokens.md) if you want one.

## Permissions

| Action | Permission |
|---|---|
| List and inspect | `images.view` |
| Pull | `images.create` |
| Remove and prune | `images.delete` |
| Scan | `images.scan` |

Each permission is checked on the host the action targets: listing and inspecting on the host you look at, pulling, scanning, removing and pruning on the host named by `?host=` (the server host by default). In the role editor they belong to the **Images, volumes, networks & registries** area: **View** gives `images.view`, **Manage** the other three. See [Permissions](../concepts/permissions.md).
