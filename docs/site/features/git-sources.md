---
title: Git sources
description: Keep a stack's compose.yaml and .env in sync with a Git repository, by polling, on demand or through a push webhook.
---

A git source binds one stack to a branch of a Git repository. Dockmesh clones the repository, copies the compose file into the stack directory and, if you turn it on, deploys the stack whenever a new commit arrives.

## Connecting a repository

There are two ways in:

- A new stack: **Stacks → New stack**, pick **From git repository**, choose the **Host** the stack is for, then **Import & sync**. The name must not belong to an existing stack.
- An existing stack: **Connect a git repository** in the stack's action menu or on its **Overview** tab, or **Connect a repository** on its **Settings** tab, then **Connect & sync**. The first sync overwrites the stack's `compose.yaml` with the one from the repository.

Either way the configuration is saved first and the first sync runs right away. If that sync fails (wrong credentials, wrong path), the configuration stays saved and the error is shown, so you can fix it and sync again.

| Field | Default | Notes |
|---|---|---|
| **Repository URL** | | HTTPS or SSH URL. |
| **Branch** | `main` | |
| **Path in repo** | `.` | A directory that contains `compose.yaml`, `compose.yml`, `docker-compose.yaml` or `docker-compose.yml`, or the path of the compose file itself. Must be relative and must not contain `..`. |
| **Authentication** | None | **None (public repo)**, HTTPS with username and token, or SSH private key. |
| **Poll interval** | 300 s | `0` turns polling off; any other value must be at least 60 s. |
| **Webhook secret** | empty | Needed for the push webhook; at least 16 characters. **Generate** creates one. |
| **Auto-deploy on new commits** | off | Deploy after every sync that brought a new commit. |

### Authentication

**HTTPS:** enter a username and a personal access token. GitHub does not accept account passwords; create a fine-grained token with **Contents: Read-only** on the repository, or a classic token with the `repo` scope.

**SSH:** enter the SSH user (default `git`) and paste an OpenSSH private key without a passphrase. Use a deploy key with read access to this one repository. The Git host's key is checked against `~/.ssh/known_hosts` of the user the server runs as, or `/etc/ssh/ssh_known_hosts` (or the files listed in `SSH_KNOWN_HOSTS`). If the sync fails with a host key error, add the key there, for example with `ssh-keyscan github.com >> ~/.ssh/known_hosts` as that user.

The token, the SSH key and the webhook secret are encrypted with the server's secrets key before they are stored, as long as secrets encryption is on (`DOCKMESH_SECRETS_ENCRYPT_ENV`, default `true`). Webhook secrets stored in plaintext by older versions are encrypted at the next start. None of them is returned by the API. When you edit the source, leave a field blank to keep the stored value.

## What a sync does

