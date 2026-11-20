---
title: Docker Swarm
description: What Dockmesh shows of a Docker Swarm, what it does not manage yet, and what it needs on the nodes.
---

Dockmesh can add a Docker Swarm as a cluster and show it read-only: its nodes, services, tasks, service logs, overlay networks and the volumes of its stacks. It does not deploy stacks to the Swarm or change the Swarm yet. Hosts that are not in a Swarm behave exactly as without this support, and nothing changes for an existing install until someone creates a cluster.

## What works today

- **Clusters.** A host that manages a Swarm shows "Docker Swarm detected" to users with `clusters.create`; **Create cluster** adds the Swarm as a cluster. Dockmesh never creates a cluster on its own. The Hosts page shows the cluster as one expandable row with its state and its linked hosts. See [Docker Swarm clusters](./multi-host.md#docker-swarm-clusters).
- **Nodes, services, tasks and networks.** The API reads them through the cluster's manager ([Reading a cluster through the API](./multi-host.md#reading-a-cluster-through-the-api)). A service shows its environment variable names but never their values, and its secrets and configs by name only.
- **Service logs.** One WebSocket streams the logs of every task of a service, each line with its task and node, including tasks on nodes without an agent ([Service logs](./multi-host.md#service-logs)).
- **Topology.** The **Topology** page can show a cluster's services and the overlay networks they attach to ([Networks](./networks.md)).
- **Stack volumes.** Volumes and networks created by `docker stack deploy` show as **Swarm stack X** on the node hosts, and the API lists per stack which nodes hold a copy of each volume.
- **Task containers.** On a node host, the containers Swarm runs for a service are not image-updated, recreated or restarted by Dockmesh; **Replace task** stops one so Swarm starts a new task ([Swarm task containers](./image-updates.md#swarm-task-containers)).
- **Agent upgrades.** Agents on the managers of one cluster are upgraded one after another, never at the same time ([Upgrade](../operations/upgrade.md)).

## Not supported yet

- Deploying a stack to a Swarm, scaling a service, updating or rolling back a service. Stacks in Dockmesh are still deployed to one host; use `docker stack deploy` on a manager for Swarm stacks.
- Creating or removing Swarm secrets and configs, changing node availability, adding or removing nodes.
- Proxy routes to a Swarm service. A route can point at a published port of a node like any other upstream.
- Backups of a Swarm stack as a whole. A volume job on a node host backs up the copy of the volume on that node.
- Migration and drain refuse stacks whose containers are Swarm tasks; Swarm places tasks itself, so drain the node in Swarm or use a placement constraint ([Migration and drain](./migration-and-drain.md#which-stacks-can-be-migrated)).

There is no Kubernetes support.

## Permissions

A role can be assigned on a cluster (scope type `cluster`). It covers the cluster itself: its node list with Hosts: View, services and tasks with Stacks: View, overlay networks with Networks: View, service logs with Containers: Logs, the topology with Networks: View and Stacks: View, and the stack volumes per node with Volumes: View and Hosts: View. It does not cover the hosts that are its nodes or their containers; give those a host or host tag assignment.

A role on a host that is a node does not reach the cluster. Such a user sees "Member of cluster X" on the host, without anything else about the cluster. Creating and deleting clusters needs `clusters.create`, which is only granted on everything. See [Permissions](../concepts/permissions.md).

Whoever has exec on a node can read the secrets of the tasks running there through their containers (`/run/secrets`), the same way exec shows a container's environment.

## Nodes without an agent

A node needs no Dockmesh agent to appear: the manager's node list includes every node, and service logs come through the manager, so they include tasks on such nodes. A node without an agent is not a host in Dockmesh, though. It has no host page, its containers and metrics are not shown, and its volumes are not read, so the volume list leaves it out. The Hosts page counts these nodes on the cluster's row.

## Resilience

Dockmesh reads a cluster through the host it was created from (the origin), which must be a manager with an agent. While that agent is offline, or the host is no longer a manager of the Swarm, the cluster shows as **Unreachable** with its last known state, and the cluster API answers 503. The Swarm itself keeps running; Dockmesh does not switch to another manager.

The Swarm survives the loss of a manager only with 3 or more managers: a Swarm keeps working while a majority of its managers is reachable. With fewer, losing a manager leaves it without quorum, which the Hosts page shows as **No quorum**.
