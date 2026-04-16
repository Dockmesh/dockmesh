-- 015_drains.sql
-- P.10: Drain host — move all stacks off a host using bin-packing.
CREATE TABLE IF NOT EXISTS drains (
    id              TEXT    NOT NULL PRIMARY KEY,
    source_host_id  TEXT    NOT NULL,
    status          TEXT    NOT NULL DEFAULT 'planning',  -- planning | executing | paused | completed | aborted
    plan_json       TEXT    NOT NULL DEFAULT '[]',
    started_at      DATETIME,
    completed_at    DATETIME,
    initiated_by    TEXT    NOT NULL,
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_drains_host ON drains(source_host_id);
CREATE INDEX IF NOT EXISTS idx_drains_status ON drains(status);