1. Clone the branch (shallow, depth 1) into `git-cache/<stack>` in the data directory, or fetch and reset the existing clone.
2. On the first sync, or when the commit changed, copy the compose file into the stack directory. If **Path in repo** is a directory, the other files and subdirectories in it are copied too, including dotfiles. Skipped at the top level of that directory: the compose file names, `.env` and `.env.age`, names starting with `.git`, names starting with `.dockmesh` and environment override files `compose.<env>.yaml`. A file over 64 MiB aborts the sync.
3. On the same occasions, merge the repository's `.env` (the one next to the compose file) into the stack's `.env`.
4. If the commit changed and auto-deploy is on, deploy the stack (see [Auto-deploy](#auto-deploy)).

Because `.dockmesh.meta.json` and override files are never copied, a push to the repository can't change the stack's dependencies, scaling rules, migration hooks or default environment override.

Before it copies anything into an existing stack, the sync checks whether `compose.yaml` or `.env` were changed outside Dockmesh. If they were, the sync stops with an error, users who may deploy the stack get a **Git deploy of &lt;stack&gt; is on hold** notice, and **Sync now** answers with a list of the changed files. Review the files and deploy the stack by hand to confirm them; syncs continue after that.

The stack's **Settings** tab shows the repository, branch and path, whether it deploys automatically, whether the webhook is on, and the short commit SHA and time of the last sync. **Sync now** pulls at once (also **Sync from git now** in the action menu), **Edit** changes the settings and **Disconnect** removes the source. Disconnecting deletes the configuration and the local clone; the stack and its files stay.

### Paths stay inside the repository

The clone is read through a directory handle that cannot leave it. A **Path in repo** that leads outside the repository is rejected, and a symlink in the repository that points outside the clone is skipped with a warning in the server log. Symlinks to files inside the clone are copied as the files they point to. The stack directory is written the same way, so a symlink that a container left in a bind-mounted part of the stack directory can't redirect the copy elsewhere.

The repository still decides what runs: its compose file, and any build context or config file it ships, end up on the host. Only connect repositories whose push access you control.

### How .env is merged

Values you set in Dockmesh are never overwritten by a sync:

- When the stack is created from the repository, its `.env` is seeded from the repository's `.env`. When you connect an existing stack, the repository's `.env` is merged into the existing one.
- Keys that appear in the repository's `.env` later are appended with the repository's value.
- `${VAR}` references in `compose.yaml` that neither file defines are appended with an empty value, so the deploy does not fail on a missing variable.
- Keys that exist only in the stack's `.env` are left alone.

When a sync added keys, the stack page shows **Env drift from the last sync** with the new keys. Fill in their values on the **Environment** tab.

## Polling

Every 30 seconds Dockmesh checks which sources are due and syncs them. A source is due when its poll interval has passed since the last sync attempt. Sources with poll interval `0` are never polled; they sync only through the webhook or **Sync now**.

> **Note:** Up to v0.4, a poll interval of `0` was saved as 300 seconds. If you meant to turn polling off on an existing source, edit it and set `0` again.

## Auto-deploy

With **Auto-deploy on new commits** on, every sync that pulls a new commit deploys the stack, like a manual **Deploy** with these differences:

- It runs on the host the stack is deployed on. A stack that was never deployed is deployed on the host it was created for, or on the server host if it has none.
- Every image is pulled again, also on agent hosts, so a new image pushed under the same tag is picked up.
- Global environment variables and the stack's default environment override are applied. If the override file was added or changed since the last manual deploy, the auto-deploy fails with `environment override changed since the last manual deploy`; deploy once by hand after you add or change an override.
- The deployment record and the stack's **History** are updated (marked as a git deploy, with the commit SHA), and the result is sent to the notification bell.

A failed auto-deploy does not fail the sync: the new files are in place, and the error is in the notification and in **History**.

## Push webhook

The endpoint is:

```text
POST https://<your-dockmesh>/api/v1/stacks/<stack>/git/webhook
```

It needs no Dockmesh login. Add it as a push webhook in your Git host with the same secret you entered in Dockmesh; the URL is also shown in the edit dialog under **Webhook URL**.

| Git host | How the secret is checked |
|---|---|
| GitHub | HMAC-SHA256 of the body in `X-Hub-Signature-256` |
| Gitea | HMAC-SHA256 of the body in `X-Gitea-Signature` |
| GitLab | Plain token in `X-Gitlab-Token` |

The webhook only works when the source has a webhook secret. Without one, and for an unknown stack or a wrong signature, the endpoint answers `401` with `webhook rejected`, so it does not reveal which stacks exist. While any source has no secret, admins see the notice **Git webhooks disabled: no webhook secret**, and the stack page shows **Git webhook disabled**. Polling and **Sync now** keep working without a secret.

A valid request returns `202` (`{"status":"sync queued"}`) and the sync runs in the background, for up to 10 minutes. Deliveries that arrive while a webhook sync of the same stack runs are combined into one more sync afterwards, so replayed deliveries can't pile up syncs or deploys. The request body is read up to 1 MiB. If pushes do not show up, check the last sync error on the stack page and in the server log.

## Deleting a stack

Deleting a stack also deletes its git source and the local clone, so a later commit does not bring the stack back.

## Limits

- One git source per stack, one branch per source.
- The repository's other files reach the stack directory on the server only. Deploys on an agent host send just `compose.yaml` and `.env`, so relative bind mounts like `./config:/etc/app` resolve to the stack directory on the agent, where those files don't exist.

## Permissions

| Action | Permission |
|---|---|
| See the git source of a stack | `stacks.view` (Stacks: View) |
| Create a stack from a repository | `stacks.create` (Stacks: Manage) |
| Connect, edit and disconnect a repository | `stacks.git` (Stacks: Manage) |
| **Sync now** | `stacks.deploy` (Stacks: Deploy) |

## API

| Method and path | Permission |
|---|---|
| `POST /api/v1/stacks/from-git` | `stacks.create` |
| `GET /api/v1/stacks/{name}/git` | `stacks.view` |
| `POST`, `DELETE /api/v1/stacks/{name}/git` | `stacks.git` |
| `POST /api/v1/stacks/{name}/git/sync` | `stacks.deploy` |
| `POST /api/v1/stacks/{name}/git/webhook` | none (webhook secret) |

`POST /api/v1/stacks/from-git` takes `name`, `host_id` and the source settings under `git`. Saving answers `422` with `sync_ok: false` and `sync_error` when the first sync fails. See [REST API](../reference/api.md).
