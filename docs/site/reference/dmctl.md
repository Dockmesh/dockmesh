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

Release tarballs exist for `linux_amd64`, `linux_arm64`, `darwin_amd64` and `darwin_arm64`. There is no Windows build; build from source with `go build ./cmd/dmctl` if you need one.

## Log in

`dmctl login <server>` stores the server URL and credentials in the config file and checks them against `GET /api/v1/me`. If you leave out the scheme, dmctl uses `http://` for `localhost`, loopback, `*.local`, `*.lan`, `*.internal` and `10.*`, `172.*`, `192.168.*` addresses, and `https://` for everything else.

With an API token (best for CI and scripts):

```bash
# token on stdin, keeps it out of shell history
echo "$DOCKMESH_TOKEN" | dmctl login https://dockmesh.example.com
# or as a flag
dmctl login https://dockmesh.example.com --token dmt_...
```

Without `--token` and `--user`, dmctl prompts for the token on a terminal or reads one line from stdin. Create tokens in the web UI under the user menu, **API tokens**, see [API tokens](../features/api-tokens.md).

With username and password:

```bash
dmctl login http://dockmesh.lan:8080 --user admin
```

dmctl prompts for the password (hidden) and, if the account has two-factor authentication, for the TOTP or recovery code. The MFA prompt needs a terminal. dmctl saves the access and refresh token and, when a request returns 401, refreshes once and retries. The session is still bound by the server's session limits (24 hours absolute and 60 minutes idle by default), so use an API token for anything unattended.

| Command | Flags | Effect |
|---|---|---|
| `login <server>` | `--token`, `--user` | Save server and credentials |
| `logout` | none | Clear the saved tokens, keep the server URL |
| `whoami` | none | Print ID, email (or username) and role of the current identity |

## Configuration

The config file is JSON with the keys `server`, `token`, `refresh_token` and `insecure`, written with mode `0600`:

| OS | Path |
|---|---|
| Linux | `~/.config/dmctl/config.json` (or `$XDG_CONFIG_HOME/dmctl/config.json`) |
| macOS | `~/Library/Application Support/dmctl/config.json` |

Server and token are resolved in this order: `--server`/`--token` flag, then `DMCTL_SERVER`/`DMCTL_TOKEN`, then the config file. `insecure` is taken from the flag or the config file. With `DMCTL_SERVER` and `DMCTL_TOKEN` set you need no config file at all, which suits CI:

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
| `--host` | local host | Host ID for stack and container commands, sent as `?host=` |
| `--insecure` | `false` | Skip TLS certificate verification. Only for test servers with self-signed certificates |
| `-o`, `--output` | `table` | `table`, `json` or `yaml` |

`yaml` prints the same indented JSON as `json`. Host IDs are listed in the UI under **Hosts** and by `GET /api/v1/hosts`; the local host is `local`. The `--host all` value is not supported by dmctl: list endpoints answer it with a different response shape that dmctl cannot read.

## stacks

`stack` works as an alias.

| Command | Flags | Calls | Needs |
|---|---|---|---|
| `stacks list` | none | `GET /stacks` | `stacks.view` |
| `stacks get <name>` | none | `GET /stacks/{name}` | `stacks.view` |
| `stacks deploy <name>` | `--environment NAME` | `POST /stacks/{name}/deploy` | `stacks.deploy` |
| `stacks stop <name>` | none | `POST /stacks/{name}/stop` | `stacks.deploy` |
| `stacks status <name>` | none | `GET /stacks/{name}/status` | `stacks.view` |
| `stacks logs <name>` | `--service`, `--tail` (100), `-f/--follow` | status + log WebSocket | `stacks.view`, `containers.logs` |
| `stacks adopt [path]` | see below | `GET /stacks/discovered`, `POST /stacks/adopt` | `stacks.view`, `stacks.adopt` |

`stacks get` prints `compose.yaml` and the decrypted `.env`. `stacks deploy` uses the overlay `compose.<NAME>.yaml` when `--environment` is set, otherwise the stack's active environment; dependencies are deployed first. The command waits until the server finishes the deploy. `stacks logs` follows the first container of each service and prefixes lines with `[service]` when there is more than one.

`stacks adopt` takes over a compose project that is already running, without restarting its containers. It looks for the compose file in `path` (default `.`), finds the running project on the target host by its `com.docker.compose.project` label, packs the directory into a bundle and uploads it.

| Flag | Default | Meaning |
|---|---|---|
| `--name` | directory name | Stack name; must match the compose project label |
| `--dry-run` | `false` | Show services, bundle contents and warnings, upload nothing |
| `--yes` | `false` | Skip the confirmation prompt |
| `--with-env` | `false` | Include `.env` (off by default because it usually holds secrets) |
| `--max-size` | `104857600` | Maximum uncompressed bundle size in bytes |

Containers keep running after adoption, but the next deploy from Dockmesh recreates all of them. See [Stacks](../features/stacks.md).

## containers

| Command | Flags | Needs |
|---|---|---|
| `containers list` | `-a/--all` | `containers.view` |
| `containers logs <id>` | `--tail` (100), `-f/--follow` | `containers.logs` |
| `containers exec <id> [-- cmd args...]` | none | `containers.exec` |

`exec` opens an interactive TTY session, `/bin/sh` by default, and forwards terminal resizes. There are no start, stop or remove commands; use the UI or the API for container lifecycle.

Known issues: `containers list --all` still lists only running containers (the server expects `all=true`, dmctl sends `all=1`), and log streams always follow until you press Ctrl-C or the container stops, whether or not you pass `--follow`.

## backup

| Command | Flags | Needs |
|---|---|---|
| `backup jobs` | none | `backups.view` |
| `backup run <job-name-or-id>` | none | `backups.update` (plus `backups.view` when you pass a name) |
| `backup runs` | `--limit` (20) | `backups.view` |

<!-- update after: backups: run in the background, set the time zone, use saved targets -->
`backup run` waits for the backup to finish. dmctl gives up on any request after 60 seconds, and the server ties the run to that request, so a longer backup is cancelled. Start long jobs from the UI or let the schedule run them. The **TYPE** column of `backup jobs` and the **BYTES** column of `backup runs` stay empty because dmctl reads field names the server does not send; use `-o json` for the full data.

## alert

| Command | Flags | Needs |
|---|---|---|
| `alert rules` | none | `alerts.view` |
| `alert history` | `--limit` (100) | `alerts.view` |
| `alert list-active` | none | `alerts.view` |

All alert commands are read-only. `list-active` looks for history entries with status `firing`, but the server records them as `fired`, so the list is always empty; use `alert history` instead. See [Alerts](../features/alerts.md).

## enroll

```bash
dmctl enroll agent --name web-02
```

Creates a host entry (`POST /agents`, needs `hosts.create`) and prints the install command to run as root on the new host. The token in that command has no expiry. dmctl's output claims the token is valid for about 15 minutes; it is not. See [Hosts and agents](../concepts/hosts-and-agents.md).

## Examples

```bash
# CI: deploy the staging overlay of a stack on a remote host
DMCTL_SERVER=https://dockmesh.example.com DMCTL_TOKEN=$TOKEN \
  dmctl stacks deploy shop --environment staging --host 3f2c9a

# Script-friendly output
dmctl stacks list -o json | jq -r '.[].name'

# Shell into a container on the local host
dmctl containers exec 4b1e2f -- /bin/bash
```
