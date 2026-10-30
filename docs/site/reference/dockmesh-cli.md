---
title: dockmesh and dockmesh-agent commands
description: Every subcommand and flag of the dockmesh server binary and the dockmesh-agent binary.
---

The `dockmesh` binary is both the server and its admin tool. Run without arguments (or with `serve`) it starts the server; every other subcommand runs once on the server host and exits. These commands work directly on the database and data directory, so they need no API token. For remote, token-based work use [dmctl](./dmctl.md).

## How commands find their configuration

Every subcommand reads the same `DOCKMESH_*` environment variables as the server (see [Environment variables](./environment-variables.md)). There are three ways to supply them:

- `--env-file PATH` (or `--env-file=PATH`), accepted anywhere on the command line for every subcommand. The file holds `KEY=VALUE` lines; `#` comments and blank lines are skipped, surrounding quotes are stripped.
- If you pass no `--env-file` and `DOCKMESH_DB_PATH` is not set, Dockmesh loads `/var/lib/dockmesh/dockmesh.env` (Linux) or `/usr/local/var/dockmesh/dockmesh.env` (macOS) if one exists.
- Variables already set in your shell always win over values from the file.

If your data root lives elsewhere (for example `/data`, which `install.sh` picks when that directory exists), pass `--env-file /data/dockmesh.env` explicitly.

Commands that write files (`admin`, `enroll`, `db`, `ca`, `secrets`, `import`, `restore`) create them as the user that runs the command. On a systemd install the service runs as `dockmesh`, so `sudo -u dockmesh dockmesh <command>` keeps file ownership consistent.

Most commands open the database and apply pending migrations first. Loading the configuration creates `secrets.env` with a new JWT secret if it is missing, and commands that load the agent PKI (`ca export`, `doctor`, `enroll create`) create a CA if none exists yet.

## Command overview

| Command | Purpose |
|---|---|
| `serve` | Start the HTTP server and the agent mTLS listener (default) |
| `init` | Interactive first-run setup on the command line |
| `restore` | Restore DB, stacks and data directory from a system backup |
| `doctor` | Run local health checks |
| `ca export` / `ca rotate` | Export or replace the agent CA |
| `db migrate` / `db backup` | Apply migrations / write a SQLite snapshot |
| `admin create` / `reset-password` / `unlock` / `list-users` | Local user administration |
| `enroll create` / `revoke` / `list` | Agent enrollment |
| `import compose-dir` | Import a directory of compose projects as stacks |
| `config show` | Print the effective configuration |
| `secrets rotate` | Rotate the age key for stack `.env` files |
| `completion bash\|zsh\|fish` | Print a shell completion script |
| `version` | Print version, commit and build date (`--version` and `-v` also work) |
| `help` | Print the command list (`--help`, `-h`) |

## serve

```bash
dockmesh serve --env-file /var/lib/dockmesh/dockmesh.env
```

Starts the web UI and REST API on `DOCKMESH_HTTP_ADDR` (default `:8080`) and the agent listener on `DOCKMESH_AGENT_LISTEN` (default `:8443`). The only flag is `--env-file`. On SIGINT or SIGTERM the server marks itself not ready, waits two seconds, then shuts down. If the database has no users at all and `DOCKMESH_SETUP_FORCE` is not set, the server creates an `admin` user with a random password and prints it once in the log at WARN level.

## init

```bash
sudo dockmesh init
sudo dockmesh init --yes --data-dir /var/lib/dockmesh --base-url https://dockmesh.example.com
```

A command-line alternative to the web setup wizard. It creates `<data-dir>/data` and `<data-dir>/stacks`, creates the admin user, writes `<data-dir>/dockmesh.env` and, unless disabled, installs and starts a service (systemd on Linux, launchd on macOS). Interactive mode needs a terminal; use `--yes` in scripts.

| Flag | Default | Meaning |
|---|---|---|
| `--yes` | `false` | Non-interactive: accept defaults, generate an 18-character admin password and print it once |
| `--data-dir` | `/var/lib/dockmesh` (Linux), `/usr/local/var/dockmesh` (macOS) | Data root |
| `--service` | `true` | Install, enable and start the OS service |
| `--systemd` | `true` | Deprecated alias; `--systemd=false` disables the service step |
| `--listen` | `:8080` | HTTP listen address (see the note below) |
| `--admin-user` | `admin` | Admin username |
| `--base-url` | derived from the hostname and port | Public URL users browse to |

The agent URL defaults to the base URL host with `wss://` and port `8443`. If the admin user already exists, init leaves its password unchanged and says so.

