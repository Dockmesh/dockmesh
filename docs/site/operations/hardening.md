---
title: Security hardening
description: A checklist for running Dockmesh on a network where not everyone is trusted.
---

Dockmesh talks to the Docker socket, and access to the Docker socket amounts to root on the host. Anyone who controls Dockmesh controls every host it manages. Work through this list once after installing, and again when you add users or hosts.

## Network exposure

**Put Dockmesh behind a TLS reverse proxy.** The server only speaks plain HTTP on `DOCKMESH_HTTP_ADDR` (default `:8080`, all interfaces). Terminate TLS in a reverse proxy and bind Dockmesh to loopback, so port 8080 is never reachable from the network:

```bash
# /var/lib/dockmesh/dockmesh.env
DOCKMESH_HTTP_ADDR=127.0.0.1:8080
DOCKMESH_BASE_URL=https://dockmesh.example.com
```

Set `DOCKMESH_BASE_URL` to the public `https://` address. Dockmesh uses it for SSO redirect URLs and for the URLs in agent install commands; agents download their installer and binary from it and enroll against it.

<!-- update after: http: trust forwarding headers only from configured proxies -->
**Make the proxy overwrite client IP headers.** Dockmesh takes the client address from `X-Forwarded-For`, `X-Real-IP` and `True-Client-IP` for every request, no matter who sends them. Any client that reaches Dockmesh directly can therefore fake its IP: that gets around the login rate limit and puts false addresses into the audit log and session list. Only expose Dockmesh through a proxy that replaces these headers with the real peer address instead of appending to them. For nginx:

```nginx
proxy_set_header X-Forwarded-For $remote_addr;
proxy_set_header X-Real-IP       $remote_addr;
proxy_set_header True-Client-IP  "";
```

Dockmesh sends no security headers (CSP, HSTS, X-Frame-Options), so add them in the proxy as well.

**Rate-limit sign-in at the proxy.** Dockmesh limits failed password logins per client address, but not the second-factor step (`/api/v1/auth/mfa`), and wrong codes don't count towards the account lockout. Limit both endpoints in the proxy. For nginx:

```nginx
# in the http block
limit_req_zone $binary_remote_addr zone=dockmesh_auth:10m rate=10r/m;

# in the server block
location ~ ^/api/v1/auth/(login|mfa)$ {
    limit_req zone=dockmesh_auth burst=5 nodelay;
    proxy_pass http://127.0.0.1:8080;
}
```

**Firewall.** On the server:

| Port | Purpose | Allow from |
|---|---|---|
| 443 (your proxy) | Web UI, API, agent enrollment and installer | Users, CI, agent hosts |
| 8080 | Dockmesh HTTP | Loopback only |
| 8443 | Agent mTLS connections (`DOCKMESH_AGENT_LISTEN`) | Agent hosts only |
| 80, 443 | Managed Caddy proxy, only when enabled | As your published apps require |
| 2019 | Caddy admin API | Never; bound to `127.0.0.1` |

The agent listener always runs; an empty `DOCKMESH_AGENT_LISTEN` falls back to `:8443`. If you don't use remote agents, block 8443 in the firewall. Agent hosts need no inbound ports at all. See [Ports](../reference/ports.md).

The managed proxy container `dockmesh-proxy` uses host networking, and its admin API on `127.0.0.1:2019` has no authentication. Any process on the server, and any container started with host networking, can change the proxy configuration. Don't run untrusted workloads with `network_mode: host` on the server.

**Metrics.** `/metrics` requires a bearer token with `metrics.view`. Keep `DOCKMESH_METRICS_AUTH` at its default (`true`) unless the endpoint is only reachable from your monitoring network.

## Setup wizard

<!-- update after: setup: block internal targets in url test, lock commit -->
The setup wizard is open while no admin user exists or while `DOCKMESH_SETUP_FORCE=true` is set, for 30 minutes after each server start. The wizard's endpoints need no login, and one of them makes the server fetch a URL of the caller's choosing. Finish setup right after installing, from a trusted network, and keep port 8080 closed to everyone else until then. Afterwards, check that `DOCKMESH_SETUP_FORCE` is gone from `dockmesh.env`. The wizard removes it, but reports a warning if it can't.

