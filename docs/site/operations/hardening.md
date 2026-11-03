---
title: Security hardening
description: A checklist for running Dockmesh on a network where not everyone is trusted, and every outbound connection the server and the agent make.
---

Dockmesh talks to the Docker socket, and access to the Docker socket amounts to root on the host. Anyone who controls Dockmesh controls every host it manages. Work through this list once after installing, and again when you add users or hosts.

## Network exposure

**Put Dockmesh behind a TLS reverse proxy.** The server only speaks plain HTTP on `DOCKMESH_HTTP_ADDR` (default `:8080`, all interfaces). Terminate TLS in a reverse proxy and bind Dockmesh to loopback, so port 8080 is never reachable from the network:

```bash
# /var/lib/dockmesh/dockmesh.env
DOCKMESH_HTTP_ADDR=127.0.0.1:8080
DOCKMESH_BASE_URL=https://dockmesh.example.com
```

Set `DOCKMESH_BASE_URL` to the public `https://` address. Dockmesh uses it for SSO redirect URLs, invite links and the URLs in agent install commands; agents download their installer from it. It wins over **Base URL** under **Settings → General**.

**Tell Dockmesh which proxies to trust.** Dockmesh believes `X-Forwarded-For`, `X-Real-IP` and `X-Forwarded-Proto` only from trusted proxies, by default loopback. A proxy on the same machine needs no configuration. For a proxy on another machine, add its address under **Settings → General → Trusted proxies** (or `DOCKMESH_TRUSTED_PROXIES`), and nothing else: whoever connects from a trusted address can set the client address that rate limits, sessions and the audit log use. Until it's configured, admins get a notice when forwarding headers arrive from an untrusted peer. See [Authentication](../features/authentication.md#client-address-and-trusted-proxies).

Let the proxy pass the real peer address and the scheme. For nginx:

```nginx
proxy_set_header X-Forwarded-For   $remote_addr;
proxy_set_header X-Real-IP         $remote_addr;
proxy_set_header X-Forwarded-Proto $scheme;
```

`X-Forwarded-Proto: https` makes Dockmesh set `Secure` on its cookies and send HSTS. Dockmesh sends `Content-Security-Policy`, `X-Frame-Options: DENY`, `X-Content-Type-Options: nosniff` and `Referrer-Policy: same-origin` itself.

