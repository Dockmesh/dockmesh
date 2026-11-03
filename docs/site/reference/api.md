---
title: REST API
description: How to authenticate against the Dockmesh REST API, pick a host, follow operations, open WebSocket streams, read errors and stay within the rate limits.
---

The web UI is a client of the same REST API you can call yourself. Everything is JSON over HTTP under `/api/v1` on the server's HTTP port (8080 by default). This page covers the parts that apply to every endpoint. For the endpoints themselves, use the OpenAPI spec the server ships.

## OpenAPI spec and Swagger UI

| Path | Content |
|---|---|
| `GET /api/v1/openapi.json` | OpenAPI 3.1 spec as JSON |
| `GET /api/v1/openapi.yaml` | The same spec as YAML |
| `GET /api/v1/docs` | Swagger UI for the spec |

All three are public; the JSON and YAML specs are cached for five minutes. Swagger UI loads its JavaScript and CSS from `cdn.jsdelivr.net`, so the browser needs internet access. Use the **Authorize** button to paste a token before you try requests, preferably a short-lived API token with a narrow restriction. The spec is maintained by hand, grouped by resource, and a CI test fails when a route is missing from it. Request and response schemas are not checked by that test, so treat field lists as documentation, not a guarantee.

```bash
curl -s https://dockmesh.example.com/api/v1/openapi.json -o dockmesh-openapi.json
```

## Authentication

