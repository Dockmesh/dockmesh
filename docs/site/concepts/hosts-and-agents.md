---
title: Hosts and agents
description: The difference between the local host and remote hosts, how agents enroll and stay connected, and which features work on remote hosts.
---

A host is a Docker engine that Dockmesh manages. There are two kinds:

- **The local host** is the Docker engine on the machine running the `dockmesh` server. Its host ID is `local`. The server talks to it directly through the Docker SDK.
- **Remote hosts** run `dockmesh-agent`, which connects out to the server and carries out Docker operations locally. Each remote host has its own ID, assigned when you add it.

All hosts appear on the **Hosts** page. The host filter in the top bar limits every page to one host; **Set as lens** on a host's page does the same. A remote host whose agent isn't connected shows as offline; see [The host shows offline](../troubleshooting/troubleshooting.md#the-host-shows-offline).

## The agent model

The agent only makes outbound connections. Remote hosts need no open inbound ports. They need to reach two addresses on the server:

- The **base URL**, while you install the agent. The install script and the agent binary are downloaded from it.
- The **agent URL** (`wss://<server>:8443/connect` by default), always. Enrollment, the permanent connection, certificate renewal and agent upgrades all go through the server's agent port.

Both URLs are set under **Settings → General** (**Base URL** and **Agent public URL**). `DOCKMESH_BASE_URL` and `DOCKMESH_AGENT_PUBLIC_URL` in `dockmesh.env` win over the fields. An empty agent URL is derived from the base URL's host and the agent port. See [Multi-host](../features/multi-host.md#before-you-add-the-first-agent).

### Adding a host

1. On **Hosts**, click **Add host**, enter a name and click **Generate install command**. The server creates a pending host and a one-time enrollment token. It stores only a hash of the token. The token is valid for 1 hour (`DOCKMESH_AGENT_ENROLL_TOKEN_TTL` changes this); **New command** replaces it.
2. The dialog shows an install command:

   ```bash
   curl -fsSLk --pinnedpubkey 'sha256//<pin>' https://<agent URL host>:<port>/install/agent.sh | sudo bash -s -- --token <token>
   ```

   The pin is the hash of the agent listener's public key. curl refuses any other server before the script runs, so the install is pinned end to end even when the base URL is plain HTTP.
3. Run the command on the remote host as root. The script has both URLs, the key pin, the agent CA and the SHA-256 checksums of the agent binaries filled in; the token comes from `--token`. It picks the `amd64` or `arm64` binary for the host, checks its checksum, and sets up a systemd service. See [What the installer does](../features/multi-host.md#what-the-installer-does).
4. On first start the agent sends the token to `https://<agent URL host>:<port>/enroll` on the agent port. It only trusts the agent CA the install script wrote, after checking its fingerprint (`DOCKMESH_CA_FINGERPRINT`), so it can't be redirected to another server. The server generates a key pair and a client certificate signed by its CA (ECDSA P-256, valid 365 days, the host ID as Common Name) and returns them. The token can't be used again.

### The connection

After enrollment the agent dials the agent URL. The `/connect` endpoint requires a client certificate signed by the server's CA (mutual TLS, TLS 1.2 or later). The server looks the certificate up by fingerprint and rejects unknown ones.

Messages are JSON frames over a single WebSocket: the server sends requests (list containers, deploy a stack, start a log stream) and the agent replies. The agent sends a heartbeat every 15 seconds and the server pings every 30 seconds. The server marks a host offline after 60 seconds without a frame; the agent gives up on a silent server after 90 seconds. The agent then reconnects at once, and after that waits 1 second, doubling up to 60 seconds with some random jitter.

Requests to an agent have time limits by type: 15 minutes for deploys and scaling, 30 minutes for commands run in a container (backup hooks, for example), 11 minutes for agent upgrades, 3 minutes for container stop/restart/remove, stack stop, image remove/prune and container file operations, and 30 seconds for everything else. When a request is cancelled or times out, the server tells the agent to stop it.

### Certificates, revocation and re-enrollment

Client certificates are valid for 365 days. An online agent asks for a new one over its existing connection once less than 30 days are left; the host keeps its ID. The host page shows when the certificate expires. Admins get a notice for an agent whose certificate expires within 14 days, and a critical one for an offline agent whose certificate has expired.

**Re-issue…** on the host's **Maintenance** tab (needs `hosts.create`) creates a new enrollment token for the same host. Running the new install command on the host re-enrolls it under the same ID, so its stacks, tags and backup jobs stay. The old certificate keeps working until the host enrolls with the new token. Use this for an expired or suspect certificate, or after reinstalling the host.

**Revoke…** in the host's row menu or on the **Maintenance** tab (needs `hosts.delete`) deletes the host from Dockmesh, after you type its name. An open connection is closed and the certificate is no longer accepted. Containers on that host keep running; Dockmesh stops managing them. Stop the agent on the host as well (`systemctl disable --now dockmesh-agent`). To bring the host back, add it again; it gets a new ID.

`dockmesh ca rotate` replaces the CA and the server certificate and resets every agent to pending with a new token, so every host has to be enrolled again.

### Agent upgrades

When an agent runs a different build than the server, the Hosts page marks it and offers an upgrade. The agent downloads the new binary from the agent port and checks it against the SHA-256 checksum the server sends before it replaces itself. The upgrade mode decides whether this happens on its own: `manual` (default), `auto` or `staged`. Agents that speak an older protocol than the server show **agent update recommended**; features they don't support are skipped or fail with a message. See [Agent upgrades](../features/multi-host.md#agent-upgrades).

## What works on remote hosts

Most features work on every host: container lists and lifecycle, logs, exec, live stats and metrics history, the container file browser, image, volume and network lists and inspect, image remove and prune, volume browsing, stack deploy, stop and scale, alert rules, backups and restores of volumes and stacks, git-source auto-deploys (they run on the host the stack is deployed on), drains and stack migration. Private registry credentials are sent to agents for the images they deploy; see [Registries](../features/registries.md).

Some features need an agent that supports them, for example the file browser, metrics history and restores in replace mode. On an older agent they fail with a message to update the agent.

These work on the local host only:

| Feature | On a remote host |
|---|---|
| Rolling updates | Returns an error. |
| Stack cleanup (preview and delete of leftover resources) | Returns an error. |
| Image pull; volume and network create, remove and prune | Always run on the local host, whatever host is selected. |
| Vulnerability scanning | Scans images on the local host only. |
| Image update detection and one-click update | Covers local containers only. |
| Auto-scaling rules | Scale on the local host only. |
| Live deploy progress | Not reported; you see the result when the deploy finishes. |

Stack migration moves the stack definition and redeploys it on the target host. Stacks with named volumes are refused, because their data isn't copied. See [Migration and drain](../features/migration-and-drain.md).

## Host tags

A host can carry up to 20 tags. Tags are lowercase letters, digits and hyphens, start with a letter or digit, and are at most 32 characters (`prod`, `eu-west`, `team-web`). Set them in the **Add host** dialog, with **Edit tags** in the host's row menu or on the **Tags** tab of the host page; changing them needs `hosts.tag`. The local host can be tagged too.

Tags are used for:

- **Filtering** on the Hosts page and in the host filter (`#tag`).
- **Scoping access.** An assignment on a host tag covers every host carrying the tag. See [Permissions](./permissions.md#scopes).
- **Registry credentials.** A credential with scope tags is used only on hosts that carry one of them; a credential without scope tags is used on every host.
- **Alert rules**, which can be limited to a host tag.

## Querying all hosts at once

Pick **All hosts** in the host filter, or pass `?host=all` to the API, to aggregate lists across every online host. This works for containers, images, volumes, networks and host metrics. The server queries all hosts in parallel with a 3-second limit per host and returns whatever arrived; hosts that failed or timed out are listed in the response's `unreachable_hosts` field. Without a `host` parameter the API uses the local host.

## Related

- [Multi-host](../features/multi-host.md): adding, monitoring and removing hosts step by step
- [Ports](../reference/ports.md): firewall rules for the agent port
- [Security hardening](../operations/hardening.md): protecting the server agents connect to
