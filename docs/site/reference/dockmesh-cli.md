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

`sudo dockmesh system migrate-layout` moves the config of an install from before 0.6 to `/etc/dockmesh/dockmesh.env` (directory 0750, file 0640, owner root, group `dockmesh`), adds `DOCKMESH_DATA_DIR` for the data root the install already uses, and replaces the old file with a symlink to the new one. The data stays where it is. It takes the env file named in `dockmesh.service`, else the first of the old default places; `--from <path>` names it instead. `--checkout <dir>` is for a source run that uses `<dir>/data/dockmesh.db` without config: it writes `/etc/dockmesh/dockmesh.env` (same owner, group and modes as above) with only `DOCKMESH_DATA_DIR=<dir>`; nothing in the checkout is moved or changed. It refuses when an env file exists at one of the old places, and when the checkout fails the ownership check of [Filesystem layout](./filesystem-layout.md#server-data-root) for root (so the server must run as root or in the `dockmesh` group to read the file). The installer never passes `--checkout`. It does nothing when `/etc/dockmesh/dockmesh.env` already exists, and leaves the file in place while it still contains `DOCKMESH_SETUP_FORCE` (the setup wizard removes that line). On an error the old file stays as it was. The installer runs it on every upgrade, so you normally never call it yourself. Linux only.

### system write-unit

`sudo dockmesh system write-unit` writes `/etc/systemd/system/dockmesh.service` from the template built into the binary and reloads systemd. The service runs as user `dockmesh`, group `dockmesh`, with the `docker` group added (and the group of the Docker socket when that is not `docker`, as on Synology or with Podman; the socket comes from `DOCKER_HOST` when that names a unix socket), and loads `/etc/dockmesh/dockmesh.env` (an install whose config was not moved yet keeps its old env file; `--env-file <path>` names it). With `ProtectSystem=strict` it may only write to the data root, the stacks directory, the directories of the database and secrets when the path variables move them, every directory in `backups.local_roots` (`/srv/backups` and `<data directory>/backups` until the setting is saved), the audit archive directory and `backups.empty_path_dir`. A value that is not a clean absolute path, contains a control character, a quote or a backslash, lies in a system directory (`/etc`, `/usr`, `/proc`, `/run`, `/root` and the like) or contains the data root stops the command with an error and leaves the unit as it was; so does a binary that is not owned and writable only by root. The settings are read by a child process running as the `dockmesh` account. The unit it replaces is kept as `/etc/dockmesh/dockmesh.service.prev`; `--restore` puts it back. Local changes go into a drop-in in `/etc/systemd/system/dockmesh.service.d/`, which Dockmesh never writes or removes. The installer runs it on every install and upgrade; after you add a directory to `backups.local_roots`, **Allow writing** on the Backups page has the root updater run it and restart the service; without the root updater, run it yourself, then `sudo systemctl restart dockmesh`. Linux only.

### system snapshot-db

`sudo dockmesh system snapshot-db` writes the snapshot of the database the root updater takes before an update, `dockmesh.db.pre-update.bak` next to the database, through a child process running as `dockmesh`. Run it with the service stopped. The installer runs it on an upgrade, with the new binary, before it replaces the old one. Linux only.

### system unit-groups

`dockmesh system unit-groups` prints the supplementary groups `system write-unit` gives the service, for example `docker, dmsock (Docker socket)`.

### update apply

`dockmesh update apply --data-dir <dir>` installs a release staged in `<data directory>/update/` and is run as root by `dockmesh-update.service`; you don't run it yourself. `system write-unit` writes that service and `dockmesh-update.path`, which starts it as soon as `<data directory>/update/request` appears, and enables both (the service also runs once at boot, to finish an update a crash or power cut interrupted). Everything in `<data directory>/update/` is treated as untrusted:

- Only the target version is read from `request`, and the request is removed. A version that is not newer than the installed one is refused, so the updater never downgrades (a release candidate counts as older than its release).
- `request`, `checksums.txt`, `checksums.txt.minisig` and the archive must be regular files with a single link; symlinks, FIFOs, devices, directories and hard links are refused, and so are files above a size limit.
- The minisign signature of `checksums.txt` is checked against the keys built into the installed binary, including the tag in its trusted comment. The archive is copied to `/var/lib/dockmesh-update/` (root only) and its SHA-256 is checked on that copy; the files that get installed are taken from that copy.

A request of type `setup-apply` finishes the setup of a new install instead; the wizard writes it. The updater runs it only when it finds, on its own, no config at any location, no database and no accounts, and refuses and logs it otherwise. It reads what the wizard entered from `<data directory>/.setup/apply.json` (the admin password only as an argon2id hash), checks every value, and creates the data directory the wizard chose (every directory above it must belong to root; an existing one only if it belongs to `dockmesh` and holds nothing but what setup leaves). It writes the new config to a temporary file in `/etc/dockmesh`, runs `dockmesh serve --check-config` with it as `dockmesh`, and only then has a child running as `dockmesh` create the database, the admin and the settings and copy the setup certificate into the data directory. The config is renamed to `/etc/dockmesh/dockmesh.env`, the unit is rewritten and the service restarted. A failed check changes nothing. If the new start does not answer healthy within 180 seconds, the config and every file the start created are renamed with a `.failed-<time>` suffix (never deleted), the old unit comes back and the server starts in setup mode again. The outcome is written to `<data directory>/update/setup-result.json`; `<data directory>/.setup/` is removed after a healthy start.

For an update it stops `dockmesh.service`, writes a snapshot of the database next to it (`dockmesh.db.pre-update.bak`, made by a child process running as `dockmesh`), keeps the old binary as `/usr/local/bin/dockmesh.prev`, installs the new `dockmesh`, `dmctl`, agent binaries and `install-agent.sh`, runs the new binary's `system write-unit`, and starts the service. If the new version does not answer `/api/v1/health` with its version within 180 seconds, or any step fails, it puts back the binaries, the units and the database snapshot and starts the old version again. The outcome is written to `<data directory>/update/result.json`. Changes made in Dockmesh while the new version was running, at most those 180 seconds, are lost on such a rollback, audit entries included. Linux only.

Commands that write files (`admin`, `enroll`, `db`, `ca`, `secrets`, `import`, `restore`) create them as the user that runs the command. On a systemd install the service runs as `dockmesh`, so `sudo -u dockmesh dockmesh <command>` keeps file ownership consistent.

Most commands open the database and apply pending migrations first (`doctor`, `admin`, `ca`, `enroll` and `db`; `config show` and `secrets rotate` do not), with the same pre-migration snapshot as the server. Don't run a newer binary's commands against an install that still runs an older version: they migrate its database under it. Loading the configuration creates `secrets.env` with a new JWT secret if it is missing, and commands that load the agent PKI (`ca export`, `doctor`, `enroll create`) create a CA if none exists yet.

## Command overview

| Command | Purpose |
|---|---|
| `serve` | Start the HTTP server and the agent mTLS listener (default) |
| `init` | Deprecated wrapper on `setup apply` |
| `setup apply` / `setup example` | Set up a new install from a setup file / print a template |
| `setup new-code` | Print a one-time code that opens the setup wizard |
| `setup status` | Say whether the setup is still unfinished |
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

Starts the web UI and REST API on `DOCKMESH_HTTP_ADDR` (default `:8080`, over HTTPS or HTTP as `DOCKMESH_TLS_MODE` says) and the agent listener on `DOCKMESH_AGENT_LISTEN` (default `:8443`). The flags are `--env-file` and `--check-config`, which only checks the config (TLS mode, listen address, a data directory the account can write, readable certificate files) without writing anything and exits 0 or 1. A new install runs setup mode over HTTPS with the certificate in `<data directory>/.setup/`, and keeps everything it creates there until setup is applied, so a restart before that comes back into setup mode. On SIGINT or SIGTERM the server marks itself not ready, waits two seconds, then shuts down. The server never creates an account by itself. It starts in setup mode only on an install without accounts that has no env file, no database and no data, or whose env file still has `DOCKMESH_SETUP_FORCE=true`; any other install without accounts logs the `dockmesh admin create` command to run.

After a system restore was staged in the UI, the server exits with status 75 so the service manager starts it again; on that start it swaps the restored files in before it opens the database.

## init

`dockmesh init` is deprecated and will be removed in 0.7.0; every run prints a warning. It is a thin wrapper on [`setup apply`](#setup-apply) and asks no questions any more:

```bash
sudo dockmesh init --file /root/dockmesh-setup.yaml
sudo dockmesh init --yes --data-dir /var/lib/dockmesh --base-url https://dockmesh.example.com:8080
```

`--file` is the same as `setup apply --file`. `--yes` builds the setup from `--data-dir`, `--listen`, `--admin-user` (default `admin`) and `--base-url`, generates a 20-character admin password, writes it to a new file (`--password-file`, default `/root/dockmesh-admin-password` as root, mode 0600; the file must not exist yet), applies it exactly like a setup file and prints the file's path, never the password. `--service` and `--systemd` are accepted and ignored. Without `--yes` or `--file` it stops with an error.

## setup apply

`sudo dockmesh setup apply --file <setup file>` sets up a new server without the browser, from a YAML file with the wizard's answers: a new installation (`admin`) or a restore of a system backup (`restore`). `dockmesh setup example` prints an annotated template. It is the same commit as the wizard's: on Linux as root it checks the server was never set up, checks the values and the data directory (setup creates it; an existing one must be empty and belong to the `dockmesh` account), runs `serve --check-config` as `dockmesh`, creates the database and the admin account (or restores the backup, then sets `address.base_url` if the file has one) as `dockmesh`, writes `/etc/dockmesh/dockmesh.env` and the unit, starts and enables the service and waits for it to answer, and puts everything back if a step fails. It refuses on a server that is set up.

```yaml
data_dir: /var/lib/dockmesh
admin:
  username: admin
  email: you@example.com
  password_file: /root/dockmesh-admin-password
address:
  base_url: https://dockmesh.example.com:8080
tls:
  mode: internal
```

Secrets are never written into the file: the admin password is `admin.password_file` (a file with the password alone, at least 12 characters) or `admin.password_hash` (argon2id); a restore names `restore.backup_file` and, for an encrypted backup, the exported key in `restore.key_file`. Unknown keys, such as a plain `password:`, are refused. When the setup file or one of the files it names is readable by group or others, the command warns, for example `warning  /root/dockmesh-setup.yaml is readable by other users (mode 0644). Run: chmod 600 /root/dockmesh-setup.yaml`, and continues. A file the setup file names is called by its key (`the file admin.password_file names is readable by other users. Run chmod 600 on it`).

| Key | Default | What it sets |
|---|---|---|
| `data_dir` | `/var/lib/dockmesh` | The data directory. |
| `stacks_dir` | `<data_dir>/stacks` | The stacks directory, checked like in the wizard: outside the data directory setup creates only a directory named `stacks` or `dockmesh-stacks…` directly in `/srv`, `/opt`, `/data`, `/mnt` or `/media` (or a disk mounted below one of them); anywhere else it must be an empty directory of the `dockmesh` account below root-owned directories. |
| `admin.username`, `admin.email` | `admin`, none | The first admin account. |
| `admin.password_file`, `admin.password_hash` | | The admin password, exactly one of them. |
| `address.base_url` | `<first IP>:8080` | The address users and agents use. |
| `address.http_addr` | `:8080` (`127.0.0.1:8080` with `tls.mode: proxy` or `caddy`) | The web listener. |
| `address.agent_port` | `8443` | The agent listener's port; it must differ from the web port. |
| `address.agent_url` | derived from `base_url` and the agent port | The `wss://` URL agents connect to, saved as the **Agent public URL** setting. |
| `tls.mode` | `internal` | `internal`, `file`, `caddy` or `proxy`. |
| `tls.cert_file`, `tls.key_file` | | Your own certificate and key, with `mode: file`. A pair that does not match or an expired certificate is refused. |
| `tls.trusted_proxies` | the listen address | With `mode: proxy`: addresses or networks of your proxy, comma separated; a network wider than /8 (IPv6 /32) is refused. |
| `restore.backup_file`, `restore.key_file` | | A system backup to restore instead of `admin`. |

`--dry-run` checks the file and the host and changes nothing. `--porcelain` prints `key=value` lines (`warning`, `url`, `fingerprint`, `admin`; `check=ok` with `--dry-run`) for the installer. As root, the setup file and every file it names must be regular files owned by root and, like their directories, writable only by root; a symlink is refused. Applies are serialized with the wizard's, and a failed apply puts everything back and leaves `dockmesh.service` stopped. Without root or outside Linux (a container, a source build, `make dev SETUP_FILE=<file>`) it sets up the data directory of `DOCKMESH_DATA_DIR` in place and writes no config or unit; `address.http_addr`, `address.agent_port` and `tls` then come from the environment, and a `stacks_dir` other than the server's own is refused.

## setup new-code

`sudo dockmesh setup new-code` prints a setup code, its expiry and the link to the wizard:

```
Setup code   K7QD-3MXA-9PRT
Valid until  16:05 (2 hours)
Open https://192.168.10.50:8080/setup#K7QD-3MXA-9PRT
Certificate  SHA-256 63:EC:84:70:…:E4:FF
```

On a new install the link is `https://` and the last line is the SHA-256 fingerprint of the certificate setup mode serves; your browser warns about that certificate, so compare the fingerprint it shows before you go on. The wizard shows the same fingerprint on its first step.

The code is 12 characters of Crockford base32; case and dashes don't matter when you type it. It is in the link's fragment (after `#`), which browsers never send to the server, and the wizard fills the field from it without submitting. Only an argon2id hash of the code and its expiry are stored, in `/etc/dockmesh/setup-code.hash` (owner root, the group of the data directory, mode 0640; if `/etc/dockmesh` belongs to another group, the command lets others enter it without listing it, and the files in it stay 0640 or 0600); the code itself is never stored or logged and can't be shown again. Each run replaces the previous code and ends a setup session opened with it.

The command must run as root. It refuses on an install that is set up (`This server is already set up`); to get an admin there, use `dockmesh admin create`. It works only while the server runs in setup mode or before its first start on a host with no config, no database and no accounts. In a container, run it as root inside the container: `docker exec -u 0 <container> /app/dockmesh setup new-code`.

`--porcelain` prints the same as `key=value` lines (`code`, `valid_until`, `url`, `fingerprint`, `replaced`); the installer reads that form.

## setup status

`sudo dockmesh setup status` says whether the server still waits for its setup and, if so, where the wizard is. It writes nothing and never creates a code. `--porcelain` prints `setup=unfinished` and `url=…`, or `setup=done`. The installer runs it after an upgrade.

## restore

```bash
sudo systemctl stop dockmesh
sudo dockmesh restore --from ./dockmesh-system.tar.gz --dry-run
sudo dockmesh restore --from ./dockmesh-system.tar.gz.age --key ./backup-key.txt --force
```

Restores the database, stacks directory and data directory from a system backup archive. Stop the server first. The archive is unpacked next to the live files and checked (the database opens and migrates, at least one admin exists); only then are the files swapped in. A restore that stops halfway finishes on the next start of the server. The replaced files are kept in `<data>/.pre-restore/<time>` and `<stacks>.pre-restore-<time>`. Files and directories keep their mode from the archive. Run as root, the old server's files get the owner of the data and stacks directories they replace; stack data a container wrote as another user keeps its owner (without root, through a helper container, so Docker must be running).

| Flag | Default | Meaning |
|---|---|---|
| `--from` | (required) | Path to the system backup archive (`.tar.gz` or `.tar.gz.age`) |
| `--key` | | Backup key file exported from the original server, for encrypted archives |
| `--force` | `false` | Restore over a database that already has users |
| `--dry-run` | `false` | Count files and bytes, write nothing |
| `--base-url` | | Base URL of the restored server instead of the one in the backup; the agent URL follows unless the backup has one set by hand. `setup apply` passes `address.base_url` here |

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
| `sudo dockmesh-agent system install [--data-dir <dir>] [--stacks-dir <dir>] [--user dockmesh-agent\|root] [--socket-gid <gid>]` | Set up the managed layout: copy this binary to `/usr/local/bin/dockmesh-agent` (root, 0755), write `/etc/dockmesh-agent/agent.env` and the units `dockmesh-agent.service`, `dockmesh-agent-update.path` and `dockmesh-agent-update.service` from the template built into the binary, then enable them. It does not start the agent. Values not given keep what the host has: the data dir is chosen at the first install only, an agent that runs as root stays root, and `--socket-gid` is kept in agent.env as `DOCKMESH_SOCKET_GID`. The account `dockmesh-agent` and its group of the same name must exist; an existing data or stacks dir must belong to the account, and a new `--stacks-dir` must pass the checks of [Changing the stacks directory](../features/multi-host.md#changing-the-stacks-directory). A re-run with the same values changes no file; drop-ins in `dockmesh-agent.service.d/` are never touched. It refuses a host where the legacy layout's copy step (`/etc/dockmesh-agent/refresh-path-copy`, `<data>/bin/dockmesh-agent` or an `ExecStartPre`) is still there ([Upgrading agents](../operations/upgrade.md#root-updater-units-of-a-managed-agent)) |
| `sudo dockmesh-agent update apply --data-dir <dir>` | Root updater of a managed agent, run by `dockmesh-agent-update.service`: switches to a new stacks directory when the agent asks for one, otherwise installs the release the agent staged in `<dir>/update/` after checking its signature again, restarts the agent and rolls back to `dockmesh-agent.prev` when the new agent does not connect within 180 seconds ([Upgrading agents](../operations/upgrade.md#upgrading-agents)) |

Any other argument is rejected with status 2. After a lost connection the agent retries at once, then backs off from one second doubling up to 60 seconds; it treats the server as gone after 90 seconds without traffic.

| Variable | Default | Meaning |
|---|---|---|
| `DOCKMESH_DATA_DIR` | `/var/lib/dockmesh-agent`, or `/var/lib/dockmesh` when the agent is enrolled there and not in the default | Certificate, key, CA, saved server URL, stack copies |
| `DOCKMESH_ENROLL_URL` | none | `https://<server>:8443/enroll` on the agent port; required for the first start |
| `DOCKMESH_TOKEN` | none | One-time enrollment token for the first start when `<data dir>/enroll-token` is missing |
| `DOCKMESH_CA_FINGERPRINT` | none | SHA-256 of the agent CA; required to enroll over `https://` |
| `DOCKMESH_ENROLL_CA_FILE` | none | Agent CA certificate to check against the fingerprint; required to enroll over `https://` |
| `DOCKMESH_AGENT_URL` | value saved at enrollment | `wss://<server>:8443/connect`; overrides the saved URL |
| `DOCKMESH_INSECURE_ENROLL` | `false` | `true` has the same effect as `--insecure-enroll` |

The agent's client certificate is valid for 365 days. The agent renews it over its connection once less than 30 days are left (it checks every 12 hours), and the host keeps its ID. If the certificate has expired anyway, the agent logs how to re-enroll: issue a new enrollment token on the host page and run the installer again. Check the remaining time with `dockmesh-agent status`, which marks a certificate as expiring soon when less than 14 days are left.

The agent reads the `DOCKMESH_*` lines of `/etc/dockmesh-agent/agent.env` itself, for variables not already set and only when root owns the file and nobody else may write it. So `sudo dockmesh-agent status` finds the same data directory as the service. `status` uses only `DOCKMESH_DATA_DIR` and `DOCKMESH_AGENT_URL`.
