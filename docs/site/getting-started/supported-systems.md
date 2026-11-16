---
title: Supported systems
description: Which Linux distributions, architectures and Docker versions Dockmesh is tested on, what should work, and what is not supported.
---

This page applies to the server and to agents alike.

## Tested on every release

For every release, the server and agents are installed, upgraded, restarted and uninstalled on these systems:

| System | Architecture | Docker |
|---|---|---|
| Ubuntu 24.04 | x86_64 | Docker CE from Docker's repository |
| Debian 12 | x86_64 | Docker from Debian's own packages (`docker.io`, 20.10) |
| Rocky Linux 9, with SELinux enforcing | x86_64 | Docker CE from Docker's repository |

Rocky Linux 9 also stands for the rest of the RHEL 9 family (RHEL, AlmaLinux). Upgrades from earlier releases are tested on Ubuntu 24.04. On arm64 (Ubuntu 24.04, with the Docker Engine of GitHub's arm64 runner) a smaller server test runs nightly and for every release tag: install with a setup file, deploy a stack, back up and restore a volume, uninstall. Agents on arm64 are not tested.

## Should work, not tested

Other Linux distributions should work if they meet all of these:

- **systemd** as the init system. The server unit, the updater behind **Update now** and the agent unit are systemd units.
- **x86_64 or arm64.** The installers stop on any other architecture.
- **Docker Engine 20.10 or newer**, reachable at `/var/run/docker.sock`. The setup wizard warns below 20.10, and Dockmesh can't talk to engines older than 19.03.
- For the server installer: `curl`, `tar`, `sha256sum` and OpenSSL 3 (for the signature check). The agent installer needs `sha256sum` and installs Docker from `get.docker.com` when it is missing.

The server and agent binaries are statically linked, so the glibc or musl version doesn't matter.

## Not supported

| System | Reason |
|---|---|
| Linux without systemd (Alpine with OpenRC, Devuan, …) | The server installer only installs the binaries and starts no service, so nothing starts the server on boot or restarts it. Setup files and **Update now** need systemd, and the agent installer stops without it. |
| macOS with Docker Desktop as a server | The installer has a macOS path (a launchd job), but no release test covers it and **Update now** is Linux only. The agent installer stops on macOS. |
| Windows, with or without Docker Desktop | There are no Windows builds of the server or the agent, only of `dmctl`. |
| WSL | Not tested. A WSL distribution isn't a standalone host: it usually uses Docker Desktop's engine and runs only while Windows keeps it running. |
| Rootless Docker | A rootless engine's socket belongs to one user and lies in their private runtime directory. The units only give the service access to a socket owned by root. Don't loosen the permissions of `/run/user/<uid>` or run the service as a login user to work around this. |
| 32-bit and other architectures (armv7, i386, …) | No builds; the installers stop. |

## SELinux and AppArmor

From v0.6.0, SELinux in enforcing mode is supported on the RHEL family: the server installer resets the labels of the files it installs, and the agent installer labels the agent binary so systemd may run it. Install `policycoreutils-python-utils` before you run the agent installer, so the label survives a full relabel. See [Multi-host](../features/multi-host.md#what-the-installer-does).

The default AppArmor profiles of Ubuntu and Debian need no changes.
