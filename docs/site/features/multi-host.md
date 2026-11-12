---
title: Multi-host
description: Add remote Docker hosts with the outbound mTLS agent, manage their tags, upgrades and certificates, and remove them again.
---

Dockmesh manages the local host directly through the Docker socket. Every other host runs `dockmesh-agent`, which dials out to the server over a mutually authenticated TLS WebSocket. The remote host needs no open inbound port; it only has to reach the server. See [Hosts and agents](../concepts/hosts-and-agents.md) for the model behind this.

## Before you add the first agent

Install commands and the agent's connection use two addresses of the server. Set them under **Settings → General** and click **Save**:

| Field | Example | Used for |
|---|---|---|
| **Base URL** | `https://dockmesh.example.com` | The web UI address: invite links, sign-in callbacks and links in notifications. Install commands created before this version download the script from it. |
| **Agent public URL** | `wss://dockmesh.example.com:8443/connect` | Enrollment, the permanent connection, certificate renewal and agent upgrades. Empty means `wss://<host of the base URL>:<agent port>/connect`. |

The setup wizard fills both from the address you enter there. Saving a new agent URL adds its host name to the agent listener's certificate; names and IPs already in the certificate stay, so enrolled agents keep connecting.

The server's environment file (`dockmesh.env`) can set the same values. These variables win over the fields; change them there and restart the server:

| Variable | Default | Purpose |
|---|---|---|
| `DOCKMESH_BASE_URL` | none (Settings) | Base URL. Without it, the **Base URL** field is used; with neither, `http://localhost:8080`. |
| `DOCKMESH_AGENT_PUBLIC_URL` | none (Settings) | `wss://` URL agents dial. Without it, the **Agent public URL** field is used, or the URL derived from the base URL. |
| `DOCKMESH_AGENT_LISTEN` | `:8443` | Address of the mTLS agent listener on the server. |
| `DOCKMESH_AGENT_SANS` | empty | Extra host names and IPs for the agent listener's certificate, comma-separated. The agent URL's host is added automatically. |
| `DOCKMESH_AGENT_ENROLL_TOKEN_TTL` | `1h` | How long an enrollment token is valid, as a Go duration between `1m` and `720h`. The server refuses to start with any other value. |

The listener certificate always covers `localhost`, `127.0.0.1` and `::1`. Port 8443 (or whatever you chose) has to be open on the server, not on the agent host. See [Ports](../reference/ports.md).

### Check the agent URL

**Check connection** under the **Agent public URL** field, and the same check that runs when you open the **Add host** dialog (above **Generate install command**), test the saved agent URL from the server: that it isn't `localhost` or a loopback address, that the listener certificate covers its host, that the name resolves, that the port accepts connections, and that the Dockmesh listener answers with its own certificate. A failed TLS check usually means a proxy terminates TLS in front of the port (a reverse proxy, or Cloudflare with proxying on). Agents authenticate with client certificates, so the connection must reach Dockmesh unchanged: use a DNS-only record, TCP passthrough or an address that bypasses the proxy.

The check runs from the server. With split DNS or NAT, remote hosts can still get a different answer.

## Add a host

1. Open **Hosts** and click **Add host**.
2. Enter a **Hostname** (the name shown in Dockmesh) and, optionally, tags.
3. Click **Generate install command**. The dialog shows a one-line command and the enrollment token, together with the time the token expires. **New command** replaces the token; the previous command stops working.
4. Run the command on the remote host as root.
5. The dialog checks every 2 seconds and closes once the host is online.

The command looks like this:

```bash
curl -fsSLk --pinnedpubkey 'sha256//<pin>' https://dockmesh.example.com:8443/install/agent.sh | sudo bash -s -- --token <token>
```

The command fetches the installer from the agent port, at the host and port of the agent URL, and pins the public key of the agent listener's certificate. `-k` only skips the certificate chain check; curl still refuses any server whose key doesn't match the pin and stops with `curl: (90)` before the script runs. The script downloads the agent binary from the same address with the same pin, so the install doesn't depend on how the web UI is exposed. The listener keeps its key when its certificate is re-issued for a new agent URL, so a command stays valid until its token is used or expires.

The token is an argument of the installer, not part of a URL, so it doesn't end up in proxy or access logs. While the installer runs it is visible in the process list and the shell history of the agent host; it can be used once.

Adding a host needs the `hosts.create` permission.

### What the installer does

The script served at `/install/agent.sh` on the agent port is `scripts/install-agent.sh` with the URLs, the listener's key pin, the agent CA and the agent binary checksums filled in. It holds no secret; the token comes from `--token`. It:

