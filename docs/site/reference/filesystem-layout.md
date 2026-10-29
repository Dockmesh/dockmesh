---
title: Filesystem layout
description: Where the Dockmesh server, the agent and the installers put the database, keys, stacks and binaries.
---

Dockmesh keeps stack definitions as files and runtime state in one SQLite database. This page lists every path it uses, so you know what to back up, what to protect and what is safe to delete. All server paths come from environment variables, see [Environment variables](./environment-variables.md#paths).

## Server data root

`install.sh` and `dockmesh init` create the same layout under the data root (`/var/lib/dockmesh` on Linux, `/usr/local/var/dockmesh` on macOS, `/data` when `install.sh` finds that directory):

<!-- update after: fs: contain paths in volume browse, restore and git sources -->
```text
/var/lib/dockmesh/
├── dockmesh.env                 environment file loaded by the service (0600)
├── dockmesh.service             copy of the systemd unit (dockmesh init only)
├── data/                        data directory = directory of DOCKMESH_DB_PATH
│   ├── dockmesh.db              SQLite database (+ -wal, -shm while running)
│   ├── secrets.env              DOCKMESH_JWT_SECRET=<hex> (0600)
│   ├── secrets.age-key          age identity for .env.age and stored credentials (0400)
│   ├── secrets.age-key.old      previous key, after a key rotation
│   ├── audit-genesis.sha256     genesis hash of the audit chain (0400)
│   ├── agents-ca.crt            agent CA certificate
│   ├── agents-ca.key            agent CA private key
│   ├── agents-server.crt        certificate of the :8443 agent listener
│   ├── agents-server.key        its private key
│   ├── *.old                    previous CA and server cert, after `dockmesh ca rotate`
│   └── git-cache/<stack>/       clones of Git-backed stacks
└── stacks/                      DOCKMESH_STACKS_ROOT
    └── <stack>/
```

What each file is for:

| File | Contains | If lost |
|---|---|---|
| `dockmesh.db` | Users, roles, sessions, hosts, deployments and history, backup jobs, alert rules, settings, audit log, SSO providers, registries, API tokens | Everything except stack files is gone |
| `secrets.env` | JWT signing secret | All sessions end; users sign in again |
| `secrets.age-key` | Key for `.env.age` files and encrypted DB fields (OIDC and OAuth2 client secrets, the LDAP bind password, registry passwords, Git credentials) and for encrypted backups | Encrypted data cannot be recovered |
| `agents-ca.key` | Root of trust for agent mTLS | Every agent must re-enroll |
| `audit-genesis.sha256` | Anchor of the audit hash chain | The chain cannot be checked against its start |

Keep `secrets.age-key` somewhere outside the server as well: it is the only way to read encrypted backups. You can copy the file, or download it with `GET /api/v1/system/backup-key/export` (permission `backups.restore`); the **Export key** link on the **Settings** page does not send your token and fails with 401. See [Backups](../operations/backups.md).

Running `dockmesh serve` with no variables set uses `./data/...` and `./stacks` relative to the working directory.

## Stack directory

<!-- update after: backups: scope hooks, limit targets, check sftp keys; fix bind mounts -->
Each stack is one directory under the stacks root. The name must be 2 to 63 characters of `a-z`, `0-9` and `-`, starting and ending with a letter or digit; `dockmesh`, `agent`, `system`, `api`, `admin` and `config` are reserved.

| File | Meaning |
|---|---|
| `compose.yaml` | The stack definition (0644) |
| `.env.age` | Stack variables, age-encrypted (when `DOCKMESH_SECRETS_ENCRYPT_ENV` is not `false`) |
| `.env` | Stack variables in plaintext (0600), when encryption is off |
| `compose.<env>.yaml` | Environment overlay, for example `compose.staging.yaml` |
| `.dockmesh.meta.json` | Dockmesh metadata such as the active environment |
| other files | Kept, for example files uploaded by `dmctl stacks adopt` or synced from Git |

Deploys do not run from this directory. Dockmesh writes `compose.yaml` and the decrypted `.env` to a temporary directory (`dockmesh-deploy-<stack>-*` under the system temp dir), parses the project there and deletes it afterwards. Relative bind mounts such as `./config:/etc/app` therefore point at that temporary directory, not at the stack directory. Use absolute host paths or named volumes for anything that must persist. See [Stacks](../concepts/stacks.md).

## Backups

The default local backup target path is `./data/backups`, relative to the server's working directory. The systemd units written by `install.sh` and `dockmesh init` set no `WorkingDirectory`, so on those installs this resolves to `/data/backups`, outside the data root. Set an absolute path (for example `/var/lib/dockmesh/data/backups`) on local targets. A system backup includes the whole data directory, so a backup target inside it makes every system backup contain the previous ones. See [Backups](../operations/backups.md).

<!-- update after: backups: keep retention per job, fail on partial or missing data -->
Archives are written as `jobs/<job-name>/<timestamp>-r<run-id>.tar.gz`, with `.age` appended when encrypted. A system backup archive contains `dockmesh.db`, `stacks/...` and `data/...`.

## Installed files

| Path | Installed by | Purpose |
|---|---|---|
| `/usr/local/bin/dockmesh` | `install.sh` (`DOCKMESH_INSTALL_DIR`) | Server binary |
| `/usr/local/bin/dockmesh.bak` | `install.sh` on upgrade | Previous binary, for rollback |
| `/usr/local/bin/dmctl` | `install.sh` | CLI client |
| `/usr/local/bin/grype` | `install.sh` unless `DOCKMESH_NO_SCANNER=1` | Vulnerability scanner |
| `/usr/local/share/dockmesh/install-agent.sh` | `install.sh` (`DOCKMESH_ASSET_DIR`) | Served at `/install/agent.sh` |
| `/usr/local/share/dockmesh/bin/dockmesh-agent-<os>-<arch>` | `install.sh` | Agent binary for the server's own OS and architecture, served at `/install/{name}` |
| `/etc/systemd/system/dockmesh.service` | `install.sh` or `dockmesh init` | Service unit, user `dockmesh`, group `docker` |
| `/Library/LaunchDaemons/dev.dockmesh.service.plist` | macOS | launchd job; logs in `/usr/local/var/log/dockmesh.log` and `.err` |

<!-- update after: agents: pin the CA at enrollment and verify self-upgrades -->
The agent install command always downloads `dockmesh-agent-linux-amd64`. On a server that is not linux/amd64 that file is missing and the download fails; copy the right binary into the asset `bin/` directory under that name if you need it.

The server writes its log to stdout, so on Linux read it with `journalctl -u dockmesh`.

## Agent hosts

`install-agent.sh` (Linux with systemd only) creates:

| Path | Purpose |
|---|---|
| `/var/lib/dockmesh/` | Agent data directory (`DOCKMESH_DATA_DIR`, 0700, owned by `dockmesh-agent` or root with `--as-root`) |
| `/var/lib/dockmesh/agent.crt`, `agent.key` | Client certificate (365 days) and key issued at enrollment |
| `/var/lib/dockmesh/ca.crt` | Server CA received at enrollment |
| `/var/lib/dockmesh/agent.url` | Saved `wss://` server URL |
| `/var/lib/dockmesh/bin/dockmesh-agent` | Agent binary; self-upgrades replace it in place via `dockmesh-agent.new` |
| `/var/lib/dockmesh/stacks/<stack>/` | Mirror of stacks deployed to this host, with a plaintext `.env` |
| `/var/lib/dockmesh/staging/` | Temporary deploy directories |
| `/usr/local/bin/dockmesh-agent` | Symlink to the binary |
| `/etc/dockmesh-agent/agent.env` | Environment file, including the enrollment token (0640) |
| `/etc/systemd/system/dockmesh-agent.service` | Service unit |

Re-running the installer with a new token removes the old certificate, key, CA, URL file and `staging/` first.

## Docker image

The repository's `Dockerfile` builds a distroless image that runs `/app/dockmesh` as user `nonroot` with working directory `/app`. There is no published image; build it yourself with `docker compose build` or `make docker`. With the default relative paths the data ends up in:

| Path in the container | Content |
|---|---|
| `/app/data` | Data directory (DB, keys, PKI, git-cache, default local backups) |
| `/app/stacks` | Stacks directory |

The repository's `docker-compose.yml` mounts `./data:/app/data`, `./stacks:/app/stacks` and the Docker socket read-only. The image contains no Grype binary and serves no agent installer or binaries unless you mount them and set `DOCKMESH_INSTALL_SCRIPT` and `DOCKMESH_BINARY_DIR`.

## Reverse proxy volumes

When the reverse proxy is enabled, the `dockmesh-proxy` container stores Caddy's state in two named Docker volumes: `dockmesh-caddy-data` (certificates, ACME account) and `dockmesh-caddy-config`. They live with your other Docker volumes, not in the Dockmesh data root.
