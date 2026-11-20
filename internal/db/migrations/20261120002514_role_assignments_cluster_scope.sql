-- role_assignments_cluster_scope
-- Adds the scope type 'cluster' (scope_host = the Swarm cluster id).
-- SQLite can't alter a CHECK, so the table is rebuilt with every row,
-- id and the AUTOINCREMENT counter kept as they are.
CREATE TABLE role_assignments_new (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role_name   TEXT NOT NULL REFERENCES roles(name),
    scope_type  TEXT NOT NULL CHECK (scope_type IN ('global', 'host', 'host_tag', 'stack', 'cluster')),
    scope_host  TEXT NOT NULL DEFAULT '',
    scope_value TEXT NOT NULL DEFAULT '',
    source      TEXT NOT NULL DEFAULT 'manual' CHECK (source IN ('manual', 'idp')),
    via         TEXT NOT NULL DEFAULT '',
    created_by  TEXT,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (user_id, role_name, scope_type, scope_host, scope_value, source, via)
);

INSERT INTO role_assignments_new (id, user_id, role_name, scope_type, scope_host, scope_value, source, via, created_by, created_at)
SELECT id, user_id, role_name, scope_type, scope_host, scope_value, source, via, created_by, created_at FROM role_assignments;

DELETE FROM sqlite_sequence WHERE name = 'role_assignments_new';
INSERT INTO sqlite_sequence (name, seq) SELECT 'role_assignments_new', seq FROM sqlite_sequence WHERE name = 'role_assignments';

DROP TABLE role_assignments;
ALTER TABLE role_assignments_new RENAME TO role_assignments;
CREATE INDEX idx_role_assignments_role ON role_assignments(role_name);
