---
title: Docker Swarm
description: See a Docker Swarm's nodes, services, tasks and logs, deploy stacks to it, and what Dockmesh does not manage there.
---

Dockmesh can add a Docker Swarm as a cluster, show it and deploy stacks to it. The **Hosts** page shows the cluster's state and its linked node hosts, the cluster page its nodes, services, tasks, stacks and networks and how the work is spread over the nodes, and the **Topology** page its services and overlay networks. The API also lists its stack volumes and streams service logs. Dockmesh deploys a stack to the Swarm as `docker stack deploy` would ([Deploying a stack to a Swarm](#deploying-a-stack-to-a-swarm)); it does not scale or roll back services or change nodes, secrets or configs.

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

## Deploying a stack to a Swarm

On a stack's page, **Preview Swarm deploy…** converts its compose file for a cluster and lists what Dockmesh refuses and warns about. When the stack is deployable, **Deploy to …** deploys exactly the plan the preview showed; if the files, the active environment or the global environment changed since, the deploy asks for a new preview.

- **Through a verified manager.** The deploy goes through a manager whose agent proved its node identity ([Node identity](./multi-host.md#node-identity)), or the server's own host when it is a manager. A manager that is offline is skipped before anything is written.
- **Like `docker stack deploy`.** Services are named `<stack>_<service>` and networks `<stack>_<network>`. Images are pinned to their digest. Registry credentials Dockmesh holds for the manager are sent along and stored in the service, as with `--with-registry-auth`, so every node that runs a task can pull. External networks, secrets and configs must exist on the Swarm.
- **Only its own objects.** Dockmesh updates only services and networks it created for the stack. A service or network with the same name created by `docker stack deploy` or by hand refuses the deploy, and so does an existing network that is less isolated than the file asks for (not internal or not encrypted). Other differences of an existing network are warnings: the network is kept as it is.
- **The outcome as it is.** A write that fails stops the deploy; the writes before it stay. When the manager does not answer, Dockmesh does not send the write again to another manager: it reads the services back from the cluster and shows per service whether it runs the new spec.
- **Leftovers.** Services Dockmesh deployed for the stack and networks it created that the file no longer has are listed in the preview. A deploy leaves them running unless **Remove these … after the deploy has applied** is ticked; they are removed only after every write of the deploy was applied, and only objects Dockmesh created for this stack. A new leftover after the preview asks for a new preview.
- **Same target.** A stack deployed on a host is not moved to a cluster, and a stack on a cluster is not deployed on a host. Dependencies must already run on the same cluster.
- **Environment.** The deploy uses the stack's active environment. Its values are part of the service specs, readable with `docker service inspect` on a manager; use Swarm secrets for real secrets.

## Adopting a stack deployed with `docker stack deploy`

The cluster page's **Stacks** tab marks stacks deployed with `docker stack deploy` as **External**. **Adopt** reads such a stack through a verified manager and shows the compose file Dockmesh would keep for it before anything is written:

- **The file.** Services, images (without the digest), commands, labels, healthchecks, ports, replicas and mode, restart policy, placement, resources, update and rollback config, mounts, networks, and references to secrets and configs. Environment values and volume and network driver options go to the stack's environment file, which is stored encrypted; the preview shows only the variable names. Commands, labels and healthchecks stay in the compose file, which everyone with Stacks: View on the stack can read.
- **What the next deploy would change.** Per service, the spec fields in which a deploy from this file differs from what runs. Settings the file can't hold are listed under **Not carried over**; a deploy from Dockmesh would drop them.
- **Networks stay.** Networks the CLI created for the stack are used as external networks. Dockmesh never changes or removes them, also not when pruning.
- **Refused.** A stack whose file Dockmesh would refuse to deploy (see the refuse list above) can't be adopted.

**Adopt as Dockmesh stack** confirms exactly that preview; if a service changed on the cluster since, it asks for a new preview. Each service gets the `dockmesh.spec-hash` label in a label-only update: no task is replaced and nothing is redeployed. Then the stack's files are written and the stack is recorded on the cluster. If not every service could be labelled, no stack is created and adopting again is safe. Adopting needs Stacks: Manage (`stacks.adopt`) and `containers.env` on the stack on the cluster, and a name no Dockmesh stack uses.

## Not supported

- Scaling a Swarm service, rolling back or resuming an update from Dockmesh.
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
- the stack volumes per node with Volumes: View and Hosts: View;
- adopting an External stack with `stacks.adopt` (Stacks: Manage) and `containers.env`;
- deploying the stacks already on the cluster with Stacks: Deploy. A stack that is not deployed anywhere yet has no target; its first deploy to a cluster needs an assignment on everything or on the stack.

A deploy runs its services on any node of the Swarm, so Stacks: Deploy on a cluster reaches every node. It does not cover the hosts that are its nodes or their containers; give those a host or host tag assignment. A role on a host that is a node does not reach the cluster: such a user sees "Member of cluster X" on the host, without the cluster's state, other nodes or services. Creating and deleting clusters needs `clusters.create`, which is only granted on everything. See [Permissions](../concepts/permissions.md).

> **Warning:** A user who can open a terminal (`containers.exec`) on a node host can read the secrets of the task containers there under `/run/secrets`, just as the terminal shows a container's environment.

## Nodes without an agent

A node needs no Dockmesh agent to appear: the manager's node list includes every node, and service logs come through the manager, so they include tasks on such nodes. A node without an agent is not a host in Dockmesh, though. It has no host page, its containers and metrics are not shown, and its volumes are not read, so the volume list leaves it out. The Hosts page counts these nodes on the cluster's row; the cluster page lists them with their tasks and says why their CPU and memory are missing.

## Resilience

Dockmesh reads a cluster through one manager host at a time, and only through managers whose agent proved its node identity (or the server's own host when it is a manager). While no manager is verified yet, for example right after an upgrade, it reads through the host the cluster was created from (the origin), which must be a manager with an agent; once managers are verified, an unverified origin is no longer used. When the current manager goes away, reads switch to another verified manager, which needs at least two verified managers. While a changed root CA waits to be accepted, reads stay on the origin if it is verified or no manager is, and don't fail over ([Node identity](./multi-host.md#node-identity)). While no manager can be read, the cluster shows as **Unreachable** with the node counts it last knew and when, and reading nodes, services, tasks, networks, service logs or the topology answers 503. The stack volume list still reads the node hosts directly. The Swarm itself keeps running.

A Swarm keeps working while a majority of its managers is reachable, so it survives the loss of a manager only with three or more managers. With fewer, losing a manager leaves it without quorum. The Hosts page shows that as **No quorum** while a manager Dockmesh reads through (a verified one, or the origin before any is verified) is still online, and as **Unreachable** when none is.
