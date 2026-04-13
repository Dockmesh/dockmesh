-- Auto-update history for one-click updates + rollback (concept §2.2).
-- Keyed on container_name rather than container_id because recreating a
-- container on update assigns it a new id.
CREATE TABLE IF NOT EXISTS update_history (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    container_name  TEXT NOT NULL,
    image_ref       TEXT NOT NULL,
    old_digest      TEXT NOT NULL,
    new_digest      TEXT NOT NULL,
    rollback_tag    TEXT NOT NULL,
    applied_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    rolled_back_at  DATETIME
);

CREATE INDEX IF NOT EXISTS idx_update_history_container ON update_history(container_name);
