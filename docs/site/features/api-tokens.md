---
title: API tokens
description: Create API tokens for scripts, CI and dmctl, and how their permissions are limited by the owner's role.
---

API tokens let scripts, CI jobs, Prometheus and `dmctl` call the Dockmesh API without a password. A token looks like `dmt_` followed by random characters. Dockmesh stores only a hash, so the full token is shown once, when you create it.

## Create a token

1. Open **API tokens** from the user menu (top right) and click **New token**.
2. Enter a **Label**, so you know later what the token is for. It shows in the token list.
3. Pick a role under **Scope**. Choose the narrowest role that does the job.
4. Pick when it **Expires**: 30, 90, 180 or 365 days, or never.
5. Click **Generate token** and copy the token. It isn't shown again.

You need `tokens.create`. You can only pick a role whose permissions you have yourself. A token created with another token can't outlive that token. Listing and revoking your own tokens needs `tokens.view` and `tokens.delete`. `tokens.manage_others` covers other users' tokens.

The list shows each token's prefix, role, expiry, and when and from which IP it was last used. Revoke a token with its delete action. It stops working immediately.

## Effective permissions

A token belongs to the user who created it. On every request, Dockmesh grants only what both of these allow:

- the token's role,
- the owner's current role.

The owner's host-tag scope applies too. If the owner later loses a permission, their tokens lose it at the same time. If the owner account is deleted, the token is rejected.

## Legacy tokens

Tokens created before tokens had an owner are marked **legacy** in the list. They keep working with the role they were created with, and no user's permissions limit them. While any active legacy token exists, admins see the `api-tokens.legacy` notice. Replace each one: sign in as the user who should own it, create a new token, switch your script over, then revoke the legacy token. The notice disappears when the last one is gone.

## Use a token with curl

Send it as a bearer token:

```bash
curl -H "Authorization: Bearer dmt_…" https://dockmesh.example.com/api/v1/stacks
```

The API is described in [API reference](../reference/api.md), and the live spec is at `/api/v1/openapi.json`.

## Use a token with dmctl

```bash
echo "dmt_…" | dmctl login https://dockmesh.example.com
dmctl whoami
dmctl stacks list
```

`dmctl login` reads the token from stdin, so it doesn't end up in your shell history, and saves it in `~/.config/dmctl/config.json`. For CI, skip the login and pass the token through the environment:

```bash
export DMCTL_SERVER=https://dockmesh.example.com
export DMCTL_TOKEN=dmt_…
dmctl stacks deploy my-stack
```

`--server` and `--token` on any command override both. See [dmctl](../reference/dmctl.md).

## Good practice

- Create one token per use, so you can revoke one without breaking the others.
- Give tokens an expiry, and give CI tokens a role that only has what the pipeline needs, for example a custom role with `stacks.view` and `stacks.deploy`.
- Store tokens in your CI's secret store, not in repositories.
- For Prometheus, create a token whose role has only `metrics.view`. See [Monitoring](./monitoring.md).
