---
title: Troubleshooting
description: Symptoms, causes and fixes for the problems Dockmesh admins run into most often.
---

Each entry starts with the symptom as you see it, an error message or a behaviour, followed by the cause and the fix. Most fixes start with a log, so read the first section even if your problem is somewhere else.

## Reading the logs

| Install | Server log | Agent log |
|---|---|---|
| Linux, `install.sh` or `dockmesh init` (systemd unit `dockmesh.service`) | `journalctl -u dockmesh` | `journalctl -u dockmesh-agent` on the agent host |
| macOS (launchd `dev.dockmesh.service`) | `/usr/local/var/log/dockmesh.log` and `/usr/local/var/log/dockmesh.err` | not applicable |

```bash
sudo systemctl status dockmesh
sudo journalctl -u dockmesh --since '10 min ago'
sudo journalctl -u dockmesh-agent -n 100     # on the agent host
docker logs dockmesh-proxy                   # the reverse proxy (Caddy)
```

The server logs JSON at `info` level. For more detail, set these in the env file (`/var/lib/dockmesh/dockmesh.env` on a default Linux install) and restart:

```bash
DOCKMESH_LOG_LEVEL=debug   # debug | info | warn | error
DOCKMESH_LOG_FORMAT=text   # text, anything else means json
```

Long-running work (backup runs, restores, image updates, new proxy sites) also has its own log: open it from the running-operations indicator in the top bar. A failed operation names the step it failed in and shows its last log lines.

### `dockmesh doctor`

```bash
sudo -u dockmesh dockmesh doctor
```

It checks that the configuration loads, that the database opens and answers, that the data directory exists (warning only), that the Docker daemon answers (warning only), that the agent CA and server certificate load, and that the data directory is writable. It exits non-zero if a check fails.

Like the other `dockmesh` subcommands, `doctor` loads the first env file it finds among `/var/lib/dockmesh/dockmesh.env`, `/data/dockmesh.env` and `/usr/local/var/dockmesh/dockmesh.env`, unless `DOCKMESH_DB_PATH` is already set in your shell. For any other location pass it explicitly: `sudo -u dockmesh dockmesh doctor --env-file /srv/dockmesh/dockmesh.env`. Run it as the service user so files it creates keep the right owner.

## Installation and sign-in

### The UI doesn't load or the service won't start

**Cause:** look for one of these lines in `journalctl -u dockmesh`:

