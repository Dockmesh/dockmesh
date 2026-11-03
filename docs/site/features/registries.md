---
title: Registries
description: Store credentials for private image registries, limit them to hosts by tag, and how deploys on the server and on agents use them.
---

Without credentials, Docker pulls anonymously, and private images fail with "pull access denied" or "unauthorized". **Settings → Registries** stores a username and password or token per registry; Dockmesh hands them to Docker when it pulls an image from that registry, on the server host and on agent hosts.

## Adding a registry

Pick a provider under **Add a known provider** (Docker Hub, GitHub Container, Harbor, Generic OCI) or click **Add registry**. The form has:

| Field | Notes |
|---|---|
| **Provider** | Fills in the URL and shows a hint for the token type. |
| **Display label** | Free text shown in the list. |
| **Registry URL** | Host only, for example `ghcr.io` or `registry.example.com:5000`. Scheme and trailing slash are removed, and the host is lower-cased. Each URL can be saved once. |
| **Username** | |
| **Password / token** | Stored encrypted and never returned by the API. When editing, leave it blank to keep the stored value. |
| **Scope (host tags)** | Empty: the credential is used on every host. With tags: only on hosts that carry at least one of them. In the form, a tag is 1 to 32 lowercase letters, digits and hyphens and starts with a letter or digit. |

Use tokens with read-only scope: on GitHub a token with `read:packages`, on Docker Hub a personal access token, on Harbor a robot account. AWS ECR tokens expire after 12 hours (`aws ecr get-login-password`) and must be replaced by hand; Dockmesh has no ECR integration.

**Test login** in the list, or **Test connection** when you edit a saved entry, logs in to the registry through the server host's Docker engine and records the result. The page counts failing tests and marks credentials as stale when the entry was last saved or tested more than 90 days ago, so a test also clears the stale mark.

## How credentials are matched

When Dockmesh pulls an image, it takes the registry host from the image reference the same way Docker does: the first path element counts as a registry when it contains a `.` or a `:` or is `localhost`; otherwise the registry is `docker.io`.

| Image | Registry looked up |
|---|---|
| `nginx:alpine`, `acme/app:1.0` | `docker.io` |
| `ghcr.io/acme/app:1.0` | `ghcr.io` |
| `registry.example.com:5000/app` | `registry.example.com:5000` |

The registry URL you saved must equal that host exactly. For Docker Hub, save the URL as `docker.io`, not `index.docker.io` or `registry-1.docker.io`. Entries without a stored password are not used.

Then the scope is checked against the tags of the host the pull runs on: the server host (host ID `local`) and agent hosts use the tags they carry under **Hosts**. A credential whose tags the host doesn't carry is skipped and the pull is anonymous.

## Where credentials are used

On the server host:

- stack deploys, rollbacks, dependency deploys, template deploys and git auto-deploys;
- **Pull image** on the **Resources** page;
- image update checks and image updates (see [Image updates](./image-updates.md)), so private images show available updates too.

On agent hosts, Dockmesh works out the images of the stack and sends the matching credentials with the request, for:

- stack deploys, rollbacks, dependency deploys, template deploys and git auto-deploys.

Scale requests carry them as well, but scaling never pulls an image (see [Scaling](./scaling.md)).

The agent uses them for that request only and does not store them. A `docker login` on the agent host makes no difference, because the agent talks to the Docker API directly and does not read the Docker CLI's stored logins.

> **Note:** Agents older than the server ignore the credentials and keep pulling anonymously. Update the agent from **Hosts**. Since a registry without scope tags applies to every host, add scope tags to registries whose credentials must stay off some hosts.

A deploy only pulls images that are missing on the host, unless the service sets `pull_policy: always` or the deploy forces a fresh pull (git auto-deploys do). When such a pull fails and the image is already on the host, Dockmesh logs a warning and deploys the cached image.

## Storage

Passwords are encrypted with the server's secrets key (`secrets.age-key` in the data directory). If secrets encryption is turned off (`DOCKMESH_SECRETS_ENCRYPT_ENV=false`), they are stored without encryption. `dockmesh secrets rotate` and **Rotate key…** don't re-encrypt registry passwords, but the previous keys stay in `secrets.age-key.history`, so stored passwords remain readable after a rotation.

Deleting a registry makes later pulls from it anonymous again. Images already on a host stay.

## Permissions

| Action | Permission |
|---|---|
| List | `registries.view` (Images, volumes, networks & registries: View) |
| Add | `registries.create` (Manage) |
| Edit and test | `registries.update` (Manage) |
| Delete | `registries.delete` (Manage) |

Adding, editing, testing and deleting are instance-wide: only an assignment on everything grants `registries.create`, `registries.update` and `registries.delete`. `registries.view` also comes from an assignment limited to hosts or tags, and lets the API list the registries (URLs, usernames and scope tags, no passwords). The page shows the list and the forms only to users with `registries.update`.

## API

| Method and path | Permission |
|---|---|
| `GET /api/v1/settings/registries` | `registries.view` |
| `POST /api/v1/settings/registries` | `registries.create` |
| `PUT /api/v1/settings/registries/{id}` | `registries.update` |
| `POST /api/v1/settings/registries/{id}/test` | `registries.update` |
| `DELETE /api/v1/settings/registries/{id}` | `registries.delete` |

See [REST API](../reference/api.md).
