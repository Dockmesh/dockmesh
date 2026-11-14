---
title: Filesystem layout
description: Where the Dockmesh server, the agent and the installers put the database, keys, stacks and binaries.
---

Dockmesh keeps stack definitions as files and runtime state in one SQLite database. This page lists every path it uses, so you know what to back up, what to protect and what is safe to delete. All server paths come from environment variables, see [Environment variables](./environment-variables.md#paths).

## Server data root

`install.sh` and `dockmesh init` create the same layout under the data root (`DOCKMESH_DATA_DIR`). The default is `/var/lib/dockmesh` on Linux and `/usr/local/var/dockmesh` on macOS; `install.sh` reuses the data root of an earlier install when it finds its `dockmesh.env`. On Linux the config moves to `/etc/dockmesh/dockmesh.env` (root:dockmesh, 0640) on the first installer upgrade; the old place then holds a symlink to it.

```text
/var/lib/dockmesh/
├── dockmesh.env                 environment file loaded by the service (0600), or a symlink to /etc/dockmesh/dockmesh.env
├── data/                        data directory = directory of DOCKMESH_DB_PATH
│   ├── dockmesh.db              SQLite database (+ -wal, -shm while running)
│   ├── dockmesh.db.pre-migrate-<time>.bak   database snapshot taken before migrations (newest 3)
│   ├── secrets.env              DOCKMESH_JWT_SECRET=<hex> (0600)
│   ├── secrets.age-key          age identity for .env.age, encrypted DB fields and backups (0400)
│   ├── secrets.age-key.history  previous keys, after a key rotation (0400)
│   ├── audit-genesis.sha256     genesis hash of the audit chain (0400)
│   ├── agents-ca.crt            agent CA certificate
│   ├── agents-ca.key            agent CA private key (0400)
│   ├── agents-server.crt        certificate of the :8443 agent listener
│   ├── agents-server.key        its private key
│   ├── *.old                    previous CA and server cert, after `dockmesh ca rotate`
│   ├── git-cache/<stack>/       clones of Git-backed stacks
│   ├── backups/                 default directory of local backup targets
│   ├── .restore-staged/         a system restore waiting for the next start
│   └── .pre-restore/<time>/     data files replaced by a system restore
├── stacks/                      DOCKMESH_STACKS_ROOT
│   └── <stack>/
└── stacks.pre-restore-<time>/   stacks replaced by a system restore
```

What each file is for:

| File | Contains | If lost |
|---|---|---|
| `dockmesh.db` | Users, roles and assignments, sessions, hosts, deployments and history, backup jobs and targets, alert rules, settings, audit log, SSO providers, registries, API tokens, operations | Everything except stack files is gone |
| `dockmesh.db.pre-migrate-*.bak` | The database as it was before the last upgrades migrated it; see [Upgrade](../operations/upgrade.md#what-happens-on-the-first-start) | Nothing; you only lose that rollback point |
| `secrets.env` | JWT signing secret | All sessions end; users sign in again |
| `secrets.age-key` | Key for `.env.age` files, for encrypted DB fields (OIDC and OAuth2 client secrets, the LDAP bind password, registry passwords, Git credentials, backup target credentials, secret global variables) and for encrypted backups | Encrypted data cannot be recovered |
| `agents-ca.key` | Root of trust for agent mTLS | Every agent must re-enroll |
| `audit-genesis.sha256` | Anchor of the audit hash chain | The chain cannot be checked against its start |

Keep `secrets.age-key` somewhere outside the server as well: it is the only way to read encrypted backups. Copy the file, or download it under **Settings** → **Encryption key** → **Download key backup** (permission `backups.key_export`, the API is `GET /api/v1/system/backup-key/export`). See [Backups](../operations/backups.md).

Running `dockmesh serve` with no variables set uses `/var/lib/dockmesh`. `make dev` sets `DOCKMESH_DATA_DIR` to the checkout, so development data stays in `./data` and `./stacks`.

## Stack directory

Each stack is one directory under the stacks root. The name must be 2 to 63 characters of `a-z`, `0-9` and `-`, starting and ending with a letter or digit; `dockmesh`, `agent`, `system`, `api`, `admin` and `config` are reserved.

| File | Meaning |
|---|---|
| `compose.yaml` | The stack definition (0644) |
| `.env.age` | Stack variables, age-encrypted (when `DOCKMESH_SECRETS_ENCRYPT_ENV` is not `false`) |
| `.env` | Stack variables in plaintext (0600), when encryption is off |
| `compose.<env>.yaml` | Environment overlay, for example `compose.staging.yaml` |
| `.dockmesh.meta.json` | Dockmesh metadata such as the active environment |
| other files | Kept, for example files uploaded by `dmctl stacks adopt`, synced from Git, or written by containers through relative bind mounts |

Deploys do not run from this directory. Dockmesh writes `compose.yaml` and the decrypted `.env` to a temporary directory (`dockmesh-deploy-<stack>-*` under the system temp dir), parses the project there and deletes it afterwards. Relative bind mounts such as `./config:/etc/app` are pointed back at the stack directory, so `./config` means `<stacks root>/<stack>/config`. Containers deployed by older versions may still have bind mounts into a deleted temporary directory; the next deploy copies their data into the stack directory first, and scaling or rolling updates are refused until the stack has been redeployed. See [Stacks](../concepts/stacks.md).

## Backups

A local backup target without a path writes to `backups/` in the data directory (`/var/lib/dockmesh/data/backups` on an installer setup). An install that already had archives in `./data/backups` below the server's working directory keeps writing there; the directory is chosen once and saved as the `backups.empty_path_dir` setting.

Local targets can only write below allowed directories, the `backups.local_roots` setting. By default these are `/srv/backups` and `<data directory>/backups`; on upgrade, directories that existing jobs already used are added, and admins get a notice to review them. A path outside every allowed directory is refused. The `dockmesh` service user must be able to write the target directory, and the unit only lets the service write to the allowed directories it knew when it was written: after adding one, run `sudo dockmesh system write-unit` and restart the service.

Archives are written as `jobs/<job id>-<job name>/<timestamp>-r<run id>.tar.gz`, with `.age` appended when encrypted. Archives of older versions stay where they are and are still managed there. A system backup archive contains `dockmesh.db`, `stacks/...` and `data/...`; it leaves out `data/backups/`, database snapshots and the staged and replaced files of a system restore. See [Backups](../operations/backups.md).

## Installed files

| Path | Installed by | Purpose |
|---|---|---|
| `/usr/local/bin/dockmesh` | `install.sh` (`DOCKMESH_INSTALL_DIR`) | Server binary |
| `/usr/local/bin/dockmesh.bak` | `install.sh` on upgrade | Previous binary, for rollback |
| `/usr/local/bin/dmctl` | `install.sh` | CLI client |
| `/usr/local/bin/grype` | `install.sh` unless `DOCKMESH_NO_SCANNER=1` | Vulnerability scanner |
| `/usr/local/share/dockmesh/install-agent.sh` | `install.sh` (`DOCKMESH_ASSET_DIR`) | Served at `/install/agent.sh` on the agent port |
| `/usr/local/share/dockmesh/bin/dockmesh-agent-linux-amd64`, `dockmesh-agent-linux-arm64` | `install.sh` | Agent binaries, served at `/install/{name}` and to agent upgrades (`DOCKMESH_BINARY_DIR`) |
| `/etc/systemd/system/dockmesh.service` | `dockmesh system write-unit`, run by `install.sh` and `dockmesh init` | Service unit, user `dockmesh`, group `dockmesh` plus `docker`; rewritten on every upgrade |
| `/etc/systemd/system/dockmesh.service.d/*.conf` | you | Local changes to the unit; Dockmesh never writes or removes them |
| `/etc/dockmesh/dockmesh.service.prev` | `dockmesh system write-unit` | The unit the last write replaced (`write-unit --restore`) |
| `/etc/systemd/system/dockmesh-update.path`, `dockmesh-update.service` | `dockmesh system write-unit` | Root updater: the path unit starts the oneshot `dockmesh update apply` when the server writes an update request; rewritten on every upgrade |
| `/var/lib/dockmesh-update/` | `dockmesh update apply` | Root-only working directory of the updater: the verified copy of the release archive, its journal and the files it replaced, until the update is confirmed |
| `/usr/local/bin/dockmesh.prev` | `dockmesh update apply` | The binary the last update replaced |
| `<data directory>/update/` | the server | Release files the server downloaded and the request for the updater; the updater writes `pending` while an update runs and `result.json` when it ends |
| `<database>.pre-update.bak` | `dockmesh update apply` | Database as it was before the last update, put back on a rollback |
| `/Library/LaunchDaemons/dev.dockmesh.service.plist` | macOS | launchd job; logs in `/usr/local/var/log/dockmesh.log` and `.err` |

Linux release archives contain the agent for both architectures, and `install.sh` installs both, whatever the server's own architecture. On macOS, the archive has one agent binary, which `install.sh` installs as `dockmesh-agent-darwin-<arch>`; the agent installer only enrolls Linux hosts and needs the Linux binaries, so copy them from a Linux archive into the asset `bin/` directory if a macOS server should enroll agents.

The server writes its log to stdout and stderr, so on Linux read it with `journalctl -u dockmesh`.

## Agent hosts

`install-agent.sh` (Linux with systemd only) picks the `amd64` or `arm64` agent from `uname -m` and creates:

| Path | Purpose |
|---|---|
| `/var/lib/dockmesh-agent/` | Agent data directory (`DOCKMESH_DATA_DIR`, 0700, owned by `dockmesh-agent` or root with `--as-root`) |
| `/var/lib/dockmesh-agent/agent.crt`, `agent.key` | Client certificate (365 days, renewed over the connection when less than 30 days are left) and key |
| `/var/lib/dockmesh-agent/ca.crt` | Server CA received at enrollment |
| `/var/lib/dockmesh-agent/enroll-ca.pem` | Server CA written by the installer, used to check the server during enrollment |
| `/var/lib/dockmesh-agent/agent.url` | Saved `wss://` server URL |
| `/var/lib/dockmesh-agent/bin/dockmesh-agent` | Agent binary; self-upgrades replace it in place |
| `/var/lib/dockmesh-agent/stacks/<stack>/` | Mirror of stacks deployed to this host: `compose.yaml` and `.dockmesh.meta.json`, plus relative bind-mount data |
| `/var/lib/dockmesh-agent/staging/` | Temporary deploy directories |
| `/usr/local/bin/dockmesh-agent` | Root-owned copy of the binary, refreshed at every service start |
| `/etc/dockmesh-agent/agent.env` | Environment file, including the enrollment token (0640) |
| `/etc/dockmesh-agent/refresh-path-copy` | Script the service runs to refresh the copy in `/usr/local/bin` |
| `/etc/systemd/system/dockmesh-agent.service` | Service unit |

Agents installed by older releases used `/var/lib/dockmesh` as their data directory, the same as the server default. A re-run of the installer keeps the data directory named in `agent.env`, but moves the agent's files out of a directory that belongs to a Dockmesh server. The agent binary itself falls back to `/var/lib/dockmesh` when `DOCKMESH_DATA_DIR` is not set.

Re-running the installer with a new token removes the old certificate, key, CA files, URL file and `staging/` first.

## Reverse proxy volumes

When the reverse proxy is enabled, the `dockmesh-proxy` container stores Caddy's state in two named Docker volumes: `dockmesh-caddy-data` (certificates, ACME account) and `dockmesh-caddy-config`. They live with your other Docker volumes, not in the Dockmesh data root.
