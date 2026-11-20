-- cluster_status
-- What the last check of a cluster's origin found, so it is still shown
-- after a server restart. nodes is the JSON of the last node list the
-- origin returned and known_at when; both stay NULL until one is known.
CREATE TABLE cluster_status (
    cluster_id TEXT PRIMARY KEY REFERENCES clusters(id) ON DELETE CASCADE,
    state      TEXT NOT NULL,
    checked_at DATETIME NOT NULL,
    nodes      TEXT,
    known_at   DATETIME
);
