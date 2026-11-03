---
title: API tokens
description: Create API tokens for scripts, CI and dmctl, and how their access follows the owner and an optional restriction.
---

API tokens let scripts, CI jobs, Prometheus and `dmctl` call the Dockmesh API without a password. A token looks like `dmt_` followed by random characters. Dockmesh stores only a hash, so the full token is shown once, when you create it.

## Create a token

1. Open the user menu at the bottom of the sidebar and choose **API tokens**, then click **New token**.
2. Enter a **Label**, so you know later what the token is for. It shows in the token list.
3. Under **Restriction**, keep **No restriction** or pick a role. With a role you also pick the scope it is **Restricted to**. Without a restriction the token acts with your full current access; choose the narrowest restriction that does the job.
4. Pick when it **Expires**: 30 days, 90 days (default), 6 months, 1 year or never.
5. Click **Generate token** and copy the token. It isn't shown again.

The **API tokens** page in the web UI needs Users & roles: Manage on everything. Other users can create, list and revoke their own tokens through the API (`POST`, `GET` and `DELETE /api/v1/settings/api-tokens`), which every signed-in user may call. A token created with another token can't outlive that token. Legacy tokens can't create tokens.

The list shows each token's prefix, restriction, expiry, and when and from which IP it was last used. Revoke a token with **Revoke** in its row. It stops working immediately. Without Users & roles: Manage you can only revoke your own tokens, and a request made with a token can only revoke that token itself. Admins also find a user's tokens on the **API tokens** tab of the user's page under **Users & roles**.

## Effective permissions

A token belongs to the user who created it. On every request it gets the owner's **current** access, narrowed to the token's restriction (role and scope) when it has one. It never gets more than the owner has. If the owner later loses an assignment, their tokens lose it at the same time. If the owner account is deleted, the token is rejected.

A token created with a restricted token stays inside that restriction. It also holds at most what the token it was created with holds at the time of use, and stops working when that token is revoked or expires.

Instance-wide permissions, for example the `/metrics` scrape, need an assignment on everything. A token restricted to a host, a host tag or a stack never has them, whatever the role says. See [Permissions](../concepts/permissions.md#scopes).

## Legacy tokens

Tokens created before tokens had an owner are marked **legacy** in the list. They act as the role they were created with, on its scope, and no user's access limits them. While any active legacy token exists, admins see a notice about it. Replace each one: sign in as the user who should own it, create a new token, switch your script over, then revoke the legacy token. The notice disappears when the last one is gone.

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

`dmctl login` reads the token from stdin, so it doesn't end up in your shell history, and saves it in dmctl's config file with mode `0600` (`~/.config/dmctl/config.json` on Linux; on macOS and Windows it is under the user's config directory). For CI, skip the login and pass the token through the environment:

```bash
export DMCTL_SERVER=https://dockmesh.example.com
export DMCTL_TOKEN=dmt_…
dmctl stacks deploy my-stack
```

`--server` and `--token` on any command override both. See [dmctl](../reference/dmctl.md).

## Audit

Creating a token is recorded as `api_token.create` (category **Access**), revoking as `api_token.revoke` (category **Sign-in & security**). Entries written by a request with a token name the token's owner and carry the token id in their details.

## Good practice

- Create one token per use, so you can revoke one without breaking the others.
- Give tokens an expiry, and restrict CI tokens to what the pipeline needs, for example the Deployer role on the one stack it deploys.
- Store tokens in your CI's secret store, not in repositories.
- For Prometheus, restrict the token to a custom role with only Settings & audit: View, on everything. See [Monitoring](./monitoring.md).