- supports Linux with systemd only (it exits on macOS and other systems) on `x86_64` and `aarch64`,
- installs Docker from `get.docker.com` if it's missing (skip this with `--skip-docker`),
- creates a locked `dockmesh-agent` system user in the `docker` group, which is equivalent to root on the host (use `--as-root` to run as root instead),
- downloads `dockmesh-agent-linux-amd64` or `dockmesh-agent-linux-arm64` from the agent port with the same key pin and refuses to install it if its SHA-256 doesn't match,
- installs the binary to `/var/lib/dockmesh-agent/bin/dockmesh-agent` and a root-owned copy to `/usr/local/bin/dockmesh-agent`, which the service refreshes at every start,
- writes the agent CA to `/var/lib/dockmesh-agent/enroll-ca.pem`, `/etc/dockmesh-agent/agent.env` and a hardened `dockmesh-agent.service` unit, then starts it,
- removes certificates from an earlier enrollment so the new token takes effect,
- waits up to 60 seconds for the agent to connect and prints the last log lines if it doesn't.

Pass flags like this:

```bash
curl -fsSLk --pinnedpubkey '<pin>' <install-url> | sudo bash -s -- --token <token> --as-root
```

The installer and binary routes on the agent port need no sign-in. They and `/enroll` are limited per IP address with the limits of the sign-in endpoints: 100 requests in 15 minutes for the installer and binaries, and 100 failed enrollments in 15 minutes, after which the address waits 15 minutes. Behind TCP passthrough every agent host shares the proxy's address.

Commands created before this version used `<base-url>/install/agent.sh?token=…` on the web port. That route still works for automation in 0.6.x and goes away in 0.7.0; the script it serves downloads the binary from the agent port with the pin, but the script itself still travels over the web port. Each use writes a warning to the server log, and admins get a notice at most once a day while the route is in use.

Re-running the installer keeps the data directory of an earlier install. Agents installed by older releases kept their files in `/var/lib/dockmesh`; the installer moves the agent's own files out of a directory that belongs to a Dockmesh server.

The server serves agent binaries from `DOCKMESH_BINARY_DIR` (`install.sh` sets it). Without a binary for the host's architecture the installer stops.

The enrollment token stays in `/etc/dockmesh-agent/agent.env` after enrollment. It can't be used again, but you can delete the `DOCKMESH_TOKEN` line once the host is online.

### Agent environment variables

The agent reads these from its environment (the installer puts them in `agent.env`):

| Variable | Default | Purpose |
|---|---|---|
| `DOCKMESH_DATA_DIR` | `/var/lib/dockmesh` (the installer writes `/var/lib/dockmesh-agent`) | Certificates, the saved agent URL, the stack mirror |
| `DOCKMESH_ENROLL_URL` | none | Enrollment endpoint, `https://<agent host>:8443/enroll`. Required on first start |
| `DOCKMESH_TOKEN` | none | Enrollment token. Required on first start |
| `DOCKMESH_CA_FINGERPRINT` | none | SHA-256 of the agent CA. Required to enroll over `https://` |
| `DOCKMESH_ENROLL_CA_FILE` | none | File with the agent CA certificate; must match the fingerprint |
| `DOCKMESH_AGENT_URL` | URL saved at enrollment | `wss://` URL to dial; overrides the saved one |
| `DOCKMESH_INSECURE_ENROLL` | unset | `true` allows enrollment over plain `http://`, like the `--insecure-enroll` flag |

The agent refuses to enroll over plain `http://` unless you start it with `--insecure-enroll` (or `DOCKMESH_INSECURE_ENROLL=true`), because the token and the client key would cross the network unencrypted. Use it only in an isolated lab.

`dockmesh-agent status` and `dockmesh-agent --version` are available on the host. Logs go to the journal: `journalctl -u dockmesh-agent -f`.

## Enrollment and certificates

Each host gets a single-use enrollment token, valid for 1 hour unless `DOCKMESH_AGENT_ENROLL_TOKEN_TTL` says otherwise. The agent enrolls on the server's agent port and trusts only the agent CA pinned in the install script, so it can't be redirected to another server. The server generates the agent's key pair and a client certificate signed by its CA, and the agent pins that CA for all later connections.

Agent certificates are valid for 365 days. An online agent renews its certificate over its connection once less than 30 days are left; the host keeps its ID. The host's **Overview** tab shows when the certificate expires. Admins get a notice for an agent whose certificate expires within 14 days, and a critical one for an offline agent whose certificate has expired. To bring such an agent back, re-issue its token (see below) and run the new install command on the host.

