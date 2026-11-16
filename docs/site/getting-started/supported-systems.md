---
title: Supported systems
description: Which Linux distributions, architectures and Docker versions Dockmesh is tested on, what should work, and what is not supported.
---

This page applies to the server and to agents alike. Both run as systemd services next to a Docker Engine on the same host.

## Tested on every release

Before a release, the server and agents are installed, upgraded, restarted and uninstalled on these systems:

| System | Architecture | Docker |
|---|---|---|
| Ubuntu 24.04 | x86_64 | Docker CE from Docker's repository |
| Debian 12 | x86_64 | Docker from Debian's own packages (`docker.io`, 20.10) |
| Rocky Linux 9, with SELinux enforcing | x86_64 | Docker CE from Docker's repository |
| Ubuntu 24.04 | arm64 | The Docker Engine of GitHub's arm64 runner |

Rocky Linux 9 also stands for the rest of the RHEL 9 family (RHEL, AlmaLinux). Upgrades from earlier releases are tested on Ubuntu 24.04. On arm64 a smaller test of the server runs for every release: install with a setup file, deploy a stack, back up and restore a volume, uninstall. Agents on arm64 are not tested.

## Should work, not tested

Other Linux distributions should work if they meet all of these:

- **systemd** as the init system. The server unit, the updater behind **Update now** and the agent unit are systemd units.
- **x86_64 or arm64.** The installers stop on any other architecture.
- **Docker Engine 20.10 or newer**, reachable at `/var/run/docker.sock`. The setup wizard warns below 20.10, and Dockmesh can't talk to engines older than 19.03. The agent unit also requires the system `docker.service`.
- For the server installer: `curl`, `tar`, `sha256sum` and OpenSSL 3 (for the signature check). The agent installer needs `sha256sum`; it installs `curl` with `apt-get`, `dnf` or `yum`, and Docker from `get.docker.com`, when they are missing.

The server and agent binaries are statically linked, so the glibc or musl version doesn't matter. Container stats work with cgroup v1 and v2.

## Not supported

| System | Reason |
|---|---|
| Linux without systemd (Alpine with OpenRC, Devuan, …) | The server installer only installs the binaries and starts no service. The setup wizard, setup files and **Update now** run through systemd units, and the agent installer stops without systemd. |
| macOS with Docker Desktop as a server | The installer has a macOS path (a launchd job), but no release test covers it and **Update now** is Linux only. Agents can't be installed on macOS. |
| Windows, with or without Docker Desktop | There are no Windows builds of the server or the agent, only of `dmctl`. |
| WSL | Not tested. A WSL distribution isn't a standalone host: it usually uses Docker Desktop's engine and runs only while Windows keeps it running. |
| Rootless Docker | The agent unit requires the system `docker.service`, and the server uses `/var/run/docker.sock` unless `DOCKER_HOST` is set in its config. A rootless engine's socket belongs to one user; that setup is not tested. |
| 32-bit and other architectures (armv7, i386, …) | No builds; the installers stop. |

## SELinux and AppArmor

From v0.6.0, SELinux in enforcing mode is supported on the RHEL family. The server installer resets the SELinux labels of the files it installs. The agent installer gives the agent's binary directory and its refresh script the type `bin_t`, so systemd is allowed to run them: with a `semanage fcontext` rule when `semanage` is installed (package `policycoreutils-python-utils`), otherwise with `chcon`. A full relabel of the filesystem undoes `chcon`, so install `semanage` before you run the agent installer. See [Multi-host](../features/multi-host.md#what-the-installer-does).

The default AppArmor profiles of Ubuntu and Debian need no changes.
