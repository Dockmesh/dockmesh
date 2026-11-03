---
title: Environment variables
description: Every environment variable read by the Dockmesh server, the agent, dmctl and the install scripts, with defaults.
---

Dockmesh has no configuration file format of its own. The server reads `DOCKMESH_*` environment variables at startup; everything else lives in the database and is changed in the UI. `configs/dockmesh.example.yaml` in the repository is not parsed by anything.

On a normal install the variables live in `dockmesh.env` in the data root (`/var/lib/dockmesh/dockmesh.env` by default). systemd loads it with `EnvironmentFile=`, launchd passes it as `--env-file`, and the `dockmesh` admin commands load it automatically (see [dockmesh commands](./dockmesh-cli.md#how-commands-find-their-configuration)). Edit the file and restart the service to apply a change:

```bash
sudo systemctl restart dockmesh
dockmesh config show   # check what the server will see
```

Boolean variables are case-sensitive: "on by default" variables are only switched off by the exact value `false`, "off by default" ones only switched on by `true`. An empty value counts as unset.

## Server

### Network and URLs

| Variable | Default | Meaning |
|---|---|---|
| `DOCKMESH_HTTP_ADDR` | `:8080` | Listen address for the web UI, REST API, `/metrics` and `/install/*`. Plain HTTP |
| `DOCKMESH_BASE_URL` | `http://localhost:8080` | Public URL of the server. SSO callback URLs and invite links are built from it. For agent install commands it wins over **Base URL** under **Settings → General** |
| `DOCKMESH_AGENT_LISTEN` | `:8443` | Listen address of the agent listener. It always runs; an empty value falls back to the default |
| `DOCKMESH_AGENT_PUBLIC_URL` | unset | `wss://` URL agents enroll at and connect to. Wins over **Agent public URL** under **Settings → General**. With neither set: `wss://<host of the base URL>:<port of DOCKMESH_AGENT_LISTEN>/connect` |
| `DOCKMESH_AGENT_SANS` | empty | Comma-separated extra host names and IPs for the agent listener certificate. `localhost`, `127.0.0.1`, `::1` and the agent URL's host are always included |
| `DOCKMESH_AGENT_ENROLL_TOKEN_TTL` | `24h` | Lifetime of host enrollment tokens, as a Go duration from `1m` to `720h`. Any other value stops the server at startup |
| `DOCKMESH_TRUSTED_PROXIES` | unset | Used when **Trusted proxies** under **Settings → General** is empty: comma-separated IPs or CIDRs whose forwarding headers are believed. With neither, only localhost |

The web setup wizard stores the address you enter as the **Base URL** and **Agent public URL** settings, so agent install commands work without editing `dockmesh.env`. SSO callbacks and invite links don't read those settings; when you use them, also set the public URL in `dockmesh.env`:

```bash
DOCKMESH_BASE_URL=https://dockmesh.example.com
```

`dockmesh init` writes `DOCKMESH_BASE_URL`, `DOCKMESH_AGENT_PUBLIC_URL` and `DOCKMESH_AGENT_SANS` into the file it creates; on those installs the environment wins over the Settings fields.

### Paths

| Variable | Default | Meaning |
|---|---|---|
| `DOCKMESH_DB_PATH` | `./data/dockmesh.db` | SQLite database. Its directory is the data directory: PKI files and `git-cache/` go there too |
| `DOCKMESH_STACKS_ROOT` | `./stacks` | Stacks directory, one subdirectory per stack |
| `DOCKMESH_SECRETS_PATH` | `./data/secrets.env` | File holding `DOCKMESH_JWT_SECRET`; created on first start |
| `DOCKMESH_SECRETS_KEY_PATH` | `./data/secrets.age-key` | age key that encrypts stack `.env` files and stored credentials |
| `DOCKMESH_AUDIT_GENESIS_PATH` | `./data/audit-genesis.sha256` | Genesis hash of the audit log chain |
| `DOCKMESH_INSTALL_SCRIPT` | `./scripts/install-agent.sh` | Template served at `/install/agent.sh` |
| `DOCKMESH_BINARY_DIR` | `./bin` | Directory with the agent binaries (`dockmesh-agent-linux-amd64`, `-arm64`) served under `/install/` and to agents for upgrades. dmctl binaries are served from it only if you copy them in |

Relative defaults resolve against the server's working directory. The installers write absolute paths. See [Filesystem layout](./filesystem-layout.md).

### Features and security

| Variable | Default | Meaning |
|---|---|---|
| `DOCKMESH_SECRETS_ENCRYPT_ENV` | `true` | Encrypt stack `.env` files as `.env.age`. With `false`, `.env` is stored in plaintext and values meant for encrypted DB columns are stored in plaintext as well |
| `DOCKMESH_SCANNER_ENABLED` | `true` | Use Grype for image scans. Scanning also needs the binary to be present |
| `DOCKMESH_SCANNER_BINARY` | `grype` | Name or path of the Grype binary, looked up in `PATH` |
| `DOCKMESH_PROXY_ENABLED` | `false` | `true` turns the reverse proxy on at every start. See the note below |
| `DOCKMESH_PROXY_IMAGE` | `caddy:2.11.7` pinned by digest | Caddy image for the `dockmesh-proxy` container. While it is set, the Caddy version can't be switched from the UI or API |
| `DOCKMESH_METRICS_AUTH` | `true` | Require a bearer token with `metrics.view` on `/metrics`. `false` makes it public |
| `DOCKMESH_SETUP_FORCE` | `false` | Start in setup mode and skip creating the bootstrap admin. `install.sh` sets it; the web wizard removes it from `dockmesh.env` when it finishes |
| `DOCKMESH_UPDATE_ROLLBACK_KEEP` | `3` | How many rollback tags per image container updates keep. Must be a positive number, or the server doesn't start |
| `DOCKMESH_SKIP_PREMIGRATE_BACKUP` | unset | `1` or `true` skips the database copy (`<db>.pre-migrate-<time>.bak`) taken before schema migrations, for example when the disk is too full for it |

> **Note:** Whether the reverse proxy starts follows the `proxy_enabled` setting, which the enable and disable buttons on the **Reverse proxy** page save. When that setting is `false` or empty and `DOCKMESH_PROXY_ENABLED` is set, the server copies the variable into the setting at startup, so `DOCKMESH_PROXY_ENABLED=true` turns the proxy back on at every start, also after you disabled it in the UI. See [Reverse proxy](../features/reverse-proxy.md).

### Logging and tracing

| Variable | Default | Meaning |
|---|---|---|
| `DOCKMESH_LOG_FORMAT` | `json` | `json` or `text` (to stdout) |
| `DOCKMESH_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error` |
| `DOCKMESH_OTEL_ENDPOINT` | empty | OTLP/gRPC endpoint for traces; empty disables tracing |
| `DOCKMESH_OTEL_INSECURE` | `false` | Plaintext connection to the OTLP endpoint |

Tracing does not start: initialisation fails with a resource schema conflict, which the server logs as a warning and continues without tracing.

### Variables that are not read from the environment

`DOCKMESH_JWT_SECRET` is a line inside the `secrets.env` file, not an environment variable. Setting it in the environment does nothing. Losing the file signs everyone out.

### Docker connection

The server connects to Docker with the standard Docker client variables (`DOCKER_HOST`, `DOCKER_API_VERSION`, `DOCKER_CERT_PATH`, `DOCKER_TLS_VERIFY`). Without them it uses `/var/run/docker.sock`.

## Agent

Written by `install-agent.sh` to `/etc/dockmesh-agent/agent.env`.

| Variable | Default | Meaning |
|---|---|---|
| `DOCKMESH_DATA_DIR` | `/var/lib/dockmesh` | Agent state: certificate, key, CA, saved URL, staging and stack mirror. The installer writes `/var/lib/dockmesh-agent` |
| `DOCKMESH_ENROLL_URL` | none | Enrollment endpoint on the agent port (`https://<host>:8443/enroll`), required on first start |
| `DOCKMESH_TOKEN` | none | One-time enrollment token, required on first start. It stays in `agent.env` after enrollment |
| `DOCKMESH_CA_FINGERPRINT` | none | SHA-256 fingerprint of the server's agent CA. Required for enrollment over `https://`; the CA the server returns must match it |
| `DOCKMESH_ENROLL_CA_FILE` | none | PEM file with the agent CA, trusted for the enrollment request |
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
| `DOCKMESH_VERSION` | `latest` | Release tag to install |
| `DOCKMESH_CHANNEL` | `stable` | `stable` or `testing` |
| `DOCKMESH_INSTALL_DIR` | `/usr/local/bin` | Directory for `dockmesh`, `dmctl` and `grype` |
| `DOCKMESH_ASSET_DIR` | `<install-dir>/../share/dockmesh` | Agent installer and agent binaries |
| `DOCKMESH_DATA_DIR` | the directory of an earlier install's `dockmesh.env`; otherwise `/data` if it exists and is writable (or the script runs as root), else `/var/lib/dockmesh` (Linux) or `/usr/local/var/dockmesh` (macOS) | Data root. Setting it to a different directory than an earlier install's stops the script |
| `DOCKMESH_NO_SUDO` | `0` | `1` runs without `sudo` |
| `DOCKMESH_FORCE` | `0` | `1` reinstalls even if the same version is installed |
| `DOCKMESH_NO_SCANNER` | `0` | `1` skips installing Grype |
| `DOCKMESH_SKIP_CHECKSUM` | `0` | `1` installs without checking the release tarball against `checksums.txt` |
| `DOCKMESH_SKIP_DOWNLOAD` | `0` | `1` uses the binary already in the install directory |
| `DOCKMESH_NO_BOOTSTRAP` | `0` | `1` skips creating the service user, env file and service; run `dockmesh init` yourself |
| `NO_COLOR` | unset | Any value disables colours |

`dockmesh init` also honours `DOCKMESH_ASSET_DIR` when it writes `DOCKMESH_INSTALL_SCRIPT` and `DOCKMESH_BINARY_DIR`.

## install-agent.sh

The server fills in the token, the URLs, the agent CA and the binary checksums when it serves the script. It accepts two flags, `--as-root` (run the agent as root instead of a `dockmesh-agent` user) and `--skip-docker`. The only variable it reads is `AGENT_WAIT_SECS` (default `60`): how long it waits for the agent to connect. Pass it through sudo, for example `| sudo AGENT_WAIT_SECS=120 bash`.

## uninstall.sh

| Variable | Default | Meaning |
|---|---|---|
| `DOCKMESH_INSTALL_DIR` | `/usr/local/bin` | Where to remove `dockmesh`, `dockmesh.bak` and `dmctl` from |
| `NO_COLOR` | unset | Disables colours |

It finds the data root and the asset directory by looking for `dockmesh.env` in `/var/lib/dockmesh`, `/usr/local/var/dockmesh`, `/opt/dockmesh` and `/data`. Flags: `--yes`, `--purge` (also deletes the data root), `--dry-run`. See [Uninstall](../operations/uninstall.md).

## Settings with an environment counterpart

These keys live in the `settings` table and are edited in the UI or with `PUT /api/v1/settings`. Each has an environment variable that takes part as described.

| Key | Where in the UI | Environment variable |
|---|---|---|
| `base_url` | **Settings → General → Base URL** | `DOCKMESH_BASE_URL` wins for agent install commands |
| `agent_public_url` | **Settings → General → Agent public URL** | `DOCKMESH_AGENT_PUBLIC_URL` wins |
| `trusted_proxies` | **Settings → General → Trusted proxies** | `DOCKMESH_TRUSTED_PROXIES` is used while the setting is empty |
| `scanner_enabled` | **Settings → General → Vulnerability scanner** | Only changes what the system health view reports; whether scans run is decided by `DOCKMESH_SCANNER_ENABLED` |
| `proxy_enabled` | **Reverse proxy** page, enable and disable | `DOCKMESH_PROXY_ENABLED=true` sets it to `true` at every start |

The release check against GitHub is set under **Settings → Updates** (`update_check_enabled`, `update_check_interval_minutes`); it has no environment variable.
