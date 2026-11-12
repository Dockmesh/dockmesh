---
title: dockmesh and dockmesh-agent commands
description: Every subcommand and flag of the dockmesh server binary and the dockmesh-agent binary.
---

The `dockmesh` binary is both the server and its admin tool. Run without arguments (or with `serve`) it starts the server; every other subcommand runs once on the server host and exits. These commands work directly on the database and data directory, so they need no API token. For remote, token-based work use [dmctl](./dmctl.md).

## How commands find their configuration

Every subcommand reads the same `DOCKMESH_*` environment variables as the server (see [Environment variables](./environment-variables.md)). There are three ways to supply them:

- `--env-file PATH` (or `--env-file=PATH`), accepted anywhere on the command line for every subcommand. The file holds `KEY=VALUE` lines; `#` comments and blank lines are skipped, surrounding quotes are stripped.
- If you pass no `--env-file` and `DOCKMESH_DB_PATH` is not set, Dockmesh loads the first of `/var/lib/dockmesh/dockmesh.env`, `/data/dockmesh.env` and `/usr/local/var/dockmesh/dockmesh.env` that exists. On Linux and macOS it only does so when the file belongs to root, to you or to the `dockmesh` user and is not writable by group or others; otherwise it prints `not loading <file>` and goes on without it.
- Variables already set in your shell always win over values from the file.

If your data root lives elsewhere, pass `--env-file <data-root>/dockmesh.env` explicitly.

Commands that write files (`admin`, `enroll`, `db`, `ca`, `secrets`, `import`, `restore`) create them as the user that runs the command. On a systemd install the service runs as `dockmesh`, so `sudo -u dockmesh dockmesh <command>` keeps file ownership consistent.

Most commands open the database and apply pending migrations first. Loading the configuration creates `secrets.env` with a new JWT secret if it is missing, and commands that load the agent PKI (`ca export`, `doctor`, `enroll create`) create a CA if none exists yet.

## Command overview

| Command | Purpose |
|---|---|
| `serve` | Start the HTTP server and the agent mTLS listener (default) |
| `init` | Interactive first-run setup on the command line |
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

Starts the web UI and REST API on `DOCKMESH_HTTP_ADDR` (default `:8080`) and the agent listener on `DOCKMESH_AGENT_LISTEN` (default `:8443`). The only flag is `--env-file`. On SIGINT or SIGTERM the server marks itself not ready, waits two seconds, then shuts down. If the database has no users at all and `DOCKMESH_SETUP_FORCE` is not set, the server creates an `admin` user with a random password and prints the password once to the console (stderr), not to the structured log. The console output stays in the journal or the container log, so change the password after the first sign-in.

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

The systemd unit init writes runs as user `dockmesh`, group `docker`, with `ProtectSystem=strict` and write access limited to the data root and `/var/run/docker.sock`. init creates the `dockmesh` user if needed, adds it to the `docker` group and hands the data root to it. A copy of the unit is kept at `<data-dir>/dockmesh.service`.

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
| `DOCKMESH_DATA_DIR` | `/var/lib/dockmesh` | Certificate, key, CA, saved server URL, stack copies |
| `DOCKMESH_ENROLL_URL` | none | `https://<server>:8443/enroll` on the agent port; required for the first start |
| `DOCKMESH_TOKEN` | none | One-time enrollment token; required for the first start |
| `DOCKMESH_CA_FINGERPRINT` | none | SHA-256 of the agent CA; required to enroll over `https://` |
| `DOCKMESH_ENROLL_CA_FILE` | none | Agent CA certificate to check against the fingerprint; required to enroll over `https://` |
| `DOCKMESH_AGENT_URL` | value saved at enrollment | `wss://<server>:8443/connect`; overrides the saved URL |
| `DOCKMESH_INSECURE_ENROLL` | `false` | `true` has the same effect as `--insecure-enroll` |

The agent's client certificate is valid for 365 days. The agent renews it over its connection once less than 30 days are left (it checks every 12 hours), and the host keeps its ID. If the certificate has expired anyway, the agent logs how to re-enroll: issue a new enrollment token on the host page and run the installer again. Check the remaining time with `dockmesh-agent status`, which marks a certificate as expiring soon when less than 14 days are left.

`dockmesh-agent status` reads only `DOCKMESH_DATA_DIR` and `DOCKMESH_AGENT_URL` from the environment. Under systemd, run it with the same environment file:

```bash
sudo bash -c 'set -a; . /etc/dockmesh-agent/agent.env; dockmesh-agent status'
```
