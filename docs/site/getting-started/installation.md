---
title: Installation
description: Install the Dockmesh server with the installer script, by hand, from source, or as a locally built container.
---

The recommended path is the installer script on a Linux host that already runs Docker. It installs the binaries, creates a service account and a systemd unit, starts the server and hands you over to a setup wizard in the browser.

## Requirements

| | |
|---|---|
| Operating system | Linux (amd64 or arm64). macOS (amd64 or arm64) is also handled by the installer but is less tested; the setup wizard warns on anything other than Linux. The server and agent do not run on Windows. |
| Docker | A running Docker Engine reachable at `/var/run/docker.sock`. 20.10 or newer is recommended. The installer does not install Docker; it prints the command for your distribution if Docker is missing. On macOS, enable **Allow the default Docker socket to be used** in Docker Desktop's advanced settings. |
| Tools | `curl`, `tar`, and `sha256sum` (or `shasum` on macOS). systemd is optional; without it the server has to be started by hand. |
| Privileges | root or `sudo`. |
| Ports | `8080/tcp` for the web UI and API. `8443/tcp` for remote agents, only needed if you add remote hosts. If you enable the reverse proxy, its Caddy container binds `80` and `443` on the host. See [Ports](../reference/ports.md). |
| Resources | The setup wizard warns below 512 MB RAM or below 2 GB free on the root filesystem. Images and volumes of your stacks need space on top of that. |
| Clock | Keep the clock synchronised (NTP). Agent TLS handshakes fail when server and host clocks drift apart. |

The UI is served over plain HTTP. For anything beyond a trusted LAN, put it behind a TLS-terminating reverse proxy; see [Security hardening](../operations/hardening.md).

## Install with the script

```bash
curl -fsSL https://get.dockmesh.dev | sudo bash
```

`get.dockmesh.dev` serves `scripts/install.sh` from the latest release. If you prefer to read it first, download it from the release assets (`https://github.com/dockmesh/dockmesh/releases/latest/download/install.sh`) and run it with `sudo bash install.sh`.

The script:

1. Checks OS, architecture, required tools, Docker, and whether ports 8080 and 8443 are free.
2. Looks up the latest release on GitHub, downloads `dockmesh_<os>_<arch>.tar.gz` and verifies it against the release's `checksums.txt`. If no checksum is published it warns and continues.
3. Installs the binaries and agent assets (table below).
4. Installs the `grype` vulnerability scanner with Anchore's install script, unless `grype` is already on the `PATH` or you set `DOCKMESH_NO_SCANNER=1`. If that install fails, Dockmesh still works; image scans are just unavailable.
5. On Linux, creates a system user `dockmesh` (no login shell, no home) and adds it to the `docker` group.
6. Creates the data root with `data/` and `stacks/` inside, owned by `dockmesh:docker`, mode 750.
7. Writes `<data root>/dockmesh.env` (mode 600) and a systemd unit, then enables and starts the service.
8. Prints the setup wizard URL, `http://<first IP of the host>:8080/setup`.

### What goes where

| Path | Content |
|---|---|
| `/usr/local/bin/dockmesh` | Server binary |
| `/usr/local/bin/dmctl` | CLI client |
| `/usr/local/bin/grype` | Vulnerability scanner (unless skipped) |
| `/usr/local/share/dockmesh/install-agent.sh` | Agent installer served to remote hosts |
| `/usr/local/share/dockmesh/bin/dockmesh-agent-<os>-<arch>` | Agent binary served to remote hosts |
| `/var/lib/dockmesh/` | Data root: `dockmesh.env`, `data/` (database, keys, certificates), `stacks/` |
| `/etc/systemd/system/dockmesh.service` | Service unit: runs `dockmesh serve` as `dockmesh:docker` with `EnvironmentFile=<data root>/dockmesh.env` |

The agent binary is not installed into `/usr/local/bin`; it is only kept so the server can hand it to remote hosts.

The data root is `/data` instead of `/var/lib/dockmesh` if a `/data` directory already exists on the host. On macOS it is `/usr/local/var/dockmesh`, the service is the launchd job `/Library/LaunchDaemons/dev.dockmesh.service.plist` running as root, and logs go to `/usr/local/var/log/dockmesh.log` and `dockmesh.err`.

The repository also contains `scripts/dockmesh.service`. That is a sample unit for running from a checkout in `/home/dockmesh/dockmesh`; neither the installer nor `dockmesh init` uses it.

### Installer options

Set these as environment variables. With `sudo`, put them after `sudo` so they reach the script:

```bash
curl -fsSL https://get.dockmesh.dev | sudo DOCKMESH_NO_SCANNER=1 bash
```

