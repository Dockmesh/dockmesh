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

The setup wizard fills both from the address you enter there. Saving a new agent URL adds its host name to the agent listener's certificate; names and IPs already in the certificate stay, so enrolled agents keep connecting. Changing the base URL also needs Users & roles: Manage, and changing the agent URL Hosts: Manage; every change of either field writes an audit entry with the old and the new value.

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

- supports Linux with systemd only (it exits on macOS and other systems) on `x86_64` and `aarch64`; tested distributions are listed in [Supported systems](../getting-started/supported-systems.md),
- installs Docker from `get.docker.com` if it's missing (with `--skip-docker` it stops instead),
- downloads `dockmesh-agent-linux-amd64` or `dockmesh-agent-linux-arm64` from the agent port with the same key pin, refuses it if its SHA-256 doesn't match, and installs it root-owned to `/usr/local/bin/dockmesh-agent`,
- creates a locked `dockmesh-agent` system user in the `docker` group; when the Docker socket belongs to another group, the service gets that group too (see [Agent user](#agent-user)),
- runs `dockmesh-agent system install`, which writes `/etc/dockmesh-agent/agent.env`, the hardened `dockmesh-agent.service` unit and the `dockmesh-agent-update.path` and `.service` units of the root updater from templates in the binary,
- writes the agent CA and the token to the data directory as the agent's account, starts the agent and waits up to 60 seconds for it to connect; when systemd cannot start the binary it says so, with the SELinux label and what to run.

The output uses colours only on a terminal; with `NO_COLOR`, `TERM=dumb` or when the output is not a terminal it is plain text with an ASCII box.

Flags: `--token <token>`, `--data-dir <path>` (first install only, default `/var/lib/dockmesh-agent`), `--stacks-dir <path>`, `--skip-docker` and `--switch-to-service-user` (see below).

### Running the installer again

Run the same command without `--token` to repair an agent: it keeps the certificate and the host's identity, installs the current binary and rewrites the config and units. An agent installed by an earlier release moves to the layout above: its binary in `<data dir>/bin/` and the copy script `/etc/dockmesh-agent/refresh-path-copy` are removed, and from then on it gets its updates through the root updater, checked against the release signature. On a host with an agent unit but no enrollment it stops with "This host is not enrolled"; run the command with a token there.

With `--token` the host enrolls again (for example with a token from the host's Maintenance › Re-issue). The previous certificate stays in place until the new one is written; when the server refuses the token the agent keeps its previous enrollment and the installer says why.

Without `--switch-to-service-user` a re-run never switches an agent that runs as root to the `dockmesh-agent` user, and it never deletes the certificates first. Installers of 0.5.x and earlier did both.

### Agent user

New agents always run as the `dockmesh-agent` user. `--as-root` was removed in 0.6.0: on a new install the installer stops with a message, and `dockmesh-agent system install` refuses `--user root` for a host without an agent unit. The user is in the `docker` group, and access to the Docker socket is equivalent to root on the host, so the agent's account still controls the host through Docker; what the separate account adds is that the agent process itself can't write system files, its config or its binary.

An agent installed with `--as-root` by an earlier release keeps running as root, also on a re-run, which prints a notice. To move it to the `dockmesh-agent` user, run the install command again with `--switch-to-service-user`. The installer creates the user and gives it the agent's files: the data directory with everything in it except `stacks/`, and in each stacks directory the directory itself, every stack directory and the files directly in it (`compose.yaml`, `.env`). What containers wrote below a stack directory, such as the data of relative bind mounts, keeps its owner. Links are never followed.

To switch an agent, pass the flag like this:

```bash
curl -fsSLk --pinnedpubkey '<pin>' <install-url> | sudo bash -s -- --switch-to-service-user
```

The installer and binary routes on the agent port need no sign-in. They and `/enroll` are limited per IP address with the limits of the sign-in endpoints: 100 requests in 15 minutes for the installer and binaries, and 100 failed enrollments in 15 minutes, after which the address waits 15 minutes. Behind TCP passthrough every agent host shares the proxy's address.

Commands created before this version used `<base-url>/install/agent.sh?token=…` on the web port. That route still works for automation in 0.6.x and goes away in 0.7.0; the script it serves downloads the binary from the agent port with the pin, but the script itself still travels over the web port. Each use writes a warning to the server log and counts against the same per-IP limit as the installer on the agent port, and admins get a notice at most once a day while the route is in use.

Re-running the installer keeps the data directory of an earlier install. Agents installed by older releases kept their files in `/var/lib/dockmesh`. On a host without a Dockmesh server the agent keeps using that directory. On a host that also runs the server, the installer moves the agent's own files out of the server's directory to `/var/lib/dockmesh-agent`. The stack copies in `stacks/`, with the data of relative bind mounts, are not moved, so those stacks start with empty bind directories at the next deploy: `/var/lib/dockmesh/stacks/` is the server's own stacks directory, so never move it; copy only the directories of the stacks deployed to this agent to `/var/lib/dockmesh-agent/stacks/`.

The server serves agent binaries from `DOCKMESH_BINARY_DIR` (`install.sh` sets it). Without a binary for the host's architecture the installer stops.

The installer writes the enrollment token to `<data dir>/enroll-token` (mode 0600, owned by the agent's account), not to `agent.env`. The agent deletes the file once the server has accepted or refused the token. When the file is present on a host that is already enrolled, the agent enrolls again with it and keeps its previous certificate until the new one is written; a refused token leaves the previous enrollment in place. A host with neither a certificate nor a token, or whose token was refused, stops the service instead of retrying every 10 seconds. Installs from earlier releases kept a spent `DOCKMESH_TOKEN` line in `agent.env`; it can't be used again, and the next installer run or update of a managed agent removes it.

### Agent environment variables

The agent reads these from its environment (the installer puts them in `agent.env`):

| Variable | Default | Purpose |
|---|---|---|
| `DOCKMESH_DATA_DIR` | `/var/lib/dockmesh-agent` (`/var/lib/dockmesh` when only that directory holds an enrollment) | Certificates, the saved agent URL, the stack mirror unless `DOCKMESH_STACKS_DIR` moves it |
| `DOCKMESH_STACKS_DIR` | `<data dir>/stacks` | Where the mirror of new stacks goes. A stack that is already in an earlier directory stays there |
| `DOCKMESH_EARLIER_STACKS_DIRS` | none | Colon-separated stacks directories used before; the agent keeps finding its stacks there. The unit keeps each writable while it holds stacks; at most two may still hold stacks. Written by root, not by hand |
| `DOCKMESH_ENROLL_URL` | none | Enrollment endpoint, `https://<agent host>:8443/enroll`. Required on first start |
| `DOCKMESH_TOKEN` | none | Enrollment token for a first start without `<data dir>/enroll-token` (container or manual starts). The installer never writes it |
| `DOCKMESH_CA_FINGERPRINT` | none | SHA-256 of the agent CA. Required to enroll over `https://` |
| `DOCKMESH_ENROLL_CA_FILE` | none | File with the agent CA certificate; must match the fingerprint |
| `DOCKMESH_AGENT_URL` | URL saved at enrollment | `wss://` URL to dial; overrides the saved one |
| `DOCKMESH_INSECURE_ENROLL` | unset | `true` allows enrollment over plain `http://`, like the `--insecure-enroll` flag |

The host's details page shows the stacks directories the agent reports, with the number of stacks in each (`stacks_dirs` in `GET /api/v1/agents`).

### Changing the stacks directory

On a managed agent the root updater switches the stacks directory (`stacks-dir-apply`). The agent checks the path first with the same rules and only asks root when they pass; root then checks everything again on its own, because the request comes from the agent's account.

Root accepts a directory in one of two cases:

- **New directory:** it does not exist yet, and its parent is `/srv`, `/opt`, `/data`, `/mnt` or `/media`, or a disk mounted somewhere below them (for example `/mnt/disk1` or `/srv/data`). Its name is `stacks` or starts with `dockmesh-stacks` (`/opt/stacks`, `/mnt/disk1/dockmesh-stacks-2`), and a mounted disk must not be another view of the root filesystem or of a protected one. Root creates exactly this one directory, owned by `dockmesh-agent` with mode 0750. `/opt/stacks` and `/mnt/disk1/stacks` work; `/opt/a/stacks` (unless `/opt/a` is a mounted disk), `/opt/apps`, `/home/x` or `/stacks` don't.
- **Prepared directory:** anywhere else, an admin creates the directory first. It must be empty, belong to the agent's account and have no symbolic link in its path; root then creates, chowns or chmods nothing and only writes the config and the unit:

  ```bash
  sudo install -d -o dockmesh-agent -g dockmesh-agent -m 0750 /srv/apps/stacks
  ```

An agent that runs as root owns every directory root made, so for it a prepared directory counts only where root could have created it itself (the new-directory rule above). A refusal names the case it failed and prints this command for the path. In both cases:

- the path is absolute and clean, of letters, digits and `. _ - /`; no component may be a symbolic link, and every directory above it belongs to root and only root may write it;
- it is not `/` and not in `/etc`, `/usr`, `/bin`, `/sbin`, `/lib*`, `/boot`, `/efi`, `/proc`, `/sys`, `/dev`, `/run`, `/root`, `/var` (except the agent's default `<data dir>/stacks`), `/tmp`, `/snap`, `/nix` or Docker's actual data directory, also not the same directory under another name;
- it is not inside the current or an earlier stacks directory, not on a bind mount of a subdirectory, not in the agent's data directory (except its default `stacks/`, only at install) and not in a Dockmesh server's data directory;
- it is on a local filesystem with Unix permissions, not on a network share or a FUSE filesystem, with at least 200 MiB free (a warning below 2 GiB).

The default `<data dir>/stacks` is accepted at install time; an agent already using another directory can't move back into its data directory, because from inside its sandbox that directory is a bind mount. Root writes `DOCKMESH_STACKS_DIR` to `agent.env`, rewrites the unit, reloads systemd and restarts the agent. It never changes the owner or mode of anything that exists and never deletes anything. Earlier stacks directories stay writable while they hold stacks; a switch is refused while two earlier ones still do. One switch runs at a time, and every switch and refusal is logged in `journalctl -u dockmesh-agent-update`. `sudo dockmesh-agent system install --stacks-dir <dir>` applies the same checks.

Root does not create directories deeper down, in other trees or under other names on purpose: services that run as root load code and config from many places below `/opt` and elsewhere (a Python environment's `site-packages`, plugin directories, `/opt/cni` once Kubernetes or Podman is installed), and a directory the agent may write in one of them would hand the agent's account root.

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

The command prints one install command per host. Run each on its host. The agent enrolls again with the new token and replaces its old certificate once the new one is written. `dockmesh ca export --out ca.pem` writes the current CA certificate. To add names or IPs to the listener certificate without touching the CA, use `dockmesh ca reissue-server --add-san <name-or-ip>`. See [dockmesh CLI](../reference/dockmesh-cli.md).

## Host tags

Tags are lowercase labels (letters, digits and hyphens, starting with a letter or digit, up to 32 characters, at most 20 per host). Set them in the **Add host** dialog, with **Edit tags** in the host's row menu on **Hosts**, or on the **Tags** tab of the host page. Changing tags needs `hosts.tag`.

Tags limit access (an assignment on a host tag covers every host with that tag, see [Permissions](../concepts/permissions.md)), registry credentials and alert rules to the hosts that carry them.

## Docker Swarm clusters

When a host is a manager of a Docker Swarm, the **Hosts** page shows "Docker Swarm detected" to users who may create clusters (`clusters.create`, granted only on everything). **Create cluster** makes the Swarm a Dockmesh cluster; Dockmesh never creates one on its own, and nothing changes on the Swarm itself.

When several hosts manage the Swarm, pick the one to create it from. That host becomes the cluster's origin, and Dockmesh refuses to create the cluster if another manager host doesn't list it as a manager.

The cluster's id is the Swarm's cluster ID. A host joins the cluster as a node only when the origin's node list contains the node ID that host reports (while the origin can't be read, a verified manager's list may add members but never removes or changes one), so a host can't add itself by claiming a cluster. Its role (manager or worker) comes from that node list. Dockmesh refreshes the links every minute: a host that left the Swarm or was revoked or removed in Dockmesh is unlinked, while a host it can't reach keeps its link. While no manager can be read, the links stay as they were. Links and unlinks appear in the audit log. Nodes without a Dockmesh agent are not hosts and are not linked. Listing the nodes needs an agent of this version on the origin.

### Node identity

A node list shows which nodes are in the Swarm, but a host's claim to be one of them is only its own word. So every agent of this version also **proves** it is the node it claims: Dockmesh sends a fresh random challenge, and the agent signs it with the node's Swarm key. The agent reads the key from `<Docker root>/swarm/certificates/` through a short-lived helper container from the pinned busybox image, which is created, copied from and removed without ever running. The key never leaves the host and is not written anywhere; the agent holds it only for the signature and clears the bytes it read. Dockmesh accepts the proof when the node certificate chains to the cluster's root CA, names the claimed node, the Swarm and the role, is valid, and its key made the signature. Proofs are repeated when an agent reconnects, before the certificate expires, when the Swarm reports a new certificate issuer, and once a day.

The cluster's root CA is pinned from the origin when the cluster is created, once at least one other manager host reports the same root; any manager host that reports a different one, or that says it manages the Swarm while the origin's node list leaves it out, stops the pin. A Swarm with a single manager, where no other host claims to manage it, pins the origin's root on its own; the **Nodes** tab shows the pinned fingerprint to compare with the manager. Clusters created before this version, or while no second manager agent could confirm the root, pin it at a later check. After `docker swarm ca --rotate` the managers report a new root. Dockmesh accepts it once two verified managers report it; in a Swarm with a single manager, a user with `clusters.create` confirms it on the cluster page (a notice says so), after comparing the fingerprint shown there with `openssl x509 -noout -fingerprint -sha256 -in /var/lib/docker/swarm/certificates/swarm-root-ca.crt` on the manager. Until then, proofs are checked against the old root. A proof counts for a day; a host that stops answering loses its verification after that.

The **Nodes** table marks each node with an agent as verified or not, with the reason in the tooltip: an agent too old to prove, no root CA pinned yet, a Swarm with autolock (the key on disk is encrypted and can't sign), or a refused proof. An unverified host stays linked, and an unverified origin keeps serving the read-only view ("origin not verified"). While no manager of a cluster is verified, users with `clusters.create` get a notice. When an agent connects from another address than the one the Swarm lists for its node, the table shows a warning to users who can view that host; NAT and proxies cause this too, so it never unlinks anything.

**Delete…** in the menu of the cluster's row on the Hosts page (also `clusters.create`; API `DELETE /api/v1/clusters/{id}`) removes a cluster, for example one created from the wrong host; the Swarm itself is not touched. Access on the cluster goes with it: role assignments and IdP group mappings on the cluster are deleted, API tokens restricted to it are revoked, and unused invites lose the cluster (an invite with nothing else left expires). The confirmation lists each of them by name before you type the cluster's name, and the audit entry records the counts.

On the Hosts page a cluster is one expandable row: its state, how many managers and workers it has, how many nodes have no Dockmesh agent, and the leader or, when no manager can be read, when its state was last known. The linked hosts are listed indented below it with their role; a node without an agent is not a host and doesn't appear there. The state comes from the view of the manager it is read through, checked every minute:

- **Healthy**: every node is ready and every manager reachable.
- **Degraded**: a node is down or a manager unreachable, but the Swarm still has quorum.
- **No quorum**: a manager is online but its Swarm has no leader, so Swarm can't change anything.
- **Unreachable**: no manager's agent is online and able to list the nodes; the row shows the last known state with its time.

**Which manager Dockmesh reads through.** Dockmesh reads a cluster through one manager host at a time, shown as "read through X". It uses only manager hosts that proved their node identity (see below), and the server's own host when it is a manager. While the current one answers, Dockmesh stays with it. When it goes away, reads switch to the leader if its host has a verified agent, otherwise to the origin, otherwise to the next verified manager, and stay on the new one when the old one comes back. With fewer than two verified managers there is no failover, and the page says that failover needs agents on at least 2 managers. While no manager is verified, for example right after an upgrade, the cluster is read through the origin alone; once managers are verified, an origin that fails its proof is no longer used. While a changed root CA waits to be accepted, reads stay on the origin.

The last state is kept across a server restart: until the first check after the start, the row shows the stored state with the time it was checked, and an unreachable cluster keeps its last known state and time. A cluster that has never been checked reads "Not checked yet". The cluster's state and shape are shown to users with Hosts: View on the cluster or on everything. A user who can only see a node host sees the cluster's name on that row and "Member of cluster X" on the host page, without a link and without anything else about the cluster. Hosts that are not in a Swarm behave exactly as before.

While a manager answers, the row also counts the running tasks and says whether the load is spread evenly or which node runs more than its share.

### Cluster page

The cluster's name on the Hosts page, and on a node's host page, opens the cluster page for users with Hosts: View on the cluster or on everything. It is read-only and shows:

- the state, the cluster ID and the host it is read through, with a notice for **Degraded** (which nodes are gone, how many more manager losses the Swarm survives), **No quorum** (what Swarm can't do until enough managers are back) and **Unreachable** (the last known node list and when);
- key figures: nodes, managers reachable and the quorum they need, the leader, services and running tasks against the desired count;
- **Workload distribution**: per node the running tasks, CPU and memory, with the fair share of tasks across the active nodes as a dashed line (**By node**), or a matrix of services × nodes with the totals (**By service**). A node is marked heavy when it runs at least 1.5 times and at least 3 tasks more than the fair share; drained, paused and unreachable nodes don't count. The hint names tasks a placement constraint (`node.hostname==` or `node.id==`) pins there; Swarm never moves running tasks, `docker service update --force` spreads a service again. CPU and memory bars turn yellow from 65 %. Nodes without an agent show their tasks but no CPU or memory, and CPU and memory of a node host need Hosts: View on that host as well, since a cluster role covers the Swarm but not the hosts that are its nodes;
- **Nodes**: role, state (Ready, Down), availability (Active, Pause, Drain), manager status, tasks, engine version, the linked host and the address;
- **Services**, grouped by stack: running against desired replicas, the nodes their tasks run on, and CPU (cores) and memory summed from the task containers on nodes with an agent, marked with `*` when some tasks run where no agent measures them;
- **Tasks**: state, desired state, error and the failures of the task's slot in the last hour; failed and repeatedly failing tasks are marked, **Problems only** lists just those, and the overview links to them;
- **Stacks** deployed with `docker stack deploy`, marked External, and the overlay **Networks** with the services attached to them.

Services, tasks and stacks need Stacks: View on the cluster, networks Networks: View. The page refreshes every 15 seconds. When no manager can be read it shows the last node list Dockmesh knows, dimmed, and keeps the lists it read earlier on the open page.

### Reading a cluster through the API

The cluster's nodes, services, tasks and overlay networks are read through the manager chosen above. The API offers them read-only:

- `GET /api/v1/clusters/{id}/nodes` needs Hosts: View on the cluster; it lists every node, including those without an agent.
- `GET /api/v1/clusters/{id}/distribution` needs Hosts: View on the cluster. It returns the running tasks per node, the fair share, which nodes are heavy, and the CPU and memory of each node host the caller may view; with Stacks: View on the cluster also the tasks of every service per node with the CPU and memory of its task containers. `?usage=false` leaves out CPU, memory and the services. When no manager can be read it answers with the last known node list, `stale` and `known_at`.
- `GET /api/v1/clusters/{id}/services`, `…/services/{service}` and `…/tasks` (optionally `?service=`) need Stacks: View on the cluster.
- `GET /api/v1/clusters/{id}/networks` needs Networks: View on the cluster. With Stacks: View on the cluster as well, each network lists the services attached to it.
- `GET /api/v1/networks/topology?cluster={id}` is the cluster graph of the **Topology** page and needs Networks: View and Stacks: View on the cluster.
- `GET /api/v1/clusters/{id}/volumes` needs Volumes: View and Hosts: View on the cluster. It lists the volumes of every Swarm stack (label `com.docker.stack.namespace`) per stack, with the linked hosts that hold a copy. Swarm creates a stack's `local` volumes on each node a task ran on, so one volume usually appears on several hosts. Only hosts with an agent are read; a host that doesn't answer is listed as unreachable, and volumes of a Compose project are left out even when they carry the label.

A host or host tag assignment on one of the nodes does not reach these. A service shows the names of its environment variables but never their values, and its secrets and configs by name only; their data never leaves the manager. Lists are capped (1000 services, 5000 tasks, 500 networks) and say so when they are cut.

Only a Swarm manager answers: an agent on a worker refuses, and while no verified manager (or the origin) is online and still a manager the API answers 503. Reading services, tasks and networks needs an agent of this version on the origin.

### Service logs

`GET /api/v1/ws/clusters/{id}/services/{service}/logs` is a WebSocket that streams the logs of every task of a service, read through the chosen manager, so tasks on nodes without an agent are included. Get a ticket with `POST /api/v1/ws/ticket?for=containers.logs` and pass it as `?ticket=`; the caller needs `containers.logs` (Containers: View or Stacks: View) on the cluster. Each message is one JSON line with the time, the task (`web.2`, or `web.<node id>` for a global service), the node's host name, `stdout` or `stderr` and the message. Messages longer than 16 KiB are cut and marked `truncated`.

- `tail`: lines per task to send first, 0 to 5000 (default 100).
- `since`: only lines from then on, as a duration back from now (`15m`, at most 90 days), an RFC 3339 time or unix seconds.
- `follow=0`: end the stream after the existing lines instead of following.

Closing the socket stops the stream on the manager. Of the log metadata only the task and node are passed on; attributes a log driver adds from labels or environment variables stay on the manager. At most 32 service log streams are open at once and 4 per user or API token; more answer 429. The origin's agent has to run this version, otherwise the request is refused with 503.

## Agent upgrades

When an agent runs a different build than the server, **Hosts** marks its version with an arrow. To upgrade it, use **Upgrade agent** in the row menu, **Upgrade to …** in the header of the host page or **Upgrade now** on its **Maintenance** tab, or select several hosts and click **Upgrade**. This needs `hosts.update`. The agent downloads `dockmesh-agent-linux-<arch>` from the agent listener over HTTPS with its client certificate, checks it against the SHA-256 the server sends, replaces itself and reconnects.

Agents that speak an older protocol than the server show **agent update recommended** on the host page.

The upgrade policy decides whether the server upgrades agents on its own. It's evaluated every 60 seconds:

| Mode | Behaviour |
|---|---|
| `manual` (default) | Nothing happens until you start an upgrade. |
| `auto` | Every connected agent that's behind is upgraded. |
| `staged` | Each evaluation upgrades `stage_percent` of the outdated agents (default 10 %, at least one). The next wave starts once `stage_gap_sec` (default 300) has passed since the previous one; evaluations run every 60 seconds, so a shorter gap acts as 60 seconds. |

In `auto` and `staged` mode, the server stops trying an agent after three attempts with the same binary that didn't leave it up to date, at least 5 minutes apart. The upgrade buttons always try.

While agents are behind, the Hosts page shows how many, the mode and the last evaluation, and **Run policy now** outside `manual` mode. There is no UI to change the mode. Use the API; changing the mode and **Run policy now** need Hosts: Manage on everything, a grant on some hosts or tags isn't enough:

```bash
curl -X PUT https://dockmesh.example.com/api/v1/agents/upgrade-policy \
  -H "Authorization: Bearer dmt_…" -H "Content-Type: application/json" \
  -d '{"mode":"staged","stage_percent":25}'
```

See also [Upgrade](../operations/upgrade.md#upgrading-agents).

## Remove a host

Open the host's row menu and click **Revoke…**, or use **Revoke…** on the **Maintenance** tab (needs `hosts.delete`), and type the host's name to confirm. This deletes the host from Dockmesh, closes its connection and stops accepting its certificate. Containers on that host keep running; Dockmesh stops tracking them. Afterwards, remove the agent on the host:

```bash
sudo dockmesh-agent uninstall
```

Until you do, the agent logs `This host was removed from Dockmesh. Run: sudo dockmesh-agent uninstall` once. After ten minutes of refusals it tries to connect only once an hour, without logging each attempt.

To bring the host back, add it again with **Add host** and run the new install command. It gets a new ID. To keep the ID, re-issue the token instead of revoking.

## Limits

- Rolling updates and stack cleanup are not supported on remote hosts.
- Image update detection covers the local host only.
- Live deploy progress isn't reported for remote hosts.

See also [Migration and drain](./migration-and-drain.md) and [Troubleshooting](../troubleshooting/troubleshooting.md).
