---
title: Git sources
description: Keep a stack's compose.yaml and .env in sync with a Git repository, by polling, on demand or through a push webhook.
---

A git source binds one stack to a branch of a Git repository. Dockmesh clones the repository, copies the compose file into the stack directory and, if you enable it, deploys the stack whenever a new commit arrives.

## Connecting a repository

There are two ways in:

- A new stack: **Stacks → New stack → From git repository**, then **Import & sync** (needs `stacks.create`).
- An existing stack: **Connect a git repository** on the stack page, then **Connect & sync** (needs `stacks.update`). The first sync overwrites the stack's `compose.yaml` with the one from the repository.

Either way the configuration is saved first and the first sync runs immediately. If that sync fails (wrong credentials, wrong path), the configuration stays saved and the error is shown, so you can fix it and sync again.

| Field | Default | Notes |
|---|---|---|
| **Repository URL** | | HTTPS or SSH URL. |
| **Branch** | `main` | |
| **Path in repo** | `.` | A directory containing `compose.yaml`, `compose.yml`, `docker-compose.yaml` or `docker-compose.yml`, or the path of the compose file itself. |
| **Authentication** | None | **None (public repo)**, HTTPS with username and token, or SSH private key. |
| **Poll interval** | 300 s | Minimum 60 s. |
| **Webhook secret** | empty | Shared secret for the push webhook. Always set one; see below. |
| **Auto-deploy on new commits** | off | Deploy after every sync that brought a new commit, including the first sync. |

### Authentication

**HTTPS:** enter a username and a personal access token. GitHub does not accept account passwords; create a fine-grained token with **Contents: Read-only** on the repository, or a classic token with the `repo` scope.

**SSH:** enter the SSH user (default `git`) and paste an OpenSSH private key without a passphrase. Use a deploy key that has read access to this one repository. The Git host's key is checked against `~/.ssh/known_hosts` of the user the server runs as, or `/etc/ssh/ssh_known_hosts` (or the files listed in `SSH_KNOWN_HOSTS`). If the sync fails with a host key error, add the key there, for example with `ssh-keyscan github.com >> ~/.ssh/known_hosts` as that user.

The token and the SSH key are encrypted with the server's age key before they are stored (when secrets encryption is on, `DOCKMESH_SECRETS_ENCRYPT_ENV`, default `true`) and are never returned by the API. When you edit the source, leave the field blank to keep the stored value.

## What a sync does

1. Clone the branch (shallow, depth 1) into `git-cache/` in the data directory, or fetch and reset an existing clone.
2. On the first sync, or when the commit changed, copy the compose file into the stack directory. If **Path in repo** is a directory, every other file and subdirectory in it is copied too, including dotfiles. Only the top-level compose file, the top-level `.env` and names starting with `.git` are skipped. A file over 64 MiB aborts the sync.
3. On the same occasions, merge the repository's `.env` (the one next to the compose file) into the stack's `.env`.
4. If the commit changed and auto-deploy is on, deploy the stack.

The stack page shows the repository, branch, the short commit SHA of the last sync and its time. **Sync now** pulls immediately, **Edit** changes the settings and the unlink icon disconnects the source. Disconnecting removes the configuration and the local clone; the stack and its files stay.

### How .env is merged

Values you set in Dockmesh are never overwritten by a sync:

- When the stack is created from the repository, its `.env` is seeded from the repository's `.env`. When you connect an existing stack, the repository's `.env` is merged into the existing one.
- Keys that appear in the repository's `.env` later are appended with the repository's value.
- `${VAR}` references in `compose.yaml` that neither file defines are appended with an empty value, so the deploy does not fail on a missing variable.
- Keys that exist only in the stack's `.env` are left alone.

When a sync added keys, the stack page shows **Env drift from last sync** with the new keys. Fill in their values on the **Environment** tab.

<!-- update after: fs: contain paths in volume browse, restore and git sources -->
> **Warning:** **Path in repo** and symlinks inside the repository are not confined to the clone directory. Only connect repositories whose content you trust as much as a shell on the server.

## Polling

Every 30 seconds Dockmesh checks which sources are due and syncs them. A source is due when its poll interval has passed since the last sync attempt. Polling cannot be switched off: the dialog suggests `0` for webhook-only use, but the server stores `0` as the default of 300 seconds.

## Auto-deploy

With **Auto-deploy on new commits** on, every sync that pulls a new commit deploys the stack. These deploys behave differently from a manual **Deploy**:

- They always run on the local host, even if the stack is normally deployed on an agent.
- Every image is pulled again, so a new image pushed under the same tag is picked up.
- Global environment variables and environment overrides are not applied.
- The result is written to the stack's **History** (success or failure, with the commit SHA) and to the notification bell, but the stack's deployment record is not updated.

If you run the stack on a remote host, leave auto-deploy off and deploy by hand after a sync.

## Push webhook

The endpoint is:

```
POST https://<your-dockmesh>/api/v1/stacks/<stack>/git/webhook
```

It needs no Dockmesh login. Add it as a push webhook in your Git host, with the same secret you entered in Dockmesh. A valid request returns `202` and starts a sync.

| Git host | How the secret is checked |
|---|---|
| GitHub | HMAC-SHA256 of the body in `X-Hub-Signature-256` |
| Gitea | HMAC-SHA256 of the body in `X-Gitea-Signature` |
| GitLab | Plain token in `X-Gitlab-Token` |

<!-- update after: gitsource: require webhook secret, sync in background -->
> **Warning:** If no webhook secret is set, the endpoint accepts any request: anyone who can reach the server and knows the stack name can trigger a sync, and with auto-deploy on, a deploy. Always set a webhook secret. The secret is stored in the database without encryption.

<!-- update after: gitsource: require webhook secret, sync in background -->
The webhook answers before the sync finishes, and the sync it starts can be cut short when the request ends. If pushes do not show up, check **Sync now** and the error on the stack page; polling picks up the commit at the next interval either way.

## Limits

- One git source per stack, one branch per source.
- A git source is not deleted with its stack. If you delete the stack while the source is still connected, the next sync recreates it. Disconnect first.
<!-- update after: backups: scope hooks, limit targets, check sftp keys; fix bind mounts -->
- The repository's files other than the compose file and `.env` are copied into the stack directory, but relative bind mounts do not resolve there; see [Relative paths and the staging directory](../concepts/stacks.md#relative-paths-and-the-staging-directory).

## API

| Method and path | Permission |
|---|---|
| `POST /api/v1/stacks/from-git` | `stacks.create` |
| `GET`, `POST`, `DELETE /api/v1/stacks/{name}/git` | `stacks.update` |
| `POST /api/v1/stacks/{name}/git/sync` | `stacks.update` |
| `POST /api/v1/stacks/{name}/git/webhook` | none (webhook secret) |

See [API](../reference/api.md) for the request bodies.