The CA (10 years) and the agent listener's certificate (5 years) live in the data directory as `agents-ca.crt`/`.key` and `agents-server.crt`/`.key`. Back them up with the rest of the data directory. Losing them means re-enrolling every agent.

### Re-issue a token

**Re-issue…** on the host's **Maintenance** tab creates a new enrollment token for an existing host (needs `hosts.create`). Running the new install command on the host re-enrolls it under the same ID, so its stacks, tags and backup jobs stay. The old certificate works until the host enrolls with the new token. Use this for an expired or suspect certificate, or after reinstalling the host.

### Rotate the CA

`dockmesh ca rotate` creates a new CA and listener certificate, keeps the old files with an `.old` suffix, and sets every agent to pending with a fresh token. Every agent has to enroll again. Run it as the service user so the new key files keep the right owner, and restart the server so it loads the new CA:

```bash
sudo systemctl stop dockmesh
sudo -u dockmesh dockmesh ca rotate --reissue-all-agents --env-file <data-dir>/dockmesh.env
sudo systemctl start dockmesh
```

The command prints one install command per host. Run each on its host. The installer removes the old certificates before enrolling. `dockmesh ca export --out ca.pem` writes the current CA certificate. To add names or IPs to the listener certificate without touching the CA, use `dockmesh ca reissue-server --add-san <name-or-ip>`. See [dockmesh CLI](../reference/dockmesh-cli.md).

## Host tags

Tags are lowercase labels (letters, digits and hyphens, starting with a letter or digit, up to 32 characters, at most 20 per host). Set them in the **Add host** dialog, with **Edit tags** in the host's row menu on **Hosts**, or on the **Tags** tab of the host page. Changing tags needs `hosts.tag`.

Tags limit access (an assignment on a host tag covers every host with that tag, see [Permissions](../concepts/permissions.md)), registry credentials and alert rules to the hosts that carry them.

## Agent upgrades

When an agent runs a different build than the server, **Hosts** marks its version with an arrow. To upgrade it, use **Upgrade agent** in the row menu, **Upgrade to …** in the header of the host page or **Upgrade now** on its **Maintenance** tab, or select several hosts and click **Upgrade**. This needs `hosts.update`. The agent downloads `dockmesh-agent-linux-<arch>` from the agent listener over HTTPS with its client certificate, checks it against the SHA-256 the server sends, replaces itself and reconnects.

Agents that speak an older protocol than the server show **agent update recommended** on the host page.

The upgrade policy decides whether the server upgrades agents on its own. It's evaluated every 60 seconds:

| Mode | Behaviour |
|---|---|
| `manual` (default) | Nothing happens until you start an upgrade. |
| `auto` | Every connected agent that's behind is upgraded. |
| `staged` | Each evaluation upgrades `stage_percent` of the outdated agents (default 10 %, at least one). `stage_gap_sec` is stored but not applied; waves are spaced by the 60-second evaluation. |

In `auto` and `staged` mode, the server stops trying an agent after three attempts with the same binary that didn't leave it up to date, at least 5 minutes apart. The upgrade buttons always try.

While agents are behind, the Hosts page shows how many, the mode and the last evaluation, and **Run policy now** outside `manual` mode. There is no UI to change the mode. Use the API; changing the mode and **Run policy now** need Hosts: Manage on everything, a grant on some hosts or tags isn't enough:

```bash
curl -X PUT https://dockmesh.example.com/api/v1/agents/upgrade-policy \
  -H "Authorization: Bearer dmt_…" -H "Content-Type: application/json" \
  -d '{"mode":"staged","stage_percent":25}'
```

See also [Upgrade](../operations/upgrade.md#upgrading-agents).

## Remove a host

Open the host's row menu and click **Revoke…**, or use **Revoke…** on the **Maintenance** tab (needs `hosts.delete`), and type the host's name to confirm. This deletes the host from Dockmesh, closes its connection and stops accepting its certificate. Containers on that host keep running; Dockmesh stops tracking them. Afterwards, on the host:

```bash
sudo systemctl disable --now dockmesh-agent
```

To bring the host back, add it again with **Add host** and run the new install command. It gets a new ID. To keep the ID, re-issue the token instead of revoking.

## Limits

- Rolling updates and stack cleanup are not supported on remote hosts.
- Image pulls, volume and network create, remove and prune run on the local host only.
- Vulnerability scans, image update detection and auto-scaling cover the local host only.
- Live deploy progress isn't reported for remote hosts.

See also [Migration and drain](./migration-and-drain.md) and [Troubleshooting](../troubleshooting/troubleshooting.md).
