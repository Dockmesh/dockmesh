---
title: Hosts and agents
description: The difference between the local host and remote hosts, how agents enroll and stay connected, and which features work on remote hosts.
---

A host is a Docker engine that Dockmesh manages. There are two kinds:

- **The local host** is the Docker engine on the machine running the `dockmesh` server. Its host ID is `local`. The server talks to it directly through the Docker SDK.
- **Remote hosts** run `dockmesh-agent`, which connects out to the server and carries out Docker operations locally. Each remote host has its own ID, assigned when you add it.

All hosts appear on the **Hosts** page and in the host switcher at the top of the UI.

## The agent model

The agent only makes outbound connections. Remote hosts need no open inbound ports; they need to reach the server's HTTP port for enrollment and agent upgrades (the new binary is downloaded from `/install/`), and the agent port (default `8443`) for the permanent connection.

### Adding a host

1. On **Hosts**, click **Add host** and enter a name. The server creates a pending host record and a one-time enrollment token. It stores only a hash of the token.
2. The dialog shows an install command:

   ```bash
   curl -fsSL https://<server>/install/agent.sh?token=<token> | sudo bash
   ```

   The server builds this URL from `DOCKMESH_BASE_URL`, so set that to an address the remote host can reach.
3. Run the command on the remote host as root. The script requires Linux with systemd, installs Docker through `get.docker.com` if it is missing (unless you pass `--skip-docker`), creates a `dockmesh-agent` system user in the `docker` group (unless you pass `--as-root`), downloads the agent binary from the server, writes `/etc/dockmesh-agent/agent.env` and starts the `dockmesh-agent` systemd service. To pass flags, use `| sudo bash -s -- --as-root`.
4. On first start the agent calls `POST /api/v1/agents/enroll` with the token. The server generates a key pair and a client certificate signed by its internal CA (ECDSA P-256, valid 365 days, the host ID as Common Name) and returns them with the CA certificate. The agent saves `agent.crt`, `agent.key`, `ca.crt` and `agent.url` in its data directory (`/var/lib/dockmesh`). The token cannot be used again.

<!-- update after: agents: pin the CA at enrollment and verify self-upgrades -->
Enrollment tokens do not expire until they are used. During enrollment the agent does not verify the server's TLS certificate; it trusts whatever CA the enrollment response contains. Enroll over a network you trust, and don't leave unused tokens lying around. The one-line installer always downloads the `linux-amd64` agent binary; on arm64 hosts, replace `/var/lib/dockmesh/bin/dockmesh-agent` with the `dockmesh-agent` binary from the linux-arm64 release tarball and restart the service. The installer is Linux-only.

### The connection

After enrollment the agent dials the URL in `agent.url`, which is `DOCKMESH_AGENT_PUBLIC_URL` if set, otherwise `wss://<host of DOCKMESH_BASE_URL>:<agent port>/connect`. The `/connect` endpoint requires a client certificate signed by the server's CA (mutual TLS, TLS 1.2 or later). The server looks the certificate up by fingerprint and rejects unknown ones.

Messages are JSON frames over a single WebSocket: the server sends requests (list containers, deploy a stack, start a log stream) and the agent replies. The agent sends a heartbeat every 15 seconds; the server pings every 30 seconds and marks the host offline after 60 seconds of silence. When the connection drops, the agent reconnects with exponential backoff from 1 to 60 seconds.

Add extra hostnames or IPs to the server's agent certificate with `DOCKMESH_AGENT_SANS` (comma-separated) when agents reach the server under a name other than the one in `DOCKMESH_BASE_URL`.

<!-- update after: agents: survive disconnects, flow-control streams, renew certs -->
Each request to an agent times out after 30 seconds. That includes deploys, so a deploy that has to pull large images on a remote host can report a timeout even though the agent keeps working. Pre-pull big images on the host, or deploy again once the pull has finished.

### Certificates, revocation and re-enrollment

<!-- update after: agents: survive disconnects, flow-control streams, renew certs -->
Client certificates are valid for 365 days and are not renewed automatically. Before a certificate expires, revoke the host and add it again.

