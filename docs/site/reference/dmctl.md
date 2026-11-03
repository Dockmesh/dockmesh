---
title: dmctl
description: Reference for dmctl, the command-line client that talks to a Dockmesh server over its REST API.
---

`dmctl` is a small client for a running Dockmesh server. It calls the same REST API as the web UI, with a user session or an API token, so it can run anywhere that reaches the server: your workstation, a CI runner, the server itself. Server-side maintenance (restore, CA rotation, user unlock) is done with the `dockmesh` binary on the server host, see [dockmesh commands](./dockmesh-cli.md).

## Install

`scripts/install.sh` installs `dmctl` next to the server binary (`/usr/local/bin/dmctl` by default). For another machine, take it from the release tarball for your platform:

```bash
curl -fLO https://github.com/dockmesh/dockmesh/releases/latest/download/dockmesh_linux_amd64.tar.gz
curl -fLO https://github.com/dockmesh/dockmesh/releases/latest/download/checksums.txt
sha256sum -c --ignore-missing checksums.txt
tar -xzf dockmesh_linux_amd64.tar.gz dmctl
sudo install -m 0755 dmctl /usr/local/bin/dmctl
dmctl --version
```

Release tarballs exist for `linux_amd64`, `linux_arm64`, `darwin_amd64` and `darwin_arm64`. Releases contain no Windows build; `make build` produces `bin/dmctl-windows-amd64.exe`, or run `go build ./cmd/dmctl`.

## Log in

`dmctl login <server>` stores the server URL and credentials in the config file and checks them against `GET /api/v1/me`. If you leave out the scheme, dmctl uses `http://` for `localhost`, `127.0.0.1`, `::1`, `*.local`, `*.lan`, `*.internal` and `10.*`, `172.*`, `192.168.*` addresses, and `https://` for everything else.

With an API token (best for CI and scripts):

```bash
# token on stdin, keeps it out of shell history
echo "$DOCKMESH_TOKEN" | dmctl login https://dockmesh.example.com
# or as a flag
dmctl login https://dockmesh.example.com --token dmt_...
```

Without `--token` and `--user`, dmctl prompts for the token on a terminal (hidden input) or reads one line from stdin. Create tokens under **API tokens** in the user menu, see [API tokens](../features/api-tokens.md).

With username and password:

```bash
dmctl login https://dockmesh.example.com --user admin
```

dmctl prompts for the password (hidden) and, if the account has two-factor authentication, for the TOTP or recovery code. dmctl saves the access and refresh tokens and, when a request returns 401, refreshes once and retries. The session ends when dmctl has not refreshed it for 60 minutes (the default idle limit on the **Authentication** settings page; each refresh also renews the 24-hour lifetime), so use an API token for anything unattended.

| Command | Flags | Effect |
|---|---|---|
| `login <server>` | `--token`, `--user` | Save server and credentials, then check them |
| `logout` | none | Clear the saved tokens, keep the server URL |
| `whoami` | none | Print the current identity |

`whoami` prints ID, email (or username) and a ROLE column. The server no longer returns a single role, so ROLE shows `<nil>`; `dmctl whoami -o json` shows the identity with its `assignments` and `permissions`. For an API token, the identity is `api-token` with the token's `api_token_id`.

## Configuration

The config file is JSON with the keys `server`, `token`, `refresh_token` and `insecure`, written with mode `0600`:

| OS | Path |
|---|---|
| Linux | `~/.config/dmctl/config.json` (or `$XDG_CONFIG_HOME/dmctl/config.json`) |
| macOS | `~/Library/Application Support/dmctl/config.json` |
| Windows | `%AppData%\dmctl\config.json` |

Server and token are resolved in this order: `--server`/`--token` flag, then `DMCTL_SERVER`/`DMCTL_TOKEN`, then the config file. `insecure` is on when the flag is set or the config file says so. With `DMCTL_SERVER` and `DMCTL_TOKEN` set you need no config file at all, which suits CI:

```bash
export DMCTL_SERVER=https://dockmesh.example.com
export DMCTL_TOKEN=dmt_...
dmctl stacks deploy web
```

## Global flags

| Flag | Default | Meaning |
|---|---|---|
| `--server` | from env/config | Server URL |
| `--token` | from env/config | Bearer token (API token or access token) |
| `--host` | server host | Host ID for stack and container commands, sent as `?host=` |
| `--insecure` | `false` | Skip TLS certificate verification. Only for test servers with self-signed certificates |
| `-o`, `--output` | `table` | `table`, `json` or `yaml` |
| `-h`, `--help` | | Help for any command |
| `-v`, `--version` | | Print the version (root command only) |

`yaml` prints the same indented JSON as `json`. Host IDs are listed under **Hosts** and by `GET /api/v1/hosts`; the server host is `local`. `--host all` works for `containers list`, which then lists containers of every host you can reach; the other commands need one host.

Requests time out after 60 seconds, except `stacks deploy`, which waits as long as the server takes. Errors print the server's `error` message and exit with status 1.

## stacks

`stack` works as an alias.

