---
title: Multi-host
description: Add remote Docker hosts with the outbound mTLS agent, manage their tags, upgrades and certificates, and remove them again.
---

Dockmesh manages the local host directly through the Docker socket. Every other host runs `dockmesh-agent`, which dials out to the server over a mutually authenticated TLS WebSocket. The remote host needs no open inbound port; it only has to reach the server. See [Hosts and agents](../concepts/hosts-and-agents.md) for the model behind this.

## Before you add the first agent

The server builds the install command and the agent's connection URL from its own configuration. With the defaults, both point at `localhost`, which a remote host can't reach. Set these in the server's environment file (`dockmesh.env` for installs done with `install.sh`) and restart the server:

| Variable | Default | Purpose |
|---|---|---|
| `DOCKMESH_BASE_URL` | `http://localhost:8080` | URL agents use to download the installer and enroll. Must be reachable from the remote host. |
| `DOCKMESH_AGENT_LISTEN` | `:8443` | Address of the mTLS agent listener on the server. |
| `DOCKMESH_AGENT_PUBLIC_URL` | derived | `wss://` URL agents dial, e.g. `wss://dockmesh.example.com:8443/connect`. If unset, Dockmesh takes the host name from `DOCKMESH_BASE_URL` and the port from `DOCKMESH_AGENT_LISTEN`. |
| `DOCKMESH_AGENT_SANS` | empty | Comma-separated host names and IPs added to the agent listener's server certificate. Include every name or IP that appears in `DOCKMESH_AGENT_PUBLIC_URL`. |

> **Note:** The public URL you enter in the web setup wizard is stored as a setting but isn't used for agent enrollment. Set `DOCKMESH_BASE_URL` in the environment file as well.

The server certificate always covers `localhost`, `127.0.0.1` and `::1`. When the SAN list contains a name the current certificate does not cover, Dockmesh issues a new server certificate on the next start. `dockmesh doctor` warns when `DOCKMESH_AGENT_PUBLIC_URL` isn't set. Port 8443 (or whatever you chose) has to be open on the server, not on the agent host. See [Ports](../reference/ports.md).

## Add a host

1. Open **Hosts** and click **Add host**.
2. Enter a **Hostname** (the name shown in Dockmesh) and, optionally, tags.
3. Click **Generate install command**. The dialog shows a one-line command and the enrollment token.
4. Run the command on the remote host as root.
5. The dialog polls every 2 seconds and closes once the agent reports **online**.

The command looks like this:

```bash
curl -fsSL https://dockmesh.example.com/install/agent.sh?token=<token> | sudo bash
```

Adding a host needs the `hosts.create` permission.

### What the installer does

The script served at `/install/agent.sh` is `scripts/install-agent.sh` with the token and URLs filled in. It:

- supports Linux with systemd only (it exits on macOS and other systems),
- installs Docker from `get.docker.com` if it's missing (skip this with `--skip-docker`),
- creates a locked `dockmesh-agent` system user in the `docker` group (use `--as-root` to run as root instead),
- installs the binary to `/var/lib/dockmesh/bin/dockmesh-agent` with a symlink in `/usr/local/bin`,
- writes `/etc/dockmesh-agent/agent.env` and a hardened `dockmesh-agent.service` unit, then starts it,
- removes certificates from an earlier enrollment in `/var/lib/dockmesh` so the new token takes effect.

Pass flags like this:

```bash
curl -fsSL "https://dockmesh.example.com/install/agent.sh?token=<token>" | sudo bash -s -- --as-root
```

