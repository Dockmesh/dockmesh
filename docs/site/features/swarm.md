---
title: Docker Swarm
description: See a Docker Swarm's nodes, services, tasks and logs read-only, and what Dockmesh does not manage there.
---

Dockmesh can add a Docker Swarm as a cluster and show it read-only. The **Hosts** page shows the cluster's state and its linked node hosts, the cluster page its nodes, services, tasks, stacks and networks and how the work is spread over the nodes, and the **Topology** page its services and overlay networks. The API also lists its stack volumes and streams service logs. Dockmesh does not deploy stacks to the Swarm or change its services, nodes, secrets or configs.

Hosts that are not Swarm nodes are unaffected. On a host that is a Swarm node, Dockmesh leaves the Swarm's task containers alone, refuses to migrate Swarm stacks and labels Swarm stack volumes and networks, even before a cluster exists. The Hosts page then offers to create one. Everything else on this page needs a cluster.

## What Dockmesh shows

- **Clusters.** When a host manages a Swarm, the **Hosts** page shows "Docker Swarm detected" to users with `clusters.create`; **Create cluster** adds the Swarm as a cluster. Dockmesh never creates a cluster on its own. The Hosts page shows the cluster as one expandable row with its state and its linked hosts. See [Docker Swarm clusters](./multi-host.md#docker-swarm-clusters).
- **Cluster page.** The cluster's name opens a read-only page with the state and resilience, the workload distribution per node or per service with a hint when one node runs markedly more than its share, and tabs for nodes, services (grouped by stack, with CPU and memory), tasks (failed and restarting ones marked), stacks and networks. See [Cluster page](./multi-host.md#cluster-page).
- **Nodes, services, tasks and networks.** The cluster page and the API read them through the cluster's manager ([Reading a cluster through the API](./multi-host.md#reading-a-cluster-through-the-api)). A service shows its environment variable names but never their values, and its secrets and configs by name only.
- **Service logs.** Through the API, one WebSocket streams the logs of every task of a service, each line with its task and node. Tasks on nodes without an agent are included, as long as their log driver is one Docker can read back, such as `json-file`, `local` or `journald` ([Service logs](./multi-host.md#service-logs)).
- **Topology.** The **Topology** page can show a cluster's services and the overlay networks they attach to ([Networks](./networks.md)). The page offers a cluster to users with Hosts: View, Networks: View and Stacks: View on it.
- **Stack volumes.** Volumes and networks created by `docker stack deploy` show as **Swarm stack** with the stack's name on the node hosts ([Volumes](./volumes.md)). The API lists per stack which linked hosts (nodes with an agent) hold a copy of each volume ([Reading a cluster through the API](./multi-host.md#reading-a-cluster-through-the-api)).
- **Task containers.** On any host that is a Swarm node, with or without a cluster, the containers Swarm runs for a service are not image-updated, recreated or restarted by Dockmesh; **Replace task** stops one so Swarm starts a new task ([Swarm task containers](./image-updates.md#swarm-task-containers)).
- **Agent upgrades.** Agents on the managers of one cluster are upgraded one after another: the next once the previous has reconnected, or after five minutes ([Upgrading agents](../operations/upgrade.md#upgrading-agents)).

## Not supported

- Deploying a stack to a Swarm, scaling a service, updating or rolling back a service. Stacks in Dockmesh are deployed to one host; use `docker stack deploy` on a manager for Swarm stacks.
- Creating or removing Swarm secrets and configs, changing node availability, adding or removing nodes.
- Proxy routes to a Swarm service. A route can point at a published port of a node like any other upstream.
- Backups of a Swarm stack as a whole. A volume job on a node host backs up the copy of the volume on that node.
- Migrating or draining Swarm stacks. Migration refuses a stack whose containers on the source are Swarm tasks, and on a Swarm node also a stack with no running container there ([Migration and drain](./migration-and-drain.md#which-stacks-can-be-migrated)).
- Kubernetes.

## Permissions

A role can be assigned on a cluster (scope type `cluster`). It covers the cluster itself:

- the node list with Hosts: View;
- services and tasks with Stacks: View;
- overlay networks with Networks: View;
- the topology with Networks: View and Stacks: View;
- service logs with `containers.logs` (Containers: View or Stacks: View);
- the stack volumes per node with Volumes: View and Hosts: View.

It does not cover the hosts that are its nodes or their containers; give those a host or host tag assignment. A role on a host that is a node does not reach the cluster: such a user sees "Member of cluster X" on the host, without the cluster's state, other nodes or services. Creating and deleting clusters needs `clusters.create`, which is only granted on everything. See [Permissions](../concepts/permissions.md).

> **Warning:** A user who can open a terminal (`containers.exec`) on a node host can read the secrets of the task containers there under `/run/secrets`, just as the terminal shows a container's environment.

## Nodes without an agent

A node needs no Dockmesh agent to appear: the manager's node list includes every node, and service logs come through the manager, so they include tasks on such nodes. A node without an agent is not a host in Dockmesh, though. It has no host page, its containers and metrics are not shown, and its volumes are not read, so the volume list leaves it out. The Hosts page counts these nodes on the cluster's row; the cluster page lists them with their tasks and says why their CPU and memory are missing.

## Resilience

Dockmesh reads a cluster through one manager host at a time, and only through managers whose agent proved its node identity (or the server's own host when it is a manager). While no manager is verified yet, for example right after an upgrade, it reads through the host the cluster was created from (the origin), which must be a manager with an agent; once managers are verified, an unverified origin is no longer used. When the current manager goes away, reads switch to another verified manager, which needs at least two verified managers. While a changed root CA waits to be accepted, reads stay on the origin and don't fail over ([Node identity](./multi-host.md#node-identity)). While no manager can be read, the cluster shows as **Unreachable** with the node counts it last knew and when, and reading nodes, services, tasks, networks, service logs or the topology answers 503. The stack volume list still reads the node hosts directly. The Swarm itself keeps running.

A Swarm keeps working while a majority of its managers is reachable, so it survives the loss of a manager only with three or more managers. With fewer, losing a manager leaves it without quorum. The Hosts page shows that as **No quorum** while a manager Dockmesh reads through (a verified one, or the origin before any is verified) is still online, and as **Unreachable** when none is.
