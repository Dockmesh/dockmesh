---
title: Registries
description: Store credentials for private image registries so deploys and pulls on the local host can fetch private images.
---

Without credentials, Docker pulls anonymously, and private images fail with "pull access denied" or "unauthorized". **Registries** in the sidebar stores a username and password or token per registry; Dockmesh hands them to Docker when it pulls an image from that registry.

## Adding a registry

Pick a provider under **Add a known provider** (Docker Hub, GitHub Container, Harbor, Generic OCI) or add one by hand. The form has:

| Field | Notes |
|---|---|
| **Provider** | Fills in the URL and shows a hint for the token type. |
| **Display label** | Free text shown in the list. |
| **Registry URL** | Host only, for example `ghcr.io` or `registry.example.com:5000`. Scheme and trailing slash are removed. |
| **Username** | |
| **Password / token** | Encrypted before it is stored when secrets encryption is on (see below); never returned by the API. When editing, leave it blank to keep the stored value. |
| **Scope (host tags)** | Leave empty. See [Limits](#limits). |

Use tokens with read-only scope: on GitHub a token with `read:packages`, on Docker Hub a personal access token, on Harbor a robot account. AWS ECR tokens expire after 12 hours (`aws ecr get-login-password`) and must be replaced by hand; Dockmesh has no ECR integration.

**Test** in the list, or **Test connection** when you edit a saved entry, logs in to the registry through the local Docker engine and records the result. The list counts failing tests, and marks credentials as stale when they were last changed more than 90 days ago.

## How credentials are matched

When Dockmesh pulls an image, it takes the registry host from the image reference the same way Docker does: the first path element counts as a registry when it contains a `.` or a `:` or is `localhost`; otherwise the registry is `docker.io`.

| Image | Registry looked up |
|---|---|
| `nginx:alpine`, `acme/app:1.0` | `docker.io` |
| `ghcr.io/acme/app:1.0` | `ghcr.io` |
| `registry.example.com:5000/app` | `registry.example.com:5000` |

The registry URL you saved must equal that host exactly. For Docker Hub, save the URL as `docker.io`, not `index.docker.io` or `registry-1.docker.io`.

## Where credentials are used

Credentials are applied to pulls on the **local host**:

- stack deploys, rollbacks and template deploys that run on the local host;
- git-source auto-deploys;
- **Pull image** and **Re-pull** on the **Resources** page;
- one-click container image updates (see [Image updates](./image-updates.md)).

They are **not** sent to agents. A stack deployed on a remote host pulls anonymously, and a `docker login` on that host does not change this, because the agent talks to the Docker API directly and does not read the Docker CLI's stored logins. A deploy only pulls images that are missing on the host, so the workaround is to pull private images on the remote host yourself before you deploy:

```bash
docker login ghcr.io -u <user>
docker pull ghcr.io/acme/app:1.4.2
```

With `pull_policy: always` the deploy still tries to pull. When that fails and the image is already on the host, Dockmesh logs a warning and deploys the cached image, so you do not get a newer one.

Image update checks for running containers query registries anonymously, so private images do not show available updates. See [Image updates](./image-updates.md).

## Storage

Passwords are encrypted with the server's age key (`secrets.age-key` in the data directory). If secrets encryption is disabled (`DOCKMESH_SECRETS_ENCRYPT_ENV=false`), they are stored without encryption. After `dockmesh secrets rotate`, stored registry passwords are not re-encrypted and can no longer be decrypted; enter them again.

Deleting a registry makes later pulls from it anonymous again. Images already on a host stay.

## Limits

- **Scope (host tags)** is saved but makes the credential unusable: Dockmesh never passes host tags when it looks up credentials, so a registry with any scope tag never matches. Leave the scope empty.
- Only one credential per registry host is used. If you save two entries with the same URL, which one wins is not defined; keep one entry per host.

## Permissions

| Action | Permission |
|---|---|
| List | `registries.view` |
| Add | `registries.create` |
| Edit and test | `registries.update` |
| Delete | `registries.delete` |

The page only shows the list and the forms to users with `registries.update`.