<!-- update after: agents: pin the CA at enrollment and verify self-upgrades -->
The installer accepts `x86_64` and `aarch64` hosts, but it always downloads `dockmesh-agent-linux-amd64` from the server. On an arm64 host, replace `/var/lib/dockmesh/bin/dockmesh-agent` with the `linux-arm64` binary from the [release tarball](https://github.com/dockmesh/dockmesh/releases) and restart the service. The server serves agent binaries from `DOCKMESH_BINARY_DIR` (`install.sh` sets it).

The enrollment token stays in `/etc/dockmesh-agent/agent.env` after enrollment. It can't be used again, but you can delete the `DOCKMESH_TOKEN` line once the host is online.

### Agent environment variables

The agent reads these from its environment (the installer puts them in `agent.env`):

| Variable | Default | Purpose |
|---|---|---|
| `DOCKMESH_DATA_DIR` | `/var/lib/dockmesh` | Certificates, the persisted agent URL, stack mirror |
| `DOCKMESH_ENROLL_URL` | none | `<base>/api/v1/agents/enroll`, required on first start |
| `DOCKMESH_TOKEN` | none | Enrollment token, required on first start |
| `DOCKMESH_AGENT_URL` | URL saved at enrollment | `wss://` URL to dial; overrides the saved one |

`dockmesh-agent status` and `dockmesh-agent --version` are available on the host. Logs go to the journal: `journalctl -u dockmesh-agent -f`.

## Enrollment and certificates

<!-- update after: agents: pin the CA at enrollment and verify self-upgrades -->
Each host gets a single-use enrollment token. Tokens don't expire. Keep unused install commands private, or revoke the pending host if a command leaks. On enrollment the server generates the agent's key pair and a client certificate signed by its internal CA, and returns them together with the CA certificate. The agent doesn't verify the server's TLS certificate during this request. It trusts whatever CA the enroll endpoint returns, and pins that CA for all later connections. Only enroll over a network path you trust, and use an `https://` base URL.

<!-- update after: agents: survive disconnects, flow-control streams, renew certs -->
Agent certificates are valid for 365 days and are not renewed automatically. When a certificate expires, the agent can no longer connect. To bring it back, revoke the host and add it again (see below). Plan a re-enrollment before the year is up.

The CA (10 years) and the agent listener's certificate (5 years) live in the data directory as `agents-ca.crt`/`.key` and `agents-server.crt`/`.key`. Back them up with the rest of the data directory. Losing them means re-enrolling every agent.

<!-- update after: agents: survive disconnects, flow-control streams, renew certs -->
When the connection drops, the agent reconnects with a backoff from 1 to 60 seconds.

### Rotate the CA

`dockmesh ca rotate` creates a new CA and listener certificate, keeps the old files with an `.old` suffix, and sets every agent to pending with a fresh token. Every agent has to enroll again. Run it as the service user so the new key files keep the right owner, and restart the server so it loads the new CA:

```bash
sudo systemctl stop dockmesh
sudo -u dockmesh dockmesh ca rotate --reissue-all-agents --env-file <data-dir>/dockmesh.env
sudo systemctl start dockmesh
```

The command prints one install command per host. Run each on its host. The installer removes the old certificates before enrolling. `dockmesh ca export --out ca.pem` writes the current CA certificate. See [dockmesh CLI](../reference/dockmesh-cli.md).

## Host tags

Tags are lowercase labels (letters, digits and hyphens, up to 32 characters, at most 20 per host). Set them in the **Add host** dialog, with **Edit tags** in the host's row menu on **Hosts**, or on the **Tags** tab of the host detail page. Changing tags needs `hosts.tag`.

Tags are used for access scoping: an assignment on a host tag covers every host with that tag. See [Permissions](../concepts/permissions.md).

## Agent upgrades

When an agent runs a different version than the server, **Hosts** shows the target version next to it. Click **Upgrade agent** in the row menu, **Upgrade now** on the host's **Maintenance** tab, or select several hosts and use **Bulk upgrade**. The agent downloads `dockmesh-agent-linux-<arch>` from the server, replaces itself and reconnects. This needs `hosts.update`.

The upgrade policy decides whether the server does this on its own. It's evaluated every 60 seconds:

| Mode | Behaviour |
|---|---|
| `manual` (default) | Nothing happens until you click upgrade. |
| `auto` | Every connected agent that's behind is upgraded. |
| `staged` | Each evaluation upgrades `stage_percent` of the outdated agents (default 10 %, at least one). `stage_gap_sec` is stored but not applied; waves are spaced by the 60-second evaluation. |

There is no UI to change the mode. Use the API:

```bash
curl -X PUT https://dockmesh.example.com/api/v1/agents/upgrade-policy \
  -H "Authorization: Bearer dmt_…" -H "Content-Type: application/json" \
  -d '{"mode":"staged","stage_percent":25}'
```

<!-- update after: agents: pin the CA at enrollment and verify self-upgrades -->
The agent downloads the new binary from the server without checking a checksum or signature. Serve the server over HTTPS if you use `auto` or `staged`.

## Remove a host

Open the host's row menu and click **Revoke…**, or use **Revoke** on the **Maintenance** tab (needs `hosts.delete`). This deletes the host from Dockmesh, and its certificate is no longer accepted. Containers on that host keep running. Dockmesh just stops tracking them. Afterwards, on the host:

```bash
sudo systemctl disable --now dockmesh-agent
```

To bring the host back, add it again with **Add host** and run the new install command.

**Re-issue** on the **Maintenance** tab creates a new token for a host that hasn't finished enrolling. It doesn't help for an enrolled host. Remove and re-add that host instead.

## Limits

- Rolling updates, cleanup and container file operations are not supported on remote hosts.
- One-click image updates, vulnerability scans, alert rules and the metrics history cover the local host only.
- Agent requests time out after 30 seconds. That includes image pulls during a remote deploy, so pull large images on the host in advance.

See also [Migration and drain](./migration-and-drain.md) and [Troubleshooting](../troubleshooting/troubleshooting.md).