Every endpoint outside the [public set](#public-endpoints) needs `Authorization: Bearer <token>`. Two kinds of token work:

| Token | Get it from | Lifetime | Use for |
|---|---|---|---|
| Access token (JWT) | `POST /api/v1/auth/login` | 15 minutes, renewed with the refresh token | Interactive tools |
| API token, prefix `dmt_` | **API tokens** in the user menu, or `POST /api/v1/settings/api-tokens` | Never expires, or the number of days chosen at creation | CI, scripts, Prometheus |

Each request is checked against the user's **current** assignments. An API token acts with its owner's current access, narrowed to the token's restriction when it has one. See [API tokens](../features/api-tokens.md) and [Permissions](../concepts/permissions.md).

### Password login

```bash
curl -s -X POST https://dockmesh.example.com/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"..."}'
```

The response holds `access_token`, `refresh_token` and `user`. If the account has two-factor authentication, it holds `mfa_required: true` and an `mfa_token` instead; finish with:

```bash
curl -s -X POST https://dockmesh.example.com/api/v1/auth/mfa \
  -H 'Content-Type: application/json' \
  -d '{"mfa_token":"...","code":"123456"}'
```

The MFA token is valid for five minutes. `code` accepts a TOTP code or a recovery code.

### Sessions, refresh and logout

```bash
curl -s -X POST https://dockmesh.example.com/api/v1/auth/refresh \
  -H 'Content-Type: application/json' -d '{"refresh_token":"..."}'
curl -s -X POST https://dockmesh.example.com/api/v1/auth/logout \
  -H 'Content-Type: application/json' -d '{"refresh_token":"..."}'
```

A login starts a session. Refresh returns a new token pair; always store both. Presenting a refresh token that was already used counts as theft and ends the whole session (`401`, code `token_reuse_detected`), so never refresh the same token from two processes. With the defaults on **Settings → Authentication**, a session ends after 60 minutes without a refresh, and each refresh extends it to 24 hours from then, so a client that keeps refreshing stays signed in. A user has at most 20 active sessions by default. Logout returns `204`.

`GET /api/v1/sessions` lists your own sessions, `DELETE /api/v1/sessions/{family_id}` ends one and `POST /api/v1/sessions/revoke-all` ends all of them.

### API tokens

```bash
curl -s https://dockmesh.example.com/api/v1/stacks \
  -H "Authorization: Bearer dmt_..."
```

Every signed-in user can create tokens. The request takes `name`, `expires_in_days` (`0` = never) and optionally a `role` with a `scope` to restrict it; a token never gets more than its owner has. The plaintext token is returned once in the create response.

### The caller

`GET /api/v1/me` returns the signed-in user with `permissions`, `assignments` and `language`; for an API token it returns `kind: "api_token"`, `api_token_id` and the token's permissions. `PUT /api/v1/me/language` with `{"language":"de"}` sets the user's language (`en`, `de`, or empty for the browser language); it answers `204`. API error messages stay in English; the `code` field is what the UI translates.

## Choosing a host

Endpoints that act on Docker resources (containers, images, volumes, networks, stacks, system metrics) take an optional `?host=` query parameter:

| Value | Target |
|---|---|
| empty or `local` | The Docker engine on the server |
| an agent's host ID | That remote host; `503` if it is offline or unknown |
| `all` | Fan-out across every online host in your scope, on the list endpoints for containers, images, volumes, networks and system metrics |

Host IDs come from `GET /api/v1/hosts`. With `host=all` the response is wrapped:

```json
{
  "items": [ { "Id": "4b1e…", "host_id": "local", "host_name": "Local" } ],
  "unreachable_hosts": [ { "host_id": "3f2c…", "host_name": "web-02", "reason": "…" } ]
}
```

Each host gets three seconds; slower hosts are listed in `unreachable_hosts` and the rest is returned.

```bash
curl -s "https://dockmesh.example.com/api/v1/containers?host=all&all=true" \
  -H "Authorization: Bearer $TOKEN"
```

## Operations

Image updates, backup runs, restores and the certificate wait of new proxy sites run as **operations**: the request that starts them returns at once, and the work continues on the server with its own steps and log.

| Method and path | Effect |
|---|---|
| `GET /api/v1/operations` | Operations you may see, newest first, without logs. Filters: `kind`, `ref`, `group`, `active=1`, `limit` |
| `GET /api/v1/operations/{id}` | One operation with its steps and log |
| `POST /api/v1/operations/{id}/cancel` | Cancel a queued operation, or a running one in a step that can be stopped |

An operation has `id`, `kind` (`image.update`, `backup.run`, `backup.restore`, `proxy.site`), `status` (`queued`, `running`, `succeeded`, `failed`, `cancelled`), `steps` with their own status, `cancellable`, `cancel_requested`, `error` and timestamps. The log keeps the last 500 lines of up to 1 KiB each, with credentials masked. Operations are kept for 7 days; work still running when the server stops is marked failed on the next start.

You see an operation only if you may view what it works on: image updates need `containers.view` on the container's host or stack, backups and restores `backups.view` on the host (system backups admins only), proxy sites `proxy.view`. An operation you may not see answers `404`. Cancelling needs the permission that started the work (`containers.upgrade`, `backups.run`, `backups.restore` or `proxy.create`); an operation that has ended or whose current step must not be interrupted answers `409`. Running work stops a moment later, so the cancel response may still show `running` with `cancel_requested: true`.

Instead of polling, follow `/ws/events`: it sends every change and log line of the operations you may see.

## WebSocket streams

Browsers cannot set headers on a WebSocket upgrade, so the stream endpoints authenticate with a short-lived ticket in the query string instead of a bearer token:

1. `POST /api/v1/ws/ticket?for=<permission>` with your bearer token. The answer is `{"ticket":"..."}`. The ticket is valid for 30 seconds and for one connection.
2. Connect to the stream with `?ticket=<ticket>`. Request a new ticket for every connection, including reconnects.

| Stream | Ticket `for=` | Extra parameters |
|---|---|---|
| `/api/v1/ws/logs/{container-id}` | `containers.logs` | `tail` (default 100), `follow` (`0` to print and close), `host` |
| `/api/v1/ws/exec/{container-id}` | `containers.exec` | `cmd`, `host` |
| `/api/v1/ws/stats/{container-id}` | `containers.view` | `host` |
| `/api/v1/ws/events` | `system.view` | none |

```bash
TICKET=$(curl -s -X POST "https://dockmesh.example.com/api/v1/ws/ticket?for=containers.logs" \
  -H "Authorization: Bearer $TOKEN" | jq -r .ticket)
websocat "wss://dockmesh.example.com/api/v1/ws/logs/4b1e2f?ticket=$TICKET&tail=50"
```

The connection is checked again every 30 seconds and closed if your session or permission is gone. Exec sessions carry terminal input and output as binary frames; resize with a text frame `{"type":"resize","cols":120,"rows":40}`.

`/ws/events` sends JSON messages from three sources:

| `source` | Content |
|---|---|
| `docker` | Docker events of the server host (`type`, `action`, `id`, `name`, `image`, `time`) |
| `stacks` | Changes to stack files (`type` `created`, `modified` or `removed`, `name`, `file`) |
| `operations` | Operation changes (`type` `update`, with `op`) and log lines (`type` `log`, with `op` and `line`) |

Every signed-in user can open it, and every message is filtered by your scope: Docker events only for containers of stacks you may see on the server host, stack events only for stacks you may see, operations as described above. The filter follows role and scope changes while the connection is open.

## Errors

Handlers answer errors with a JSON body and a matching status:

```json
{ "error": "stack not found", "code": "stack_not_found" }
```

`error` is an English message for humans. `code` is stable: messages without runtime detail have their own code (for example `out_of_scope` when none of your assignments allows the action on that resource), all others get a generic code per status (`http_bad_request`, `http_unauthorized`, `http_forbidden`, `http_not_found`, `http_conflict`, `http_too_large`, `http_unprocessable`, `http_too_many_requests`, `http_bad_gateway`, `http_unavailable`, `http_timeout`, `http_internal`, and `http_error` for any other status). Match on the status and `code`, never on the text, and ignore JSON fields you don't know; some errors add fields, such as `files` when stack files were changed outside Dockmesh.

Some responses differ:

| Case | Status | Body |
|---|---|---|
| Missing or invalid bearer token | `401` | plain text `unauthorized` |
| Permission missing | `403` | plain text `forbidden` |
| Missing or invalid WebSocket ticket | `401` | plain text |
| Account locked by the lockout policy | `423` | JSON, with `Retry-After` |
| Too many attempts | `429` | JSON, with `Retry-After` |
| Refresh token from before a security update | `401` | `{"error":"…","code":"session_outdated"}` |
| Server still in setup mode | `503` | `{"error":"…","setup_url":"/setup"}` |
| API token checks overloaded | `503` | plain text, `Retry-After: 1` |
| Unknown path under `/api/` | `404` | plain text |

## Rate limits and lockout

There is no general rate limit. These limits count failed attempts; a successful attempt doesn't use up the address's allowance:

| What | Limit | Answer |
|---|---|---|
| Password login, per client address | 100 failures in 15 minutes, then blocked for 15 minutes | `429` |
| Password and two-factor sign-in, per account | 10 failures in 15 minutes, then blocked for 15 minutes | `429` |
| Lockout policy, per account | 5 wrong passwords or 2FA codes in a row lock the account for 15 minutes; both numbers are set on **Settings → Authentication** (0 turns the lockout off) | `423` |
| `POST /auth/mfa`, per client address | 20 failures in 15 minutes | `429` |
| LDAP sign-in | 10 failures per address and 10 per account in 15 minutes | `429` |
| Invite acceptance | 10 failures per address and 5 per invite in 15 minutes | `429` |
| `POST /auth/refresh`, per client address | 300 failures in 5 minutes | `429` |
| Disabling your own MFA (`DELETE /api/v1/mfa`) | 5 wrong codes in 15 minutes | `429` |

`dockmesh admin unlock` or the unlock action on the user page lifts a lock. IPv6 clients are counted per /64.

The client address is the TCP peer unless the peer is a trusted proxy: only then are `X-Forwarded-For`, `X-Real-IP` and `X-Forwarded-Proto` believed. Trusted proxies are set under **Settings → General** (**Trusted proxies**) or with `DOCKMESH_TRUSTED_PROXIES`; without either, only loopback is trusted, so a reverse proxy on the same machine works without configuration. When a peer outside the list sends forwarding headers, admins get a notice. Add rate limiting in front of Dockmesh if it faces the internet.

## Public endpoints

These need no token: `GET /api/v1/health`, `GET /healthz/live`, `GET /healthz/ready` (503 while shutting down or when the database does not answer), the OpenAPI paths above, the sign-in routes under `/api/v1/auth/` (login, MFA, refresh, logout, SSO), `/api/v1/setup/*` during setup, `POST /api/v1/agents/enroll`, `/api/v1/invite/{token}`, `POST /api/v1/stacks/{name}/git/webhook` (checked with the webhook secret, see [Git sources](../features/git-sources.md#push-webhook)), and `/install/agent.sh` and `/install/{name}`. The agent listener (port 8443) accepts enrollment at `/enroll` without a client certificate. `/metrics` is public only with `DOCKMESH_METRICS_AUTH=false`; otherwise it needs a token with `metrics.view`. See [Monitoring](../features/monitoring.md).

## dmctl

[dmctl](./dmctl.md) wraps the common calls (login with refresh, stacks, containers, logs, exec, backups, alerts, enrollment) and is the quickest way to script against the API without handling tokens yourself.
