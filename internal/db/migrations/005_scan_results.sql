-- Cache of the latest scan per image reference (§2.1 CVE scanning).
-- Keyed on image_ref so re-scanning an image upserts the row.
CREATE TABLE IF NOT EXISTS scan_results (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    image_ref       TEXT NOT NULL UNIQUE,
    scanner         TEXT NOT NULL,
    scanner_version TEXT,
    findings_json   TEXT NOT NULL,
    critical        INTEGER NOT NULL DEFAULT 0,
    high            INTEGER NOT NULL DEFAULT 0,
    medium          INTEGER NOT NULL DEFAULT 0,
    low             INTEGER NOT NULL DEFAULT 0,
    negligible      INTEGER NOT NULL DEFAULT 0,
    unknown_sev     INTEGER NOT NULL DEFAULT 0,
    scanned_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_scan_scanned_at ON scan_results(scanned_at);