**Sign-in limits.** Dockmesh limits failed password and 2FA attempts per username and per client address, and the account lockout counts wrong 2FA codes too (see [Authentication](../features/authentication.md#lockout-and-rate-limits)). The per-address counters live in memory and reset on restart. For an extra limit at the proxy, for nginx:

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
| 443 (your proxy) | Web UI, API, agent installer | Users, CI, agent hosts |
| 8080 | Dockmesh HTTP | Loopback only |
| 8443 | Agent enrollment and mTLS connections (`DOCKMESH_AGENT_LISTEN`) | Agent hosts only |
| 80, 443 | Managed Caddy proxy, only when enabled | As your published apps require |
| 2019 | Caddy admin API | Never; bound to `127.0.0.1` |

The HTTP port also accepts enrollment at `POST /api/v1/agents/enroll` and returns the agent's key and certificate in the response. Reach it only through TLS (your proxy on 443), never over plain HTTP across a network.

The agent listener always runs; an empty `DOCKMESH_AGENT_LISTEN` falls back to `:8443`. If you don't use remote agents, block 8443 in the firewall. Agent hosts need no inbound ports at all. See [Ports](../reference/ports.md).

The managed proxy container `dockmesh-proxy` uses host networking, and its admin API on `127.0.0.1:2019` has no authentication. It refuses requests from browsers on other sites, and proxy routes can't forward to it, but any process on the server, and any container started with host networking, can change the proxy configuration. Don't run untrusted workloads with `network_mode: host` on the server.

**Metrics.** `/metrics` requires a bearer token with Settings & audit: View on everything. Keep `DOCKMESH_METRICS_AUTH` at its default (`true`) unless the endpoint is only reachable from your monitoring network.

## Setup wizard

The setup wizard is open only while no admin user exists, for 30 minutes after each server start; `DOCKMESH_SETUP_FORCE=true` never reopens it once an admin exists. The wizard's endpoints need no login; they refuse cross-site browser requests. Its URL test makes the server fetch the URL you enter, but refuses loopback, link-local and multicast addresses and private addresses of other machines; only this server's own private addresses on ports 80, 443 and its HTTP port are allowed. Finish setup right after installing, from a trusted network, and keep port 8080 closed to everyone else until then. Afterwards, check that `DOCKMESH_SETUP_FORCE` is gone from `dockmesh.env`. The wizard removes it, but reports a warning if it can't.

## Users and roles

- Proxy, users and roles, settings and the audit log only follow from assignments on everything. Give people narrow assignments (a host, a host tag or a stack) where you can.
- The extras are the risky part of a role: the terminal, files and environment-values extras reveal secrets inside containers, downloading backups hands out full volume data, and exporting the backup key decrypts every backup. Keep them off custom roles unless needed.
- Backup jobs with hooks run commands in containers and need the terminal extra on those containers.
- Grants never exceed the granter's own access, and the last admin assignment on everything can't be removed.
- Review users, roles and [API tokens](../features/api-tokens.md) regularly, and restrict tokens to what they need.
- Password rotation, password reuse rules and requiring 2FA for admins don't exist. Ask admins to set up two-factor authentication themselves. See [Authentication](../features/authentication.md#sign-in-options-that-dont-exist).

See [Permissions](../concepts/permissions.md).

## Git sources

The push webhook of a git-backed stack only works with a webhook secret of at least 16 characters; without one, every webhook request is rejected. Git credentials and webhook secrets are stored encrypted. See [Git sources](../features/git-sources.md).

## Agents

- The install command downloads a script from `DOCKMESH_BASE_URL`. The script carries the CA fingerprint and the checksums of the agent binary, so serve the base URL over `https://`.
- The agent enrolls over HTTPS on the agent port and checks the server's certificate against the pinned CA fingerprint. It refuses plain-HTTP enrollment unless started with `--insecure-enroll` (or `DOCKMESH_INSECURE_ENROLL=true`); use that only in an isolated lab.
- Enrollment tokens expire after 24 hours (`DOCKMESH_AGENT_ENROLL_TOKEN_TTL`, a Go duration such as `72h`). Treat an install command like a password until then, and revoke hosts you never finished enrolling.
- Agent self-upgrades download the binary from the agent listener over the agent's mTLS connection and check it against the SHA-256 the server sends.
- Revoking a host closes a connected agent's session. Stop the agent service on the host as well; see [Uninstall](./uninstall.md#remove-an-agent).

## Secrets and files

- Leave `DOCKMESH_SECRETS_ENCRYPT_ENV` at `true`. With it off, stack `.env` files, registry passwords, SSO client secrets, the LDAP bind password, backup target credentials and git credentials are stored in plaintext, and backups are not encrypted.
- Some secrets are plaintext even with encryption on: the agent's mirrored `.env` files on remote hosts (mode `0600`), secrets generated by templates (written into `compose.yaml`), and, in the database, global environment variables not marked as secret, notification channel tokens and passwords, the SAML service-provider key, TOTP secrets and the audit webhook secret. Protect the database file and every copy of it, including system backups and `dockmesh db backup` output.
- Keep the data root private. `install.sh` creates it as `dockmesh:docker` with mode `750`, and the server creates the age key with mode `0400`. Installs moved off root by an upgrade are `dockmesh:dockmesh` with mode `700`. On a fresh install, members of the `docker` group can read it, so keep that group to people who would have root anyway, or tighten it:

  ```bash
  sudo chmod 700 /var/lib/dockmesh
  ```

- Store the exported age key and backups off the server, see [Disaster recovery](./disaster-recovery.md).
- Run the service as the `dockmesh` user (the default). `systemctl show -p User --value dockmesh` should print `dockmesh`.

## Workloads

Dockmesh deploys what the compose file says, including `privileged: true`, host bind mounts and the Docker socket. Whoever can edit or deploy a stack (Stacks: Deploy or higher) can take over the host. Keep that level to people you would give shell access.

## Outbound connections

Dockmesh sends no telemetry. Everything below is a connection the server, the agent or the Docker daemon they drive opens to another system, with what triggers it and how to turn it off. DNS lookups for these names go to the host's resolver.

### Server

| Connection | Goes to | When | How to turn it off |
|---|---|---|---|
| Release check | `api.github.com` (latest Dockmesh release) and `github.com/Dockmesh/dockmesh/releases/download/…` (its `release-manifest.json`) | 30 seconds after start, then every 2 hours by default (15 minutes to 7 days), and on **Check now** | **Settings → Updates**: switch off **Check for updates**. **Check now** still works |
| Image update checks | the registry of every image a running container on the server uses, through the server's Docker daemon, with credentials from **Registries** | every 30 minutes | Can't be turned off. Agent hosts aren't checked |
| Update details | `hub.docker.com` (tag and description) and `api.github.com` (the project's latest release) for Docker Hub images | when you open a container's updates | Don't open it |
| Image pulls | registries, through the Docker daemon of the host | deploys, **Pull**, image updates; helper images `alpine` and `busybox` from Docker Hub for volume backups, browsing and copies; the Caddy image when the proxy is enabled | Mirror the images and use a registry you control |
| Grype vulnerability database | Anchore's servers (`grype` subprocess) | on scans: the first scan downloads the database, later scans check for a newer one | `GRYPE_DB_AUTO_UPDATE=false` in `dockmesh.env`; Grype may then refuse to scan once the database is too old |
| Grype application update check | Anchore's servers (`grype` subprocess) | each scan | `GRYPE_CHECK_FOR_APP_UPDATE=false` in `dockmesh.env`. Dockmesh doesn't set it; Grype inherits the service's environment |
| Notification channels | the channel's URL or server: webhook, ntfy, Discord, Slack, Microsoft Teams, Gotify, SMTP, `events.pagerduty.com` (PagerDuty), `api.pushover.net` (Pushover) | when an alert fires or resolves, and on test | Remove or don't create the channel |
| Audit webhook | the URL under **Audit settings → Forwarding** | every new audit entry, and **Send test** | Switch the webhook off |
| OIDC providers | the issuer (discovery, keys, token endpoint) | sign-in through the provider (discovery cached for 30 minutes), **Test discovery** | Disable or delete the provider |
| OAuth2 providers | token and userinfo URLs | sign-in, provider test | Disable or delete the provider |
| LDAP providers | the directory host and port | every LDAP sign-in, provider test | Disable or delete the provider |
| SAML providers | none; the browser carries the redirects, and the IdP metadata is pasted | | |
| Backup targets | S3, SFTP, SMB or WebDAV servers | backup runs, restores, target tests, audit log archiving to a target | Use local targets only |
| Git sources | the repository URL (HTTPS or SSH) | every 300 seconds per git-backed stack by default, on webhook and on manual sync | Set the poll interval to 0; sync by webhook or by hand |
| Certificate checks | `<site hostname>:443` for each proxy site | the reverse proxy and overview pages, alert rules for certificate expiry | Don't create proxy sites, or rules for certificate expiry |
| Agent URL check | the configured agent URL | **Add host** and **Settings → General** check it on demand | Don't run it |
| Setup URL test | the URL entered in the setup wizard | **Test connection** in the wizard | Don't run it |
| Tracing | `DOCKMESH_OTEL_ENDPOINT` (OTLP/gRPC) | continuously while set | Leave it unset (default) |

The managed Caddy proxy, when enabled, gets certificates for sites with automatic HTTPS from the certificate authority set under the proxy settings (Let's Encrypt by default, its staging environment, or ZeroSSL) and contacts it to issue and renew them. Sites with TLS set to internal or none don't use ACME. See [Reverse proxy](../features/reverse-proxy.md#proxy-settings).

The browser loads template icons from the HTTPS URLs the templates name; the Content-Security-Policy allows `https:` images for that.

### Agent

| Connection | Goes to | When | How to turn it off |
|---|---|---|---|
| Server connection | the agent URL (`wss://…:8443/connect`), mTLS | always; reconnects after a drop | Stop the agent |
| Enrollment | the enroll URL on the agent port (`https://…:8443/enroll`) | first start | Not needed after enrolling |
| Self-upgrade | the agent listener's binary endpoint, mTLS | when an admin upgrades the agent | Don't upgrade from the UI |
| Image pulls | registries, through the host's Docker daemon, with credentials the server sends for registries in scope | deploys and image updates on that host; `busybox` from Docker Hub for volume exports | Mirror the images |

Backups of agent volumes stream through the agent connection; the server writes them to the backup target. The agent installer itself downloads the installer script and binary from `DOCKMESH_BASE_URL` and, when Docker is missing, runs `https://get.docker.com`. The server installer `install.sh` downloads from GitHub and installs Grype from `raw.githubusercontent.com/anchore/grype` unless `DOCKMESH_NO_SCANNER=1` is set.