| Log message | Meaning |
|---|---|
| `http listening` with `addr` | The UI is up on that address (default `:8080`, `DOCKMESH_HTTP_ADDR`). If the browser still can't reach it, check the firewall. |
| `http server error` | The listener failed, usually because another process holds the port. |
| `db open failed` / `db migrate failed` | The database path is wrong, not writable by the service user, or a migration failed (see [Upgrades](#upgrades)). |
| `docker daemon not reachable yet` | Dockmesh runs, but container endpoints return 503 until Docker answers. |
| `server starting in setup-mode` | The install wizard owns the UI. Open `/setup`. |

**Fix:** for a port conflict, find the other process and either stop it or set `DOCKMESH_HTTP_ADDR=:<port>` in the env file. For Docker, check the daemon and that the service user is in the `docker` group.

```bash
sudo ss -ltnp | grep ':8080'
sudo systemctl status docker
id dockmesh
```

See [Ports](../reference/ports.md).

### "setup window expired — restart the dockmesh service to start a fresh window"

**Cause:** the setup wizard accepts its final step for 30 minutes after the server starts.

**Fix:** restart and finish the wizard within 30 minutes.

```bash
sudo systemctl restart dockmesh
```

### The sign-in page appears instead of the setup wizard

**Cause:** the wizard at `/setup` is only active while the database has no admin account. `install.sh` sets `DOCKMESH_SETUP_FORCE=true` so the server waits for the wizard. Without that flag (for example after `dockmesh init`), the first start creates a user named `admin` with a random password and prints it once to the console. The log shows `bootstrap admin created, the password was printed to the console once`.

**Fix:** find the password in the first lines of the server log, or set a new one (see the next entry).

```bash
sudo journalctl -u dockmesh | grep -A3 'Bootstrap admin created'
```

If an admin already exists and `DOCKMESH_SETUP_FORCE=true` is still in the env file, the wizard stays closed and admins get the notice **Setup flag ignored**. Delete the line from the env file and restart. The wizard removes the flag itself after a successful run; if it couldn't, it reports `could not scrub setup flag`.

### Forgot the admin password

**Cause:** passwords are stored as argon2id hashes and can't be recovered.

**Fix:** set a new one on the server. The **Forgot password?** link on the sign-in page shows the command too, as `sudo dockmesh admin reset-password --user <username>`. Run it as the service user as below, so the database files keep their owner.

```bash
sudo -u dockmesh dockmesh admin reset-password --user admin
# prompts for the new password; --password '<new>' also works but ends up in shell history
```

`--user` takes a username or user ID. `reset-password` signs out every session of that user but doesn't clear a lockout; run `admin unlock` as well if the account is locked. `sudo -u dockmesh dockmesh admin list-users` lists all accounts with role and 2FA state.

### "This account is locked. Try again in 15 minutes."

**Cause:** five failed sign-ins in a row lock the account for 15 minutes (the API answers `account temporarily locked — try again in N minutes`). Both values are set on **Settings → Authentication** (**Lockout · max attempts**, **Lockout · duration (min)**); 0 turns the lockout off. A browser autofilling an old password is the usual reason it keeps happening.

**Fix:** wait, or clear the lock on the server, then remove the saved password for the Dockmesh URL from the browser.

```bash
sudo -u dockmesh dockmesh admin unlock --user admin
```

### "Too many attempts. Try again in N minutes."

**Cause:** 100 failed sign-ins within 15 minutes from one IP address block that address for 15 minutes, for every account. Behind a reverse proxy all users can share one address unless the proxy is listed under **Settings → General → Trusted proxies**.

**Fix:** wait for the block to end. If users behind a proxy hit it together, add the proxy's address to **Trusted proxies** so Dockmesh sees the real client addresses. See [Hardening](../operations/hardening.md).

### "You were signed out after a security update."

**Cause:** the session was created before an upgrade that changed the token format, so it can't be renewed (the API answers with code `session_outdated`).

**Fix:** sign in again. Nothing else is needed.

### "Your session expired. Sign in again to continue."

**Cause:** the refresh token expired or was revoked, for example by an admin ending your sessions (**Sign out everywhere** on the user's page under **Users & roles**), a password reset, or the idle timeout on **Settings → Authentication**. Open tabs share one session renewal, so several tabs don't sign each other out.

**Fix:** sign in again.

## Deploy fails

Deploy errors come back as the response message and show up in the stack page. Dockmesh pulls every missing image before it touches any container, so a failed pull leaves the running deployment as it was. An error after that point stops the deploy at the failing service: services before it already run the new containers, services after it keep the old ones, and nothing is rolled back automatically. Use the stack's **History** tab to go back to an earlier deploy. See [Stacks](../features/stacks.md).

### "Stack files changed outside Dockmesh"

**Cause:** `compose.yaml`, `.env`, `.env.age`, `.dockmesh.meta.json` or a `compose.<env>.yaml` override file on disk differ from what Dockmesh last wrote, for example after editing them over SSH or a `git pull` in the stack directory. The deploy is refused with HTTP 409 and code `modified_outside` (`stack files changed outside Dockmesh; review the files and confirm the deploy`). Migrations, scaling, rolling updates and image updates of the stack's containers are refused the same way, and the auto-scaler pauses the stack.

**Fix:** open the files and check the changes. If they are intended, click **Deploy anyway** in the dialog; that records the files as reviewed. API clients send the request again with `?confirm_external=1`.

### "port already in use on the target host — pick a different host port (see compose.yaml ports:) or stop whatever is bound to it"

**Cause:** another container or process on the target host already holds a host port from `ports:`. Dockmesh doesn't check ports before deploying; Docker refuses the container (HTTP 422).

**Fix:** change the host side of the port mapping, or stop whatever holds it.

```bash
sudo ss -ltnp | grep ':8080'
docker ps --filter publish=8080
```

A `bind: permission denied` error (also 422) means the port is below 1024 and the Docker daemon may not bind it, which happens with rootless Docker.

### "could not pull image — check the tag exists and, for private registries, that credentials are configured: ..."

**Cause:** the registry answered `manifest unknown` or `pull access denied`: the tag doesn't exist, the image name is misspelled, or the image is private and no credentials matched.

**Fix:** check the image and tag. For a private image, add the registry under **Settings → Registries**. Credentials with scope tags only apply to hosts that carry one of those tags.

### "registry authentication failed — configure credentials under Settings → Registries: ..."

**Cause:** the registry answered `unauthorized`: credentials are missing, wrong or expired.

**Fix:** add or update the registry under **Settings → Registries** and deploy again. On agent hosts, the server sends the matching credentials with each deploy; an agent older than the server pulls anonymously, so upgrade it (see [Agents](#agents)). See [Registries](../features/registries.md).

### "No such image" when the container is created

**Cause:** the pull didn't fail outright but didn't leave the image behind either, for example when the registry rate-limits you in the middle of the pull. The error then comes from creating the container (`container create <name>: ... No such image`).

**Fix:** pull the image on the target host by hand to see the registry's real error, then deploy again.

```bash
docker pull <image>
```

### "compose file could not be parsed: ..."

**Cause:** a YAML syntax error in `compose.yaml` (the rest of the message is the parser's `yaml: line N: ...`). Other compose errors, such as an unknown field, a wrong type or a missing required variable (`${VAR:?}`), come back as `load project: ...` with the compose-go message.

**Fix:** correct the file in the editor and deploy again. Dockmesh saves files without validating them, so the error shows up at deploy time.

### "image is required (build: not supported in phase 1)"

**Cause:** a service has `build:` but no `image:`. Dockmesh doesn't build images.

**Fix:** build and push the image yourself and reference it with `image:`.

### "service <name> did not become healthy: ..."

**Cause:** the new container exited (`container exited with code N`), its healthcheck reported unhealthy (`container became unhealthy: <last check output>`), or it took too long (`healthcheck timeout`). Dockmesh waits `start_period` + `interval` × `retries` + 10 s; a container without a healthcheck only has to stay up for 5 s.

**Fix:** read the container's logs on its **Logs** tab or with `docker logs <container>`, fix the cause and deploy again.

### "agent offline"

**Cause:** the stack's host is an agent host that isn't connected (HTTP 503). A deploy that loses the agent midway fails with `agent request "req.stack.deploy": agent disconnected`, and one that takes longer than 15 minutes with `agent request "req.stack.deploy" timed out`.

**Fix:** bring the agent back (see [The host shows offline](#the-host-shows-offline)), then deploy again.

### "out of scope: none of your assignments allows this here"

**Cause:** your roles allow deploying, but none of your assignments covers this stack on this host (HTTP 403). A plain `forbidden` means none of your roles includes the permission at all. Stacks created before 0.5.0 have no host until their first deploy, so users limited to some hosts can't deploy them.

**Fix:** an admin can check the reason under **Users & roles** → the user → **Access** → **Access check**, which names the assignment that allows an action or says why none does. Give the user an assignment on the host, its host tag or the stack, or let an admin deploy the stack once.

### Dependency errors

**Cause:** the stack declares other stacks it depends on, and one of them can't be deployed first:

| Message | Meaning |
|---|---|
| `dependency cycle` | Two or more stacks depend on each other. |
| `dependency <name> runs on another host; deploy it there first` | Dependencies are deployed on the same host as the stack. |
| `dependency <name> is declared but missing on disk: ...` | The dependency stack doesn't exist any more. |
| `deploy dependency <name>: ...` | The dependency's own deploy failed; the rest of the message is its error. |

**Fix:** remove the cycle or the stale dependency on the stack page, or deploy the dependency on the right host first.

### "environment override not found: <name>"

**Cause:** the deploy asked for an override file `compose.<name>.yaml` that doesn't exist in the stack directory.

**Fix:** create the override or deploy without it. Git auto-deploys refuse an override that changed since the last manual deploy (`environment override changed since the last manual deploy; deploy the stack once to confirm it`); deploy once by hand to confirm it.

### "this stack still uses a bind mount from a temporary directory; redeploy it first so its data is moved to the stack directory"

**Cause:** the stack was deployed by an older version that resolved relative bind mounts (`./data:/var/lib/app`) against a temporary directory. Scaling and rolling updates refuse to replace those containers, because their data only lives there.

**Fix:** redeploy the stack. The deploy copies the data from the old mount into the stack directory and mounts that from then on.

### Compose options are ignored

**Cause:** Dockmesh doesn't run `docker compose`. It reads the file with compose-go and creates containers through the Docker API, and supports only a subset of service fields. Fields such as `depends_on` (services start in alphabetical order), `devices`, `extra_hosts`, `dns`, `ulimits`, `logging`, `secrets` and `configs` are dropped without a warning.

**Fix:** check the field lists in [How stacks work](../concepts/stacks.md) and move unsupported settings into the image or a different construct.

### "... on remote hosts is not yet implemented"

**Cause:** rolling updates and the resource cleanup preview and delete run on the server's own host only and return `rolling updates on remote hosts are not yet implemented` or `resource cleanup on remote hosts is not yet implemented` on agent hosts. Image update checks and vulnerability scans also cover the server's host only.

**Fix:** use a normal redeploy on agent hosts. See [Multi-host](../features/multi-host.md).

## Agents

### The host shows offline

**Cause:** the agent isn't running, can't reach the server's agent port, or its certificate is rejected.

**Fix:** start on the agent host:

```bash
sudo systemctl status dockmesh-agent
sudo journalctl -u dockmesh-agent -n 100
sudo dockmesh-agent status
```

`dockmesh-agent status` prints whether the agent is enrolled, when its certificate expires (`EXPIRED`, or `expiring soon` within 14 days), the server URL it dials, and whether a TLS connection to that address can be opened. It skips certificate verification, so `reachability:  ok (tls handshake succeeded)` only proves that a TLS listener answers on that address.

The agent connects to the **agent URL** (`wss://<host>:8443/connect` by default) and enrolls at `https://<host>:8443/enroll`, so port 8443 must be reachable from the agent host, directly and not through a proxy that terminates TLS. The agent URL is set under **Settings → General → Agent public URL**; when it is empty it is derived from **Base URL** on port 8443, and `DOCKMESH_AGENT_PUBLIC_URL` in the server's env file wins over both. **Check connection** next to the field, and in the **Add host** dialog, dials the agent URL from the server and reports name resolution, port and certificate. Remote hosts must reach the same address. See [Multi-host](../features/multi-host.md) and [Ports](../reference/ports.md).

If the agent log shows `disconnected, reconnecting`, the connection keeps dropping; the agent retries with a growing delay. A server that disappears is noticed within 90 seconds.

### "certificate is valid for ..., not <name>"

**Cause:** the agent dials a name or IP that isn't in the agent listener certificate. The certificate covers `localhost`, `127.0.0.1`, `::1`, the host of the agent URL and everything in `DOCKMESH_AGENT_SANS`. An agent can dial a different name when `DOCKMESH_AGENT_URL` was edited on the agent host or the server moved to a new address. At startup the server warns with `agent server certificate does not cover the agent URL host` if even the agent URL isn't covered.

**Fix:** add the missing name and restart the server. The certificate is re-issued from the same CA, so agents don't need to enroll again.

```bash
sudo -u dockmesh dockmesh ca reissue-server --add-san dockmesh.example.com --add-san 192.0.2.10
sudo systemctl restart dockmesh
```

To point an agent at a new address, edit `DOCKMESH_AGENT_URL` in `/etc/dockmesh-agent/agent.env` on the agent host and run `sudo systemctl restart dockmesh-agent`. That value wins over the URL saved at enrollment.

### `curl: (90)` when you run the install command

**Cause:** the agent port answered with a different key than the one pinned in the command. Either the command reached another server (a proxy that terminates TLS, a wrong DNS answer), or the agent CA was rotated since you copied it. Copy a new command from **Hosts → Add host**; if it fails again, check that port 8443 reaches Dockmesh without TLS termination in between.

### "This install command has expired or was already used" during the install command

**Cause:** the installer prints this when the agent logs `enrollment failed`. Look at the error in the last log lines it prints:

| Agent log error | Meaning |
|---|---|
| `enroll http 401: invalid or expired enrollment token` | The token expired (1 hour by default, `DOCKMESH_AGENT_ENROLL_TOKEN_TTL`), was already used, or was replaced by a newer one. Revoked hosts can't enroll at all. |
| `DOCKMESH_CA_FINGERPRINT is required to enroll over https` | The install command was copied incompletely or the agent was started by hand without it. |
| `refusing to enroll over plain http` | The agent URL is `ws://`. Enrollment sends the token and the client key, so plain http needs `--insecure-enroll`, for isolated labs only. |
| `x509: certificate signed by unknown authority` | Something between the agent and port 8443 presents its own certificate, usually a proxy or load balancer that terminates TLS. |
| Connection refused or timeout | Port 8443 isn't reachable from the agent host. |

**Fix:** copy a fresh install command (**Hosts** → **Add host** for a new host, or the host page → **Maintenance** → **Re-issue…** for an existing one) and run it again. Make port 8443 reachable without TLS termination in between.

### "CA fingerprint mismatch: pinned ..., got ..."

**Cause:** the install command pins the fingerprint of the agent CA it was generated with. The CA the agent got doesn't match it, because the CA was rotated with `dockmesh ca rotate` after the command was copied, or because a different server answered on the enroll address.

**Fix:** copy a new install command from the server and run it again. If you didn't rotate the CA, check that the agent URL really points at your Dockmesh server before you enroll.

### "Agent certificate expires soon" or "Agent certificate expired"

**Cause:** agent client certificates are valid for 365 days. Agents of v0.5.0 or later renew theirs over the live connection once it has less than 30 days left, and keep their host ID. Admins get the warning **Agent certificate expires soon: <name>** 14 days before expiry if that didn't happen (an old agent, or one that was offline), and the critical notice **Agent certificate expired: <name>** once an offline agent's certificate has run out. The agent log says `client certificate expired: re-issue the enrollment token on the host page and re-run the installer on this host`.

**Fix:** while the agent is still online, upgrade it so it renews by itself. Once it has expired, open the host page → **Maintenance** → **Re-issue enrollment token** → **Re-issue…**, and run the new install command on the agent host. The host keeps its ID, stacks, tags and backup jobs; the old certificate stops working once the agent presents the new one.

### The agent runs but the server logs "agent connect: unknown fingerprint"

**Cause:** the agent's certificate doesn't belong to any enrolled host: the host was revoked, re-enrolled from another machine, or the CA was rotated.

**Fix:** for a revoked host, add it again under **Hosts** → **Add host** (it gets a new host ID, so redeploy its stacks). Otherwise re-issue the token on the host page and run the install command again.

### Containers and stacks on an agent host fail with Docker errors

**Cause:** the agent is connected but can't talk to Docker on its host. The log line `docker client init failed — agent will only respond to pings` means the Docker client settings (for example `DOCKER_HOST`) are invalid; a stopped daemon gives Docker connection errors on every request.

**Fix:** check Docker on the agent host and that the `dockmesh-agent` user is in the `docker` group, then restart the agent.

```bash
sudo systemctl status docker
id dockmesh-agent
sudo systemctl restart dockmesh-agent
```

### "agent update recommended"

**Cause:** the agent speaks an older protocol than the server. Some features, such as the container file browser, metrics history or replace restores, need an up-to-date agent.

**Fix:** open the host page → **Maintenance** → **Upgrade agent** → **Upgrade now**. If the agent already runs the server's build, the server answers `this host's agent already runs the same build as the server; there is nothing to upgrade`. See [Upgrade](../operations/upgrade.md).

## Reverse proxy

The proxy is a Caddy container named `dockmesh-proxy` that runs with host networking, so it binds ports 80 and 443 on the server. Its admin API listens on a Unix socket in `<data dir>/caddy-admin/`. Dockmesh keeps sites in its database and pushes the full config to Caddy whenever a site changes. See [Reverse proxy](../features/reverse-proxy.md).

### The proxy doesn't start, or saving a site fails with "address already in use"

**Cause:** another web server (nginx, Apache, another Caddy or Traefik) already holds port 80 or 443 on the server. Caddy can't bind the ports when Dockmesh pushes the config: **Enable proxy** returns a `caddy /load` error that mentions `address already in use`, and a new site ends with `caddy rejected the configuration; the server log has the details`. A program that shares the port with the proxy doesn't cause an error; the Reverse Proxy page and the notification bell then say which port another program is listening on.

**Fix:** find and stop the other service, or move it to other ports, then click **Enable proxy** again on **Reverse proxy**.

```bash
sudo ss -ltnp | grep -E ':(80|443)\b'
docker logs dockmesh-proxy --tail 50
```

See [Ports](../reference/ports.md).

### A site doesn't get a certificate

**Cause:** for **Let's Encrypt** sites, the certificate authority has to reach the server on port 80 or 443 under the site's name. The usual reasons are DNS that doesn't point at the server yet, a firewall or router that doesn't forward 80 and 443, a proxy or CDN in front that answers the challenge itself, or the authority's rate limits after many attempts. The new-site progress then ends the **Get a certificate** step with a warning (`Caddy could not get a certificate yet and keeps retrying: ...`, or `no certificate seen within 3 minutes`); the site stays saved and Caddy keeps trying.

**Fix:** check the **Certificates** tab (status `failed` or `not served`) and the **Activity** tab of **Reverse proxy**, which show Caddy's certificate events with the authority's error. Fix DNS and port forwarding; Caddy retries by itself.

```bash
dig +short app.example.com
docker logs dockmesh-proxy 2>&1 | grep -i acme
```

While testing, switch **Certificate authority** on the **Caddy** tab to **Let's Encrypt staging (testing)** to stay clear of rate limits; browsers don't trust staging certificates. For names that are only used on your LAN, use the **Internal CA** TLS mode instead.

### The site answers with 502

**Cause:** Caddy can't reach the upstream. It runs on the host network, so container names and Docker network addresses don't resolve.

**Fix:** publish the container's port and use `127.0.0.1:<host port>` (or another address the server itself can reach) as the upstream. Upstreams must be `host:port`.

### "Some proxy routes are not served"

**Cause:** these routes have a host or upstream Dockmesh doesn't accept: an upstream that is not `host:port` or points at the proxy's admin API, or a host that is `localhost`, a loopback address or a bare `*`. They are left out of the Caddy config. Saving such a site fails with `upstream must be host:port, e.g. 127.0.0.1:8080 or app.lan:3000` or `host must be a domain name or address like app.example.com (localhost, loopback addresses and a bare * are not allowed)`.

**Fix:** edit each listed site to a domain name and a `host:port` upstream, or delete it.

### "the Caddy image is set with DOCKMESH_PROXY_IMAGE, so Dockmesh doesn't manage its version"

**Cause:** `DOCKMESH_PROXY_IMAGE` in the env file pins the proxy image, so the Caddy version can't be switched from the UI.

**Fix:** remove the variable and restart Dockmesh to use the versions it ships, or change the image there. A version switched from the **Caddy** tab that doesn't come up with the current config is rolled back automatically.

## Backups and restore

See [Backups](../operations/backups.md) for jobs, targets and restore modes.

### A backup run fails because the target is unreachable

**Cause:** the server (or, for jobs on agent hosts, the server reading from the agent) can't reach the target. The run's error names it, for example `sftp: ssh dial: ...`, `ssh handshake with <host> timed out`, `smb: dial: ...` or `smb: auth failed: ...`. A run whose transfer moves no data for 10 minutes fails with `target stalled`.

**Fix:** open **Backups** → **Targets** → the target and click **Test connection**. Check address, port, credentials and the firewall between the server and the target, then click **Run now** on the job. A running backup continues when you close the browser; stop it with **Cancel** in its progress dialog.

### "sftp: host key changed (recorded ..., server now presents ...)"

**Cause:** the SFTP server presents a different host key than the one recorded the first time. Backups to this target stop until the key is accepted.

**Fix:** if you know why it changed (server reinstalled, key rotated), click **Accept new host key…** on the target. Otherwise treat it as a possible interception and check the server first.

### "the stored credentials can't be decrypted; enter them again"

**Cause:** target credentials are encrypted with the server's secrets key. A different key, for example after restoring the database without its key, can't read them.

**Fix:** edit the target and enter the password or key again.

### "local backup path is outside the allowed backup directories"

**Cause:** local targets may only write below the directories in the `backups.local_roots` setting, by default `/srv/backups` and `<data directory>/backups`.

**Fix:** use a path below one of them. Changing the list is done with `PUT /api/v1/settings` and needs Settings & audit: Manage on everything (`system.upgrade` and `system.update`). If the server runs under the unit `dockmesh init` writes (`ProtectSystem=strict`), the path also has to be in `ReadWritePaths` in `/etc/systemd/system/dockmesh.service`.

### "this job is already running"

**Cause:** a job runs only once at a time. A scheduled run that falls into a running one is skipped with a notification.

**Fix:** wait for the current run, or cancel it from its progress dialog. Schedules use `backups.timezone` (an IANA name such as `Europe/Berlin`), otherwise the server's time zone; the job wizard shows which one applies.

### A restore is refused

**Cause:** restores check the archive and the destination before writing anything. The message says why:

| Message | Meaning |
|---|---|
| `volume <name> is used by running containers (...); stop them before replacing its contents` | **Replace** needs the volume unused. |
| `this backup has no recorded checksum, so it can only be merged, not replace the volume's contents` | Older backups can only be restored with **Merge**. |
| `not enough free space in the volume for a replace restore ...` | **Replace** keeps old and new contents side by side until the swap. |
| `the agent on this host is too old to restore volumes; upgrade it from the Hosts page` | **Replace** onto an agent host needs an up-to-date agent. |
| `another restore is in progress` | Only one restore may write a volume or stack at a time, and at most two run at once. |
| `run has no restorable archive (failed, still running, or removed by retention)` | Pick another run. |
| `archive does not match what the backup run wrote` / `backup archive failed verification, nothing was restored` | The archive on the target is damaged or was replaced. |
| `the backup is encrypted; add the backup key file exported from the original install` | A system restore of an encrypted backup needs the key file. |
| `out of scope: ...`, `only an admin can ...` | Restoring a stack needs the right to edit (or create) that stack on its host; Dockmesh's own volumes and whole-system restores are admin-only. |

**Fix:** act on the message: stop the containers, choose **Merge**, free space, upgrade the agent, wait for the other restore, or pick a different run. Nothing was changed on the destination.

## Upgrades

See [Upgrade](../operations/upgrade.md) for the procedure.

### The bell says "Dockmesh vX.Y.Z available"

**Cause:** the server checks GitHub for new stable releases every 2 hours and tells admins once per version (again after a server restart). Development builds always get the notice.

**Fix:** read the release notes, back up, and upgrade as described in [Upgrade](../operations/upgrade.md). **Settings → Updates** shows the current version, the release notes and the upgrade command. To stop the checks, for example on an air-gapped install, turn off **Automatic update checks** there.

### The server doesn't start after an upgrade

**Cause:** the server takes a copy of the database (`<db>.pre-migrate-<time>.bak` next to it) before it applies migrations. `pre-migration database snapshot failed, no migration applied: ...` means it couldn't write that copy, usually for lack of disk space or permissions. `db migrate failed` means a migration failed.

**Fix:** for the snapshot, free disk space next to the database or fix the directory's owner, then restart. For a failed migration, keep the log lines for a bug report and go back to the previous release as described in [Upgrade](../operations/upgrade.md).

```bash
df -h /var/lib/dockmesh
sudo journalctl -u dockmesh -b | grep -i migrat
```

### Notices to act on after an upgrade

**Cause:** some releases change behaviour that admins have to check, and raise notices for it, for example accounts or tokens whose access was narrowed, or agents that are too old for a feature.

**Fix:** open **Activity**, read each notice and follow the link in it. The bell shows the latest notifications and counts the unread ones.

### The UI shows up in German

**Cause:** the UI follows the browser's language until you pick one.

**Fix:** choose **English** under **My account** → **Language**. The choice is saved per user.

## Vulnerability scanner

### Scans fail with "scanner not configured"

**Cause:** the `grype` binary wasn't found when the server started (HTTP 503). The server log shows `scanner disabled — install grype to enable CVE scans`. `install.sh` installs Grype unless `DOCKMESH_NO_SCANNER=1` is set; a failed download shows up there as `grype install failed — CVE scanning disabled`.

**Fix:** install Grype where the service user finds it on `PATH`, or set `DOCKMESH_SCANNER_BINARY` to its full path, and restart Dockmesh. The binary is only looked up at startup.

Download a release, check it against the release's checksum file and install it (replace the version with the one you want and `amd64` with `arm64` on ARM):

```bash
V=0.121.1
cd "$(mktemp -d)"
curl -fsSLO "https://github.com/anchore/grype/releases/download/v$V/grype_${V}_linux_amd64.tar.gz"
curl -fsSLO "https://github.com/anchore/grype/releases/download/v$V/grype_${V}_checksums.txt"
sha256sum --ignore-missing -c "grype_${V}_checksums.txt"
tar -xzf "grype_${V}_linux_amd64.tar.gz" grype
sudo install -m 0755 grype /usr/local/bin/grype
sudo systemctl restart dockmesh
```

Scanning can also be off on purpose: check the **Scan images with Grype** switch under **Settings → General**, which takes effect without a restart. `DOCKMESH_SCANNER_ENABLED=false` only sets its default. See [Vulnerability scanning](../features/vulnerability-scanning.md).

## Alerts

### An alert rule never fires

**Cause:** a rule fires once its condition has lasted for the rule's duration, and not again during its cooldown or while it is muted. Rules limited to a host, host tag or stack only watch those. A disabled channel, or one whose delivery fails, sends nothing.

**Fix:** check the rule's scope, duration, cooldown and **muted until**. On **Alerts** → **Channels**, click **Test** next to the channel; a failed delivery shows as `last delivery failed — <error>` on the channel. See [Alerts](../features/alerts.md).

## Reporting a bug

Open an issue at [github.com/dockmesh/dockmesh/issues](https://github.com/dockmesh/dockmesh/issues) and include:

- the output of `dockmesh version` (and `dockmesh-agent --version` for agent problems)
- OS, Docker version and how you installed Dockmesh
- the relevant log lines, ideally with `DOCKMESH_LOG_LEVEL=debug`
- steps to reproduce

Remove tokens, passwords and host names you don't want to publish before posting logs.

Don't report security vulnerabilities in a public issue. Follow [`SECURITY.md`](https://github.com/dockmesh/dockmesh/blob/main/SECURITY.md) in the repository.
