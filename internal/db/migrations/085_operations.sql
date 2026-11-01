-- Long-running work (image updates, backup runs, restores, new proxy
-- sites) with its steps and the last log lines, so its progress can be
-- followed live and looked at after it finished. Steps and log are JSON
-- arrays; times are Unix milliseconds. Rows are pruned after 7 days.
CREATE TABLE operations (
    id          TEXT PRIMARY KEY,
    kind        TEXT NOT NULL,
    target      TEXT NOT NULL DEFAULT '',
    ref         TEXT NOT NULL DEFAULT '',
    group_id    TEXT NOT NULL DEFAULT '',
    host_id     TEXT NOT NULL DEFAULT '',
    stack       TEXT NOT NULL DEFAULT '',
    admin_only  INTEGER NOT NULL DEFAULT 0,
    actor       TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL,
    error       TEXT NOT NULL DEFAULT '',
    steps       TEXT NOT NULL DEFAULT '[]',
    log         TEXT NOT NULL DEFAULT '[]',
    created_at  INTEGER NOT NULL,
    started_at  INTEGER,
    finished_at INTEGER
);
CREATE INDEX operations_created ON operations(created_at);
CREATE INDEX operations_status ON operations(status);
CREATE INDEX operations_ref ON operations(kind, ref);
