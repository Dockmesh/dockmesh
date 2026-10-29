---
title: Ports and network access
description: Which ports the Dockmesh server, the agents and the optional reverse proxy use, and what must reach what.
---

Use this page when you write firewall rules or put Dockmesh behind a load balancer. All ports below are defaults; the server ports change with `DOCKMESH_HTTP_ADDR` and `DOCKMESH_AGENT_LISTEN` (see [Environment variables](./environment-variables.md)).

## Server

| Port | Bound by | Needed when | Who connects |
|---|---|---|---|
| 8080/tcp | `dockmesh` (`DOCKMESH_HTTP_ADDR`) | always | Browsers, dmctl, API clients, Prometheus; agent hosts during install, enrollment and upgrades |
| 8443/tcp | `dockmesh` (`DOCKMESH_AGENT_LISTEN`) | you have remote hosts | Agents (mTLS, WebSocket at `/connect`) |
| 80/tcp, 443/tcp | `dockmesh-proxy` container (host network) | the reverse proxy is enabled | Clients of the routed apps, ACME certificate challenges |
| 127.0.0.1:2019/tcp | `dockmesh-proxy` container | the reverse proxy is enabled | The Dockmesh server only (Caddy admin API) |

Port 8080 serves plain HTTP: the web UI, `/api/v1/*`, the WebSocket endpoints, `/metrics`, `/healthz/live`, `/healthz/ready` and the agent installer under `/install/`. Dockmesh does not terminate TLS on it. For HTTPS, put a reverse proxy in front (your own, or a route on the built-in proxy pointing at the server) and set `DOCKMESH_BASE_URL` to the `https://` address.

Port 8443 uses TLS with a certificate from Dockmesh's own CA and requires a client certificate on every connection. It is not used by browsers or dmctl. The listener starts whenever `DOCKMESH_AGENT_LISTEN` is set, which is the default, even if you have no agents. Set it to an empty value to turn it off on single-host installs.

<!-- update after: proxy: keep the Caddy container, resume routes, serve tls none as http -->
When the reverse proxy is enabled, Dockmesh runs the `caddy:2` image as container `dockmesh-proxy` with host networking. Caddy listens on `:80` and `:443` of the host, so nothing else on the host may use those ports, and it serves both ports even for routes set to `tls none`. Its admin API on `127.0.0.1:2019` has no authentication: any process on the host that can open a local TCP connection can change the proxy configuration. Do not run untrusted workloads with host networking on the server. See [Reverse proxy](../features/reverse-proxy.md).

The server also reaches out:

| Destination | Why |
|---|---|
| Docker socket (`/var/run/docker.sock`) or `DOCKER_HOST` | Manage the local host |
| Container registries (443) | Image pulls, update checks |
| `api.github.com` (443) | Release check (can be turned off in **Settings**) |
| Git remotes | Git-backed stacks |
| Backup targets (S3, SFTP, SMB, WebDAV) | Backups |
| SMTP and webhook endpoints | Notifications |
| Your IdP | OIDC, SAML, OAuth2 or LDAP sign-in |
| Grype's vulnerability database | Image scans |

## Agent hosts

Agents never listen on a port. They connect out to the server:

| Destination | When |
|---|---|
| `DOCKMESH_BASE_URL` (8080 by default) | Install script and binary download, enrollment (`POST /api/v1/agents/enroll`), agent self-upgrades |
| `DOCKMESH_AGENT_PUBLIC_URL` (8443 by default) | Always, one long-lived WebSocket |
| Container registries | Image pulls on the agent host |

So the server needs 8443 reachable from every agent host, and 8080 (or whatever `DOCKMESH_BASE_URL` points at) reachable at least while you install, enroll or upgrade an agent. The agent's hostname or IP in `DOCKMESH_AGENT_PUBLIC_URL` must be covered by `DOCKMESH_AGENT_SANS`.

<!-- update after: agents: pin the CA at enrollment and verify self-upgrades -->
Enrollment does not verify the server's TLS certificate and also works over plain `http://`. Whoever can intercept that first request can impersonate the server to the agent, so enroll over a network you trust (LAN, VPN) rather than across the internet.

## Firewall examples

Allow the UI from an admin network, the agent port from the agent network, and nothing else:

```bash
# ufw on the Dockmesh server
sudo ufw default deny incoming
sudo ufw allow from 10.0.10.0/24 to any port 8080 proto tcp   # admins, CI
sudo ufw allow from 10.0.20.0/24 to any port 8080 proto tcp   # agents: enroll + upgrades
sudo ufw allow from 10.0.20.0/24 to any port 8443 proto tcp   # agents: connection
# only with the reverse proxy enabled
sudo ufw allow 80/tcp
sudo ufw allow 443/tcp
sudo ufw enable
```

```text
# nftables
table inet filter {
  chain input {
    type filter hook input priority 0; policy drop;
    iifname lo accept
    ct state established,related accept
    ip saddr 10.0.10.0/24 tcp dport 8080 accept
    ip saddr 10.0.20.0/24 tcp dport { 8080, 8443 } accept
    tcp dport { 80, 443 } accept   # reverse proxy only
  }
}
```

Docker publishes container ports through its own iptables rules, which bypass ufw's `INPUT` chain. Ports your stacks publish are reachable regardless of the rules above unless you bind them to an address (`127.0.0.1:5432:5432`) or filter in the `DOCKER-USER` chain.

On agent hosts, no inbound rule is needed for Dockmesh. Allow outbound traffic to the server on 8080 and 8443.

For a single-host install without agents, set `DOCKMESH_AGENT_LISTEN=` (empty) and expose only 8080 (or only your TLS proxy). More advice in [Hardening](../operations/hardening.md).
