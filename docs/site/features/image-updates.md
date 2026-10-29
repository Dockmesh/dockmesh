---
title: Image updates
description: How Dockmesh detects newer images for running containers, what the one-click update does, and how to roll it back.
---

Dockmesh checks whether the images of your running containers have a newer version in the registry. You can then update one container with a click and roll it back if needed.

<!-- update after: updater: recreate stack containers via compose, keep volumes -->
> **Warning:** The one-click update recreates a single container from its inspected configuration and bypasses compose. Anonymous volumes (a `VOLUME` declared in the image without a named volume or bind mount in `compose.yaml`) are not carried over, so their data is lost. Named volumes and bind mounts are kept. For stacks, prefer pulling and redeploying the stack.

## Detection

<!-- update after: updater: recreate stack containers via compose, keep volumes -->
- Every 30 minutes, Dockmesh lists the running containers on the local host and compares each image's local digest with the registry's current digest for the same tag.
- The registry is queried without credentials. Images from private registries show an error instead of a result.
- Images you built locally show an error, or show up as updatable if an image with the same name and tag exists in the registry.
- Results are kept in memory. After a server restart, the first check runs again and can post the same notifications again.
- Containers on agent hosts aren't checked.

When an image changes from up to date to update available, Dockmesh posts an `image.update` bell notification linking to the images list. The current results are also available from `GET /api/v1/images/updates`.

## Update a container

1. Open the container and switch to the **Updates** tab. The tab is hidden for containers on agent hosts.
2. Dockmesh shows a preview. For Docker Hub images this includes the remote tag's last update time, size and, where available, the latest release. Other registries show less.
3. Click **Pull & restart** and confirm.

Dockmesh then:

1. pulls the image tag again, using matching credentials from [Registries](./registries.md) if there are any,
2. tags the old image as `<repo>:<tag>-rollback-<timestamp>`,
3. stops and removes the container and creates a new one with the same name, configuration, host config and networks, using the new image,
4. records the change in the container's update history.

If the pulled image is identical to the running one, nothing is recreated. If creating the new container fails, the old one is already gone and its rollback tag is removed, so redeploy the stack to restore it. There's no health check or scan gate before the swap.

Because the container is replaced outside compose, the stack's `compose.yaml` doesn't change. The next stack deploy recreates the container from `compose.yaml` again, with whatever image that tag resolves to.

Updating and rolling back need `containers.update`.

## Roll back

The **Rollback timeline** on the **Updates** tab lists earlier updates. Use **Roll back to this image** on an entry to recreate the container from that entry's rollback tag, the same way as an update. Rollback tags are never deleted automatically. Remove old ones with an image prune or by hand when you no longer need them.

The API equivalents are `POST /api/v1/containers/{id}/update`, `POST /api/v1/containers/{id}/rollback` with a `history_id`, and `GET /api/v1/containers/{id}/update-history`.

See also [Containers](./containers.md) and [Stacks](./stacks.md).
