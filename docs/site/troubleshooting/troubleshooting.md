---
title: Troubleshooting
description: Symptoms, causes and fixes for the problems Dockmesh admins run into most often.
---

This page is organised by symptom. Most fixes start with the server log, so read the first section even if your problem is somewhere else.

## Where the logs are

| Install | Server log | Agent log |
|---|---|---|
| Linux, `install.sh` or `dockmesh init` (systemd unit `dockmesh.service`) | `journalctl -u dockmesh` | `journalctl -u dockmesh-agent` |
| macOS (launchd `dev.dockmesh.service`) | `/usr/local/var/log/dockmesh.log` and `dockmesh.err` | not applicable |
| Locally built Docker image | `docker logs <container>` | not applicable |

The server logs JSON at `info` level by default. For more detail, set these in the env file (`/var/lib/dockmesh/dockmesh.env` on a default Linux install) and restart:

```bash
DOCKMESH_LOG_LEVEL=debug   # debug | info | warn | error
DOCKMESH_LOG_FORMAT=text   # text or json (anything else means json)
```

## Cannot reach the UI or the service won't start

Check the service and the last log lines:

```bash
sudo systemctl status dockmesh
sudo journalctl -u dockmesh --since '10 min ago'
```

Look for these lines:

| Log message | Meaning |
|---|---|
| `http listening` with `addr` | The web UI is up on that address (default `:8080`, set with `DOCKMESH_HTTP_ADDR`). |
| `http server error` | The listener failed, usually because another process already holds the port. |
| `db open failed` / `db migrate failed` | The database path is wrong or not writable by the service user. |
| `docker daemon not reachable yet` | Dockmesh runs, but container endpoints return 503 until Docker answers. Check `systemctl status docker` and that the service user is in the `docker` group. |
| `server starting in setup-mode` | The install wizard owns the UI. Open `/setup` (see below). |

If you picked a non-default port in `dockmesh init` and the server still listens on 8080, add `DOCKMESH_HTTP_ADDR=:<port>` to the env file and restart. `init` writes the port under a variable name the server does not read.

### Run `dockmesh doctor`

```bash
sudo -u dockmesh dockmesh doctor
```