| Command | Flags | Calls | Needs |
|---|---|---|---|
| `stacks list` | none | `GET /stacks` | `stacks.view` |
| `stacks get <name>` | none | `GET /stacks/{name}` | `stacks.view` |
| `stacks deploy <name>` | `--environment NAME` | `POST /stacks/{name}/deploy` | `stacks.deploy` |
| `stacks stop <name>` | none | `POST /stacks/{name}/stop` | `stacks.operate` |
| `stacks status <name>` | none | `GET /stacks/{name}/status` | `stacks.view` |
| `stacks logs <name>` | `--service`, `--tail` (100), `-f`/`--follow` | status + log WebSocket | `stacks.view`, `containers.logs` |
| `stacks adopt [path]` | see below | `GET /stacks/discovered`, `POST /stacks/adopt` | `stacks.view`, `stacks.adopt` |

`stacks list` prints name, host and deployment status. `stacks get` prints `compose.yaml` and `.env`; the `.env` part is only there when you may deploy the stack or hold the **See environment values** extra on it. `stacks deploy` uses the override `compose.<NAME>.yaml` when `--environment` is set, otherwise the stack's default override; dependencies are deployed first. The command waits until the server has finished the deploy. If the stack's files were changed outside Dockmesh, the deploy is refused until you confirm the change in the UI. `stacks logs` reads the first container of each service and prefixes lines with `[service]` when there is more than one; without `--follow` it prints the last lines and exits.

`stacks adopt` takes over a compose project that is already running, without restarting its containers. It looks for the compose file in `path` (default `.`), finds the running project on the target host by its `com.docker.compose.project` label, packs the directory into a bundle and uploads it. Without `--host` the stack is bound to the host the project was found on.

| Flag | Default | Meaning |
|---|---|---|
| `--name` | directory name | Stack name; must match the compose project label |
| `--dry-run` | `false` | Show services, bundle contents and warnings, upload nothing |
| `--yes` | `false` | Skip the confirmation prompt |
| `--with-env` | `false` | Include `.env` (off by default because it usually holds secrets) |
| `--max-size` | `104857600` | Maximum uncompressed bundle size in bytes |

Containers keep running after adoption, but the next deploy from Dockmesh recreates all of them. See [Stacks](../features/stacks.md#adopting-a-running-project).

## containers

| Command | Flags | Needs |
|---|---|---|
| `containers list` | `-a`/`--all` | `containers.view` |
| `containers logs <id>` | `--tail` (100), `-f`/`--follow` | `containers.logs` |
| `containers exec <id> [-- cmd args...]` | none | `containers.exec` |

`list` shows running containers, or all with `--all`. `logs` prints the last lines and exits, or keeps streaming with `--follow` until you press Ctrl-C or the container stops. `exec` opens an interactive TTY session, `/bin/sh` by default, and forwards terminal resizes. There are no start, stop or remove commands; use the UI or the API for container lifecycle.

## backup

| Command | Flags | Needs |
|---|---|---|
| `backup jobs` | none | `backups.view` |
| `backup run <job-name-or-id>` | `--no-wait` | `backups.run`, plus `backups.view` for a job name and for waiting |
| `backup runs` | `--limit` (20) | `backups.view` |

`backup run` starts the job (`POST /backups/jobs/{id}/run`), which runs in the background on the server, and then waits for it: it polls `GET /backups/runs/{id}` every two seconds until the run is no longer `running`, and prints the final status, size and error. Five failed polls in a row (a server restart, for example) end the wait with an error; the run itself goes on. The command exits with status 1 when the run failed. `--no-wait` prints the run ID and returns at once. Stopping dmctl with Ctrl-C only stops the waiting; cancel the run from the UI.

A job that is already running is not started a second time; the server answers `409`. The **TYPE** column of `backup jobs` stays empty, because dmctl reads a field the server does not send; use `-o json` for the full data. See [Backups](../operations/backups.md).

## alert

| Command | Flags | Needs |
|---|---|---|
| `alert rules` | none | `alerts.view` |
| `alert history` | `--limit` (100) | `alerts.history` |
| `alert list-active` | none | `alerts.history` |

All alert commands are read-only. `list-active` reads the latest 500 history entries and shows, per rule and target, those whose newest entry is `fired`. One-off events (OOM kills, failed backups) never count as active. See [Alerts](../features/alerts.md).

## enroll

```bash
dmctl enroll agent --name web-02
```

Creates a host entry (`POST /agents`, needs `hosts.create`) and prints the install command to run as root on the new host. The enrollment token in that command can be used once and expires after 24 hours unless the server sets another lifetime (`DOCKMESH_AGENT_ENROLL_TOKEN_TTL`). With `-o json` dmctl prints the full response, including the token and its expiry. See [Hosts and agents](../concepts/hosts-and-agents.md).

## Examples

```bash
# CI: deploy the staging override of a stack on a remote host
DMCTL_SERVER=https://dockmesh.example.com DMCTL_TOKEN=$TOKEN \
  dmctl stacks deploy shop --environment staging --host 3f2c9a

# Run a backup job and fail the CI step if it fails
dmctl backup run nightly-db

# Script-friendly output
dmctl stacks list -o json | jq -r '.[].name'

# Shell into a container on the server host
dmctl containers exec 4b1e2f -- /bin/bash
```
