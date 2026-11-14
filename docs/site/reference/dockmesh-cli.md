---
title: dockmesh and dockmesh-agent commands
description: Every subcommand and flag of the dockmesh server binary and the dockmesh-agent binary.
---

The `dockmesh` binary is both the server and its admin tool. Run without arguments (or with `serve`) it starts the server; every other subcommand runs once on the server host and exits. These commands work directly on the database and data directory, so they need no API token. For remote, token-based work use [dmctl](./dmctl.md).

## How commands find their configuration

Every subcommand reads the same `DOCKMESH_*` environment variables as the server (see [Environment variables](./environment-variables.md)). There are three ways to supply them:

- `--env-file PATH` (or `--env-file=PATH`), accepted anywhere on the command line for every subcommand. The file holds `KEY=VALUE` lines; `#` comments and blank lines are skipped, surrounding quotes are stripped.
- If you pass no `--env-file` and neither `DOCKMESH_DATA_DIR` nor `DOCKMESH_DB_PATH` is set, Dockmesh loads the first of `/etc/dockmesh/dockmesh.env`, `/var/lib/dockmesh/dockmesh.env`, `/data/dockmesh.env` and `/usr/local/var/dockmesh/dockmesh.env` that exists. On Linux and macOS it only does so when the file belongs to root, to you or to the `dockmesh` user and is not writable by group or others; otherwise it prints `not loading <file>` and goes on without it.
- Variables already set in your shell always win over values from the file.

If your config lives elsewhere, pass `--env-file <path>` explicitly.

### system migrate-layout

`sudo dockmesh system migrate-layout` moves the config of an install from before 0.6 to `/etc/dockmesh/dockmesh.env` (directory 0750, file 0640, owner root, group `dockmesh`), adds `DOCKMESH_DATA_DIR` for the data root the install already uses, and replaces the old file with a symlink to the new one. The data stays where it is. It takes the env file named in `dockmesh.service`, else the first of the old default places; `--from <path>` names it instead. It does nothing when `/etc/dockmesh/dockmesh.env` already exists, and leaves the file in place while it still contains `DOCKMESH_SETUP_FORCE` (the setup wizard removes that line). On an error the old file stays as it was. The installer runs it on every upgrade, so you normally never call it yourself. Linux only.

### system write-unit

`sudo dockmesh system write-unit` writes `/etc/systemd/system/dockmesh.service` from the template built into the binary and reloads systemd. The service runs as user `dockmesh`, group `dockmesh`, with the `docker` group added (and the group of the Docker socket when that is not `docker`, as on Synology or with Podman; the socket comes from `DOCKER_HOST` when that names a unix socket), and loads `/etc/dockmesh/dockmesh.env` (an install whose config was not moved yet keeps its old env file; `--env-file <path>` names it). With `ProtectSystem=strict` it may only write to the data root, the stacks directory, the directories of the database and secrets when the path variables move them, every directory in `backups.local_roots` (`/srv/backups` and `<data directory>/backups` until the setting is saved), the audit archive directory and `backups.empty_path_dir`. A value that is not a clean absolute path, contains a control character, a quote or a backslash, lies in a system directory (`/etc`, `/usr`, `/proc`, `/run`, `/root` and the like) or contains the data root stops the command with an error and leaves the unit as it was; so does a binary that is not owned and writable only by root. The settings are read by a child process running as the `dockmesh` account. The unit it replaces is kept as `/etc/dockmesh/dockmesh.service.prev`; `--restore` puts it back. Local changes go into a drop-in in `/etc/systemd/system/dockmesh.service.d/`, which Dockmesh never writes or removes. The installer runs it on every install and upgrade; run it yourself after you add a directory to `backups.local_roots`, then `sudo systemctl restart dockmesh`. Linux only.

Commands that write files (`admin`, `enroll`, `db`, `ca`, `secrets`, `import`, `restore`) create them as the user that runs the command. On a systemd install the service runs as `dockmesh`, so `sudo -u dockmesh dockmesh <command>` keeps file ownership consistent.

Most commands open the database and apply pending migrations first. Loading the configuration creates `secrets.env` with a new JWT secret if it is missing, and commands that load the agent PKI (`ca export`, `doctor`, `enroll create`) create a CA if none exists yet.

## Command overview

