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
| `DOCKMESH_BASE_URL` | `http://localhost:8080` | Public URL of the server. Used for SSO callback URLs, invite links, agent enrollment and binary download URLs, and the default agent URL |
| `DOCKMESH_AGENT_LISTEN` | `:8443` | Listen address of the agent mTLS listener. It always runs; an empty value falls back to the default |
| `DOCKMESH_AGENT_PUBLIC_URL` | derived | `wss://` URL agents dial, put into install commands. Empty means `wss://<host of BASE_URL>:<port of AGENT_LISTEN>/connect` |
| `DOCKMESH_AGENT_SANS` | empty | Comma-separated extra hostnames and IPs for the agent listener certificate. `localhost`, `127.0.0.1` and `::1` are always included |

The web setup wizard stores the public URL you enter in the database setting `base_url`, but the server builds links from `DOCKMESH_BASE_URL`. The `dockmesh.env` written by `install.sh` does not contain `DOCKMESH_BASE_URL`, so before you enroll agents or configure SSO, add it yourself:

```bash
DOCKMESH_BASE_URL=https://dockmesh.example.com
DOCKMESH_AGENT_PUBLIC_URL=wss://dockmesh.example.com:8443/connect
DOCKMESH_AGENT_SANS=dockmesh.example.com,192.0.2.10
```

Agents verify the listener certificate against the names in `DOCKMESH_AGENT_SANS`, so every name or IP that appears in `DOCKMESH_AGENT_PUBLIC_URL` must be listed. The server reissues its listener certificate at startup when a name is missing.

### Paths

| Variable | Default | Meaning |
|---|---|---|
| `DOCKMESH_DB_PATH` | `./data/dockmesh.db` | SQLite database. Its directory is the data directory: PKI files and `git-cache/` go there too |
| `DOCKMESH_STACKS_ROOT` | `./stacks` | Stacks directory, one subdirectory per stack |
| `DOCKMESH_SECRETS_PATH` | `./data/secrets.env` | File holding `DOCKMESH_JWT_SECRET`; created on first start |
| `DOCKMESH_SECRETS_KEY_PATH` | `./data/secrets.age-key` | age key that encrypts stack `.env` files and stored credentials |
| `DOCKMESH_AUDIT_GENESIS_PATH` | `./data/audit-genesis.sha256` | Genesis hash of the audit log chain |
| `DOCKMESH_INSTALL_SCRIPT` | `./scripts/install-agent.sh` | Template served at `/install/agent.sh` |
| `DOCKMESH_BINARY_DIR` | `./bin` | Directory served at `/install/{name}`. `install.sh` puts only the agent binary for the server's own platform there; dmctl binaries are served only if you copy them in |

Relative defaults resolve against the server's working directory. The installers write absolute paths. See [Filesystem layout](./filesystem-layout.md).

### Features and security

| Variable | Default | Meaning |
|---|---|---|
| `DOCKMESH_SECRETS_ENCRYPT_ENV` | `true` | Encrypt stack `.env` files as `.env.age`. With `false`, `.env` is stored in plaintext and values meant for encrypted DB columns are stored in plaintext as well |
| `DOCKMESH_SCANNER_ENABLED` | `true` | Use Grype for image scans. Scanning also needs the binary to be present |
| `DOCKMESH_SCANNER_BINARY` | `grype` | Name or path of the Grype binary, looked up in `PATH` |
| `DOCKMESH_PROXY_ENABLED` | `false` | Default for the reverse proxy. See the note below |
| `DOCKMESH_METRICS_AUTH` | `true` | Require a bearer token with `metrics.view` on `/metrics`. `false` makes it public |
| `DOCKMESH_RBAC_V2_ENFORCE` | `true` | Enforce role scopes (per host, stack, host tag). `false` turns role scopes off; permissions and user scope tags still apply |
| `DOCKMESH_SETUP_FORCE` | `false` | Start in setup mode and skip creating the bootstrap admin. `install.sh` sets it; the web wizard removes it from `dockmesh.env` when it finishes |

<!-- update after: proxy: keep the Caddy container, resume routes, serve tls none as http -->
> **Note:** The reverse proxy at boot follows the database setting `proxy_enabled`. When that setting is empty or `false` and `DOCKMESH_PROXY_ENABLED` is set, the server copies the variable into the setting at startup, so `DOCKMESH_PROXY_ENABLED=true` turns the proxy on and keeps it on for as long as the variable is set. The enable button on the **Proxy** page starts the proxy but does not change the setting. Without the variable, set the setting through the API (needs `system.update`):
>
> ```bash
> curl -X PUT https://dockmesh.example.com/api/v1/settings \
>   -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
>   -d '[{"key":"proxy_enabled","value":"true"}]'
> ```
>
> See [Reverse proxy](../features/reverse-proxy.md).

### Logging and tracing