> **Note:** init writes the listen address as `DOCKMESH_LISTEN`, which the server does not read. The server keeps listening on `:8080` unless you add `DOCKMESH_HTTP_ADDR` to `dockmesh.env`.

The systemd unit init writes runs as user `dockmesh`, group `docker`, with `ProtectSystem=strict` and write access limited to the data root and `/var/run/docker.sock`. A copy of the unit is kept at `<data-dir>/dockmesh.service`.

## restore

<!-- update after: restore: stage system restores and swap them in at startup -->
```bash
sudo systemctl stop dockmesh
sudo dockmesh restore --from ./dockmesh-system.tar.gz --dry-run
sudo dockmesh restore --from ./dockmesh-system.tar.gz --force
sudo chown -R dockmesh:docker /var/lib/dockmesh
```

Restores the database, stacks directory and data directory from a system backup archive. Stop the server first: files are written in place. Run as root, the restored files belong to root, hence the `chown` afterwards.

| Flag | Default | Meaning |
|---|---|---|
| `--from` | (required) | Path to the system backup tarball |
| `--force` | `false` | Restore over a database that already has users. The current database is moved to `dockmesh.db.pre-restore-<unix time>` |
| `--dry-run` | `false` | Count files and bytes, write nothing |
| `--skip-sanity` | `false` | Skip the post-restore checks (DB opens, migrates, at least one admin exists) |

Encrypted archives (`.tar.gz.age`) are refused. Decrypt them first with `age -d` and the key exported from the old server. See [Disaster recovery](../operations/disaster-recovery.md).

## doctor

```bash
sudo -u dockmesh dockmesh doctor
```

Checks, in this order, that the configuration loads (warning if `DOCKMESH_AGENT_PUBLIC_URL` is unset), the database opens and answers, the data directory exists (warning only), the Docker daemon answers (warning only), the agent CA and server certificate load, and the data directory is writable. Exits with status 1 if any check fails; warnings alone do not fail. No flags besides the global `--env-file`.

## ca

```bash
dockmesh ca export --out ./ca.pem
sudo systemctl stop dockmesh
sudo -u dockmesh dockmesh ca rotate --reissue-all-agents
sudo systemctl start dockmesh
```

`ca export --out PATH` writes the agent CA certificate (public part only). `--out` is required.

`ca rotate` renames `agents-ca.*` and `agents-server.*` to `*.old`, creates a new CA and server certificate (SANs from `DOCKMESH_AGENT_SANS`), and sets every agent to `pending` with a fresh enrollment token. It prints one `curl … /install/agent.sh?token=… | sudo bash` line per agent, built from `DOCKMESH_BASE_URL`. Run that line on each agent host to re-enroll it.

| Flag | Meaning |
|---|---|
| `--reissue-all-agents` | Required. Confirms that every agent must re-enroll |
| `--yes` | Skip the `[y/N]` prompt |

The running server keeps the old certificates in memory, so rotate while it is stopped (or restart it right after).

## db

```bash
sudo -u dockmesh dockmesh db migrate
sudo -u dockmesh sh -c 'umask 077; dockmesh db backup --out /var/lib/dockmesh/dockmesh-$(date +%F).db'
```

`db migrate` applies pending migrations and reports the DB path. `db backup --out PATH` writes a consistent snapshot with SQLite `VACUUM INTO`; it works while the server runs and refuses to overwrite an existing file. The snapshot holds the database only, not stacks or keys, but the database contains plaintext secrets, so keep the file private (hence the `umask 077`).

## admin

```bash
sudo -u dockmesh dockmesh admin list-users
sudo -u dockmesh dockmesh admin create --username ci --role deployer
sudo -u dockmesh dockmesh admin reset-password --user admin
sudo -u dockmesh dockmesh admin unlock --user admin
```

| Subcommand | Flags | Effect |
|---|---|---|
| `create` | `--username` (required), `--email`, `--role` (default `viewer`), `--password` (omit it to be prompted or pipe it on stdin; a value on the command line ends up in shell history and `ps`) | Creates a local user. Role is a built-in (`admin`, `host-admin`, `deployer`, `operator`, `viewer`) or the name of a custom role. The value is stored without validation, so check the spelling |
| `reset-password` | `--user` (username or ID, required), `--password` | Sets a new password and signs out all of the user's sessions |
| `unlock` | `--user` (required) | Clears a login lockout and the failed-attempt counter; the password stays |
| `list-users` | none | Prints ID, username, role, email and MFA status |

When `--password` is omitted, the command prompts without echo, or reads the password from stdin when stdin is not a terminal. Prefer the prompt or stdin over `--password`, which ends up in shell history. See [Permissions](../concepts/permissions.md) for the roles.