| Variable | Default | Effect |
|---|---|---|
| `DOCKMESH_VERSION` | latest release | Release tag to install, e.g. `v0.3.3`. |
| `DOCKMESH_CHANNEL` | `stable` | `testing` also considers pre-releases when resolving "latest". |
| `DOCKMESH_INSTALL_DIR` | `/usr/local/bin` | Where `dockmesh`, `dmctl` and `grype` go. |
| `DOCKMESH_ASSET_DIR` | `<install dir>/../share/dockmesh` | Where the agent installer and agent binary go. |
| `DOCKMESH_DATA_DIR` | `/data` if it exists, else `/var/lib/dockmesh` | Data root. |
| `DOCKMESH_NO_SCANNER` | unset | `1` skips the `grype` install. |
| `DOCKMESH_NO_BOOTSTRAP` | unset | `1` installs the binaries only: no service user, data root, env file or unit. Run `sudo dockmesh init` afterwards. |
| `DOCKMESH_NO_SUDO` | unset | `1` never calls `sudo` (the install directory must be writable). |
| `DOCKMESH_FORCE` | unset | `1` reinstalls even if the installed version is already the target version. |
| `NO_COLOR` | unset | Disables colored output. |

### The env file

The installer writes a minimal `dockmesh.env`:

```bash
DOCKMESH_HTTP_ADDR=:8080
DOCKMESH_AGENT_LISTEN=:8443
DOCKMESH_DB_PATH=/var/lib/dockmesh/data/dockmesh.db
DOCKMESH_STACKS_ROOT=/var/lib/dockmesh/stacks
DOCKMESH_SECRETS_PATH=/var/lib/dockmesh/data/secrets.env
DOCKMESH_SECRETS_KEY_PATH=/var/lib/dockmesh/data/secrets.age-key
DOCKMESH_AUDIT_GENESIS_PATH=/var/lib/dockmesh/data/audit-genesis.sha256
DOCKMESH_INSTALL_SCRIPT=/usr/local/share/dockmesh/install-agent.sh
DOCKMESH_BINARY_DIR=/usr/local/share/dockmesh/bin
DOCKMESH_SETUP_FORCE=true
```

`DOCKMESH_SETUP_FORCE=true` keeps the server in setup mode until the web wizard has run; the wizard removes the line when it finishes. Edit the file and restart the service to change settings. All variables are listed in [Environment variables](../reference/environment-variables.md).

`dockmesh` subcommands such as `dockmesh admin list-users` load `/var/lib/dockmesh/dockmesh.env` automatically. If your data root is elsewhere, pass `--env-file <path>`. Run them with `sudo`, because the data root is only readable by the service account.

## The setup wizard

Open the URL the installer printed (`http://<host>:8080/setup`). While setup mode is active, the rest of the UI and API answer with 503 and point to `/setup`.

<!-- update after: restore: stage system restores and swap them in at startup -->
The wizard has seven steps:

1. **Welcome** runs preflight checks: operating system, CPU, RAM, root disk space, Docker Engine, Docker socket, time synchronisation. A failed Docker check blocks the wizard.
2. **Data** asks for the data root.
3. **Service user** asks which OS account Dockmesh runs as (`dockmesh` is recommended).
4. **Admin** creates the first administrator: username, password, optional email.
5. **URL** asks for the public URL users and agents use to reach the server. It defaults to the address in your browser; enter the `https://` address of your reverse proxy instead of accepting a plain `http://<host>:8080`.
6. **Review** shows your answers. Confirming here starts the installation.
7. **Done** streams the progress of the installation, then shows the URL and admin credentials.

On commit the server creates the admin account, stores the public URL as the server's base URL setting, and removes `DOCKMESH_SETUP_FORCE` from `dockmesh.env`. Sign in afterwards at `http://<host>:8080`.

The wizard's URL ends up only in the database setting. Agent install commands and SSO redirect URLs are built from the environment, so also add the public address to `dockmesh.env` and restart the service (for remote hosts, see [Multi-host](../features/multi-host.md) for the agent variables as well):

```bash
DOCKMESH_BASE_URL=https://dockmesh.example.com
```

Port 8080 is plain HTTP. Before you use Dockmesh beyond a trusted LAN, put a TLS reverse proxy in front and keep 8080 off the network; see [Security hardening](../operations/hardening.md) and [Ports](../reference/ports.md).

The data root and service user answers do not relocate anything: the server keeps using the paths in `dockmesh.env` and keeps running as the user from the unit file. The wizard only creates the directory you name and, if the server runs as root, the user. To move the data root, stop the service, move the files and edit `dockmesh.env`.

<!-- update after: setup: block internal targets in url test, lock commit -->
The wizard accepts a commit for 30 minutes after the server starts. After that it refuses with "setup window expired"; restart the service (`sudo systemctl restart dockmesh`) to open a new window. Until the wizard is finished, anyone who can reach port 8080 can complete it and become admin, so finish it right away and keep the port closed to untrusted networks until then.

## Install with `dockmesh init`

`dockmesh init` is a terminal wizard that does the same job without a browser. Use it after a binaries-only install (`DOCKMESH_NO_BOOTSTRAP=1`) or a manual install. It must run as root to install the service.

```bash
sudo dockmesh init
```

It asks for the data root, HTTP listen address, admin username and password (leave the password empty to generate one), public base URL, the agent connection URL (default `wss://<host>:8443/connect`), and whether to install and start the service. It then creates the database and admin account, writes `<data dir>/dockmesh.env` and, on Linux, writes a hardened systemd unit to `/etc/systemd/system/dockmesh.service` (a copy stays in the data root), creates the `dockmesh` user, and starts the service. A generated password is printed once.

