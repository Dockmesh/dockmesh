---
title: Ports and network access
description: Which ports the Dockmesh server, the agents and the optional reverse proxy use, and what must reach what.
---

Use this page when you write firewall rules or put Dockmesh behind a load balancer. All ports below are defaults; the server ports change with `DOCKMESH_HTTP_ADDR` and `DOCKMESH_AGENT_LISTEN` (see [Environment variables](./environment-variables.md)).

## Server

| Port | Bound by | Needed when | Who connects |
|---|---|---|---|
| 8080/tcp | `dockmesh` (`DOCKMESH_HTTP_ADDR`) | always | Browsers, dmctl, API clients, Prometheus; agent hosts while the install script and the agent binary download |
| 8443/tcp | `dockmesh` (`DOCKMESH_AGENT_LISTEN`) | you have remote hosts | Agents: enrollment, the permanent connection, certificate renewal and upgrades |
| 80/tcp | `dockmesh-proxy` container (host network) | the reverse proxy is enabled | Clients of the routed apps, ACME HTTP challenges, redirects to HTTPS |
| 443/tcp | `dockmesh-proxy` container (host network) | at least one proxy site uses TLS | Clients of the routed apps |
| 127.0.0.1:2019/tcp | `dockmesh-proxy` container | the reverse proxy is enabled | The Dockmesh server only (Caddy admin API) |

Port 8080 serves plain HTTP: the web UI, `/api/v1/*`, the WebSocket endpoints, `/metrics`, `/healthz/live`, `/healthz/ready` and the agent installer and binaries under `/install/`. Dockmesh does not terminate TLS on it. For HTTPS, put a reverse proxy in front (your own, or a site on the built-in proxy pointing at the server) and set the base URL under **Settings → General** to the `https://` address. SSO callback URLs and invite links are built from `DOCKMESH_BASE_URL` only, so set that variable to the same address as well. Forwarding headers (`X-Forwarded-For`, `X-Real-IP`, `X-Forwarded-Proto`) are only believed from the addresses under **Trusted proxies** on the same page, by default localhost only.

Port 8443 uses TLS with a certificate from Dockmesh's own agent CA. It serves three paths: `/enroll` (the enrollment token is the credential), `/connect` and `/agent-binary/` (both need a client certificate from that CA). It is not used by browsers or dmctl. The listener always starts, even if you have no agents; an empty `DOCKMESH_AGENT_LISTEN` falls back to `:8443`. Block the port in the firewall on single-host installs. Agents must reach it directly: a proxy that terminates TLS in front of it breaks the client certificates.

When the reverse proxy is enabled, Dockmesh runs Caddy (`caddy:2.11.7`, pinned by digest, unless `DOCKMESH_PROXY_IMAGE` names another image) as container `dockmesh-proxy` with host networking. Caddy listens on `:80` of the host, and on `:443` once a site uses TLS, so nothing else on the host may use those ports. Sites set to no TLS are served on port 80 only. Its admin API on `127.0.0.1:2019` accepts only requests with a local `Host` and `Origin`, which stops browsers and DNS rebinding, but has no authentication: any process on the host, and any container with `network_mode: host`, can change the proxy configuration. Do not run untrusted workloads with host networking on the server. See [Reverse proxy](../features/reverse-proxy.md).

The server also reaches out:

| Destination | Why |
|---|---|
| Docker socket (`/var/run/docker.sock`) or `DOCKER_HOST` | Manage the local host |
| Container registries (443) | Image pulls, update checks |
| `api.github.com` (443) | Release check (turn off under **Settings → Updates**) |
| Git remotes | Git-backed stacks |
| Backup targets (S3, SFTP, SMB, WebDAV) | Backups |
| SMTP and webhook endpoints | Notifications |
| Your IdP | OIDC, SAML, OAuth2 or LDAP sign-in |
| Grype's vulnerability database | Image scans |

## Agent hosts

Agents never listen on a port. They connect out to the server:

| Destination | When |
|---|---|
| Base URL (8080 by default, 443 behind a TLS proxy) | Install script and agent binary download, only while you install |
| Agent URL (8443 by default) | Enrollment, then always: one long-lived WebSocket; certificate renewal and agent upgrades go over the same port |
| Container registries | Image pulls on the agent host |
| `get.docker.com` | Only if the installer has to install Docker |

So the server needs 8443 reachable from every agent host, and the base URL reachable while you install an agent. The host name or IP in the agent URL must be covered by the agent listener's certificate; Dockmesh adds the agent URL's host itself, and `DOCKMESH_AGENT_SANS` adds more. **Check connection** under **Settings → General** tests this from the server; see [Multi-host](../features/multi-host.md#check-the-agent-url).

Enrollment runs over TLS on the agent port, and the agent trusts only the agent CA pinned in the install script. The install script itself comes from the base URL, so serve that over `https://` when agent hosts reach it across an untrusted network.

## Firewall examples

The recommended layout puts a TLS reverse proxy in front of Dockmesh on the same server: users, CI and agent hosts reach the UI, API and installer on 443, port 8080 is only reachable from the server itself, and 8443 is open to agent hosts only. Bind Dockmesh to loopback with `DOCKMESH_HTTP_ADDR=127.0.0.1:8080`, or block 8080 in the firewall:

```bash
# ufw on the Dockmesh server
sudo ufw default deny incoming
sudo ufw allow 443/tcp                                        # TLS proxy: users, CI, agent installer
sudo ufw allow from 10.0.20.0/24 to any port 8443 proto tcp   # agents: enrollment and connection
sudo ufw enable
```

```text
# nftables
table inet filter {
  chain input {
    type filter hook input priority 0; policy drop;
    iifname lo accept
    ct state established,related accept
    tcp dport 443 accept
    ip saddr 10.0.20.0/24 tcp dport 8443 accept
  }
}
```

If you use the managed Caddy proxy for your apps, it already owns 80 and 443 on the host; allow `80/tcp` as well for ACME HTTP challenges, and serve Dockmesh itself through one of its sites or a separate proxy.

> **Warning:** Opening 8080 directly (for example `ufw allow from 10.0.10.0/24 to any port 8080`) means plain HTTP: passwords, tokens and agent install commands cross the network unencrypted. Only do this on a trusted LAN or VPN. See [Security hardening](../operations/hardening.md).

Docker publishes container ports through its own iptables rules, which bypass ufw's `INPUT` chain. Ports your stacks publish are reachable regardless of the rules above unless you bind them to an address (`127.0.0.1:5432:5432`) or filter in the `DOCKER-USER` chain.

On agent hosts, no inbound rule is needed for Dockmesh. Allow outbound traffic to the server on 8443, and on the port of the base URL (443 behind a proxy) while you install.

For a single-host install without agents, block 8443 (the listener always runs; an empty `DOCKMESH_AGENT_LISTEN` falls back to `:8443`) and expose only your TLS proxy. More advice in [Security hardening](../operations/hardening.md).