## enroll

```bash
sudo -u dockmesh dockmesh enroll create --name web-02
sudo -u dockmesh dockmesh enroll list
sudo -u dockmesh dockmesh enroll revoke --name web-02
```

<!-- update after: agents: pin the CA at enrollment and verify self-upgrades -->
`enroll create --name NAME` registers a host and prints its ID, the one-time enrollment token and an install command. The token does not expire. `enroll list` shows ID, name, status, hostname, agent version and last-seen time. `enroll revoke` takes `--name` or `--id` and deletes the agent record; an agent that is connected at that moment keeps its connection until it reconnects. The same actions are available under **Hosts** in the UI, see [Hosts and agents](../concepts/hosts-and-agents.md).

## import compose-dir

```bash
dockmesh import compose-dir --path /opt/compose --prefix legacy- --dry-run
```

Scans each subdirectory of `--path` for `compose.yaml`, `compose.yml`, `docker-compose.yaml` or `docker-compose.yml` and creates one stack per directory. The directory name is lowercased and turned into a valid stack name. Only the compose file and a `.env` next to it are copied; other files in the directory (config files, bind-mount data) are not. Nothing is deployed.

| Flag | Default | Meaning |
|---|---|---|
| `--path` | (required) | Directory containing one subdirectory per project |
| `--prefix` | empty | Prepended to every stack name |
| `--force` | `false` | Overwrite existing stacks instead of skipping them |
| `--dry-run` | `false` | Report what would happen |

To take over projects that are already running without restarting them, use `dmctl stacks adopt` instead, see [dmctl](./dmctl.md#stacks).

## config show

```bash
dockmesh config show
```

Prints the effective paths, URLs and toggles after environment and env-file loading, with the JWT secret redacted. Useful when you need to know which `dockmesh.env` a command actually picked up.

## secrets rotate

<!-- update after: fs: contain paths in volume browse, restore and git sources -->
```bash
sudo systemctl stop dockmesh
sudo -u dockmesh dockmesh secrets rotate
sudo systemctl start dockmesh
```

Moves `secrets.age-key` to `secrets.age-key.old`, generates a new key and re-encrypts every stack's `.env.age`. Only one `.old` key is kept. Other age-encrypted data (SSO provider secrets, registry passwords, Git credentials, encrypted backups) is not re-encrypted and can no longer be decrypted with the new key. Fails when `DOCKMESH_SECRETS_ENCRYPT_ENV=false`. A running server keeps the old key in memory, so restart it afterwards. The **Settings** page has a **Rotate key** button that does the same on the live server.

## completion

```bash
dockmesh completion bash > /etc/bash_completion.d/dockmesh
dockmesh completion zsh > "${fpath[1]}/_dockmesh"
dockmesh completion fish > ~/.config/fish/completions/dockmesh.fish
```

## dockmesh-agent

The agent has no flags. It is configured through environment variables, which `install-agent.sh` writes to `/etc/dockmesh-agent/agent.env`.

| Invocation | Effect |
|---|---|
| `dockmesh-agent` | Enroll on first start if no certificate exists, then connect to the server and reconnect with backoff (1 s doubling to 60 s) |
| `dockmesh-agent status` | Print enrollment state, certificate subject and expiry, server URL, and whether a TLS handshake to the server succeeds |
| `dockmesh-agent version` | Print version, OS/arch, commit and build date (`--version`, `-v` also work) |

| Variable | Default | Meaning |
|---|---|---|
| `DOCKMESH_DATA_DIR` | `/var/lib/dockmesh` | Certificate, key, CA, saved server URL, stack mirror |
| `DOCKMESH_ENROLL_URL` | none | `<base-url>/api/v1/agents/enroll`; required for the first start |
| `DOCKMESH_TOKEN` | none | One-time enrollment token; required for the first start |
| `DOCKMESH_AGENT_URL` | value saved at enrollment | `wss://<server>:8443/connect`; overrides the saved URL |

<!-- update after: agents: survive disconnects, flow-control streams, renew certs -->
The agent's client certificate is valid for 365 days and is not renewed automatically. Check the remaining time with `dockmesh-agent status`. Enrollment only accepts tokens of hosts in `pending` state, so to replace an expiring certificate, remove the host and enroll it again (or rotate the CA, which sets every host to `pending`).

`dockmesh-agent status` reads only `DOCKMESH_DATA_DIR` and `DOCKMESH_AGENT_URL` from the environment. Under systemd, run it with the same environment file:

```bash
sudo bash -c 'set -a; . /etc/dockmesh-agent/agent.env; dockmesh-agent status'
```
