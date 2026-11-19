---
title: Installation
description: Install the Dockmesh server with the installer script, by hand, or from source.
---

The recommended path is the installer script on a Linux host that already runs Docker. It installs the binaries, creates a service account and a systemd unit, starts the server and hands you over to a setup wizard in the browser.

## Requirements

| Requirement | Details |
|---|---|
| Operating system | Linux with systemd on amd64 or arm64. Without systemd the installer only installs the binaries ([not supported](./supported-systems.md#not-supported)). The installer also has a macOS path, but macOS is not supported as a server. The server and agent do not run on Windows. Tested distributions and Docker versions: [Supported systems](./supported-systems.md). |
| Docker | A running Docker Engine reachable at `/var/run/docker.sock`. 20.10 or newer is recommended; the setup wizard warns below it. The installer does not install Docker; it prints the command for your distribution if Docker is missing. When the socket belongs to another group than `docker` (Synology, Podman), the server and agent units get that group as well. On macOS, enable **Allow the default Docker socket to be used** in Docker Desktop's advanced settings. |
| Tools | `curl`, `tar`, and `sha256sum` (or `shasum` on macOS). |
| Privileges | root or `sudo`. |
| Ports | `8080/tcp` for the web UI and API. `8443/tcp` for remote agents, only needed if you add remote hosts. If you enable the reverse proxy, its Caddy container binds `80` and `443` on the host. See [Ports](../reference/ports.md). |
| Resources | Plan for at least 512 MB RAM. The data directory needs at least 200 MiB free (the setup wizard warns below 2 GiB); images and volumes of your stacks need space on top of that. |
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

The installer never picks `/data` itself; installs that already use `/data` keep it through their config. On Linux a new install does not use `DOCKMESH_DATA_DIR`: choose the data directory in the setup wizard. macOS is [not supported](./supported-systems.md#not-supported); on the installer's macOS path the data root is `/usr/local/var/dockmesh`, the installer still writes `<data root>/dockmesh.env` there, the service is the launchd job `/Library/LaunchDaemons/dev.dockmesh.service.plist` running as root, and logs go to `/usr/local/var/log/dockmesh.log` and `dockmesh.err`.

A reinstall over kept data (for example after `uninstall.sh` without `--purge`) is an upgrade: the script keeps the config and the data next to it, recreates the `dockmesh` user and hands the data back to it, so the existing database, stacks and keys are picked up and you sign in with your existing account.

The installer writes the systemd unit with `dockmesh system write-unit` on every install and on every upgrade of a host with a service, so changes made directly to `/etc/systemd/system/dockmesh.service` are replaced. Put local changes into a drop-in in `/etc/systemd/system/dockmesh.service.d/`, which Dockmesh never touches.

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
| `DOCKMESH_DATA_DIR` | unset | On an upgrade, the data root of the earlier install, if the script can't find it at the default places; it must match the config the script finds. Not used on a new Linux install. |
| `DOCKMESH_NO_SCANNER` | unset | `1` skips the `grype` install. |
| `DOCKMESH_NO_BOOTSTRAP` | unset | `1` on a new Linux install installs the binaries, the `dockmesh` user, the data root and the units, but does not start setup mode. Start it later with `sudo systemctl start dockmesh && sudo dockmesh setup new-code`, or apply a setup file with `sudo dockmesh setup apply --file <file>`. |
| `DOCKMESH_SETUP_FILE` | unset | Absolute path of a [setup file](#install-without-a-browser) to apply instead of starting setup mode; new Linux installs only. |
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

Open the link the installer printed (`https://<host>:8080/setup#<code>`). Your browser warns about the certificate: continue only if it shows the SHA-256 fingerprint the installer printed. The page takes the code from the link into the field, removes it from the address bar at once and waits until you select **Continue**. Without the link, open `/setup` and type the code; lower case, spaces and missing dashes are fine. Before a right code is entered, the server answers only the code form, a bare status endpoint and the health checks; every other page redirects to `/setup` and every other API call, including restore and the wizard's own checks, is refused with 401.

The setup code is valid for 2 hours and cannot be shown again. `sudo dockmesh setup new-code` prints a new one and the old one stops working. After 5 wrong codes from one address, that address waits 15 minutes; after 20 wrong codes in total the code is locked until `new-code` runs again, so someone on your network guessing codes blocks setup until you create a new one. A right code opens one setup session in that browser; entering the code in another browser takes the session over, and the first browser then says so. The session ends when the code expires. Once you start the install it can still follow the progress, but changes nothing more.

After the code, choose **New installation** or **Restore from backup**. A restore asks for the data directory (with the same disk list and checks as below), then for a system backup and, if the backup is encrypted, the backup key file exported from the old server. The server checks the backup (it opens, its database migrates, it has an admin), writes the config, restores the backup into the data directory, restarts once and shows the progress; sign in with an account from the backup afterwards. See [Disaster recovery](../operations/disaster-recovery.md).

A new installation has these steps, listed on the left; select a finished step to go back to it. Nothing is written until you select **Install** on the review step:

1. **Admin** creates the first administrator: username (3 to 32 lower-case letters, digits, dot, dash or underscore), optional email, and a password of at least 12 characters that is not the username. **Generate** fills in a 20-character password and shows it once.
2. **Data directory** proposes `/var/lib/dockmesh` and lists the disks of the server with a **Use** button each; network shares and filesystems without Unix permissions are greyed out, Docker's own directories are not listed. The directory is checked as you leave the field, with the rules the install applies: an absolute path outside system directories and `/var/lib/docker`, whose parent exists and belongs to root alone, not a symbolic link, not there yet or empty and owned by the `dockmesh` account (let setup create it; a directory you create yourself with `mkdir` belongs to root, so `chown dockmesh:dockmesh` it first), on a local filesystem with at least 200 MiB free (below 2 GiB you get a warning). Stacks go to `<data directory>/stacks`; **Change the stacks directory** puts them elsewhere. A stacks directory outside the data directory follows the rules for an agent's stacks directory: setup creates it only as one new directory named `stacks` or starting with `dockmesh-stacks`, directly in `/srv`, `/opt`, `/data`, `/mnt` or `/media` or in a disk mounted below one of them (for example `/srv/stacks` or `/mnt/disk1/stacks`). Anywhere else, prepare it as an empty directory of the `dockmesh` account first (the wizard shows the `sudo install -d -o dockmesh -g dockmesh -m 0750 <path>` command); every directory above it must belong to root. Dockmesh adds it to the service's writable paths.
3. **Address and TLS** asks for the base URL (it defaults to the address in your browser), the web port and the agent port (default 8443). **Test connection** checks that the server can reach the address; it only connects to public addresses and to this host's own private addresses on ports 80, 443 and the port the wizard is served on. A port another program already listens on is refused. For the certificate choose the Dockmesh certificate from this server's own CA (default), your own certificate and key files (Dockmesh reloads them when they change), or **Behind my own reverse proxy**: Dockmesh then serves plain HTTP on the listen address you give (default `127.0.0.1`) and trusts only that address as a proxy unless you list your proxy's addresses or networks under **Trusted proxies** (a network wider than /8, or /32 for IPv6, is refused). Your own certificate is checked as the `dockmesh` user before you go on: a file it cannot read, a key that does not belong to the certificate or an expired certificate stop the step; a certificate that does not cover the base URL's host or ends within 30 days gets a warning. Agents connect to the agent port with mutual TLS; a proxy in front of them has to pass TLS through. The agent URL is derived from the base URL and the agent port; **Change** sets another one, for example when agents reach the server through a different name. **Let Caddy get a certificate** needs a base URL with a public DNS name (`https://dockmesh.example.org`, no IP address) and ports 80 and 443: Dockmesh then listens on `127.0.0.1:8080` only, setup turns the reverse proxy on and creates the proxy route for that name to Dockmesh, and Caddy gets a Let's Encrypt certificate at the first start. The wizard checks that the name resolves; a name that points to another address (behind NAT) gets a warning.
4. **Reverse proxy** turns on the managed Caddy proxy for your stacks (required with the Caddy certificate option, otherwise optional; **Skip for now** leaves it off). It takes an ACME email (required with the Caddy option) and checks that no other program listens on ports 80 and 443 and that Docker can pull the Caddy image. Setup stores the choice; the proxy starts with the first start of the server.
5. **Notifications** adds one notification channel (email, ntfy, Gotify, webhook, Discord, Slack, Teams, Pushover or PagerDuty) with the fields of the channel form under **Alerts**. **Send test** sends a test message right away; link-local and cloud metadata addresses are refused before an admin exists. The channel and its secrets stay in your browser until **Install**; setup removes them from its state once the apply is over. Add more channels under **Alerts** after you sign in.
6. **Review** shows every answer with a link back to its step.
7. **Apply** writes `/etc/dockmesh/dockmesh.env`, creates the data directory, the database and your account, restarts Dockmesh once and then takes you to the sign-in page at the base URL. If the server does not start with the new configuration, it goes back to setup mode and keeps what it created as `*.failed-<time>`; nothing is deleted. The wizard then offers **Back to data directory** and **Try again**; both ask for the setup code once more, because the restart ended the session. The reason is in `sudo journalctl -u dockmesh-update`. If the server does not answer for 3 minutes, the wizard shows the commands to check the service.

The base URL is also stored as the **Base URL** setting, together with an **Agent public URL** on the same host and the agent port (`wss://<host>:8443/connect`), or the agent URL you set. Agent install commands, single sign-on redirect URLs and invite links use these settings; change them under **Settings** → **General** without restarting the server. `DOCKMESH_BASE_URL` and `DOCKMESH_AGENT_PUBLIC_URL` in `dockmesh.env` win over the settings when set.

An install from before 0.6 whose setup never finished keeps its data directory, port and TLS settings: the wizard skips those choices, creates your account, stores the base URL and does not restart. Its setup mode may still be plain HTTP, so finish it from a trusted network.

## Install without a browser

For Ansible, cloud-init and other installs without a browser, put the wizard's answers into a setup file and hand it to the installer. There is no setup mode and no setup code then:

```bash
# the template: run `dockmesh setup example` on any machine with Dockmesh
# 0.6 or later, edit it, and copy it to the new server
chmod 600 /root/dockmesh-setup.yaml
curl -fsSL https://get.dockmesh.dev | sudo DOCKMESH_SETUP_FILE=/root/dockmesh-setup.yaml bash
```

The installer checks the file first (`dockmesh setup apply --dry-run`), prints a warning for any file that others may read, then applies it with `dockmesh setup apply --file` as its last step (`[6/6] Applying setup file`) and prints a box with the address, the certificate fingerprint when Dockmesh serves its own certificate, and the admin to sign in as. The file format is in the [CLI reference](../reference/dockmesh-cli.md#setup-apply). As root, the setup file and every file it names must belong to root, and they and their directories must be writable only by root. On a host with an earlier install the file is ignored with a warning and the run is an upgrade.

`dockmesh init` still works as a wrapper on `setup apply` (`--yes` or `--file`), prints a deprecation warning and goes away in 0.7.0.

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
sudo useradd --system --no-create-home --shell /usr/sbin/nologin -G docker dockmesh
sudo install -d -m 0750 -o dockmesh -g docker /var/lib/dockmesh
sudo dockmesh system write-unit
sudo systemctl enable --now dockmesh
sudo dockmesh setup new-code
```

Instead of the last two lines, `sudo dockmesh setup apply --file <setup file>` sets the server up from a [setup file](#install-without-a-browser).

The `minisign` line checks the signature and that its trusted comment names `$VER`. Details and the rotation key are in [Verify a release](../operations/verify-release.md).

Install `grype` yourself if you want image scans ([grype installation](https://github.com/anchore/grype#installation)).

## Build from source

You need Go (the version in `go.mod`), Node.js with npm, `make` and `git`.

```bash
git clone https://github.com/dockmesh/dockmesh.git
cd dockmesh
make build VERSION=v0.0.0-local
```

`make build` builds the web UI, embeds it, and produces `./dockmesh`, the agents `bin/dockmesh-agent-linux-amd64` and `bin/dockmesh-agent-linux-arm64`, and `dmctl` builds for Linux, macOS and Windows in `bin/`. Run `./dockmesh` from the checkout; it serves the agent installer from `./scripts/` and `./bin/`. Without config it keeps its data in `/var/lib/dockmesh` (see [Filesystem layout](../reference/filesystem-layout.md)), so set `DOCKMESH_DATA_DIR` to keep a run's data somewhere else, for example `DOCKMESH_DATA_DIR=$PWD ./dockmesh` for `./data/` and `./stacks/`. A checkout that already has `./data/dockmesh.db` from a source run up to 0.5.x keeps using `./data/` and `./stacks/` when the server starts from it without config, and admins see a notice about it; `sudo ./dockmesh system migrate-layout --checkout <checkout>` writes `/etc/dockmesh/dockmesh.env` with `DOCKMESH_DATA_DIR` set to the checkout, the data stays where it is. This only happens when the checkout, its `data/` and the database are not symlinks, are owned by root or by the user the server runs as, and are not writable by group or others, and the same holds for every directory above the checkout; otherwise `serve` prints why, refuses to start and creates nothing at the default data root. Set `DOCKMESH_DATA_DIR` once the checkout passes the check, or start the server from another directory. `migrate-layout --checkout` only accepts a checkout owned by root. Config is an env file at `/etc/dockmesh/dockmesh.env` or one of the old default places (`/var/lib/dockmesh/dockmesh.env`, `/data/dockmesh.env`, `/usr/local/var/dockmesh/dockmesh.env`), `--env-file`, or `DOCKMESH_DATA_DIR` or `DOCKMESH_DB_PATH` in the environment; an env file at a default place is loaded when neither variable is set. The server never creates an account by itself. Create the first admin before the first start with `./dockmesh admin create --username <name> --role admin` (it asks for the password), or open the setup wizard at `/setup` on a machine with no env file, no database and no data files.

For development, `make dev` starts the backend on `:8080` and the Vite dev server on `:5173`. `make test` runs the Go tests (with `-race`), `svelte-check` and the web unit tests.

To run a source build as a service, copy `./dockmesh` to `/usr/local/bin/dockmesh` (root-owned) and run the installer with `DOCKMESH_SKIP_DOWNLOAD=1`; it sets up the user, data root and units around that binary. Build with `VERSION=<tag>` from a release tag, so the server reports that version and agents of the release don't count as outdated. A build without `VERSION` reports `dev`: the update check always offers the latest release and **Update now** is not available. To upgrade, rebuild and repeat these steps.

The repository's `Dockerfile` is for development. No container image is published.

## Next steps

Continue with [First steps](./first-steps.md). For later releases see [Upgrade](../operations/upgrade.md); to remove Dockmesh again, see [Uninstall](../operations/uninstall.md).
