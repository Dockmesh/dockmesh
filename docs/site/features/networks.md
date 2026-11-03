---
title: Networks
description: List and inspect Docker networks, see which containers are attached, and create, remove or prune networks.
---

The **Networks** tab on the **Resources** page lists the Docker networks on the host selected in the host switcher, or on every host with **All hosts**. Stacks create their own project networks on deploy (named `<stack>_<key>` unless the compose file sets a `name`), so you mostly use this tab to look things up. `/networks` redirects to this tab.

## The list

| Column | Shows |
|---|---|
| Name | Network name, below it the stack it belongs to or the short ID |
| Driver / scope | For example `bridge` / `local` |
| Subnet | From the IPAM configuration |
| Flags | Internal, attachable |
| Containers | Number of attached containers, stopped ones included |
| Created | Creation time |

**Show** filters the list to **user**, **internal**, **attachable** or **empty** networks. Docker's built-in networks `bridge`, `host` and `none` are hidden until you tick **show system networks**; they are then sorted first. With **All hosts**, hosts that don't answer within three seconds are left out and counted as unreachable.

Users whose access is limited to some hosts or stacks only see networks on those hosts and the project networks of those stacks.

## Network details

Click a network to open its page; it looks the network up on the host the row belongs to.

The tabs:

- **Overview**: name, ID, driver, scope, creation time, the internal, attachable and IPv6 flags, labels and driver options.
- **Containers**: attached containers with their IPv4, IPv6 and MAC addresses.
- **IPAM**: subnets, gateways, IP ranges and auxiliary addresses.

The **Topology** page in the sidebar draws the networks and containers of the server host as a graph, limited to what your access covers. Agent hosts are not included.

## Creating a network

**Create network** asks for a **Name** and a **Driver** (`bridge`, `overlay`, `macvlan` or `ipvlan`). `overlay` only works on a Docker engine in Swarm mode. The network is created on the server host with default IPAM settings; subnet, gateway and the internal flag can't be set here. If you need the internal or attachable flag, declare the network in a stack's `compose.yaml`:

```yaml
networks:
  backend:
    internal: true
```

Dockmesh ignores the top-level `ipam` key in compose files (see [Compose fields that are silently ignored](../concepts/stacks.md#compose-fields-that-are-silently-ignored)), so custom subnets need to be created with `docker network create` on the host and referenced as `external: true`.

## Removing and pruning

| Action | Effect |
|---|---|
| Delete (row, or **Delete network** on the detail page) | Removes the network. Docker refuses while containers are attached. Not offered for `bridge`, `host` and `none`. |
| **Prune empty** | Removes all networks without attached containers. `bridge`, `host` and `none` are kept. |
| **System cleanup…** | Prunes dangling images, unused volumes and unused networks together. |

Creating, deleting and pruning networks always act on the server host, even when a remote host is selected, and need the permission on the server host.

Stopping a stack leaves its networks in place, so a stopped stack whose containers were removed has an empty network that **Prune empty** removes. That is harmless: the next deploy creates it again.

## Permissions

| Action | Permission |
|---|---|
| List, inspect, topology | `networks.view` (Images, volumes, networks & registries: View) |
| Create | `networks.create` (Manage) |
| Delete and prune | `networks.delete` (Manage) |
