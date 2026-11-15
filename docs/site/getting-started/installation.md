---
title: Installation
description: Install the Dockmesh server with the installer script, by hand, or from source.
---

The recommended path is the installer script on a Linux host that already runs Docker. It installs the binaries, creates a service account and a systemd unit, starts the server and hands you over to a setup wizard in the browser.

## Requirements

| Requirement | Details |
|---|---|
| Operating system | Linux on amd64 or arm64. macOS (amd64 or arm64) is also handled by the installer but is less tested; the setup wizard warns on anything other than Linux. The server and agent do not run on Windows. |
| Docker | A running Docker Engine reachable at `/var/run/docker.sock`. 20.10 or newer is recommended. The installer does not install Docker; it prints the command for your distribution if Docker is missing. When the socket belongs to another group than `docker` (Synology, Podman), the server and agent units get that group as well. On macOS, enable **Allow the default Docker socket to be used** in Docker Desktop's advanced settings. |
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

The script first decides whether this is a new install or an upgrade, once, before it changes anything. It counts as an upgrade when any trace of an earlier install exists: the `dockmesh` binary, a `dockmesh.service` unit, a config (`/etc/dockmesh/dockmesh.env`, `dockmesh.env` in `/var/lib/dockmesh`, `/data` or `/usr/local/var/dockmesh`, the one an existing unit names, or `$DOCKMESH_DATA_DIR/dockmesh.env`) or a database at any of those places. An upgrade never prints a setup code and never writes a config.

On a new install the script:

1. Checks the OS, the architecture (`amd64` or `arm64`, from `uname -m`; anything else aborts), required tools (OpenSSL 3 among them, for the signature check), systemd, Docker, and whether ports 8080 and 8443 are free.
2. Looks up the latest release on GitHub, downloads `dockmesh_<os>_<arch>.tar.gz`, checks the minisign signature of the release's `checksums.txt` and then the archive against `checksums.txt`. It aborts when the signature is missing or doesn't verify, on a checksum mismatch, and when no checksum is published for the archive ([Verify a release](../operations/verify-release.md)).
3. Installs the binaries and agent assets (table below), and the `grype` vulnerability scanner unless `grype` is already on the `PATH` or you set `DOCKMESH_NO_SCANNER=1`. If the scanner install fails, Dockmesh still works without image scans.
4. Creates a system user `dockmesh` (no login shell, no home) and adds it to the `docker` group if that group exists. Membership in `docker` is equivalent to root on the host, since it gives full control of the Docker engine. It creates an empty `/var/lib/dockmesh` (mode 750), where setup mode keeps its state.
5. Writes `dockmesh.service` and the updater units `dockmesh-update.path` and `dockmesh-update.service` with `dockmesh system write-unit`.
6. Starts the service in setup mode and prints the setup link, the code and its expiry, and the SHA-256 fingerprint of the certificate setup mode serves:

```
Dockmesh installer

[1/6] Checking the system        Linux amd64 · systemd · Docker 28.3.1
[2/6] Downloading 0.6.0          verified (minisign, key 01A0941E358E1C5A)
[3/6] Installing binaries        /usr/local/bin/dockmesh, dmctl
[4/6] Creating user dockmesh     groups: docker
[5/6] Installing services        dockmesh.service, dockmesh-update.path
[6/6] Starting setup mode        ok

┌ Finish the setup in your browser ─────────────────────────────┐
│                                                               │
│   https://192.168.1.20:8080/setup#K7QD-3MXA-9PRT              │
│                                                               │
│   Setup code   K7QD-3MXA-9PRT                                 │
│   Valid until  14:32 (2 hours)                                │
│                                                               │
│   Your browser will warn about the certificate. Check that    │
│   it shows this SHA-256 fingerprint before you continue:      │
│   3F:A2:…                                                     │
│   …:9C                                                        │
│                                                               │
│   New code:    sudo dockmesh setup new-code                   │
└───────────────────────────────────────────────────────────────┘
```