| Command | Purpose |
|---|---|
| `serve` | Start the HTTP server and the agent mTLS listener (default) |
| `init` | Interactive first-run setup on the command line |
| `setup new-code` | Print a one-time code that opens the setup wizard |
| `restore` | Restore database, stacks and data directory from a system backup |
| `doctor` | Run local health checks |
| `ca export` / `ca rotate` / `ca reissue-server` | Export the agent CA, replace it, or add names to the agent listener certificate |
| `db migrate` / `db backup` | Apply migrations / write a SQLite snapshot |
| `admin create` / `reset-password` / `unlock` / `list-users` | Local user administration |
| `enroll create` / `revoke` / `list` | Agent enrollment |
| `import compose-dir` | Import a directory of compose projects as stacks |
| `config show` | Print the effective configuration |
| `secrets rotate` | Rotate the secrets key |
| `completion bash\|zsh\|fish` | Print a shell completion script |
| `version` | Print version, commit and build date (`--version` and `-v` also work) |
| `help` | Print the command list (`--help`, `-h`) |

An unknown subcommand prints the command list and exits with status 2. A failing command exits with status 1.

## serve

```bash
dockmesh serve --env-file /var/lib/dockmesh/dockmesh.env
```

Starts the web UI and REST API on `DOCKMESH_HTTP_ADDR` (default `:8080`) and the agent listener on `DOCKMESH_AGENT_LISTEN` (default `:8443`). The only flag is `--env-file`. On SIGINT or SIGTERM the server marks itself not ready, waits two seconds, then shuts down. The server never creates an account by itself. It starts in setup mode only on an install without accounts that has no env file, no database and no data, or whose env file still has `DOCKMESH_SETUP_FORCE=true`; any other install without accounts logs the `dockmesh admin create` command to run.

After a system restore was staged in the UI, the server exits with status 75 so the service manager starts it again; on that start it swaps the restored files in before it opens the database.

## init

```bash
sudo dockmesh init
sudo dockmesh init --yes --data-dir /var/lib/dockmesh --base-url https://dockmesh.example.com
```

A command-line alternative to the web setup wizard. It walks through six steps (data directory, listen address, admin user, base URL, agent URL, service), creates `<data-dir>/data` and `<data-dir>/stacks`, creates the admin user, writes `<data-dir>/dockmesh.env` (mode `0600`) and, unless disabled, installs and starts a service (systemd on Linux, launchd on macOS). Interactive mode needs a terminal; use `--yes` in scripts.

| Flag | Default | Meaning |
|---|---|---|
| `--yes` | `false` | Non-interactive: accept defaults, generate an 18-character admin password and print it once |
| `--data-dir` | `/var/lib/dockmesh` (Linux), `/usr/local/var/dockmesh` (macOS) | Data root |
| `--service` | `true` | Install, enable and start the OS service |
| `--systemd` | `true` | Deprecated alias; `--systemd=false` disables the service step |
| `--listen` | `:8080` | HTTP listen address, written as `DOCKMESH_HTTP_ADDR` |
| `--admin-user` | `admin` | Admin username |
| `--base-url` | `http://<hostname>:<port>` | Public URL users browse to |

The agent URL defaults to the base URL's host with `wss://`, port `8443` and path `/connect`. init writes it as `DOCKMESH_AGENT_PUBLIC_URL`, which then wins over the agent URL on the **Settings** page, and derives `DOCKMESH_AGENT_SANS` from both URLs and the hostname. If the admin user already exists, init leaves its password unchanged and says so.

