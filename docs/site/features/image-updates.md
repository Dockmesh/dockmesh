---
title: Image updates
description: How Dockmesh detects newer images for containers on the server host, the Updates page, single and batch updates, and rollback.
---

Dockmesh checks whether the images of running containers on the server host have a newer version in their registry. You can update one container or several at once, follow each update live, and roll back to the previous image.

Update checks and updates only cover the server host, the one Dockmesh runs on. Containers on agent hosts aren't checked, aren't listed on the **Updates** page, and their container page has no **Updates** tab. To move a stack on an agent host to a newer image, pull and redeploy the stack.

## Detection

- Every 30 minutes, Dockmesh lists the running containers on the server host and compares each image's registry digest with the registry's current digest for the same tag.
- Private registries are queried with the matching credentials from [Registries](./registries.md).
- Images built or loaded locally have no registry digest and show "no registry digest (image built or loaded locally)" instead of a result.
- Results are stored in the database, so a restart neither forgets them nor announces the same update again.

When a check finds a newer image for a tag, Dockmesh posts an `image.update` bell notification to users with `images.view` on the server host, once per new upstream digest. The update also shows up as information under **Needs attention** (see [Monitoring](./monitoring.md#needs-attention)). The containers list marks the image with **update**. The current results are available from `GET /api/v1/images/updates`.

## The Updates page

**Updates** in the sidebar has two parts:

- **Apps**: the app images on the server host that have a newer image, with stack, update kind (**major**, **minor**, **patch**, when the tag is a release version) and when it was checked, plus the containers updated recently.
- **System**: the Dockmesh server version, the Caddy version of the [reverse proxy](./reverse-proxy.md#caddy-version) (the version Dockmesh is tested with) and the agent versions, which follow the agent upgrade policy.

Click **Update** on a row, or select several rows and click **Update selected**. Selected updates are queued and run one after another on the server, each as its own operation; you can leave the page while they run. The API for several containers is `POST /api/v1/containers/updates` with `{"container_ids": ["…"]}` (up to 100), which returns `202` with the queued operations. Every container is checked like a single update first, and each one is authorised again when its turn comes.

## Update a single container

1. Open the container and switch to the **Updates** tab.
2. Dockmesh shows a preview. For Docker Hub images this includes the remote tag's push time, size and, where available, the latest GitHub release. Other registries show less.
3. Click **Pull & restart** and confirm.

The update runs as an operation with these steps, shown live with its log in the operations indicator (see [Monitoring](./monitoring.md#running-operations)):

1. **Pull image**: pulls the tag again, with credentials from [Registries](./registries.md) if any match. If the pulled image is identical to the running one, the other steps are skipped.
2. **Keep current image for rollback**: tags the old image as `<repo>:<tag>-rollback-<timestamp>`.
3. **Replace container and wait until it runs**: the old container is renamed aside and stopped, the new one is created under the original name, and the old one is removed only once the new one runs and, if it has a healthcheck, passes it. If the new one fails, it is removed and the old one is put back. Named, anonymous and bind-mounted volumes are carried over.

The update keeps running if you close the page. It can be cancelled while it pulls; after that the swap isn't interrupted.

**Stack containers.** A container of a Dockmesh stack is rebuilt from the stack's `compose.yaml`, together with the other replicas of its service, one at a time. If one replica fails, the ones already moved are put back on the old image. The new image is recorded in the stack's deploy history. Because this applies the stack's current `compose.yaml`, it also needs `stacks.deploy` on the stack. When stack files were changed outside Dockmesh, the update is refused like a deploy (HTTP 409, code `modified_outside`) until the files are confirmed: review them and deploy the stack, or repeat the API request with `?confirm_external=1`.

**Standalone containers** are recreated with their own configuration. Values the new image defines (environment, command, entrypoint, working directory, user, labels, healthcheck) come from the new image unless they were set on the container itself.

There's no vulnerability scan gate before the swap. Containers started with `--rm` are refused, since stopping them would delete them.

## Roll back

The **Rollback timeline** on the **Updates** tab lists earlier updates. Use **Roll back to this image** on an entry to recreate the container from that entry's rollback tag, the same way as an update. A rollback only accepts history entries of the container it is called for.

After each successful update, only the newest three rollback tags per image tag are kept. Set `DOCKMESH_UPDATE_ROLLBACK_KEEP` in the server environment to keep a different number. Only tags the updater created are removed; an image a container still uses stays.

## Permissions

| Action | Permission |
|---|---|
| See available updates | `containers.view` and `images.view` on the server host |
| Update, update several, roll back, see the update history | `containers.upgrade` on the container's host and stack |
| Update or roll back a stack container | also `stacks.deploy` on the stack |

Dockmesh's own containers (the reverse proxy, agents, helpers) can only be updated by an admin. Each update is audited as `container.update`, each rollback as `container.rollback`.

The API equivalents are `POST /api/v1/containers/{id}/update`, `POST /api/v1/containers/{id}/rollback` with a `history_id`, `GET /api/v1/containers/{id}/update-info` (the preview) and `GET /api/v1/containers/{id}/update-history`.

See also [Containers](./containers.md) and [Stacks](./stacks.md).
