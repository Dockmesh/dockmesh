---
title: Introduction
description: What Dockmesh is, what it manages, and what it deliberately leaves out.
---

Dockmesh is a self-hosted manager for Docker hosts. One server gives you a web UI and a REST API for compose stacks, containers, images, volumes and networks on the machine it runs on, and on any number of remote hosts that run a small agent.

It is meant for people who already run Docker and Docker Compose and want one place to deploy, watch and back up what runs on their hosts, without moving to a cluster scheduler.

## What it manages

- **Stacks**: compose projects stored as `compose.yaml` files on disk. You edit, deploy, stop and roll them back from the UI, from `dmctl`, or by editing the files directly. See [Stacks](../concepts/stacks.md).
- **Containers, images, volumes, networks** on every connected host: logs, a terminal into running containers, live stats, and (on the local host) a file browser.
- **Hosts**: the local host the server runs on, plus remote hosts whose agents connect to the server. See [Hosts and agents](../concepts/hosts-and-agents.md).
- **Around the stacks**: users and roles, single sign-on, API tokens, backups, alerts and notifications, an audit log, image vulnerability scans and an optional reverse proxy.

## The three binaries

| Binary | Runs on | Purpose |
|---|---|---|
| `dockmesh` | the server host | The server: HTTP API, embedded web UI, SQLite database, scheduler. Talks to the local Docker engine through the Docker API. Also carries admin subcommands (`init`, `doctor`, `admin`, `db`, `restore`, ...). |
| `dockmesh-agent` | each remote host | Connects out to the server over a mutually authenticated TLS WebSocket and carries out Docker operations on its host. Remote hosts need no open inbound ports. |
| `dmctl` | your workstation or CI | Command-line client for the REST API: log in, list and deploy stacks, follow logs, run backups. See [dmctl](../reference/dmctl.md). |

Each release archive contains all three. The web UI is compiled into `dockmesh`, so there is no separate frontend to deploy.

## Design choices

- **Compose files on disk are the source of truth.** Each stack is a directory under the stacks root (`<stacks>/<name>/compose.yaml`, plus an optional `.env`). The database holds runtime state such as deployments, history, users and settings, not your stack definitions. You can keep the stacks directory in Git or edit it over SSH.
- **No Kubernetes, no Swarm.** Dockmesh drives plain Docker engines through the Docker API and reconciles compose files itself. There is no cluster, no scheduler that places workloads for you and no overlay networking between hosts. Each stack is deployed to one host you choose.
- **SQLite only.** The server keeps its state in one SQLite file. There is no option to use Postgres or MySQL.
- **Outbound agents.** Remote hosts dial the server; the server never connects to them.
- **Not all compose features are supported.** Dockmesh reads compose files with the compose-spec library but creates containers itself, and some keys (for example `depends_on`, `deploy`, `devices`, `logging`) are ignored. [Stacks](../concepts/stacks.md) lists what is applied.
- **External helpers stay external.** The reverse proxy, when you enable it, is a separate Caddy container named `dockmesh-proxy` that the server manages over Caddy's admin API. Vulnerability scanning runs the `grype` binary as a subprocess; the installer puts it on the host for you.

## License

Dockmesh is free software under the GNU Affero General Public License, version 3 only (AGPL-3.0-only). Every feature, including roles and single sign-on, is in the open-source code; there is no paid tier. If you modify Dockmesh and offer it to others over a network, the AGPL requires you to make your modified source available to them. The full text is in the `LICENSE` file of the [repository](https://github.com/dockmesh/dockmesh).

## Next steps

- [Installation](./installation.md)
- [First steps](./first-steps.md)
- [Architecture](../concepts/architecture.md)