init creates the `dockmesh` user if needed, adds it to the `docker` group, hands the data root to it and writes the unit the same way as [`system write-unit`](#system-write-unit).

## setup new-code

`sudo dockmesh setup new-code` prints a setup code, its expiry and the link to the wizard:

```
Setup code   K7QD-3MXA-9PRT
Valid until  16:05 (2 hours)
Open http://192.168.10.50:8080/setup#K7QD-3MXA-9PRT
```

The code is 12 characters of Crockford base32; case and dashes don't matter when you type it. It is in the link's fragment (after `#`), which browsers never send to the server, and the wizard fills the field from it without submitting. Only an argon2id hash of the code and its expiry are stored, in `/etc/dockmesh/setup-code.hash` (owner root, the group of the data directory, mode 0640; if `/etc/dockmesh` belongs to another group, the command lets others enter it without listing it, and the files in it stay 0640 or 0600); the code itself is never stored or logged and can't be shown again. Each run replaces the previous code and ends a setup session opened with it.

The command must run as root. It refuses on an install that is set up (`This server is already set up`); to get an admin there, use `dockmesh admin create`. It works only while the server runs in setup mode or before its first start on a host with no config, no database and no accounts. In a container, run it as root inside the container: `docker exec -u 0 <container> /app/dockmesh setup new-code`.

## restore

```bash
sudo systemctl stop dockmesh
sudo dockmesh restore --from ./dockmesh-system.tar.gz --dry-run
sudo dockmesh restore --from ./dockmesh-system.tar.gz.age --key ./backup-key.txt --force
sudo chown -R dockmesh:docker /var/lib/dockmesh
```

Restores the database, stacks directory and data directory from a system backup archive. Stop the server first. The archive is unpacked next to the live files and checked (the database opens and migrates, at least one admin exists); only then are the files swapped in. A restore that stops halfway finishes on the next start of the server. The replaced files are kept in `<data>/.pre-restore/<time>` and `<stacks>.pre-restore-<time>`. Run as root, the restored files belong to root, hence the `chown` afterwards.

| Flag | Default | Meaning |
|---|---|---|
| `--from` | (required) | Path to the system backup archive (`.tar.gz` or `.tar.gz.age`) |
| `--key` | | Backup key file exported from the original server, for encrypted archives |
| `--force` | `false` | Restore over a database that already has users |
| `--dry-run` | `false` | Count files and bytes, write nothing |

Without `--dry-run` the checks always run and their results are printed. System backups can also be restored from the UI and from the setup wizard of a fresh install. See [Disaster recovery](../operations/disaster-recovery.md).

## doctor

```bash
sudo -u dockmesh dockmesh doctor
```

Checks, in this order: the configuration loads (and which agent public URL it has), the database opens and answers (with the user count), the data directory exists (warning only), the Docker daemon answers (warning only), the agent CA and server certificate load, and the data directory is writable. Exits with status 1 if any check fails; warnings alone do not fail. No flags besides the global `--env-file`.

## ca

```bash
dockmesh ca export --out ./ca.pem
sudo -u dockmesh dockmesh ca reissue-server --add-san 203.0.113.10 --add-san dockmesh.example.com
sudo systemctl stop dockmesh
sudo -u dockmesh dockmesh ca rotate --reissue-all-agents
sudo systemctl start dockmesh
```

`ca export --out PATH` writes the agent CA certificate (public part only). `--out` is required.

`ca reissue-server` issues a new agent listener certificate (`agents-server.crt`/`.key`) signed by the existing CA. It keeps every name and IP of the current certificate and adds the ones you pass; the old files are kept as `agents-server.{crt,key}.bak-<time>`. Enrolled agents keep trusting the server, so nobody has to re-enroll. Use it when the server moves to a new address; restart the server afterwards and point the agents' `DOCKMESH_AGENT_URL` at the new address. `--help` prints the full moving procedure.

| Flag | Meaning |
|---|---|
| `--add-san NAME` | DNS name or IP address to add. Required; repeat it or pass a comma-separated list |

`ca rotate` renames `agents-ca.*` and `agents-server.*` to `*.old`, creates a new CA and server certificate (SANs from `DOCKMESH_AGENT_SANS`), and sets every agent to `pending` with a fresh enrollment token. It prints one `curl … --pinnedpubkey … /install/agent.sh | sudo bash -s -- --token …` line per agent, built from the agent URL in `dockmesh.env` or on the **Settings** page and pinned to the new listener key. Restart the server, then run that line on each agent host to re-enroll it.

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

`db migrate` applies pending migrations and reports the database path. `db backup --out PATH` writes a consistent snapshot with SQLite `VACUUM INTO`; it works while the server runs and refuses to overwrite an existing file. The snapshot holds the database only, not stacks or keys. The database still holds data that is not encrypted, such as plain global variables (and every stored secret when secrets encryption is off), so keep the file private (hence the `umask 077`).

## admin

```bash
sudo -u dockmesh dockmesh admin list-users
sudo -u dockmesh dockmesh admin create --username ci --role deployer
sudo -u dockmesh dockmesh admin reset-password --user admin
sudo -u dockmesh dockmesh admin unlock --user admin
```

| Subcommand | Flags | Effect |
|---|---|---|
| `create` | `--username` (required), `--email`, `--role` (default `viewer`), `--password` | Creates a local user with that role on everything. The role must exist: a built-in (`admin`, `host-admin`, `deployer`, `operator`, `viewer`) or the name of a custom role |
| `reset-password` | `--user` (username or ID, required), `--password` | Sets a new password and signs out all of the user's sessions |
| `unlock` | `--user` (username or ID, required) | Clears a login lockout and the failed-attempt counters; the password stays |
| `list-users` | none | Prints ID, username, assignments, email and MFA status |

When `--password` is omitted, the command prompts without echo, or reads the password from stdin when stdin is not a terminal. Prefer the prompt or stdin over `--password`, which ends up in shell history and `ps`. Passwords must meet the password policy. See [Permissions](../concepts/permissions.md) for the roles.

## enroll

```bash
sudo -u dockmesh dockmesh enroll create --name web-02
sudo -u dockmesh dockmesh enroll list
sudo -u dockmesh dockmesh enroll revoke --name web-02
```

`enroll create --name NAME` registers a host and prints its ID, the enrollment token and an install command. The install command pins the agent listener's public key. The token can be used once and expires after 1 hour, or after `DOCKMESH_AGENT_ENROLL_TOKEN_TTL` (a Go duration from `1m` to `720h`).

`enroll list` shows ID, name, status, hostname, agent version and last-seen time. `enroll revoke` takes `--name` or `--id` and deletes the agent record from the database. A running server is not told about it, so an agent that is connected at that moment stays connected until it reconnects; revoke under **Hosts** in the UI to close the connection right away. See [Hosts and agents](../concepts/hosts-and-agents.md).

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

Prints the env file it loaded (or `none`), then the effective paths, URLs and toggles after environment and env-file loading, with the JWT secret redacted.

## secrets rotate

```bash
sudo systemctl stop dockmesh
sudo -u dockmesh dockmesh secrets rotate
sudo systemctl start dockmesh
```

Generates a new `secrets.age-key` and re-encrypts every stack's `.env.age`, the credentials of saved backup targets and backup jobs, and secret global variables. The previous keys are kept in `secrets.age-key.history`, and data still encrypted with one of them stays readable; this covers SSO provider secrets, registry passwords, Git credentials and encrypted backups, which are not re-encrypted. Fails when `DOCKMESH_SECRETS_ENCRYPT_ENV=false`. A running server keeps the old key in memory, so stop it first or restart it right after. **Rotate key…** under **Settings → Encryption key** does the same on the live server without a restart.

## completion

```bash
dockmesh completion bash > /etc/bash_completion.d/dockmesh
dockmesh completion zsh > "${fpath[1]}/_dockmesh"
dockmesh completion fish > ~/.config/fish/completions/dockmesh.fish
```

The scripts complete subcommands; `init` and `ca reissue-server` are missing from them.

## dockmesh-agent

The agent is configured through environment variables, which `install-agent.sh` writes to `/etc/dockmesh-agent/agent.env`.

| Invocation | Effect |
|---|---|
| `dockmesh-agent` | Enroll on first start if no certificate exists, then connect to the server and reconnect after a drop |
| `dockmesh-agent --insecure-enroll` | Same, but allow enrollment over plain `http://` (lab setups only) |
| `dockmesh-agent status` | Print enrollment state, certificate subject and expiry, server URL, and whether a TLS handshake to the server succeeds |
| `dockmesh-agent version` | Print version, OS/arch, commit and build date (`--version`, `-v` also work) |

Any other argument is rejected with status 2. After a lost connection the agent retries at once, then backs off from one second doubling up to 60 seconds; it treats the server as gone after 90 seconds without traffic.

| Variable | Default | Meaning |
|---|---|---|
| `DOCKMESH_DATA_DIR` | `/var/lib/dockmesh-agent`, or `/var/lib/dockmesh` when the agent is enrolled there and not in the default | Certificate, key, CA, saved server URL, stack copies |
| `DOCKMESH_ENROLL_URL` | none | `https://<server>:8443/enroll` on the agent port; required for the first start |
| `DOCKMESH_TOKEN` | none | One-time enrollment token; required for the first start |
| `DOCKMESH_CA_FINGERPRINT` | none | SHA-256 of the agent CA; required to enroll over `https://` |
| `DOCKMESH_ENROLL_CA_FILE` | none | Agent CA certificate to check against the fingerprint; required to enroll over `https://` |
| `DOCKMESH_AGENT_URL` | value saved at enrollment | `wss://<server>:8443/connect`; overrides the saved URL |
| `DOCKMESH_INSECURE_ENROLL` | `false` | `true` has the same effect as `--insecure-enroll` |

The agent's client certificate is valid for 365 days. The agent renews it over its connection once less than 30 days are left (it checks every 12 hours), and the host keeps its ID. If the certificate has expired anyway, the agent logs how to re-enroll: issue a new enrollment token on the host page and run the installer again. Check the remaining time with `dockmesh-agent status`, which marks a certificate as expiring soon when less than 14 days are left.

The agent reads the `DOCKMESH_*` lines of `/etc/dockmesh-agent/agent.env` itself, for variables not already set and only when root owns the file and nobody else may write it. So `sudo dockmesh-agent status` finds the same data directory as the service. `status` uses only `DOCKMESH_DATA_DIR` and `DOCKMESH_AGENT_URL`.