## Users and roles

<!-- update after: rbac: scope read endpoints, limit grants, gate sensitive downloads -->
- **`backups.view`** lets a user download any backup decrypted, including system archives with the database, the JWT secret and the age key, and read backup target credentials. Every built-in role, viewer included, has it. Give users you don't fully trust a custom role without it. Built-in roles cannot be edited. See [Backups](./backups.md#permissions).
- **host-admin** is close to admin: it holds `system.update` (settings, SSO configuration, global environment) and `backups.restore`, which includes exporting the age key and extracting an archive into any host path as root (an absolute path as restore target becomes a bind mount). Assign it like admin.
- `backups.create` and `backups.update` let a user run arbitrary commands in any container through backup hooks.
- User and role management don't check that a grant is within the granter's own permissions, and nothing stops the last admin from being removed. Keep `users.*` and `roles.*` with admins.
- Many read endpoints ignore host and role scopes. Treat a scoped role as a convenience filter, not as a security boundary.
- Review users, roles and [API tokens](../features/api-tokens.md) regularly and remove what is no longer needed.

<!-- update after: auth: hide sign-in settings that aren't enforced -->
Some sign-in settings under **Authentication** are stored but not enforced, including **Require 2FA for admin role**, self-registration, password reuse and password rotation. Don't rely on them; ask admins to set up two-factor authentication themselves. See [Authentication](../features/authentication.md).

## Git sources

<!-- update after: gitsource: require webhook secret, sync in background -->
Always set a webhook secret on git-backed stacks. Without one, the webhook endpoint accepts requests from anyone and triggers a sync and deploy. See [Git sources](../features/git-sources.md).

## Agents

<!-- update after: agents: pin the CA at enrollment and verify self-upgrades -->
- Enroll agents only against an `https://` `BASE_URL` and over a network you trust. The agent accepts whatever CA the server presents during enrollment, and agent self-upgrades download the binary without a checksum check.
- Enrollment tokens don't expire. Treat an install command like a password and revoke hosts you never finished enrolling.
- Revoking a host does not close a connected agent's session. Stop the agent service on the host as well; see [Uninstall](./uninstall.md#remove-an-agent).

## Secrets and files

- Leave `DOCKMESH_SECRETS_ENCRYPT_ENV` at `true`. With it off, stack `.env` files, registry passwords, SSO secrets and git credentials are stored in plaintext, and backups are not encrypted.
- Some secrets are plaintext even with encryption on: the agent's mirrored `.env` files on remote hosts, secrets generated by templates (written into `compose.yaml`), and, in the database, global environment variables, backup target credentials (S3 keys, SFTP, SMB and WebDAV passwords and keys), notification channel tokens and passwords, the SAML service-provider key, TOTP secrets, and Git and audit webhook secrets. Protect the database file and every copy of it, including system backups and `dockmesh db backup` output.
- Keep the data root private. `install.sh` creates it as `dockmesh:docker` with mode `750`, and the server creates the age key with mode `0400`. Installs moved off root by an upgrade are `dockmesh:dockmesh` with mode `700`. On a fresh install, members of the `docker` group can read it, so keep that group to people who would have root anyway, or tighten it:

  ```bash
  sudo chmod 700 /var/lib/dockmesh
  ```

- Store the exported age key and backups off the server, see [Disaster recovery](./disaster-recovery.md).
- Run the service as the `dockmesh` user (the default). `systemctl show -p User --value dockmesh` should print `dockmesh`.

## Workloads

Dockmesh deploys what the compose file says, including `privileged: true`, host bind mounts and the Docker socket. Whoever can edit or deploy a stack (`stacks.create`, `stacks.update`, `stacks.deploy`) can take over the host. Keep these permissions to people you would give shell access.