It checks, in this order: that the configuration loads (and warns when `DOCKMESH_AGENT_PUBLIC_URL` is unset), that the database opens and answers, that the data directory exists (a warning only), that the Docker daemon answers (a warning, not a failure, if it doesn't), that the agent CA and server certificate load, and that the data directory is writable. It exits non-zero if any check fails.

Like the other `dockmesh` subcommands, `doctor` loads `/var/lib/dockmesh/dockmesh.env` (macOS: `/usr/local/var/dockmesh/dockmesh.env`) automatically unless `DOCKMESH_DB_PATH` is already set in your shell. If your data lives elsewhere, for example `/data`, pass the file explicitly: `sudo -u dockmesh dockmesh doctor --env-file /data/dockmesh.env`. Run it as the service user (`sudo -u dockmesh`) so files it creates keep the right owner.

## Lost admin password or locked out

After five failed sign-ins the account is locked for 15 minutes and the login page shows `account temporarily locked — try again in N minutes`. The lock expires on its own. Both values are configurable on the **Authentication** page (**Lockout · max attempts**, **Lockout · duration (min)**); 0 disables the lockout. Separately, ten failed attempts per minute from one IP return `too many login attempts — try again later` for five minutes.

To clear a lock without changing the password:

```bash
sudo -u dockmesh dockmesh admin unlock --user admin
```

To set a new password:

```bash
sudo -u dockmesh dockmesh admin reset-password --user admin
# prompts for the password; or pass --password '<new>' (it then ends up in shell history)
```

`--user` takes a username or user ID. `reset-password` signs out every session of that user, but it does not clear an active lockout, so run `admin unlock` too if the account is locked. `sudo -u dockmesh dockmesh admin list-users` shows all accounts with role and MFA state. If the browser keeps getting locked out, delete the saved password for the Dockmesh URL; autofill replaying an old password is the usual cause.

## Setup wizard not showing or expired

The wizard at `/setup` is active only when the database has no admin user, or when `DOCKMESH_SETUP_FORCE=true` is set. `install.sh` writes that flag into the env file, and a successful wizard run removes it again.

- **The wizard expired.** It accepts the final step for 30 minutes after the server starts. After that the commit fails with `setup window expired — restart the dockmesh service to start a fresh window`. Run `sudo systemctl restart dockmesh` and finish within 30 minutes.
- **You get the login page instead of the wizard.** An admin already exists. Without `DOCKMESH_SETUP_FORCE=true`, the first start creates a user named `admin` with a random password and logs it once at WARN level as `bootstrap admin created — store this password, it will not be shown again`. Find it in the first log lines (`journalctl -u dockmesh` or `docker logs`), or set a new one with `dockmesh admin reset-password --user admin`.
- **The wizard keeps coming back after setup.** The commit could not remove `DOCKMESH_SETUP_FORCE` from the env file (the wizard reports `could not scrub setup flag`). Delete the line by hand and restart.

## "Signed out because of a security update" after an upgrade

If the login page says `You were signed out because of a security update. Please sign in again.`, the refresh request was answered with `session_outdated`. Sessions created before an upgrade that changed the token format can't be refreshed. Sign in again; nothing else is needed.

<!-- update after: web: share session refresh across tabs, fix stale page loads -->
If you are signed out unexpectedly while several Dockmesh tabs are open, the tabs refreshed the same token at the same time. The server treats that as token reuse (`token reuse detected`) and revokes the session. Sign in again, and keep one tab open if it keeps happening.

## Agent shows offline

Start on the agent host:

```bash
sudo systemctl status dockmesh-agent
sudo journalctl -u dockmesh-agent -n 100
sudo /var/lib/dockmesh/bin/dockmesh-agent status
```

`dockmesh-agent status` prints whether the agent is enrolled, when its certificate expires (`EXPIRED` or `expiring soon` within 14 days), the server URL it dials, and whether a TLS handshake to that URL succeeds.

The agent talks to the server on two URLs, both derived from the server's environment:

| Purpose | Server setting | Default |
|---|---|---|
| Enrollment and installer download (HTTP API) | `DOCKMESH_BASE_URL` | `http://localhost:8080` |
| Persistent mTLS connection | `DOCKMESH_AGENT_PUBLIC_URL` | `wss://<host of BASE_URL>:8443/connect` |

The **Add host** install command is built from these values, so a default `DOCKMESH_BASE_URL` gives a command that points the agent at its own `localhost`. Set both variables in the server's env file to addresses the agent hosts can reach and restart before adding hosts. The public URL you enter in the setup wizard does not change these values.

Common causes:

- **Port 8443 not reachable.** The agent listener (`DOCKMESH_AGENT_LISTEN`, default `:8443`) must be reachable from the agent host, directly and not through a proxy that terminates TLS. Test with `openssl s_client -connect <server>:8443`.
- **Certificate name mismatch.** The agent log shows `certificate is valid for ... not <name>`. The server certificate covers `localhost`, `127.0.0.1`, `::1` and whatever is in `DOCKMESH_AGENT_SANS` (comma-separated hostnames and IPs; `dockmesh init` fills it in). Add the name the agent dials, then restart the server. The certificate is re-issued from the same CA, so agents don't need to re-enroll.
- **Wrong server URL on the agent.** Edit `DOCKMESH_AGENT_URL` in `/etc/dockmesh-agent/agent.env` and run `sudo systemctl restart dockmesh-agent`. The environment value takes precedence over the URL saved at enrollment.
- **Docker not running on the agent host.** The agent stays connected, but every container and stack request fails with a Docker connection error. Check `systemctl status docker` on the agent host. The log line `docker client init failed` means the Docker client settings (for example `DOCKER_HOST`) are invalid, not that the daemon is down.

### Certificate expired after a year

<!-- update after: agents: survive disconnects, flow-control streams, renew certs -->
Agent certificates are valid for 365 days from enrollment and are not renewed automatically. After that the agent can no longer connect and has to be enrolled again. Check the expiry date with `dockmesh-agent status`.

### Re-enrolling an existing host

<!-- update after: agents: pin the CA at enrollment and verify self-upgrades -->
**Re-issue** on the host detail page creates a new token, but a host that has enrolled before cannot use it: the server accepts enrollment tokens only for hosts in `pending` state, and the agent's enroll request fails with `invalid token`. To re-enroll a single host (expired certificate, reinstalled machine), remove it and add it again:

1. On the host detail page, click **Revoke &lt;name&gt;…** (or run `sudo -u dockmesh dockmesh enroll revoke --name <name>` on the server).
2. On **Hosts**, click **Add host** with the same name and copy the install command.
3. Run the command on the agent host. The installer stops the agent, deletes the old certificate and enrolls again.

Containers on the host keep running. The new entry gets a new host ID, so redeploy stacks that were deployed to the old entry. To re-enroll every host at once, for example after a CA compromise, `sudo -u dockmesh dockmesh ca rotate` issues a new CA and puts all agents back into `pending` with fresh tokens it prints.

## Deploy fails

Deploy errors are returned to the UI as the response message. Dockmesh pulls every missing image before it touches any container, so a pull failure leaves the running deployment as it was. Errors after that point stop the deploy at the failing service. Services before it already run the new containers, services after it keep the old ones, and there is no automatic rollback. Use the stack's deploy history to go back (see [Stacks](../features/stacks.md)).

| Message | Cause and fix |
|---|---|
| `port already in use on the target host — pick a different host port (see compose.yaml ports:) or stop whatever is bound to it` (HTTP 422) | Another container or process holds the host port. Dockmesh does not check ports before deploying; the message comes from Docker. Change the host port or stop the other service. |
| `could not pull image — check the tag exists and, for private registries, that credentials are configured: ...` | Wrong tag or a private image. Add credentials on the **Registries** page. |
| `registry authentication failed — configure credentials under Settings → Registries: ...` | Credentials are missing or wrong. They are managed on the **Registries** page in the sidebar. |
| `compose file could not be parsed: ...` | YAML or compose syntax error. |
| `service <name>: image is required (build: not supported in phase 1)` | `build:` without `image:` is not supported. Build and push the image yourself and reference it with `image:`. |
| `service <name> did not become healthy: ...` | The container exited (`container exited with code N`), its healthcheck reported unhealthy, or it timed out. Dockmesh waits `start_period` + `interval` × `retries` + 10 s; containers without a healthcheck only need to stay up for 5 s. Check the container logs. |
| `agent request "..." timed out` | Remote host only: every agent request has a fixed 30 s limit, which includes image pulls. |

<!-- update after: agents: survive disconnects, flow-control streams, renew certs -->
On remote hosts, private registry credentials are not passed to the agent, and slow pulls run into the 30 s limit. Both have the same workaround: log in and pull the image on the agent host first (`docker login`, `docker pull <image>`). Dockmesh only pulls when the image is missing, so the deploy then uses the local copy.

### Compose options are ignored

Dockmesh does not run `docker compose`. It reads the file with compose-go and creates containers through the Docker API, and it supports only a subset of service fields. Fields such as `depends_on`, `deploy` (except `deploy.update_config`, which rolling updates read), `devices`, `extra_hosts`, `dns`, `ulimits`, `logging`, `secrets` and `configs` are dropped without a warning. The full list is in [How stacks work](../concepts/stacks.md).

### Relative bind mounts lose their data

<!-- update after: backups: scope hooks, limit targets, check sftp keys; fix bind mounts -->
A bind mount such as `./data:/var/lib/app` is resolved against a temporary staging directory that is deleted after the deploy, not against the stack directory. Data written there does not persist. Use an absolute host path (`/srv/app/data:/var/lib/app`) or a named volume.

## Remote host operation returns "not yet implemented"

These operations work on the local host only and return a `... on remote hosts is not yet implemented` error on agent hosts: rolling updates, the resource cleanup preview and delete, and browsing files inside containers. Image update detection, one-click updates, vulnerability scans, container metrics history and alert rules also cover local containers only. See [Multi-host](../features/multi-host.md).

## Reverse proxy routes vanish or TLS behaves unexpectedly

<!-- update after: proxy: keep the Caddy container, resume routes, serve tls none as http -->
The proxy is a Caddy container named `dockmesh-proxy`. Dockmesh keeps the routes in its database and pushes the whole config to Caddy's admin API whenever a route changes. Caddy holds that config in memory only:

- **Routes gone after Caddy or the Docker host restarted.** Caddy came back with an empty config. Restart Dockmesh, or click **Stop** and then **Enable proxy** on the **Proxy** page, or edit any route. Each of these pushes all routes again.
- **Proxy off after a Dockmesh restart.** **Enable proxy** on the **Proxy** page does not save the setting. Set `DOCKMESH_PROXY_ENABLED=true` in the env file so the proxy starts with the server.
- **A route with TLS `none` still redirects to HTTPS.** All routes share one Caddy server, and Caddy applies automatic HTTPS to every host name. There is no plain-HTTP mode. Use `internal` for LAN-only names.

More in [Reverse proxy](../features/reverse-proxy.md).

## Vulnerability scanner unavailable

Scans return `scanner not configured` (HTTP 503) when the `grype` binary was not found at startup. The server log shows `scanner disabled — install grype to enable CVE scans`. Grype is an external program that Dockmesh runs as a subprocess:

- `install.sh` installs it unless you set `DOCKMESH_NO_SCANNER=1`. A failed download shows up as `grype install failed — CVE scanning disabled`.
- To fix, install grype ([anchore/grype](https://github.com/anchore/grype)) so the service user finds it on `PATH`, or set `DOCKMESH_SCANNER_BINARY` to its full path, then restart Dockmesh. The binary is only looked up at startup.
- The Docker image built from the repository does not contain grype, so scanning is unavailable there unless you add the binary to the container yourself.
- `DOCKMESH_SCANNER_ENABLED=false` turns the scanner off on purpose.

## Backups

**A backup run seems stuck.** Runs execute in the server, so closing the browser does not stop them. Open the run's progress dialog to see the step it is in and its log. A run whose transfer moves no data for 10 minutes fails on its own with "target stalled"; you can also stop it earlier with **Cancel**. Schedules use the server's local time zone.

<!-- update after: backups: run in the background, set the time zone, use saved targets -->
**SMB targets fail when the server runs on Linux.** The SMB target builds remote paths incorrectly on Linux, so the job folder is never created and listing breaks. Use SFTP or S3, or mount the share on the server (`mount -t cifs`) and point a **Local** target at the mount path. If your unit was written by `dockmesh init` (it sets `ProtectSystem=strict`), add the mount path to `ReadWritePaths` in `/etc/systemd/system/dockmesh.service`.

<!-- update after: backups: keep retention per job, fail on partial or missing data -->
**Retention deletes another job's archives.** Archives are stored under `jobs/<name>/`, where the name is lowercased and every character other than `a-z`, `0-9` and `-` becomes `-`. Retention lists the target by that prefix, so on S3 a job named `db` also sees the archives of `db-prod`, and `My DB` and `my-db` share one folder. Give every job a unique name that is not a prefix of another job's name and contains at least one letter or digit.

More in [Backups](../operations/backups.md).

## Alerts are not firing

<!-- update after: alerts: fix duration gate, cooldown and mute handling -->
Alert rules can only watch `cpu_percent` and `mem_percent` of containers on the local host. The duration check almost never passes: a rule with duration 0 never fires, and rules with a duration fire only rarely. The built-in rules are therefore effectively silent as well. Cooldown and **muted until** are saved but not applied. Don't rely on Dockmesh alert rules for anything important; scrape `/metrics` with Prometheus or use another external monitor.

To check that a channel delivers at all, click **Test** next to it on the channel list of the **Alerts** page. The test does not depend on any rule. See [Alerts](../features/alerts.md).

## Reporting a bug

Open an issue at [github.com/dockmesh/dockmesh/issues](https://github.com/dockmesh/dockmesh/issues) and include:

- the output of `dockmesh version` (and `dockmesh-agent --version` for agent problems)
- OS, Docker version and how you installed Dockmesh
- the relevant log lines, ideally with `DOCKMESH_LOG_LEVEL=debug`
- steps to reproduce

Remove tokens, passwords and host names you don't want to publish before posting logs.

<!-- update after: security: report through GitHub private advisories -->
Do not report security vulnerabilities in a public issue. Follow [`SECURITY.md`](https://github.com/dockmesh/dockmesh/blob/main/SECURITY.md) in the repository.