**Revoke** (in the host's row menu or on the host detail page) deletes the host record. The agent's certificate is rejected on its next connection. A connection that is already open is not cut immediately, so stop the agent on the host as well (`systemctl disable --now dockmesh-agent`). Containers on that host keep running; Dockmesh just stops managing them.

<!-- update after: agents: pin the CA at enrollment and verify self-upgrades -->
**Re-issue token** on the host detail page creates a new token, but the server only accepts enrollment for hosts that are still pending. For a host that has already enrolled, revoke it and add it again instead. Running the new install command on the host removes the old certificate files before enrolling.

`dockmesh ca rotate` replaces the CA and server certificate and resets every agent to pending with a new token, so every host has to be enrolled again.

### Agent upgrades

<!-- update after: agents: pin the CA at enrollment and verify self-upgrades -->
The upgrade mode decides what happens to agents whose version differs from the server's: `auto`, `staged` (a percentage of agents per wave) or `manual`. The default is `manual`, where you click **Upgrade now** on the host detail page. The **Agent upgrade policy** panel on the Hosts page shows the mode and how many agents are behind; changing the mode is done through the API (`PUT /api/v1/agents/upgrade-policy`). The agent downloads the new binary from the server without a checksum check. See [Upgrade](../operations/upgrade.md#upgrading-agents).

## What works on remote hosts

Most day-to-day features work on every host: container lists and lifecycle, logs, exec, live stats, image, volume and network lists and inspect, stack deploy, stop and scale, host metrics, drains and stack migration.

These work on the local host only:

| Feature | On a remote host |
|---|---|
| Rolling updates | Returns an error. |
| Stack cleanup (preview and delete of leftover resources) | Returns an error. |
| Container file browser | Returns an error. |
| Image pull, volume and network create, remove and prune | Always run on the local host, whatever host is selected. |
| Container metrics history | Only live stats; no stored history. |
| Alerts | Rules evaluate local containers only. |
| Vulnerability scanning | Scans images on the local host only. |
| Image update detection and one-click update | Watches local containers only. |
| Live deploy progress | Not reported; you see the result when the deploy finishes. |
| Git-source auto-deploy and auto-scaling rules | Run against the local host only. |
| Private registry credentials | Not sent to agents; pulls on remote hosts are anonymous. Pre-pull private images on the host (`docker login` and `docker pull`); see [Registries](../features/registries.md). |

Volume browsing on a remote host works only when the agent runs as root (`--as-root`). Volume backups of remote volumes stream through the server; restores write to the local host. See [Backups](../operations/backups.md).

<!-- update after: fs: contain paths in volume browse, restore and git sources -->
Stack migration moves the stack definition and redeploys it on the target host, but it does not copy volume data: the target starts with empty volumes. See [Migration and drain](../features/migration-and-drain.md).

## Host tags

A host can carry up to 20 tags. Tags are lowercase letters, digits and hyphens, start with a letter or digit, and are at most 32 characters (`prod`, `eu-west`, `team-web`). Edit them with **Edit tags** in the host's row menu or on the **Tags** tab of the host detail page; this needs `hosts.tag`. The local host can be tagged too.

Tags are used for:

- **Searching** on the Hosts page.
- **Scoping access.** A user's scope tags and a custom role's `host_tag` scope match against host tags. See [Permissions](./permissions.md#scopes).

Leave the **Scope (host tags)** field on saved registry credentials empty: Dockmesh never passes host tags when it looks up credentials, so a credential with any scope tag is never used.

## Querying all hosts at once

Pick **All hosts** in the host switcher, or pass `?host=all` to the API, to aggregate lists across every online host. This works for containers, images, volumes, networks and host metrics. The server queries all hosts in parallel with a 3-second limit per host and returns whatever arrived; hosts that failed or timed out are listed in the response's `unreachable_hosts` field. Without a `host` parameter the API uses the local host.

## Related

- [Multi-host](../features/multi-host.md): adding, monitoring and removing hosts step by step
- [Ports](../reference/ports.md): firewall rules for the agent port
- [Security hardening](../operations/hardening.md): protecting the server agents connect to
