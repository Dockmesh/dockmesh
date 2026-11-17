---
title: Environment variables
description: Every environment variable read by the Dockmesh server, the agent, dmctl and the install scripts, with defaults.
---

Dockmesh has no configuration file format of its own. The server reads `DOCKMESH_*` environment variables at startup; everything else lives in the database and is changed in the UI. `configs/dockmesh.example.yaml` in the repository is not parsed by anything.

On a Linux install the variables live in `/etc/dockmesh/dockmesh.env` (owned by root, readable by the `dockmesh` group). Installs from before 0.6 kept the file in the data root (`/var/lib/dockmesh/dockmesh.env`); the installer moves it to `/etc/dockmesh` on the next upgrade and leaves a symlink at the old place, and until then the server keeps reading it from there. systemd loads it with `EnvironmentFile=`, launchd passes it as `--env-file`, and the `dockmesh` admin commands load it automatically (see [dockmesh commands](./dockmesh-cli.md#how-commands-find-their-configuration)). Edit the file and restart the service to apply a change:

```bash
sudo systemctl restart dockmesh
dockmesh config show   # check what the server will see
```

Boolean variables are case-sensitive: "on by default" variables are only switched off by the exact value `false`, "off by default" ones only switched on by `true`. An empty value counts as unset.

## Server

### Network and URLs

| Variable | Default | Meaning |
|---|---|---|
| `DOCKMESH_HTTP_ADDR` | `:8080` (`127.0.0.1:8080` with `DOCKMESH_TLS_MODE` `caddy` or `proxy`) | Listen address for the web UI, REST API, `/metrics` and `/install/*` |
| `DOCKMESH_TLS_MODE` | unset | What that listener serves. Unset: plain HTTP. `internal`: HTTPS with a certificate from the built-in CA, kept across restarts (its SHA-256 fingerprint is logged at start). `file`: HTTPS with `DOCKMESH_TLS_CERT_FILE` and `DOCKMESH_TLS_KEY_FILE`. `caddy`: plain HTTP behind the managed Caddy proxy. `proxy`: plain HTTP behind your own reverse proxy. Any other value stops the server at startup. Setup mode on a new install always serves HTTPS |
| `DOCKMESH_TLS_CERT_FILE`, `DOCKMESH_TLS_KEY_FILE` | unset | PEM certificate (with its chain) and key for `DOCKMESH_TLS_MODE=file`. The `dockmesh` user must be able to read both. Dockmesh checks the files every 10 seconds and serves a renewed pair without a restart; a pair that does not load keeps the previous one in service |
| `DOCKMESH_BASE_URL` | `http://localhost:8080` | Public URL of the server. Wins over **Base URL** under **Settings → General** for SSO callback URLs, invite links and agent install commands |
| `DOCKMESH_AGENT_LISTEN` | `:8443` | Listen address of the agent listener. It always runs; an empty value falls back to the default |
| `DOCKMESH_AGENT_PUBLIC_URL` | unset | `wss://` URL agents enroll at and connect to. Wins over **Agent public URL** under **Settings → General**. With neither set: `wss://<host of the base URL>:<port of DOCKMESH_AGENT_LISTEN>/connect` |
| `DOCKMESH_AGENT_SANS` | empty | Comma-separated extra host names and IPs for the agent listener certificate. `localhost`, `127.0.0.1`, `::1` and the agent URL's host are always included |
| `DOCKMESH_AGENT_ENROLL_TOKEN_TTL` | `1h` | Lifetime of host enrollment tokens, as a Go duration from `1m` to `720h`. Any other value stops the server at startup |
| `DOCKMESH_TRUSTED_PROXIES` | unset | Used when **Trusted proxies** under **Settings → General** is empty: comma-separated IPs or CIDRs whose forwarding headers are believed. With neither, only localhost; with `DOCKMESH_TLS_MODE` `caddy` or `proxy`, the IP of `DOCKMESH_HTTP_ADDR` (none for a wildcard address) |

The web setup wizard stores the address you enter as the **Base URL** and **Agent public URL** settings, and agent install commands, SSO callback URLs and invite links are built from them, so you don't need to edit `dockmesh.env`. A change under **Settings → General** applies without a restart.

When `DOCKMESH_BASE_URL` or `DOCKMESH_AGENT_PUBLIC_URL` is set in `dockmesh.env`, it wins over the Settings field. Installs set up with `dockmesh init` before 0.6 have `DOCKMESH_BASE_URL`, `DOCKMESH_AGENT_PUBLIC_URL` and `DOCKMESH_AGENT_SANS` in their config.

### Paths

`DOCKMESH_DATA_DIR` is the one path a config needs; the others are derived from it. Set one of the others only to move that single path somewhere else.

| Variable | Default | Meaning |
|---|---|---|
| `DOCKMESH_DATA_DIR` | `/var/lib/dockmesh`, or, when only `DOCKMESH_DB_PATH` is set, the directory above its `data/`; without any config, the working directory when it holds `data/dockmesh.db` of a source run up to 0.5.x and passes the ownership check in [Filesystem layout](./filesystem-layout.md#server-data-root) | Data root. All paths below default to places in it |
| `DOCKMESH_DB_PATH` | `<data root>/data/dockmesh.db` | SQLite database. Its directory is the data directory: PKI files and `git-cache/` go there too |
| `DOCKMESH_STACKS_ROOT` | `<data root>/stacks` | Stacks directory, one subdirectory per stack |
| `DOCKMESH_SECRETS_PATH` | `<data root>/data/secrets.env` | File holding `DOCKMESH_JWT_SECRET`; created on first start |
| `DOCKMESH_SECRETS_KEY_PATH` | `<data root>/data/secrets.age-key` | age key that encrypts stack `.env` files and stored credentials |
| `DOCKMESH_AUDIT_GENESIS_PATH` | `<data root>/data/audit-genesis.sha256` | Genesis hash of the audit log chain |
| `DOCKMESH_INSTALL_SCRIPT` | `./scripts/install-agent.sh` | Template served at `/install/agent.sh` on the agent port (and on the web port for older commands) |
| `DOCKMESH_BINARY_DIR` | `./bin` | Directory with the agent binaries (`dockmesh-agent-linux-amd64`, `-arm64`) served under `/install/` and to agents for upgrades. dmctl binaries are served from it only if you copy them in |

Relative defaults resolve against the server's working directory. The installers write absolute paths. See [Filesystem layout](./filesystem-layout.md).

### Features and security

| Variable | Default | Meaning |
|---|---|---|
| `DOCKMESH_SECRETS_ENCRYPT_ENV` | `true` | Encrypt stack `.env` files as `.env.age`. With `false`, `.env` is stored in plaintext and values meant for encrypted DB columns are stored in plaintext as well |
| `DOCKMESH_SCANNER_ENABLED` | `true` | Default for **Scan images with Grype** under **Settings → General**; a value saved there wins. Scanning also needs the binary to be present |
| `DOCKMESH_SCANNER_BINARY` | `grype` | Name or path of the Grype binary, looked up in `PATH` |
| `DOCKMESH_PROXY_ENABLED` | `false` | `true` turns the reverse proxy on while the `proxy_enabled` setting has never been saved. See the note below |
| `DOCKMESH_PROXY_IMAGE` | `caddy:2.11.7` pinned by digest | Caddy image for the `dockmesh-proxy` container. While it is set, the Caddy version can't be switched from the UI or API |
| `DOCKMESH_METRICS_AUTH` | `true` | Require a bearer token with `metrics.view` on `/metrics`. `false` makes it public |
| `DOCKMESH_SETUP_FORCE` | `false` | Marks an install whose setup wizard has not finished: with no user accounts, the server starts in setup mode. Ignored once accounts exist. `install.sh` sets it on macOS (on Linux only installs before 0.6 have it); the web wizard removes it from `dockmesh.env` when it finishes |
| `DOCKMESH_UPDATE_ROLLBACK_KEEP` | `3` | How many rollback tags per image container updates keep. Must be a positive number, or the server doesn't start |
| `DOCKMESH_SKIP_PREMIGRATE_BACKUP` | unset | `1` or `true` skips the database copy (`<db>.pre-migrate-<time>.bak`) taken before schema migrations, for example when the disk is too full for it |

> **Note:** Whether the reverse proxy starts follows the `proxy_enabled` setting, which the enable and disable buttons on the **Reverse proxy** page save. `DOCKMESH_PROXY_ENABLED=true` only counts while that setting has never been saved. Once you enable or stop the proxy on the page, the saved choice wins over the variable. See [Reverse proxy](../features/reverse-proxy.md).

### Logging and tracing

| Variable | Default | Meaning |
|---|---|---|
| `DOCKMESH_LOG_FORMAT` | `json` | `json` or `text` (to stdout) |
| `DOCKMESH_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error` |
| `DOCKMESH_OTEL_ENDPOINT` | empty | OTLP/gRPC endpoint for traces; empty disables tracing |
| `DOCKMESH_OTEL_INSECURE` | `false` | Plaintext connection to the OTLP endpoint |

With `DOCKMESH_OTEL_ENDPOINT` set, the server sends traces of HTTP requests over OTLP/gRPC and logs `otel tracing enabled` at start. If the exporter can't be set up, it logs a warning and runs without tracing.

### Variables that are not read from the environment

`DOCKMESH_JWT_SECRET` is a line inside the `secrets.env` file, not an environment variable. Setting it in the environment does nothing. Losing the file signs everyone out.

### Docker connection

The server connects to Docker with the standard Docker client variables (`DOCKER_HOST`, `DOCKER_API_VERSION`, `DOCKER_CERT_PATH`, `DOCKER_TLS_VERIFY`). Without them it uses `/var/run/docker.sock`.

### Time zone

The server runs in the time zone of its process (`TZ`, otherwise the system zone). Backup schedules use it unless the `backups.timezone` setting names another zone; see [Backups](../operations/backups.md#schedules-and-time-zone).

## Agent

Written by `install-agent.sh` to `/etc/dockmesh-agent/agent.env`.

| Variable | Default | Meaning |
|---|---|---|
| `DOCKMESH_DATA_DIR` | `/var/lib/dockmesh-agent` (`/var/lib/dockmesh` when only that directory holds an enrollment) | Agent state: certificate, key, CA, saved URL, staging and stack mirror. The installer writes it to `/etc/dockmesh-agent/agent.env`, which the agent also reads when started by hand |
| `DOCKMESH_ENROLL_URL` | none | Enrollment endpoint on the agent port (`https://<host>:8443/enroll`), required on first start |
| `DOCKMESH_TOKEN` | none | One-time enrollment token for a first start without `<data dir>/enroll-token`. The installer writes the token to that file instead, and the agent deletes it after enrollment |
| `DOCKMESH_CA_FINGERPRINT` | none | SHA-256 fingerprint of the server's agent CA. Required for enrollment over `https://`; the CA the server returns must match it |
| `DOCKMESH_ENROLL_CA_FILE` | none | PEM file with the agent CA, trusted for the enrollment request. Required for enrollment over `https://`; it must match `DOCKMESH_CA_FINGERPRINT` |
| `DOCKMESH_AGENT_URL` | saved at enrollment | `wss://` URL of the server's agent listener. Overrides the saved one |
| `DOCKMESH_INSECURE_ENROLL` | unset | `true` allows enrollment over plain `http://`, like the `--insecure-enroll` flag. For isolated labs only |

## dmctl

| Variable | Meaning |
|---|---|
| `DMCTL_SERVER` | Server URL, used when `--server` is not given |
| `DMCTL_TOKEN` | API token or access token, used when `--token` is not given |

## install.sh

Set these in front of `bash` when piping the installer, for example `curl -fsSL https://get.dockmesh.dev | sudo DOCKMESH_NO_SCANNER=1 bash`.

| Variable | Default | Meaning |
|---|---|---|
| `DOCKMESH_VERSION` | `latest` | Release tag to install (same as `--version`) |
| `DOCKMESH_CHANNEL` | `stable` | `stable` or `testing`; `testing` is deprecated and removed in 0.7.0, use `--version <tag>` |
| `DOCKMESH_INSTALL_DIR` | `/usr/local/bin` | Directory for `dockmesh`, `dmctl` and `grype` |
| `DOCKMESH_ASSET_DIR` | `<install-dir>/../share/dockmesh` | Agent installer and agent binaries |
| `DOCKMESH_DATA_DIR` | the data root of an earlier install | On an upgrade, the data root of the earlier install; a different directory than the one its config names stops the script. Not used on a new Linux install (the setup wizard or setup file chooses); on macOS the data root of a new install (default `/usr/local/var/dockmesh`) |
| `DOCKMESH_NO_SUDO` | `0` | `1` runs without `sudo` |
| `DOCKMESH_FORCE` | `0` | `1` reinstalls even if the same version is installed |
| `DOCKMESH_NO_SCANNER` | `0` | `1` skips installing Grype |
| `DOCKMESH_SKIP_CHECKSUM` | `0` | `1` installs without checking the signature or the release tarball against `checksums.txt`; the binary runs as root, so only for a build you verified yourself |
| `DOCKMESH_SKIP_SIGNATURE` | `0` | `1` trusts `checksums.txt` without checking its minisign signature and prints a warning; needed for releases before v0.6.0 and hosts without OpenSSL 3, see [Verify a release](../operations/verify-release.md) |
| `DOCKMESH_SKIP_DOWNLOAD` | `0` | `1` uses the binary already in the install directory |
| `DOCKMESH_NO_BOOTSTRAP` | `0` | `1` on a new Linux install creates the service user, data root and units but does not start setup mode |
| `DOCKMESH_SETUP_FILE` | unset | Setup file to apply instead of starting setup mode, on a new Linux install (see `dockmesh setup apply`) |
| `NO_COLOR` | unset | Any value disables colours |

The config a setup writes points `DOCKMESH_INSTALL_SCRIPT` and `DOCKMESH_BINARY_DIR` at `share/dockmesh` next to the directory of the `dockmesh` binary.

## install-agent.sh

The server fills in the URLs, the agent listener's key pin, the agent CA and the binary checksums when it serves the script. It takes the enrollment token as `--token <token>` and accepts `--data-dir`, `--stacks-dir`, `--skip-docker` and `--switch-to-service-user` (see [Running the installer again](../features/multi-host.md#running-the-installer-again)). The only variable it reads is `AGENT_WAIT_SECS` (default `60`): how long it waits for the agent to connect. Pass it through sudo, for example `| sudo AGENT_WAIT_SECS=120 bash`.

## uninstall.sh

| Variable | Default | Meaning |
|---|---|---|
| `DOCKMESH_INSTALL_DIR` | `/usr/local/bin` | Where to remove `dockmesh`, `dockmesh.bak` and `dmctl` from |
| `NO_COLOR` | unset | Disables colours |

It reads the data root from `DOCKMESH_DATA_DIR` in `/etc/dockmesh/dockmesh.env`, else it looks for `dockmesh.env` in `/var/lib/dockmesh`, `/usr/local/var/dockmesh`, `/opt/dockmesh` and `/data`. Flags: `--yes`, `--purge` (also removes Dockmesh's files from the data root), `--dry-run`. See [Uninstall](../operations/uninstall.md).

## Settings with an environment counterpart

These keys live in the `settings` table and are edited in the UI or with `PUT /api/v1/settings`. Each has an environment variable that takes part as described.

| Key | Where in the UI | Environment variable |
|---|---|---|
| `base_url` | **Settings → General → Base URL** | `DOCKMESH_BASE_URL` wins |
| `agent_public_url` | **Settings → General → Agent public URL** | `DOCKMESH_AGENT_PUBLIC_URL` wins |
| `trusted_proxies` | **Settings → General → Trusted proxies** | `DOCKMESH_TRUSTED_PROXIES` is used while the setting is empty |
| `scanner_enabled` | **Settings → General → Vulnerability scanner** | Turns image scanning on or off without a restart. `DOCKMESH_SCANNER_ENABLED` only sets the default |
| `proxy_enabled` | **Reverse proxy** page, enable and disable | `DOCKMESH_PROXY_ENABLED=true` counts as `true` until the setting is saved |

The release check against GitHub is set under **Settings → Updates** (`update_check_enabled`, `update_check_interval_minutes`); it has no environment variable.
