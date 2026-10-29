---
title: REST API
description: How to authenticate against the Dockmesh REST API, pick a host, open WebSocket streams and read errors.
---

The web UI is a client of the same REST API you can call yourself. Everything is JSON over HTTP under `/api/v1` on the server's HTTP port (8080 by default). This page covers the parts that apply to every endpoint. For the endpoints themselves, use the OpenAPI spec the server ships.

## OpenAPI spec and Swagger UI

| Path | Content |
|---|---|
| `GET /api/v1/openapi.json` | OpenAPI 3.1 spec as JSON |
| `GET /api/v1/openapi.yaml` | The same spec as YAML |
| `GET /api/v1/docs` | Swagger UI for the spec |

All three are public and cached for five minutes. Swagger UI loads its JavaScript and CSS from `cdn.jsdelivr.net`, so the browser needs internet access; use the **Authorize** button to paste a token before you try requests. The spec is maintained by hand next to the router, and a CI test fails when a route is missing from it. Request and response schemas are not checked by that test, so treat field lists as documentation, not a guarantee.

```bash
curl -s https://dockmesh.example.com/api/v1/openapi.json -o dockmesh-openapi.json
```

## Authentication

Every endpoint outside the public set needs `Authorization: Bearer <token>`. Two kinds of token work:

| Token | Get it from | Lifetime | Use for |
|---|---|---|---|
| Access token (JWT) | `POST /api/v1/auth/login` | 15 minutes, renewed with the refresh token | Interactive tools |
| API token, prefix `dmt_` | User menu, **API tokens**, or `POST /api/v1/settings/api-tokens` | Never expires, or the number of days chosen at creation | CI, scripts, Prometheus |

Each request is checked against the current permissions of the user (and, for an API token, the token's role and the owner's current role, whichever allows less). See [API tokens](../features/api-tokens.md) and [Permissions](../concepts/permissions.md).

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

### Refresh and logout

```bash
curl -s -X POST https://dockmesh.example.com/api/v1/auth/refresh \
  -H 'Content-Type: application/json' -d '{"refresh_token":"..."}'
curl -s -X POST https://dockmesh.example.com/api/v1/auth/logout \
  -H 'Content-Type: application/json' -d '{"refresh_token":"..."}'
```

Refresh returns a new token pair; always store both. Presenting a refresh token that was already used counts as theft and revokes the whole session (`401`, `token reuse detected`), so never refresh the same token from two processes. Sessions end after 24 hours or 60 minutes of inactivity by default, whatever you refresh. Logout returns `204`.

### API tokens

```bash
curl -s https://dockmesh.example.com/api/v1/stacks \
  -H "Authorization: Bearer dmt_..."
```

Creating a token needs `tokens.create`; the role you give it cannot hold permissions you lack. The plaintext token is returned once in the create response.

## Choosing a host

Endpoints that act on Docker resources (containers, images, volumes, networks, stacks, system metrics) take an optional `?host=` query parameter:

| Value | Target |
|---|---|
| empty or `local` | The Docker engine on the server |
| an agent's host ID | That remote host; `503` if it is offline or unknown |
| `all` | Fan-out across every online host, on list endpoints for containers, images, volumes, networks and system metrics |

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

## WebSocket streams

Browsers cannot set headers on a WebSocket upgrade, so the stream endpoints authenticate with a short-lived ticket in the query string instead of a bearer token:

1. `POST /api/v1/ws/ticket?for=<permission>` with your bearer token. The answer is `{"ticket":"..."}`. The ticket is valid for 30 seconds and for one connection.
2. Connect to the stream with `?ticket=<ticket>`. Request a new ticket for every connection, including reconnects.

| Stream | Ticket `for=` | Extra parameters |
|---|---|---|
| `/api/v1/ws/logs/{container-id}` | `containers.logs` | `tail` (default 100), `host` |
| `/api/v1/ws/exec/{container-id}` | `containers.exec` | `cmd`, `host` |
| `/api/v1/ws/stats/{container-id}` | `containers.view` | `host` |
| `/api/v1/ws/events` | `system.view` | none (local host only) |

```bash
TICKET=$(curl -s -X POST "https://dockmesh.example.com/api/v1/ws/ticket?for=containers.logs" \
  -H "Authorization: Bearer $TOKEN" | jq -r .ticket)
websocat "wss://dockmesh.example.com/api/v1/ws/logs/4b1e2f?ticket=$TICKET&tail=50"
```

The connection is checked again every 30 seconds and closed if your session or permission is gone. Log streams always follow. Exec sessions carry terminal input and output as binary frames; resize with a text frame `{"type":"resize","cols":120,"rows":40}`.

<!-- update after: rbac: scope read endpoints, limit grants, gate sensitive downloads -->
`/ws/events` sends Docker events of the local host (`"source":"docker"`) and changes to stack files (`"source":"stacks"`). It has no `host` parameter and is not filtered by scope: everyone with `system.view` sees all of these events.

## Errors

Handlers answer errors with a JSON body and a matching status:

```json
{ "error": "stack not found" }
```

Some responses differ:

| Case | Status | Body |
|---|---|---|
| Missing or invalid bearer token | `401` | plain text `unauthorized` |
| Permission missing | `403` | plain text `forbidden` |
| Account locked after failed logins | `423` | JSON, with `Retry-After` |
| Too many failed logins from one address | `429` | JSON, with `Retry-After` |
| Refresh token from before a security update | `401` | `{"error":"…","code":"session_outdated"}` |
| Server still in setup mode | `503` | `{"error":"…","setup_url":"/setup"}` |
| API token checks overloaded | `503` | plain text, `Retry-After: 1` |
| Unknown path under `/api/` | `404` | |

Check the status code first and treat the body as a message for humans. Clients should ignore unknown JSON fields.

## Rate limits and lockout

<!-- update after: http: trust forwarding headers only from configured proxies -->
There is no general rate limit. Two protections apply to sign-in:

- **Per account:** 5 failed passwords lock the account for 15 minutes (`423`). Both numbers are set in the password policy on the **Authentication** page (0 disables the lockout). An admin can lift a lock with `dockmesh admin unlock`.
- **Per client address:** 10 failed logins within a minute block `POST /auth/login` from that address for 5 minutes (`429`).

The client address is taken from `X-Forwarded-For` when that header is present, from any client. If the server is reachable directly, a client can send its own header and avoid the per-address limit, so expose Dockmesh only through a proxy that overwrites `X-Forwarded-For`, and add rate limiting there if the server faces the internet. `POST /auth/mfa`, SSO logins, invite acceptance and enrollment have no limit. Disabling your own MFA (`DELETE /api/v1/mfa`) allows 5 attempts per 15 minutes.

## Public endpoints

These need no token: `GET /api/v1/health`, `GET /healthz/live`, `GET /healthz/ready` (503 while shutting down), the OpenAPI paths above, `/api/v1/auth/*` sign-in routes (login, MFA, refresh, logout, SSO), `/api/v1/setup/*` during setup, `POST /api/v1/agents/enroll`, `/api/v1/invite/{token}`, `POST /api/v1/stacks/{name}/git/webhook`, and `/install/agent.sh` and `/install/{name}`. `/metrics` is public only with `DOCKMESH_METRICS_AUTH=false`; otherwise it needs a token with `metrics.view`. See [Monitoring](../features/monitoring.md).

## dmctl

[dmctl](./dmctl.md) wraps the common calls (login with refresh, stacks, containers, logs, exec, backups, alerts) and is the quickest way to script against the API without handling tokens yourself.