The new install has no config yet: the setup wizard writes `/etc/dockmesh/dockmesh.env` when you finish it. The code goes only to the terminal, never to a log. When the group of the Docker socket is not `docker` (Synology, Podman), step 4 names it too, for example `groups: docker, dmsock (Docker socket)`. Output is plain text without colours when `NO_COLOR` is set or the output is not a terminal, and the box uses ASCII characters outside a UTF-8 locale.

On an upgrade the script checks and downloads in the same way, then stops the service, writes a snapshot of the database next to it (`dockmesh.db.pre-update.bak`), installs the binaries (the previous server binary is kept as `dockmesh.bak`), moves an old config to `/etc/dockmesh/dockmesh.env` with [`dockmesh system migrate-layout`](../reference/dockmesh-cli.md#system-migrate-layout), rewrites the unit and starts the service again:

```
Dockmesh installer · upgrade 0.5.2 → 0.6.0

[1/6] Checking the system        Linux amd64 · systemd · Docker 28.3.1
[2/6] Downloading 0.6.0          verified (minisign, key 01A0941E358E1C5A)
[3/6] Taking a snapshot          snapshot /var/lib/dockmesh/data/dockmesh.db.pre-update.bak
[4/6] Installing binaries        /usr/local/bin/dockmesh, dmctl (previous kept as dockmesh.bak)
[5/6] Migrating config           config in /etc/dockmesh/dockmesh.env, unit from the template
moved  /var/lib/dockmesh/dockmesh.env → /etc/dockmesh/dockmesh.env (symlink left)
[6/6] Restarting                 ok, down 2.4 s

ok  Dockmesh 0.6.0 is running. Sign in as usual.
```

If the earlier install never finished its setup, a box follows that says how to get a setup code (`sudo dockmesh setup new-code`) and where to open the wizard. A host with only the binary and no unit, config or database gets its binaries replaced but no service.

### What goes where

| Path | Content |
|---|---|
| `/usr/local/bin/dockmesh` | Server binary |
| `/usr/local/bin/dmctl` | CLI client |
| `/usr/local/bin/grype` | Vulnerability scanner (unless skipped) |
| `/usr/local/share/dockmesh/install-agent.sh` | Agent installer served to remote hosts |
| `/usr/local/share/dockmesh/bin/dockmesh-agent-linux-amd64`, `dockmesh-agent-linux-arm64` | Agent binaries served to remote hosts; Linux archives carry both architectures |
| `/var/lib/dockmesh/` | Data root; during setup mode only its `.setup/` state |
| `/etc/dockmesh/dockmesh.env` | Config, written by the setup wizard |
| `/etc/systemd/system/dockmesh.service` | Service unit: runs `dockmesh serve` as `dockmesh` with the `docker` group |

The agent binaries are not installed into `/usr/local/bin`; they are only kept so the server can hand them to remote hosts. The full tree is in [Filesystem layout](../reference/filesystem-layout.md).

The installer no longer picks `/data` by itself; installs that already use `/data` keep it through their config. On a new install `DOCKMESH_DATA_DIR` is not used: choose the data directory in the setup wizard. On macOS the data root is `/usr/local/var/dockmesh`, the installer still writes `<data root>/dockmesh.env` there, the service is the launchd job `/Library/LaunchDaemons/dev.dockmesh.service.plist` running as root, and logs go to `/usr/local/var/log/dockmesh.log` and `dockmesh.err`.

A reinstall over kept data (for example after `uninstall.sh` without `--purge`) is an upgrade: the script keeps the config and the data next to it, recreates the `dockmesh` user and hands the data back to it, so the existing database, stacks and keys are picked up and you sign in with your existing account.

The installer writes the systemd unit with `dockmesh system write-unit` on every install and upgrade, so changes made directly to `/etc/systemd/system/dockmesh.service` are replaced. Put local changes into a drop-in in `/etc/systemd/system/dockmesh.service.d/`, which Dockmesh never touches.

### Installer options

Set these as environment variables. With `sudo`, put them after `sudo` so they reach the script:

```bash
curl -fsSL https://get.dockmesh.dev | sudo DOCKMESH_NO_SCANNER=1 bash
```

| Variable | Default | Effect |
|---|---|---|
| `DOCKMESH_VERSION` | latest release | Release tag to install, e.g. `v0.5.3`; same as `--version v0.5.3` after `bash -s --`. Pre-releases are only installed when named. |
| `DOCKMESH_CHANNEL` | `stable` | Deprecated, removed in 0.7.0: `testing` also considers pre-releases when resolving "latest" and prints a warning. Name the pre-release with `--version` instead. |
| `DOCKMESH_INSTALL_DIR` | `/usr/local/bin` | Where `dockmesh`, `dmctl` and `grype` go. |
| `DOCKMESH_ASSET_DIR` | `<install dir>/../share/dockmesh` | Where the agent installer and agent binaries go. |
| `DOCKMESH_DATA_DIR` | unset | On an upgrade, the data root of the earlier install, if the script can't find it at the default places; it must match the config the script finds. Not used on a new install. |
| `DOCKMESH_NO_SCANNER` | unset | `1` skips the `grype` install. |
| `DOCKMESH_NO_BOOTSTRAP` | unset | `1` on a new install installs the binaries, the `dockmesh` user, the data root and the units, but does not start setup mode. Start it later with `sudo systemctl start dockmesh && sudo dockmesh setup new-code`. |
| `DOCKMESH_SKIP_DOWNLOAD` | unset | `1` uses a `dockmesh` binary you placed in the install directory yourself instead of downloading a release. It does not count as an earlier install. |
| `DOCKMESH_NO_SUDO` | unset | `1` never calls `sudo` (the install directory must be writable). |
| `DOCKMESH_FORCE` | unset | `1` reinstalls even if the installed version is already the target version. |
| `DOCKMESH_SKIP_SIGNATURE` | unset | `1` trusts `checksums.txt` without checking its signature and prints a warning. Needed for releases before v0.6.0 (unsigned) and hosts without OpenSSL 3. |
| `DOCKMESH_SKIP_CHECKSUM` | unset | `1` installs without checking the signature or the archive against `checksums.txt`. The binary runs as root, so skip the check only for a build you verified yourself. |
| `NO_COLOR` | unset | Disables colored output. |

### The config file

The setup wizard writes `/etc/dockmesh/dockmesh.env` (owner root, group `dockmesh`, mode 640) when it finishes. Installs from before 0.6 kept `dockmesh.env` in the data root; the next upgrade moves it to `/etc/dockmesh/dockmesh.env` and leaves a symlink. An old file that still has `DOCKMESH_SETUP_FORCE=true` belongs to an install whose wizard never finished: it opens setup mode only after `sudo dockmesh setup new-code`. Edit the file and restart the service to change settings. All variables are listed in [Environment variables](../reference/environment-variables.md).

`dockmesh` subcommands such as `dockmesh admin list-users` load `/etc/dockmesh/dockmesh.env`, else the first `dockmesh.env` they find in `/var/lib/dockmesh`, `/data` or `/usr/local/var/dockmesh`. If your data root is elsewhere, pass `--env-file <path>`. Run them as the service user (`sudo -u dockmesh dockmesh …`), because the data root is only readable by the service account and files they create keep the right owner.

## The setup wizard

Open the link the installer printed (`http://<host>:8080/setup#<code>`) and select **Continue**: the code from the link is filled in. Without the link, open `/setup` and type the code. Before a right code is entered, the server answers only the code form, a bare status endpoint and the health checks; every other page redirects to `/setup` and every other API call, including restore and the wizard's own checks, is refused with 401.

The setup code is valid for 2 hours and cannot be shown again. `sudo dockmesh setup new-code` prints a new one and the old one stops working. After 5 wrong codes from one address, that address waits 15 minutes; after 20 wrong codes in total the code is locked until `new-code` runs again, so someone on your network guessing codes blocks setup until you create a new one. A right code opens one setup session in that browser; entering the code in another browser takes the session over. The session ends when the code expires. Once you start the install it can still follow the progress, but changes nothing more.

To move an existing install to this machine instead, choose **Restore from a backup instead** on the first page and upload a system backup (plus the backup key file exported from the old server if the backup is encrypted). The server checks the backup, ends setup mode and restarts into the restored install; see [Disaster recovery](../operations/disaster-recovery.md).

The wizard has seven steps. Nothing is written until you confirm on the review step:

1. **Welcome** runs preflight checks: operating system, CPU, RAM, root disk space, Docker Engine, Docker socket, time synchronisation. A failed Docker check blocks the wizard.
2. **Data** asks for the data directory. It proposes `/var/lib/dockmesh`.
3. **Service user** asks which OS account Dockmesh runs as (`dockmesh` is recommended).
4. **Admin** creates the first administrator: username, password (at least 12 characters), optional email.
5. **URL** asks for the public URL users and agents use to reach the server. It defaults to the address in your browser; enter the `https://` address of your reverse proxy instead of accepting a plain `http://<host>:8080`. **Test connection** checks that the server can reach the address; it only connects to public addresses and to this host's own private addresses on ports 80, 443 and the port the wizard is served on.
6. **Review** shows your answers. **Install** starts the installation.
7. **Done** streams the progress of the installation and says whether it finished or failed, then shows the **Login URL** and your username.

On install the server creates the admin account, stores the public URL as the **Base URL** setting together with an **Agent public URL** on the same host and the agent port (`wss://<host>:8443/connect`), and removes `DOCKMESH_SETUP_FORCE` from `dockmesh.env`. Sign in afterwards at the URL shown.

Agent install commands, single sign-on redirect URLs and invite links use these settings; change them under **Settings** → **General** without restarting the server. `DOCKMESH_BASE_URL` and `DOCKMESH_AGENT_PUBLIC_URL` in `dockmesh.env` win over the settings when set.

Port 8080 is plain HTTP. Before you use Dockmesh beyond a trusted LAN, put a TLS reverse proxy in front and keep 8080 off the network; see [Security hardening](../operations/hardening.md) and [Ports](../reference/ports.md).

The data directory and service user answers do not relocate anything: the server keeps using the paths in `dockmesh.env` and keeps running as the user from the unit file. The wizard only creates the directory you name and, if the server runs as root, the user. To move the data root, stop the service, move the files and edit `dockmesh.env`.

Only one install runs at a time. Setup mode still serves plain HTTP, so the code and the admin password cross the network unencrypted: finish the wizard from a trusted network and keep port 8080 closed to everyone else until then.

## Install with `dockmesh init`

`dockmesh init` is a terminal wizard that does the same job without a browser. Use it after a binaries-only install (`DOCKMESH_NO_BOOTSTRAP=1`) or a manual install. It must run as root to install the service.

```bash
sudo dockmesh init
```

It asks for the data root, HTTP listen address, admin username and password (leave the password empty to generate one), public base URL, the agent connection URL (default `wss://<host>:8443/connect`), and whether to install and start the service. It then creates the database and admin account, writes `<data dir>/dockmesh.env` (with `DOCKMESH_HTTP_ADDR`, `DOCKMESH_BASE_URL`, `DOCKMESH_AGENT_PUBLIC_URL`, `DOCKMESH_AGENT_SANS` and the paths) and, on Linux, writes a hardened systemd unit to `/etc/systemd/system/dockmesh.service` (a copy stays in the data root), creates the `dockmesh` user, and starts the service. A generated password is printed once.

| Flag | Default | Effect |
|---|---|---|
| `--yes` | off | Non-interactive: accept defaults, generate the admin password. Required when stdin is not a terminal. |
| `--data-dir` | `/var/lib/dockmesh` (`/usr/local/var/dockmesh` on macOS) | Data root. |
| `--listen` | `:8080` | HTTP listen address. |
| `--admin-user` | `admin` | Admin username. |
| `--base-url` | `http://<hostname>:<port>` | Public base URL. |
| `--service` | `true` | Install and start a systemd unit (launchd on macOS). `--service=false` to skip. |

Because `init` creates the admin itself, the server starts without setup mode and you go straight to the sign-in page. Re-running `init` is safe for the account: an existing admin keeps its password (reset it with `sudo -u dockmesh dockmesh admin reset-password --user admin`). It does write `dockmesh.env` again.

## Manual install from a release archive

Each release on [GitHub](https://github.com/dockmesh/dockmesh/releases) has `dockmesh_linux_amd64.tar.gz`, `dockmesh_linux_arm64.tar.gz`, `dockmesh_darwin_amd64.tar.gz`, `dockmesh_darwin_arm64.tar.gz` and a `checksums.txt`, from v0.6.0 on with its signature `checksums.txt.minisig`. Each archive contains `dockmesh`, `dmctl`, `install-agent.sh`, `LICENSE`, `README.md` and the agent: the Linux archives `dockmesh-agent-linux-amd64` and `dockmesh-agent-linux-arm64`, the macOS archives a single `dockmesh-agent`.

```bash
VER=v0.6.0
ARCH=amd64   # or arm64
curl -fLO https://github.com/dockmesh/dockmesh/releases/download/$VER/dockmesh_linux_$ARCH.tar.gz
curl -fLO https://github.com/dockmesh/dockmesh/releases/download/$VER/checksums.txt
curl -fLO https://github.com/dockmesh/dockmesh/releases/download/$VER/checksums.txt.minisig
minisign -Vm checksums.txt -P RWRaHI41HpSgAcJDtFP8X2HnLIjHDLUdnj0GyCoGiGCdDW1rlLDWXzK1 | grep -Fqx "Trusted comment: dockmesh $VER checksums.txt" || { echo "bad signature, aborting"; exit 1; }
sha256sum --ignore-missing -c checksums.txt
tar -xzf dockmesh_linux_$ARCH.tar.gz
sudo install -m 0755 dockmesh dmctl /usr/local/bin/
sudo install -D -m 0755 install-agent.sh /usr/local/share/dockmesh/install-agent.sh
sudo install -D -m 0755 -t /usr/local/share/dockmesh/bin dockmesh-agent-linux-amd64 dockmesh-agent-linux-arm64
sudo dockmesh init
```

The `minisign` line checks the signature and that its trusted comment names `$VER`. Details and the rotation key are in [Verify a release](../operations/verify-release.md).

Install `grype` yourself if you want image scans ([grype installation](https://github.com/anchore/grype#installation)).

## Build from source

You need Go (the version in `go.mod`), Node.js with npm, `make` and `git`.

```bash
git clone https://github.com/dockmesh/dockmesh.git
cd dockmesh
make build VERSION=v0.0.0-local
```

`make build` builds the web UI, embeds it, and produces `./dockmesh`, the agents `bin/dockmesh-agent-linux-amd64` and `bin/dockmesh-agent-linux-arm64`, and `dmctl` builds for Linux, macOS and Windows in `bin/`. Run `./dockmesh` from the checkout: without an env file it uses `./data/` and `./stacks/` and serves the agent installer from `./scripts/` and `./bin/`. If one of the default env files (`/var/lib/dockmesh/dockmesh.env`, `/data/dockmesh.env`, `/usr/local/var/dockmesh/dockmesh.env`) exists on the machine and `DOCKMESH_DB_PATH` is unset, that file is loaded instead. The server never creates an account by itself. Create the first admin before the first start with `./dockmesh admin create --username <name> --role admin` (it asks for the password), or open the setup wizard at `/setup` on a machine with no env file and no database.

For development, `make dev` starts the backend on `:8080` and the Vite dev server on `:5173`. `make test` runs the Go tests (with `-race`), `svelte-check` and the web unit tests.

The repository's `Dockerfile` is for development. No container image is published.

## Next steps

Continue with [First steps](./first-steps.md). For later releases see [Upgrade](../operations/upgrade.md); to remove Dockmesh again, see [Uninstall](../operations/uninstall.md).