| Variable | Default | Meaning |
|---|---|---|
| `DOCKMESH_LOG_FORMAT` | `json` | `json` or `text` (to stdout) |
| `DOCKMESH_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error` |
| `DOCKMESH_OTEL_ENDPOINT` | empty | OTLP/gRPC endpoint for traces; empty disables tracing |
| `DOCKMESH_OTEL_INSECURE` | `false` | Plaintext connection to the OTLP endpoint |

Tracing does not start: initialisation fails with a resource schema conflict, which the server logs as a warning and continues without tracing.

### Variables that are not read from the environment

- `DOCKMESH_JWT_SECRET` is a line inside the `secrets.env` file, not an environment variable. Setting it in the environment does nothing. Losing the file signs everyone out.
- `DOCKMESH_LISTEN` and `DOCKMESH_DATA_DIR` appear in the `dockmesh.env` written by `dockmesh init`, but the server reads neither. Use `DOCKMESH_HTTP_ADDR` to change the listen address.

### Docker connection

The server connects to Docker with the standard Docker client variables (`DOCKER_HOST`, `DOCKER_API_VERSION`, `DOCKER_CERT_PATH`, `DOCKER_TLS_VERIFY`). Without them it uses `/var/run/docker.sock`.

## Agent

Written by `install-agent.sh` to `/etc/dockmesh-agent/agent.env`.

| Variable | Default | Meaning |
|---|---|---|
| `DOCKMESH_DATA_DIR` | `/var/lib/dockmesh` | Agent state: certificate, key, CA, saved URL, staging and stack mirror |
| `DOCKMESH_ENROLL_URL` | none | Enrollment endpoint, required on first start |
| `DOCKMESH_TOKEN` | none | One-time enrollment token, required on first start. It stays in `agent.env` after enrollment |
| `DOCKMESH_AGENT_URL` | saved at enrollment | `wss://` URL of the server's agent listener |

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
| `DOCKMESH_INSTALL_DIR` | `/usr/local/bin` | Directory for `dockmesh` and `dmctl` |
| `DOCKMESH_ASSET_DIR` | `<install-dir>/../share/dockmesh` | Agent installer and agent binaries |
| `DOCKMESH_DATA_DIR` | `/data` if it exists and is writable (or the script runs as root), else `/var/lib/dockmesh` (Linux) or `/usr/local/var/dockmesh` (macOS) | Data root |
| `DOCKMESH_NO_SUDO` | `0` | `1` runs without `sudo` |
| `DOCKMESH_FORCE` | `0` | `1` reinstalls even if the same version is installed |
| `DOCKMESH_NO_SCANNER` | `0` | `1` skips installing Grype |
| `DOCKMESH_SKIP_DOWNLOAD` | `0` | `1` uses the binary already in the install directory |
| `DOCKMESH_NO_BOOTSTRAP` | `0` | `1` skips creating the service user, env file and service; run `dockmesh init` yourself |
| `NO_COLOR` | unset | Any value disables colours |

`dockmesh init` also honours `DOCKMESH_ASSET_DIR` when it writes `DOCKMESH_INSTALL_SCRIPT` and `DOCKMESH_BINARY_DIR`.

## install-agent.sh

The agent installer reads no environment variables: the server fills in the token and URLs when it serves the script. It accepts two flags, `--as-root` (run the agent as root instead of a `dockmesh-agent` user) and `--skip-docker`.

## uninstall.sh

| Variable | Default | Meaning |
|---|---|---|
| `DOCKMESH_INSTALL_DIR` | `/usr/local/bin` | Where to remove `dockmesh`, `dockmesh.bak` and `dmctl` from |
| `NO_COLOR` | unset | Disables colours |

It finds the data root and the asset directory by looking for `dockmesh.env` in `/var/lib/dockmesh`, `/usr/local/var/dockmesh`, `/opt/dockmesh` and `/data`. Flags: `--yes`, `--purge` (also deletes the data root), `--dry-run`. See [Uninstall](../operations/uninstall.md).

## Settings stored in the database

<!-- update after: http: trust forwarding headers only from configured proxies -->
<!-- update after: backups: run in the background, set the time zone, use saved targets -->
These keys live in the `settings` table. They are edited on the **Settings** page or with `PUT /api/v1/settings`, not through the environment.

| Key | Seed | Effect |
|---|---|---|
| `proxy_enabled` | `false` | Read at startup to decide whether to start the `dockmesh-proxy` container |
| `scanner_enabled` | `true` | Only changes what the system health view reports; whether scans run is decided by `DOCKMESH_SCANNER_ENABLED` |
| `base_url`, `agent_public_url` | `http://localhost:8080`, empty | Shown and editable in **Settings**; links are still built from the environment variables above |
| `update_check_enabled`, `update_check_interval_minutes` | `true`, `120` | Release check against GitHub |
