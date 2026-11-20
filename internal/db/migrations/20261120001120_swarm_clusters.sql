-- swarm_clusters
-- A cluster is a Docker Swarm someone chose to manage; id is the Swarm's
-- cluster ID. origin_host is the manager host it was created from.
CREATE TABLE clusters (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    origin_host TEXT NOT NULL,
    created_by  TEXT,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- A host with an agent (or the server's own host) that a manager's node
-- list confirmed as a node of the cluster. role is the manager's view.
CREATE TABLE cluster_memberships (
    host_id    TEXT PRIMARY KEY,
    cluster_id TEXT NOT NULL REFERENCES clusters(id) ON DELETE CASCADE,
    node_id    TEXT NOT NULL,
    role       TEXT NOT NULL CHECK (role IN ('manager', 'worker')),
    linked_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (cluster_id, node_id)
);
