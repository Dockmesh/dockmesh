---
title: Networks
description: List and inspect Docker networks, see which containers are attached, and create, remove or prune networks.
---

The **Networks** tab on the **Resources** page lists the Docker networks on the host selected in the host switcher, or on every host with **All hosts**. Stacks create their own project networks on deploy (named `<stack>_<key>` unless the compose file sets a `name`), so you mostly use this tab to look things up.

## The list

| Column | Shows |
|---|---|
| name | Network name |
| driver / scope | For example `bridge` / `local` |
| subnet | From the IPAM configuration |
| flags | Internal, attachable |
| containers | Number of attached containers |
| created | Creation time |

The pills **user**, **internal**, **attachable** and **empty** filter the list. Docker's built-in networks `bridge`, `host` and `none` are always sorted first.

## Network details

Click a network to open its page:

- **Overview**: name, ID, driver, scope, creation time, the internal, attachable and IPv6 flags, labels and driver options.
- **Containers**: attached containers with their IP and MAC addresses.
- **IPAM**: subnets, gateways and IP ranges.

The **Topology** page in the sidebar draws hosts, networks and containers as a graph, if you prefer a picture.

## Creating a network

**Create network** asks for a **Name** and a **Driver**, `bridge` or `overlay`. `overlay` only works on a Docker engine in Swarm mode. The network is created on the local host with default IPAM settings; subnet, gateway and the internal flag cannot be set here. If you need those, declare the network in a stack's `compose.yaml`:

```yaml
networks:
  backend:
    internal: true
```

Note that Dockmesh ignores the top-level `ipam` key in compose files (see [Compose fields that are silently ignored](../concepts/stacks.md#compose-fields-that-are-silently-ignored)), so custom subnets need to be created with `docker network create` on the host and referenced as `external: true`.

## Removing and pruning

| Action | Effect |
|---|---|
| Delete (row or detail page) | Removes the network. Docker refuses while containers are attached. |
| **Prune empty** | Removes all networks without attached containers. `bridge`, `host` and `none` are kept. |
| **System cleanup…** | Prunes dangling images, unused volumes and unused networks together. |

Creating, deleting and pruning networks always act on the local host, even when a remote host is selected.

Stopping a stack leaves its networks in place, so a stack that is stopped has an empty network that **Prune empty** removes. That is harmless: the next deploy creates it again.

## Permissions

| Action | Permission |
|---|---|
| List, inspect, topology | `networks.view` |
| Create | `networks.create` |
| Delete and prune | `networks.delete` |
