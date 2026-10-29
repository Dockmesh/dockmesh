-- Last result of the image-update watcher per image ref, so a restart
-- neither forgets known updates nor notifies about them again.
-- notified_digest is the remote digest the bell was last raised for.
CREATE TABLE image_update_checks (
    image            TEXT PRIMARY KEY,
    update_available INTEGER NOT NULL DEFAULT 0,
    local_digest     TEXT NOT NULL DEFAULT '',
    remote_digest    TEXT NOT NULL DEFAULT '',
    error            TEXT NOT NULL DEFAULT '',
    notified_digest  TEXT NOT NULL DEFAULT '',
    checked_at       DATETIME NOT NULL
);