| Flag | Default | |
|---|---|---|
| `--yes` | off | Non-interactive: accept defaults, generate the admin password. Required when stdin is not a terminal. |
| `--data-dir` | `/var/lib/dockmesh` (`/usr/local/var/dockmesh` on macOS) | Data root. |
| `--listen` | `:8080` | HTTP listen address. |
| `--admin-user` | `admin` | Admin username. |
| `--base-url` | `http://<hostname>:<port>` | Public base URL. |
| `--service` | `true` | Install and start a systemd unit (launchd on macOS). `--service=false` to skip. |

Because `init` creates the admin itself, the server starts without setup mode and you go straight to the login page. Re-running `init` is safe: an existing admin keeps its password (reset it with `sudo dockmesh admin reset-password --user admin`).

> **Note:** `init` writes the listen address as `DOCKMESH_LISTEN`, which the server does not read; the server listens on `DOCKMESH_HTTP_ADDR` (default `:8080`). If you chose another address, add `DOCKMESH_HTTP_ADDR=<address>` to `dockmesh.env` and restart.

## Manual install from a release archive

Each release on [GitHub](https://github.com/dockmesh/dockmesh/releases) has `dockmesh_linux_amd64.tar.gz`, `dockmesh_linux_arm64.tar.gz`, `dockmesh_darwin_amd64.tar.gz`, `dockmesh_darwin_arm64.tar.gz` and a `checksums.txt`. Each archive contains `dockmesh`, `dockmesh-agent`, `dmctl`, `install-agent.sh`, `LICENSE` and `README.md`.

```bash
VER=v0.3.3
curl -fLO https://github.com/dockmesh/dockmesh/releases/download/$VER/dockmesh_linux_amd64.tar.gz
curl -fLO https://github.com/dockmesh/dockmesh/releases/download/$VER/checksums.txt
sha256sum --ignore-missing -c checksums.txt
tar -xzf dockmesh_linux_amd64.tar.gz
sudo install -m 0755 dockmesh dmctl /usr/local/bin/
sudo install -D -m 0755 install-agent.sh /usr/local/share/dockmesh/install-agent.sh
sudo install -D -m 0755 dockmesh-agent /usr/local/share/dockmesh/bin/dockmesh-agent-linux-amd64
sudo dockmesh init
```

Install `grype` yourself if you want image scans ([grype installation](https://github.com/anchore/grype#installation)).

## Build from source

You need Go (the version in `go.mod`), Node.js with npm, `make` and `git`.

```bash
git clone https://github.com/dockmesh/dockmesh.git
cd dockmesh
make build VERSION=v0.0.0-local
```

`make build` builds the web UI, embeds it, and produces `./dockmesh`, the agents `bin/dockmesh-agent-linux-amd64` and `bin/dockmesh-agent-linux-arm64`, and `dmctl` builds for Linux, macOS and Windows in `bin/`. Run `./dockmesh` from the checkout: without an env file it uses `./data/` and `./stacks/` and serves the agent installer from `./scripts/` and `./bin/`. If `/var/lib/dockmesh/dockmesh.env` exists on the machine and `DOCKMESH_DB_PATH` is unset, that file is loaded instead. On first start it creates a user `admin` with a random password and writes it to the log once: the default JSON log line has the message `bootstrap admin created` and the password in its `password` field.

For development, `make dev` starts the backend on `:8080` and the Vite dev server on `:5173`. `make test` runs the Go tests and `svelte-check`.

## Run in Docker

There is no published Dockmesh image. The repository's `Dockerfile` and `docker-compose.yml` build one locally:

```bash
git clone https://github.com/dockmesh/dockmesh.git
cd dockmesh
docker compose up -d --build
docker compose logs dockmesh | grep "bootstrap admin"
```

The compose file publishes port 8080, mounts the Docker socket, and bind-mounts `./data` to `/app/data` and `./stacks` to `/app/stacks`. Because no setup flag is set, the server creates the `admin` user on first start and logs its password instead of showing the setup wizard.

Things to know about the image:

- It is distroless and runs as the non-root user 65532. Make `./data` and `./stacks` writable for that user (`sudo chown -R 65532:65532 data stacks`), and give the container access to the socket by adding `group_add: ["<gid of the docker group on the host>"]` to the service.
- `grype` is not in the image, so image scans are unavailable.
- The agent installer and agent binaries are not in the image, so the one-line agent install from this server does not work unless you mount them and set `DOCKMESH_INSTALL_SCRIPT` and `DOCKMESH_BINARY_DIR`. Port 8443 is not published either; add `8443:8443` if you use remote agents.
- The builder stage uses `golang:1.22-alpine`, older than the Go version `go.mod` requires. The build fails with a Go version error until you change that line to `golang:1.25-alpine`.
- Mounting the socket read-only does not restrict what can be done through it. Whoever controls Dockmesh controls the Docker engine.

## Next steps

Continue with [First steps](./first-steps.md). To remove Dockmesh again, see [Uninstall](../operations/uninstall.md).
